package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
)

// handleCreateScript generates a pgAdmin-style CREATE TABLE script for a table
// and returns it as JSON {query: "..."} so the client can open a new script
// tab pre-filled with it.
func (s *Server) handleCreateScript(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	queries := pgdb.New(pool)

	cols, err := queries.GetTableColumnsDetailed(ctx, pgdb.GetTableColumnsDetailedParams{
		Column1: pgtype.Text{String: schemaName, Valid: true},
		Column2: pgtype.Text{String: tableName, Valid: true},
	})
	if err != nil {
		http.Error(w, "Failed to query columns: "+err.Error(), http.StatusInternalServerError)
		return
	}

	pkRows, err := queries.GetPrimaryKeyColumns(ctx, pgdb.GetPrimaryKeyColumnsParams{
		Nspname: schemaName,
		Relname: tableName,
	})
	if err != nil {
		http.Error(w, "Failed to query primary key: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tableInfo, err := queries.GetTableInfo(ctx, pgdb.GetTableInfoParams{
		Schemaname: schemaName,
		Tablename:  tableName,
	})
	owner := ""
	tablespace := "pg_default"
	if err == nil {
		owner = tableInfo.Tableowner
		if tableInfo.Tablespace.Valid && tableInfo.Tablespace.String != "" {
			tablespace = tableInfo.Tablespace.String
		}
	}

	query := buildCreateTableScript(schemaName, tableName, owner, tablespace, cols, pkRows)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"query": query})
}

// handleDeleteScript generates a skeleton DELETE statement for a table and
// returns it as JSON {query: "..."} so the client can open a new script tab
// pre-filled with it.
func (s *Server) handleDeleteScript(w http.ResponseWriter, r *http.Request) {
	_, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}

	query := "DELETE FROM " + qualIdent(schemaName, tableName) + "\n\tWHERE <condition>;"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"query": query})
}

// handleInsertScript generates a skeleton INSERT statement for a table and
// returns it as JSON {query: "..."} so the client can open a new script tab
// pre-filled with it. Values are the column names as placeholders, matching
// pgAdmin's INSERT Script output.
func (s *Server) handleInsertScript(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}

	items, err := pgdb.New(pool).GetTableColumns(r.Context(), pgdb.GetTableColumnsParams{
		TableSchema: schemaName,
		TableName:   tableName,
	})
	if err != nil {
		http.Error(w, "Failed to query columns: "+err.Error(), http.StatusInternalServerError)
		return
	}

	cols := make([]string, 0, len(items))
	for _, it := range items {
		cols = append(cols, getString(it))
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")

	query := "INSERT INTO " + qualIdent(schemaName, tableName) + "(\n\t" +
		strings.Join(cols, ", ") + ")\n\tVALUES (" + placeholders + ");"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"query": query})
}

// handleCreateViewScript generates a pgAdmin-style CREATE OR REPLACE VIEW
// script for a view and returns it as JSON {query: "..."}. The view body is
// the live definition returned by pg_get_viewdef.
func (s *Server) handleCreateViewScript(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, viewName, ok := s.loadViewPool(w, r)
	if !ok {
		return
	}

	definition, err := pgdb.New(pool).GetViewDefinition(r.Context(), pgdb.GetViewDefinitionParams{
		Nspname: schemaName,
		Relname: viewName,
	})
	if err != nil {
		http.Error(w, "Failed to query view definition: "+err.Error(), http.StatusInternalServerError)
		return
	}

	query := buildCreateViewScript(schemaName, viewName, definition)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"query": query})
}

// handleInsertViewScript generates a skeleton INSERT statement for a view and
// returns it as JSON {query: "..."}. View columns are read from
// information_schema, exactly as they are for tables.
func (s *Server) handleInsertViewScript(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, viewName, ok := s.loadViewPool(w, r)
	if !ok {
		return
	}

	items, err := pgdb.New(pool).GetTableColumns(r.Context(), pgdb.GetTableColumnsParams{
		TableSchema: schemaName,
		TableName:   viewName,
	})
	if err != nil {
		http.Error(w, "Failed to query columns: "+err.Error(), http.StatusInternalServerError)
		return
	}

	cols := make([]string, 0, len(items))
	for _, it := range items {
		cols = append(cols, getString(it))
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")

	query := "INSERT INTO " + qualIdent(schemaName, viewName) + "(\n\t" +
		strings.Join(cols, ", ") + ")\n\tVALUES (" + placeholders + ");"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"query": query})
}

// selectColumns renders a SELECT column list with one column per line.
func selectColumns(cols []string) string {
	if len(cols) == 0 {
		return "SELECT *"
	}
	return "SELECT\n    " + strings.Join(cols, ",\n    ")
}

// handleSelectViewScript returns a SELECT script for a view as JSON
// {query: "..."}, listing its columns exactly like the table SELECT script.
func (s *Server) handleSelectViewScript(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, viewName, ok := s.loadViewPool(w, r)
	if !ok {
		return
	}

	items, err := pgdb.New(pool).GetTableColumns(r.Context(), pgdb.GetTableColumnsParams{
		TableSchema: schemaName,
		TableName:   viewName,
	})
	if err != nil {
		http.Error(w, "Failed to query columns", http.StatusInternalServerError)
		return
	}

	var cols []string
	for _, it := range items {
		cols = append(cols, getString(it))
	}

	query := selectColumns(cols) + "\nFROM " + viewName + ";"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"query": query})
}

// handleSelectMatViewScript returns a SELECT script for a materialized view
// as JSON {query: "..."}, listing its columns like the table/view SELECT
// script. information_schema.columns does not expose materialized views, so
// the catalog-based GetMatViewColumnsDetailed is used instead.
func (s *Server) handleSelectMatViewScript(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}
	mvName := chi.URLParam(r, "mvName")

	items, err := pgdb.New(pool).GetMatViewColumnsDetailed(r.Context(), pgdb.GetMatViewColumnsDetailedParams{
		Nspname: schemaName,
		Relname: mvName,
	})
	if err != nil {
		http.Error(w, "Failed to query columns", http.StatusInternalServerError)
		return
	}

	var cols []string
	for _, it := range items {
		cols = append(cols, it.ColumnName)
	}

	query := selectColumns(cols) + "\nFROM " + qualIdent(schemaName, mvName) + ";"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"query": query})
}

// columnLine renders one CREATE TABLE column definition from its metadata,
// matching pgAdmin's formatting: serial detection, collation, NOT NULL and
// DEFAULT clauses.
func columnLine(col pgdb.GetTableColumnsDetailedRow) string {
	dataType := getString(col.DataType)
	if maxLen := optString(col.CharacterMaximumLength); maxLen != "" {
		switch dataType {
		case "character varying":
			dataType = "character varying(" + maxLen + ")"
		case "character":
			dataType = "character(" + maxLen + ")"
		}
	}

	def := optString(col.ColumnDefault)
	isSerial := false
	if def != "" && nextvalPattern.MatchString(def) {
		switch dataType {
		case "integer":
			dataType = "serial"
			isSerial = true
		case "bigint":
			dataType = "bigserial"
			isSerial = true
		case "smallint":
			dataType = "smallserial"
			isSerial = true
		}
	}

	s := quoteIfNeeded(getString(col.ColumnName)) + " " + dataType

	if coll := getString(col.Collation); coll != "" {
		s += " COLLATE pg_catalog." + quoteIdent(coll)
	}

	if getString(col.IsNullable) == "NO" {
		s += " NOT NULL"
	}

	if def != "" && !isSerial {
		s += " DEFAULT " + def
	}

	return s
}

// qualIdent joins a schema and table name into a qualification string,
// quoting either part only when required.
func qualIdent(schema, table string) string {
	return quoteIfNeeded(schema) + "." + quoteIfNeeded(table)
}

// quoteIdent wraps an identifier in double quotes, escaping embedded quotes.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

var safeIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// quoteIfNeeded returns s as-is when it is a safe lowercase identifier that
// needs no quoting, otherwise it double-quotes it.
func quoteIfNeeded(s string) string {
	if safeIdent.MatchString(s) {
		return s
	}
	return quoteIdent(s)
}

var nextvalPattern = regexp.MustCompile(`^nextval\(.*'::regclass\)$`)

// optString formats a possibly-nil sqlc value as a string, returning "" for
// NULL.
func optString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// buildCreateTableScript renders a pgAdmin-style CREATE TABLE script for a
// table. It is shared by handleCreateScript and the table properties SQL tab.
func buildCreateTableScript(schemaName, tableName, owner, tablespace string, cols []pgdb.GetTableColumnsDetailedRow, pkRows []pgdb.GetPrimaryKeyColumnsRow) string {
	var b strings.Builder
	qualified := qualIdent(schemaName, tableName)

	b.WriteString("-- Table: ")
	b.WriteString(qualified)
	b.WriteString("\n\n-- DROP TABLE IF EXISTS ")
	b.WriteString(qualified)
	b.WriteString(";\n\nCREATE TABLE IF NOT EXISTS ")
	b.WriteString(qualified)
	b.WriteString("\n(\n")

	for i, col := range cols {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString("    ")
		b.WriteString(columnLine(col))
	}

	if len(pkRows) > 0 {
		if len(cols) > 0 {
			b.WriteString(",\n")
		}
		names := make([]string, 0, len(pkRows))
		for _, r := range pkRows {
			names = append(names, quoteIfNeeded(r.Attname))
		}
		b.WriteString("    CONSTRAINT ")
		b.WriteString(quoteIfNeeded(pkRows[0].Conname))
		b.WriteString(" PRIMARY KEY (")
		b.WriteString(strings.Join(names, ", "))
		b.WriteString(")")
	}

	b.WriteString("\n)\n\nTABLESPACE ")
	b.WriteString(quoteIfNeeded(tablespace))
	b.WriteString(";\n\n")

	if owner != "" {
		b.WriteString("ALTER TABLE IF EXISTS ")
		b.WriteString(qualified)
		b.WriteString("\n    OWNER to ")
		b.WriteString(quoteIfNeeded(owner))
		b.WriteString(";")
	}

	return b.String()
}

// buildCreateViewScript renders a pgAdmin-style CREATE OR REPLACE VIEW script
// for a view, shared by handleCreateViewScript and the view properties SQL tab.
func buildCreateViewScript(schemaName, viewName, definition string) string {
	var b strings.Builder
	qualified := qualIdent(schemaName, viewName)

	b.WriteString("-- View: ")
	b.WriteString(qualified)
	b.WriteString("\n\n-- DROP VIEW IF EXISTS ")
	b.WriteString(qualified)
	b.WriteString(";\n\nCREATE OR REPLACE VIEW ")
	b.WriteString(qualified)
	b.WriteString(" AS\n")
	b.WriteString(strings.TrimRight(definition, " \t\r\n"))
	b.WriteString(";\n")

	return b.String()
}

// buildCreateMatViewScript renders a pgAdmin-style CREATE MATERIALIZED VIEW
// script for a materialized view, shared by the matview properties SQL tab.
func buildCreateMatViewScript(schemaName, matViewName, definition string) string {
	var b strings.Builder
	qualified := qualIdent(schemaName, matViewName)

	b.WriteString("-- Materialized View: ")
	b.WriteString(qualified)
	b.WriteString("\n\n-- DROP MATERIALIZED VIEW IF EXISTS ")
	b.WriteString(qualified)
	b.WriteString(";\n\nCREATE MATERIALIZED VIEW ")
	b.WriteString(qualified)
	b.WriteString(" AS\n")
	b.WriteString(strings.TrimRight(definition, " \t\r\n"))
	b.WriteString(";\n")

	return b.String()
}
