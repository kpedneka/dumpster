package theme

import (
	"math"
	"sort"
)

// SelectTopCommunities returns the largest communities in members, ranked
// by member count — the "handful of largest/most-connected clusters" a
// theme label is worth generating for, not every community detection
// found. Limited to the top ceil(5% of the total community count), at
// least one when any communities exist: nearest-rank by count rather than
// interpolating a size threshold, since community counts are typically
// small (bounded by the entity-count ceiling community detection itself
// enforces), so percentile-interpolation edge cases at low N aren't worth
// the complexity — and zero themes for a KB that genuinely has community
// structure would be a confusing, unhelpful outcome.
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
	return sorted[:n]
}
