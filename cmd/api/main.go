package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	communitypg "github.com/kunalpednekar/dumpster/internal/community/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	graphragpg "github.com/kunalpednekar/dumpster/internal/graphrag/pgstore"
	inquirypg "github.com/kunalpednekar/dumpster/internal/inquiry/pgstore"
	"github.com/kunalpednekar/dumpster/internal/intrusion"
	intrusionpg "github.com/kunalpednekar/dumpster/internal/intrusion/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/llm/anthropic"
	llminference "github.com/kunalpednekar/dumpster/internal/llm/inference"
	"github.com/kunalpednekar/dumpster/internal/llm/instrumented"
	manifestpg "github.com/kunalpednekar/dumpster/internal/manifest/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	ratelimitpg "github.com/kunalpednekar/dumpster/internal/ratelimit/pgstore"
	retrievalpg "github.com/kunalpednekar/dumpster/internal/retrieval/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	queryrouter "github.com/kunalpednekar/dumpster/internal/router"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/server"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	statspg "github.com/kunalpednekar/dumpster/internal/stats/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
	"github.com/kunalpednekar/dumpster/internal/theme"
	themepg "github.com/kunalpednekar/dumpster/internal/theme/pgstore"
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
	// usage_stats has no RLS policy and is read by the public,
	// unauthenticated GET /stats endpoint, so it deliberately uses a plain
	// TxRunner rather than the RLS-aware one everything else here does.
	statsRepo := statspg.New(db.NewTxRunner(pool))

	logger.Info("object storage config",
		"endpoint", cfg.S3Endpoint,
		"bucket", cfg.S3Bucket,
		"region", cfg.S3Region,
		"path_style", cfg.S3UsePathStyle,
		"key_set", cfg.S3AccessKey != "",
	)
	obj, err := s3store.New(context.Background(), s3store.Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})
	if err != nil {
		logger.Error("object store setup failed", "err", err)
		os.Exit(1)
	}

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

	jobs := qpg.New(pool)
	deps := server.Deps{
		KBs:                       kbpg.New(txRunner),
		Docs:                      docpg.New(txRunner),
		Objects:                   obj,
		Publisher:                 jobs,
		JobStatusReader:           jobs,
		Manifest:                  manifestpg.New(txRunner),
		Canonical:                 canonicalpg.New(txRunner),
		Communities:               communitypg.New(txRunner),
		MaxCommunityGraphEntities: cfg.MaxCommunityGraphEntities,
		CommunityDetectionTimeout: cfg.CommunityDetectionTimeout,
		Themes:                    themepg.New(txRunner),
		ThemeSummarizer:           theme.NewSummarizer(generator),
		Intrusion:                 intrusionpg.New(txRunner),
		IntrusionTester:           intrusion.NewTester(generator),
		// Relations/RelationExtractor deliberately left nil: POST
		// /kbs/{id}/relations is unreachable (registerRelationRoutes only
		// registers when both are set — see server.go) until relation
		// extraction has its own before/after quality measurement and a
		// decided place in the pipeline, tracked as a future idea rather
		// than active work. The feature itself is untouched, just unwired.
		Searcher:     searcher,
		Inquiries:    inquirypg.New(txRunner),
		Sessions:     sessionpg.New(pool),
		Stats:        statsRepo,
		Instruments:  instruments,
		SPADir:       "web/dist",
		CookieSecure: cfg.CookieSecure,
		// Postgres-backed, not internal/ratelimit/memory: the in-process
		// implementation keeps its counter in this replica's own memory,
		// which silently under-enforces once api runs more than one
		// replica (N replicas = N independent counters = an effective
		// limit N times more permissive). This shares state across every
		// replica via rate_limit_counters (migrations/030).
		RateLimiter: ratelimitpg.New(pool, cfg.RateLimitRequests, cfg.RateLimitWindow),
	}

	router := server.NewRouter(deps)

	addr := fmt.Sprintf(":%s", cfg.HTTPPort)
	logger.Info("api starting", "addr", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}
