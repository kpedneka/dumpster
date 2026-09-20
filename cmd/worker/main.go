package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/awsbatch"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/crosslink"
	crosslinkpg "github.com/kunalpednekar/dumpster/internal/crosslink/pgstore"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	entityawsbatch "github.com/kunalpednekar/dumpster/internal/entity/awsbatch"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	graphedgepg "github.com/kunalpednekar/dumpster/internal/graphedge/pgstore"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/llm/anthropic"
	llmawsbatch "github.com/kunalpednekar/dumpster/internal/llm/awsbatch"
	"github.com/kunalpednekar/dumpster/internal/llm/bedrock"
	regionsawsbatch "github.com/kunalpednekar/dumpster/internal/manifest/awsbatch"
	manifestpg "github.com/kunalpednekar/dumpster/internal/manifest/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	"github.com/kunalpednekar/dumpster/internal/queue"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	"github.com/kunalpednekar/dumpster/internal/queuemetrics"
	metricscloudwatch "github.com/kunalpednekar/dumpster/internal/queuemetrics/cloudwatch"
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

	instruments, shutdownTelemetry, err := telemetry.Setup(context.Background(), "worker")
	if err != nil {
		logger.Error("telemetry setup failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdownTelemetry(context.Background()); err != nil {
			logger.Error("telemetry shutdown failed", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := connectDBWithRetry(ctx, logger, cfg)
	if err != nil {
		logger.Info("shutdown requested while waiting for database")
		return
	}
	defer pool.Close()

	txRunner := rls.New(pool)
	// usage_stats has no RLS policy, so it deliberately uses a plain
	// TxRunner rather than the RLS-aware one everything else here does.
	statsRepo := statspg.New(db.NewTxRunner(pool))

	obj, err := s3store.New(ctx, s3store.Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})
	if err != nil {
		logger.Error("object store setup failed", "err", err)
		os.Exit(1)
	}
	// Separate bucket for AWS Batch jobs' transient input/output handoff
	// (entity extraction, embedding) -- never real user documents. Kept
	// apart from obj above deliberately: scratch objects don't need (and
	// shouldn't get) real documents' durability/retention contract, and a
	// dedicated bucket makes its size alone a useful at-a-glance signal
	// for whether Batch scratch cleanup is actually working.
	scratchObj, err := s3store.New(ctx, s3store.Config{
		Endpoint:     cfg.S3ScratchEndpoint,
		Region:       cfg.S3ScratchRegion,
		Bucket:       cfg.S3ScratchBucket,
		AccessKey:    cfg.S3ScratchAccessKey,
		SecretKey:    cfg.S3ScratchSecretKey,
		UsePathStyle: cfg.S3ScratchUsePathStyle,
	})
	if err != nil {
		logger.Error("scratch object store setup failed", "err", err)
		os.Exit(1)
	}

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
	if cfg.S3ScratchBucket == "" {
		logger.Error("scratch object store setup failed", "err", "S3_SCRATCH_BUCKET is required")
		os.Exit(1)
	}
	if cfg.RegionsBatchJobQueue == "" || cfg.RegionsBatchJobDefinition == "" {
		logger.Error("region extractor setup failed", "err", "REGIONS_BATCH_JOB_QUEUE and REGIONS_BATCH_JOB_DEFINITION are required")
		os.Exit(1)
	}
	awsBatchClient, err := awsbatch.NewClient(ctx, cfg.AWSRegion)
	if err != nil {
		logger.Error("aws batch client setup failed", "err", err)
		os.Exit(1)
	}
	cloudwatchClient, err := metricscloudwatch.NewClient(ctx, cfg.AWSRegion)
	if err != nil {
		logger.Error("cloudwatch client setup failed", "err", err)
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
	// Region extraction runs on AWS Batch, the same idle-capacity-cost
	// motivation as embedding's own move (see internal/llm/awsbatch's
	// comment above) -- no HTTP fallback to the old always-on inference
	// service, matching how embedding and entity extraction already work.
	// This was briefly an opt-in flag while the AWS Batch resources it
	// needs hadn't been provisioned yet; now that Fly deploys have
	// stopped entirely, keeping a fallback this process will never be
	// deployed anywhere that needs would just be dead code. See
	// internal/manifest/awsbatch's package doc.
	layoutExtractor := regionsawsbatch.New(awsBatchClient, scratchObj, regionsawsbatch.Config{
		JobQueue:      cfg.RegionsBatchJobQueue,
		JobDefinition: cfg.RegionsBatchJobDefinition,
		PollInterval:  cfg.BatchPollInterval,
		PresignTTL:    cfg.BatchPresignTTL,
	})

	docHandler := worker.NewDocumentHandler(docs, obj, chunks, splitter, embedder).
		WithEntityExtractionPublisher(q).
		WithStats(statsRepo)
	regionHandler := worker.NewRegionClassificationHandler(
		docs, obj, chunks, manifestRepo, layoutExtractor, embedder, q,
	).WithStats(statsRepo).WithPhaseTracking(q)
	entityHandler := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, cfg.EntityTypes).
		WithDownstreamPublisher(q).
		WithBatchSize(cfg.EntityExtractionBatchSize).
		WithHeartbeat(q)
	edgeHandler := worker.NewEdgeHandler(docs, entities, edges)

	// Alias merging and cross-chunk linking now run automatically after
	// every document, instead of requiring cmd/aliasresolve/cmd/crosslink
	// to be run by hand -- both validated (graphrecall 0.75 -> 0.875)
	// before this wiring landed. Both still call Anthropic directly here,
	// same as the cmd/ tools always did; self-hosting either on GPU Batch
	// compute is separate, deferred work (see the confirm-step hosting
	// design and the "Fold cross-chunk linking + alias merging into
	// automatic ingestion" dev board card) -- this is a contained swap
	// behind the same llm.Generator interface whenever that happens, not
	// a reason to hold off on shipping the wiring now.
	var generator llm.Generator
	switch cfg.LLMProvider {
	case "bedrock":
		var err error
		generator, err = bedrock.New(context.Background(), cfg.AWSRegion, cfg.BedrockModelID)
		if err != nil {
			logger.Error("bedrock generator setup failed", "err", err)
			os.Exit(1)
		}
	default:
		generator = anthropic.New(cfg.AnthropicAPIKey, cfg.AnthropicModel)
	}
	aliasJudge := &canonical.LLMAliasJudge{Generator: generator}
	crosslinkRepo := crosslinkpg.New(txRunner)
	crosslinkExtractor := crosslink.NewExtractor(generator)
	canonicalizationHandler := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo).
		WithAliasJudge(aliasJudge).
		WithCrossLink(crosslinkRepo, crosslinkExtractor, 0)

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

	// Queue-depth metrics publish on their own, much shorter cadence (see
	// QueueMetricsPublishInterval's doc) -- the signal
	// terraform/ecs_worker_autoscaling.tf's step-scaling policy watches.
	// Every worker replica runs this independently and publishes the same
	// table-wide total; CloudWatch tolerates the redundant data points
	// trivially (well within the free tier), and it means the scaling
	// signal doesn't depend on any single replica staying alive to publish
	// it.
	go runQueueMetricsLoop(ctx, cfg.QueueMetricsPublishInterval, queuemetrics.New(q, cloudwatchClient), logger)

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

// connectDBMaxBackoff caps connectDBWithRetry's wait between attempts.
const connectDBMaxBackoff = 30 * time.Second

// connectDBWithRetry retries db.ConnectDirect with capped exponential
// backoff until it succeeds or ctx is cancelled (a shutdown signal).
//
// Real incident: the worker used to os.Exit(1) on the very first failed
// connection attempt, which was fine for a genuinely broken DSN but meant
// that when the database was merely temporarily unreachable (down, or --
// what actually happened in production -- a managed Postgres provider
// auto-paused the project after its usage quota was hit), ECS just
// crash-looped the whole task on every restart instead of the process
// quietly waiting for the database to come back. Same "keep trying, less
// and less often" philosophy as runLoop's own dequeue backoff
// (internal/worker/worker.go) -- deliberately not specific to any one
// failure cause, since a real outage and a paused/rate-limited database
// both just show up as a connection error here too.
func connectDBWithRetry(ctx context.Context, logger *slog.Logger, cfg *config.Config) (*pgxpool.Pool, error) {
	backoff := time.Second
	for {
		pool, err := db.ConnectDirect(ctx, cfg)
		if err == nil {
			return pool, nil
		}
		logger.Error("db connect failed, retrying", "err", err, "retry_in", backoff)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > connectDBMaxBackoff {
			backoff = connectDBMaxBackoff
		}
	}
}

// runQueueMetricsLoop publishes queue backlog depth immediately on startup
// and then on every tick of interval, so the worker's ECS step-scaling
// policy has a fresh data point from the moment this process starts rather
// than waiting a full interval. Exits when ctx is cancelled.
func runQueueMetricsLoop(ctx context.Context, interval time.Duration, publisher *queuemetrics.Publisher, logger *slog.Logger) {
	run := func() {
		if err := publisher.Publish(ctx); err != nil {
			logger.Error("queue metrics publish failed", "err", err)
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
