package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	graphragpg "github.com/kunalpednekar/dumpster/internal/graphrag/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/llm/anthropic"
	llminference "github.com/kunalpednekar/dumpster/internal/llm/inference"
	"github.com/kunalpednekar/dumpster/internal/llm/instrumented"
	manifestpg "github.com/kunalpednekar/dumpster/internal/manifest/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	retrievalpg "github.com/kunalpednekar/dumpster/internal/retrieval/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	queryrouter "github.com/kunalpednekar/dumpster/internal/router"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/server"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "api")

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

	pool, err := db.Connect(context.Background(), cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	txRunner := rls.New(pool)

	logger.Info("object storage config",
		"endpoint", cfg.S3Endpoint,
		"bucket", cfg.S3Bucket,
		"region", cfg.S3Region,
		"path_style", cfg.S3UsePathStyle,
		"key_set", cfg.S3AccessKey != "",
	)
	obj := s3store.New(s3store.Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})

	embedder := instrumented.NewEmbedder(llminference.NewQueryEmbedder(cfg.InferenceServiceURL), instruments)
	generator := anthropic.New(cfg.AnthropicAPIKey, cfg.AnthropicModel)
	retriever := retrievalpg.New(txRunner, embedder)
	answerer := search.NewAnswerer(generator)
	queryRouter := queryrouter.NewLLMRouter(generator)
	graphRetriever := graphragpg.New(txRunner)
	searcher := search.New(retriever, answerer,
		search.WithRouter(queryRouter),
		search.WithGraphRetriever(graphRetriever),
	)

	deps := server.Deps{
		KBs:          kbpg.New(txRunner),
		Docs:         docpg.New(txRunner),
		Objects:      obj,
		Publisher:    qpg.New(pool),
		Manifest:     manifestpg.New(txRunner),
		Searcher:     searcher,
		Sessions:     sessionpg.New(pool),
		Instruments:  instruments,
		SPADir:       "web/dist",
		CookieSecure: cfg.CookieSecure,
	}

	router := server.NewRouter(deps)

	addr := fmt.Sprintf(":%s", cfg.HTTPPort)
	logger.Info("api starting", "addr", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}
