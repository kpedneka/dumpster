package config

import (
	"os"
	"testing"
)

func TestLoad_defaults(t *testing.T) {
	c := Load()

	if c.HTTPPort != "8080" {
		t.Errorf("HTTPPort: got %q, want %q", c.HTTPPort, "8080")
	}
	if c.DBHost != "localhost" {
		t.Errorf("DBHost: got %q, want %q", c.DBHost, "localhost")
	}
	if c.DBName != "dumpster" {
		t.Errorf("DBName: got %q, want %q", c.DBName, "dumpster")
	}
}

func TestLoad_fromEnv(t *testing.T) {
	t.Setenv("HTTP_PORT", "9090")
	t.Setenv("DB_HOST", "db.example.com")

	c := Load()

	if c.HTTPPort != "9090" {
		t.Errorf("HTTPPort: got %q, want %q", c.HTTPPort, "9090")
	}
	if c.DBHost != "db.example.com" {
		t.Errorf("DBHost: got %q, want %q", c.DBHost, "db.example.com")
	}
}

func TestGetEnv_fallback(t *testing.T) {
	os.Unsetenv("NONEXISTENT_VAR")
	if got := getEnv("NONEXISTENT_VAR", "default"); got != "default" {
		t.Errorf("got %q, want %q", got, "default")
	}
}
