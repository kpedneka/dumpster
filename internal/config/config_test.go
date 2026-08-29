package config

import (
	"os"
	"reflect"
	"testing"
	"time"
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

func TestLoad_sweepIntervalDefault(t *testing.T) {
	c := Load()
	if c.SweepInterval != 5*time.Minute {
		t.Errorf("SweepInterval default: got %v, want 5m", c.SweepInterval)
	}
}

func TestLoad_sweepIntervalFromEnv(t *testing.T) {
	t.Setenv("SWEEP_INTERVAL", "2m")
	c := Load()
	if c.SweepInterval != 2*time.Minute {
		t.Errorf("SweepInterval: got %v, want 2m", c.SweepInterval)
	}
}

func TestLoad_jobStaleTimeoutDefault(t *testing.T) {
	c := Load()
	if c.JobStaleTimeout != 15*time.Minute {
		t.Errorf("JobStaleTimeout default: got %v, want 15m", c.JobStaleTimeout)
	}
}

func TestLoad_jobStaleTimeoutFromEnv(t *testing.T) {
	t.Setenv("JOB_STALE_TIMEOUT", "10m")
	c := Load()
	if c.JobStaleTimeout != 10*time.Minute {
		t.Errorf("JobStaleTimeout: got %v, want 10m", c.JobStaleTimeout)
	}
}
