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
