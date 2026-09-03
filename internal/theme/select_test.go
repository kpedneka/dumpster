package theme

import "testing"

func TestSelectTopCommunities_EmptyInput_ReturnsNil(t *testing.T) {
	if got := SelectTopCommunities(nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestSelectTopCommunities_SmallCount_AlwaysReturnsAtLeastOne(t *testing.T) {
	members := []CommunityMembers{
		{CommunityID: 0, Entities: make([]EntityRef, 3)},
		{CommunityID: 1, Entities: make([]EntityRef, 5)},
		{CommunityID: 2, Entities: make([]EntityRef, 1)},
	}
	got := SelectTopCommunities(members)
	if len(got) != 1 {
		t.Fatalf("got %d communities, want 1 (ceil(5%% of 3) = 1)", len(got))
	}
	if got[0].CommunityID != 1 {
		t.Errorf("got community %d, want 1 (the largest, 5 entities)", got[0].CommunityID)
	}
}

func TestSelectTopCommunities_ScalesWithCommunityCount(t *testing.T) {
	// 40 communities -> ceil(0.05*40) = 2.
	members := make([]CommunityMembers, 40)
	for i := range members {
		members[i] = CommunityMembers{CommunityID: i, Entities: make([]EntityRef, i+1)}
	}
	got := SelectTopCommunities(members)
	if len(got) != 2 {
		t.Fatalf("got %d communities, want 2", len(got))
	}
	// Largest two: community 39 (40 entities), community 38 (39 entities).
	if got[0].CommunityID != 39 || got[1].CommunityID != 38 {
		t.Errorf("got communities %d, %d, want 39, 38 (largest first)", got[0].CommunityID, got[1].CommunityID)
	}
}

// Regression test: a real KB with 563 communities (mostly barely-connected
// singletons) hit ceil(5% of 563) = 29 themes in one recompute before this
// cap existed -- overwhelming to read even though each individual
// label/summary stayed short.
func TestSelectTopCommunities_CapsAtFiveRegardlessOfPercentile(t *testing.T) {
	members := make([]CommunityMembers, 563)
	for i := range members {
		members[i] = CommunityMembers{CommunityID: i, Entities: make([]EntityRef, i+1)}
	}
	got := SelectTopCommunities(members)
	if len(got) != 5 {
		t.Fatalf("got %d communities, want 5 (capped, not ceil(5%% of 563) = 29)", len(got))
	}
	// Largest five: communities 562 down to 558.
	for i, want := range []int{562, 561, 560, 559, 558} {
		if got[i].CommunityID != want {
			t.Errorf("got[%d].CommunityID = %d, want %d", i, got[i].CommunityID, want)
		}
	}
}

func TestSelectTopCommunities_DoesNotMutateInput(t *testing.T) {
	members := []CommunityMembers{
		{CommunityID: 0, Entities: make([]EntityRef, 1)},
		{CommunityID: 1, Entities: make([]EntityRef, 2)},
	}
	SelectTopCommunities(members)
	if members[0].CommunityID != 0 || members[1].CommunityID != 1 {
		t.Errorf("input order was mutated: %+v", members)
	}
}
