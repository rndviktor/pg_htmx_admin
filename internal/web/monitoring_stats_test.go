package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckpointQueryByVersion(t *testing.T) {
	tests := []struct {
		ver  int
		want string
		not  string
	}{
		{160004, "FROM pg_stat_bgwriter", "pg_stat_checkpointer"},
		{169999, "buffers_checkpoint", "pg_stat_checkpointer"},
		{170000, "pg_stat_checkpointer", "buffers_checkpoint"},
		{180001, "num_requested", "checkpoints_req"},
	}
	for _, tc := range tests {
		got := checkpointQuery(tc.ver)
		if !strings.Contains(got, tc.want) || strings.Contains(got, tc.not) {
			t.Errorf("version %d: unexpected query:\n%s", tc.ver, got)
		}
	}
}

func TestMonitoringResponseOmitsUnavailableStats(t *testing.T) {
	data, err := json.Marshal(monitoringResponse{Cluster: &clusterStats{WAL: &walStats{Bytes: 42}}})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`"wal":{"records":0,"fpi":0,"bytes":42}`, `"tempBytes":0`, `"deadlocks":0`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	for _, bad := range []string{`"io"`, `"checkpoints"`} {
		if strings.Contains(body, bad) {
			t.Errorf("unexpected %q in %s", bad, body)
		}
	}

	data, _ = json.Marshal(monitoringResponse{})
	if strings.Contains(string(data), `"cluster"`) {
		t.Errorf("cluster should be omitted when nil: %s", data)
	}
}

func TestCacheHitLabel(t *testing.T) {
	if got := cacheHitLabel(0, 0); got != "–" {
		t.Errorf("idle database = %q", got)
	}
	if got := cacheHitLabel(99, 1); got != "99.0%" {
		t.Errorf("hit ratio = %q", got)
	}
}

func TestMonitoringDatabasesRenders(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	w := httptest.NewRecorder()
	RenderPartial(w, "monitoring_databases.html", []dbStatRow{
		{Name: "shop", Size: "8.0 MiB", Backends: 3, Commits: 10, Rollbacks: 1, CacheHit: "99.0%", TempBytes: "0 B", Deadlocks: 2},
	})
	body := w.Body.String()
	for _, want := range []string{">shop<", "8.0 MiB", "99.0%"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}

	w = httptest.NewRecorder()
	RenderPartial(w, "monitoring_databases.html", []dbStatRow{})
	if !strings.Contains(w.Body.String(), "No databases.") {
		t.Errorf("empty list not handled:\n%s", w.Body.String())
	}
}

// Runs the version-gated statistics queries and the server snapshot against a
// live server (TEST_PG_DSN), whatever its version.
func TestServerMonitoringLive(t *testing.T) {
	pool := testPool(t, 2)
	ctx := context.Background()

	resp, err := queryServerMonitoring(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if resp.MaxConnections == 0 || resp.DatabaseSizeBytes == 0 {
		t.Errorf("server snapshot looks empty: %+v", resp)
	}
	if resp.Cluster == nil || resp.Cluster.Checkpoints == nil {
		t.Fatalf("expected checkpoint stats on every supported version, got %+v", resp.Cluster)
	}
	ver, err := serverVersionNum(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if (ver >= verStatIO) != (resp.Cluster.IO != nil) {
		t.Errorf("pg_stat_io availability does not match version %d", ver)
	}
	if (ver >= verStatWAL) != (resp.Cluster.WAL != nil) {
		t.Errorf("pg_stat_wal availability does not match version %d", ver)
	}

	rows, err := queryDatabaseStats(ctx, pool)
	if err != nil || len(rows) == 0 {
		t.Fatalf("database stats: %v (%d rows)", err, len(rows))
	}
}
