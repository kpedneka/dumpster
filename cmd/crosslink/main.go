// cmd/crosslink runs the cross-chunk linking pass (internal/crosslink)
// against a benchmark KB: it finds canonical-entity pairs connected only
// by a chain longer than graphrag.TraversalLeg's hardcoded 2-hop
// expansion can reach, asks an LLM whether the two source chunks together
// actually support a specific relationship, and persists confirmed
// relationships as cross_chunk_edges rows -- which TraversalLeg then
// consults directly on future queries.
//
// Requires migrations/029_cross_chunk_edges.sql to have been applied.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/crosslink"
	crosslinkpg "github.com/kunalpednekar/dumpster/internal/crosslink/pgstore"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/llm/anthropic"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	sessionIDFlag := flag.String("session-id", "", "the session ID printed by cmd/evalbaseline (required)")
	kbIDFlag := flag.String("kb-id", "", "the benchmark KB's UUID, as printed by cmd/evalbaseline (required)")
	limit := flag.Int("limit", 20, "max candidate chains to review in this run -- bounds the LLM call cost")
	review := flag.Bool("review", false, "instead of running new extraction, list every previously-confirmed relationship with human-readable entity text, for spot-checking precision")
	purge := flag.Bool("purge", false, "instead of running new extraction, delete every persisted relation that wouldn't qualify as a candidate under the current quality guard (e.g. a non-capitalized generic-word entity) -- cleans up relations confirmed before the guard existed")
	flag.Parse()

	logger := telemetry.NewLogger(os.Stdout, "crosslink")

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
	repo := crosslinkpg.New(txRunner)

	if *purge {
		deleted, err := repo.PurgeLowQuality(ctx, userID, kbID)
		if err != nil {
			logger.Error("purge failed", "err", err)
			os.Exit(1)
		}
		logger.Info("purge complete", "deleted", deleted)
		return
	}

	if *review {
		confirmed, err := repo.ListConfirmed(ctx, userID, kbID)
		if err != nil {
			logger.Error("listing confirmed relations failed", "err", err)
			os.Exit(1)
		}
		out, err := json.MarshalIndent(confirmed, "", "  ")
		if err != nil {
			logger.Error("marshal result failed", "err", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
		logger.Info("review complete", "confirmed_total", len(confirmed))
		return
	}

	generator := anthropic.New(cfg.AnthropicAPIKey, cfg.AnthropicModel)
	extractor := crosslink.NewExtractor(generator)

	candidates, err := repo.CandidateChains(ctx, userID, kbID, *limit)
	if err != nil {
		logger.Error("finding candidate chains failed", "err", err)
		os.Exit(1)
	}
	logger.Info("found candidate chains", "count", len(candidates))
	if len(candidates) == 0 {
		fmt.Println("{}")
		return
	}

	updates, err := extractor.Extract(ctx, candidates)
	if err != nil {
		logger.Error("extraction failed", "err", err)
		os.Exit(1)
	}

	if err := repo.ApplyUpdates(ctx, userID, kbID, updates); err != nil {
		logger.Error("applying updates failed", "err", err)
		os.Exit(1)
	}

	var confirmed, none int
	for _, u := range updates {
		if u.RelationType == crosslink.NoneRelation {
			none++
		} else {
			confirmed++
		}
	}

	out, err := json.MarshalIndent(map[string]any{
		"candidates_reviewed": len(candidates),
		"confirmed":           confirmed,
		"none":                none,
		"unanswered":          len(candidates) - len(updates),
		"updates":             updates,
	}, "", "  ")
	if err != nil {
		logger.Error("marshal result failed", "err", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
	logger.Info("crosslink run complete", "candidates", len(candidates), "confirmed", confirmed, "none", none)
}
