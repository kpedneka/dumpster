package config

import "os"

type Config struct {
	HTTPPort   string
	DBHost     string
	DBPort     string
	DBName     string
	DBUser     string
	DBPassword string
	DBSSL      string
	MinioEndpoint  string
	MinioAccessKey string
	MinioSecretKey string
	// Auth
	JWTSecret      string
	JWTExpiryHours string
	// LLM
	AnthropicAPIKey  string
	AnthropicModel   string
	OpenAIAPIKey     string
	OpenAIEmbedModel string
}

func Load() *Config {
	return &Config{
		HTTPPort:       getEnv("HTTP_PORT", "8080"),
		DBHost:         getEnv("DB_HOST", "localhost"),
		DBPort:         getEnv("DB_PORT", "5432"),
		DBName:         getEnv("DB_NAME", "dumpster"),
		DBUser:         getEnv("DB_USER", "dumpster"),
		DBPassword:     getEnv("DB_PASSWORD", "dumpster"),
		DBSSL:          getEnv("DB_SSLMODE", "disable"),
		MinioEndpoint:  getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey: getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinioSecretKey: getEnv("MINIO_SECRET_KEY", "minioadmin"),
		JWTSecret:      getEnv("JWT_SECRET", "change-me-in-production"),
		JWTExpiryHours: getEnv("JWT_EXPIRY_HOURS", "24"),
		AnthropicAPIKey:  getEnv("ANTHROPIC_API_KEY", ""),
		AnthropicModel:   getEnv("ANTHROPIC_MODEL", "claude-sonnet-4-6"),
		OpenAIAPIKey:     getEnv("OPENAI_API_KEY", ""),
		OpenAIEmbedModel: getEnv("OPENAI_EMBED_MODEL", "text-embedding-3-small"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

