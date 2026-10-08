package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Server-side cursors (opt-in per tab, "cursor" toggle in the toolbar). The
// default paging re-runs the whole query for every page (COUNT(*) plus
// LIMIT/OFFSET). In cursor mode the first run declares one scrollable cursor
// and later pages are MOVE/FETCH on it, so the query executes once.
//
// A cursor lives only inside a transaction, so the tab's connection stays
// pinned (session.go) while it is open: the tab shows "In transaction", the
// transaction holds a snapshot, and the janitor ends it after
// cursorIdleTimeout. If the tab was not already in a transaction, this code
// opened one (ownsTx) and commits it when the cursor is closed. Any run on the
// tab that is not a page of the same query closes the cursor first, so a later
// statement never executes inside a transaction the user did not ask for.
// There is one cursor per tab.

const (
	cursorName = "pgh_cur"
	// cursorIdleTimeout is shorter than txIdleTimeout: an idle cursor holds a
	// snapshot that keeps VACUUM from cleaning up.
	cursorIdleTimeout = 5 * time.Minute
	// pgInvalidCursorName is SQLSTATE 34000: the cursor is gone, e.g. the user
	// committed or rolled back the transaction it lived in.
	pgInvalidCursorName = "34000"
)

type cursorState struct {
	query  string
	conn   *pgxpool.Conn // the connection the cursor lives on
	ownsTx bool          // this code opened the transaction and must end it
}

// cursors holds the tab -> open cursor map. It is guarded by sessMu together
// with the pinned sessions it belongs to.
var cursors = map[string]*cursorState{}

// cursorFor returns the tab's cursor if it still lives on conn, else nil.
func cursorFor(tabID string, conn *pgxpool.Conn) *cursorState {
	sessMu.Lock()
	defer sessMu.Unlock()
	cs := cursors[tabID]
	if cs != nil && cs.conn != conn {
		delete(cursors, tabID) // its session was replaced: the cursor is gone
		return nil
	}
	return cs
}

// endCursor closes the cursor and, if this code opened the transaction, ends
// it. Errors are ignored: a failed CLOSE means the cursor is already gone.
func endCursor(conn *pgxpool.Conn, tabID string, cs *cursorState) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn.Exec(ctx, "CLOSE "+cursorName)
	if cs.ownsTx {
		conn.Exec(ctx, "COMMIT")
	}
	sessMu.Lock()
	if cursors[tabID] == cs {
		delete(cursors, tabID)
	}
	sessMu.Unlock()
}

// declareCursor opens the tab's cursor on query, starting a transaction first
// when the connection is idle.
func declareCursor(ctx context.Context, conn *pgxpool.Conn, tabID, query string, pool *pgxpool.Pool) (*cursorState, error) {
	owns := false
	if conn.Conn().PgConn().TxStatus() == 'I' {
		sessMu.Lock()
		full := pinnedCountLocked(pool) >= maxPinned(pool)
		sessMu.Unlock()
		if full {
			return nil, &acquireError{http.StatusTooManyRequests,
				"Too many open transactions on this database. Commit or roll back one in another tab first."}
		}
		if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
			return nil, err
		}
		owns = true
	}
	// The newline keeps a trailing "-- comment" from swallowing anything.
	if _, err := conn.Exec(ctx, "DECLARE "+cursorName+" SCROLL CURSOR WITHOUT HOLD FOR "+query+"\n"); err != nil {
		if owns {
			conn.Exec(ctx, "ROLLBACK")
		}
		return nil, err
	}
	cs := &cursorState{query: query, conn: conn, ownsTx: owns}
	sessMu.Lock()
	cursors[tabID] = cs
	sessMu.Unlock()
	return cs, nil
}

// cursorRows are the rows of one fetched page.
type cursorRows struct {
	headers    []string
	rows       [][]string
	formatTime time.Duration
}

// fetchCursorPage positions the cursor after `offset` rows and reads up to n.
func fetchCursorPage(ctx context.Context, conn *pgxpool.Conn, offset, n int) (cursorRows, error) {
	var out cursorRows
	if _, err := conn.Exec(ctx, fmt.Sprintf("MOVE ABSOLUTE %d FROM %s", offset, cursorName)); err != nil {
		return out, err
	}
	rows, err := conn.Query(ctx, fmt.Sprintf("FETCH FORWARD %d FROM %s", n, cursorName))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for _, fd := range rows.FieldDescriptions() {
		out.headers = append(out.headers, fd.Name)
	}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return out, err
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
		out.rows = append(out.rows, row)
		out.formatTime += time.Since(fmtStart)
	}
	return out, rows.Err()
}

// cursorTotal counts the cursor's rows by moving over all of them once.
func cursorTotal(ctx context.Context, conn *pgxpool.Conn) (int, error) {
	if _, err := conn.Exec(ctx, "MOVE ABSOLUTE 0 FROM "+cursorName); err != nil {
		return 0, err
	}
	tag, err := conn.Exec(ctx, "MOVE FORWARD ALL FROM "+cursorName)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func isInvalidCursor(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgInvalidCursorName
}

// renderCursorPage serves one page of a cursor-mode run. page "last" first
// counts the rows. The query is fetched one row past the page so the UI knows
// whether another page exists without counting everything up front.
func (s *Server) renderCursorPage(w http.ResponseWriter, r *http.Request, conn *pgxpool.Conn, pool *pgxpool.Pool,
	tabID, query string, page, limit int, last bool, serverID int64, dbName string, notices *noticeSink, start time.Time) {

	ctx := r.Context()
	fail := func(err error) {
		log.Printf("[query] cursor page failed on server %d db %q: %v", serverID, dbName, err)
		s.recordQueryHistory(ctx, serverID, dbName, tabID, query, time.Since(start).Milliseconds(), 0, queryStatus(err))
		var ae *acquireError
		if errors.As(err, &ae) {
			http.Error(w, ae.Msg, ae.Status)
			return
		}
		http.Error(w, "Query failed: "+err.Error(), http.StatusBadRequest)
	}

	cs := cursorFor(tabID, conn)
	if cs == nil {
		var err error
		if cs, err = declareCursor(ctx, conn, tabID, query, pool); err != nil {
			fail(err)
			return
		}
	}

	var total int
	totalKnown := false
	if last {
		n, err := cursorTotal(ctx, conn)
		if err != nil {
			fail(err)
			return
		}
		total, totalKnown = n, true
		page = (n + limit - 1) / limit
		if page < 1 {
			page = 1
		}
	}
	offset := (page - 1) * limit

	got, err := fetchCursorPage(ctx, conn, offset, limit+1)
	if isInvalidCursor(err) { // the user ended the transaction: declare again
		endCursor(conn, tabID, cs)
		if cs, err = declareCursor(ctx, conn, tabID, query, pool); err == nil {
			got, err = fetchCursorPage(ctx, conn, offset, limit+1)
		}
	}
	if err != nil {
		fail(err)
		return
	}

	hasMore := len(got.rows) > limit
	if hasMore {
		got.rows = got.rows[:limit]
	}
	if !hasMore {
		total, totalKnown = offset+len(got.rows), true
	} else if !totalKnown {
		total = offset + len(got.rows)
	}
	totalPages := page
	if hasMore {
		totalPages = page + 1
	}

	elapsed := (time.Since(start) - got.formatTime).Seconds()
	s.recordQueryHistory(ctx, serverID, dbName, tabID, query, int64(elapsed*1000), int64(len(got.rows)), historySuccess)

	RenderPartial(w, "query_result.html", map[string]any{
		"Headers": got.headers, "Rows": got.rows, "Page": page, "Total": total,
		"TotalPages": totalPages, "Limit": limit, "Offset": offset, "Elapsed": elapsed,
		"Message": "", "Notices": notices.JSON(), "Unknown": !totalKnown, "Cursor": true,
	})
}
