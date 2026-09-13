// Package layout holds RawRegion, the shared region-classification result
// type. It no longer contains an HTTP client of its own -- region
// extraction moved to an AWS Batch job (internal/manifest/awsbatch) once
// Fly deploys stopped entirely (see the "Fold region classification into
// the AWS Batch embed job" dev board card), so the always-on inference
// service's synchronous /regions call this package used to wrap has no
// remaining caller and was deleted, not kept behind a flag: cmd/worker is
// no longer deployed anywhere that needs the fallback.
package layout

// RawRegion is a single classified region, produced by
// internal/manifest/awsbatch.Extractor. The RegionClassificationHandler
// routes each RawRegion through additional Go-side steps before
// persisting it to the ingestion manifest.
type RawRegion struct {
	// RegionType is the coarse label assigned by the layered classifier:
	// "native_text", "native_table", "figure", "unconfirmed_text", or
	// "unconfirmed_table".
	RegionType string
	// PageNumber is 1-indexed.
	PageNumber int
	// BoundingBox holds fractional page coordinates in [0,1]: [x0,y0,x1,y1].
	BoundingBox [4]float64
	// Text is set for native_text and native_table regions where the
	// classifier could extract readable text directly.
	Text string
	// ImageBase64 is a base64-encoded PNG crop of the region, set for figure
	// and unconfirmed_* regions that need a VLM follow-up call.
	ImageBase64 string
	// NeedsVLM classifies why layers 1+2 couldn't fully resolve this region:
	//   "describe"      — a figure/image (no VLM description; see
	//                     RegionClassificationHandler's type doc).
	//   "confirm_scan"  — suspected scanned content (no VLM confirmation;
	//                     treated as skipped rather than guessed).
	//   ""              — no follow-up needed (native content already resolved).
	NeedsVLM string
}
