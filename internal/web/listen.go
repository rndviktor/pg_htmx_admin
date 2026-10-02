package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LISTEN / NOTIFY streaming. LISTEN registrations belong to one backend
// connection, so a pooled connection (released after every run) cannot hold
// them. Instead each tab that LISTENs gets one dedicated connection, opened
// outside the pool from the pool's own connection settings. A goroutine waits
// for notifications on it and fans them out to the tab's Server-Sent-Events
// streams, which the browser shows in the Notifications tab. NOTIFY itself is
// an ordinary statement and runs on the tab's normal connection.
//
// The listener closes on UNLISTEN of the last channel, tab close, server or
// database disconnect, a lost connection, or after listenIdleTimeout without a
// subscribed browser stream.

const (
	// listenPoll is how long one wait for a notification lasts; commands on the
	// connection (LISTEN/UNLISTEN) get in between waits, so they can be delayed
	// by up to this long.
	listenPoll = 500 * time.Millisecond
	// maxListeners bounds the dedicated connections, which count against the
	// server's max_connections and are not part of any pool.
	maxListeners      = 10
	listenIdleTimeout = 30 * time.Minute
)

type notifyEvent struct {
	Channel string `json:"channel"`
	Payload string `json:"payload"`
	PID     uint32 `json:"pid"`
	Time    string `json:"time"`
}

type listener struct {
	tabID    string
	serverID int64
	db       string
	conn     *pgx.Conn

	cmdMu   sync.Mutex // serializes the wait loop and commands on conn
	pending int32      // commands waiting for cmdMu; the wait loop yields to them

	mu       sync.Mutex // guards the fields below
	channels map[string]struct{}
	subs     map[chan notifyEvent]struct{}
	lastSeen time.Time
	closed   bool
	// cancelWait aborts the wait in progress so a command need not sit out the
	// rest of the poll interval.
	cancelWait context.CancelFunc

	done chan struct{} // closed when the listener ends
}

var (
	listenMu  sync.Mutex
	listeners = map[string]*listener{}
)

// listenerFor returns the tab's active listener, or nil.
func listenerFor(tabID string) *listener {
	listenMu.Lock()
	defer listenMu.Unlock()
	return listeners[tabID]
}

// getListener returns the tab's listener, opening its dedicated connection on
// first use.
func getListener(ctx context.Context, tabID string, pool *pgxpool.Pool, serverID int64, dbName string) (*listener, error) {
	listenMu.Lock()
	if l := listeners[tabID]; l != nil {
		listenMu.Unlock()
		return l, nil
	}
	if len(listeners) >= maxListeners {
		listenMu.Unlock()
		return nil, fmt.Errorf("too many listening tabs (limit %d); UNLISTEN in another tab first", maxListeners)
	}
	listenMu.Unlock()

	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("cannot open the listener connection: %w", err)
	}
	l := &listener{
		tabID: tabID, serverID: serverID, db: dbName, conn: conn,
		channels: map[string]struct{}{}, subs: map[chan notifyEvent]struct{}{},
		lastSeen: time.Now(), done: make(chan struct{}),
	}

	listenMu.Lock()
	if existing := listeners[tabID]; existing != nil { // lost a race
		listenMu.Unlock()
		conn.Close(context.Background())
		return existing, nil
	}
	listeners[tabID] = l
	listenMu.Unlock()

	go l.run()
	return l, nil
}

// run waits for notifications until the listener ends.
func (l *listener) run() {
	for {
		select {
		case <-l.done:
			return
		default:
		}

		l.cmdMu.Lock()
		if atomic.LoadInt32(&l.pending) > 0 { // a command is waiting: let it in
			l.cmdMu.Unlock()
			time.Sleep(time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), listenPoll)
		l.mu.Lock()
		l.cancelWait = cancel
		l.mu.Unlock()
		n, err := l.conn.WaitForNotification(ctx)
		timedOut := ctx.Err() != nil
		cancel()
		l.cmdMu.Unlock()

		if err != nil {
			select {
			case <-l.done: // closed on purpose, not a failure
				return
			default:
			}
			if timedOut || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			log.Printf("[listen] connection of tab %s lost: %v", l.tabID, err)
			l.end("The listener connection was lost: " + err.Error())
			return
		}
		l.broadcast(notifyEvent{
			Channel: n.Channel, Payload: n.Payload, PID: n.PID,
			Time: time.Now().Format("15:04:05"),
		})
	}
}

func (l *listener) broadcast(ev notifyEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ch := range l.subs {
		select {
		case ch <- ev:
		default: // a slow browser drops events rather than blocking the loop
		}
	}
}

// subscribe returns a channel of events and a func to stop receiving.
func (l *listener) subscribe() (chan notifyEvent, func()) {
	ch := make(chan notifyEvent, 64)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.lastSeen = time.Now()
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.lastSeen = time.Now()
		l.mu.Unlock()
	}
}

// lockCmd takes cmdMu for a command, cutting the current wait short and making
// the wait loop yield, so commands are not delayed by the poll interval.
func (l *listener) lockCmd() {
	atomic.AddInt32(&l.pending, 1)
	l.mu.Lock()
	cancel := l.cancelWait
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	l.cmdMu.Lock()
	atomic.AddInt32(&l.pending, -1)
}

// exec runs a command on the listener connection between waits.
func (l *listener) exec(ctx context.Context, sql string) error {
	l.lockCmd()
	defer l.cmdMu.Unlock()
	_, err := l.conn.Exec(ctx, sql)
	return err
}

// end closes the connection and unregisters the listener; safe to call twice.
// Subscribed streams see done closed.
func (l *listener) end(reason string) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.mu.Unlock()

	listenMu.Lock()
	if listeners[l.tabID] == l {
		delete(listeners, l.tabID)
	}
	listenMu.Unlock()
	close(l.done)

	l.lockCmd() // let an in-flight wait or command finish first
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	l.conn.Close(ctx)
	cancel()
	l.cmdMu.Unlock()
	log.Printf("[listen] tab %s listener ended: %s", l.tabID, reason)
}

// listen / unlisten run the statement and track the channel set. UNLISTEN of
// the last channel ends the listener.
func (l *listener) listen(ctx context.Context, channel string) error {
	if err := l.exec(ctx, "LISTEN "+quoteIdent(channel)); err != nil {
		return err
	}
	l.mu.Lock()
	l.channels[channel] = struct{}{}
	l.mu.Unlock()
	return nil
}

func (l *listener) unlisten(ctx context.Context, channel string) error {
	sql := "UNLISTEN *"
	if channel != "*" {
		sql = "UNLISTEN " + quoteIdent(channel)
	}
	if err := l.exec(ctx, sql); err != nil {
		return err
	}
	l.mu.Lock()
	if channel == "*" {
		l.channels = map[string]struct{}{}
	} else {
		delete(l.channels, channel)
	}
	empty := len(l.channels) == 0
	l.mu.Unlock()
	if empty {
		l.end("UNLISTEN")
	}
	return nil
}

// closeListener ends a tab's listener.
func closeListener(tabID string) {
	if l := listenerFor(tabID); l != nil {
		l.end("closed")
	}
}

// closeListeners ends the listeners of a server (dbName "" = all its databases).
func closeListeners(serverID int64, dbName string) {
	listenMu.Lock()
	var doomed []*listener
	for _, l := range listeners {
		if l.serverID == serverID && (dbName == "" || l.db == dbName) {
			doomed = append(doomed, l)
		}
	}
	listenMu.Unlock()
	for _, l := range doomed {
		l.end("server disconnected")
	}
}

// startListenJanitor ends listeners nobody has been watching for a long time.
func startListenJanitor() {
	go func() {
		for range time.Tick(time.Minute) {
			cutoff := time.Now().Add(-listenIdleTimeout)
			listenMu.Lock()
			var doomed []*listener
			for _, l := range listeners {
				l.mu.Lock()
				if len(l.subs) == 0 && l.lastSeen.Before(cutoff) {
					doomed = append(doomed, l)
				}
				l.mu.Unlock()
			}
			listenMu.Unlock()
			for _, l := range doomed {
				l.end("idle")
			}
		}
	}()
}

// parseListen parses `LISTEN channel`, `UNLISTEN channel` and `UNLISTEN *`.
// Unquoted channel names fold to lower case like any identifier.
func parseListen(stmt string) (kind, channel string, err error) {
	fields := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(stmt), ";"))
	idx := strings.IndexAny(fields, " \t\r\n")
	if idx < 0 {
		return "", "", errors.New("A channel name is required")
	}
	kind = strings.ToUpper(fields[:idx])
	arg := strings.TrimSpace(fields[idx:])

	switch {
	case arg == "*" && kind == "UNLISTEN":
		return kind, "*", nil
	case len(arg) >= 2 && arg[0] == '"' && arg[len(arg)-1] == '"':
		name := strings.ReplaceAll(arg[1:len(arg)-1], `""`, `"`)
		if name == "" || strings.Contains(strings.ReplaceAll(arg[1:len(arg)-1], `""`, ""), `"`) {
			return "", "", errors.New("Invalid channel name")
		}
		return kind, name, nil
	case safeIdent.MatchString(strings.ToLower(arg)):
		return kind, strings.ToLower(arg), nil
	}
	return "", "", errors.New("Unsupported LISTEN syntax; use LISTEN channel or UNLISTEN channel | *")
}

// listenIntercept returns the hook that routes LISTEN/UNLISTEN statements to the
// tab's dedicated listener instead of a pooled connection. ok is false for any
// other statement.
func listenIntercept(ctx context.Context, tabID string, pool *pgxpool.Pool, serverID int64, dbName string) func(string) (string, bool, error) {
	return func(stmt string) (string, bool, error) {
		kw := firstKeyword(stmt)
		if kw != "LISTEN" && kw != "UNLISTEN" {
			return "", false, nil
		}
		if tabID == "" {
			return kw, true, errors.New("LISTEN needs a script tab")
		}
		kind, channel, err := parseListen(stmt)
		if err != nil {
			return kind, true, err
		}
		if kind == "LISTEN" {
			l, err := getListener(ctx, tabID, pool, serverID, dbName)
			if err != nil {
				return kind, true, err
			}
			return kind, true, l.listen(ctx, channel)
		}
		if l := listenerFor(tabID); l != nil {
			return kind, true, l.unlisten(ctx, channel)
		}
		return kind, true, nil // nothing to stop
	}
}

// renderCommandResult renders a command-tag style result (used for a lone
// LISTEN/UNLISTEN), records it in history and flags an active listener.
func (s *Server) renderCommandResult(w http.ResponseWriter, r *http.Request, serverID int64, dbName, tabID, query string, start time.Time, limit int, tag string, err error) {
	message := tag
	if err != nil {
		message = err.Error()
	}
	elapsed := time.Since(start)
	s.recordQueryHistory(r.Context(), serverID, dbName, tabID, query, elapsed.Milliseconds(), 0, queryStatus(err))
	RenderPartial(w, "query_result.html", map[string]any{
		"Headers": []string{}, "Rows": [][]string{}, "Page": 1, "Total": 0, "TotalPages": 1,
		"Limit": limit, "Offset": 0, "Elapsed": elapsed.Seconds(), "Message": message,
		"IsError": err != nil, "Listening": listenerFor(tabID) != nil,
	})
}

// handleListenStream is GET /api/listen/stream?tab_id=...: Server-Sent Events
// with one JSON notification per event, and a final "closed" event when the
// listener ends.
func (s *Server) handleListenStream(w http.ResponseWriter, r *http.Request) {
	l := listenerFor(r.URL.Query().Get("tab_id"))
	if l == nil {
		http.Error(w, "This tab is not listening", http.StatusNotFound)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	events, unsubscribe := l.subscribe()
	defer unsubscribe()

	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	fl.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-l.done:
			w.Write([]byte("event: closed\ndata: listener closed\n\n"))
			fl.Flush()
			return
		case ev := <-events:
			b, _ := json.Marshal(ev)
			if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// handleListenClose is POST /api/listen/close {tab_id}: the tab was closed (or
// the page hidden), so its listener connection is dropped.
func (s *Server) handleListenClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID string `json:"tab_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TabID == "" {
		http.Error(w, "Missing tab_id", http.StatusBadRequest)
		return
	}
	closeListener(req.TabID)
	w.WriteHeader(http.StatusNoContent)
}
