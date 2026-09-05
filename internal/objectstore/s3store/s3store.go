// Package s3store provides an ObjectStore backed by any S3-compatible API
// (MinIO locally, Cloudflare R2 in the cloud). Switch providers via config.
package s3store

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	Endpoint     string // full URL, e.g. "http://localhost:9000"
	Region       string // "auto" for R2, "us-east-1" for MinIO
	Bucket       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool // required for MinIO
}

func New(cfg Config) *Store {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	client := s3.New(s3.Options{
		Region:       cfg.Region,
		Credentials:  creds,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: cfg.UsePathStyle,
	})
	return &Store{
		client:  client,
		presign: s3.NewPresignClient(client),
		bucket:  cfg.Bucket,
	}
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
