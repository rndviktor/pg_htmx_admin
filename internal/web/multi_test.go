package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScriptStatements(t *testing.T) {
	got := scriptStatements("-- only a comment\n; select 1; select ';' ; DO $$ BEGIN PERFORM 1; END $$; -- trailing\n")
	if len(got) != 3 {
		t.Fatalf("got %d statements %q, want 3", len(got), got)
	}
	if n := len(scriptStatements("select 1")); n != 1 {
		t.Fatalf("single statement: got %d", n)
	}
	if n := len(scriptStatements("select 1;")); n != 1 {
		t.Fatalf("single statement with semicolon: got %d", n)
	}
}

func TestRunStatementKinds(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()
	conn, finish, err := acquireTabConn(ctx, "t-multi", pool, testServerID, "db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()

	set, _, _, err := runStatement(ctx, conn, 1, "SELECT g FROM generate_series(1, 5) g", 3)
	if err != nil || len(set.Rows) != 3 || set.Total != 5 || !set.Truncated {
		t.Fatalf("rows: err=%v rows=%d total=%d truncated=%v", err, len(set.Rows), set.Total, set.Truncated)
	}
	if !strings.Contains(set.Label, "5 rows") {
		t.Fatalf("label = %q", set.Label)
	}

	set, n, _, err := runStatement(ctx, conn, 2, "CREATE TEMP TABLE multi_t (a int)", 10)
	if err != nil || set.Message != "CREATE TABLE" || len(set.Headers) != 0 {
		t.Fatalf("ddl: err=%v msg=%q", err, set.Message)
	}
	_ = n
	set, n, _, err = runStatement(ctx, conn, 3, "INSERT INTO multi_t VALUES (1), (2)", 10)
	if err != nil || set.Message != "INSERT 0 2" || n != 2 {
		t.Fatalf("dml: err=%v msg=%q n=%d", err, set.Message, n)
	}
	set, _, _, err = runStatement(ctx, conn, 4, "SELECT 1/0", 10)
	if err == nil || !set.IsError || set.Message == "" {
		t.Fatalf("error: err=%v set=%+v", err, set)
	}
}

func TestRenderMultiStopsAtFirstError(t *testing.T) {
	pool := testPool(t, 4)
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	ctx := context.Background()
	conn, finish, err := acquireTabConn(ctx, "t-multi2", pool, testServerID, "db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()

	notices, stop := watchNotices(conn.Conn().PgConn())
	defer stop()
	stmts := scriptStatements("SELECT 1 AS a; DO $$ BEGIN RAISE NOTICE 'n1'; END $$; SELECT 1/0; SELECT 99")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/execute-query", nil)
	(&Server{}).renderMulti(w, r, conn, stmts, 0, "db", "t-multi2", "script", 100, notices, time.Now())

	body := w.Body.String()
	for _, want := range []string{`query-multi`, `data-is-error="true"`, `Statement 3 of 4 failed`, `(1 not run)`, `data-label="1 · SELECT (1 rows)"`, `data-error="true"`, `NOTICE: n1`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "SELECT 99") || strings.Contains(body, `data-label="4`) {
		t.Error("statement after the failure was run")
	}
}
