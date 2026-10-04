package sqs_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queue/sqs"
	"github.com/kunalpednekar/dumpster/internal/queue/sqs/mock"
)

func testConfig() sqs.Config {
	return sqs.Config{
		EdgeExtractionQueueURL:   "https://sqs.example/edge-extraction",
		CanonicalizationQueueURL: "https://sqs.example/canonicalization",
	}
}

func TestDispatch_SendsToTheJobTypesQueue(t *testing.T) {
	cases := map[queue.JobType]string{
		queue.JobTypeEdgeExtraction:   testConfig().EdgeExtractionQueueURL,
		queue.JobTypeCanonicalization: testConfig().CanonicalizationQueueURL,
	}
	for jobType, wantURL := range cases {
		t.Run(string(jobType), func(t *testing.T) {
			client := mock.New()
			store := sqs.New(client, testConfig())
			run := queue.JobRun{Type: jobType, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 2}

			if err := store.Dispatch(context.Background(), run); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}

			sent := client.Sent()
			if len(sent) != 1 {
				t.Fatalf("SendMessage calls = %d, want 1", len(sent))
			}
			if sent[0].QueueURL != wantURL {
				t.Errorf("queue URL = %q, want %q", sent[0].QueueURL, wantURL)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			want := map[string]any{
				"type":        string(jobType),
				"document_id": run.DocumentID.String(),
				"user_id":     run.UserID.String(),
				"attempt":     float64(2),
			}
			for k, v := range want {
				if body[k] != v {
					t.Errorf("body[%q] = %v, want %v", k, body[k], v)
				}
			}
		})
	}
}

func TestDispatch_UnsupportedJobType_SendsNothing(t *testing.T) {
	client := mock.New()
	store := sqs.New(client, testConfig())

	err := store.Dispatch(context.Background(), queue.JobRun{
		Type: queue.JobTypeDocumentIndexing, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 1,
	})
	if err == nil {
		t.Error("Dispatch err = nil, want an error for a job type that has no SQS queue")
	}
	if n := len(client.Sent()); n != 0 {
		t.Errorf("SendMessage calls = %d, want 0", n)
	}
}

func TestDispatch_UnconfiguredQueue_SendsNothing(t *testing.T) {
	client := mock.New()
	cfg := testConfig()
	cfg.CanonicalizationQueueURL = ""
	store := sqs.New(client, cfg)

	err := store.Dispatch(context.Background(), queue.JobRun{
		Type: queue.JobTypeCanonicalization, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 1,
	})
	if err == nil {
		t.Error("Dispatch err = nil, want an error for an unconfigured queue URL")
	}
	if n := len(client.Sent()); n != 0 {
		t.Errorf("SendMessage calls = %d, want 0", n)
	}
}

func TestDispatch_SendError_IsReturned(t *testing.T) {
	boom := errors.New("sqs unavailable")
	client := mock.New()
	client.SendErr = boom
	store := sqs.New(client, testConfig())

	err := store.Dispatch(context.Background(), queue.JobRun{
		Type: queue.JobTypeEdgeExtraction, DocumentID: uuid.New(), UserID: uuid.New(), Attempt: 1,
	})
	if !errors.Is(err, boom) {
		t.Errorf("Dispatch err = %v, want it to wrap %v", err, boom)
	}
}
