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

// maintModalData is the view model for the shared maintenance panel partial.
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
	// Side-panel fields (see ddlModalData): the panel id, header, live-SQL
	// endpoint and the database the script tab runs the SQL on.
	PanelID    string
	Title      string
	PreviewURL string
	ConnDB     string
	Action     string
	FolderID   string
}

func (s *Server) renderMaintPanel(w http.ResponseWriter, m maintModalData) {
	if m.Values == nil {
		m.Values = map[string]string{}
	}
	if op, ok := maintOps[m.Op]; ok {
		m.OpLabel = op.Label
	}
	m.Action = "maint"
	m.PreviewURL = "/api/maint/" + m.Op + "/preview"
	m.Title = m.OpLabel + " " + maintTargetLabel(m.Op, m.Values["target"], m.ReindexTarget)
	m.ConnDB = m.DB
	RenderPartial(w, "maint_panel.html", m)
}

// maintTargetLabel names what the operation runs against, for the panel header.
func maintTargetLabel(op, target, reindexTarget string) string {
	switch op {
	case "vacuum", "analyze":
		if target == "table" {
			return "Table"
		}
		return "Database"
	case "cluster":
		return "Table"
	}
	switch reindexTarget {
	case "table":
		return "Table"
	case "index":
		return "Index"
	case "schema":
		return "Schema"
	}
	return "Database"
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

func (s *Server) handleMaintPanel(w http.ResponseWriter, r *http.Request) {
	op := chi.URLParam(r, "op")
	if _, ok := maintOps[op]; !ok {
		log.Printf("Maintenance panel: unknown op %q on %s", op, r.URL.Path)
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

	panelID := r.URL.Query().Get("panel_id")
	if !panelIDPattern.MatchString(panelID) {
		http.Error(w, "Invalid panel id", http.StatusBadRequest)
		return
	}

	var indexes []string
	if op == "cluster" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		indexes = s.clusterIndexes(ctx, sid, dbName, schema, table)
	}

	s.renderMaintPanel(w, maintModalData{
		PanelID:       panelID,
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

// handleMaintPreview renders the SQL for the current form values, reusing the
// DDL preview partial.
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
