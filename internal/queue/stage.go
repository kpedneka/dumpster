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

// ActiveStageKeys returns the deduplicated set of stage keys genuinely
// active right now, in canonical stage order (the same order
// StagesForDocument returns). More than one can come back at once now
// that entity extraction can start before a document's own indexing job
// (region_classification/document_indexing) finishes -- see
// worker.RegionClassificationHandler.process's doc: while
// region_classification is still embedding (phase=embedding) and
// entity_extraction is already processing, both are genuinely active for
// the same document simultaneously, and collapsing that down to a single
// "current stage" would either hide real concurrent work or (worse, if
// the wrong one were picked) claim the document is further along than it
// actually is.
//
// The first key in the result is always the *earliest* stage still in
// progress -- the actual bottleneck, i.e. what a single-line inline
// summary should name -- while the full slice is what a checklist needs
// to mark every currently-active stage, not just one. Returns nil if
// active is empty.
func ActiveStageKeys(hasRegionClassification bool, active []JobStatus) []string {
	stages := StagesForDocument(hasRegionClassification)
	presentKeys := make(map[string]bool, len(active))
	for _, job := range active {
		presentKeys[StageFor(job.Type, job.Phase).Key] = true
	}

	var keys []string
	for _, s := range stages {
		if presentKeys[s.Key] {
			keys = append(keys, s.Key)
		}
	}
	return keys
}
