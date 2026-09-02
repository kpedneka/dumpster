package community

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// WeightedEdge is an undirected edge between two canonical entities with an
// aggregated co-occurrence weight. Callers must not include self-loops
// (A == B) or duplicate/reversed pairs (A-B and B-A as separate entries) —
// Louvain does not deduplicate or validate this.
type WeightedEdge struct {
	A, B   uuid.UUID
	Weight float64
}

// Graph is an undirected weighted graph over canonical entity identities.
// Nodes lists every node id, including isolated ones with no edges (an
// entity that never co-occurred with another) — Louvain assigns each
// isolated node its own singleton community.
type Graph struct {
	Nodes []uuid.UUID
	Edges []WeightedEdge
}

// edge is one undirected edge between two nodes, referenced by dense index
// rather than uuid.UUID, used internally once nodes have been indexed.
type edge struct {
	a, b   int
	weight float64
}

// neighbor is one adjacency-list entry: a node index and the edge weight
// connecting it to the node whose adjacency list this entry belongs to.
type neighbor struct {
	node   int
	weight float64
}

// level is one graph in the Louvain aggregation hierarchy: level 0 is the
// original graph (indexed), and each subsequent level collapses the
// previous level's communities into super-nodes. selfLoop[i] is the total
// weight of edges folded entirely inside super-node i by a prior
// aggregation (0 at level 0, since Graph forbids self-loops in its input).
// degree[i] is i's total weighted degree — sum of adj[i] weights plus
// 2*selfLoop[i], the standard convention that makes a super-node's degree
// equal the sum of its members' original degrees.
type level struct {
	n        int
	edges    []edge
	adj      [][]neighbor
	selfLoop []float64
	degree   []float64
}

// buildLevel constructs a level's adjacency list and degree from n nodes,
// edges, and per-node self-loop weight, in deterministic order: adjacency
// entries for a node appear in the order their edges were encountered
// while iterating edges front-to-back.
func buildLevel(n int, edges []edge, selfLoop []float64) *level {
	adj := make([][]neighbor, n)
	degree := make([]float64, n)
	for i := 0; i < n; i++ {
		degree[i] = 2 * selfLoop[i]
	}
	for _, e := range edges {
		adj[e.a] = append(adj[e.a], neighbor{e.b, e.weight})
		adj[e.b] = append(adj[e.b], neighbor{e.a, e.weight})
		degree[e.a] += e.weight
		degree[e.b] += e.weight
	}
	return &level{n: n, edges: edges, adj: adj, selfLoop: selfLoop, degree: degree}
}

// localMoving runs repeated passes over lv's nodes, each node tentatively
// moving to whichever neighboring community most increases modularity (or
// staying, if no neighboring community gives a strictly positive gain),
// until a full pass makes zero moves. Node visitation within a pass always
// follows index order 0..n-1 — never map iteration — so results are
// reproducible for identical input. Returns the resulting community
// assignment (dense node index -> community index, not yet renumbered) and
// whether any node ever moved across any pass.
func localMoving(ctx context.Context, lv *level, m2 float64) ([]int, bool, error) {
	n := lv.n
	comm := make([]int, n)
	for i := range comm {
		comm[i] = i
	}
	sigmaTot := make([]float64, n)
	copy(sigmaTot, lv.degree)

	anyMoveEver := false
	// maxPasses is a defensive cap, not an expected limit: every accepted
	// move strictly increases modularity, which is bounded above, so passes
	// must converge (a pass makes zero moves) well before this in practice.
	const maxPasses = 1000
	for pass := 0; pass < maxPasses; pass++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}

		movedThisPass := false
		for i := 0; i < n; i++ {
			ci := comm[i]
			ki := lv.degree[i]

			// Provisionally remove i from its community before evaluating
			// any candidate, including staying in ci — this is what the
			// modularity-gain formula's Σtot(C) requires.
			sigmaTot[ci] -= ki

			var neighOrder []int
			neighSum := make(map[int]float64, len(lv.adj[i]))
			for _, nb := range lv.adj[i] {
				c := comm[nb.node]
				if _, ok := neighSum[c]; !ok {
					neighOrder = append(neighOrder, c)
				}
				neighSum[c] += nb.weight
			}

			bestComm := ci
			bestGain := 0.0 // strictly positive gain required to move
			for _, c := range neighOrder {
				gain := neighSum[c] - sigmaTot[c]*ki/m2
				if gain > bestGain {
					bestGain = gain
					bestComm = c
				}
			}

			sigmaTot[bestComm] += ki
			comm[i] = bestComm
			if bestComm != ci {
				movedThisPass = true
				anyMoveEver = true
			}
		}
		if !movedThisPass {
			break
		}
	}
	return comm, anyMoveEver, nil
}

// aggregate collapses lv's communities (comm) into a new, smaller level:
// every node sharing a community becomes one super-node, edges within a
// community fold into that super-node's self-loop, and edges crossing
// communities are summed into one new inter-community edge per pair.
// Community ids are compacted to dense 0..k-1 in first-seen order over
// node index 0..n-1 (deterministic). Returns the new level and its node
// count.
func aggregate(lv *level, comm []int) (*level, int) {
	remap := make(map[int]int)
	compact := make([]int, lv.n)
	next := 0
	for i := 0; i < lv.n; i++ {
		c := comm[i]
		rc, ok := remap[c]
		if !ok {
			rc = next
			remap[c] = rc
			next++
		}
		compact[i] = rc
	}
	newN := next

	newSelfLoop := make([]float64, newN)
	for i := 0; i < lv.n; i++ {
		newSelfLoop[compact[i]] += lv.selfLoop[i]
	}

	type pair struct{ a, b int }
	var order []pair
	sums := make(map[pair]float64)
	for _, e := range lv.edges {
		ca, cb := compact[e.a], compact[e.b]
		if ca == cb {
			newSelfLoop[ca] += e.weight
			continue
		}
		p := pair{ca, cb}
		if p.a > p.b {
			p.a, p.b = p.b, p.a
		}
		if _, ok := sums[p]; !ok {
			order = append(order, p)
		}
		sums[p] += e.weight
	}
	newEdges := make([]edge, len(order))
	for i, p := range order {
		newEdges[i] = edge{p.a, p.b, sums[p]}
	}

	return buildLevel(newN, newEdges, newSelfLoop), newN
}

// modularity computes Newman's Q for comm (a dense community assignment
// over the ORIGINAL n nodes) against the original graph's edges and
// per-node degree: Q = Σ_c [ e_c - a_c^2 ], where e_c is community c's
// share of total edge weight that falls strictly inside it, and a_c is
// its share of total degree. Computed directly from level-0 data rather
// than relying on an aggregated level's modularity equaling the original
// graph's — simpler to get right, and cheap enough to just do directly.
func modularity(n int, edges []edge, degree []float64, totalWeight float64, comm []int) float64 {
	if totalWeight == 0 {
		return 0
	}
	internals := make(map[int]float64)
	for _, e := range edges {
		if comm[e.a] == comm[e.b] {
			internals[comm[e.a]] += e.weight
		}
	}
	degreeByComm := make(map[int]float64)
	for i := 0; i < n; i++ {
		degreeByComm[comm[i]] += degree[i]
	}
	m := totalWeight
	var q float64
	for c, deg := range degreeByComm {
		a := deg / (2 * m)
		q += internals[c]/m - a*a
	}
	return q
}

// Louvain computes communities over g via modularity maximization
// (Blondel et al. 2008): repeated local-moving passes (each node
// tentatively moves to whichever neighboring community most increases
// modularity, or stays) until no move improves modularity, then the
// resulting communities are collapsed into a smaller "super-graph" and the
// same process repeats on that, until a full pass produces no further
// gain. Returns a dense community id (starting at 0, stable only within
// this one call — not meaningfully comparable across separate calls) per
// original node, plus the final modularity score.
//
// ctx is checked for cancellation at the start of every local-moving pass
// and every aggregation level. Louvain returns ctx.Err() promptly on
// cancellation rather than completing the run — a caller enforcing a
// timeout needs this to actually stop consuming CPU/memory, not just
// abandon listening for the result while the computation keeps running.
func Louvain(ctx context.Context, g *Graph) (map[uuid.UUID]int, float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, fmt.Errorf("community: louvain: %w", err)
	}

	n := len(g.Nodes)
	if n == 0 {
		return map[uuid.UUID]int{}, 0, nil
	}

	idx := make(map[uuid.UUID]int, n)
	for i, id := range g.Nodes {
		idx[id] = i
	}

	level0Edges := make([]edge, 0, len(g.Edges))
	var totalWeight float64
	for _, e := range g.Edges {
		a, aok := idx[e.A]
		b, bok := idx[e.B]
		if !aok || !bok || a == b {
			continue
		}
		level0Edges = append(level0Edges, edge{a, b, e.Weight})
		totalWeight += e.Weight
	}

	if totalWeight == 0 {
		result := make(map[uuid.UUID]int, n)
		for i, id := range g.Nodes {
			result[id] = i
		}
		return result, 0, nil
	}

	level0 := buildLevel(n, level0Edges, make([]float64, n))
	m2 := 2 * totalWeight

	cur := level0
	// finalComm[i] is original node i's community index in cur's node
	// space, composed through every aggregation performed so far.
	finalComm := make([]int, n)
	for i := range finalComm {
		finalComm[i] = i
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, fmt.Errorf("community: louvain: %w", err)
		}

		comm, moved, err := localMoving(ctx, cur, m2)
		if err != nil {
			return nil, 0, fmt.Errorf("community: louvain: %w", err)
		}
		if !moved {
			break
		}

		for i := range finalComm {
			finalComm[i] = comm[finalComm[i]]
		}

		next, newN := aggregate(cur, comm)
		cur = next
		if newN == 1 {
			// Fully collapsed to one community; nothing more to improve.
			break
		}
	}

	result := make(map[uuid.UUID]int, n)
	remap := make(map[int]int)
	nextID := 0
	for i, id := range g.Nodes {
		c := finalComm[i]
		rc, ok := remap[c]
		if !ok {
			rc = nextID
			remap[c] = rc
			nextID++
		}
		result[id] = rc
	}

	q := modularity(n, level0Edges, level0.degree, totalWeight, finalComm)
	return result, q, nil
}
