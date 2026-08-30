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
	// MinIO locally and Cloudflare R2 in the cloud.
	S3Endpoint     string
	S3Region       string
	S3Bucket       string
	S3AccessKey    string
	S3SecretKey    string
	S3UsePathStyle bool
	// Observability
	MetricsPort string
	// LLM
	AnthropicAPIKey  string
	AnthropicModel   string
	OpenAIAPIKey     string
	OpenAIEmbedModel string
	// Entity extraction
	// EntityTypes is the closed-but-broad, domain-agnostic set of entity
	// types the extractor is allowed to emit. It is config, not code: add
	// or remove a type by changing ENTITY_TYPES, no migration required.
	EntityTypes []string
	// InferenceServiceURL is the base URL of the consolidated ML inference
	// service (v4.8): entity extraction, PDF region classification, and
	// local embeddings, called over HTTP by both cmd/api and cmd/worker
	// instead of each embedding its own warm Python subprocess. Defaults to
	// the docker-compose service name; override for local dev without Docker
	// or to point at a different deployment.
	InferenceServiceURL string

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
	// WorkerConcurrency is how many jobs the worker processes at once (v4.11).
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
		S3Endpoint:        getEnv("S3_ENDPOINT", "http://localhost:9000"),
		S3Region:          getEnv("S3_REGION", "auto"),
		S3Bucket:          getEnv("S3_BUCKET", "dumpster"),
		S3AccessKey:       getEnv("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:       getEnv("S3_SECRET_KEY", "minioadmin"),
		S3UsePathStyle:    getEnv("S3_USE_PATH_STYLE", "true") == "true",
		AnthropicAPIKey:   getEnv("ANTHROPIC_API_KEY", ""),
		AnthropicModel:    getEnv("ANTHROPIC_MODEL", "claude-sonnet-4-6"),
		OpenAIAPIKey:      getEnv("OPENAI_API_KEY", ""),
		OpenAIEmbedModel:  getEnv("OPENAI_EMBED_MODEL", "text-embedding-3-small"),

		EntityTypes:         getEntityTypes("ENTITY_TYPES", defaultEntityTypes),
		InferenceServiceURL: getEnv("INFERENCE_SERVICE_URL", "http://inference:8000"),

		CookieSecure: getEnv("COOKIE_SECURE", "true") == "true",

		MaxDocumentsPerSession: getEnvInt("MAX_DOCUMENTS_PER_SESSION", 20),
		RateLimitRequests:      getEnvInt("RATE_LIMIT_REQUESTS", 100),
		RateLimitWindow:        getEnvDuration("RATE_LIMIT_WINDOW", time.Minute),
		SweepInterval:          getEnvDuration("SWEEP_INTERVAL", 5*time.Minute),
		JobStaleTimeout:        getEnvDuration("JOB_STALE_TIMEOUT", 15*time.Minute),
		WorkerConcurrency:      getEnvInt("WORKER_CONCURRENCY", 5),
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
