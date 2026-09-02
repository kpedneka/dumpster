// Package stats tracks site-wide usage counters shown on the public
// GET /stats endpoint: total documents indexed, average document size,
// total queries executed, average query duration, and total sessions
// created/swept.
package stats

import "context"

// Snapshot is the current site-wide totals. Averages are 0 when their
// corresponding count is 0, never NaN.
type Snapshot struct {
	DocumentsIndexed     int64
	AvgDocumentSizeBytes float64
	QueriesExecuted      int64
	AvgQueryDurationMs   float64
	SessionsCreated      int64
	SessionsSwept        int64
}

// Repository records usage events and reports the running site-wide
// totals. Deliberately outside the multi-tenant model (no user_id, no
// per-tenant filtering): these numbers answer "how much has this
// deployment done in total," a question with one answer for the whole
// app, not one per session. They're also deliberately not derived by
// scanning document/inquiry tables at read time — those rows disappear
// when a session's short-lived (2-24h) account sweeps, so a point-in-time
// scan would silently shrink over time instead of staying a durable,
// monotonically increasing total.
type Repository interface {
	// RecordDocumentIndexed increments the documents-indexed counter and
	// adds sizeBytes to the running size total. Called once a document
	// successfully reaches the indexed state, regardless of which
	// ingestion path (DocumentHandler or RegionClassificationHandler) got
	// it there.
	RecordDocumentIndexed(ctx context.Context, sizeBytes int64) error
	// RecordQueryExecuted increments the queries-executed counter and adds
	// durationMs to the running duration total. Called once per completed
	// search request, including re-evaluations — each one re-runs
	// retrieval and generation in full, so it counts as its own query.
	RecordQueryExecuted(ctx context.Context, durationMs int64) error
	// RecordSessionCreated increments the sessions-created counter. Called
	// once per anonymous session minted (see auth.Middleware) — not once
	// per request, since most requests resolve an existing session rather
	// than creating a new one.
	RecordSessionCreated(ctx context.Context) error
	// RecordSessionsSwept increments the sessions-swept counter by count.
	// Called once per sweep run (see account.Sweep) with that run's
	// SweepResult.Deleted, not once per session, so a sweep that deletes
	// many sessions in one pass is one write, not many.
	RecordSessionsSwept(ctx context.Context, count int) error
	// Get returns the current site-wide totals.
	Get(ctx context.Context) (Snapshot, error)
}
