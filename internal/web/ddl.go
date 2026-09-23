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

// DDL dialogs generate CREATE/DROP/ALTER statements server-side from posted
// form values (the client never sends raw SQL). Kinds whose Scope is
// "server" run against the server's maintenance database (database, role,
// tablespace); "db" kinds run against the specific target database. A
// successful create/drop/alter responds with a ddl-refresh HX-Trigger
// carrying the tree container id to re-fetch, so the tree shows the new
// live objects/counts.

type ddlScope string

const (
	ddlScopeServer ddlScope = "server"
	ddlScopeDB     ddlScope = "db"
)

// ddlKind describes one object type's create/drop/alter dialogs. BuildCreate
// and BuildDrop receive the flattened form values, so each kind can pick the
// fields it needs (context fields db/schema/table travel alongside).
// BuildCreateForm, when set, receives the raw parsed form instead — CREATE
// TABLE uses it to read its repeated column rows (parallel col_* lists that
// formValues would collapse). BuildAlter, when set, enables the "Alter..."
// context action for the kind.
type ddlKind struct {
	Label           string
	Scope           ddlScope
	HasCascade      bool
	HasForce        bool
	BuildCreateForm func(form url.Values) (string, error)
	BuildCreate     func(v map[string]string) (string, error)
	BuildDrop       func(v map[string]string) (string, error)
	BuildAlter      func(v map[string]string) (string, error)
}

var ddlKinds = map[string]ddlKind{
	"database": {Label: "Database", Scope: ddlScopeServer, HasForce: true,
		BuildCreate: buildCreateDatabase, BuildDrop: buildDropDatabase,
		BuildAlter: buildAlterDatabase},
	"role": {Label: "Role", Scope: ddlScopeServer,
		BuildCreate: buildCreateRole, BuildDrop: buildDropRole,
		BuildAlter: buildAlterRole},
	"tablespace": {Label: "Tablespace", Scope: ddlScopeServer,
		BuildCreate: buildCreateTablespace, BuildDrop: buildDropTablespace,
		BuildAlter: buildAlterTablespace},
	"schema": {Label: "Schema", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateSchema, BuildDrop: buildDropSchema,
		BuildAlter: buildAlterSchema},
	"table": {Label: "Table", Scope: ddlScopeDB, HasCascade: true,
		BuildCreateForm: buildCreateTable, BuildDrop: buildDropTable,
		BuildAlter: buildAlterTable},
	"sequence": {Label: "Sequence", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateSequence, BuildDrop: buildDropSequence,
		BuildAlter: buildAlterSequence},
	"view": {Label: "View", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateView, BuildDrop: buildDropView,
		BuildAlter: buildAlterView},
	"matview": {Label: "Materialized View", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateMatView, BuildDrop: buildDropMatView,
		BuildAlter: buildAlterMatView},
	"function": {Label: "Function", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateFunction, BuildDrop: buildDropFunction,
		BuildAlter: buildAlterFunction},
	"procedure": {Label: "Procedure", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateProcedure, BuildDrop: buildDropProcedure,
		BuildAlter: buildAlterProcedure},
	"extension": {Label: "Extension", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateExtension, BuildDrop: buildDropExtension,
		BuildAlter: buildAlterExtension},
	"publication": {Label: "Publication", Scope: ddlScopeDB,
		BuildCreate: buildCreatePublication, BuildDrop: buildDropPublication,
		BuildAlter: buildAlterPublication},
	"index": {Label: "Index", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateIndex, BuildDrop: buildDropIndex,
		BuildAlter: buildAlterIndex},
	"trigger": {Label: "Trigger", Scope: ddlScopeDB, HasCascade: true,
		BuildCreate: buildCreateTrigger, BuildDrop: buildDropTrigger,
		BuildAlter: buildAlterTrigger},
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
	// Kinds with repeated form rows (CREATE TABLE's columns) read the raw
	// url.Values before it is collapsed to one value per field.
	if k.BuildCreateForm != nil {
		return k.BuildCreateForm(form)
	}
	v := formValues(form)
	if kind == "index" {
		v["cols"] = strings.Join(form["columns"], ",")
	}
	return k.BuildCreate(v)
}

// fullCreateStatement returns def unchanged when it already starts with a
// complete CREATE <object> statement (e.g. a script pasted from the SQL
// editor), and an empty string otherwise. This lets the view/matview dialogs
// accept a full statement instead of only a bare SELECT body.
func fullCreateStatement(def, object string) string {
	lower := strings.ToLower(strings.TrimSpace(def))
	for _, prefix := range []string{
		"create " + object + " ",
		"create or replace " + object + " ",
		"create temp " + object + " ",
		"create temporary " + object + " ",
	} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(def)
		}
	}
	return ""
}

// ensureSemicolon returns s with exactly one trailing semicolon.
func ensureSemicolon(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimRight(s, ";")
	return s + ";"
}

// stripTrailingStatement removes trailing separators/whitespace so a wrapped
// definition can be safely embedded as a single statement.
func stripTrailingStatement(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ";")
}

// splitStatements splits a SQL script on top-level semicolons, honouring
// string literals, quoted identifiers, line/block comments and dollar-quoted
// bodies (including $tag$ ... $tag$). It powers multi-statement DDL scripts;
// pgx executes each returned statement individually.
func splitStatements(sql string) []string {
	var stmts []string
	var cur strings.Builder
	const (
		stNormal = iota
		stSingle
		stDouble
		stLine
		stBlock
		stDollar
	)
	state := stNormal
	dollarTag := ""

	write := func(s string) { cur.WriteString(s) }

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			stmts = append(stmts, s)
		}
		cur.Reset()
	}

	i := 0
	n := len(sql)
	for i < n {
		c := sql[i]
		switch state {
		case stNormal:
			switch {
			case c == '\'':
				state = stSingle
				write(string(c))
			case c == '"':
				state = stDouble
				write(string(c))
			case c == '-' && i+1 < n && sql[i+1] == '-':
				state = stLine
				write("--")
				i++
			case c == '/' && i+1 < n && sql[i+1] == '*':
				state = stBlock
				write("/*")
				i++
			case c == '$' && (i == 0 || !isSQLIdentByte(sql[i-1])):
				if tag := dollarQuoteTag(sql[i:]); tag != "" {
					dollarTag = tag
					state = stDollar
					write(tag)
					i += len(tag) - 1
				} else {
					write(string(c))
				}
			case c == ';':
				flush()
			default:
				write(string(c))
			}
		case stSingle:
			write(string(c))
			if c == '\'' {
				state = stNormal
			}
		case stDouble:
			write(string(c))
			if c == '"' {
				state = stNormal
			}
		case stLine:
			write(string(c))
			if c == '\n' {
				state = stNormal
			}
		case stBlock:
			write(string(c))
			if c == '*' && i+1 < n && sql[i+1] == '/' {
				write("/")
				i++
				state = stNormal
			}
		case stDollar:
			if strings.HasPrefix(sql[i:], dollarTag) {
				write(dollarTag)
				i += len(dollarTag) - 1
				state = stNormal
			} else {
				write(string(c))
			}
		}
		i++
	}
	flush()
	return stmts
}

// isSQLIdentByte reports whether c can appear in a SQL identifier or a
// dollar-quote tag.
func isSQLIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// dollarQuoteTag detects a dollar-quote delimiter ($$ or $tag$) at the start
// of s, returning the full delimiter if present.
func dollarQuoteTag(s string) string {
	if len(s) < 2 || s[0] != '$' {
		return ""
	}
	if s[1] == '$' {
		return "$$"
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '$' && i > 1 {
			return s[:i+1]
		}
		if !isSQLIdentByte(s[i]) {
			return ""
		}
	}
	return ""
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

// singleLine collapses embedded newlines so a typed type or default
// expression stays on one line of the generated statement. Inner spaces are
// preserved (they may sit inside a string literal).
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", " ")
}

// buildCreateTable generates CREATE TABLE from the raw form. Column rows are
// posted as parallel col_name/col_type/col_nullable/col_default/col_key
// lists — every row always submits all five fields, so the indexes stay
// aligned regardless of which rows the user added or removed. Rows with no
// name and no type are untouched and skipped. Key is "pk" (table-level
// PRIMARY KEY, multi-column ok) or "uniq" (inline UNIQUE). The optional owner
// is applied as a follow-up ALTER TABLE, which is the only portable form.
func buildCreateTable(form url.Values) (string, error) {
	v := formValues(form)
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Table name")
	}
	schema := strings.TrimSpace(v["schema"])
	if schema == "" {
		return "", formErr("Target schema is missing.")
	}

	names := form["col_name"]
	types := form["col_type"]
	nulls := form["col_nullable"]
	defaults := form["col_default"]
	keys := form["col_key"]
	at := func(list []string, i int) string {
		if i < len(list) {
			return strings.TrimSpace(list[i])
		}
		return ""
	}

	var defs []string
	var pkCols []string
	for i := range names {
		cn := at(names, i)
		ct := at(types, i)
		if cn == "" {
			if ct == "" && at(defaults, i) == "" && at(keys, i) == "" {
				continue // untouched row
			}
			return "", formErr("Every column needs a name.")
		}
		if ct == "" {
			return "", formErr("Type is required for column " + cn + ".")
		}
		line := quoteIdent(cn) + " " + singleLine(ct)
		if at(nulls, i) == "NO" {
			line += " NOT NULL"
		}
		if d := at(defaults, i); d != "" {
			line += " DEFAULT " + singleLine(d)
		}
		switch at(keys, i) {
		case "pk":
			pkCols = append(pkCols, quoteIdent(cn))
		case "uniq":
			line += " UNIQUE"
		}
		defs = append(defs, "    "+line)
	}
	if len(defs) == 0 {
		return "", formErr("Add at least one column.")
	}
	if len(pkCols) > 0 {
		defs = append(defs, "    PRIMARY KEY ("+strings.Join(pkCols, ", ")+")")
	}
	if c := strings.TrimSpace(v["check_expr"]); c != "" {
		defs = append(defs, "    CHECK ("+singleLine(c)+")")
	}

	var sb strings.Builder
	sb.WriteString("CREATE TABLE ")
	sb.WriteString(qualIdent(schema, name))
	sb.WriteString("\n(\n")
	sb.WriteString(strings.Join(defs, ",\n"))
	sb.WriteString("\n);\n")
	if owner := strings.TrimSpace(v["owner"]); owner != "" {
		sb.WriteString("ALTER TABLE ")
		sb.WriteString(qualIdent(schema, name))
		sb.WriteString(" OWNER TO ")
		sb.WriteString(quoteIdent(owner))
		sb.WriteString(";\n")
	}
	return sb.String(), nil
}

func buildDropTable(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Table name")
	}
	return "DROP TABLE " + qualIdent(v["schema"], name) + dropCascade(v) + ";\n", nil
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

	if full := fullCreateStatement(definition, "view"); full != "" {
		return ensureSemicolon(full), nil
	}

	var sb strings.Builder
	sb.WriteString("CREATE OR REPLACE VIEW ")
	sb.WriteString(qualIdent(v["schema"], name))
	if cols := strings.TrimSpace(v["columns"]); cols != "" {
		sb.WriteString("\n(\n    " + cols + "\n)")
	}
	sb.WriteString("\nAS\n")
	sb.WriteString(stripTrailingStatement(definition))
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

	if full := fullCreateStatement(definition, "materialized view"); full != "" {
		return ensureSemicolon(full), nil
	}

	var sb strings.Builder
	sb.WriteString("CREATE MATERIALIZED VIEW ")
	sb.WriteString(qualIdent(v["schema"], name))
	sb.WriteString("\nAS\n")
	sb.WriteString(stripTrailingStatement(definition))
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

// joinStatements joins ALTER statements into one script. Attribute changes
// must run before a RENAME (which invalidates the old name), so callers put
// the rename last.
func joinStatements(stmts []string) string {
	return strings.Join(stmts, ";\n") + ";\n"
}

// buildAlterSchema emits OWNER / RENAME statements for a schema.
func buildAlterSchema(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Schema name")
	}
	var stmts []string
	if owner := strings.TrimSpace(v["owner"]); owner != "" {
		stmts = append(stmts, "ALTER SCHEMA "+quoteIdent(name)+" OWNER TO "+quoteIdent(owner))
	}
	if nn := strings.TrimSpace(v["newname"]); nn != "" {
		stmts = append(stmts, "ALTER SCHEMA "+quoteIdent(name)+" RENAME TO "+quoteIdent(nn))
	}
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterDatabase emits OWNER / connectivity / CONNECTION LIMIT / RENAME
// statements for a database. Blank fields mean "leave unchanged".
func buildAlterDatabase(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Database name")
	}
	var stmts []string
	if owner := strings.TrimSpace(v["owner"]); owner != "" {
		stmts = append(stmts, "ALTER DATABASE "+quoteIdent(name)+" OWNER TO "+quoteIdent(owner))
	}
	if cl := strings.TrimSpace(v["connlimit"]); cl != "" {
		n, err := strconv.Atoi(cl)
		if err != nil || n < -1 {
			return "", formErr("Connection limit must be -1 (unlimited) or higher.")
		}
		stmts = append(stmts, "ALTER DATABASE "+quoteIdent(name)+" CONNECTION LIMIT "+strconv.Itoa(n))
	}
	switch v["allowconn"] {
	case "on":
		stmts = append(stmts, "ALTER DATABASE "+quoteIdent(name)+" ALLOW CONNECTIONS")
	case "off":
		stmts = append(stmts, "ALTER DATABASE "+quoteIdent(name)+" DISALLOW CONNECTIONS")
	}
	if nn := strings.TrimSpace(v["newname"]); nn != "" {
		stmts = append(stmts, "ALTER DATABASE "+quoteIdent(name)+" RENAME TO "+quoteIdent(nn))
	}
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner, connection limit, connectivity or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterRole emits attribute / PASSWORD / RENAME statements for a role.
// Boolean attributes are tri-state form selects: "" leaves them unchanged,
// "on"/"off" emit the positive/negative option. The role options use the
// documented space-separated `ALTER ROLE name [ WITH ] option ...` form.
func buildAlterRole(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Role name")
	}
	var acts []string
	tri := func(field, yes, no string) {
		switch v[field] {
		case "on":
			acts = append(acts, yes)
		case "off":
			acts = append(acts, no)
		}
	}
	tri("login", "LOGIN", "NOLOGIN")
	if pwd := v["password"]; pwd != "" {
		acts = append(acts, "PASSWORD "+quoteLiteral(pwd))
	}
	tri("superuser", "SUPERUSER", "NOSUPERUSER")
	tri("createdb", "CREATEDB", "NOCREATEDB")
	tri("createrole", "CREATEROLE", "NOCREATEROLE")
	tri("inherit", "INHERIT", "NOINHERIT")
	tri("replication", "REPLICATION", "NOREPLICATION")
	if cl := strings.TrimSpace(v["connlimit"]); cl != "" {
		n, err := strconv.Atoi(cl)
		if err != nil || n < -1 {
			return "", formErr("Connection limit must be -1 (unlimited) or higher.")
		}
		acts = append(acts, "CONNECTION LIMIT "+strconv.Itoa(n))
	}
	if vu := strings.TrimSpace(v["validuntil"]); vu != "" {
		acts = append(acts, "VALID UNTIL "+quoteLiteral(vu))
	}

	var stmts []string
	if len(acts) > 0 {
		stmts = append(stmts, "ALTER ROLE "+quoteIdent(name)+" WITH "+strings.Join(acts, " "))
	}
	if nn := strings.TrimSpace(v["newname"]); nn != "" {
		stmts = append(stmts, "ALTER ROLE "+quoteIdent(name)+" RENAME TO "+quoteIdent(nn))
	}
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an attribute or a new name.")
	}
	return joinStatements(stmts), nil
}

// alterOwnerSchemaRename emits the OWNER TO / SET SCHEMA / RENAME TO
// statements shared by several ALTER <object> forms. qualify re-quotes the
// target for a given schema; RENAME must reference the object by its
// *current* schema, which changes if SET SCHEMA ran first, so the rename
// statement recomputes the qualified name against the post-SET-SCHEMA value.
func alterOwnerSchemaRename(object, schema string, qualify func(schema string) string, v map[string]string, hasOwner, hasSchema bool) []string {
	var stmts []string
	target := qualify(schema)
	if hasOwner {
		if owner := strings.TrimSpace(v["owner"]); owner != "" {
			stmts = append(stmts, "ALTER "+object+" "+target+" OWNER TO "+quoteIdent(owner))
		}
	}
	finalSchema := schema
	if hasSchema {
		if ns := strings.TrimSpace(v["newschema"]); ns != "" {
			stmts = append(stmts, "ALTER "+object+" "+target+" SET SCHEMA "+quoteIdent(ns))
			finalSchema = ns
		}
	}
	if nn := strings.TrimSpace(v["newname"]); nn != "" {
		stmts = append(stmts, "ALTER "+object+" "+qualify(finalSchema)+" RENAME TO "+quoteIdent(nn))
	}
	return stmts
}

// buildAlterTablespace emits OWNER / RENAME statements for a tablespace.
func buildAlterTablespace(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Tablespace name")
	}
	stmts := alterOwnerSchemaRename("TABLESPACE", "", func(string) string { return quoteIdent(name) }, v, true, false)
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterPublication emits OWNER / RENAME statements for a publication.
func buildAlterPublication(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Publication name")
	}
	stmts := alterOwnerSchemaRename("PUBLICATION", "", func(string) string { return quoteIdent(name) }, v, true, false)
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterTable emits OWNER / SET SCHEMA / RENAME statements for a table.
// Column-level changes (ADD/DROP/ALTER COLUMN, constraints) are not covered.
func buildAlterTable(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Table name")
	}
	schema := strings.TrimSpace(v["schema"])
	stmts := alterOwnerSchemaRename("TABLE", schema, func(s string) string { return qualIdent(s, name) }, v, true, true)
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner, schema or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterView emits OWNER / SET SCHEMA / RENAME statements for a view.
func buildAlterView(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("View name")
	}
	schema := strings.TrimSpace(v["schema"])
	stmts := alterOwnerSchemaRename("VIEW", schema, func(s string) string { return qualIdent(s, name) }, v, true, true)
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner, schema or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterMatView emits OWNER / SET SCHEMA / RENAME statements for a
// materialized view.
func buildAlterMatView(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Materialized view name")
	}
	schema := strings.TrimSpace(v["schema"])
	stmts := alterOwnerSchemaRename("MATERIALIZED VIEW", schema, func(s string) string { return qualIdent(s, name) }, v, true, true)
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner, schema or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterFunction emits OWNER / SET SCHEMA / RENAME statements for a
// function. name may carry an argument signature (e.g. "foo(integer)").
func buildAlterFunction(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Function name")
	}
	schema := strings.TrimSpace(v["schema"])
	stmts := alterOwnerSchemaRename("FUNCTION", schema, func(s string) string { return qualifiedRoutine(s, name) }, v, true, true)
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner, schema or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterProcedure emits OWNER / SET SCHEMA / RENAME statements for a
// procedure. name may carry an argument signature.
func buildAlterProcedure(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Procedure name")
	}
	schema := strings.TrimSpace(v["schema"])
	stmts := alterOwnerSchemaRename("PROCEDURE", schema, func(s string) string { return qualifiedRoutine(s, name) }, v, true, true)
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set an owner, schema or a new name.")
	}
	return joinStatements(stmts), nil
}

// buildAlterSequence emits numeric-option / OWNER / SET SCHEMA / RENAME
// statements for a sequence. Blank numeric fields mean "leave unchanged";
// cycle is tri-state like the role/database alter forms.
func buildAlterSequence(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Sequence name")
	}
	schema := strings.TrimSpace(v["schema"])
	target := qualIdent(schema, name)

	var opts []string
	if inc := strings.TrimSpace(v["increment"]); inc != "" {
		opts = append(opts, "INCREMENT BY "+inc)
	}
	if min := strings.TrimSpace(v["minvalue"]); min != "" {
		opts = append(opts, "MINVALUE "+min)
	}
	if max := strings.TrimSpace(v["maxvalue"]); max != "" {
		opts = append(opts, "MAXVALUE "+max)
	}
	if restart := strings.TrimSpace(v["restart"]); restart != "" {
		opts = append(opts, "RESTART WITH "+restart)
	}
	if cache := strings.TrimSpace(v["cache"]); cache != "" {
		opts = append(opts, "CACHE "+cache)
	}
	switch v["cycle"] {
	case "on":
		opts = append(opts, "CYCLE")
	case "off":
		opts = append(opts, "NO CYCLE")
	}

	var stmts []string
	if len(opts) > 0 {
		stmts = append(stmts, "ALTER SEQUENCE "+target+" "+strings.Join(opts, " "))
	}
	stmts = append(stmts, alterOwnerSchemaRename("SEQUENCE", schema, func(s string) string { return qualIdent(s, name) }, v, true, true)...)
	if len(stmts) == 0 {
		return "", formErr("No changes requested.")
	}
	return joinStatements(stmts), nil
}

// buildAlterExtension emits UPDATE TO / SET SCHEMA statements for an
// extension. Postgres has no ALTER EXTENSION ... OWNER TO / RENAME form.
func buildAlterExtension(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Extension name")
	}
	var stmts []string
	if ver := strings.TrimSpace(v["version"]); ver != "" {
		stmts = append(stmts, "ALTER EXTENSION "+quoteIdent(name)+" UPDATE TO "+quoteLiteral(ver))
	}
	if ns := strings.TrimSpace(v["newschema"]); ns != "" {
		stmts = append(stmts, "ALTER EXTENSION "+quoteIdent(name)+" SET SCHEMA "+quoteIdent(ns))
	}
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Set a target version or a new schema.")
	}
	return joinStatements(stmts), nil
}

// buildAlterIndex emits a RENAME statement for an index. Indexes have no
// owner of their own and cannot change schema independently of their table.
func buildAlterIndex(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Index name")
	}
	nn := strings.TrimSpace(v["newname"])
	if nn == "" {
		return "", formErr("Enter a new name for the index.")
	}
	return "ALTER INDEX " + qualIdent(v["schema"], name) + " RENAME TO " + quoteIdent(nn) + ";\n", nil
}

// buildAlterTrigger emits ENABLE/DISABLE TRIGGER (tri-state) and RENAME
// statements for a trigger.
func buildAlterTrigger(v map[string]string) (string, error) {
	name := strings.TrimSpace(v["name"])
	if name == "" {
		return "", errRequired("Trigger name")
	}
	if v["schema"] == "" || v["table"] == "" {
		return "", formErr("Target table is missing.")
	}
	table := qualIdent(v["schema"], v["table"])

	var stmts []string
	switch v["enabled"] {
	case "on":
		stmts = append(stmts, "ALTER TABLE "+table+" ENABLE TRIGGER "+quoteIdent(name))
	case "off":
		stmts = append(stmts, "ALTER TABLE "+table+" DISABLE TRIGGER "+quoteIdent(name))
	}
	if nn := strings.TrimSpace(v["newname"]); nn != "" {
		stmts = append(stmts, "ALTER TRIGGER "+quoteIdent(name)+" ON "+table+" RENAME TO "+quoteIdent(nn))
	}
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Toggle the enabled state or set a new name.")
	}
	return joinStatements(stmts), nil
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
	case "table", "view", "matview", "sequence", "function", "procedure":
		// Alter dialogs for these kinds offer a "new schema" select.
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
		log.Printf("DDL modal: unknown kind %q on %s", kind, r.URL.Path)
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("action") == "alter" && ddlKinds[kind].BuildAlter == nil {
		log.Printf("DDL modal: alter not supported for kind %q on %s", kind, r.URL.Path)
		http.Error(w, "Altering this object kind is not supported", http.StatusNotFound)
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

	// The alter dialog reuses the pool acquired above so it can open with the
	// object's current values (owner, flags, ...) pre-filled.
	if r.URL.Query().Get("action") == "alter" {
		name := r.URL.Query().Get("name")
		values := map[string]string{"name": name}
		if perr == nil {
			for fk, fv := range s.alterPrefill(ctx, pool, kind, name, schema, table) {
				values[fk] = fv
			}
		}
		s.renderDDLModal(w, ddlModalData{
			Partial:   "ddl_alter_modal.html",
			Kind:      kind,
			ServerID:  sid,
			FolderID:  folderID,
			DB:        db,
			Schema:    schema,
			Table:     table,
			Name:      name,
			Values:    values,
			Dropdowns: dd,
			Error:     errMsg,
		})
		return
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
		case "database", "tablespace", "schema", "sequence", "view", "matview", "function", "procedure", "publication", "table":
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
		log.Printf("DDL preview: unknown kind %q on %s", kind, r.URL.Path)
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("DDL preview: invalid form data: %v", err)
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sqlStr, err := renderCreateDDL(kind, r.Form)
	contents := sqlStr
	if r.FormValue("mode") == "alter" {
		// The alter dialog shares the preview endpoint; mode=alter routes the
		// form values through BuildAlter instead of the create builder.
		k := ddlKinds[kind]
		if k.BuildAlter == nil {
			contents = "Error: altering this object kind is not supported."
		} else if sqlStr, err = k.BuildAlter(formValues(r.Form)); err != nil {
			contents = "Error: " + err.Error()
		} else {
			contents = sqlStr
		}
	} else if err != nil {
		contents = "Error: " + err.Error()
	}
	RenderPartial(w, "ddl_preview.html", map[string]any{"Contents": contents})
}

func (s *Server) handleDDLCreate(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlKinds[kind]; !ok {
		log.Printf("DDL create: unknown kind %q on %s", kind, r.URL.Path)
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("DDL create: invalid form data: %v", err)
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sid, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	if err != nil || sid < 1 {
		log.Printf("DDL create %s: missing or invalid server_id %q", kind, r.FormValue("server_id"))
		s.renderDDLModal(w, ddlModalData{
			Partial: "ddl_" + kind + "_modal.html",
			Kind:    kind,
			Values:  formValues(r.Form),
			Error:   "Missing or invalid server id.",
		})
		return
	}
	folderID := r.FormValue("folder_id")
	db := r.FormValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	if s.isDisconnected(sid) {
		log.Printf("DDL create %s: server %d is disconnected", kind, sid)
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

	rerender := func(errMsg string) {
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
		log.Printf("DDL create %s: cannot reach target database: %v", kind, perr)
		rerender("Cannot reach the target database: " + perr.Error())
		return
	}

	sqlStr, err := renderCreateDDL(kind, r.Form)
	if err != nil {
		log.Printf("DDL create %s: build error: %v", kind, err)
		rerender(err.Error())
		return
	}

	tag, err := s.runOnTarget(ctx, kind, sid, db, splitStatements(sqlStr))
	if err != nil {
		log.Printf("DDL create %s failed: %v", kind, err)
		rerender("Execution failed: " + err.Error())
		return
	}

	s.refreshDDLTree(w, folderID)
	RenderPartial(w, "ddl_success.html", map[string]any{"Message": tag})
}

func (s *Server) handleDDLDrop(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	if _, ok := ddlKinds[kind]; !ok {
		log.Printf("DDL drop: unknown kind %q on %s", kind, r.URL.Path)
		http.Error(w, "Unknown DDL object kind", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("DDL drop: invalid form data: %v", err)
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sid, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	if err != nil || sid < 1 {
		log.Printf("DDL drop %s: missing or invalid server_id %q", kind, r.FormValue("server_id"))
		s.renderDDLModal(w, ddlModalData{Partial: "ddl_drop_modal.html", Kind: kind, Error: "Missing or invalid server id."})
		return
	}
	folderID := r.FormValue("folder_id")
	db := r.FormValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		log.Printf("DDL drop %s: object name is required", kind)
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
		log.Printf("DDL drop %s: server %d is disconnected", kind, sid)
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

	rerender := func(errMsg string) {
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
		log.Printf("DDL drop %s: build error: %v", kind, err)
		rerender(err.Error())
		return
	}

	tag, err := s.runOnTarget(ctx, kind, sid, db, []string{sqlStr})
	if err != nil {
		log.Printf("DDL drop %s failed: %v", kind, err)
		rerender("Execution failed: " + err.Error())
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

// alterPrefill loads an object's current server-side attributes so the alter
// form opens showing its existing values. Best-effort: failures are logged
// and only the fields that could be loaded are returned.
func (s *Server) alterPrefill(ctx context.Context, pool *pgxpool.Pool, kind, name, schema, table string) map[string]string {
	if pool == nil || name == "" {
		return nil
	}
	v := map[string]string{}
	switch kind {
	case "tablespace":
		var owner string
		err := pool.QueryRow(ctx, `SELECT pg_get_userbyid(spcowner) FROM pg_tablespace WHERE spcname = $1`, name).Scan(&owner)
		if err != nil {
			log.Printf("alter prefill tablespace %q: %v", name, err)
			return v
		}
		v["owner"] = owner
	case "table":
		var owner string
		err := pool.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE schemaname = $1 AND tablename = $2`, schema, name).Scan(&owner)
		if err != nil {
			log.Printf("alter prefill table %q: %v", name, err)
			return v
		}
		v["owner"] = owner
	case "view":
		var owner string
		err := pool.QueryRow(ctx, `SELECT viewowner FROM pg_views WHERE schemaname = $1 AND viewname = $2`, schema, name).Scan(&owner)
		if err != nil {
			log.Printf("alter prefill view %q: %v", name, err)
			return v
		}
		v["owner"] = owner
	case "matview":
		var owner string
		err := pool.QueryRow(ctx, `SELECT matviewowner FROM pg_matviews WHERE schemaname = $1 AND matviewname = $2`, schema, name).Scan(&owner)
		if err != nil {
			log.Printf("alter prefill materialized view %q: %v", name, err)
			return v
		}
		v["owner"] = owner
	case "sequence":
		var owner string
		var increment, cache, minValue, maxValue int64
		var cycle bool
		err := pool.QueryRow(ctx,
			`SELECT sequenceowner, increment_by, min_value, max_value, cache_size, cycle
FROM pg_sequences WHERE schemaname = $1 AND sequencename = $2`, schema, name).
			Scan(&owner, &increment, &minValue, &maxValue, &cache, &cycle)
		if err != nil {
			log.Printf("alter prefill sequence %q: %v", name, err)
			return v
		}
		v["owner"] = owner
		v["increment"] = strconv.FormatInt(increment, 10)
		v["minvalue"] = strconv.FormatInt(minValue, 10)
		v["maxvalue"] = strconv.FormatInt(maxValue, 10)
		v["cache"] = strconv.FormatInt(cache, 10)
		if cycle {
			v["cycle"] = "on"
		} else {
			v["cycle"] = "off"
		}
	case "function", "procedure":
		ref := qualifiedRoutine(schema, name)
		var owner string
		err := pool.QueryRow(ctx,
			`SELECT pg_get_userbyid(proowner) FROM pg_proc WHERE oid = to_regprocedure($1)`, ref).
			Scan(&owner)
		if err != nil {
			log.Printf("alter prefill %s %q: %v", kind, name, err)
			return v
		}
		v["owner"] = owner
	case "extension":
		var version, ns string
		err := pool.QueryRow(ctx,
			`SELECT e.extversion, n.nspname FROM pg_extension e
JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = $1`, name).
			Scan(&version, &ns)
		if err != nil {
			log.Printf("alter prefill extension %q: %v", name, err)
			return v
		}
		v["version"] = version
	case "publication":
		var owner string
		err := pool.QueryRow(ctx, `SELECT pg_get_userbyid(pubowner) FROM pg_publication WHERE pubname = $1`, name).Scan(&owner)
		if err != nil {
			log.Printf("alter prefill publication %q: %v", name, err)
			return v
		}
		v["owner"] = owner
	case "trigger":
		if schema == "" || table == "" {
			return v
		}
		var enabled string
		err := pool.QueryRow(ctx,
			`SELECT tgenabled FROM pg_trigger WHERE tgname = $1 AND tgrelid = $2::regclass AND NOT tgisinternal`,
			name, qualIdent(schema, table)).Scan(&enabled)
		if err != nil {
			log.Printf("alter prefill trigger %q: %v", name, err)
			return v
		}
		if enabled == "D" {
			v["enabled"] = "off"
		} else {
			v["enabled"] = "on"
		}
	case "schema":
		gen, err := pgdb.New(pool).GetSchemaGeneral(ctx, name)
		if err != nil {
			log.Printf("alter prefill schema %q: %v", name, err)
			return v
		}
		v["owner"] = gen.Owner
	case "database":
		var owner string
		var connlimit int
		var allowconn bool
		err := pool.QueryRow(ctx,
			`SELECT pg_get_userbyid(datdba), datconnlimit, datallowconn
FROM pg_database WHERE datname = $1`, name).
			Scan(&owner, &connlimit, &allowconn)
		if err != nil {
			log.Printf("alter prefill database %q: %v", name, err)
			return v
		}
		v["owner"] = owner
		v["connlimit"] = strconv.Itoa(connlimit)
		if allowconn {
			v["allowconn"] = "on"
		} else {
			v["allowconn"] = "off"
		}
	case "role":
		var super, createdb, createrole, canlogin, inherit, repl bool
		var connlimit int
		var validuntil string
		err := pool.QueryRow(ctx,
			`SELECT rolsuper, rolcreatedb, rolcreaterole, rolcanlogin, rolinherit,
    rolreplication, rolconnlimit,
    COALESCE(to_char(NULLIF(rolvaliduntil, 'infinity'::timestamptz), 'YYYY-MM-DD"T"HH24:MI'), '')
FROM pg_roles WHERE rolname = $1`, name).
			Scan(&super, &createdb, &createrole, &canlogin, &inherit, &repl, &connlimit, &validuntil)
		if err != nil {
			log.Printf("alter prefill role %q: %v", name, err)
			return v
		}
		tri := func(b bool) string {
			if b {
				return "on"
			}
			return "off"
		}
		v["login"] = tri(canlogin)
		v["superuser"] = tri(super)
		v["createdb"] = tri(createdb)
		v["createrole"] = tri(createrole)
		v["inherit"] = tri(inherit)
		v["replication"] = tri(repl)
		v["connlimit"] = strconv.Itoa(connlimit)
		v["validuntil"] = validuntil
	}
	return v
}

// handleDDLAlter builds and runs the ALTER script for kinds that support
// edit-in-place (database, role, schema). Structure mirrors handleDDLCreate:
// errors re-render the form with the submitted values and live dropdowns.
func (s *Server) handleDDLAlter(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	k, ok := ddlKinds[kind]
	if !ok || k.BuildAlter == nil {
		log.Printf("DDL alter: unsupported kind %q on %s", kind, r.URL.Path)
		http.Error(w, "Altering this object kind is not supported", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("DDL alter: invalid form data: %v", err)
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sid, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	if err != nil || sid < 1 {
		log.Printf("DDL alter %s: missing or invalid server_id %q", kind, r.FormValue("server_id"))
		s.renderDDLModal(w, ddlModalData{
			Partial: "ddl_alter_modal.html",
			Kind:    kind,
			Name:    strings.TrimSpace(r.FormValue("name")),
			Values:  formValues(r.Form),
			Error:   "Missing or invalid server id.",
		})
		return
	}
	folderID := r.FormValue("folder_id")
	db := r.FormValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	name := strings.TrimSpace(r.FormValue("name"))
	if s.isDisconnected(sid) {
		log.Printf("DDL alter %s: server %d is disconnected", kind, sid)
		s.renderDDLModal(w, ddlModalData{
			Partial:  "ddl_alter_modal.html",
			Kind:     kind,
			ServerID: sid,
			FolderID: folderID,
			DB:       db,
			Schema:   schema,
			Table:    table,
			Name:     name,
			Values:   formValues(r.Form),
			Error:    "Server is disconnected. Reconnect it first.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pool, perr := s.ddlTargetPool(ctx, kind, sid, db)
	var dd map[string][]string
	if perr != nil {
		log.Printf("DDL alter %s pool: %v", kind, perr)
		dd = map[string][]string{}
	} else {
		dd, _ = s.ddlDropdowns(ctx, kind, pool, schema, table)
	}

	rerender := func(errMsg string) {
		s.renderDDLModal(w, ddlModalData{
			Partial:   "ddl_alter_modal.html",
			Kind:      kind,
			ServerID:  sid,
			FolderID:  folderID,
			DB:        db,
			Schema:    schema,
			Table:     table,
			Name:      name,
			Values:    formValues(r.Form),
			Dropdowns: dd,
			Error:     errMsg,
		})
	}

	if perr != nil {
		rerender("Cannot reach the target database: " + perr.Error())
		return
	}
	sqlStr, err := k.BuildAlter(formValues(r.Form))
	if err != nil {
		log.Printf("DDL alter %s: build error: %v", kind, err)
		rerender(err.Error())
		return
	}
	tag, err := s.runOnTarget(ctx, kind, sid, db, splitStatements(sqlStr))
	if err != nil {
		log.Printf("DDL alter %s failed: %v", kind, err)
		rerender("Execution failed: " + err.Error())
		return
	}

	s.refreshDDLTree(w, folderID)
	RenderPartial(w, "ddl_success.html", map[string]any{"Message": tag})
}
