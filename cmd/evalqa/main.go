// cmd/evalqa runs the multi-hop QA eval (internal/multihopqa) against a
// benchmark KB previously ingested by cmd/evalbaseline, once its documents
// have actually finished processing through a real worker.
//
// Unlike cmd/evalscore, this exercises the full production query pipeline
// -- the query router, hybrid retrieval, graph legs, RRF merge, and the
// answerer -- not just the graph legs directly. It needs the same
// dependencies cmd/api wires up: an embedder reaching the inference
// service and a generator reaching Anthropic's API, both configured the
// same way (INFERENCE_SERVICE_URL, ANTHROPIC_API_KEY). If you're running
// this from the host rather than inside docker-compose's network,
// INFERENCE_SERVICE_URL's default ("http://inference:8000", a
// docker-internal hostname) won't resolve -- override it to
// http://localhost:8000.
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
	"github.com/kunalpednekar/dumpster/internal/llm/anthropic"
	llminference "github.com/kunalpednekar/dumpster/internal/llm/inference"
	"github.com/kunalpednekar/dumpster/internal/multihopqa"
	retrievalpg "github.com/kunalpednekar/dumpster/internal/retrieval/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	queryrouter "github.com/kunalpednekar/dumpster/internal/router"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	sessionIDFlag := flag.String("session-id", "", "the session ID printed by cmd/evalbaseline (required)")
	kbIDFlag := flag.String("kb-id", "", "the benchmark KB's UUID, as printed by cmd/evalbaseline (required)")
	corpusDir := flag.String("corpus-dir", "validation", "path to the validation corpus directory")
	force := flag.Bool("force", false, "score anyway even if some documents aren't fully indexed yet -- the resulting score is not trustworthy as a baseline")
	flag.Parse()

	logger := telemetry.NewLogger(os.Stdout, "evalqa")

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

	questions, err := evalcorpus.ResolveQuestions(gt, filenameToDocID)
	if err != nil {
		logger.Error("resolving ground truth against ingested documents failed -- has ingestion finished processing?", "err", err)
		os.Exit(1)
	}

	embedder := llminference.NewQueryEmbedder(cfg.InferenceServiceURL)
	generator := anthropic.New(cfg.AnthropicAPIKey, cfg.AnthropicModel)
	retriever := retrievalpg.New(txRunner, embedder)
	answerer := search.NewAnswerer(generator)
	router := queryrouter.NewLLMRouter(generator)
	graphRetriever := graphragpg.New(txRunner)
	searcher := search.New(retriever, answerer,
		search.WithRouter(router),
		search.WithGraphRetriever(graphRetriever),
	)

	tester := &multihopqa.Tester{
		Searcher: searcher,
		Judge:    &multihopqa.LLMJudge{Generator: generator},
	}
	result, err := tester.Run(ctx, kbID, questions)
	if err != nil {
		logger.Error("multihopqa run failed", "err", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("marshal result failed", "err", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
	logger.Info("multihop qa score",
		"retrieval_score", result.RetrievalScore,
		"answer_score", result.AnswerScore,
		"questions_tested", len(result.Results),
	)
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
