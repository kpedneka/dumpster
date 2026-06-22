// cmd/cleanup runs the demo account TTL sweep once and exits: it warns
// accounts entering their day-6 window and hard-deletes accounts past their
// day-7 TTL. It's meant to be invoked once per run by an external scheduler
// (cron, a Kubernetes CronJob, etc.) — wiring up that trigger is its own
// infrastructure concern and deliberately not done here.
package main

import (
	"context"
	"os"

	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/auth/clerk"
	authpg "github.com/kunalpednekar/dumpster/internal/auth/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/email/resend"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	"github.com/kunalpednekar/dumpster/internal/rls"
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

	users := authpg.New(pool)
	deleter := account.New(
		clerk.NewDeleter(cfg.ClerkSecretKey),
		obj,
		docpg.New(txRunner),
		users,
	)
	emails := resend.New(cfg.ResendAPIKey, cfg.ResendFromAddr)
	sweep := account.NewSweep(users, deleter, emails)

	result, err := sweep.Run(ctx)
	if err != nil {
		logger.Error("sweep failed", "err", err)
		os.Exit(1)
	}

	logger.Info("sweep complete", "warned", result.Warned, "deleted", result.Deleted, "errors", len(result.Errors))
	for _, sweepErr := range result.Errors {
		logger.Error("sweep: per-account error", "err", sweepErr)
	}
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
}
