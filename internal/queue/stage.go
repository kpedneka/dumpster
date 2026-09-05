package queue

// DisplayStage is one user-facing step in a document's ingestion pipeline
// (e.g. for a staged progress UI). Centralized here specifically so
// adding a phase or a new job type's display mapping only ever needs one
// new entry in one place, not a hunt across handlers -- see StageFor.
type DisplayStage struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Message string `json:"message,omitempty"`
}

// PhaseEmbedding is the only phase value any handler writes today.
// region_classification is the first (and so far only) job type whose
// job_type alone isn't a fine enough signal: it runs region analysis and
// embedding back-to-back inside one job, with no intervening jobs-table
// update between them otherwise. Every other job type maps to exactly
// one DisplayStage regardless of phase and never calls SetPhase at all.
const PhaseEmbedding = "embedding"

// StageKeyComplete is the terminal stage a document sits on forever once
// its entire pipeline -- including the background entity-extraction stage
// that keeps running after indexing itself finishes -- is done. Unlike
// every other stage key, no job_type ever maps to it via StageFor: it's
// inferred by the caller (dochandler.go's enrichPage) from the absence of
// any active job for an already-indexed document, not from a job's own
// state.
const StageKeyComplete = "complete"

var (
	stageAnalyzing = DisplayStage{
		Key:     "analyzing",
		Label:   "Analyzing document",
		Message: "Identifying text, tables, and figures.",
	}
	stageEmbedding = DisplayStage{
		Key:     "embedding",
		Label:   "Preparing for search",
		Message: "This document will be searchable once this step completes.",
	}
	stageEntities = DisplayStage{
		Key:     "entities",
		Label:   "Extracting entities",
		Message: "Unlocks communities, themes, and multi-hop queries once complete.",
	}
	stageComplete = DisplayStage{
		Key:     StageKeyComplete,
		Label:   "Fully indexed",
		Message: "Indexed, searchable, and included in communities and themes.",
	}
)

// StageFor maps a job's (Type, Phase) to the display stage a user should
// see for it right now.
//
// entity_extraction, edge_extraction, and canonicalization are
// deliberately collapsed into the same "Extracting entities" stage: to a
// user who doesn't know or care that the backend splits this into three
// job types, watching the label change three times for what reads as one
// conceptual step would be confusing, not informative -- exactly the
// kind of pipeline-internal detail this whole feature exists to hide.
func StageFor(jobType JobType, phase string) DisplayStage {
	switch jobType {
	case JobTypeRegionClassification:
		if phase == PhaseEmbedding {
			return stageEmbedding
		}
		return stageAnalyzing
	case JobTypeDocumentIndexing:
		return stageEmbedding
	case JobTypeEntityExtraction, JobTypeEdgeExtraction, JobTypeCanonicalization:
		return stageEntities
	default:
		return DisplayStage{Key: "processing", Label: "Processing"}
	}
}

// StagesForDocument returns the full ordered list of stages a document
// will pass through end to end, given whether it goes through region
// classification (PDF/image) or not (plain text/markdown). Deliberately
// not a single universal list -- a document that skips region
// classification entirely shouldn't show a fake "analyzing" stage that
// never actually happens for it. Every document ends on stageComplete,
// regardless of content type -- it's the one stage every pipeline reaches
// the same way, and stays on permanently so a user can see historically
// that processing finished, not just while it's still in flight.
func StagesForDocument(hasRegionClassification bool) []DisplayStage {
	if hasRegionClassification {
		return []DisplayStage{stageAnalyzing, stageEmbedding, stageEntities, stageComplete}
	}
	return []DisplayStage{stageEmbedding, stageEntities, stageComplete}
}
