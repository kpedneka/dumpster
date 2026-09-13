package s3store

import (
	"context"
	"testing"
)

// New has two credential paths: static keys (required for any
// S3-compatible-but-not-AWS endpoint, e.g. Cloudflare R2 or local MinIO,
// since neither participates in AWS IAM) and the standard AWS credential
// chain (for real AWS S3, resolved the same way internal/awsbatch.NewClient
// resolves it — env vars, shared config, or an ECS task role). Both paths
// are exercised here without touching the network: aws-sdk-go-v2 resolves
// credentials lazily on first use, so client construction succeeds
// regardless of whether real credentials are actually present in the test
// environment.
func TestNew_staticCredentials(t *testing.T) {
	store, err := New(context.Background(), Config{
		Endpoint:     "http://localhost:9000",
		Region:       "us-east-1",
		Bucket:       "test-bucket",
		AccessKey:    "minioadmin",
		SecretKey:    "minioadmin",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("New with static credentials: %v", err)
	}
	if store.bucket != "test-bucket" {
		t.Errorf("bucket = %q, want %q", store.bucket, "test-bucket")
	}
}

func TestNew_defaultCredentialChain(t *testing.T) {
	// AccessKey/SecretKey both empty signals "real AWS S3, resolve
	// credentials via the default chain" -- the same signal
	// internal/awsbatch.NewClient's callers rely on by never having a
	// static-credential option at all. Endpoint is also left empty here,
	// matching real usage: a real AWS S3 bucket needs no BaseEndpoint
	// override, only a region.
	store, err := New(context.Background(), Config{
		Region: "us-east-1",
		Bucket: "dumpster-staging",
	})
	if err != nil {
		t.Fatalf("New with default credential chain: %v", err)
	}
	if store.bucket != "dumpster-staging" {
		t.Errorf("bucket = %q, want %q", store.bucket, "dumpster-staging")
	}
}
