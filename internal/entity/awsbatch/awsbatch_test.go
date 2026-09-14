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

// waitForJobs polls batchClient until at least n jobs have been submitted,
// returning a snapshot of them. Needed by any test that must observe a
// submission while the call that made it (Extract/ExtractBatches, which
// submit and then block awaiting the result) is still in flight.
func waitForJobs(t *testing.T, batchClient *batchmock.Client, n int) []awsbatch.SubmitJobParams {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var jobs []awsbatch.SubmitJobParams
	for time.Now().Before(deadline) {
		jobs = batchClient.Jobs()
		if len(jobs) >= n {
			return jobs
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected %d jobs submitted, got %d", n, len(jobs))
	return nil
}

// writeResult uploads result to the S3 key encoded in a submitted job's
// RESULT_URL -- simulating scripts/batch_entity_job.py's own PUT. The
// result key isn't known ahead of time (submit() generates it internally
// with a random UUID suffix), so this can only run after the job has
// actually been submitted, unlike the old deterministic-job-ID-keyed
// SetLogs() pattern.
func writeResult(t *testing.T, store *objectstoremock.Store, resultURL string, result entitiesResponse) {
	t.Helper()
	resultKey := strings.TrimPrefix(resultURL, "http://mock/")
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := store.Put(context.Background(), resultKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		t.Fatalf("write result: %v", err)
	}
}

// runExtractWithResult calls ex.Extract(ctx, chunks, allowedTypes) and,
// once the job it submits is visible, uploads result to that job's
// RESULT_URL and marks it succeeded -- simulating
// scripts/batch_entity_job.py actually running, in the correct order
// (result written, then job marked terminal) since Extract reads the
// result only after waitForCompletion returns.
func runExtractWithResult(t *testing.T, ex *Extractor, batchClient *batchmock.Client, store *objectstoremock.Store, chunks []*chunk.Chunk, allowedTypes []entity.Type, result entitiesResponse) ([]*entity.Entity, error) {
	t.Helper()
	type outcome struct {
		entities []*entity.Entity
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		entities, err := ex.Extract(context.Background(), chunks, allowedTypes)
		done <- outcome{entities, err}
	}()

	jobs := waitForJobs(t, batchClient, 1)
	submitted := jobs[len(jobs)-1]
	writeResult(t, store, submitted.Environment["RESULT_URL"], result)
	batchClient.SetState(fmt.Sprintf("mock-job-%d", len(jobs)), awsbatch.JobState{Status: awsbatch.StatusSucceeded})

	select {
	case out := <-done:
		return out.entities, out.err
	case <-time.After(2 * time.Second):
		t.Fatal("Extract did not return after the job was marked succeeded")
		return nil, nil
	}
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

	got, err := runExtractWithResult(t, ex, batchClient, store, []*chunk.Chunk{c}, []entity.Type{"person"}, entitiesResponse{
		Entities: []entityResponse{{ChunkID: chunkID.String(), Type: "person", Text: "Ada Lovelace", Start: 0, End: 12, Score: 0.93}},
	})
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
	if _, ok := submitted.Environment["RESULT_URL"]; !ok {
		t.Error("expected RESULT_URL to be set in the job's environment")
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

// recordingPutStore wraps an ObjectStore, capturing the bytes of the first
// Put() call (the job input -- the result upload in these tests goes
// through the embedded Store's Put directly, not this wrapper) so a test
// can inspect an upload's content even after the code under test has since
// deleted it as cleanup.
type recordingPutStore struct {
	*objectstoremock.Store
	lastPutBody []byte
}

func (s *recordingPutStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if s.lastPutBody == nil {
		s.lastPutBody = data
	}
	return s.Store.Put(ctx, key, bytes.NewReader(data), size, contentType)
}

func TestExtract_UploadsRequestBodyMatchingWireProtocol(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "Ada Lovelace wrote notes."}

	batchClient := batchmock.New()
	// Extract() deletes the input object as cleanup before returning (see
	// TestExtract_DeletesInputAndResultObjectsAfterSuccess), so inspecting
	// it via the store after Extract() returns wouldn't find anything — a
	// recording wrapper captures what was actually uploaded at Put time.
	store := &recordingPutStore{Store: objectstoremock.New()}
	ex := New(batchClient, store, testConfig())

	if _, err := runExtractWithResult(t, ex, batchClient, store.Store, []*chunk.Chunk{c}, []entity.Type{"person"}, entitiesResponse{}); err != nil {
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

func TestExtract_DeletesInputAndResultObjectsAfterSuccess(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hi"}
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	if _, err := runExtractWithResult(t, ex, batchClient, store, []*chunk.Chunk{c}, []entity.Type{"person"}, entitiesResponse{}); err != nil {
		t.Fatal(err)
	}

	submitted := batchClient.SubmittedJobs[0]
	for _, envKey := range []string{"CHUNKS_URL", "RESULT_URL"} {
		presignedURL := submitted.Environment[envKey]
		key := strings.TrimPrefix(presignedURL, "http://mock/")
		if _, err := store.Get(context.Background(), key); err == nil {
			t.Errorf("expected the temp object for %s to be deleted after Extract completes", envKey)
		}
	}
}

func TestExtract_UnknownChunkID_Skipped(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hello"}
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	got, err := runExtractWithResult(t, ex, batchClient, store, []*chunk.Chunk{c}, []entity.Type{"person"}, entitiesResponse{
		Entities: []entityResponse{{ChunkID: uuid.New().String(), Type: "person", Text: "X", Start: 0, End: 1, Score: 0.5}},
	})
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

// TestExtract_JobSucceededButNeverWroteResult_ReturnsError covers a job
// that AWS Batch reports SUCCEEDED without ever PUTing to RESULT_URL (e.g.
// it crashed after computing entities but before the upload, or the upload
// itself silently failed) -- Get() on a key that was never written is the
// only signal of that failure mode, since there's no log line to fall back
// on any more.
func TestExtract_JobSucceededButNeverWroteResult_ReturnsError(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hi"}
	batchClient := batchmock.New()
	store := objectstoremock.New()
	ex := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusSucceeded})

	_, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected an error when the job succeeded without writing a result")
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

	done := make(chan []entity.BatchResult, 1)
	go func() {
		done <- ex.ExtractBatches(context.Background(), batches, []entity.Type{"person"})
	}()

	// submit() is called in batch order (0, then 1), so the mock's
	// sequential job IDs are deterministic: "mock-job-1" for batch 0,
	// "mock-job-2" for batch 1.
	jobs := waitForJobs(t, batchClient, 2)
	batchClient.SetState("mock-job-1", awsbatch.JobState{Status: awsbatch.StatusFailed, Reason: "boom"})
	writeResult(t, store, jobs[1].Environment["RESULT_URL"], entitiesResponse{
		Entities: []entityResponse{{ChunkID: succeeding.ID.String(), Type: "person", Text: "B", Start: 0, End: 1, Score: 0.5}},
	})
	batchClient.SetState("mock-job-2", awsbatch.JobState{Status: awsbatch.StatusSucceeded})

	var results []entity.BatchResult
	select {
	case results = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExtractBatches did not return after both jobs reached a terminal state")
	}

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

	done := make(chan []entity.BatchResult, 1)
	go func() {
		done <- ex.ExtractBatches(context.Background(), batches, []entity.Type{"person"})
	}()

	jobs := waitForJobs(t, batchClient, 2)
	for i, job := range jobs {
		writeResult(t, store, job.Environment["RESULT_URL"], entitiesResponse{})
		batchClient.SetState(fmt.Sprintf("mock-job-%d", i+1), awsbatch.JobState{Status: awsbatch.StatusSucceeded})
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExtractBatches did not return after both jobs reached a terminal state")
	}

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

	done := make(chan []entity.BatchResult, 1)
	go func() {
		done <- ex.ExtractBatches(context.Background(), batches, []entity.Type{"person"})
	}()

	jobs := waitForJobs(t, batchClient, 1)
	writeResult(t, store, jobs[0].Environment["RESULT_URL"], entitiesResponse{})
	batchClient.SetState("mock-job-1", awsbatch.JobState{Status: awsbatch.StatusSucceeded})

	var results []entity.BatchResult
	select {
	case results = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExtractBatches did not return after the job reached a terminal state")
	}

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
