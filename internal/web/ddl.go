package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
)

// DDL dialogs for the object types whose create/drop forms are self-contained
// (no catalog round-trip needed to prefill): database, role and tablespace.
// Every endpoint regenerates the SQL server-side from the posted form values
// (the client never sends raw SQL), and all three run against the server's
// maintenance database. A successful create/drop responds with a ddl-refresh
// HX-Trigger carrying the tree container id (the server node), so the tree
// re-fetches and shows the new live counts.

var ddlCreateKinds = map[string]struct{}{
	"database":   {},
	"role":       {},
	"tablespace": {},
}

var ddlKindLabels = map[string]string{
	"database":   "Database",
	"role":       "Role",
	"tablespace": "Tablespace",
}

// ddlModalData is the view model shared by the create and drop modal partials.
type ddlModalData struct {
	Kind      string
	KindLabel string
	ServerID  int64
	FolderID  string
	Values    map[string]string
	Error     string
	Name      string
	HasForce  bool
	Force     bool
	// Dropdowns holds the live option lists rendered as <select> where the
	// catalog can provide them: "roles", and for the database dialog also
	// "encodings" and "templates".
	Dropdowns map[string][]string
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// formValues flattens a parsed form into its first value per field, matching
// what a single-value input emits.
func formValues(form url.Values) map[string]string {
	m := make(map[string]string, len(form))
	for k, vals := range form {
		if len(vals) > 0 {
			m[k] = vals[0]
		}
	}
	return m
}

// renderCreateDDL builds the CREATE statement for the given kind from the raw
// posted form. The same builder drives both the live SQL preview and the
// actual create, so what the user sees is exactly what runs.
func renderCreateDDL(kind string, form url.Values) (string, error) {
	v := formValues(form)
	switch kind {
	case "database":
		return buildCreateDatabase(v)
	case "role":
		return buildCreateRole(v)
	case "tablespace":
		return buildCreateTablespace(v)
	}
	return "", errUnsupportedDDLKind(kind)
}

func buildCreateDatabase(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Database name")
	}

	clauses := []string{}
	if owner := strings.TrimSpace(v["owner"]); owner != "" {
		clauses = append(clauses, "    OWNER = "+quoteIdent(owner))
	}
	if enc := strings.TrimSpace(v["encoding"]); enc != "" {
		clauses = append(clauses, "    ENCODING = "+quoteLiteral(enc))
	}
	if tpl := strings.TrimSpace(v["template"]); tpl != "" {
		clauses = append(clauses, "    TEMPLATE = "+quoteIdent(tpl))
	}
	if lc := strings.TrimSpace(v["lc_collate"]); lc != "" {
		clauses = append(clauses, "    LC_COLLATE = "+quoteLiteral(lc))
	}
	if lc := strings.TrimSpace(v["lc_ctype"]); lc != "" {
		clauses = append(clauses, "    LC_CTYPE = "+quoteLiteral(lc))
	}

	var sb strings.Builder
	sb.WriteString("CREATE DATABASE ")
	sb.WriteString(quoteIdent(name))
	if len(clauses) > 0 {
		sb.WriteString("\n    WITH\n")
		sb.WriteString(strings.Join(clauses, ",\n"))
	}
	sb.WriteString(";\n")
	return sb.String(), nil
}

func buildDropDatabase(name string, force bool) string {
	if force {
		return "DROP DATABASE " + quoteIdent(name) + " WITH (FORCE);\n"
	}
	return "DROP DATABASE " + quoteIdent(name) + ";\n"
}

func buildCreateRole(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Role name")
	}

	parts := []string{}
	if v["login"] == "on" {
		parts = append(parts, "    LOGIN")
	} else {
		parts = append(parts, "    NOLOGIN")
	}
	if pwd := v["password"]; pwd != "" {
		parts = append(parts, "    PASSWORD "+quoteLiteral(pwd))
	}
	if v["superuser"] == "on" {
		parts = append(parts, "    SUPERUSER")
	} else {
		parts = append(parts, "    NOSUPERUSER")
	}
	if v["createdb"] == "on" {
		parts = append(parts, "    CREATEDB")
	} else {
		parts = append(parts, "    NOCREATEDB")
	}
	if v["createrole"] == "on" {
		parts = append(parts, "    CREATEROLE")
	} else {
		parts = append(parts, "    NOCREATEROLE")
	}
	if v["inherit"] == "on" {
		parts = append(parts, "    INHERIT")
	} else {
		parts = append(parts, "    NOINHERIT")
	}
	if v["replication"] == "on" {
		parts = append(parts, "    REPLICATION")
	} else {
		parts = append(parts, "    NOREPLICATION")
	}
	if limit, err := strconv.Atoi(strings.TrimSpace(v["connlimit"])); err == nil && limit >= 0 {
		parts = append(parts, "    CONNECTION LIMIT "+strconv.Itoa(limit))
	}
	if validUntil := strings.TrimSpace(v["validuntil"]); validUntil != "" {
		parts = append(parts, "    VALID UNTIL "+quoteLiteral(validUntil))
	}

	var sb strings.Builder
	sb.WriteString("CREATE ROLE ")
	sb.WriteString(quoteIdent(name))
	sb.WriteString(" WITH\n")
	sb.WriteString(strings.Join(parts, ",\n"))
	sb.WriteString(";\n")
	return sb.String(), nil
}

func buildDropRole(name string) string {
	return "DROP ROLE " + quoteIdent(name) + ";\n"
}

func buildCreateTablespace(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Tablespace name")
	}
	location := strings.TrimSpace(v["location"])
	if location == "" {
		return "", errRequired("Tablespace location")
	}

	var sb strings.Builder
	sb.WriteString("CREATE TABLESPACE ")
	sb.WriteString(quoteIdent(name))
	sb.WriteString("\n")
	if owner := strings.TrimSpace(v["owner"]); owner != "" {
		sb.WriteString("    OWNER ")
		sb.WriteString(quoteIdent(owner))
		sb.WriteString("\n")
	}
	sb.WriteString("    LOCATION ")
	sb.WriteString(quoteLiteral(location))
	sb.WriteString(";\n")
	return sb.String(), nil
}

func buildDropTablespace(name string) string {
	return "DROP TABLESPACE " + quoteIdent(name) + ";\n"
}

func errRequired(field string) error {
	return formErr(field + " is required.")
}

func errUnsupportedDDLKind(kind string) error {
	return formErr("Unsupported DDL kind: " + kind)
}

func formErr(msg string) error {
	return &ddlError{msg: msg}
}

type ddlError struct {
	msg string
}

func (e *ddlError) Error() string { return e.msg }

// runDDL executes one or more statements on a dedicated connection to the
// server's maintenance database (CREATE DATABASE must be its own statement,
// so statements are executed individually rather than concatenated).
func (s *Server) runDDL(ctx context.Context, serverID int64, statements []string) (string, error) {
	pool, err := s.getOrCreatePool(ctx, serverID)
	if err != nil {
		return "", err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()

	tag := "OK"
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		t, err := conn.Exec(ctx, stmt)
		if err != nil {
			return "", err
		}
		tag = t.String()
	}
	return tag, nil
}

// renderDDLModal renders one of the DDL modal partials into #modal-container.
func (s *Server) renderDDLModal(w http.ResponseWriter, partial, kind string, sid int64, folderID string, values map[string]string, dropdowns map[string][]string, errMsg, name string, force bool) {
	if values == nil {
		values = map[string]string{}
	}
	if dropdowns == nil {
		dropdowns = map[string][]string{}
	}
	RenderPartial(w, partial, ddlModalData{
		Kind:      kind,
		KindLabel: ddlKindLabels[kind],
		ServerID:  sid,
		FolderID:  folderID,
		Values:    values,
		Error:     errMsg,
		Name:      name,
		HasForce:  kind == "database",
		Force:     force,
		Dropdowns: dropdowns,
	})
}

// ddlDropdowns fetches the live option lists used by the create forms: the
// role list for every kind, plus encodings and template databases for the
// database dialog, from the maintenance pool. Best-effort: any catalog or
// connection error is logged and the affected list is left empty, so the form
// still renders (with blank dropdowns) on unreachable servers.
func (s *Server) ddlDropdowns(r *http.Request, kind string, sid int64) (map[string][]string, string) {
	dd := make(map[string][]string)
	if sid < 1 {
		return dd, ""
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	pool, err := s.getOrCreatePool(ctx, sid)
	if err != nil {
		return dd, ""
	}

	queries := pgdb.New(pool)
	if roles, err := queries.ListRoles(ctx); err == nil {
		dd["roles"] = roles
	} else {
		log.Printf("ddl dropdowns[ListRoles]: %v", err)
	}

	if kind == "database" {
		if encs, err := queries.ListEncodings(ctx); err == nil {
			dd["encodings"] = encs
		} else {
			log.Printf("ddl dropdowns[ListEncodings]: %v", err)
		}
		if tmpls, err := queries.ListDatabaseTemplates(ctx); err == nil {
			dd["templates"] = tmpls
		} else {
			log.Printf("ddl dropdowns[ListDatabaseTemplates]: %v", err)
		}
	}

	currentUser := ""
	if cu, err := queries.GetCurrentUser(ctx); err == nil {
		currentUser = getString(cu)
	} else {
		log.Printf("ddl dropdowns[GetCurrentUser]: %v", err)
	}
	return dd, currentUser
}

// refreshServerTree emits the HX-Trigger that makes ddl.js re-fetch the server
// node's folders (fresh databases/roles/tablespaces lists and badges).
func (s *Server) refreshServerTree(w http.ResponseWriter, folderID string) {
	trigger, err := json.Marshal(map[string]string{"ddl-refresh": folderID})
	if err != nil {
		log.Printf("Failed to marshal ddl-refresh trigger: %v", err)
		return
	}
	w.Header().Set("HX-Trigger", string(trigger))
}

func (s *Server) handleDDLModal(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlCreateKinds[kind]; !ok {
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}

	sid, _ := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
	folderID := r.URL.Query().Get("folder_id")

	if r.URL.Query().Get("action") == "drop" {
		s.renderDDLModal(w, "ddl_drop_modal.html", kind, sid, folderID, nil, nil, "", r.URL.Query().Get("name"), false)
		return
	}

	// Defaults shown in the fresh create form: roles log in and inherit by
	// default, with an unlimited connection count and the connecting user
	// as the owner where an owner is offered.
	dd, currentUser := s.ddlDropdowns(r, kind, sid)
	values := map[string]string{"login": "on", "inherit": "on", "connlimit": "-1"}
	if kind == "database" || kind == "tablespace" {
		if currentUser != "" {
			values["owner"] = currentUser
		}
	}
	s.renderDDLModal(w, "ddl_"+kind+"_modal.html", kind, sid, folderID, values, dd, "", "", false)
}

// handleDDLPreview re-renders the SQL preview panel from the current form
// values. Errors are rendered inside the preview instead of blocking the form.
func (s *Server) handleDDLPreview(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlCreateKinds[kind]; !ok {
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sqlStr, err := renderCreateDDL(kind, r.Form)
	contents := sqlStr
	if err != nil {
		contents = "Error: " + err.Error()
	}
	RenderPartial(w, "ddl_preview.html", map[string]any{"Contents": contents})
}

func (s *Server) handleDDLCreate(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlCreateKinds[kind]; !ok {
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sid, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	if err != nil || sid < 1 {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, "ddl_"+kind+"_modal.html", kind, 0, "", formValues(r.Form), nil, "Missing or invalid server id.", "", false)
		return
	}
	folderID := r.FormValue("folder_id")
	if s.isDisconnected(sid) {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, "ddl_"+kind+"_modal.html", kind, sid, folderID, formValues(r.Form), nil, "Server is disconnected. Reconnect it first.", "", false)
		return
	}

	// Repopulate the owner/encoding/template dropdowns when the form is
	// re-rendered with an error, so the user does not lose the option lists.
	dd, _ := s.ddlDropdowns(r, kind, sid)

	sqlStr, err := renderCreateDDL(kind, r.Form)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, "ddl_"+kind+"_modal.html", kind, sid, folderID, formValues(r.Form), dd, err.Error(), "", false)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tag, err := s.runDDL(ctx, sid, []string{sqlStr})
	if err != nil {
		log.Printf("DDL create %s failed: %v", kind, err)
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, "ddl_"+kind+"_modal.html", kind, sid, folderID, formValues(r.Form), dd, "Execution failed: "+err.Error(), "", false)
		return
	}

	s.refreshServerTree(w, folderID)
	RenderPartial(w, "ddl_success.html", map[string]any{"Message": tag})
}

func (s *Server) handleDDLDrop(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlCreateKinds[kind]; !ok {
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sid, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	if err != nil || sid < 1 {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, "ddl_drop_modal.html", kind, 0, "", nil, nil, "Missing or invalid server id.", "", false)
		return
	}
	folderID := r.FormValue("folder_id")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, "ddl_drop_modal.html", kind, sid, folderID, nil, nil, "Object name is required.", "", false)
		return
	}
	if s.isDisconnected(sid) {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, "ddl_drop_modal.html", kind, sid, folderID, nil, nil, "Server is disconnected. Reconnect it first.", name, false)
		return
	}

	force := r.FormValue("force") == "on"

	var sqlStr string
	switch kind {
	case "database":
		sqlStr = buildDropDatabase(name, force)
	case "role":
		sqlStr = buildDropRole(name)
	case "tablespace":
		sqlStr = buildDropTablespace(name)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tag, err := s.runDDL(ctx, sid, []string{sqlStr})
	if err != nil {
		log.Printf("DDL drop %s failed: %v", kind, err)
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, "ddl_drop_modal.html", kind, sid, folderID, nil, nil, "Execution failed: "+err.Error(), name, force)
		return
	}

	s.refreshServerTree(w, folderID)
	RenderPartial(w, "ddl_success.html", map[string]any{"Message": tag})
}
