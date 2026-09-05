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

func stageKeys(stages []DisplayStage) []string {
	keys := make([]string, len(stages))
	for i, s := range stages {
		keys[i] = s.Key
	}
	return keys
}
