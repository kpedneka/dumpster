package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/awsbatch"
	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	entityawsbatch "github.com/kunalpednekar/dumpster/internal/entity/awsbatch"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	graphedgepg "github.com/kunalpednekar/dumpster/internal/graphedge/pgstore"
	llmawsbatch "github.com/kunalpednekar/dumpster/internal/llm/awsbatch"
	"github.com/kunalpednekar/dumpster/internal/manifest/layout"
	manifestpg "github.com/kunalpednekar/dumpster/internal/manifest/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	"github.com/kunalpednekar/dumpster/internal/queue"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	"github.com/kunalpednekar/dumpster/internal/stats"
	statspg "github.com/kunalpednekar/dumpster/internal/stats/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

func main() {
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "worker")

	instruments, metricsHandler, err := telemetry.Setup(context.Background())
	if err != nil {
		logger.Error("telemetry setup failed", "err", err)
		os.Exit(1)
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metricsHandler)
	go func() {
		addr := fmt.Sprintf(":%s", cfg.MetricsPort)
		logger.Info("metrics endpoint starting", "addr", addr+"/metrics")
		if err := http.ListenAndServe(addr, metricsMux); err != nil {
			logger.Error("metrics server error", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.ConnectDirect(ctx, cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	txRunner := rls.New(pool)
	// usage_stats has no RLS policy, so it deliberately uses a plain
	// TxRunner rather than the RLS-aware one everything else here does.
	statsRepo := statspg.New(db.NewTxRunner(pool))

	obj := s3store.New(s3store.Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})
	// Separate bucket for AWS Batch jobs' transient input/output handoff
	// (entity extraction, embedding) -- never real user documents. Kept
	// apart from obj above deliberately: scratch objects don't need (and
	// shouldn't get) real documents' durability/retention contract, and a
	// dedicated bucket makes its size alone a useful at-a-glance signal
	// for whether Batch scratch cleanup is actually working.
	scratchObj := s3store.New(s3store.Config{
		Endpoint:     cfg.R2ScratchEndpoint,
		Region:       cfg.R2ScratchRegion,
		Bucket:       cfg.R2ScratchBucket,
		AccessKey:    cfg.R2ScratchAccessKey,
		SecretKey:    cfg.R2ScratchSecretKey,
		UsePathStyle: cfg.R2ScratchUsePathStyle,
	})

	q := qpg.New(pool)
	docs := docpg.New(txRunner)
	chunks := chunkpg.New(txRunner)
	entities := entitypg.New(txRunner)
	edges := graphedgepg.New(txRunner)
	canonicalRepo := canonicalpg.New(txRunner)
	splitter := chunk.DefaultFixedWindow()
	if cfg.BatchJobQueue == "" || cfg.BatchJobDefinition == "" {
		logger.Error("entity extractor setup failed", "err", "BATCH_JOB_QUEUE and BATCH_JOB_DEFINITION are required")
		os.Exit(1)
	}
	if cfg.EmbedBatchJobQueue == "" || cfg.EmbedBatchJobDefinition == "" {
		logger.Error("embedder setup failed", "err", "EMBED_BATCH_JOB_QUEUE and EMBED_BATCH_JOB_DEFINITION are required")
		os.Exit(1)
	}
	awsBatchClient, err := awsbatch.NewClient(ctx, cfg.AWSRegion)
	if err != nil {
		logger.Error("aws batch client setup failed", "err", err)
		os.Exit(1)
	}
	// Ingestion-time embedding runs on AWS Batch (a separate, CPU-only
	// Fargate compute environment from entity extraction's GPU one) —
	// moved off the HTTP inference service after measuring Fly's
	// shared-cpu tier throttling a 47.5s embedding call into 13-16+
	// minutes in production. Query-time embedding (cmd/api) is
	// unaffected — it still uses llminference.NewQueryEmbedder, since a
	// per-request Batch cold start would be unacceptable for a live
	// search. See internal/llm/awsbatch's package doc for the full story.
	embedder := llmawsbatch.New(awsBatchClient, scratchObj, llmawsbatch.Config{
		JobQueue:      cfg.EmbedBatchJobQueue,
		JobDefinition: cfg.EmbedBatchJobDefinition,
		PollInterval:  cfg.BatchPollInterval,
		PresignTTL:    cfg.BatchPresignTTL,
	})
	extractor := entityawsbatch.New(awsBatchClient, scratchObj, entityawsbatch.Config{
		JobQueue:      cfg.BatchJobQueue,
		JobDefinition: cfg.BatchJobDefinition,
		PollInterval:  cfg.BatchPollInterval,
		PresignTTL:    cfg.BatchPresignTTL,
	})

	manifestRepo := manifestpg.New(txRunner)
	layoutExtractor := layout.New(layout.Config{BaseURL: cfg.InferenceServiceURL})

	docHandler := worker.NewDocumentHandler(docs, obj, chunks, splitter, embedder).
		WithEntityExtractionPublisher(q).
		WithStats(statsRepo)
	regionHandler := worker.NewRegionClassificationHandler(
		docs, obj, chunks, manifestRepo, layoutExtractor, embedder, q,
	).WithStats(statsRepo)
	entityHandler := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, cfg.EntityTypes).
		WithDownstreamPublisher(q).
		WithBatchSize(cfg.EntityExtractionBatchSize).
		WithHeartbeat(q)
	edgeHandler := worker.NewEdgeHandler(docs, entities, edges)
	canonicalizationHandler := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo)

	w := worker.New(q, docHandler, worker.Config{Instruments: instruments, Concurrency: cfg.WorkerConcurrency})
	w.RegisterHandler(queue.JobTypeRegionClassification, regionHandler)
	w.RegisterHandler(queue.JobTypeEntityExtraction, entityHandler)
	w.RegisterHandler(queue.JobTypeEdgeExtraction, edgeHandler)
	w.RegisterHandler(queue.JobTypeCanonicalization, canonicalizationHandler)

	// Session sweep runs on a ticker inside this always-on process so no
	// external scheduler (cron, Fly Machines cron job, etc.) is required.
	// Cadence is controlled by SWEEP_INTERVAL (default 5m).
	go runSweepLoop(ctx, cfg.SweepInterval, pool, obj, txRunner, statsRepo, logger)

	// Job-reclaim runs on the same cadence as the session sweep — both are
	// periodic maintenance with no need for independent tuning at this
	// scale. Staleness threshold is JOB_STALE_TIMEOUT (default 15m); see
	// queue/pgstore.Store.ReclaimStale for what this recovers from.
	go runJobReclaimLoop(ctx, cfg.SweepInterval, cfg.JobStaleTimeout, q, logger)

	logger.Info("worker starting",
		"db_host", cfg.DBHost, "db_name", cfg.DBName,
		"entity_types", cfg.EntityTypes,
		"sweep_interval", cfg.SweepInterval,
	)
	if err := w.Run(ctx); err != nil {
		logger.Info("worker stopped", "reason", err)
	}
}

// runSweepLoop runs the session sweep immediately on startup and then on every
// tick of interval. It exits when ctx is cancelled (worker shutdown).
func runSweepLoop(ctx context.Context, interval time.Duration, pool *pgxpool.Pool, obj *s3store.Store, txRunner *rls.TxRunner, statsRepo stats.Repository, logger *slog.Logger) {
	sessions := sessionpg.New(pool)
	deleter := account.New(sessions, obj, docpg.New(txRunner))
	sweep := account.NewSweep(sessions, deleter)

	run := func() {
		result, err := sweep.Run(ctx)
		if err != nil {
			logger.Error("sweep failed", "err", err)
			return
		}
		logger.Info("sweep complete",
			"warned", result.Warned,
			"deleted", result.Deleted,
			"errors", len(result.Errors),
		)
		for _, sweepErr := range result.Errors {
			logger.Error("sweep: per-session error", "err", sweepErr)
		}
		if result.Deleted > 0 {
			if err := statsRepo.RecordSessionsSwept(ctx, result.Deleted); err != nil {
				logger.Error("sweep: record sessions swept", "err", err)
			}
		}
	}

	run() // run once immediately so a restarted worker doesn't wait a full interval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			run()
		case <-ctx.Done():
			return
		}
	}
}

// runJobReclaimLoop resets jobs orphaned by a dead or restarted worker
// (stuck "processing" past staleAfter) back to "pending" immediately on
// startup and then on every tick of interval, so this worker itself
// recovers anything left behind by its own previous instance. Exits when
// ctx is cancelled.
func runJobReclaimLoop(ctx context.Context, interval, staleAfter time.Duration, q *qpg.Store, logger *slog.Logger) {
	run := func() {
		reclaimed, deadLettered, err := q.ReclaimStale(ctx, staleAfter)
		if err != nil {
			logger.Error("job reclaim failed", "err", err)
			return
		}
		if reclaimed > 0 || deadLettered > 0 {
			logger.Info("job reclaim complete", "reclaimed", reclaimed, "dead_lettered", deadLettered)
		}
	}

	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			run()
		case <-ctx.Done():
			return
		}
	}
}
