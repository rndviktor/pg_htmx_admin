package web

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// explainSQL builds the EXPLAIN statement. BUFFERS is only valid (and only
// useful) together with ANALYZE.
func explainSQL(query string, analyze bool) string {
	opts := "FORMAT JSON"
	if analyze {
		opts += ", ANALYZE, BUFFERS"
	}
	return "EXPLAIN (" + opts + ") " + query
}

// explainMutates reports whether an ANALYZEd query is not a plain read, i.e.
// whether the rollback is what keeps its changes from persisting. It only
// drives the "rolled back" note; ANALYZE always runs in a rolled-back
// transaction, because even a SELECT can call a function that writes.
func explainMutates(query string) bool {
	switch firstKeyword(query) {
	case "SELECT", "VALUES", "TABLE":
		return false
	}
	return true
}

// handleExplain runs EXPLAIN (FORMAT JSON), optionally with ANALYZE, and
// returns the plan rendered as HTML (explain_plan.go). EXPLAIN ANALYZE really
// executes the statement, so it runs inside a transaction that is always
// rolled back: plans are real, but INSERT/UPDATE/DELETE leave no trace.
func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	query := strings.TrimRight(r.FormValue("sql_query"), " \t\n\r;")
	dbName := r.FormValue("db_name")
	tabID := r.FormValue("tab_id")
	analyze := r.FormValue("analyze") == "on"
	serverID, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	if query == "" || dbName == "" || err != nil || serverID < 1 {
		http.Error(w, "Missing query, server_id, or db_name", http.StatusBadRequest)
		return
	}
	if isExplain(query) {
		http.Error(w, "The query already starts with EXPLAIN; remove it to use the plan viewer", http.StatusBadRequest)
		return
	}

	pool, err := s.getOrCreateDbPool(r.Context(), serverID, dbName)
	if err != nil {
		log.Printf("[explain] cannot connect to server %d db %q: %v", serverID, dbName, err)
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return
	}
	conn, err := pool.Acquire(r.Context())
	if err != nil {
		log.Printf("[explain] pool acquire for server %d db %q failed: %v", serverID, dbName, err)
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Release()

	s.registerQuery(tabID, conn.Conn().PgConn().PID(), pool)
	defer s.unregisterQuery(tabID)

	start := time.Now()
	var plan []byte
	if analyze {
		tx, txErr := conn.Begin(r.Context())
		if txErr != nil {
			err = txErr
		} else {
			// Detached context: the rollback must happen even if the browser
			// aborted the request (Stop button).
			defer tx.Rollback(context.WithoutCancel(r.Context()))
			err = tx.QueryRow(r.Context(), explainSQL(query, true)).Scan(&plan)
		}
	} else {
		err = conn.QueryRow(r.Context(), explainSQL(query, false)).Scan(&plan)
	}
	elapsed := time.Since(start)

	s.recordQueryHistory(r.Context(), serverID, dbName, tabID, explainSQL(query, analyze), elapsed.Milliseconds(), 0, queryStatus(err))
	if err != nil {
		log.Printf("[explain] failed on server %d db %q: %v", serverID, dbName, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	group := tabID
	if !panelIDPattern.MatchString(group) {
		group = "x"
	}
	view, err := buildPlanView(plan, analyze && explainMutates(query), group)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	RenderPartial(w, "explain_plan.html", view)
}
