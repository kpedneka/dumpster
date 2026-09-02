// Package memory provides an in-memory queue.Publisher for use in tests.
package memory

import (
	"context"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Publisher records published events in memory.
type Publisher struct {
	mu                    sync.Mutex
	events                []queue.DocumentUploaded
	entityExtractions     []queue.EntityExtractionRequested
	edgeExtractions       []queue.EdgeExtractionRequested
	regionClassifications []queue.RegionClassificationRequested
	canonicalizations     []queue.CanonicalizationRequested
}

// New returns an empty in-memory Publisher.
func New() *Publisher { return &Publisher{} }

// PublishDocumentUploaded appends evt to the recorded event list.
func (p *Publisher) PublishDocumentUploaded(_ context.Context, evt queue.DocumentUploaded) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, evt)
	return nil
}

// PublishEntityExtraction appends evt to the recorded entity-extraction event list.
func (p *Publisher) PublishEntityExtraction(_ context.Context, evt queue.EntityExtractionRequested) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entityExtractions = append(p.entityExtractions, evt)
	return nil
}

// Events returns a snapshot of all published DocumentUploaded events.
func (p *Publisher) Events() []queue.DocumentUploaded {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]queue.DocumentUploaded, len(p.events))
	copy(cp, p.events)
	return cp
}

// EntityExtractionEvents returns a snapshot of all published
// EntityExtractionRequested events.
func (p *Publisher) EntityExtractionEvents() []queue.EntityExtractionRequested {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]queue.EntityExtractionRequested, len(p.entityExtractions))
	copy(cp, p.entityExtractions)
	return cp
}

// PublishEdgeExtraction appends evt to the recorded edge-extraction event list.
func (p *Publisher) PublishEdgeExtraction(_ context.Context, evt queue.EdgeExtractionRequested) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.edgeExtractions = append(p.edgeExtractions, evt)
	return nil
}

// EdgeExtractionEvents returns a snapshot of all published
// EdgeExtractionRequested events.
func (p *Publisher) EdgeExtractionEvents() []queue.EdgeExtractionRequested {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]queue.EdgeExtractionRequested, len(p.edgeExtractions))
	copy(cp, p.edgeExtractions)
	return cp
}

// PublishRegionClassification appends evt to the recorded region-classification event list.
func (p *Publisher) PublishRegionClassification(_ context.Context, evt queue.RegionClassificationRequested) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.regionClassifications = append(p.regionClassifications, evt)
	return nil
}

// RegionClassificationEvents returns a snapshot of all published
// RegionClassificationRequested events.
func (p *Publisher) RegionClassificationEvents() []queue.RegionClassificationRequested {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]queue.RegionClassificationRequested, len(p.regionClassifications))
	copy(cp, p.regionClassifications)
	return cp
}

// PublishCanonicalization appends evt to the recorded canonicalization event list.
func (p *Publisher) PublishCanonicalization(_ context.Context, evt queue.CanonicalizationRequested) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.canonicalizations = append(p.canonicalizations, evt)
	return nil
}

// CanonicalizationEvents returns a snapshot of all published
// CanonicalizationRequested events.
func (p *Publisher) CanonicalizationEvents() []queue.CanonicalizationRequested {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]queue.CanonicalizationRequested, len(p.canonicalizations))
	copy(cp, p.canonicalizations)
	return cp
}

var _ queue.Publisher = (*Publisher)(nil)
