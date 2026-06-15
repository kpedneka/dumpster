package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/llm/openai"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

func main() {
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "worker")

	if _, _, err := telemetry.Setup(context.Background()); err != nil {
		logger.Error("telemetry setup failed", "err", err)
		os.Exit(1)
	}

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
	splitter := chunk.DefaultFixedWindow()
	embedder := openai.New(cfg.OpenAIAPIKey, cfg.OpenAIEmbedModel)

	h := worker.NewDocumentHandler(docs, obj, chunks, splitter, embedder)
	w := worker.New(q, h, worker.Config{})

	logger.Info("worker starting", "db_host", cfg.DBHost, "db_name", cfg.DBName)
	if err := w.Run(ctx); err != nil {
		logger.Info("worker stopped", "reason", err)
	}
}
