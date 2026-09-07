// cmd/evalscore runs the graph-recall metric (internal/graphrecall) against
// a benchmark KB previously ingested by cmd/evalbaseline, once its
// documents have actually finished processing through a real worker.
//
// It resolves validation/ground_truth.json's cross-document pairs against
// whatever documents the KB actually has today (by exact filename match --
// see internal/evalcorpus.ResolvePairs), then exercises the real
// graphrag.GraphRetriever (AggregationLeg/TraversalLeg) the same way
// production Q&A retrieval does. It does not run the intrusion test --
// that already has its own endpoint (POST /kbs/{id}/intrusion-test> once
// community detection (POST /kbs/{id}/communities) has been triggered for
// this KB, and duplicating that wiring here would just be two ways to do
// the same thing.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/evalcorpus"
	graphragpg "github.com/kunalpednekar/dumpster/internal/graphrag/pgstore"
	"github.com/kunalpednekar/dumpster/internal/graphrecall"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	sessionIDFlag := flag.String("session-id", "", "the session ID printed by cmd/evalbaseline -- there are no separate accounts in this app, every KB's tenant is a session ID (required)")
	kbIDFlag := flag.String("kb-id", "", "the benchmark KB's UUID, as printed by cmd/evalbaseline (required)")
	corpusDir := flag.String("corpus-dir", "validation", "path to the validation corpus directory")
	k := flag.Int("k", 10, "how many chunks to request per graph leg -- should match production search's k")
	force := flag.Bool("force", false, "score anyway even if some documents aren't fully indexed yet (e.g. still processing, or permanently failed) -- the resulting score is not trustworthy as a baseline")
	flag.Parse()

	logger := telemetry.NewLogger(os.Stdout, "evalscore")

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

	gt, err := loadGroundTruth(*corpusDir)
	if err != nil {
		logger.Error("loading ground truth failed", "err", err)
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
	docs := docpg.New(txRunner)

	docList, err := docs.ListByKB(ctx, userID, kbID)
	if err != nil {
		logger.Error("listing kb documents failed", "err", err)
		os.Exit(1)
	}
	filenameToDocID := make(map[string]uuid.UUID, len(docList))
	for _, d := range docList {
		filenameToDocID[d.Filename] = d.ID
	}
	logger.Info("found kb documents", "count", len(docList))

	if err := evalcorpus.CheckAllIndexed(docList); err != nil {
		if *force {
			logger.Error("scoring anyway despite incomplete documents (-force set) -- this score is not a trustworthy baseline", "err", err)
		} else {
			logger.Error("refusing to score against an incompletely-processed KB -- pass -force to override", "err", err)
			os.Exit(1)
		}
	}

	pairs, err := evalcorpus.ResolvePairs(gt, filenameToDocID)
	if err != nil {
		logger.Error("resolving ground truth against ingested documents failed -- has ingestion finished processing?", "err", err)
		os.Exit(1)
	}

	tester := &graphrecall.Tester{Retriever: graphragpg.New(txRunner), K: *k}
	result, err := tester.Run(ctx, kbID, pairs)
	if err != nil {
		logger.Error("graphrecall run failed", "err", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("marshal result failed", "err", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
	logger.Info("graphrecall score", "score", result.Score, "pairs_tested", len(result.Results))
}

func loadGroundTruth(corpusDir string) (evalcorpus.GroundTruth, error) {
	gtPath := filepath.Join(corpusDir, "ground_truth.json")
	raw, err := os.ReadFile(gtPath)
	if err != nil {
		return evalcorpus.GroundTruth{}, fmt.Errorf("reading %s: %w", gtPath, err)
	}
	var gt evalcorpus.GroundTruth
	if err := json.Unmarshal(raw, &gt); err != nil {
		return evalcorpus.GroundTruth{}, fmt.Errorf("parsing %s: %w", gtPath, err)
	}
	return gt, nil
}
