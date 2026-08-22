// cmd/cleanup runs the demo account TTL sweep once and exits: it marks
// sessions entering their day-6 window as warned and hard-deletes sessions
// past their day-7 TTL. It's meant to be invoked once per run by an external
// scheduler (cron, a Kubernetes CronJob, etc.) — wiring up that trigger is
// its own infrastructure concern and deliberately not done here.
package main

import (
	"context"
	"os"

	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	ctx := context.Background()
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "cleanup")

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

	sessions := sessionpg.New(pool)
	deleter := account.New(sessions, obj, docpg.New(txRunner))
	sweep := account.NewSweep(sessions, deleter)

	result, err := sweep.Run(ctx)
	if err != nil {
		logger.Error("sweep failed", "err", err)
		os.Exit(1)
	}

	logger.Info("sweep complete", "warned", result.Warned, "deleted", result.Deleted, "errors", len(result.Errors))
	for _, sweepErr := range result.Errors {
		logger.Error("sweep: per-session error", "err", sweepErr)
	}
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
}
