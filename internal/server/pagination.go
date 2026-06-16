package server

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/kb"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// KBPage is the paginated list response for knowledge bases.
type KBPage struct {
	Items      []*kb.KnowledgeBase `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

// DocumentPage is the paginated list response for documents.
type DocumentPage struct {
	Items      []*document.Document `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// parsePagination reads ?limit= and ?after= from the request.
// limit is clamped to [1, maxPageLimit]; after is a UUID cursor (zero UUID means start from beginning).
func parsePagination(r *http.Request) (limit int, after uuid.UUID) {
	limit = defaultPageLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}

	after = uuid.Nil
	if s := r.URL.Query().Get("after"); s != "" {
		if id, err := uuid.Parse(s); err == nil {
			after = id
		}
	}
	return limit, after
}

// paginateKBs sorts the given knowledge bases by created_at (then id) and
// returns a KBPage with at most limit items starting after the cursor item.
func paginateKBs(items []*kb.KnowledgeBase, limit int, after uuid.UUID) KBPage {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID.String() < items[j].ID.String()
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})

	start := kbCursorPos(items, after)
	end := start + limit
	hasMore := end < len(items)
	if end > len(items) {
		end = len(items)
	}

	slice := items[start:end]
	if slice == nil {
		slice = []*kb.KnowledgeBase{}
	}

	p := KBPage{Items: slice}
	if hasMore && len(slice) > 0 {
		p.NextCursor = slice[len(slice)-1].ID.String()
	}
	return p
}

// paginateDocs sorts the given documents by created_at (then id) and returns
// a DocumentPage with at most limit items starting after the cursor item.
func paginateDocs(items []*document.Document, limit int, after uuid.UUID) DocumentPage {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID.String() < items[j].ID.String()
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})

	start := docCursorPos(items, after)
	end := start + limit
	hasMore := end < len(items)
	if end > len(items) {
		end = len(items)
	}

	slice := items[start:end]
	if slice == nil {
		slice = []*document.Document{}
	}

	p := DocumentPage{Items: slice}
	if hasMore && len(slice) > 0 {
		p.NextCursor = slice[len(slice)-1].ID.String()
	}
	return p
}

// kbCursorPos returns the slice index immediately after the item whose ID
// matches after. Returns 0 if after is the zero UUID or not found.
func kbCursorPos(items []*kb.KnowledgeBase, after uuid.UUID) int {
	if after == uuid.Nil {
		return 0
	}
	for i, k := range items {
		if k.ID == after {
			return i + 1
		}
	}
	return 0
}

// docCursorPos returns the slice index immediately after the item whose ID
// matches after. Returns 0 if after is the zero UUID or not found.
func docCursorPos(items []*document.Document, after uuid.UUID) int {
	if after == uuid.Nil {
		return 0
	}
	for i, d := range items {
		if d.ID == after {
			return i + 1
		}
	}
	return 0
}
