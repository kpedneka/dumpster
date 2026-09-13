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
	"github.com/kunalpednekar/dumpster/internal/manifest/layout"
	objectstoremock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
)

// testConfig returns a Config with a short poll interval so tests don't
// spend real wall-clock time waiting between polls.
func testConfig() Config {
	return Config{JobQueue: "test-queue", JobDefinition: "test-job-def", PollInterval: time.Millisecond}
}

// runExtractWithResult calls e.ExtractRegions(ctx, pdfBytes) and, once the
// job it submits is visible, uploads result to the RESULT_URL that job was
// given and marks it succeeded -- simulating scripts/batch_regions_job.py
// actually running. Mirrors internal/llm/awsbatch's runEmbedWithResult
// exactly, for the same reason: the result key isn't known until
// ExtractRegions() generates it internally.
func runExtractWithResult(t *testing.T, e *Extractor, batchClient *batchmock.Client, store *objectstoremock.Store, pdfBytes []byte, result regionsResponse) ([]*layout.RawRegion, error) {
	t.Helper()
	type outcome struct {
		regions []*layout.RawRegion
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		regions, err := e.ExtractRegions(context.Background(), pdfBytes)
		done <- outcome{regions, err}
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
		return out.regions, out.err
	case <-time.After(2 * time.Second):
		t.Fatal("ExtractRegions did not return after the job was marked succeeded")
		return nil, nil
	}
}

func TestExtractRegions_EmptyInput_NoJobSubmitted(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	got, err := e.ExtractRegions(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("ExtractRegions(nil): got (%v, %v), want (nil, nil)", got, err)
	}
	if len(batchClient.SubmittedJobs) != 0 {
		t.Error("no job should be submitted for empty input")
	}
}

func TestExtractRegions_SubmitsJobAndReturnsRegionsInOrder(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	got, err := runExtractWithResult(t, e, batchClient, store, []byte("%PDF-1.4 fake"), regionsResponse{
		Regions: []rawRegionJSON{
			{RegionType: "native_text", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 0.5}, Text: "first"},
			{RegionType: "native_table", PageNumber: 1, BoundingBox: [4]float64{0, 0.5, 1, 1}, Text: "second"},
		},
		PeakRSSKB: 12345,
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
	if _, ok := submitted.Environment["PDF_URL"]; !ok {
		t.Error("expected PDF_URL to be set in the job's environment")
	}
	if _, ok := submitted.Environment["RESULT_URL"]; !ok {
		t.Error("expected RESULT_URL to be set in the job's environment")
	}

	if len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Errorf("got %+v, want regions in order [first, second]", got)
	}
	if got[0].RegionType != "native_text" || got[1].RegionType != "native_table" {
		t.Errorf("region types not carried through: %+v", got)
	}
}

func TestExtractRegions_UploadsRawPDFBytesNotWrapped(t *testing.T) {
	batchClient := batchmock.New()
	// ExtractRegions deletes the input object as cleanup before returning,
	// so inspecting it via the store after the call returns wouldn't find
	// anything -- a recording wrapper captures what was actually uploaded
	// at Put time, same pattern internal/llm/awsbatch's equivalent test uses.
	store := &recordingPutStore{Store: objectstoremock.New()}
	e := New(batchClient, store, testConfig())

	pdfBytes := []byte("%PDF-1.4 real bytes, not base64 or JSON-wrapped")
	if _, err := runExtractWithResult(t, e, batchClient, store.Store, pdfBytes, regionsResponse{}); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(store.lastInputBody, pdfBytes) {
		t.Errorf("uploaded input = %q, want the raw PDF bytes unmodified: %q", store.lastInputBody, pdfBytes)
	}
}

func TestExtractRegions_DeletesInputAndResultObjectsAfterSuccess(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	if _, err := runExtractWithResult(t, e, batchClient, store, []byte("%PDF-1.4"), regionsResponse{}); err != nil {
		t.Fatal(err)
	}

	submitted := batchClient.SubmittedJobs[0]
	for _, envKey := range []string{"PDF_URL", "RESULT_URL"} {
		presignedURL := submitted.Environment[envKey]
		key := strings.TrimPrefix(presignedURL, "http://mock/")
		if _, err := store.Get(context.Background(), key); err == nil {
			t.Errorf("expected the temp object for %s to be deleted after ExtractRegions completes", envKey)
		}
	}
}

func TestExtractRegions_JobFailed_ReturnsErrorWithReason(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusFailed, Reason: "region extraction exceeded the 1024MB memory limit"})

	_, err := e.ExtractRegions(context.Background(), []byte("%PDF-1.4"))
	if err == nil || !strings.Contains(err.Error(), "memory limit") {
		t.Fatalf("got %v, want an error mentioning the failure reason", err)
	}
}

func TestExtractRegions_JobSucceededButNeverWroteResult_ReturnsError(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	jobID := batchClient.NextJobID()
	batchClient.SetState(jobID, awsbatch.JobState{Status: awsbatch.StatusSucceeded})

	_, err := e.ExtractRegions(context.Background(), []byte("%PDF-1.4"))
	if err == nil {
		t.Fatal("expected an error when the job succeeded without writing a result")
	}
}

func TestExtractRegions_SubmitJobError_Propagates(t *testing.T) {
	batchClient := batchmock.New()
	batchClient.SubmitErr = fmt.Errorf("batch unavailable")
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	_, err := e.ExtractRegions(context.Background(), []byte("%PDF-1.4"))
	if err == nil || !strings.Contains(err.Error(), "batch unavailable") {
		t.Fatalf("got %v, want the submit error to propagate", err)
	}
}

func TestExtractRegions_ContextCancellation_StopsPolling(t *testing.T) {
	batchClient := batchmock.New()
	store := objectstoremock.New()
	e := New(batchClient, store, testConfig())

	// Never set a terminal state -- the job stays "running" forever, so
	// only context cancellation can end the wait.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.ExtractRegions(ctx, []byte("%PDF-1.4"))
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExtractRegions did not return promptly after context cancellation")
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

// recordingPutStore wraps an ObjectStore, capturing the bytes of the
// first Put() call (the job input -- the result upload in these tests
// goes through the embedded Store's Put directly, not this wrapper) so a
// test can inspect an upload's content even after the code under test has
// since deleted it as cleanup. Same helper internal/llm/awsbatch's test
// suite defines independently -- not shared, since duplicating ~10 lines
// here isn't worth an inter-package test dependency.
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
