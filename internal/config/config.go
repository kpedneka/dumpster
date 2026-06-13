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
	MinioEndpoint string
	MinioAccessKey string
	MinioSecretKey string
}

func Load() *Config {
	return &Config{
		HTTPPort:      getEnv("HTTP_PORT", "8080"),
		DBHost:        getEnv("DB_HOST", "localhost"),
		DBPort:        getEnv("DB_PORT", "5432"),
		DBName:        getEnv("DB_NAME", "dumpster"),
		DBUser:        getEnv("DB_USER", "dumpster"),
		DBPassword:    getEnv("DB_PASSWORD", "dumpster"),
		DBSSL:         getEnv("DB_SSLMODE", "disable"),
		MinioEndpoint: getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey: getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinioSecretKey: getEnv("MINIO_SECRET_KEY", "minioadmin"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

