package web

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

// NOTICE / WARNING capture. PostgreSQL delivers notices (RAISE NOTICE, "table
// does not exist, skipping", ...) asynchronously on the connection that ran
// the statement. onNotice is installed on every pool at dial time
// (dialServer); while a query runs the handler registers a sink for the
// connection, and the notices raised during that run are returned with the
// result for the Notifications tab. Anything arriving outside a run is dropped.

// noticeSinks maps a connection to the sink collecting its notices. It is keyed
// by the *pgconn.PgConn, not the backend PID, because PIDs collide across
// servers.
var noticeSinks sync.Map

type noticeSink struct {
	mu    sync.Mutex
	items []string
}

func (n *noticeSink) add(s string) {
	n.mu.Lock()
	n.items = append(n.items, s)
	n.mu.Unlock()
}

// JSON returns the collected notices as a JSON array ("" when there are none),
// ready for a data attribute.
func (n *noticeSink) JSON() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.items) == 0 {
		return ""
	}
	b, err := json.Marshal(n.items)
	if err != nil {
		return ""
	}
	return string(b)
}

// watchNotices starts collecting the notices of one connection; the returned
// func stops it.
func watchNotices(pc *pgconn.PgConn) (*noticeSink, func()) {
	sink := &noticeSink{}
	noticeSinks.Store(pc, sink)
	return sink, func() { noticeSinks.Delete(pc) }
}

// onNotice is the pgconn OnNotice callback installed on every pool.
func onNotice(pc *pgconn.PgConn, n *pgconn.Notice) {
	if v, ok := noticeSinks.Load(pc); ok {
		v.(*noticeSink).add(formatNotice(n))
	}
}

// formatNotice renders "SEVERITY: message" followed by any detail and hint.
func formatNotice(n *pgconn.Notice) string {
	var b strings.Builder
	b.WriteString(n.Severity)
	b.WriteString(": ")
	b.WriteString(n.Message)
	if n.Detail != "" {
		b.WriteString("\nDETAIL: ")
		b.WriteString(n.Detail)
	}
	if n.Hint != "" {
		b.WriteString("\nHINT: ")
		b.WriteString(n.Hint)
	}
	return b.String()
}
