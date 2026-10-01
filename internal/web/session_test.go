package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a real PostgreSQL server: set TEST_PG_DSN, e.g.
// postgres://root:rootpassword@localhost:5432/mydatabase?sslmode=disable
// Without it they are skipped.

const testServerID = 987654

func testPool(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = maxConns
	cfg.ConnConfig.OnNotice = onNotice
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseSessions(testServerID, "")
		pool.Close()
	})
	return pool
}

func run(t *testing.T, conn *pgxpool.Conn, sql string) error {
	t.Helper()
	_, err := conn.Exec(context.Background(), sql)
	return err
}

func sessionState(tab string) string {
	sessMu.Lock()
	defer sessMu.Unlock()
	if s := sessions[tab]; s != nil {
		return s.state
	}
	return txStateIdle
}

func TestBeginPinsAndCommitReleases(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()

	conn, finish, err := acquireTabConn(ctx, "t-pin", pool, testServerID, "db", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t, conn, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	finish()
	if got := sessionState("t-pin"); got != txStateActive {
		t.Fatalf("after BEGIN state = %q, want active", got)
	}

	conn2, finish2, err := acquireTabConn(ctx, "t-pin", pool, testServerID, "db", true)
	if err != nil {
		t.Fatal(err)
	}
	if conn2 != conn {
		t.Fatal("the pinned connection was not reused")
	}
	if err := run(t, conn2, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	finish2()
	if got := sessionState("t-pin"); got != txStateIdle {
		t.Fatalf("after COMMIT state = %q, want idle", got)
	}
	if n := pool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("%d connections still acquired after COMMIT", n)
	}
}

func TestErrorInTransactionIsAborted(t *testing.T) {
	pool := testPool(t, 4)
	conn, finish, err := acquireTabConn(context.Background(), "t-abort", pool, testServerID, "db", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t, conn, "SELECT 1/0"); err == nil {
		t.Fatal("expected a division error")
	}
	finish()
	if got := sessionState("t-abort"); got != txStateAborted {
		t.Fatalf("state = %q, want aborted", got)
	}
}

func TestBusyTabIsRejected(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()
	conn, finish, err := acquireTabConn(ctx, "t-busy", pool, testServerID, "db", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn
	finish() // pinned (autocommit off opened a transaction)

	_, finish2, err := acquireTabConn(ctx, "t-busy", pool, testServerID, "db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer finish2()
	_, _, err = acquireTabConn(ctx, "t-busy", pool, testServerID, "db", true)
	var ae *acquireError
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		t.Fatalf("second concurrent run: err = %v, want 409", err)
	}
}

func TestPinnedLimitGuard(t *testing.T) {
	pool := testPool(t, 3) // at most 2 pinned, one stays free
	ctx := context.Background()
	for _, tab := range []string{"t-l1", "t-l2"} {
		_, finish, err := acquireTabConn(ctx, tab, pool, testServerID, "db", false)
		if err != nil {
			t.Fatal(err)
		}
		finish()
	}
	_, _, err := acquireTabConn(ctx, "t-l3", pool, testServerID, "db", false)
	var ae *acquireError
	if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests {
		t.Fatalf("third transaction: err = %v, want 429", err)
	}
	// An ordinary auto-commit query still gets the reserved connection.
	conn, finish, err := acquireTabConn(ctx, "t-l3", pool, testServerID, "db", true)
	if err != nil {
		t.Fatalf("auto-commit query refused: %v", err)
	}
	if err := run(t, conn, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	finish()
}

func TestReleaseSessionsBeforeCloseDoesNotHang(t *testing.T) {
	pool := testPool(t, 4)
	_, finish, err := acquireTabConn(context.Background(), "t-close", pool, testServerID, "db", false)
	if err != nil {
		t.Fatal(err)
	}
	finish()

	done := make(chan struct{})
	go func() {
		releaseSessions(testServerID, "")
		pool.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pool.Close hung with a pinned transaction")
	}
}

func TestIdleSessionsAreRolledBack(t *testing.T) {
	pool := testPool(t, 4)
	_, finish, err := acquireTabConn(context.Background(), "t-idle", pool, testServerID, "db", false)
	if err != nil {
		t.Fatal(err)
	}
	finish()

	cutoff := time.Now().Add(time.Second) // everything pinned so far is "older"
	endSessions(takeSessions(func(_ string, s *tabSession) bool { return s.lastUsed.Before(cutoff) }, false), "idle note")
	if got := sessionState("t-idle"); got != txStateIdle {
		t.Fatalf("state = %q after janitor pass, want idle", got)
	}
	sessMu.Lock()
	note := txNotes["t-idle"]
	delete(txNotes, "t-idle")
	sessMu.Unlock()
	if note != "idle note" {
		t.Fatalf("note = %q", note)
	}
}

func TestNoticesAreCollectedPerRun(t *testing.T) {
	pool := testPool(t, 4)
	conn, finish, err := acquireTabConn(context.Background(), "t-notice", pool, testServerID, "db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()

	sink, stop := watchNotices(conn.Conn().PgConn())
	if err := run(t, conn, `DO $$ BEGIN RAISE NOTICE 'hello %', 42; END $$`); err != nil {
		t.Fatal(err)
	}
	stop()
	if got := sink.JSON(); !strings.Contains(got, "NOTICE: hello 42") {
		t.Fatalf("notices = %q", got)
	}
	// Outside a run nothing is collected.
	if err := run(t, conn, `DO $$ BEGIN RAISE NOTICE 'later'; END $$`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sink.JSON(), "later") {
		t.Fatal("a notice raised after stop was collected")
	}
}
