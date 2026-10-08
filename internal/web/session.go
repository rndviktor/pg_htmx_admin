package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Per-tab transactions. A script tab's connection is pinned exactly while it is
// inside a transaction: after every run the connection's transaction status
// decides whether it goes back to the pool ('I', idle) or stays pinned to the
// tab ('T' in a transaction, 'E' aborted). The next run on that tab reuses the
// pinned connection. Toolbar BEGIN/COMMIT/ROLLBACK, a typed BEGIN/SAVEPOINT and
// auto-commit-off therefore all work through the same rule, and idle tabs hold
// no connection.

const (
	txStateIdle    = "idle"
	txStateActive  = "active"
	txStateAborted = "aborted"
)

// txIdleTimeout is how long a pinned transaction may sit unused before the
// janitor rolls it back (a variable so tests can shorten it).
var txIdleTimeout = 15 * time.Minute

type tabSession struct {
	conn     *pgxpool.Conn
	pool     *pgxpool.Pool
	serverID int64
	db       string
	state    string
	lastUsed time.Time
	busy     bool
}

var (
	sessMu   sync.Mutex
	sessions = map[string]*tabSession{}
	// txNotes holds a one-shot message per tab (e.g. "rolled back after 15
	// minutes idle"), delivered by the next status request.
	txNotes = map[string]string{}
)

// acquireError carries the HTTP status for a refused acquire.
type acquireError struct {
	Status int
	Msg    string
}

func (e *acquireError) Error() string { return e.Msg }

func stateFromStatus(st byte) string {
	if st == 'E' {
		return txStateAborted
	}
	return txStateActive
}

// rollbackBestEffort ends a pinned transaction; a connection that cannot be
// rolled back is destroyed by pgxpool when released, which also ends it.
func rollbackBestEffort(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, "ROLLBACK"); err != nil {
		log.Printf("[tx] rollback failed: %v", err)
	}
}

// pinnedCountLocked counts the sessions pinned on pool. Caller holds sessMu.
func pinnedCountLocked(pool *pgxpool.Pool) int {
	n := 0
	for _, sess := range sessions {
		if sess.pool == pool {
			n++
		}
	}
	return n
}

// maxPinned is how many connections of a pool may be held by transactions: one
// is always left for ordinary queries and for the Stop button's cancel.
func maxPinned(pool *pgxpool.Pool) int {
	if n := int(pool.Config().MaxConns) - 1; n > 1 {
		return n
	}
	return 1
}

// acquireTabConn returns the connection a tab's query must run on and a finish
// func to call once the response is complete (it releases or pins the
// connection, see the package comment). With autocommit false a transaction is
// opened first if the connection is idle.
func acquireTabConn(ctx context.Context, tabID string, pool *pgxpool.Pool, serverID int64, dbName string, autocommit bool) (*pgxpool.Conn, func(), error) {
	var sess *tabSession
	if tabID != "" {
		sessMu.Lock()
		sess = sessions[tabID]
		if sess != nil {
			if sess.busy {
				sessMu.Unlock()
				return nil, nil, &acquireError{http.StatusConflict, "A query is already running in this tab."}
			}
			sess.busy = true
			sessMu.Unlock()
			return sess.conn, finishFunc(tabID, sess, sess.conn, pool, serverID, dbName), nil
		}
		if !autocommit && pinnedCountLocked(pool) >= maxPinned(pool) {
			sessMu.Unlock()
			return nil, nil, &acquireError{http.StatusTooManyRequests,
				"Too many open transactions on this database. Commit or roll back one in another tab first."}
		}
		sessMu.Unlock()
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !autocommit {
		if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
			conn.Release()
			return nil, nil, err
		}
	}
	return conn, finishFunc(tabID, nil, conn, pool, serverID, dbName), nil
}

// finishFunc builds the post-run hook: release the connection when its
// transaction is over, keep (or newly pin) it while one is open.
func finishFunc(tabID string, sess *tabSession, conn *pgxpool.Conn, pool *pgxpool.Pool, serverID int64, dbName string) func() {
	return func() {
		pc := conn.Conn()
		st := pc.PgConn().TxStatus()
		inTx := !pc.IsClosed() && (st == 'T' || st == 'E')

		sessMu.Lock()
		cur := sessions[tabID]
		// The session may have been removed while the query ran (server
		// disconnect, tab close); then the connection must not stay pinned.
		keep := inTx && tabID != "" && (sess == nil || cur == sess)
		if keep && sess == nil && pinnedCountLocked(pool) >= maxPinned(pool) {
			keep = false
			txNotes[tabID] = "The transaction was rolled back: too many open transactions on this database."
		}
		if keep {
			if sess == nil {
				sess = &tabSession{conn: conn, pool: pool, serverID: serverID, db: dbName}
				sessions[tabID] = sess
			}
			sess.state, sess.lastUsed, sess.busy = stateFromStatus(st), time.Now(), false
			sessMu.Unlock()
			return
		}
		if cur == sess && sess != nil {
			delete(sessions, tabID)
		}
		sessMu.Unlock()

		if inTx {
			rollbackBestEffort(conn)
		}
		conn.Release()
	}
}

// takeSessions removes the sessions matching match and returns the idle ones
// for the caller to end. A busy session (a query is running on its connection)
// is only unregistered when dropBusy is set; its handler's finish func then
// sees it was removed and rolls back and releases the connection itself.
func takeSessions(match func(tabID string, s *tabSession) bool, dropBusy bool) map[string]*tabSession {
	taken := map[string]*tabSession{}
	sessMu.Lock()
	defer sessMu.Unlock()
	for id, s := range sessions {
		if !match(id, s) {
			continue
		}
		switch {
		case !s.busy:
			taken[id] = s
			delete(sessions, id)
		case dropBusy:
			delete(sessions, id)
		}
	}
	return taken
}

func endSessions(taken map[string]*tabSession, note string) {
	for id, s := range taken {
		rollbackBestEffort(s.conn)
		s.conn.Release()
		sessMu.Lock()
		delete(cursors, id) // the rollback closed the tab's cursor, if any
		if note != "" {
			txNotes[id] = note
		}
		sessMu.Unlock()
	}
}

// releaseSessions rolls back and releases the pinned transactions of a server
// (dbName "" = every database of it). It must run before a pool is closed:
// pgxpool.Close blocks until every acquired connection has been returned.
func releaseSessions(serverID int64, dbName string) {
	endSessions(takeSessions(func(_ string, s *tabSession) bool {
		return s.serverID == serverID && (dbName == "" || s.db == dbName)
	}, true), "The transaction was rolled back because the connection was closed.")
}

// startSessionJanitor rolls back transactions left open and unused for longer
// than txIdleTimeout, so an abandoned tab cannot hold locks forever.
func startSessionJanitor() {
	go func() {
		for range time.Tick(time.Minute) {
			now := time.Now()
			// takeSessions calls match under sessMu, which also guards cursors.
			endSessions(takeSessions(func(id string, s *tabSession) bool {
				limit := txIdleTimeout
				if cursors[id] != nil && cursorIdleTimeout < limit {
					limit = cursorIdleTimeout // an idle cursor holds a snapshot
				}
				return s.lastUsed.Before(now.Add(-limit))
			}, false), "The transaction was rolled back after a period of inactivity ("+
				txIdleTimeout.String()+", or "+cursorIdleTimeout.String()+" with an open cursor).")
		}
	}()
}

// handleTxStatus is GET /api/tx/status?tab_id=...: the tab's transaction state
// (from memory, no database round trip) plus a one-shot note when its
// transaction was ended by the server.
func (s *Server) handleTxStatus(w http.ResponseWriter, r *http.Request) {
	tabID := r.URL.Query().Get("tab_id")
	state := txStateIdle
	sessMu.Lock()
	if sess := sessions[tabID]; sess != nil {
		state = sess.state
	}
	note := txNotes[tabID]
	delete(txNotes, tabID)
	sessMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"state": state, "note": note})
}

// handleTxClose is POST /api/tx/close {tab_id}: the tab was closed (or the page
// hidden), so its open transaction is rolled back and the connection released.
func (s *Server) handleTxClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID string `json:"tab_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TabID == "" {
		http.Error(w, "Missing tab_id", http.StatusBadRequest)
		return
	}
	endSessions(takeSessions(func(id string, _ *tabSession) bool { return id == req.TabID }, false), "")
	sessMu.Lock()
	delete(txNotes, req.TabID)
	sessMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
