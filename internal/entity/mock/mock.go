// Package mock provides a test double for entity.Extractor, used everywhere
// except the real spaCy+GLiNER adapter (internal/entity/gliner).
package mock

import (
	"context"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

// Extractor is a test double for entity.Extractor.
type Extractor struct {
	ExtractFn func(ctx context.Context, chunks []*chunk.Chunk, allowedTypes []entity.Type) ([]*entity.Entity, error)
}

// New returns an Extractor whose ExtractFn always returns an empty result.
func New() *Extractor {
	return &Extractor{
		ExtractFn: func(_ context.Context, _ []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
			return nil, nil
		},
	}
}

// NewFixed returns an Extractor whose ExtractFn always returns entities,
// regardless of the chunks/types passed in. Useful when a test only cares
// that extraction results get persisted, not what drove them.
func NewFixed(entities []*entity.Entity) *Extractor {
	return &Extractor{
		ExtractFn: func(_ context.Context, _ []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
			return entities, nil
		},
	}
}

// NewError returns an Extractor whose ExtractFn always fails with err.
func NewError(err error) *Extractor {
	return &Extractor{
		ExtractFn: func(_ context.Context, _ []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
			return nil, err
		},
	}
}

// Extract delegates to ExtractFn.
func (m *Extractor) Extract(ctx context.Context, chunks []*chunk.Chunk, allowedTypes []entity.Type) ([]*entity.Entity, error) {
	return m.ExtractFn(ctx, chunks, allowedTypes)
}

var _ entity.Extractor = (*Extractor)(nil)
