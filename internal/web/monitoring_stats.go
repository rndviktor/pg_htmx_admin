package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// -----------------------------------------------------------------------------
// Cluster-wide dashboard statistics: checkpoints, WAL and I/O counters, plus the
// server-level dashboard (aggregates over every database). Counters are
// cumulative, like the rest of the monitoring snapshot; the browser turns them
// into per-second rates. Each group depends on the server version and is simply
// left out of the response when it is unavailable.
// -----------------------------------------------------------------------------

type ioStats struct {
	Reads     int64 `json:"reads"`
	Writes    int64 `json:"writes"`
	Hits      int64 `json:"hits"`
	Evictions int64 `json:"evictions"`
}

type walStats struct {
	Records int64 `json:"records"`
	FPI     int64 `json:"fpi"`
	Bytes   int64 `json:"bytes"`
}

type checkpointStats struct {
	Timed          int64 `json:"timed"`
	Requested      int64 `json:"requested"`
	BuffersWritten int64 `json:"buffersWritten"`
	BuffersClean   int64 `json:"buffersClean"`
}

// clusterStats groups the counters shared by every database of a server.
type clusterStats struct {
	IO          *ioStats         `json:"io,omitempty"`
	WAL         *walStats        `json:"wal,omitempty"`
	Checkpoints *checkpointStats `json:"checkpoints,omitempty"`
}

// Minimum server_version_num of each statistics view.
const (
	verStatWAL          = 140000 // pg_stat_wal
	verStatIO           = 160000 // pg_stat_io
	verStatCheckpointer = 170000 // pg_stat_checkpointer (checkpoint columns left pg_stat_bgwriter)
)

func serverVersionNum(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var v int
	err := pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&v)
	return v, err
}

// checkpointQuery returns the (timed, requested, buffers written by
// checkpoints, buffers cleaned by the bgwriter) query for the server version.
func checkpointQuery(ver int) string {
	if ver >= verStatCheckpointer {
		return `SELECT c.num_timed, c.num_requested, c.buffers_written, b.buffers_clean
FROM pg_stat_checkpointer c, pg_stat_bgwriter b`
	}
	return `SELECT checkpoints_timed, checkpoints_req, buffers_checkpoint, buffers_clean FROM pg_stat_bgwriter`
}

const (
	walQuery = `SELECT wal_records, wal_fpi, wal_bytes::bigint FROM pg_stat_wal`
	ioQuery  = `SELECT COALESCE(sum(reads), 0)::bigint, COALESCE(sum(writes), 0)::bigint,
    COALESCE(sum(hits), 0)::bigint, COALESCE(sum(evictions), 0)::bigint FROM pg_stat_io`
)

// queryClusterStats collects the version-dependent counters. A group that is
// unsupported, or whose query fails (e.g. missing privileges), stays nil.
func queryClusterStats(ctx context.Context, pool *pgxpool.Pool) *clusterStats {
	ver, err := serverVersionNum(ctx, pool)
	if err != nil {
		log.Printf("[monitoring] server version: %v", err)
		return nil
	}
	cs := &clusterStats{}

	ck := &checkpointStats{}
	if err := pool.QueryRow(ctx, checkpointQuery(ver)).
		Scan(&ck.Timed, &ck.Requested, &ck.BuffersWritten, &ck.BuffersClean); err == nil {
		cs.Checkpoints = ck
	} else {
		log.Printf("[monitoring] checkpoint stats: %v", err)
	}
	if ver >= verStatWAL {
		w := &walStats{}
		if err := pool.QueryRow(ctx, walQuery).Scan(&w.Records, &w.FPI, &w.Bytes); err == nil {
			cs.WAL = w
		} else {
			log.Printf("[monitoring] wal stats: %v", err)
		}
	}
	if ver >= verStatIO {
		io := &ioStats{}
		if err := pool.QueryRow(ctx, ioQuery).Scan(&io.Reads, &io.Writes, &io.Hits, &io.Evictions); err == nil {
			cs.IO = io
		} else {
			log.Printf("[monitoring] io stats: %v", err)
		}
	}
	return cs
}

// addDatabaseExtras adds the cluster counters and the current database's temp
// file / deadlock counters to a database snapshot. Best-effort: a failure only
// leaves the extra fields empty, it never fails the poll.
func addDatabaseExtras(ctx context.Context, pool *pgxpool.Pool, resp *monitoringResponse) {
	resp.Cluster = queryClusterStats(ctx, pool)
	if err := pool.QueryRow(ctx, `
		SELECT temp_files, temp_bytes, deadlocks
		FROM pg_stat_database WHERE datname = current_database()`).
		Scan(&resp.TempFiles, &resp.TempBytes, &resp.Deadlocks); err != nil {
		log.Printf("[monitoring] database extras: %v", err)
	}
}

// queryServerMonitoring builds the server-level snapshot in the same shape as a
// database snapshot, aggregated over every database, so the dashboard code
// path is shared. Sessions and replication are cluster-wide by nature.
func queryServerMonitoring(ctx context.Context, pool *pgxpool.Pool) (*monitoringResponse, error) {
	resp, err := queryKPIs(ctx, pool)
	if err != nil {
		return nil, err
	}
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(sum(pg_database_size(oid)), 0)::bigint
		FROM pg_database WHERE has_database_privilege(oid, 'CONNECT')`).
		Scan(&resp.DatabaseSizeBytes); err != nil {
		return nil, err
	}
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(sum(blks_hit), 0)::bigint, COALESCE(sum(blks_read), 0)::bigint,
		       COALESCE(sum(xact_commit), 0)::bigint, COALESCE(sum(xact_rollback), 0)::bigint,
		       COALESCE(sum(tup_inserted), 0)::bigint, COALESCE(sum(tup_updated), 0)::bigint,
		       COALESCE(sum(tup_deleted), 0)::bigint, COALESCE(sum(temp_files), 0)::bigint,
		       COALESCE(sum(temp_bytes), 0)::bigint, COALESCE(sum(deadlocks), 0)::bigint
		FROM pg_stat_database WHERE datname IS NOT NULL`).
		Scan(&resp.BlksHit, &resp.BlksRead, &resp.XactCommit, &resp.XactRollback,
			&resp.Inserts, &resp.Updates, &resp.Deletes, &resp.TempFiles, &resp.TempBytes, &resp.Deadlocks); err != nil {
		return nil, err
	}
	resp.Cluster = queryClusterStats(ctx, pool)
	return resp, nil
}

// handleServerMonitoring returns the server-level snapshot for {serverID}.
func (s *Server) handleServerMonitoring(w http.ResponseWriter, r *http.Request) {
	id, ok := parseServerID(w, r)
	if !ok {
		return
	}
	pool, err := s.getOrCreatePool(r.Context(), id)
	if err != nil {
		log.Printf("[monitoring] server %d: %v", id, err)
		http.Error(w, "Cannot connect to server: "+err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := queryServerMonitoring(r.Context(), pool)
	if err != nil {
		log.Printf("[monitoring] %s: %v", r.URL.Path, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// dbStatRow is one line of the server dashboard's databases table.
type dbStatRow struct {
	Name      string
	Size      string
	Backends  int64
	Commits   int64
	Rollbacks int64
	CacheHit  string
	TempBytes string
	Deadlocks int64
	hit, read int64
}

const dbStatsQuery = `
SELECT d.datname,
       CASE WHEN has_database_privilege(d.oid, 'CONNECT') THEN pg_database_size(d.oid) END,
       COALESCE(s.numbackends, 0), COALESCE(s.xact_commit, 0), COALESCE(s.xact_rollback, 0),
       COALESCE(s.blks_hit, 0), COALESCE(s.blks_read, 0), COALESCE(s.temp_bytes, 0), COALESCE(s.deadlocks, 0)
FROM pg_database d LEFT JOIN pg_stat_database s ON s.datid = d.oid
WHERE NOT d.datistemplate ORDER BY d.datname`

func cacheHitLabel(hit, read int64) string {
	if hit+read == 0 {
		return "–"
	}
	return fmt.Sprintf("%.1f%%", float64(hit)/float64(hit+read)*100)
}

func queryDatabaseStats(ctx context.Context, pool *pgxpool.Pool) ([]dbStatRow, error) {
	rows, err := pool.Query(ctx, dbStatsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbStatRow
	for rows.Next() {
		var r dbStatRow
		var size *int64
		var temp int64
		if err := rows.Scan(&r.Name, &size, &r.Backends, &r.Commits, &r.Rollbacks, &r.hit, &r.read, &temp, &r.Deadlocks); err != nil {
			return nil, err
		}
		r.Size = "–"
		if size != nil {
			r.Size = humanSize(*size)
		}
		r.CacheHit = cacheHitLabel(r.hit, r.read)
		r.TempBytes = humanSize(temp)
		out = append(out, r)
	}
	return out, rows.Err()
}

// handleServerMonitoringDatabases renders the per-database table rows of the
// server dashboard.
func (s *Server) handleServerMonitoringDatabases(w http.ResponseWriter, r *http.Request) {
	id, ok := parseServerID(w, r)
	if !ok {
		return
	}
	pool, err := s.getOrCreatePool(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot connect to server: "+err.Error(), http.StatusBadGateway)
		return
	}
	rows, err := queryDatabaseStats(r.Context(), pool)
	if err != nil {
		log.Printf("[monitoring] database stats: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	RenderPartial(w, "monitoring_databases.html", rows)
}
