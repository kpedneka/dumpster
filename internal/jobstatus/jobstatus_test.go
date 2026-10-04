package jobstatus_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

func testKey() jobstatus.Key {
	return jobstatus.Key{UserID: uuid.New(), DocumentID: uuid.New(), JobType: queue.JobTypeDocumentIndexing}
}

func TestStatusActive(t *testing.T) {
	cases := map[jobstatus.Status]bool{
		jobstatus.StatusPending:    true,
		jobstatus.StatusProcessing: true,
		jobstatus.StatusSucceeded:  false,
		jobstatus.StatusFailed:     false,
	}
	for status, want := range cases {
		if got := status.Active(); got != want {
			t.Errorf("%s.Active() = %v, want %v", status, got, want)
		}
	}
}

func TestEnqueue_NoExistingRecord_StartsAttemptOne(t *testing.T) {
	key := testKey()
	next, res := jobstatus.Enqueue(nil, key)

	if !res.Started || res.Attempt != 1 {
		t.Errorf("result = %+v, want {Attempt:1 Started:true}", res)
	}
	want := jobstatus.Record{Key: key, Status: jobstatus.StatusPending, Attempt: 1}
	if next != want {
		t.Errorf("next = %+v, want %+v", next, want)
	}
}

func TestEnqueue_ActiveRecord_IsANoOp(t *testing.T) {
	for _, status := range []jobstatus.Status{jobstatus.StatusPending, jobstatus.StatusProcessing} {
		t.Run(string(status), func(t *testing.T) {
			cur := jobstatus.Record{Key: testKey(), Status: status, Phase: "embedding", Attempt: 2}
			next, res := jobstatus.Enqueue(&cur, cur.Key)

			if res.Started || res.Attempt != 2 {
				t.Errorf("result = %+v, want {Attempt:2 Started:false}", res)
			}
			if next != cur {
				t.Errorf("next = %+v, want unchanged %+v", next, cur)
			}
		})
	}
}

func TestEnqueue_FinishedRecord_StartsNextAttemptAndClearsState(t *testing.T) {
	for _, status := range []jobstatus.Status{jobstatus.StatusSucceeded, jobstatus.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			cur := jobstatus.Record{Key: testKey(), Status: status, Phase: "embedding", Attempt: 2, LastError: "boom"}
			next, res := jobstatus.Enqueue(&cur, cur.Key)

			if !res.Started || res.Attempt != 3 {
				t.Errorf("result = %+v, want {Attempt:3 Started:true}", res)
			}
			want := jobstatus.Record{Key: cur.Key, Status: jobstatus.StatusPending, Attempt: 3}
			if next != want {
				t.Errorf("next = %+v, want %+v", next, want)
			}
		})
	}
}

func TestTransition(t *testing.T) {
	key := testKey()
	rec := func(status jobstatus.Status, phase, lastErr string) jobstatus.Record {
		return jobstatus.Record{Key: key, Status: status, Phase: phase, Attempt: 1, LastError: lastErr}
	}

	cases := []struct {
		name        string
		cur         jobstatus.Record
		attempt     int
		to          jobstatus.Status
		phase       string
		reason      string
		want        jobstatus.Record
		wantChanged bool
		wantErr     error
	}{
		{
			name: "pending to processing", cur: rec(jobstatus.StatusPending, "", ""), attempt: 1,
			to: jobstatus.StatusProcessing, want: rec(jobstatus.StatusProcessing, "", ""), wantChanged: true,
		},
		{
			name: "pending to processing with phase", cur: rec(jobstatus.StatusPending, "", ""), attempt: 1,
			to: jobstatus.StatusProcessing, phase: "embedding", want: rec(jobstatus.StatusProcessing, "embedding", ""), wantChanged: true,
		},
		{
			name: "processing again without phase keeps phase", cur: rec(jobstatus.StatusProcessing, "embedding", ""), attempt: 1,
			to: jobstatus.StatusProcessing, want: rec(jobstatus.StatusProcessing, "embedding", ""), wantChanged: false,
		},
		{
			name: "processing to new phase", cur: rec(jobstatus.StatusProcessing, "regions", ""), attempt: 1,
			to: jobstatus.StatusProcessing, phase: "embedding", want: rec(jobstatus.StatusProcessing, "embedding", ""), wantChanged: true,
		},
		{
			name: "processing to succeeded clears phase", cur: rec(jobstatus.StatusProcessing, "embedding", ""), attempt: 1,
			to: jobstatus.StatusSucceeded, want: rec(jobstatus.StatusSucceeded, "", ""), wantChanged: true,
		},
		{
			name: "pending to failed records reason", cur: rec(jobstatus.StatusPending, "", ""), attempt: 1,
			to: jobstatus.StatusFailed, reason: "parse error", want: rec(jobstatus.StatusFailed, "", "parse error"), wantChanged: true,
		},
		{
			name: "processing to failed keeps phase for diagnosis", cur: rec(jobstatus.StatusProcessing, "embedding", ""), attempt: 1,
			to: jobstatus.StatusFailed, reason: "boom", want: rec(jobstatus.StatusFailed, "embedding", "boom"), wantChanged: true,
		},
		{
			name: "succeeded again is idempotent", cur: rec(jobstatus.StatusSucceeded, "", ""), attempt: 1,
			to: jobstatus.StatusSucceeded, want: rec(jobstatus.StatusSucceeded, "", ""), wantChanged: false,
		},
		{
			name: "failed again keeps first reason", cur: rec(jobstatus.StatusFailed, "", "first"), attempt: 1,
			to: jobstatus.StatusFailed, reason: "second", want: rec(jobstatus.StatusFailed, "", "first"), wantChanged: false,
		},
		{
			name: "failed to succeeded rejected", cur: rec(jobstatus.StatusFailed, "", "boom"), attempt: 1,
			to: jobstatus.StatusSucceeded, want: rec(jobstatus.StatusFailed, "", "boom"), wantErr: jobstatus.ErrNotActive,
		},
		{
			name: "succeeded to failed rejected", cur: rec(jobstatus.StatusSucceeded, "", ""), attempt: 1,
			to: jobstatus.StatusFailed, reason: "late", want: rec(jobstatus.StatusSucceeded, "", ""), wantErr: jobstatus.ErrNotActive,
		},
		{
			name: "succeeded to processing rejected", cur: rec(jobstatus.StatusSucceeded, "", ""), attempt: 1,
			to: jobstatus.StatusProcessing, want: rec(jobstatus.StatusSucceeded, "", ""), wantErr: jobstatus.ErrNotActive,
		},
		{
			name: "older attempt rejected", cur: rec(jobstatus.StatusProcessing, "", ""), attempt: 0,
			to: jobstatus.StatusSucceeded, want: rec(jobstatus.StatusProcessing, "", ""), wantErr: jobstatus.ErrAttemptSuperseded,
		},
		{
			name: "pending is not a transition target", cur: rec(jobstatus.StatusProcessing, "", ""), attempt: 1,
			to: jobstatus.StatusPending, want: rec(jobstatus.StatusProcessing, "", ""), wantErr: jobstatus.ErrInvalidTransition,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := jobstatus.Transition(tc.cur, tc.attempt, tc.to, tc.phase, tc.reason)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("record = %+v, want %+v", got, tc.want)
			}
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
		})
	}
}
