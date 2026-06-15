package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/server"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "api")

	_, metricsHandler, err := telemetry.Setup(context.Background())
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

	obj := s3store.New(s3store.Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})

	deps := server.Deps{
		KBs:       kbpg.New(txRunner),
		Docs:      docpg.New(txRunner),
		Objects:   obj,
		Publisher: qpg.New(pool),
		JWTSecret: cfg.JWTSecret,
	}

	router := server.NewRouter(deps)

	addr := fmt.Sprintf(":%s", cfg.HTTPPort)
	logger.Info("api starting", "addr", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}
