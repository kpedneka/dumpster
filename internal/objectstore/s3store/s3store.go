// Package s3store provides an ObjectStore backed by any S3-compatible API:
// local MinIO or Cloudflare R2 (both need static credentials, since neither
// participates in AWS IAM) as well as real AWS S3 in staging/production
// (which instead resolves credentials via the standard AWS credential chain
// — an ECS task role, in this project's deployments). Switch providers via
// config.
package s3store

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
)

// Store implements objectstore.ObjectStore.
type Store struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

// Config holds the endpoint-configurable S3 connection details.
type Config struct {
	// Endpoint overrides the S3 API's base URL, e.g. "http://localhost:9000"
	// for MinIO or "https://<account-id>.r2.cloudflarestorage.com" for R2.
	// Leave empty for real AWS S3 -- the SDK resolves the correct regional
	// endpoint on its own from Region.
	Endpoint string
	Region   string // "auto" for R2, a real AWS region (e.g. "us-east-1") for S3
	Bucket   string
	// AccessKey and SecretKey are required for any endpoint outside AWS IAM
	// (MinIO, R2). Leave both empty for real AWS S3 to resolve credentials
	// via the standard AWS credential chain instead -- the same chain
	// internal/awsbatch.NewClient uses, so an ECS task role works here too.
	AccessKey    string
	SecretKey    string
	UsePathStyle bool // required for MinIO; must be false for real AWS S3
}

func New(ctx context.Context, cfg Config) (*Store, error) {
	var creds aws.CredentialsProvider
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		creds = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
		if err != nil {
			return nil, fmt.Errorf("s3store: load default AWS config: %w", err)
		}
		creds = awsCfg.Credentials
	}

	opts := s3.Options{
		Region:       cfg.Region,
		Credentials:  creds,
		UsePathStyle: cfg.UsePathStyle,
	}
	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
	}

	client := s3.New(opts)
	return &Store{
		client:  client,
		presign: s3.NewPresignClient(client),
		bucket:  cfg.Bucket,
	}, nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          r,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("s3store: put %q: %w", key, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3store: get %q: %w", key, err)
	}
	return out.Body, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3store: delete %q: %w", key, err)
	}
	return nil
}

func (s *Store) PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3store: presign %q: %w", key, err)
	}
	return req.URL, nil
}

// PresignedPutURL returns a time-limited PUT URL for key.
func (s *Store) PresignedPutURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3store: presign put %q: %w", key, err)
	}
	return req.URL, nil
}

// Compile-time check.
var _ objectstore.ObjectStore = (*Store)(nil)
