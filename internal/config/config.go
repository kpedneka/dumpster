package config

import (
	"os"

	env "github.com/joho/godotenv"
)

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
	// Auth — Clerk manages sign-up/sign-in/session lifecycle; the API only
	// needs a secret key to verify sessions and call the Backend API, and
	// a webhook signing secret to authenticate Clerk's lifecycle webhooks.
	ClerkSecretKey     string
	ClerkWebhookSecret string
	// LLM
	AnthropicAPIKey  string
	AnthropicModel   string
	OpenAIAPIKey     string
	OpenAIEmbedModel string
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
		ClerkSecretKey:     getEnv("CLERK_SECRET_KEY", ""),
		ClerkWebhookSecret: getEnv("CLERK_WEBHOOK_SECRET", ""),
		AnthropicAPIKey:    getEnv("ANTHROPIC_API_KEY", ""),
		AnthropicModel:     getEnv("ANTHROPIC_MODEL", "claude-sonnet-4-6"),
		OpenAIAPIKey:       getEnv("OPENAI_API_KEY", ""),
		OpenAIEmbedModel:   getEnv("OPENAI_EMBED_MODEL", "text-embedding-3-small"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
