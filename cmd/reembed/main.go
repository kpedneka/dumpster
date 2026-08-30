// cmd/reembed runs the local-embeddings backfill once and exits: it
// re-embeds every chunk left with a nil embedding by the embedding-model
// migration (migrations/019_local_embeddings.sql) using the local
// inference service, across every tenant. Meant to be run once, by hand,
// shortly after that migration and the accompanying code deploy land —
// wiring up recurring/automatic invocation is not needed since this only
// ever has work to do once per embedding-model change.
package main

import (
	"context"
	"os"

	chunkpg "github.com/kunalpednekar/dumpster/internal/chunk/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	llminference "github.com/kunalpednekar/dumpster/internal/llm/inference"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/reembed"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	ctx := context.Background()
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "reembed")

	pool, err := db.ConnectDirect(ctx, cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	txRunner := rls.New(pool)

	backfill := reembed.New(
		sessionpg.New(pool),
		kbpg.New(txRunner),
		chunkpg.New(txRunner),
		llminference.NewDocumentEmbedder(cfg.InferenceServiceURL),
	)

	result, err := backfill.Run(ctx)
	if err != nil {
		logger.Error("reembed failed", "err", err)
		os.Exit(1)
	}

	logger.Info("reembed complete", "reembedded", result.Reembedded, "errors", len(result.Errors))
	for _, reembedErr := range result.Errors {
		logger.Error("reembed: per-chunk error", "err", reembedErr)
	}
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
}
