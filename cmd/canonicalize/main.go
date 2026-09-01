// cmd/canonicalize runs the canonical-entities backfill once and exits: it
// resolves canonical entities for every entity mention left unlinked by the
// canonicalization migration (migrations/022_canonical_entities.sql),
// across every tenant. Meant to be run once, by hand, shortly after that
// migration and the accompanying code deploy land — new mentions going
// forward are canonicalized as part of the normal ingestion pipeline (see
// internal/worker.CanonicalizationHandler), so this only ever has work to
// do once.
package main

import (
	"context"
	"os"

	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	"github.com/kunalpednekar/dumpster/internal/canonicalize"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	ctx := context.Background()
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "canonicalize")

	pool, err := db.ConnectDirect(ctx, cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	txRunner := rls.New(pool)

	backfill := canonicalize.New(
		sessionpg.New(pool),
		kbpg.New(txRunner),
		docpg.New(txRunner),
		entitypg.New(txRunner),
		canonicalpg.New(txRunner),
	)

	result, err := backfill.Run(ctx)
	if err != nil {
		logger.Error("canonicalize failed", "err", err)
		os.Exit(1)
	}

	logger.Info("canonicalize complete", "resolved", result.Resolved, "errors", len(result.Errors))
	for _, resolveErr := range result.Errors {
		logger.Error("canonicalize: per-document error", "err", resolveErr)
	}
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
}
