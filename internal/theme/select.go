package theme

import (
	"math"
	"sort"
)

// maxSelectedCommunities caps how many themes a single recompute ever
// generates, regardless of how many communities exist. Without it, the
// percentile alone doesn't bound the count for a KB with many small
// communities — a real KB with 563 communities (mostly barely-connected
// singletons or near-singletons; see describeCommunityStructure's own
// "barely connected" case in the frontend) hit ceil(5% of 563) = 29
// themes in one recompute, which is overwhelming to read even though each
// individual label/summary stayed short. 5 keeps this to "the handful of
// largest/most-connected clusters" the card originally called for, not a
// number that scales with an unrelated (and often noisy) community count.
const maxSelectedCommunities = 5

// SelectTopCommunities returns the largest communities in members, ranked
// by member count — the "handful of largest/most-connected clusters" a
// theme label is worth generating for, not every community detection
// found. Limited to the top ceil(5% of the total community count), capped
// at maxSelectedCommunities, at least one when any communities exist:
// nearest-rank by count rather than interpolating a size threshold, since
// community counts are typically small (bounded by the entity-count
// ceiling community detection itself enforces), so percentile-
// interpolation edge cases at low N aren't worth the complexity — and
// zero themes for a KB that genuinely has community structure would be a
// confusing, unhelpful outcome.
func SelectTopCommunities(members []CommunityMembers) []CommunityMembers {
	if len(members) == 0 {
		return nil
	}
	sorted := make([]CommunityMembers, len(members))
	copy(sorted, members)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i].Entities) > len(sorted[j].Entities)
	})

	n := int(math.Ceil(0.05 * float64(len(sorted))))
	if n < 1 {
		n = 1
	}
	if n > maxSelectedCommunities {
		n = maxSelectedCommunities
	}
	return sorted[:n]
}
