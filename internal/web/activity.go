package web

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// loadPoolFromQuery reads server_id and db_name from query parameters and
// returns a pgx pool for that database. Used by the sessions/locks/prepared-
// transactions endpoints which are called via fetch() with explicit params.
func (s *Server) loadPoolFromQuery(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, bool) {
	serverIDStr := r.URL.Query().Get("server_id")
	dbName := r.URL.Query().Get("db_name")
	if serverIDStr == "" || dbName == "" {
		log.Printf("[%s] Missing server_id or db_name params", r.URL.Path)
		http.Error(w, "Missing server_id or db_name", http.StatusBadRequest)
		return nil, false
	}
	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil || serverID < 1 {
		log.Printf("[%s] Invalid server_id %q", r.URL.Path, serverIDStr)
		http.Error(w, "Invalid server_id", http.StatusBadRequest)
		return nil, false
	}
	pool, err := s.getOrCreateDbPool(r.Context(), serverID, dbName)
	if err != nil {
		log.Printf("[%s] Cannot connect to database (server %d, db %q): %v", r.URL.Path, serverID, dbName, err)
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return nil, false
	}
	return pool, true
}

// matchesSearch reports whether any field contains the (already lower-cased)
// search term. An empty term matches everything. Shared by the sessions,
// locks and prepared-transactions listings.
func matchesSearch(search string, fields ...string) bool {
	if search == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), search) {
			return true
		}
	}
	return false
}

type sessionRow struct {
	PID             int64
	Usename         string
	ApplicationName string
	ClientAddr      string
	BackendStart    string
	XactStart       string
	State           string
	WaitEvent       string
	BlockingPIDs    string
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	pool, ok := s.loadPoolFromQuery(w, r)
	if !ok {
		return
	}

	activeOnly := r.URL.Query().Get("active_only") == "true"
	search := strings.ToLower(r.URL.Query().Get("search"))

	query := `
		SELECT
			a.pid::bigint,
			COALESCE(a.usename, ''),
			COALESCE(a.application_name, ''),
			COALESCE(a.client_addr::text, ''),
			COALESCE(a.backend_start::text, ''),
			COALESCE(a.xact_start::text, ''),
			COALESCE(a.state, ''),
			CASE WHEN a.wait_event_type IS NULL OR a.wait_event IS NULL
				THEN '' ELSE a.wait_event_type || ': ' || a.wait_event END,
			COALESCE((
				SELECT string_agg(DISTINCT blocker.pid::text, ', ')
				FROM pg_locks waiting
				JOIN pg_locks granted ON granted.locktype = waiting.locktype
					AND granted.relation = waiting.relation
					AND granted.page = waiting.page
					AND granted.tuple = waiting.tuple
					AND granted.granted
					AND granted.pid != waiting.pid
				JOIN pg_stat_activity blocker ON blocker.pid = granted.pid
				WHERE waiting.pid = a.pid AND NOT waiting.granted
			), '')
		FROM pg_stat_activity a
		WHERE a.pid != pg_backend_pid()
			AND (NOT $1 OR a.state = 'active')
		ORDER BY a.pid`

	rows, err := pool.Query(r.Context(), query, activeOnly)
	if err != nil {
		log.Printf("[sessions] Failed to query sessions: %v", err)
		http.Error(w, "Failed to query sessions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []sessionRow
	for rows.Next() {
		var row sessionRow
		if err := rows.Scan(&row.PID, &row.Usename, &row.ApplicationName, &row.ClientAddr,
			&row.BackendStart, &row.XactStart, &row.State, &row.WaitEvent,
			&row.BlockingPIDs); err != nil {
			log.Printf("[sessions] error scanning session row: %v", err)
			continue
		}
		if matchesSearch(search, strconv.FormatInt(row.PID, 10), row.Usename,
			row.ApplicationName, row.ClientAddr, row.State, row.WaitEvent) {
			results = append(results, row)
		}
	}

	RenderPartial(w, "sessions_rows.html", map[string]any{"Sessions": results})
}

func (s *Server) handleSessionCancel(w http.ResponseWriter, r *http.Request) {
	pool, pid, ok := s.loadSessionPool(w, r)
	if !ok {
		return
	}

	if _, err := pool.Exec(r.Context(), "SELECT pg_cancel_backend($1)", pid); err != nil {
		log.Printf("[sessions] cancel backend %d failed: %v", pid, err)
		http.Error(w, "Failed to cancel: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleSessionTerminate(w http.ResponseWriter, r *http.Request) {
	pool, pid, ok := s.loadSessionPool(w, r)
	if !ok {
		return
	}

	if _, err := pool.Exec(r.Context(), "SELECT pg_terminate_backend($1)", pid); err != nil {
		log.Printf("[sessions] terminate backend %d failed: %v", pid, err)
		http.Error(w, "Failed to terminate: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

// loadSessionPool resolves the target database pool and the {pid} path param
// for the session cancel/terminate endpoints. On failure it writes the error
// response itself and returns ok=false.
func (s *Server) loadSessionPool(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, int64, bool) {
	pool, ok := s.loadPoolFromQuery(w, r)
	if !ok {
		return nil, 0, false
	}

	pid, err := strconv.ParseInt(chi.URLParam(r, "pid"), 10, 64)
	if err != nil || pid < 1 {
		log.Printf("[sessions] Invalid PID %q on %s", chi.URLParam(r, "pid"), r.URL.Path)
		http.Error(w, "Invalid PID", http.StatusBadRequest)
		return nil, 0, false
	}

	return pool, pid, true
}

type lockRow struct {
	PID                  int64
	Locktype             string
	Relation             string
	Page                 string
	Tuple                string
	VirtualTransactionID string
	TransactionID        string
	ClassID              string
	ObjID                string
	VirtualXIDOwner      string
	Mode                 string
	Granted              bool
}

func (s *Server) handleLocks(w http.ResponseWriter, r *http.Request) {
	pool, ok := s.loadPoolFromQuery(w, r)
	if !ok {
		return
	}

	search := strings.ToLower(r.URL.Query().Get("search"))

	query := `
		SELECT
			l.pid::bigint,
			l.locktype,
			COALESCE(c.relname, '') AS relation,
			COALESCE(l.page::text, '') AS page,
			COALESCE(l.tuple::text, '') AS tuple,
			COALESCE(l.virtualxid, '') AS virtual_transaction_id,
			COALESCE(l.transactionid::text, '') AS transaction_id,
			COALESCE(l.classid::text, '') AS classid,
			COALESCE(l.objid::text, '') AS objid,
			COALESCE(l.virtualxid, '') AS virtual_xid_owner,
			l.mode,
			l.granted
		FROM pg_locks l
		LEFT JOIN pg_class c ON c.oid = l.relation
		ORDER BY l.pid`

	rows, err := pool.Query(r.Context(), query)
	if err != nil {
		log.Printf("[locks] Failed to query locks: %v", err)
		http.Error(w, "Failed to query locks: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []lockRow
	for rows.Next() {
		var row lockRow
		if err := rows.Scan(&row.PID, &row.Locktype, &row.Relation, &row.Page, &row.Tuple,
			&row.VirtualTransactionID, &row.TransactionID, &row.ClassID, &row.ObjID,
			&row.VirtualXIDOwner, &row.Mode, &row.Granted); err != nil {
			log.Printf("[locks] error scanning lock row: %v", err)
			continue
		}
		if matchesSearch(search, strconv.FormatInt(row.PID, 10), row.Locktype,
			row.Relation, row.Mode, strconv.FormatBool(row.Granted)) {
			results = append(results, row)
		}
	}

	RenderPartial(w, "locks_rows.html", map[string]any{"Locks": results})
}

type preparedTxRow struct {
	Name       string
	Owner      string
	XID        string
	PreparedAt string
}

func (s *Server) handlePreparedTransactions(w http.ResponseWriter, r *http.Request) {
	pool, ok := s.loadPoolFromQuery(w, r)
	if !ok {
		return
	}

	search := strings.ToLower(r.URL.Query().Get("search"))

	query := `
		SELECT
			COALESCE(gid, '') AS name,
			COALESCE(owner, '') AS owner,
			COALESCE(transaction::text, '') AS xid,
			COALESCE(prepared::text, '') AS prepared_at
		FROM pg_prepared_xacts
		ORDER BY prepared`

	rows, err := pool.Query(r.Context(), query)
	if err != nil {
		log.Printf("[prepared-transactions] query failed: %v", err)
		// pg_prepared_xacts requires superuser or pg_monitor membership.
		// Render an empty list on permission error rather than failing hard.
		RenderPartial(w, "prepared_rows.html", map[string]any{})
		return
	}
	defer rows.Close()

	var results []preparedTxRow
	for rows.Next() {
		var row preparedTxRow
		if err := rows.Scan(&row.Name, &row.Owner, &row.XID, &row.PreparedAt); err != nil {
			log.Printf("[prepared-transactions] error scanning row: %v", err)
			continue
		}
		if matchesSearch(search, row.Name, row.Owner, row.XID) {
			results = append(results, row)
		}
	}

	RenderPartial(w, "prepared_rows.html", map[string]any{"Prepared": results})
}

// writeJSON is the small shared helper for the endpoints that still answer
// with a JSON status object.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("Failed to encode JSON response: %v", err)
	}
}
