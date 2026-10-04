// Command lambda-pipeline is the one Lambda function behind every pipeline
// state machine: document indexing (internal/pipeline/docindex), entity
// extraction (internal/pipeline/entityextract) and region classification
// (internal/pipeline/regionclassify). Each state machine's Lambda tasks send
// an event carrying its job type at run.type, and internal/pipeline/router
// hands it to that package's handler.
//
// The steps publish follow-on jobs (entity extraction after indexing; edge
// extraction and canonicalization after entity extraction) through
// internal/queue/dispatch, the same publisher cmd/api uses after cutover.
// Deployed by terraform/modules/pipeline.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	statuspg "github.com/kunalpednekar/dumpster/internal/jobstatus/pgstore"
	manifestpg "github.com/kunalpednekar/dumpster/internal/manifest/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	"github.com/kunalpednekar/dumpster/internal/pipeline/docindex"
	"github.com/kunalpednekar/dumpster/internal/pipeline/entityextract"
	"github.com/kunalpednekar/dumpster/internal/pipeline/regionclassify"
	"github.com/kunalpednekar/dumpster/internal/pipeline/router"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queue/dispatch"
	"github.com/kunalpednekar/dumpster/internal/queue/sqs"
	"github.com/kunalpednekar/dumpster/internal/queue/stepfunctions"
	"github.com/kunalpednekar/dumpster/internal/rls"
	statspg "github.com/kunalpednekar/dumpster/internal/stats/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "lambda-pipeline")
	slog.SetDefault(logger)
	ctx := context.Background()

	fail := func(msg string, err error) {
		logger.Error(msg, "err", err)
		os.Exit(1)
	}

	// Pooled DSN, like cmd/lambda-stateless-jobs: short-lived, concurrent
	// invocations are the PgBouncer transaction-mode case.
	pool, err := db.Connect(ctx, cfg)
	if err != nil {
		fail("db connect failed", err)
	}
	defer pool.Close()
	txRunner := rls.New(pool)

	uploads, err := s3store.New(ctx, s3store.Config{
		Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, UsePathStyle: cfg.S3UsePathStyle,
	})
	if err != nil {
		fail("uploads object store setup failed", err)
	}
	scratch, err := s3store.New(ctx, s3store.Config{
		Endpoint: cfg.S3ScratchEndpoint, Region: cfg.S3ScratchRegion, Bucket: cfg.S3ScratchBucket,
		AccessKey: cfg.S3ScratchAccessKey, SecretKey: cfg.S3ScratchSecretKey, UsePathStyle: cfg.S3ScratchUsePathStyle,
	})
	if err != nil {
		fail("scratch object store setup failed", err)
	}

	sqsClient, err := sqs.NewClient(ctx, cfg.AWSRegion)
	if err != nil {
		fail("sqs client setup failed", err)
	}
	sfnClient, err := stepfunctions.NewClient(ctx, cfg.AWSRegion)
	if err != nil {
		fail("step functions client setup failed", err)
	}

	docs := docpg.New(txRunner)
	chunks := chunkpg.New(txRunner)
	status := statuspg.New(txRunner)
	// usage_stats has no RLS policy, so it uses a plain TxRunner, as in
	// cmd/worker.
	stats := statspg.New(db.NewTxRunner(pool))
	publisher := dispatch.New(status,
		sqs.New(sqsClient, sqs.Config{
			EdgeExtractionQueueURL:   cfg.EdgeExtractionQueueURL,
			CanonicalizationQueueURL: cfg.CanonicalizationQueueURL,
		}),
		stepfunctions.New(sfnClient, stepfunctions.Config{
			DocumentIndexingStateMachineARN:     cfg.DocumentIndexingStateMachineARN,
			EntityExtractionStateMachineARN:     cfg.EntityExtractionStateMachineARN,
			RegionClassificationStateMachineARN: cfg.RegionClassificationStateMachineARN,
		}),
	)
	splitter := chunk.DefaultFixedWindow()

	docIndex := docindex.New(docindex.Deps{
		Docs: docs, Uploads: uploads, Scratch: scratch, Chunks: chunks, Splitter: splitter,
		Publisher: publisher, Status: status, Stats: stats,
	}, docindex.Config{})
	entityExtract := entityextract.New(entityextract.Deps{
		Docs: docs, Chunks: chunks, Entities: entitypg.New(txRunner), Canonical: canonicalpg.New(txRunner),
		Scratch: scratch, Publisher: publisher, Status: status,
	}, entityextract.Config{AllowedTypes: cfg.EntityTypes, BatchSize: cfg.EntityExtractionBatchSize})
	regionClassify := regionclassify.New(regionclassify.Deps{
		Docs: docs, Uploads: uploads, Scratch: scratch, Chunks: chunks, Manifest: manifestpg.New(txRunner),
		Splitter: splitter, Publisher: publisher, Status: status, Stats: stats,
	}, regionclassify.Config{})

	r := router.New().
		Register(queue.JobTypeDocumentIndexing, router.Typed(docIndex.Handle)).
		Register(queue.JobTypeEntityExtraction, router.Typed(entityExtract.Handle)).
		Register(queue.JobTypeRegionClassification, router.Typed(regionClassify.Handle))

	lambda.Start(r.Route)
}
