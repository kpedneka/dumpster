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

	"github.com/kunalpednekar/dumpster/internal/awsbatch"
	batchmock "github.com/kunalpednekar/dumpster/internal/awsbatch/mock"
	objectstoremock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
)

// testConfig returns a Config with a short poll interval so tests don't
// spend real wall-clock time waiting between polls.
func testConfig() Config {
	return Config{JobQueue: "test-queue", JobDefinition: "test-job-def", PollInterval: time.Millisecond}
}

// runEmbedWithResult calls e.Embed(ctx, texts) and, once the job it
// submits is visible, uploads result to the RESULT_URL that job was
// given and marks it succeeded -- simulating scripts/batch_embed_job.py
// actually running, in the correct order (result written, then job
// marked terminal) since Embed() reads the result only after
// waitForCompletion returns. The job's result key isn't known until
// Embed() generates it internally, so this can't be set up before
// calling Embed() the way entity/awsbatch's NextJobID()-based tests can.
func runEmbedWithResult(t *testing.T, e *Embedder, batchClient *batchmock.Client, store *objectstoremock.Store, texts []string, result embedResponse) ([][]float32, error) {
	t.Helper()
	type outcome struct {
		vecs [][]float32
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		vecs, err := e.Embed(context.Background(), texts)
		done <- outcome{vecs, err}
	}()

	deadline := time.Now().Add(2 * time.Second)
	var jobs []awsbatch.SubmitJobParams
	for time.Now().Before(deadline) {
		jobs = batchClient.Jobs()
		if len(jobs) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(jobs) == 0 {
		t.Fatal("job was never submitted")
	}
	submitted := jobs[len(jobs)-1]
	resultURL := submitted.Environment["RESULT_URL"]
	resultKey := strings.TrimPrefix(resultURL, "http://mock/")

	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := store.Put(context.Background(), resultKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		t.Fatalf("write result: %v", err)
	}
	batchClient.SetState(fmt.Sprintf("mock-job-%d", len(jobs)), awsbatch.JobState{Status: awsbatch.StatusSucceeded})

	select {
	case out := <-done:
		return out.vecs, out.err
	case <-time.After(2 * time.Second):
		t.Fatal("Embed did not return after the job was marked succeeded")
		return nil, nil
	}
}

func TestEmbed_EmptyInput_NoJobSubmitted(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	got, err := e.Embed(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("Embed(nil): got (%v, %v), want (nil, nil)", got, err)
	}
	if len(batchClient.SubmittedJobs) != 0 {
		t.Error("no job should be submitted for empty input")
	}
}

func TestEmbed_SubmitsJobAndReturnsVectorsInOrder(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	got, err := runEmbedWithResult(t, e, batchClient, store, []string{"first", "second"}, embedResponse{
		Embeddings: [][]float32{{1, 0}, {0, 1}},
		Dims:       2,
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
	if _, ok := submitted.Environment["TEXTS_URL"]; !ok {
		t.Error("expected TEXTS_URL to be set in the job's environment")
	}
	if _, ok := submitted.Environment["RESULT_URL"]; !ok {
		t.Error("expected RESULT_URL to be set in the job's environment")
	}

	want := [][]float32{{1, 0}, {0, 1}}
	if len(got) != len(want) || got[0][0] != 1 || got[1][1] != 1 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEmbed_UploadsRequestBodyMatchingWireProtocol(t *testing.T) {
	batchClient := batchmock.New()
	// Embed() deletes the input object as cleanup before returning (see
	// TestEmbed_DeletesInputAndResultObjectsAfterSuccess), so inspecting it
	// via the store after Embed() returns wouldn't find anything -- a
	// recording wrapper captures what was actually uploaded at Put time.
	store := &recordingPutStore{Store: objectstoremock.New()}
	e := New(batchClient, store, testConfig())

	if _, err := runEmbedWithResult(t, e, batchClient, store.Store, []string{"a passage"}, embedResponse{
		Embeddings: [][]float32{{0.1}},
		Dims:       1,
	}); err != nil {
		t.Fatal(err)
	}

	if store.lastInputBody == nil {
		t.Fatal("expected an input object to have been uploaded")
	}
	var uploaded embedRequest
	if err := json.Unmarshal(store.lastInputBody, &uploaded); err != nil {
		t.Fatalf("uploaded input isn't valid embedRequest JSON: %v", err)
	}
	if len(uploaded.Texts) != 1 || uploaded.Texts[0] != "a passage" {
		t.Errorf("uploaded texts don't match input: %+v", uploaded.Texts)
	}
}

func TestEmbed_DeletesInputAndResultObjectsAfterSuccess(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	if _, err := runEmbedWithResult(t, e, batchClient, store, []string{"a passage"}, embedResponse{
		Embeddings: [][]float32{{0.1}},
		Dims:       1,
	}); err != nil {
		t.Fatal(err)
	}

	submitted := batchClient.SubmittedJobs[0]
	for _, envKey := range []string{"TEXTS_URL", "RESULT_URL"} {
		presignedURL := submitted.Environment[envKey]
		key := strings.TrimPrefix(presignedURL, "http://mock/")
		if _, err := store.Get(context.Background(), key); err == nil {
			t.Errorf("expected the temp object for %s to be deleted after Embed completes", envKey)
		}
	}
}

func TestEmbed_MismatchedEmbeddingCount_ReturnsError(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	_, err := runEmbedWithResult(t, e, batchClient, store, []string{"one", "two"}, embedResponse{
		Embeddings: [][]float32{{0.1}},
		Dims:       1,
	})
	if err == nil {
		t.Fatal("expected an error when the job returns fewer embeddings than texts sent")
	}
}

func TestEmbed_JobFailed_ReturnsErrorWithReason(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusFailed, Reason: "out of memory"})

	_, err := e.Embed(context.Background(), []string{"text"})
	if err == nil || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("got %v, want an error mentioning the failure reason", err)
	}
}

// TestEmbed_JobSucceededButNeverWroteResult_ReturnsError covers a job that
// AWS Batch reports SUCCEEDED without ever PUTing to RESULT_URL (e.g. it
// crashed after computing embeddings but before the upload, or the upload
// itself silently failed) -- Get() on a key that was never written is the
// only signal of that failure mode, since there's no log line to fall
// back on any more.
func TestEmbed_JobSucceededButNeverWroteResult_ReturnsError(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusSucceeded})

	_, err := e.Embed(context.Background(), []string{"text"})
	if err == nil {
		t.Fatal("expected an error when the job succeeded without writing a result")
	}
}

func TestEmbed_SubmitJobError_Propagates(t *testing.T) {
	batchClient := batchmock.New()
	batchClient.SubmitErr = fmt.Errorf("batch unavailable")
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	_, err := e.Embed(context.Background(), []string{"text"})
	if err == nil || !strings.Contains(err.Error(), "batch unavailable") {
		t.Fatalf("got %v, want the submit error to propagate", err)
	}
}

func TestEmbed_ContextCancellation_StopsPolling(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	// Never set a terminal state -- the job stays "running" forever, so
	// only context cancellation can end the wait.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.Embed(ctx, []string{"text"})
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Embed did not return promptly after context cancellation")
	}
}

func TestNew_AppliesDefaultsForZeroValues(t *testing.T) {
	e := New(batchmock.New(), objectstoremock.New(), Config{JobQueue: "q", JobDefinition: "d"})
	if e.cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval default = %v, want 5s", e.cfg.PollInterval)
	}
	if e.cfg.PresignTTL != 15*time.Minute {
		t.Errorf("PresignTTL default = %v, want 15m", e.cfg.PresignTTL)
	}
}

func TestDims_Returns384(t *testing.T) {
	e := New(batchmock.New(), objectstoremock.New(), testConfig())
	if e.Dims() != 384 {
		t.Errorf("Dims() = %d, want 384", e.Dims())
	}
}

// recordingPutStore wraps an ObjectStore, capturing the bytes of the
// first Put() call (the job input -- the result upload in these tests
// goes through the embedded Store's Put directly, not this wrapper) so a
// test can inspect an upload's content even after the code under test has
// since deleted it as cleanup.
type recordingPutStore struct {
	*objectstoremock.Store
	lastInputBody []byte
}

func (s *recordingPutStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if s.lastInputBody == nil {
		s.lastInputBody = body
	}
	return s.Store.Put(ctx, key, bytes.NewReader(body), size, contentType)
}
