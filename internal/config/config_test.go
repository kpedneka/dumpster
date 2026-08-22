package config

import (
	"os"
	"reflect"
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
	if !reflect.DeepEqual(c.EntityTypes, defaultEntityTypes) {
		t.Errorf("EntityTypes: got %v, want %v", c.EntityTypes, defaultEntityTypes)
	}
	if c.EntityExtractorScript != "scripts/extract_entities.py" {
		t.Errorf("EntityExtractorScript: got %q, want %q", c.EntityExtractorScript, "scripts/extract_entities.py")
	}
}

func TestLoad_entityTypesFromEnv(t *testing.T) {
	t.Setenv("ENTITY_TYPES", "person, organization ,custom_type")

	c := Load()

	want := []string{"person", "organization", "custom_type"}
	if !reflect.DeepEqual(c.EntityTypes, want) {
		t.Errorf("EntityTypes: got %v, want %v", c.EntityTypes, want)
	}
}

func TestGetEntityTypes_fallbackOnEmpty(t *testing.T) {
	t.Setenv("ENTITY_TYPES_EMPTY_TEST", "  ,  ,")
	fallback := []string{"a", "b"}
	if got := getEntityTypes("ENTITY_TYPES_EMPTY_TEST", fallback); !reflect.DeepEqual(got, fallback) {
		t.Errorf("got %v, want fallback %v", got, fallback)
	}
}

func TestGetEntityTypes_unset(t *testing.T) {
	if err := os.Unsetenv("ENTITY_TYPES_UNSET_TEST"); err != nil {
		t.Fatal(err)
	}
	fallback := []string{"a", "b"}
	if got := getEntityTypes("ENTITY_TYPES_UNSET_TEST", fallback); !reflect.DeepEqual(got, fallback) {
		t.Errorf("got %v, want fallback %v", got, fallback)
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

func TestLoad_resendEnvVarsPresent(t *testing.T) {
	// Smoke-test that the Resend config block still loads from env.
	t.Setenv("RESEND_API_KEY", "re_smoke_test")
	c := Load()
	if c.ResendAPIKey != "re_smoke_test" {
		t.Errorf("ResendAPIKey: got %q, want %q", c.ResendAPIKey, "re_smoke_test")
	}
}

func TestLoad_resendDefaults(t *testing.T) {
	c := Load()

	if c.ResendAPIKey != "" {
		t.Errorf("ResendAPIKey: got %q, want empty default", c.ResendAPIKey)
	}
	if c.ResendFromAddr != "noreply@example.com" {
		t.Errorf("ResendFromAddr: got %q, want %q", c.ResendFromAddr, "noreply@example.com")
	}
}

func TestLoad_resendFromEnv(t *testing.T) {
	t.Setenv("RESEND_API_KEY", "re_test_abc")
	t.Setenv("RESEND_FROM_ADDR", "demo@dumpster.example.com")

	c := Load()

	if c.ResendAPIKey != "re_test_abc" {
		t.Errorf("ResendAPIKey: got %q, want %q", c.ResendAPIKey, "re_test_abc")
	}
	if c.ResendFromAddr != "demo@dumpster.example.com" {
		t.Errorf("ResendFromAddr: got %q, want %q", c.ResendFromAddr, "demo@dumpster.example.com")
	}
}
