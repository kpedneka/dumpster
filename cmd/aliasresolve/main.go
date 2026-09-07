// cmd/aliasresolve runs canonical.ResolveAllAliases against a KB: it
// checks every existing canonical entity for a fuzzy (token-subset) alias
// -- a surname where a full name exists elsewhere, an abbreviation already
// spelled out -- confirms each via an LLM, and merges confirmed pairs.
//
// Not wired into the automatic per-document worker pipeline, matching how
// internal/relation and internal/crosslink are also separately-triggered,
// LLM-cost-bounded passes rather than silently added to every future
// ingestion's cost. Safe to run repeatedly on the same KB: an
// already-merged pair simply won't reappear as a candidate (its "losing"
// canonical row no longer exists to be found), and this also serves as the
// backfill path for a KB canonicalized before this mechanism existed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/llm/anthropic"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	sessionIDFlag := flag.String("session-id", "", "the session ID printed by cmd/evalbaseline (required)")
	kbIDFlag := flag.String("kb-id", "", "the benchmark KB's UUID, as printed by cmd/evalbaseline (required)")
	flag.Parse()

	logger := telemetry.NewLogger(os.Stdout, "aliasresolve")

	if *sessionIDFlag == "" || *kbIDFlag == "" {
		logger.Error("missing required flags", "need", "-session-id and -kb-id")
		os.Exit(1)
	}
	userID, err := uuid.Parse(*sessionIDFlag)
	if err != nil {
		logger.Error("invalid -session-id", "err", err)
		os.Exit(1)
	}
	kbID, err := uuid.Parse(*kbIDFlag)
	if err != nil {
		logger.Error("invalid -kb-id", "err", err)
		os.Exit(1)
	}

	ctx := auth.WithUserID(context.Background(), userID)
	cfg := config.Load()

	pool, err := db.ConnectDirect(ctx, cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	txRunner := rls.New(pool)
	repo := canonicalpg.New(txRunner)
	generator := anthropic.New(cfg.AnthropicAPIKey, cfg.AnthropicModel)
	judge := &canonical.LLMAliasJudge{Generator: generator}

	merged, err := canonical.ResolveAllAliases(ctx, repo, userID, kbID, judge)
	if err != nil {
		logger.Error("resolve all aliases failed", "err", err)
		os.Exit(1)
	}

	fmt.Printf("{\"merged\": %d}\n", merged)
	logger.Info("alias resolution complete", "merged", merged)
}
