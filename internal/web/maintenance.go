package web

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
)

// Maintenance dialogs (Vacuum/Analyze/Cluster/Reindex) build a single SQL
// statement server-side from posted form values and run it through the same
// generate-then-preview-then-run convention as the DDL dialogs in ddl.go —
// but as fixed verbs against an already-identified object rather than
// Create/Drop/Alter of a named one, so they live in their own small op map
// instead of ddlKinds. Every op always runs against a specific database pool;
// there is no server-wide (maintenance-db) variant.

type maintOp struct {
	Label string
	Build func(v map[string]string) (string, error)
}

var maintOps = map[string]maintOp{
	"vacuum":  {Label: "Vacuum", Build: buildVacuum},
	"analyze": {Label: "Analyze", Build: buildAnalyze},
	"cluster": {Label: "Cluster", Build: buildCluster},
	"reindex": {Label: "Reindex", Build: buildReindex},
}

// vacuumAnalyzeTarget returns the " schema.table" suffix for Vacuum/Analyze
// when targeting a single table, or "" when targeting the whole database.
func vacuumAnalyzeTarget(v map[string]string) (string, error) {
	if v["target"] != "table" {
		return "", nil
	}
	if v["schema"] == "" || v["table"] == "" {
		return "", formErr("Table is required.")
	}
	return " " + qualIdent(v["schema"], v["table"]), nil
}

func buildVacuum(v map[string]string) (string, error) {
	target, err := vacuumAnalyzeTarget(v)
	if err != nil {
		return "", err
	}
	var opts []string
	if v["full"] == "on" {
		opts = append(opts, "FULL")
	}
	if v["freeze"] == "on" {
		opts = append(opts, "FREEZE")
	}
	if v["analyze"] == "on" {
		opts = append(opts, "ANALYZE")
	}
	if v["verbose"] == "on" {
		opts = append(opts, "VERBOSE")
	}
	stmt := "VACUUM"
	if len(opts) > 0 {
		stmt += " (" + strings.Join(opts, ", ") + ")"
	}
	return stmt + target, nil
}

func buildAnalyze(v map[string]string) (string, error) {
	target, err := vacuumAnalyzeTarget(v)
	if err != nil {
		return "", err
	}
	stmt := "ANALYZE"
	if v["verbose"] == "on" {
		stmt += " (VERBOSE)"
	}
	return stmt + target, nil
}

func buildCluster(v map[string]string) (string, error) {
	if v["schema"] == "" || v["table"] == "" {
		return "", formErr("Table is required.")
	}
	stmt := "CLUSTER"
	if v["verbose"] == "on" {
		stmt += " (VERBOSE)"
	}
	stmt += " " + qualIdent(v["schema"], v["table"])
	if idx := strings.TrimSpace(v["index"]); idx != "" {
		stmt += " USING " + quoteIdent(idx)
	}
	return stmt, nil
}

func buildReindex(v map[string]string) (string, error) {
	var opts []string
	if v["concurrently"] == "on" {
		opts = append(opts, "CONCURRENTLY")
	}
	if v["verbose"] == "on" {
		opts = append(opts, "VERBOSE")
	}

	var target, name string
	switch v["reindex_target"] {
	case "table":
		if v["schema"] == "" || v["table"] == "" {
			return "", formErr("Table is required.")
		}
		target, name = "TABLE", qualIdent(v["schema"], v["table"])
	case "index":
		if v["schema"] == "" || v["name"] == "" {
			return "", formErr("Index is required.")
		}
		target, name = "INDEX", qualIdent(v["schema"], v["name"])
	case "schema":
		if v["schema"] == "" {
			return "", formErr("Schema is required.")
		}
		target, name = "SCHEMA", quoteIdent(v["schema"])
	case "database":
		if v["db"] == "" {
			return "", formErr("Database is required.")
		}
		target, name = "DATABASE", quoteIdent(v["db"])
	default:
		return "", formErr("Unsupported reindex target.")
	}

	stmt := "REINDEX"
	if len(opts) > 0 {
		stmt += " (" + strings.Join(opts, ", ") + ")"
	}
	return stmt + " " + target + " " + name, nil
}

// maintModalData is the view model for the shared maintenance modal partial.
type maintModalData struct {
	Op            string
	OpLabel       string
	ServerID      int64
	DB            string
	Schema        string
	Table         string
	ReindexTarget string
	Name          string
	Values        map[string]string
	// Indexes holds the target table's index names, for Cluster's "USING
	// index" dropdown.
	Indexes []string
	Error   string
}

func (s *Server) renderMaintModal(w http.ResponseWriter, m maintModalData) {
	if m.Values == nil {
		m.Values = map[string]string{}
	}
	if op, ok := maintOps[m.Op]; ok {
		m.OpLabel = op.Label
	}
	RenderPartial(w, "maint_modal.html", m)
}

// clusterIndexes fetches the target table's index names for the Cluster
// dialog's "USING index" dropdown. Best-effort: a connection or query error
// is logged and an empty list is returned so the form still renders.
func (s *Server) clusterIndexes(ctx context.Context, sid int64, dbName, schema, table string) []string {
	pool, err := s.getOrCreateDbPool(ctx, sid, dbName)
	if err != nil {
		log.Printf("Maintenance cluster indexes pool: %v", err)
		return nil
	}
	indexes, err := pgdb.New(pool).ListTableIndexes(ctx, pgdb.ListTableIndexesParams{Nspname: schema, Relname: table})
	if err != nil {
		log.Printf("Maintenance cluster indexes: %v", err)
		return nil
	}
	return indexes
}

func (s *Server) handleMaintModal(w http.ResponseWriter, r *http.Request) {
	op := chi.URLParam(r, "op")
	if _, ok := maintOps[op]; !ok {
		log.Printf("Maintenance modal: unknown op %q on %s", op, r.URL.Path)
		http.Error(w, "Unknown maintenance operation", http.StatusNotFound)
		return
	}

	sid, _ := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
	dbName := r.URL.Query().Get("db")
	schema := r.URL.Query().Get("schema")
	table := r.URL.Query().Get("table")
	target := r.URL.Query().Get("target")
	reindexTarget := r.URL.Query().Get("reindex_target")
	name := r.URL.Query().Get("name")

	var indexes []string
	if op == "cluster" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		indexes = s.clusterIndexes(ctx, sid, dbName, schema, table)
	}

	s.renderMaintModal(w, maintModalData{
		Op:            op,
		ServerID:      sid,
		DB:            dbName,
		Schema:        schema,
		Table:         table,
		ReindexTarget: reindexTarget,
		Name:          name,
		Values:        map[string]string{"target": target},
		Indexes:       indexes,
	})
}

// handleMaintPreview re-renders the SQL preview panel from the current form
// values, reusing the DDL dialogs' preview partial.
func (s *Server) handleMaintPreview(w http.ResponseWriter, r *http.Request) {
	op := chi.URLParam(r, "op")
	o, ok := maintOps[op]
	if !ok {
		log.Printf("Maintenance preview: unknown op %q on %s", op, r.URL.Path)
		http.Error(w, "Unknown maintenance operation", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("Maintenance preview: invalid form data: %v", err)
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	contents, err := o.Build(formValues(r.Form))
	if err != nil {
		contents = "Error: " + err.Error()
	}
	RenderPartial(w, "ddl_preview.html", map[string]any{"Contents": contents})
}

func (s *Server) handleMaintRun(w http.ResponseWriter, r *http.Request) {
	op := chi.URLParam(r, "op")
	o, ok := maintOps[op]
	if !ok {
		log.Printf("Maintenance run: unknown op %q on %s", op, r.URL.Path)
		http.Error(w, "Unknown maintenance operation", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("Maintenance run: invalid form data: %v", err)
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sid, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	dbName := r.FormValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	reindexTarget := r.FormValue("reindex_target")
	name := r.FormValue("name")

	rerender := func(errMsg string) {
		var indexes []string
		if op == "cluster" {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			indexes = s.clusterIndexes(ctx, sid, dbName, schema, table)
			cancel()
		}
		s.renderMaintModal(w, maintModalData{
			Op: op, ServerID: sid, DB: dbName, Schema: schema, Table: table,
			ReindexTarget: reindexTarget, Name: name,
			Values: formValues(r.Form), Indexes: indexes, Error: errMsg,
		})
	}

	if err != nil || sid < 1 {
		log.Printf("Maintenance run %s: missing or invalid server_id %q", op, r.FormValue("server_id"))
		rerender("Missing or invalid server id.")
		return
	}
	if s.isDisconnected(sid) {
		log.Printf("Maintenance run %s: server %d is disconnected", op, sid)
		rerender("Server is disconnected. Reconnect it first.")
		return
	}

	// Vacuum/Reindex/Cluster can run long on large tables; give them room
	// beyond the DDL dialogs' short create/alter timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	pool, perr := s.getOrCreateDbPool(ctx, sid, dbName)
	if perr != nil {
		log.Printf("Maintenance run %s: cannot reach target database: %v", op, perr)
		rerender("Cannot reach the target database: " + perr.Error())
		return
	}

	sqlStr, err := o.Build(formValues(r.Form))
	if err != nil {
		log.Printf("Maintenance run %s: build error: %v", op, err)
		rerender(err.Error())
		return
	}

	tag, err := runDDLOn(ctx, pool, []string{sqlStr})
	if err != nil {
		log.Printf("Maintenance run %s failed: %v", op, err)
		rerender("Execution failed: " + err.Error())
		return
	}

	RenderPartial(w, "maint_success.html", map[string]any{"Message": tag})
}
