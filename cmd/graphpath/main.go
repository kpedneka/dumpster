// cmd/graphpath finds the shortest canonical-entity path between two named
// entities in a KB's co-occurrence graph -- a diagnostic for unexpected
// connectivity (e.g. two topics meant to be fully disconnected turning out
// not to be), not something any production code path depends on.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

type edge struct {
	aID, aText string
	bID, bText string
}

func queryEdges(ctx context.Context, runner db.TxRunner, kbID, userID uuid.UUID) ([]edge, error) {
	var result []edge
	err := runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT
			       CASE WHEN ca.id < cb.id THEN ca.id             ELSE cb.id             END,
			       CASE WHEN ca.id < cb.id THEN ca.canonical_text ELSE cb.canonical_text END,
			       CASE WHEN ca.id < cb.id THEN cb.id             ELSE ca.id             END,
			       CASE WHEN ca.id < cb.id THEN cb.canonical_text ELSE ca.canonical_text END
			FROM   entity_edges ee
			JOIN   entities e1 ON e1.id = ee.entity_a_id
			JOIN   entities e2 ON e2.id = ee.entity_b_id
			JOIN   canonical_entities ca ON ca.id = e1.canonical_entity_id
			JOIN   canonical_entities cb ON cb.id = e2.canonical_entity_id
			WHERE  ee.kb_id = $1 AND ee.user_id = $2 AND ca.id <> cb.id`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e edge
			if err := rows.Scan(&e.aID, &e.aText, &e.bID, &e.bText); err != nil {
				return err
			}
			result = append(result, e)
		}
		return rows.Err()
	})
	return result, err
}

func main() {
	sessionIDFlag := flag.String("session-id", "", "required")
	kbIDFlag := flag.String("kb-id", "", "required")
	from := flag.String("from", "", "substring to match the starting canonical entity's text (required)")
	to := flag.String("to", "", "substring to match the target canonical entity's text (required)")
	flag.Parse()

	logger := telemetry.NewLogger(os.Stdout, "graphpath")

	if *sessionIDFlag == "" || *kbIDFlag == "" || *from == "" || *to == "" {
		logger.Error("missing required flags", "need", "-session-id, -kb-id, -from, -to")
		os.Exit(1)
	}
	userID, err := uuid.Parse(*sessionIDFlag)
	if err != nil {
		logger.Error("invalid -session-id", "err", err)
		os.Exit(1)
	}
	kbID, err := uuid.Parse(*kbIDFlag)
	if err != nil {
		logger.Error("invalid -kb-id", "err", err)
		os.Exit(1)
	}

	ctx := auth.WithUserID(context.Background(), userID)
	cfg := config.Load()

	pool, err := db.ConnectDirect(ctx, cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	txRunner := rls.New(pool)

	edges, err := queryEdges(ctx, txRunner, kbID, userID)
	if err != nil {
		logger.Error("query edges failed", "err", err)
		os.Exit(1)
	}

	names := map[string]string{}
	adj := map[string]map[string]bool{}
	for _, e := range edges {
		names[e.aID] = e.aText
		names[e.bID] = e.bText
		if adj[e.aID] == nil {
			adj[e.aID] = map[string]bool{}
		}
		if adj[e.bID] == nil {
			adj[e.bID] = map[string]bool{}
		}
		adj[e.aID][e.bID] = true
		adj[e.bID][e.aID] = true
	}
	logger.Info("loaded graph", "canonical_entities", len(names), "edges", len(edges))

	var starts, ends []string
	for id, text := range names {
		lt := strings.ToLower(text)
		if strings.Contains(lt, strings.ToLower(*from)) {
			starts = append(starts, id)
		}
		if strings.Contains(lt, strings.ToLower(*to)) {
			ends = append(ends, id)
		}
	}
	if len(starts) == 0 {
		logger.Error("no canonical entity matched -from", "from", *from)
		os.Exit(1)
	}
	if len(ends) == 0 {
		logger.Error("no canonical entity matched -to", "to", *to)
		os.Exit(1)
	}
	fmt.Printf("Matched -from %q: %v\n", *from, textsOf(starts, names))
	fmt.Printf("Matched -to %q: %v\n\n", *to, textsOf(ends, names))

	endSet := map[string]bool{}
	for _, e := range ends {
		endSet[e] = true
	}

	visited := map[string]string{} // node -> predecessor ("" for a start node)
	queue := []string{}
	for _, s := range starts {
		visited[s] = s
		queue = append(queue, s)
	}
	var found string
	for len(queue) > 0 && found == "" {
		cur := queue[0]
		queue = queue[1:]
		if endSet[cur] {
			found = cur
			break
		}
		for nbr := range adj[cur] {
			if _, seen := visited[nbr]; !seen {
				visited[nbr] = cur
				queue = append(queue, nbr)
			}
		}
	}

	if found == "" {
		fmt.Println("No path found -- the two are in genuinely disconnected components.")
		return
	}

	var path []string
	for n := found; ; {
		path = append([]string{n}, path...)
		p := visited[n]
		if p == n {
			break
		}
		n = p
	}
	fmt.Printf("Shortest path (%d hops):\n", len(path)-1)
	for i, id := range path {
		fmt.Printf("  %d. %s\n", i, names[id])
	}
}

func textsOf(ids []string, names map[string]string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = names[id]
	}
	return out
}
