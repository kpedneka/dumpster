package awsbatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/awsbatch"
	batchmock "github.com/kunalpednekar/dumpster/internal/awsbatch/mock"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
	objectstoremock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
)

// testConfig returns a Config with a short poll interval so tests don't
// spend real wall-clock time waiting between polls.
func testConfig() Config {
	return Config{JobQueue: "test-queue", JobDefinition: "test-job-def", PollInterval: time.Millisecond}
}

func TestExtract_EmptyInputs_NoJobSubmitted(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	got, err := ex.Extract(context.Background(), nil, []entity.Type{"person"})
	if err != nil || got != nil {
		t.Fatalf("Extract(no chunks): got (%v, %v), want (nil, nil)", got, err)
	}
	got, err = ex.Extract(context.Background(), []*chunk.Chunk{{Text: "hi"}}, nil)
	if err != nil || got != nil {
		t.Fatalf("Extract(no types): got (%v, %v), want (nil, nil)", got, err)
	}
	if len(batchClient.SubmittedJobs) != 0 {
		t.Error("no job should be submitted when there is nothing to extract")
	}
}

func TestExtract_SubmitsJobAndMapsResponse(t *testing.T) {
	docID, kbID, userID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	c := &chunk.Chunk{ID: chunkID, DocumentID: docID, KBID: kbID, UserID: userID, Text: "Ada Lovelace wrote notes."}

	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	resultJSON, _ := json.Marshal(entitiesResponse{
		Entities: []entityResponse{{ChunkID: chunkID.String(), Type: "person", Text: "Ada Lovelace", Start: 0, End: 12, Score: 0.93}},
	})
	batchClient.SetLogs(jobID, "extract_entities: 1 chunks -> 1 entities in 0.29s\n"+resultMarker+string(resultJSON))

	got, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}

	if len(batchClient.SubmittedJobs) != 1 {
		t.Fatalf("expected exactly 1 job submitted, got %d", len(batchClient.SubmittedJobs))
	}
	submitted := batchClient.SubmittedJobs[0]
	if submitted.JobQueue != "test-queue" || submitted.JobDefinition != "test-job-def" {
		t.Errorf("job submitted against wrong queue/definition: %+v", submitted)
	}
	if _, ok := submitted.Environment["CHUNKS_URL"]; !ok {
		t.Error("expected CHUNKS_URL to be set in the job's environment")
	}

	if len(got) != 1 {
		t.Fatalf("got %d entities, want 1", len(got))
	}
	e := got[0]
	if e.DocumentID != docID || e.KBID != kbID || e.UserID != userID || e.ChunkID != chunkID {
		t.Errorf("entity not populated from source chunk: %+v", e)
	}
	if e.Type != "person" || e.Text != "Ada Lovelace" || e.Start != 0 || e.End != 12 || e.Score != 0.93 {
		t.Errorf("unexpected entity fields: %+v", e)
	}
}

func TestExtract_UploadsRequestBodyMatchingWireProtocol(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "Ada Lovelace wrote notes."}

	batchClient := batchmock.New()
	// Extract() deletes the input object as cleanup before returning (see
	// TestExtract_DeletesInputObjectAfterSuccess), so inspecting it via
	// the store after Extract() returns wouldn't find anything — a
	// recording wrapper captures what was actually uploaded at Put time.
	store := &recordingPutStore{Store: objectstoremock.New()}
	ex := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	batchClient.SetLogs(jobID, resultMarker+`{"entities":[]}`)

	if _, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"}); err != nil {
		t.Fatal(err)
	}

	// The uploaded input object must be exactly what
	// scripts/batch_entity_job.py expects to fetch from CHUNKS_URL —
	// extract_entities.py's stdin protocol shape.
	if store.lastPutBody == nil {
		t.Fatal("expected an input object to have been uploaded")
	}
	var uploaded entitiesRequest
	if err := json.Unmarshal(store.lastPutBody, &uploaded); err != nil {
		t.Fatalf("uploaded input isn't valid entitiesRequest JSON: %v", err)
	}
	if len(uploaded.Chunks) != 1 || uploaded.Chunks[0].ChunkID != c.ID.String() || uploaded.Chunks[0].Text != c.Text {
		t.Errorf("uploaded chunks don't match input: %+v", uploaded.Chunks)
	}
	if len(uploaded.AllowedTypes) != 1 || uploaded.AllowedTypes[0] != "person" {
		t.Errorf("uploaded allowed_types don't match input: %+v", uploaded.AllowedTypes)
	}
}

// recordingPutStore wraps an ObjectStore, capturing the bytes of the last
// Put() call so a test can inspect an upload's content even after the
// code under test has since deleted it as cleanup.
type recordingPutStore struct {
	*objectstoremock.Store
	lastPutBody []byte
}

func (s *recordingPutStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.lastPutBody = data
	return s.Store.Put(ctx, key, bytes.NewReader(data), size, contentType)
}

func TestExtract_DeletesInputObjectAfterSuccess(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hi"}
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	batchClient.SetLogs(jobID, resultMarker+`{"entities":[]}`)

	if _, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"}); err != nil {
		t.Fatal(err)
	}

	presignedURL := batchClient.SubmittedJobs[0].Environment["CHUNKS_URL"]
	key := presignedURL[len("http://mock/"):]
	if _, err := store.Get(context.Background(), key); err == nil {
		t.Error("expected the temp input object to be deleted after Extract completes")
	}
}

func TestExtract_UnknownChunkID_Skipped(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hello"}
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	resultJSON, _ := json.Marshal(entitiesResponse{
		Entities: []entityResponse{{ChunkID: uuid.New().String(), Type: "person", Text: "X", Start: 0, End: 1, Score: 0.5}},
	})
	batchClient.SetLogs(jobID, resultMarker+string(resultJSON))

	got, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected entities for unknown chunk ids to be skipped, got %d", len(got))
	}
}

func TestExtract_JobFailed_ReturnsErrorWithReason(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hi"}
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusFailed, Reason: "OutOfMemoryError"})

	_, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error for a failed job")
	}
	if got := err.Error(); !strings.Contains(got, "OutOfMemoryError") {
		t.Errorf("error should surface the failure reason, got: %v", got)
	}
}

func TestExtract_NoResultLineInLogs_ReturnsErrorWithLogContent(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hi"}
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	batchClient.SetLogs(jobID, "BATCH_ERROR: fetching CHUNKS_URL: connection refused")

	_, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error when no BATCH_RESULT line is present")
	}
	if got := err.Error(); !strings.Contains(got, "BATCH_ERROR") {
		t.Errorf("error should surface the job's actual log output, got: %v", got)
	}
}

func TestExtract_SubmitJobError_Propagates(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hi"}
	batchClient := batchmock.New()
	batchClient.SubmitErr = errors.New("insufficient capacity")
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	_, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error when SubmitJob fails")
	}
}

func TestExtract_ContextCancellation_StopsPolling(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hi"}
	batchClient := batchmock.New()
	store := objectstoremock.New()
	// Job never reaches a terminal state -- default StatusRunning forever
	// -- so the only way Extract returns is via context cancellation.
	ex := New(batchClient, store, testConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := ex.Extract(ctx, []*chunk.Chunk{c}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error when context is cancelled while polling")
	}
}

func TestNew_AppliesDefaultsForZeroValues(t *testing.T) {
	ex := New(batchmock.New(), objectstoremock.New(), Config{JobQueue: "q", JobDefinition: "d"})
	if ex.cfg.PollInterval <= 0 {
		t.Error("expected a non-zero default PollInterval")
	}
	if ex.cfg.PresignTTL <= 0 {
		t.Error("expected a non-zero default PresignTTL")
	}
}

// TestExtractBatches_SubmitsAllBatchesBeforeAwaitingAny is the core
// behavioral test for why ExtractBatches exists: every batch's AWS Batch
// job must be submitted before this call starts waiting on any of them,
// so a real compute environment scaling from zero sees the whole backlog
// at once instead of one job at a time. None of the 3 jobs here are ever
// given a terminal state, so awaiting blocks (and the context expires)
// without any of them completing -- if submission were still done lazily,
// one at a time as each prior job's await returned, this would submit
// exactly 1 job, not 3.
func TestExtractBatches_SubmitsAllBatchesBeforeAwaitingAny(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	docID := uuid.New()
	batches := make([][]*chunk.Chunk, 3)
	for i := range batches {
		batches[i] = []*chunk.Chunk{{ID: uuid.New(), DocumentID: docID, Text: "hi"}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	ex.ExtractBatches(ctx, batches, []entity.Type{"person"})

	if len(batchClient.SubmittedJobs) != 3 {
		t.Fatalf("expected all 3 batches submitted regardless of any batch completing, got %d", len(batchClient.SubmittedJobs))
	}
}

// TestExtractBatches_OneBatchFailingDoesNotBlockOthers verifies batches
// are awaited independently: one batch's job failing must not prevent a
// sibling batch's result from being reported, in either direction.
func TestExtractBatches_OneBatchFailingDoesNotBlockOthers(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	docID := uuid.New()
	failing := &chunk.Chunk{ID: uuid.New(), DocumentID: docID, Text: "a"}
	succeeding := &chunk.Chunk{ID: uuid.New(), DocumentID: docID, Text: "b"}
	batches := [][]*chunk.Chunk{{failing}, {succeeding}}

	// submit() is called in batch order (0, then 1), so the mock's
	// sequential job IDs are deterministic: "mock-job-1" for batch 0,
	// "mock-job-2" for batch 1.
	batchClient.SetState("mock-job-1", awsbatch.JobState{Status: awsbatch.StatusFailed, Reason: "boom"})
	batchClient.SetState("mock-job-2", awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	resultJSON, _ := json.Marshal(entitiesResponse{
		Entities: []entityResponse{{ChunkID: succeeding.ID.String(), Type: "person", Text: "B", Start: 0, End: 1, Score: 0.5}},
	})
	batchClient.SetLogs("mock-job-2", resultMarker+string(resultJSON))

	results := ex.ExtractBatches(context.Background(), batches, []entity.Type{"person"})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Err == nil {
		t.Error("expected batch 0 to report its own failure")
	}
	if results[1].Err != nil {
		t.Fatalf("batch 1 should have succeeded independently of batch 0's failure: %v", results[1].Err)
	}
	if len(results[1].Entities) != 1 || results[1].Entities[0].Text != "B" {
		t.Errorf("batch 1 entities: %+v", results[1].Entities)
	}
}

// TestExtractBatches_JobNamingIsTraceableToDocumentAndBatch is a
// regression test for a real debugging gap found live: a bare random UUID
// in the job name made a second, legitimate batch of the same document's
// extraction indistinguishable from an unrelated job when read from `aws
// batch list-jobs` output alone.
func TestExtractBatches_JobNamingIsTraceableToDocumentAndBatch(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	docID := uuid.New()
	batches := [][]*chunk.Chunk{
		{{ID: uuid.New(), DocumentID: docID, Text: "a"}},
		{{ID: uuid.New(), DocumentID: docID, Text: "b"}},
	}
	batchClient.SetState("mock-job-1", awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	batchClient.SetLogs("mock-job-1", resultMarker+`{"entities":[]}`)
	batchClient.SetState("mock-job-2", awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	batchClient.SetLogs("mock-job-2", resultMarker+`{"entities":[]}`)

	ex.ExtractBatches(context.Background(), batches, []entity.Type{"person"})

	if len(batchClient.SubmittedJobs) != 2 {
		t.Fatalf("expected 2 jobs submitted, got %d", len(batchClient.SubmittedJobs))
	}
	wantNames := []string{
		fmt.Sprintf("entity-extraction-%s-batch-0", docID),
		fmt.Sprintf("entity-extraction-%s-batch-1", docID),
	}
	for i, want := range wantNames {
		if got := batchClient.SubmittedJobs[i].JobName; got != want {
			t.Errorf("batch %d job name: got %q, want %q", i, got, want)
		}
	}
}

// TestExtractBatches_EmptyBatchesSkipped verifies an empty batch (no
// chunks) is reported as a zero-value result without submitting a job for
// it or blocking the others.
func TestExtractBatches_EmptyBatchesSkipped(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	docID := uuid.New()
	batches := [][]*chunk.Chunk{
		{},
		{{ID: uuid.New(), DocumentID: docID, Text: "a"}},
	}
	batchClient.SetState("mock-job-1", awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	batchClient.SetLogs("mock-job-1", resultMarker+`{"entities":[]}`)

	results := ex.ExtractBatches(context.Background(), batches, []entity.Type{"person"})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Err != nil || len(results[0].Entities) != 0 {
		t.Errorf("empty batch result: got %+v, want zero-value", results[0])
	}
	if len(batchClient.SubmittedJobs) != 1 {
		t.Fatalf("expected exactly 1 job submitted (for the non-empty batch), got %d", len(batchClient.SubmittedJobs))
	}
}

var _ entity.Extractor = (*Extractor)(nil)
