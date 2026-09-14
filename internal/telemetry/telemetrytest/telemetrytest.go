// Package telemetrytest gives tests in other packages real OTel instruments
// backed by an in-memory reader, so they can record against the real metric
// API and assert on what was actually recorded. telemetry.Setup pushes over
// OTLP/gRPC to an ADOT Collector sidecar that doesn't exist in a unit test,
// so tests build instruments through here instead.
package telemetrytest

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

// New returns real telemetry.Instruments and a Collector that reads back
// whatever gets recorded on them.
func New(t *testing.T) (*telemetry.Instruments, *Collector) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	inst, err := telemetry.NewInstruments(provider.Meter("dumpster-test"))
	if err != nil {
		t.Fatalf("telemetrytest: new instruments: %v", err)
	}
	return inst, &Collector{t: t, reader: reader}
}

// Collector reads back metric data recorded on instruments returned by New.
type Collector struct {
	t      *testing.T
	reader *sdkmetric.ManualReader
}

func (c *Collector) collect() metricdata.ResourceMetrics {
	c.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := c.reader.Collect(context.Background(), &rm); err != nil {
		c.t.Fatalf("telemetrytest: collect: %v", err)
	}
	return rm
}

func (c *Collector) findMetric(name string) (metricdata.Metrics, bool) {
	rm := c.collect()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// HasMetric reports whether any data point has ever been recorded for the
// named instrument. A counter or histogram with zero recordings does not
// appear in collected data at all, which is what distinguishes "recorded
// zero" from "never attempted" for tests asserting an outcome was never
// counted (e.g. a validation rejection that must not count as an attempt).
func (c *Collector) HasMetric(name string) bool {
	_, ok := c.findMetric(name)
	return ok
}

// HistogramCount returns the total number of observations recorded across
// every attribute set for the named histogram, or 0 if it was never
// recorded.
func (c *Collector) HistogramCount(name string) int {
	m, ok := c.findMetric(name)
	if !ok {
		return 0
	}
	data, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		return 0
	}
	total := 0
	for _, dp := range data.DataPoints {
		total += int(dp.Count)
	}
	return total
}

// CounterValue returns the recorded value of the named int64 counter or
// up-down-counter for the data point whose attribute set exactly matches
// attrs, and whether a matching data point was found at all.
func (c *Collector) CounterValue(name string, attrs ...attribute.KeyValue) (int64, bool) {
	m, ok := c.findMetric(name)
	if !ok {
		return 0, false
	}
	data, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return 0, false
	}
	want := attribute.NewSet(attrs...)
	for _, dp := range data.DataPoints {
		if dp.Attributes.Equals(&want) {
			return dp.Value, true
		}
	}
	return 0, false
}
