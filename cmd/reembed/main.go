// cmd/reembed runs the local-embeddings backfill once and exits: it
// re-embeds every chunk left with a nil embedding by the embedding-model
// migration (migrations/019_local_embeddings.sql), across every tenant.
// Meant to be run once, by hand, shortly after that migration and the
// accompanying code deploy land — wiring up recurring/automatic
// invocation is not needed since this only ever has work to do once per
// embedding-model change.
//
// Embeds via AWS Batch (internal/llm/awsbatch), same as cmd/worker's
// ingestion-time embedding and for the same reason — this is a bulk,
// latency-tolerant background job, exactly the shape that suffered under
// Fly's shared-cpu throttling. See internal/llm/awsbatch's package doc
// for the full story.
package main

import (
	"context"
	"os"

	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	llmawsbatch "github.com/kunalpednekar/dumpster/internal/llm/awsbatch"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	"github.com/kunalpednekar/dumpster/internal/reembed"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"

	"github.com/kunalpednekar/dumpster/internal/awsbatch"
)

func main() {
	ctx := context.Background()
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "reembed")

	pool, err := db.ConnectDirect(ctx, cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	txRunner := rls.New(pool)

	if cfg.EmbedBatchJobQueue == "" || cfg.EmbedBatchJobDefinition == "" {
		logger.Error("embedder setup failed", "err", "EMBED_BATCH_JOB_QUEUE and EMBED_BATCH_JOB_DEFINITION are required")
		os.Exit(1)
	}
	awsBatchClient, err := awsbatch.NewClient(ctx, cfg.AWSRegion)
	if err != nil {
		logger.Error("aws batch client setup failed", "err", err)
		os.Exit(1)
	}
	// Only ever used for the embedder's Batch input/output handoff below --
	// cmd/reembed never reads or writes real documents directly, so this
	// points at the scratch bucket, not the main document one.
	scratchObj := s3store.New(s3store.Config{
		Endpoint:     cfg.R2ScratchEndpoint,
		Region:       cfg.R2ScratchRegion,
		Bucket:       cfg.R2ScratchBucket,
		AccessKey:    cfg.R2ScratchAccessKey,
		SecretKey:    cfg.R2ScratchSecretKey,
		UsePathStyle: cfg.R2ScratchUsePathStyle,
	})
	embedder := llmawsbatch.New(awsBatchClient, scratchObj, llmawsbatch.Config{
		JobQueue:      cfg.EmbedBatchJobQueue,
		JobDefinition: cfg.EmbedBatchJobDefinition,
		PollInterval:  cfg.BatchPollInterval,
		PresignTTL:    cfg.BatchPresignTTL,
	})

	backfill := reembed.New(
		sessionpg.New(pool),
		kbpg.New(txRunner),
		chunkpg.New(txRunner),
		embedder,
	)

	result, err := backfill.Run(ctx)
	if err != nil {
		logger.Error("reembed failed", "err", err)
		os.Exit(1)
	}

	logger.Info("reembed complete", "reembedded", result.Reembedded, "errors", len(result.Errors))
	for _, reembedErr := range result.Errors {
		logger.Error("reembed: per-chunk error", "err", reembedErr)
	}
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
}
