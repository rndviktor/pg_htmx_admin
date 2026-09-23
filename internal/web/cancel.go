package web

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runningQuery tracks a query currently executing on a backend connection so
// the Stop button can cancel it. pid is the PostgreSQL backend process id of
// the connection running the query; pool is used to open a separate
// connection that issues pg_cancel_backend(pid).
type runningQuery struct {
	pid  uint32
	pool *pgxpool.Pool
}

// registerQuery associates the query executing on tabID's connection with its
// backend PID so a cancel request can find it.
func (s *Server) registerQuery(tabID string, pid uint32, pool *pgxpool.Pool) {
	s.queryMu.Lock()
	s.runningQueries[tabID] = &runningQuery{pid: pid, pool: pool}
	s.queryMu.Unlock()
}

// unregisterQuery drops the tracking entry for tabID once its query finishes,
// cancels, or errors out.
func (s *Server) unregisterQuery(tabID string) {
	s.queryMu.Lock()
	delete(s.runningQueries, tabID)
	s.queryMu.Unlock()
}

// cancelQuery asks PostgreSQL to cancel the backend running tabID's query by
// issuing pg_cancel_backend from a separate connection of the same pool. It
// reports whether a running query was registered, and any error encountered
// while issuing the cancellation.
func (s *Server) cancelQuery(r *http.Request, tabID string) (bool, error) {
	s.queryMu.Lock()
	run := s.runningQueries[tabID]
	s.queryMu.Unlock()
	if run == nil {
		return false, nil
	}

	conn, err := run.pool.Acquire(r.Context())
	if err != nil {
		return true, err
	}
	defer conn.Release()

	var cancelled bool
	err = conn.QueryRow(r.Context(), "SELECT pg_cancel_backend($1)", run.pid).Scan(&cancelled)
	if cancelled {
		return true, nil
	}
	return true, err
}

// handleCancelQuery is the POST /api/cancel-query endpoint: it cancels the
// backend running the script tab's current query, if any.
func (s *Server) handleCancelQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID string `json:"tab_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TabID == "" {
		log.Printf("[cancel-query] missing or invalid tab_id: body decode err=%v", err)
		http.Error(w, "Missing tab_id", http.StatusBadRequest)
		return
	}

	found, err := s.cancelQuery(r, req.TabID)
	if err != nil {
		log.Printf("[cancel-query] cancelling tab %q failed: %v", req.TabID, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		log.Printf("[cancel-query] no running query for tab %q", req.TabID)
		http.Error(w, "No query is currently running for this tab", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"cancelled": true})
}
