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
	if err := os.Unsetenv("NONEXISTENT_VAR"); err != nil {
		t.Fatal(err)
	}
	if got := getEnv("NONEXISTENT_VAR", "default"); got != "default" {
		t.Errorf("got %q, want %q", got, "default")
	}
}

func TestLoad_clerkFromEnv(t *testing.T) {
	t.Setenv("CLERK_SECRET_KEY", "sk_test_abc")
	t.Setenv("CLERK_WEBHOOK_SECRET", "whsec_abc")

	c := Load()

	if c.ClerkSecretKey != "sk_test_abc" {
		t.Errorf("ClerkSecretKey: got %q, want %q", c.ClerkSecretKey, "sk_test_abc")
	}
	if c.ClerkWebhookSecret != "whsec_abc" {
		t.Errorf("ClerkWebhookSecret: got %q, want %q", c.ClerkWebhookSecret, "whsec_abc")
	}
}
