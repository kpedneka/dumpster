// Package vision defines the interface for local vision-language model
// inference — figure description and scanned-content confirmation — used
// during PDF region classification. The only implementation is
// internal/vision/ollama, which calls a locally-running Ollama service
// (see docker-compose.yml) via plain HTTP. Nothing outside vision/ollama
// may import an HTTP client or VLM SDK for this purpose.
package vision

import (
	"context"
	"errors"
)

// ErrVLMUnavailable is returned when the VLM service cannot be reached or
// returns an unrecoverable error. The caller (RegionClassificationHandler)
// treats this as a "confirm_scan → skip" signal rather than a job failure,
// matching the spec's "honestly skip rather than guess" bar.
var ErrVLMUnavailable = errors.New("vision: VLM service unavailable")

// Describer calls a local vision-language model for two distinct tasks:
//   - Describe: generate a natural-language description of a figure image.
//   - ConfirmScanned: decide whether an image is a rasterized/scanned page
//     rather than an extractable document element.
//
// Implementations must default to skip-safe outcomes on ambiguity or error:
// Describe returns "" (no description, region will be skipped) and
// ConfirmScanned returns true (treat as scanned, skip) rather than guessing.
type Describer interface {
	// Describe returns a natural-language description of image suitable
	// for embedding and search. Returns "" if the VLM cannot produce a
	// useful description, in which case the caller marks the region skipped.
	Describe(ctx context.Context, image []byte) (description string, err error)
	// ConfirmScanned returns true if image appears to be a rasterized or
	// scanned page element (as opposed to vector/native content that a
	// future OCR step might process). Defaults to true (confirm = scanned)
	// on any error or ambiguity, matching the spec's "honestly skip rather
	// than guess" bar.
	ConfirmScanned(ctx context.Context, image []byte) (isScanned bool, err error)
}
