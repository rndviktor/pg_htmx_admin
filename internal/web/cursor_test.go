package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB-backed tests need TEST_PG_DSN (see session_test.go) and are skipped
// without it.

const series25 = "SELECT g FROM generate_series(1, 25) g"

func cursorConn(t *testing.T, pool *pgxpool.Pool, tab string, autocommit bool) (*pgxpool.Conn, func()) {
	t.Helper()
	conn, finish, err := acquireTabConn(context.Background(), tab, pool, testServerID, "db", autocommit)
	if err != nil {
		t.Fatal(err)
	}
	return conn, finish
}

func cursorInMap(tab string) bool {
	sessMu.Lock()
	defer sessMu.Unlock()
	return cursors[tab] != nil
}

func TestDeclareOwnsTxAndEndCommits(t *testing.T) {
	pool := testPool(t, 4)
	conn, finish := cursorConn(t, pool, "c-own", true)
	cs, err := declareCursor(context.Background(), conn, "c-own", series25, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.ownsTx || cursorFor("c-own", conn) != cs {
		t.Fatalf("ownsTx = %v, cursorFor mismatch", cs.ownsTx)
	}
	finish()
	if got := sessionState("c-own"); got != txStateActive {
		t.Fatalf("with an open cursor state = %q, want active", got)
	}

	conn, finish = cursorConn(t, pool, "c-own", true)
	endCursor(conn, "c-own", cs)
	finish()
	if cursorInMap("c-own") {
		t.Fatal("cursor still registered after endCursor")
	}
	if got := sessionState("c-own"); got != txStateIdle {
		t.Fatalf("after endCursor state = %q, want idle", got)
	}
}

func TestDeclareInsideUserTxDoesNotOwn(t *testing.T) {
	pool := testPool(t, 4)
	conn, finish := cursorConn(t, pool, "c-user", true)
	if err := run(t, conn, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	cs, err := declareCursor(context.Background(), conn, "c-user", series25, pool)
	if err != nil {
		t.Fatal(err)
	}
	if cs.ownsTx {
		t.Fatal("cursor claims to own the user's transaction")
	}
	endCursor(conn, "c-user", cs)
	if got := conn.Conn().PgConn().TxStatus(); got != 'T' {
		t.Fatalf("TxStatus = %q after endCursor, want the user's transaction still open", got)
	}
	run(t, conn, "ROLLBACK")
	finish()
}

func TestFetchCursorPagePaging(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()
	conn, finish := cursorConn(t, pool, "c-page", true)
	defer finish()
	cs, err := declareCursor(ctx, conn, "c-page", series25, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer endCursor(conn, "c-page", cs)

	// offset, rows wanted (limit+1), rows returned, first value
	for _, c := range []struct{ offset, n, rows int }{
		{0, 11, 11}, {10, 11, 11}, {20, 11, 5}, {0, 11, 11}, // the last one scrolls back
	} {
		got, err := fetchCursorPage(ctx, conn, c.offset, c.n)
		if err != nil {
			t.Fatalf("offset %d: %v", c.offset, err)
		}
		if len(got.rows) != c.rows {
			t.Fatalf("offset %d: %d rows, want %d", c.offset, len(got.rows), c.rows)
		}
		if want := fmt.Sprint(c.offset + 1); got.rows[0][0] != want {
			t.Fatalf("offset %d: first value %s, want %s", c.offset, got.rows[0][0], want)
		}
	}
}

func TestCursorTotal(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()
	conn, finish := cursorConn(t, pool, "c-total", true)
	defer finish()
	cs, err := declareCursor(ctx, conn, "c-total", series25, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer endCursor(conn, "c-total", cs)

	if n, err := cursorTotal(ctx, conn); err != nil || n != 25 {
		t.Fatalf("cursorTotal = %d, %v; want 25", n, err)
	}
	got, err := fetchCursorPage(ctx, conn, 0, 5)
	if err != nil || len(got.rows) != 5 || got.rows[0][0] != "1" {
		t.Fatalf("fetch after total: %v rows=%v", err, got.rows)
	}
}

func TestInvalidCursorAfterCommit(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()
	conn, finish := cursorConn(t, pool, "c-gone", true)
	defer finish()
	cs, err := declareCursor(ctx, conn, "c-gone", series25, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t, conn, "COMMIT"); err != nil { // the user ends the transaction
		t.Fatal(err)
	}
	if _, err := fetchCursorPage(ctx, conn, 0, 5); !isInvalidCursor(err) {
		t.Fatalf("err = %v, want invalid cursor name", err)
	}
	endCursor(conn, "c-gone", cs) // must not fail or leave state behind
	if cursorInMap("c-gone") {
		t.Fatal("cursor still registered")
	}
}

func TestCursorForDropsStaleConn(t *testing.T) {
	pool := testPool(t, 4)
	conn, finish := cursorConn(t, pool, "c-stale", true)
	cs, err := declareCursor(context.Background(), conn, "c-stale", series25, pool)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if cursorFor("c-stale", &pgxpool.Conn{}) != nil {
		t.Fatal("cursor returned for a different connection")
	}
	if cursorInMap("c-stale") {
		t.Fatal("stale cursor was not dropped")
	}
	_ = cs
}

func TestEndSessionsDropsCursor(t *testing.T) {
	pool := testPool(t, 4)
	conn, finish := cursorConn(t, pool, "c-end", true)
	if _, err := declareCursor(context.Background(), conn, "c-end", series25, pool); err != nil {
		t.Fatal(err)
	}
	finish()
	releaseSessions(testServerID, "")
	if cursorInMap("c-end") {
		t.Fatal("cursor survived its session")
	}
}

func TestPinnedLimitRefusesCursor(t *testing.T) {
	pool := testPool(t, 3) // at most 2 pinned
	ctx := context.Background()
	for _, tab := range []string{"c-p1", "c-p2"} {
		_, finish := cursorConn(t, pool, tab, false)
		finish()
	}
	conn, finish := cursorConn(t, pool, "c-p3", true)
	defer finish()
	_, err := declareCursor(ctx, conn, "c-p3", series25, pool)
	var ae *acquireError
	if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want 429", err)
	}
}

func TestIsInvalidCursor(t *testing.T) {
	pgErr := &pgconn.PgError{Code: pgInvalidCursorName}
	for _, c := range []struct {
		err  error
		want bool
	}{
		{pgErr, true},
		{fmt.Errorf("wrapped: %w", pgErr), true},
		{&pgconn.PgError{Code: "42601"}, false},
		{errors.New("other"), false},
		{nil, false},
	} {
		if got := isInvalidCursor(c.err); got != c.want {
			t.Errorf("isInvalidCursor(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
