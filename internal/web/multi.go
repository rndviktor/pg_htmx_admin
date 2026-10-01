package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Multiple result sets. A script with several statements runs them in order on
// the tab's one connection and shows one result tab per statement, like psql
// run in a GUI. Statements run exactly as typed (each autocommits unless the
// tab is in a transaction), execution stops at the first error, and every
// result set shows at most `limit` rows: a script's statements are not wrapped
// for COUNT/OFFSET paging, so paging only applies to single-statement runs.

// resultSet is one statement's outcome.
type resultSet struct {
	Label     string
	Message   string // command tag or error text; empty for row results
	IsError   bool
	Headers   []string
	Rows      [][]string
	Total     int // rows the statement returned (>= len(Rows) when truncated)
	Truncated bool
}

// scriptStatements splits a script into its executable statements, dropping
// ones that are only "--" comments. A result of 0 or 1 statements means the
// normal single-statement path applies.
func scriptStatements(script string) []string {
	var stmts []string
	for _, st := range splitStatements(script) {
		if stripLeadingComments(strings.TrimSpace(st)) != "" {
			stmts = append(stmts, st)
		}
	}
	return stmts
}

// runStatement executes one statement and collects up to limit rows. It
// returns the result set, the rows touched (returned or affected) and the time
// spent turning values into strings (not database time).
func runStatement(ctx context.Context, conn *pgxpool.Conn, n int, stmt string, limit int) (resultSet, int64, time.Duration, error) {
	set := resultSet{Label: fmt.Sprintf("%d · %s", n, firstKeyword(stmt))}
	var formatTime time.Duration

	rows, err := conn.Query(ctx, stmt)
	if err != nil {
		set.Message, set.IsError = err.Error(), true
		return set, 0, 0, err
	}
	fields := rows.FieldDescriptions()
	for _, fd := range fields {
		set.Headers = append(set.Headers, fd.Name)
	}
	for rows.Next() {
		set.Total++
		if len(set.Rows) >= limit {
			set.Truncated = true // keep counting, but stop collecting
			continue
		}
		vals, verr := rows.Values()
		if verr != nil {
			rows.Close()
			set.Message, set.IsError = verr.Error(), true
			return set, 0, formatTime, verr
		}
		fmtStart := time.Now()
		row := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				row[i] = "NULL"
			} else {
				row[i] = fmt.Sprintf("%v", v)
			}
		}
		set.Rows = append(set.Rows, row)
		formatTime += time.Since(fmtStart)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		set.Message, set.IsError = err.Error(), true
		return set, 0, formatTime, err
	}

	if len(fields) == 0 { // no result set: show the command tag
		tag := rows.CommandTag()
		set.Message = tag.String()
		return set, tag.RowsAffected(), formatTime, nil
	}
	set.Label += fmt.Sprintf(" (%d rows)", set.Total)
	return set, int64(set.Total), formatTime, nil
}

// renderMulti runs a multi-statement script and renders its result sets.
func (s *Server) renderMulti(w http.ResponseWriter, r *http.Request, conn *pgxpool.Conn, stmts []string, serverID int64, dbName, tabID, script string, limit int, notices *noticeSink, start time.Time) {
	sets := make([]resultSet, 0, len(stmts))
	var touched int64
	var formatTime time.Duration
	var failed error
	failedAt := 0

	for i, stmt := range stmts {
		set, n, ft, err := runStatement(r.Context(), conn, i+1, stmt, limit)
		sets = append(sets, set)
		touched += n
		formatTime += ft
		if err != nil {
			failed, failedAt = err, i+1
			break
		}
	}

	elapsed := (time.Since(start) - formatTime).Seconds()
	s.recordQueryHistory(r.Context(), serverID, dbName, tabID, script, int64(elapsed*1000), touched, queryStatus(failed))

	message := fmt.Sprintf("%d statements executed", len(stmts))
	if failed != nil {
		log.Printf("[query] script failed at statement %d on server %d db %q: %v", failedAt, serverID, dbName, failed)
		message = fmt.Sprintf("Statement %d of %d failed: %s", failedAt, len(stmts), failed)
		if rest := len(stmts) - failedAt; rest > 0 {
			message += fmt.Sprintf(" (%d not run)", rest)
		}
	}

	RenderPartial(w, "query_multi.html", map[string]any{
		"Sets":    sets,
		"Total":   touched,
		"Limit":   limit,
		"Elapsed": elapsed,
		"Message": message,
		"IsError": failed != nil,
		"Notices": notices.JSON(),
	})
}
