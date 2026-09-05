package queue

import "testing"

func TestStageFor_RegionClassification_PhaseDeterminesStage(t *testing.T) {
	analyzing := StageFor(JobTypeRegionClassification, "")
	if analyzing.Key != "analyzing" {
		t.Errorf("empty phase: got stage %q, want analyzing", analyzing.Key)
	}

	embedding := StageFor(JobTypeRegionClassification, PhaseEmbedding)
	if embedding.Key != "embedding" {
		t.Errorf("PhaseEmbedding: got stage %q, want embedding", embedding.Key)
	}
}

func TestStageFor_DocumentIndexing_AlwaysEmbedding(t *testing.T) {
	got := StageFor(JobTypeDocumentIndexing, "")
	if got.Key != "embedding" {
		t.Errorf("got stage %q, want embedding", got.Key)
	}
}

func TestStageFor_EntityPipelineJobTypes_CollapseToOneStage(t *testing.T) {
	for _, jt := range []JobType{JobTypeEntityExtraction, JobTypeEdgeExtraction, JobTypeCanonicalization} {
		got := StageFor(jt, "")
		if got.Key != "entities" {
			t.Errorf("job type %s: got stage %q, want entities (all three should read as one user-facing stage)", jt, got.Key)
		}
	}
}

func TestStagesForDocument_RegionClassification_IncludesAnalyzing(t *testing.T) {
	stages := StagesForDocument(true)
	if len(stages) != 4 {
		t.Fatalf("got %d stages, want 4", len(stages))
	}
	if stages[0].Key != "analyzing" || stages[1].Key != "embedding" || stages[2].Key != "entities" || stages[3].Key != "complete" {
		t.Errorf("got stage order %v, want [analyzing embedding entities complete]", stageKeys(stages))
	}
}

func TestStagesForDocument_PlainText_SkipsAnalyzing(t *testing.T) {
	stages := StagesForDocument(false)
	if len(stages) != 3 {
		t.Fatalf("got %d stages, want 3", len(stages))
	}
	if stages[0].Key != "embedding" || stages[1].Key != "entities" || stages[2].Key != "complete" {
		t.Errorf("got stage order %v, want [embedding entities complete]", stageKeys(stages))
	}
}

func TestStagesForDocument_AlwaysEndsOnComplete(t *testing.T) {
	for _, hasRegionClassification := range []bool{true, false} {
		stages := StagesForDocument(hasRegionClassification)
		last := stages[len(stages)-1]
		if last.Key != StageKeyComplete {
			t.Errorf("hasRegionClassification=%v: last stage = %q, want %q", hasRegionClassification, last.Key, StageKeyComplete)
		}
	}
}

// TestActiveStageKeys_ConcurrentEmbedAndEntityExtraction_ReturnsBoth is
// the regression test for the exact bug introduced by letting entity
// extraction start before a document's own indexing job finishes:
// entity extraction can now be active at the same time as
// region_classification is still embedding. Collapsing that down to
// whichever job was created most recently (the old CurrentJobsForDocuments
// heuristic) would surface "Extracting entities" alone while the document
// isn't searchable yet -- a false cue that querying it would work. Both
// stages must come back, honestly reflecting that both are in progress.
func TestActiveStageKeys_ConcurrentEmbedAndEntityExtraction_ReturnsBoth(t *testing.T) {
	active := []JobStatus{
		{Type: JobTypeEntityExtraction, Status: "processing"},
		{Type: JobTypeRegionClassification, Phase: PhaseEmbedding, Status: "processing"},
	}
	keys := ActiveStageKeys(true, active)
	if len(keys) != 2 || keys[0] != "embedding" || keys[1] != "entities" {
		t.Errorf("got %v, want [embedding entities] (canonical order, embedding first as the bottleneck)", keys)
	}
}

func TestActiveStageKeys_OrderOfInputDoesNotMatter(t *testing.T) {
	a := []JobStatus{
		{Type: JobTypeRegionClassification, Phase: PhaseEmbedding, Status: "processing"},
		{Type: JobTypeEntityExtraction, Status: "processing"},
	}
	b := []JobStatus{a[1], a[0]}

	keysA := ActiveStageKeys(true, a)
	keysB := ActiveStageKeys(true, b)
	if len(keysA) != len(keysB) || keysA[0] != keysB[0] || keysA[1] != keysB[1] {
		t.Errorf("result depended on input order: %v vs %v", keysA, keysB)
	}
}

func TestActiveStageKeys_MultipleJobsSameStage_Deduplicated(t *testing.T) {
	// entity_extraction, edge_extraction, and canonicalization all map to
	// the same "entities" stage -- if more than one happened to be active
	// at once, the result must still list "entities" only once.
	active := []JobStatus{
		{Type: JobTypeEntityExtraction, Status: "processing"},
		{Type: JobTypeEdgeExtraction, Status: "processing"},
	}
	keys := ActiveStageKeys(true, active)
	if len(keys) != 1 || keys[0] != "entities" {
		t.Errorf("got %v, want [entities] (deduplicated)", keys)
	}
}

func TestActiveStageKeys_SingleActiveJob_ReturnsItsStage(t *testing.T) {
	keys := ActiveStageKeys(true, []JobStatus{{Type: JobTypeEntityExtraction}})
	if len(keys) != 1 || keys[0] != "entities" {
		t.Errorf("got %v, want [entities]", keys)
	}
}

func TestActiveStageKeys_NoActiveJobs_ReturnsEmpty(t *testing.T) {
	if keys := ActiveStageKeys(true, nil); len(keys) != 0 {
		t.Errorf("got %v, want empty", keys)
	}
}

func TestActiveStageKeys_AnalyzingBeatsEmbeddingBeatsEntities(t *testing.T) {
	// Canonical order wins regardless of input order.
	active := []JobStatus{
		{Type: JobTypeEntityExtraction},
		{Type: JobTypeRegionClassification, Phase: PhaseEmbedding},
		{Type: JobTypeRegionClassification},
	}
	keys := ActiveStageKeys(true, active)
	if len(keys) != 3 || keys[0] != "analyzing" || keys[1] != "embedding" || keys[2] != "entities" {
		t.Errorf("got %v, want [analyzing embedding entities]", keys)
	}
}

func stageKeys(stages []DisplayStage) []string {
	keys := make([]string, len(stages))
	for i, s := range stages {
		keys[i] = s.Key
	}
	return keys
}
