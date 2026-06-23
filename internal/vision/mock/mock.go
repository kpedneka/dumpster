// Package mock provides a test double for vision.Describer.
package mock

import (
	"context"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/vision"
)

// Describer is an in-memory test double for vision.Describer.
type Describer struct {
	mu sync.Mutex
	// DescribeResponse is returned by every Describe call. Defaults to
	// a non-empty placeholder so tests don't accidentally trigger the
	// "empty description → skip" path unless they explicitly set it.
	DescribeResponse string
	// IsScanned is returned by every ConfirmScanned call.
	IsScanned bool
	// DescribeCalls records the number of times Describe was called.
	DescribeCalls int
	// ConfirmScannedCalls records the number of times ConfirmScanned was called.
	ConfirmScannedCalls int
}

// New returns a Describer whose Describe produces a non-empty description
// (so the caller treats the region as indexed) and ConfirmScanned returns
// false (not scanned, i.e. the suspected content is extractable).
func New() *Describer {
	return &Describer{DescribeResponse: "A figure showing relevant information."}
}

func (d *Describer) Describe(_ context.Context, _ []byte) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.DescribeCalls++
	return d.DescribeResponse, nil
}

func (d *Describer) ConfirmScanned(_ context.Context, _ []byte) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ConfirmScannedCalls++
	return d.IsScanned, nil
}

var _ vision.Describer = (*Describer)(nil)
