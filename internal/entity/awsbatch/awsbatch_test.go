package awsbatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

var _ entity.Extractor = (*Extractor)(nil)
