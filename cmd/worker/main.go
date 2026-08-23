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
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/entity/gliner"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	graphedgepg "github.com/kunalpednekar/dumpster/internal/graphedge/pgstore"
	"github.com/kunalpednekar/dumpster/internal/llm/openai"
	"github.com/kunalpednekar/dumpster/internal/manifest/layout"
	manifestpg "github.com/kunalpednekar/dumpster/internal/manifest/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	"github.com/kunalpednekar/dumpster/internal/queue"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
	ollamavision "github.com/kunalpednekar/dumpster/internal/vision/ollama"
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

	obj := s3store.New(s3store.Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})

	q := qpg.New(pool)
	docs := docpg.New(txRunner)
	chunks := chunkpg.New(txRunner)
	entities := entitypg.New(txRunner)
	edges := graphedgepg.New(txRunner)
	splitter := chunk.DefaultFixedWindow()
	embedder := openai.New(cfg.OpenAIAPIKey, cfg.OpenAIEmbedModel)
	extractor := gliner.New(gliner.Config{
		PythonPath: cfg.EntityExtractorPython,
		ScriptPath: cfg.EntityExtractorScript,
	})

	manifestRepo := manifestpg.New(txRunner)
	layoutExtractor := layout.New(layout.Config{
		PythonPath: cfg.EntityExtractorPython, // reuse the same Python interpreter
		ScriptPath: cfg.RegionExtractorScript,
	})
	describer := ollamavision.New(cfg.OllamaURL, cfg.OllamaVLMModel)

	docHandler := worker.NewDocumentHandler(docs, obj, chunks, splitter, embedder).
		WithEntityExtractionPublisher(q)
	regionHandler := worker.NewRegionClassificationHandler(
		docs, obj, chunks, manifestRepo, layoutExtractor, describer, embedder, q,
	)
	entityHandler := worker.NewEntityHandler(docs, chunks, entities, extractor, cfg.EntityTypes).
		WithEdgeExtractionPublisher(q)
	edgeHandler := worker.NewEdgeHandler(docs, entities, edges)

	w := worker.New(q, docHandler, worker.Config{Instruments: instruments})
	w.RegisterHandler(queue.JobTypeRegionClassification, regionHandler)
	w.RegisterHandler(queue.JobTypeEntityExtraction, entityHandler)
	w.RegisterHandler(queue.JobTypeEdgeExtraction, edgeHandler)

	// Session sweep runs on a ticker inside this always-on process so no
	// external scheduler (cron, Fly Machines cron job, etc.) is required.
	// Cadence is controlled by SWEEP_INTERVAL (default 5m).
	go runSweepLoop(ctx, cfg.SweepInterval, pool, obj, txRunner, logger)

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
func runSweepLoop(ctx context.Context, interval time.Duration, pool *pgxpool.Pool, obj *s3store.Store, txRunner *rls.TxRunner, logger *slog.Logger) {
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
