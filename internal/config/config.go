package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	env "github.com/joho/godotenv"
)

// defaultEntityTypes is the closed-but-broad, domain-agnostic entity type
// set used when ENTITY_TYPES is not set. It is deliberately not the only
// possible set: operators add or remove types by changing config, not code,
// and no migration is required to do so.
var defaultEntityTypes = []string{
	"person",
	"organization",
	"location",
	"concept",
	"event",
	"work_of_art",
	"date_time",
}

type Config struct {
	HTTPPort   string
	DBHost     string
	DBPort     string
	DBName     string
	DBUser     string
	DBPassword string
	DBSSL      string
	// DatabaseURL is a full Postgres connection string (e.g. from Neon).
	// When set it takes precedence over the individual DB_* fields.
	// Use the direct (non-pooled) endpoint for the worker and migrations.
	DatabaseURL string
	// DatabaseURLPooled is the PgBouncer connection string for the API.
	// When set it takes precedence over DatabaseURL for pooled access.
	// Use the Neon pooler endpoint (host has -pooler suffix) for the API.
	DatabaseURLPooled string
	// Object storage — endpoint-configurable so the same adapter serves
	// MinIO or Cloudflare R2 locally and real AWS S3 in staging/production
	// (see internal/objectstore/s3store's doc comment for the credential
	// implications of that split). Holds real, user-owned documents — a
	// different retention/access contract than the scratch bucket below,
	// which is why the two are never conflated.
	S3Endpoint     string
	S3Region       string
	S3Bucket       string
	S3AccessKey    string
	S3SecretKey    string
	S3UsePathStyle bool
	// S3Scratch* configures a separate bucket for AWS Batch jobs' transient
	// input/output handoff (internal/entity/awsbatch, internal/llm/awsbatch,
	// internal/manifest/awsbatch) — never real user documents. Kept out of
	// the main document bucket above deliberately: scratch objects don't
	// need (and shouldn't get) the same durability/retention guarantees
	// real documents do, and a separate bucket makes that bucket's size
	// alone a useful at-a-glance signal for whether Batch scratch cleanup
	// is actually working, without it being muddied by real document
	// storage growth. Named S3Scratch, not R2Scratch, on purpose -- this
	// bucket moved from Cloudflare R2 to real AWS S3 (see the "Migrate
	// object storage: R2 -> S3" dev board card); AccessKey/SecretKey are
	// left empty in every AWS deployment so internal/objectstore/s3store
	// resolves credentials via the ECS task role instead of a static key.
	S3ScratchEndpoint     string
	S3ScratchRegion       string
	S3ScratchBucket       string
	S3ScratchAccessKey    string
	S3ScratchSecretKey    string
	S3ScratchUsePathStyle bool
	// Observability
	MetricsPort string
	// LLM
	AnthropicAPIKey string
	AnthropicModel  string
	// Entity extraction
	// EntityTypes is the closed-but-broad, domain-agnostic set of entity
	// types the extractor is allowed to emit. It is config, not code: add
	// or remove a type by changing ENTITY_TYPES, no migration required.
	EntityTypes []string
	// InferenceServiceURL is the base URL of the consolidated ML inference
	// service, called over HTTP: PDF region classification (both cmd/api
	// and cmd/worker) and query-time embeddings (cmd/api only — see
	// internal/llm/inference.NewQueryEmbedder). Ingestion-time (document)
	// embeddings do NOT go through this any more — see
	// internal/llm/awsbatch and EmbedBatchJobQueue/EmbedBatchJobDefinition
	// below for why (Fly's shared-cpu tier throttled that CPU-bound work
	// severely; a live search query still can't tolerate a Batch cold
	// start, so it stays here). Entity extraction never went through this
	// either — see internal/entity/awsbatch and AWSRegion/BatchJobQueue/
	// BatchJobDefinition below. Defaults to the docker-compose service
	// name; override for local dev without Docker or to point at a
	// different deployment.
	InferenceServiceURL string

	// AWSRegion is the region cmd/worker's AWS Batch/CloudWatch Logs calls
	// target, for both entity extraction (internal/entity/awsbatch) and
	// ingestion-time embedding (internal/llm/awsbatch). Credentials
	// themselves come from the standard AWS SDK chain
	// (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY env vars in this app's
	// deployments), not a dedicated config field.
	AWSRegion string
	// BatchJobQueue and BatchJobDefinition identify the AWS Batch resources
	// entity extraction submits jobs against. Required — cmd/worker exits
	// at startup if either is empty.
	BatchJobQueue      string
	BatchJobDefinition string
	// EmbedBatchJobQueue and EmbedBatchJobDefinition identify the separate
	// AWS Batch resources ingestion-time embedding submits jobs against
	// (internal/llm/awsbatch) — a distinct, CPU-only Fargate compute
	// environment from entity extraction's GPU one above, since the two
	// have nothing in common but "runs on AWS Batch." Required —
	// cmd/worker and cmd/reembed exit at startup if either is empty.
	EmbedBatchJobQueue      string
	EmbedBatchJobDefinition string
	// RegionsBatchJobQueue and RegionsBatchJobDefinition identify a third,
	// independent AWS Batch resource pair for PDF region extraction
	// (internal/manifest/awsbatch) -- required, same as the two pairs
	// above; cmd/worker exits at startup if either is empty. No HTTP
	// fallback to the old always-on inference service: that path existed
	// briefly while these Batch resources hadn't been provisioned yet,
	// but was removed once Fly deploys stopped entirely, since keeping a
	// fallback this process would never actually use is just dead code.
	RegionsBatchJobQueue      string
	RegionsBatchJobDefinition string
	// BatchPollInterval is how often the awsbatch extractor/embedder
	// checks a submitted job's status. Defaults to 5s (their own default)
	// when unset. Shared between entity extraction and embedding — both
	// poll AWS Batch the same way, no reason for two separate knobs.
	BatchPollInterval time.Duration
	// BatchPresignTTL is how long the input URL handed to a Batch job stays
	// valid. Defaults to 15m (their own default) when unset. Shared for
	// the same reason as BatchPollInterval.
	BatchPresignTTL time.Duration

	// CookieSecure controls the Secure attribute on the session cookie.
	// Must stay true wherever the API is reached over TLS (e.g. behind
	// Fly.io's TLS termination in production/staging). Set COOKIE_SECURE=false
	// only for local development, where the Vite dev server proxies to a
	// plain-http backend and browsers silently drop Secure cookies sent
	// over http.
	CookieSecure bool

	// Guardrails — per-session upload limits and IP rate limiting.
	// MaxDocumentsPerSession caps how many documents a single anonymous
	// session may upload in total (across all knowledge bases).
	MaxDocumentsPerSession int
	// RateLimitRequests is the maximum number of requests allowed from a
	// single IP within RateLimitWindow.
	RateLimitRequests int
	// RateLimitWindow is the sliding window over which RateLimitRequests is
	// counted.
	RateLimitWindow time.Duration
	// SweepInterval is how often the always-on worker runs the session sweep.
	// Defaults to 5 minutes; lower it in staging to verify cleanup quickly.
	SweepInterval time.Duration
	// QueueMetricsPublishInterval is how often cmd/worker publishes queue
	// backlog depth to CloudWatch (internal/queuemetrics) for the worker's
	// queue-depth ECS auto-scaling policy. A much shorter cadence than
	// SweepInterval on purpose: CloudWatch evaluates standard-resolution
	// alarms roughly once a minute, so a slower publish cadence would make
	// the scaling signal lag real backlog changes. Defaults to 60s.
	QueueMetricsPublishInterval time.Duration
	// WorkerConcurrency is how many jobs the worker processes at once.
	// Defaults to 5. Safe to run above 1 now that entity extraction and
	// region classification call the inference service over HTTP instead of
	// embedding a warm Python subprocess in this process — see
	// worker.Config.Concurrency's doc for why raising this used to be unsafe.
	WorkerConcurrency int
	// JobStaleTimeout is how long a job may sit claimed ("processing")
	// before it's treated as orphaned and reclaimed back to "pending" on
	// the same ticker as the session sweep. Guards against a job stuck
	// forever if the worker that claimed it dies or restarts mid-run —
	// nothing else detects that: Dequeue's claim-then-commit transaction is
	// short, so a crash mid-Handle() never touches the jobs table again,
	// and Nack (which normally dead-letters after enough failures) only
	// fires when Handle() actually returns, which a killed process never
	// gets the chance to do. Defaults to 15 minutes, generous enough that a
	// legitimately slow job (cold GLiNER model load plus many chunks has
	// been observed taking several minutes) isn't reclaimed out from under
	// a worker still actively running it.
	JobStaleTimeout time.Duration

	// MaxCommunityGraphEntities caps how many canonical entities a KB may
	// have before POST /kbs/{id}/communities refuses to run Louvain
	// synchronously in the request handler. A cheap pre-check (a COUNT
	// query) against this ceiling runs before the graph is ever built, so a
	// pathological KB never gets far enough to risk the API process's
	// memory. Defaults to 5000 — generous for any realistic KB under the
	// current 20-document/32MB-each upload limits, small enough to keep
	// worst-case memory and runtime bounded on the API's 256MB Fly VM.
	MaxCommunityGraphEntities int
	// CommunityDetectionTimeout bounds one POST /kbs/{id}/communities call.
	// Backstop, not the primary guard — internal/community.Louvain polls
	// for context cancellation between passes, so this actually stops the
	// computation rather than just abandoning the HTTP response while it
	// keeps running. Defaults to 30 seconds.
	CommunityDetectionTimeout time.Duration

	// EntityExtractionBatchSize caps how many chunks EntityHandler sends to
	// the inference service's /entities endpoint in one request. Bounds
	// worst-case single-request duration regardless of document size —
	// a document with hundreds of chunks sent in one unbatched call was
	// measured taking long enough to exceed JOB_STALE_TIMEOUT under normal
	// operation, no deploy/restart involved (see the production ingestion
	// incident writeup). Defaults to 50.
	EntityExtractionBatchSize int
}

func Load() *Config {
	// .env.local is listed first so its values win over .env for duplicate keys.
	// Both files are optional; missing files are silently skipped.
	_ = env.Load(".env.local", ".env")
	return &Config{
		HTTPPort:          getEnv("HTTP_PORT", "8080"),
		DBHost:            getEnv("DB_HOST", "localhost"),
		DBPort:            getEnv("DB_PORT", "5432"),
		DBName:            getEnv("DB_NAME", "dumpster"),
		DBUser:            getEnv("DB_USER", "dumpster"),
		DBPassword:        getEnv("DB_PASSWORD", "dumpster"),
		DBSSL:             getEnv("DB_SSLMODE", "disable"),
		DatabaseURL:       getEnv("DATABASE_URL", ""),
		DatabaseURLPooled: getEnv("DATABASE_URL_POOLED", ""),
		MetricsPort:       getEnv("METRICS_PORT", "9090"),
		// No placeholder default for Endpoint/AccessKey/SecretKey (e.g.
		// "http://localhost:9000", "minioadmin") on purpose: on real AWS S3
		// (staging/production) all three are deliberately left unset in the
		// deployed environment -- an empty Endpoint tells s3store.New to let
		// the SDK resolve the real regional S3 endpoint on its own, and
		// empty Access/SecretKey tells it to resolve credentials via the
		// ECS task role instead of attempting -- and failing -- static auth
		// with a bogus key. A real local MinIO/R2 setup always sets all
		// three explicitly via .env.local (see .env.example), so none of
		// these defaults are ever actually relied on for local dev either.
		S3Endpoint:     getEnv("S3_ENDPOINT", ""),
		S3Region:       getEnv("S3_REGION", "auto"),
		S3Bucket:       getEnv("S3_BUCKET", "dumpster"),
		S3AccessKey:    getEnv("S3_ACCESS_KEY", ""),
		S3SecretKey:    getEnv("S3_SECRET_KEY", ""),
		S3UsePathStyle: getEnv("S3_USE_PATH_STYLE", "true") == "true",
		// No local-dev default (unlike S3* above): the Fargate job writing
		// here needs a real, internet-reachable bucket regardless of
		// environment -- a local MinIO instance on docker-compose's
		// internal network isn't reachable from AWS. AccessKey/SecretKey
		// also default to empty, not a placeholder value: on real AWS S3
		// (staging/production) they're deliberately left unset so
		// s3store.New resolves credentials via the ECS task role instead.
		S3ScratchEndpoint:     getEnv("S3_SCRATCH_ENDPOINT", ""),
		S3ScratchRegion:       getEnv("S3_SCRATCH_REGION", "auto"),
		S3ScratchBucket:       getEnv("S3_SCRATCH_BUCKET", ""),
		S3ScratchAccessKey:    getEnv("S3_SCRATCH_ACCESS_KEY", ""),
		S3ScratchSecretKey:    getEnv("S3_SCRATCH_SECRET_KEY", ""),
		S3ScratchUsePathStyle: getEnv("S3_SCRATCH_USE_PATH_STYLE", "false") == "true",
		AnthropicAPIKey:       getEnv("ANTHROPIC_API_KEY", ""),
		AnthropicModel:        getEnv("ANTHROPIC_MODEL", "claude-sonnet-4-6"),

		EntityTypes:         getEntityTypes("ENTITY_TYPES", defaultEntityTypes),
		InferenceServiceURL: getEnv("INFERENCE_SERVICE_URL", "http://inference:8000"),

		AWSRegion:                 getEnv("AWS_REGION", "us-east-1"),
		BatchJobQueue:             getEnv("BATCH_JOB_QUEUE", ""),
		BatchJobDefinition:        getEnv("BATCH_JOB_DEFINITION", ""),
		EmbedBatchJobQueue:        getEnv("EMBED_BATCH_JOB_QUEUE", ""),
		EmbedBatchJobDefinition:   getEnv("EMBED_BATCH_JOB_DEFINITION", ""),
		RegionsBatchJobQueue:      getEnv("REGIONS_BATCH_JOB_QUEUE", ""),
		RegionsBatchJobDefinition: getEnv("REGIONS_BATCH_JOB_DEFINITION", ""),
		BatchPollInterval:         getEnvDuration("BATCH_POLL_INTERVAL", 0),
		BatchPresignTTL:           getEnvDuration("BATCH_PRESIGN_TTL", 0),

		CookieSecure: getEnv("COOKIE_SECURE", "true") == "true",

		MaxDocumentsPerSession:      getEnvInt("MAX_DOCUMENTS_PER_SESSION", 20),
		RateLimitRequests:           getEnvInt("RATE_LIMIT_REQUESTS", 100),
		RateLimitWindow:             getEnvDuration("RATE_LIMIT_WINDOW", time.Minute),
		SweepInterval:               getEnvDuration("SWEEP_INTERVAL", 5*time.Minute),
		QueueMetricsPublishInterval: getEnvDuration("QUEUE_METRICS_PUBLISH_INTERVAL", 60*time.Second),
		JobStaleTimeout:             getEnvDuration("JOB_STALE_TIMEOUT", 15*time.Minute),
		WorkerConcurrency:           getEnvInt("WORKER_CONCURRENCY", 5),

		MaxCommunityGraphEntities: getEnvInt("MAX_COMMUNITY_GRAPH_ENTITIES", 5000),
		CommunityDetectionTimeout: getEnvDuration("COMMUNITY_DETECTION_TIMEOUT", 30*time.Second),

		EntityExtractionBatchSize: getEnvInt("ENTITY_EXTRACTION_BATCH_SIZE", 50),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// getEntityTypes reads a comma-separated list of entity types from the
// named env var, trimming whitespace and dropping empty entries. Falls back
// to fallback when the env var is unset or empty after trimming.
func getEntityTypes(key string, fallback []string) []string {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
