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
	// Email — transactional email for the demo account lifecycle, sent via Resend.
	// Reserved for future use; sessions are anonymous and have no email address.
	ResendAPIKey   string
	ResendFromAddr string
	// OllamaURL is the base URL of the local Ollama service used by the
	// RegionClassificationHandler for VLM inference (figure description and
	// scanned-content confirmation). Defaults to the docker-compose service
	// name; override in .env.local for local development without Docker.
	OllamaURL      string
	// OllamaVLMModel is the Ollama model name used for vision-language
	// inference. Pull it once with: docker compose exec ollama ollama pull qwen2.5vl
	OllamaVLMModel string
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
	// EntityExtractorScript is the path to the Python script the gliner
	// adapter invokes via os/exec to run local spaCy+GLiNER extraction.
	EntityExtractorScript string
	// EntityExtractorPython is the Python interpreter used to run
	// EntityExtractorScript (and RegionExtractorScript).
	EntityExtractorPython string
	// RegionExtractorScript is the path to scripts/extract_regions.py, the
	// PDF region classifier invoked by the RegionClassificationHandler.
	RegionExtractorScript string

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
}

func Load() *Config {
	// .env.local is listed first so its values win over .env for duplicate keys.
	// Both files are optional; missing files are silently skipped.
	_ = env.Load(".env.local", ".env")
	return &Config{
		HTTPPort:           getEnv("HTTP_PORT", "8080"),
		DBHost:             getEnv("DB_HOST", "localhost"),
		DBPort:             getEnv("DB_PORT", "5432"),
		DBName:             getEnv("DB_NAME", "dumpster"),
		DBUser:             getEnv("DB_USER", "dumpster"),
		DBPassword:         getEnv("DB_PASSWORD", "dumpster"),
		DBSSL:              getEnv("DB_SSLMODE", "disable"),
		DatabaseURL:        getEnv("DATABASE_URL", ""),
		DatabaseURLPooled:  getEnv("DATABASE_URL_POOLED", ""),
		MetricsPort:        getEnv("METRICS_PORT", "9090"),
		S3Endpoint:         getEnv("S3_ENDPOINT", "http://localhost:9000"),
		S3Region:           getEnv("S3_REGION", "auto"),
		S3Bucket:           getEnv("S3_BUCKET", "dumpster"),
		S3AccessKey:        getEnv("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:        getEnv("S3_SECRET_KEY", "minioadmin"),
		S3UsePathStyle:     getEnv("S3_USE_PATH_STYLE", "true") == "true",
		ResendAPIKey:       getEnv("RESEND_API_KEY", ""),
		ResendFromAddr:     getEnv("RESEND_FROM_ADDR", "noreply@example.com"),
		OllamaURL:          getEnv("OLLAMA_URL", "http://ollama:11434"),
		OllamaVLMModel:     getEnv("OLLAMA_VLM_MODEL", "qwen2.5vl"),
		AnthropicAPIKey:    getEnv("ANTHROPIC_API_KEY", ""),
		AnthropicModel:     getEnv("ANTHROPIC_MODEL", "claude-sonnet-4-6"),
		OpenAIAPIKey:       getEnv("OPENAI_API_KEY", ""),
		OpenAIEmbedModel:   getEnv("OPENAI_EMBED_MODEL", "text-embedding-3-small"),

		EntityTypes:           getEntityTypes("ENTITY_TYPES", defaultEntityTypes),
		EntityExtractorScript: getEnv("ENTITY_EXTRACTOR_SCRIPT", "scripts/extract_entities.py"),
		EntityExtractorPython: getEnv("ENTITY_EXTRACTOR_PYTHON", "python3"),
		RegionExtractorScript: getEnv("REGION_EXTRACTOR_SCRIPT", "scripts/extract_regions.py"),

		MaxDocumentsPerSession: getEnvInt("MAX_DOCUMENTS_PER_SESSION", 20),
		RateLimitRequests:      getEnvInt("RATE_LIMIT_REQUESTS", 100),
		RateLimitWindow:        getEnvDuration("RATE_LIMIT_WINDOW", time.Minute),
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
