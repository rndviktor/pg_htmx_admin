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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
)

// DDL dialogs generate CREATE/DROP statements server-side from posted form
// values (the client never sends raw SQL). Kinds whose Scope is "server" run
// against the server's maintenance database (database, role, tablespace);
// "db" kinds run against the specific target database. A successful
// create/drop responds with a ddl-refresh HX-Trigger carrying the tree
// container id to re-fetch, so the tree shows the new live objects/counts.

type ddlScope string

const (
	ddlScopeServer ddlScope = "server"
	ddlScopeDB     ddlScope = "db"
)

// ddlKind describes one object type's create/drop dialogs. BuildCreate and
// BuildDrop receive the flattened form values, so each kind can pick the
// fields it needs (context fields db/schema/table travel alongside).
type ddlKind struct {
	Label       string
	Scope       ddlScope
	HasCascade  bool
	HasForce    bool
	BuildCreate func(v map[string]string) (string, error)
	BuildDrop   func(v map[string]string) (string, error)
}

var ddlKinds = map[string]ddlKind{
	"database": {Label: "Database", Scope: ddlScopeServer, HasForce: true,
		BuildCreate: buildCreateDatabase, BuildDrop: buildDropDatabase},
	"role": {Label: "Role", Scope: ddlScopeServer,
		BuildCreate: buildCreateRole, BuildDrop: buildDropRole},
	"tablespace": {Label: "Tablespace", Scope: ddlScopeServer,
		BuildCreate: buildCreateTablespace, BuildDrop: buildDropTablespace},
	"schema": {Label: "Schema", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateSchema, BuildDrop: buildDropSchema},
	"sequence": {Label: "Sequence", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateSequence, BuildDrop: buildDropSequence},
	"view": {Label: "View", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateView, BuildDrop: buildDropView},
	"matview": {Label: "Materialized View", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateMatView, BuildDrop: buildDropMatView},
	"function": {Label: "Function", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateFunction, BuildDrop: buildDropFunction},
	"procedure": {Label: "Procedure", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateProcedure, BuildDrop: buildDropProcedure},
	"extension": {Label: "Extension", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateExtension, BuildDrop: buildDropExtension},
	"publication": {Label: "Publication", Scope: ddlScopeDB,
		BuildCreate: buildCreatePublication, BuildDrop: buildDropPublication},
	"index": {Label: "Index", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateIndex, BuildDrop: buildDropIndex},
	"trigger": {Label: "Trigger", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateTrigger, BuildDrop: buildDropTrigger},
}

// ddlModalData is the view model shared by the create and drop modal partials.
type ddlModalData struct {
	Partial    string
	Kind       string
	KindLabel  string
	ServerID   int64
	FolderID   string
	DB         string
	Schema     string
	Table      string
	Values     map[string]string
	Error      string
	Name       string
	HasForce   bool
	Force      bool
	HasCascade bool
	Cascade    bool
	// Dropdowns holds the live option lists rendered as <select>: "roles",
	// plus "encodings"/"templates" for databases, "extensions"/"schemas"
	// for extensions and "columns" for indexes.
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
	k, ok := ddlKinds[kind]
	if !ok {
		return "", errUnsupportedDDLKind(kind)
	}
	v := formValues(form)
	if kind == "index" {
		v["cols"] = strings.Join(form["columns"], ",")
	}
	return k.BuildCreate(v)
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

func buildDropDatabase(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Database name")
	}
	if v["force"] == "on" {
		return "DROP DATABASE " + quoteIdent(name) + " WITH (FORCE);\n", nil
	}
	return "DROP DATABASE " + quoteIdent(name) + ";\n", nil
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

func buildDropRole(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Role name")
	}
	return "DROP ROLE " + quoteIdent(name) + ";\n", nil
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

func buildDropTablespace(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Tablespace name")
	}
	return "DROP TABLESPACE " + quoteIdent(name) + ";\n", nil
}

func buildCreateSchema(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Schema name")
	}
	if owner := strings.TrimSpace(v["owner"]); owner != "" {
		return "CREATE SCHEMA " + quoteIdent(name) + " AUTHORIZATION " + quoteIdent(owner) + ";\n", nil
	}
	return "CREATE SCHEMA " + quoteIdent(name) + ";\n", nil
}

func buildDropSchema(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Schema name")
	}
	return "DROP SCHEMA " + quoteIdent(name) + dropCascade(v) + ";\n", nil
}

func buildCreateSequence(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Sequence name")
	}

	var sb strings.Builder
	sb.WriteString("CREATE SEQUENCE ")
	sb.WriteString(qualIdent(v["schema"], name))
	sb.WriteString("\n")
	if inc := strings.TrimSpace(v["increment"]); inc != "" {
		sb.WriteString("    INCREMENT BY " + inc + "\n")
	}
	if start := strings.TrimSpace(v["start"]); start != "" {
		sb.WriteString("    START WITH " + start + "\n")
	}
	if min := strings.TrimSpace(v["minvalue"]); min != "" {
		sb.WriteString("    MINVALUE " + min + "\n")
	}
	if max := strings.TrimSpace(v["maxvalue"]); max != "" {
		sb.WriteString("    MAXVALUE " + max + "\n")
	}
	if cache := strings.TrimSpace(v["cache"]); cache != "" {
		sb.WriteString("    CACHE " + cache + "\n")
	}
	if v["cycle"] == "on" {
		sb.WriteString("    CYCLE\n")
	}
	sb.WriteString(";\n")
	return sb.String(), nil
}

func buildDropSequence(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Sequence name")
	}
	return "DROP SEQUENCE " + qualIdent(v["schema"], name) + dropCascade(v) + ";\n", nil
}

func buildCreateView(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("View name")
	}
	definition := strings.TrimSpace(v["definition"])
	if definition == "" {
		return "", errRequired("View definition")
	}

	var sb strings.Builder
	sb.WriteString("CREATE OR REPLACE VIEW ")
	sb.WriteString(qualIdent(v["schema"], name))
	if cols := strings.TrimSpace(v["columns"]); cols != "" {
		sb.WriteString("\n(\n    " + cols + "\n)")
	}
	sb.WriteString("\nAS\n")
	sb.WriteString(definition)
	sb.WriteString(";\n")
	return sb.String(), nil
}

func buildDropView(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("View name")
	}
	return "DROP VIEW " + qualIdent(v["schema"], name) + dropCascade(v) + ";\n", nil
}

func buildCreateMatView(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Materialized view name")
	}
	definition := strings.TrimSpace(v["definition"])
	if definition == "" {
		return "", errRequired("Materialized view definition")
	}

	var sb strings.Builder
	sb.WriteString("CREATE MATERIALIZED VIEW ")
	sb.WriteString(qualIdent(v["schema"], name))
	sb.WriteString("\nAS\n")
	sb.WriteString(definition)
	sb.WriteString("\nWITH DATA;\n")
	return sb.String(), nil
}

func buildDropMatView(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Materialized view name")
	}
	return "DROP MATERIALIZED VIEW " + qualIdent(v["schema"], name) + dropCascade(v) + ";\n", nil
}

func buildCreateFunction(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Function name")
	}
	body := strings.TrimSpace(v["body"])
	if body == "" {
		return "", errRequired("Function body")
	}

	var sb strings.Builder
	sb.WriteString("CREATE OR REPLACE FUNCTION ")
	sb.WriteString(qualIdent(v["schema"], name))
	sb.WriteString("(")
	if args := strings.TrimSpace(v["args"]); args != "" {
		sb.WriteString(args)
	}
	sb.WriteString(")\n")
	if ret := strings.TrimSpace(v["returns"]); ret != "" {
		sb.WriteString("RETURNS " + ret + "\n")
	}
	if lang := strings.TrimSpace(v["language"]); lang != "" {
		sb.WriteString("LANGUAGE " + quoteIdent(lang) + "\n")
	}
	if vol := strings.TrimSpace(v["volatility"]); vol != "" {
		sb.WriteString(strings.ToUpper(vol) + "\n")
	}
	if v["strict"] == "on" {
		sb.WriteString("STRICT\n")
	}
	if v["security"] == "on" {
		sb.WriteString("SECURITY DEFINER\n")
	}
	sb.WriteString("AS $function$\n")
	sb.WriteString(body)
	sb.WriteString("\n$function$;\n")
	return sb.String(), nil
}

func buildDropFunction(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Function name")
	}
	return "DROP FUNCTION " + qualifiedRoutine(v["schema"], name) + dropCascade(v) + ";\n", nil
}

func buildCreateProcedure(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Procedure name")
	}
	body := strings.TrimSpace(v["body"])
	if body == "" {
		return "", errRequired("Procedure body")
	}

	var sb strings.Builder
	sb.WriteString("CREATE OR REPLACE PROCEDURE ")
	sb.WriteString(qualIdent(v["schema"], name))
	sb.WriteString("(")
	if args := strings.TrimSpace(v["args"]); args != "" {
		sb.WriteString(args)
	}
	sb.WriteString(")\n")
	if lang := strings.TrimSpace(v["language"]); lang != "" {
		sb.WriteString("LANGUAGE " + quoteIdent(lang) + "\n")
	}
	if v["security"] == "on" {
		sb.WriteString("SECURITY DEFINER\n")
	}
	sb.WriteString("AS $procedure$\n")
	sb.WriteString(body)
	sb.WriteString("\n$procedure$;\n")
	return sb.String(), nil
}

func buildDropProcedure(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Procedure name")
	}
	return "DROP PROCEDURE " + qualifiedRoutine(v["schema"], name) + dropCascade(v) + ";\n", nil
}

func buildCreateExtension(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Extension name")
	}
	if strings.TrimSpace(v["install_schema"]) == "" {
		v["install_schema"] = "public"
	}

	var sb strings.Builder
	sb.WriteString("CREATE EXTENSION ")
	sb.WriteString(quoteIdent(name))
	sb.WriteString("\n    WITH SCHEMA ")
	sb.WriteString(quoteIdent(v["install_schema"]))
	if v["cascade"] == "on" {
		sb.WriteString("\n    CASCADE")
	}
	sb.WriteString(";\n")
	return sb.String(), nil
}

func buildDropExtension(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Extension name")
	}
	return "DROP EXTENSION " + quoteIdent(name) + dropCascade(v) + ";\n", nil
}

func buildCreatePublication(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Publication name")
	}

	var sb strings.Builder
	sb.WriteString("CREATE PUBLICATION ")
	sb.WriteString(quoteIdent(name))
	if v["all_tables"] == "on" {
		sb.WriteString("\n    FOR ALL TABLES")
	} else if tables := strings.TrimSpace(v["tables"]); tables != "" {
		parts := splitList(tables)
		quoted := make([]string, 0, len(parts))
		for _, t := range parts {
			quoted = append(quoted, qualIdentFromList(t))
		}
		sb.WriteString("\n    FOR TABLE ")
		sb.WriteString(strings.Join(quoted, ", "))
	}
	sb.WriteString(";\n")

	if owner := strings.TrimSpace(v["owner"]); owner != "" {
		sb.WriteString("ALTER PUBLICATION ")
		sb.WriteString(quoteIdent(name))
		sb.WriteString(" OWNER TO ")
		sb.WriteString(quoteIdent(owner))
		sb.WriteString(";\n")
	}
	return sb.String(), nil
}

func buildDropPublication(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Publication name")
	}
	return "DROP PUBLICATION " + quoteIdent(name) + ";\n", nil
}

func buildCreateIndex(v map[string]string) (string, error) {
	if v["schema"] == "" || v["table"] == "" {
		return "", formErr("Target table is missing.")
	}
	cols := splitList(v["cols"])
	if len(cols) == 0 {
		return "", formErr("Select at least one column.")
	}
	method := strings.TrimSpace(v["method"])
	if method == "" {
		method = "btree"
	}
	quoted := make([]string, 0, len(cols))
	for _, c := range cols {
		quoted = append(quoted, quoteIdent(c))
	}

	var sb strings.Builder
	sb.WriteString("CREATE ")
	if v["unique"] == "on" {
		sb.WriteString("UNIQUE ")
	}
	sb.WriteString("INDEX ")
	if name := strings.TrimSpace(v["name"]); name != "" {
		sb.WriteString(quoteIdent(name) + " ")
	}
	sb.WriteString("ON ")
	sb.WriteString(qualIdent(v["schema"], v["table"]))
	sb.WriteString("\n    USING ")
	sb.WriteString(quoteIfNeeded(method))
	sb.WriteString(" (")
	sb.WriteString(strings.Join(quoted, ", "))
	sb.WriteString(");\n")
	return sb.String(), nil
}

func buildDropIndex(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Index name")
	}
	return "DROP INDEX " + qualIdent(v["schema"], name) + dropCascade(v) + ";\n", nil
}

func buildCreateTrigger(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Trigger name")
	}
	if v["schema"] == "" || v["table"] == "" {
		return "", formErr("Target table is missing.")
	}
	timing := strings.TrimSpace(v["timing"])
	if timing == "" {
		timing = "BEFORE"
	}
	events := strings.TrimSpace(v["events"])
	if events == "" {
		return "", formErr("Select at least one event.")
	}
	fn := strings.TrimSpace(v["on_function"])
	if fn == "" {
		return "", formErr("Execution function is required.")
	}

	var sb strings.Builder
	sb.WriteString("CREATE TRIGGER ")
	sb.WriteString(quoteIdent(name))
	sb.WriteString("\n    ")
	sb.WriteString(strings.ToUpper(timing))
	sb.WriteString(" ")
	sb.WriteString(strings.ToUpper(events))
	sb.WriteString(" ON ")
	sb.WriteString(qualIdent(v["schema"], v["table"]))
	if v["foreach_row"] == "on" {
		sb.WriteString("\n    FOR EACH ROW")
	}
	sb.WriteString("\n    EXECUTE FUNCTION ")
	sb.WriteString(fn)
	sb.WriteString(";\n")
	return sb.String(), nil
}

func buildDropTrigger(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Trigger name")
	}
	if v["schema"] == "" || v["table"] == "" {
		return "", formErr("Target table is missing.")
	}
	return "DROP TRIGGER " + quoteIdent(name) + " ON " + qualIdent(v["schema"], v["table"]) + dropCascade(v) + ";\n", nil
}

// dropCascade is the DROP ... CASCADE suffix when the drop form requested it.
func dropCascade(v map[string]string) string {
	if v["cascade"] == "on" {
		return " CASCADE"
	}
	return ""
}

// qualifiedRoutine quotes the identifier part of a routine name that may carry
// an argument list, e.g. "foo(integer)" -> "schema"."foo"(integer). Names
// without a signature are qualified wholesale.
func qualifiedRoutine(schema, name string) string {
	if i := strings.IndexByte(name, '('); i >= 0 {
		return qualIdent(schema, name[:i]) + name[i:]
	}
	return qualIdent(schema, name)
}

// splitList splits a comma-separated list, trimming whitespace and dropping
// empty entries.
func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// qualIdentFromList qualifies a user-supplied table reference that may already
// be schema-qualified (e.g. "public.users2" or "users2"): only the final
// segment is quoted, the schema part is preserved as typed.
func qualIdentFromList(ref string) string {
	if i := strings.LastIndex(ref, "."); i >= 0 {
		return ref[:i+1] + quoteIfNeeded(ref[i+1:])
	}
	return quoteIfNeeded(ref)
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

// runDDLOn executes one or more statements on a dedicated connection. CREATE
// DATABASE must be its own statement, so statements are executed individually
// rather than concatenated.
func runDDLOn(ctx context.Context, pool *pgxpool.Pool, statements []string) (string, error) {
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

// runDDL executes statements against the server's maintenance database.
func (s *Server) runDDL(ctx context.Context, serverID int64, statements []string) (string, error) {
	pool, err := s.getOrCreatePool(ctx, serverID)
	if err != nil {
		return "", err
	}
	return runDDLOn(ctx, pool, statements)
}

// runDatabaseDDL executes statements against a specific target database.
func (s *Server) runDatabaseDDL(ctx context.Context, serverID int64, dbName string, statements []string) (string, error) {
	pool, err := s.getOrCreateDbPool(ctx, serverID, dbName)
	if err != nil {
		return "", err
	}
	return runDDLOn(ctx, pool, statements)
}

// ddlTargetPool returns the pool a kind's DDL should run against: the
// maintenance database for server-scoped kinds, the target database otherwise.
func (s *Server) ddlTargetPool(ctx context.Context, kind string, sid int64, dbName string) (*pgxpool.Pool, error) {
	k, ok := ddlKinds[kind]
	if !ok {
		return nil, errUnsupportedDDLKind(kind)
	}
	if k.Scope == ddlScopeDB {
		if strings.TrimSpace(dbName) == "" {
			return nil, formErr("Database is required for this object.")
		}
		return s.getOrCreateDbPool(ctx, sid, dbName)
	}
	return s.getOrCreatePool(ctx, sid)
}

// renderDDLModal renders one of the DDL modal partials into #modal-container.
func (s *Server) renderDDLModal(w http.ResponseWriter, m ddlModalData) {
	if m.Values == nil {
		m.Values = map[string]string{}
	}
	if m.Dropdowns == nil {
		m.Dropdowns = map[string][]string{}
	}
	if k, ok := ddlKinds[m.Kind]; ok {
		m.KindLabel = k.Label
		m.HasForce = k.HasForce
		m.HasCascade = k.HasCascade
	}
	RenderPartial(w, m.Partial, m)
}

// ddlDropdowns fetches the live option lists used by the create forms: the
// role list (for every kind), plus encodings/template databases for the
// database dialog, available extensions/schemas for the extension dialog and
// the target table's columns for the index dialog. Best-effort: any catalog
// or connection error is logged and the affected list is left empty, so the
// form still renders (with blank dropdowns) on unreachable servers.
func (s *Server) ddlDropdowns(ctx context.Context, kind string, pool *pgxpool.Pool, schema, table string) (map[string][]string, string) {
	dd := make(map[string][]string)
	queries := pgdb.New(pool)

	currentUser := ""
	if cu, err := queries.GetCurrentUser(ctx); err == nil {
		currentUser = getString(cu)
	} else {
		log.Printf("ddl dropdowns[GetCurrentUser]: %v", err)
	}
	if roles, err := queries.ListRoles(ctx); err == nil {
		dd["roles"] = roles
	} else {
		log.Printf("ddl dropdowns[ListRoles]: %v", err)
	}

	switch kind {
	case "database":
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
	case "extension":
		if exts, err := queries.ListAvailableExtensions(ctx); err == nil {
			dd["extensions"] = exts
		} else {
			log.Printf("ddl dropdowns[ListAvailableExtensions]: %v", err)
		}
		if schemas, err := queries.ListSchemas(ctx); err == nil {
			dd["schemas"] = schemas
		} else {
			log.Printf("ddl dropdowns[ListSchemas]: %v", err)
		}
	case "index":
		if table == "" || schema == "" {
			break
		}
		cols, err := queries.GetTableColumnsDetailed(ctx, pgdb.GetTableColumnsDetailedParams{
			Column1: pgtype.Text{String: schema, Valid: true},
			Column2: pgtype.Text{String: table, Valid: true},
		})
		if err != nil {
			log.Printf("ddl dropdowns[GetTableColumnsDetailed]: %v", err)
			break
		}
		for _, c := range cols {
			dd["columns"] = append(dd["columns"], getString(c.ColumnName))
		}
	}
	return dd, currentUser
}

// refreshDDLTree emits the HX-Trigger that makes ddl.js re-fetch the tree
// container id produced by a successful create/drop.
func (s *Server) refreshDDLTree(w http.ResponseWriter, folderID string) {
	trigger, err := json.Marshal(map[string]string{"ddl-refresh": folderID})
	if err != nil {
		log.Printf("Failed to marshal ddl-refresh trigger: %v", err)
		return
	}
	w.Header().Set("HX-Trigger", string(trigger))
}

func (s *Server) handleDDLModal(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlKinds[kind]; !ok {
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}

	sid, _ := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
	folderID := r.URL.Query().Get("folder_id")
	db := r.URL.Query().Get("db")
	schema := r.URL.Query().Get("schema")
	table := r.URL.Query().Get("table")

	if r.URL.Query().Get("action") == "drop" {
		s.renderDDLModal(w, ddlModalData{
			Partial:  "ddl_drop_modal.html",
			Kind:     kind,
			ServerID: sid,
			FolderID: folderID,
			DB:       db,
			Schema:   schema,
			Table:    table,
			Name:     r.URL.Query().Get("name"),
		})
		return
	}

	// Buffered context for the dropdown queries; connection failures are
	// surfaced as an error banner but the form still renders.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	values := map[string]string{"login": "on", "inherit": "on", "connlimit": "-1"}
	dd := make(map[string][]string)
	errMsg := ""
	currentUser := ""
	pool, perr := s.ddlTargetPool(ctx, kind, sid, db)
	if perr != nil {
		log.Printf("DDL modal %s pool: %v", kind, perr)
		errMsg = "Cannot reach the target database: " + perr.Error()
	} else {
		dd, currentUser = s.ddlDropdowns(ctx, kind, pool, schema, table)
	}

	// Defaults shown in the fresh create form: roles log in and inherit by
	// default with an unlimited connection count, and the connecting user is
	// the owner where an owner is offered.
	switch kind {
	case "database", "role":
		values["login"] = "on"
		values["inherit"] = "on"
		values["connlimit"] = "-1"
	}
	if currentUser != "" {
		switch kind {
		case "database", "tablespace", "schema", "sequence", "view", "matview", "function", "procedure", "publication":
			values["owner"] = currentUser
		}
	}
	switch kind {
	case "function":
		values["language"] = "plpgsql"
		values["volatility"] = "volatile"
	case "procedure":
		values["language"] = "plpgsql"
	case "extension":
		schemaDefault := "public"
		if schema != "" {
			schemaDefault = schema
		}
		values["install_schema"] = schemaDefault
	}

	s.renderDDLModal(w, ddlModalData{
		Partial:   "ddl_" + kind + "_modal.html",
		Kind:      kind,
		ServerID:  sid,
		FolderID:  folderID,
		DB:        db,
		Schema:    schema,
		Table:     table,
		Values:    values,
		Dropdowns: dd,
		Error:     errMsg,
	})
}

// handleDDLPreview re-renders the SQL preview panel from the current form
// values. Errors are rendered inside the preview instead of blocking the form.
func (s *Server) handleDDLPreview(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlKinds[kind]; !ok {
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
	if _, ok := ddlKinds[kind]; !ok {
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
		s.renderDDLModal(w, ddlModalData{
			Partial:  "ddl_" + kind + "_modal.html",
			Kind:     kind,
			Values:   formValues(r.Form),
			Error:    "Missing or invalid server id.",
		})
		return
	}
	folderID := r.FormValue("folder_id")
	db := r.FormValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	if s.isDisconnected(sid) {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, ddlModalData{
			Partial:  "ddl_" + kind + "_modal.html",
			Kind:     kind,
			ServerID: sid,
			FolderID: folderID,
			DB:       db,
			Schema:   schema,
			Table:    table,
			Values:   formValues(r.Form),
			Error:    "Server is disconnected. Reconnect it first.",
		})
		return
	}

	// Repopulate the live dropdowns when the form is re-rendered with an
	// error, so the user does not lose the option lists.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pool, perr := s.ddlTargetPool(ctx, kind, sid, db)
	var dd map[string][]string
	if perr != nil {
		log.Printf("DDL create %s pool: %v", kind, perr)
		dd = map[string][]string{}
	} else {
		dd, _ = s.ddlDropdowns(ctx, kind, pool, schema, table)
	}

	rerender := func(errMsg string, status int) {
		w.WriteHeader(status)
		s.renderDDLModal(w, ddlModalData{
			Partial:   "ddl_" + kind + "_modal.html",
			Kind:      kind,
			ServerID:  sid,
			FolderID:  folderID,
			DB:        db,
			Schema:    schema,
			Table:     table,
			Values:    formValues(r.Form),
			Dropdowns: dd,
			Error:     errMsg,
		})
	}

	if perr != nil {
		rerender("Cannot reach the target database: "+perr.Error(), http.StatusBadRequest)
		return
	}

	sqlStr, err := renderCreateDDL(kind, r.Form)
	if err != nil {
		rerender(err.Error(), http.StatusBadRequest)
		return
	}

	tag, err := s.runOnTarget(ctx, kind, sid, db, []string{sqlStr})
	if err != nil {
		log.Printf("DDL create %s failed: %v", kind, err)
		rerender("Execution failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.refreshDDLTree(w, folderID)
	RenderPartial(w, "ddl_success.html", map[string]any{"Message": tag})
}

func (s *Server) handleDDLDrop(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlKinds[kind]; !ok {
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
		s.renderDDLModal(w, ddlModalData{Partial: "ddl_drop_modal.html", Kind: kind, Error: "Missing or invalid server id."})
		return
	}
	folderID := r.FormValue("folder_id")
	db := r.FormValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, ddlModalData{
			Partial:  "ddl_drop_modal.html",
			Kind:     kind,
			ServerID: sid,
			FolderID: folderID,
			DB:       db,
			Schema:   schema,
			Table:    table,
			Error:    "Object name is required.",
		})
		return
	}
	if s.isDisconnected(sid) {
		w.WriteHeader(http.StatusBadRequest)
		s.renderDDLModal(w, ddlModalData{
			Partial:  "ddl_drop_modal.html",
			Kind:     kind,
			ServerID: sid,
			FolderID: folderID,
			DB:       db,
			Schema:   schema,
			Table:    table,
			Error:    "Server is disconnected. Reconnect it first.",
			Name:     name,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	rerender := func(errMsg string, status int) {
		w.WriteHeader(status)
		s.renderDDLModal(w, ddlModalData{
			Partial:  "ddl_drop_modal.html",
			Kind:     kind,
			ServerID: sid,
			FolderID: folderID,
			DB:       db,
			Schema:   schema,
			Table:    table,
			Error:    errMsg,
			Name:     name,
			Force:    r.FormValue("force") == "on",
			Cascade:  r.FormValue("cascade") == "on",
		})
	}

	v := formValues(r.Form)
	sqlStr, err := ddlKinds[kind].BuildDrop(v)
	if err != nil {
		rerender(err.Error(), http.StatusBadRequest)
		return
	}

	tag, err := s.runOnTarget(ctx, kind, sid, db, []string{sqlStr})
	if err != nil {
		log.Printf("DDL drop %s failed: %v", kind, err)
		rerender("Execution failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.refreshDDLTree(w, folderID)
	RenderPartial(w, "ddl_success.html", map[string]any{"Message": tag})
}

// runOnTarget executes create/drop DDL on the pool selected by the kind's
// scope, refreshing the target database connection as needed.
func (s *Server) runOnTarget(ctx context.Context, kind string, sid int64, db string, stmts []string) (string, error) {
	k, ok := ddlKinds[kind]
	if !ok {
		return "", errUnsupportedDDLKind(kind)
	}
	if k.Scope == ddlScopeDB {
		return s.runDatabaseDDL(ctx, sid, db, stmts)
	}
	return s.runDDL(ctx, sid, stmts)
}