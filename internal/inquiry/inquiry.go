// Package inquiry defines the domain types and persistence boundary for an
// Inquiry: a persisted, ordered sequence of query/answer turns a researcher
// can leave and return to within one knowledge base. v1 supports exactly
// one Inquiry per (KBID, UserID) — see Repository.GetOrCreate — and each
// turn is answered independently: no conversation history is fed back into
// retrieval or generation (that would be a v2 change to
// internal/search.Service, not to this package).
package inquiry

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Repository methods when the requested record
// does not exist or belongs to a different tenant.
var ErrNotFound = errors.New("inquiry: not found")

// Role distinguishes a Message as the researcher's query or the system's
// generated answer.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Inquiry is the single persisted thread of turns for one knowledge base.
type Inquiry struct {
	ID     uuid.UUID
	KBID   uuid.UUID
	UserID uuid.UUID
	// Title is nullable dial room for a future auto-title-suggestion pass.
	// Unused by this package.
	Title     *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BoundingBox mirrors search.CitationBoundingBox. Defined here (rather than
// imported) so this package has no dependency on the retrieval/generation
// pipeline that produces the values it stores — inquiry is a pure
// persistence boundary.
type BoundingBox struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// Citation mirrors search.Citation, with one deliberate difference: FileName
// replaces Text. The live search response resolves a file name via a
// request-time lookup (safe, since the document still exists at answer
// time), but a persisted Inquiry message is read back long after that —
// possibly after the source document was renamed or deleted. FileName is
// snapshotted at persist time so a historical citation still identifies its
// source even then; nothing displays raw chunk text anymore (see the
// grouped-citation frontend redesign), so there's no resilience lost by not
// snapshotting Text too.
type Citation struct {
	Number      int          `json:"number"`
	DocumentID  uuid.UUID    `json:"document_id"`
	ChunkID     uuid.UUID    `json:"chunk_id"`
	CharStart   int          `json:"char_start"`
	CharEnd     int          `json:"char_end"`
	FileName    string       `json:"file_name"`
	PageNumber  *int         `json:"page_number,omitempty"`
	BoundingBox *BoundingBox `json:"bounding_box,omitempty"`
}

// RetrievedDocument names one file in a message's ranked retrieval set.
// FileName is snapshotted at persist time for the same reason as
// Citation.FileName: a bare DocumentID resolved via a live lookup goes
// blank the moment the source document is renamed or deleted, which a
// historical Inquiry message needs to survive.
type RetrievedDocument struct {
	DocumentID uuid.UUID `json:"document_id"`
	FileName   string    `json:"file_name"`
}

// Message is one turn in an Inquiry. A user-role message carries only
// Content (the query text); an assistant-role message additionally carries
// Citations and RetrievedDocuments, mirroring search.Result.
type Message struct {
	ID                 uuid.UUID
	InquiryID          uuid.UUID
	KBID               uuid.UUID
	UserID             uuid.UUID
	Role               Role
	Content            string
	Citations          []Citation
	RetrievedDocuments []RetrievedDocument
	// SupersedesMessageID is set on a re-evaluation: a new assistant message
	// produced by re-running an earlier message's query against the current
	// KB state. Re-evaluating appends rather than overwrites, since seeing
	// that an answer changed is often as valuable as the new answer itself.
	// Nil for every ordinary turn.
	SupersedesMessageID *uuid.UUID
	// Ordinal is assigned by Repository.AppendMessage; callers must not set
	// it when constructing a Message to append.
	Ordinal   int
	CreatedAt time.Time
}

// Repository is the persistence boundary for Inquiry and Message records.
// Every method is tenant-scoped: userID is always a filter, never optional.
type Repository interface {
	// GetOrCreate returns the single Inquiry for (userID, kbID), creating
	// it on first use. Safe for concurrent callers: creation races resolve
	// via the inquiries_kb_user_key unique constraint, not application
	// locking.
	GetOrCreate(ctx context.Context, userID, kbID uuid.UUID) (*Inquiry, error)
	// Get returns the Inquiry for (userID, kbID) without creating one; it
	// returns ErrNotFound if none exists yet.
	Get(ctx context.Context, userID, kbID uuid.UUID) (*Inquiry, error)
	// AppendMessage adds msg as the next turn in msg.InquiryID, assigning
	// Ordinal as one past the current highest ordinal for that inquiry.
	// msg.UserID must be set to userID; ID, Ordinal, and CreatedAt are
	// assigned by the repository. msg.SupersedesMessageID, if set, is
	// persisted as given — the repository does not validate that it points
	// to an assistant-role message in the same inquiry; callers (the API
	// layer) own that check.
	AppendMessage(ctx context.Context, userID uuid.UUID, msg *Message) (*Message, error)
	// ListMessages returns every message in inquiryID, ordered by Ordinal.
	ListMessages(ctx context.Context, userID, inquiryID uuid.UUID) ([]*Message, error)
}
