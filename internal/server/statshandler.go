package server

import (
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/stats"
)

// registerStatsRoutes registers the public, unauthenticated usage-stats
// endpoint. Deliberately registered on the outer mux rather than the authed
// one (see NewRouter) — no session should be minted just to read a number
// that isn't scoped to any tenant.
func registerStatsRoutes(mux *http.ServeMux, repo stats.Repository) {
	mux.HandleFunc("GET /stats", statsHandlerFunc(repo))
}

// statsResponse is the JSON shape of GET /stats.
type statsResponse struct {
	DocumentsIndexed     int64   `json:"documents_indexed"`
	AvgDocumentSizeBytes float64 `json:"avg_document_size_bytes"`
	QueriesExecuted      int64   `json:"queries_executed"`
	AvgQueryDurationMs   float64 `json:"avg_query_duration_ms"`
}

func statsHandlerFunc(repo stats.Repository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap, err := repo.Get(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load usage stats")
			return
		}
		writeJSON(w, http.StatusOK, statsResponse{
			DocumentsIndexed:     snap.DocumentsIndexed,
			AvgDocumentSizeBytes: snap.AvgDocumentSizeBytes,
			QueriesExecuted:      snap.QueriesExecuted,
			AvgQueryDurationMs:   snap.AvgQueryDurationMs,
		})
	}
}
