package web

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleExportCSV streams the complete result of a row-returning query as CSV
// (header row included), not just the page shown in the grid. The server uses
// COPY ... TO STDOUT, so rows flow from PostgreSQL to the browser without being
// buffered; statements COPY cannot wrap (SHOW, EXPLAIN) are rejected.
func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	query := strings.TrimRight(r.FormValue("sql_query"), " \t\n\r;")
	dbName := r.FormValue("db_name")
	tabID := r.FormValue("tab_id")
	serverID, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	if query == "" || dbName == "" || err != nil || serverID < 1 {
		http.Error(w, "Missing query, server_id, or db_name", http.StatusBadRequest)
		return
	}
	if !isRowReturning(query) || isExplain(query) || firstKeyword(query) == "SHOW" {
		http.Error(w, "Only SELECT, VALUES, TABLE and WITH queries can be downloaded as CSV", http.StatusBadRequest)
		return
	}

	pool, err := s.getOrCreateDbPool(r.Context(), serverID, dbName)
	if err != nil {
		log.Printf("[export] cannot connect to server %d db %q: %v", serverID, dbName, err)
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return
	}
	conn, err := pool.Acquire(r.Context())
	if err != nil {
		log.Printf("[export] pool acquire for server %d db %q failed: %v", serverID, dbName, err)
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Release()

	// Registered so the Stop button can cancel a long export like any query.
	if tabID != "" {
		s.registerQuery(tabID, conn.Conn().PgConn().PID(), pool)
		defer s.unregisterQuery(tabID)
	}

	// The newline before ")" keeps a trailing "-- comment" from swallowing it.
	copySQL := "COPY (" + query + "\n) TO STDOUT WITH (FORMAT csv, HEADER true)"

	out := &firstWriteHeaders{w: w, filename: "query_result_" + time.Now().Format("20060102_150405") + ".csv"}
	if _, err := conn.Conn().PgConn().CopyTo(r.Context(), out, copySQL); err != nil {
		log.Printf("[export] CSV export failed on server %d db %q: %v", serverID, dbName, err)
		if !out.started {
			http.Error(w, "Export failed: "+err.Error(), http.StatusBadRequest)
		}
		// Once bytes are on the wire the status is already 200; the truncated
		// download is the only signal left, so the error is only logged.
	}
}

// firstWriteHeaders sets the CSV download headers just before the first byte
// is written, so an error raised before any data can still be reported as a
// normal HTTP error response.
type firstWriteHeaders struct {
	w        http.ResponseWriter
	filename string
	started  bool
}

func (f *firstWriteHeaders) Write(p []byte) (int, error) {
	if !f.started {
		f.started = true
		h := f.w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+f.filename+`"`)
	}
	return f.w.Write(p)
}

var _ io.Writer = (*firstWriteHeaders)(nil)

// firstKeyword returns the upper-cased first word of a query after leading
// comments, as used by isRowReturning.
func firstKeyword(query string) string {
	trimmed := stripLeadingComments(strings.TrimSpace(strings.ToUpper(query)))
	if idx := strings.IndexAny(trimmed, " \t\n\r"); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}
