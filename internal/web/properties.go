package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
)

// -----------------------------------------------------------------------------
// View model shared by the table / view properties panels.
// -----------------------------------------------------------------------------

// propKV is one label/value row of a generic metadata section (General,
// Statistics).
type propKV struct {
	Label string
	Value string
}

type propColumn struct {
	Name      string
	DataType  string
	Length    string
	Nullable  string
	Default   string
	Collation string
	Comment   string
}

type propConstraint struct {
	Name       string
	Type       string
	Definition string
	Deferrable string
}

type propIndex struct {
	Name       string
	Definition string
	Unique     string
	Tablespace string
	Comment    string
}

type propPrivilege struct {
	Grantee   string
	Privilege string
	Grantable string
}

type propDependency struct {
	Kind   string
	Name   string
	Detail string
}

// propertiesVM carries everything the properties partial needs. Kind drives
// which in-panel section tabs are rendered ("table", "view",
// "materialized-view", "sequence", "function", "index", "trigger",
// "schema"). Errored sections simply stay empty, matching how tree folders
// degrade gracefully.
type propertiesVM struct {
	Kind         string
	KindLabel    string
	Icon         string
	Qualified    string
	General      []propKV
	Columns      []propColumn
	IndexColumns []string
	Constraints  []propConstraint
	Indexes      []propIndex
	Privileges   []propPrivilege
	Dependencies []propDependency
	Stats        []propKV
	SQL          string
	Err          string
}

// -----------------------------------------------------------------------------
// Property queries
// -----------------------------------------------------------------------------

// propertiesGeneralErr handles a failed object General query: pgx.ErrNoRows
// renders the panel's "not found" state, anything else is logged and answered
// with a 500. Callers invoke it and return when err != nil.
func propertiesGeneralErr(w http.ResponseWriter, kind, notFoundMsg string, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		RenderPartial(w, "properties_panel.html", propertiesVM{Kind: kind, Err: notFoundMsg})
		return
	}
	log.Printf("Failed to load %s general properties: %v", kind, err)
	http.Error(w, "Failed to load "+kind+" properties", http.StatusInternalServerError)
}

// handleTableProperties renders the read-only properties panel of a table:
// metadata sections (General, Columns, Constraints, Indexes, Statistics,
// Privileges, Dependencies) plus a SQL preview of its CREATE script.
func (s *Server) handleTableProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetTableGeneral(ctx, pgdb.GetTableGeneralParams{
		Schemaname: schemaName,
		Tablename:  tableName,
	})
	if err != nil {
		propertiesGeneralErr(w, "table", "Table not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "table",
		KindLabel: "Table",
		Icon:      "📋",
		Qualified: qualIdent(schemaName, tableName),
		General: []propKV{
			{Label: "Owner", Value: gen.Tableowner},
			{Label: "Tablespace", Value: gen.Tablespace},
			{Label: "Row estimate", Value: strconv.FormatInt(gen.RowEstimate, 10)},
			{Label: "Table size", Value: formatBytes(gen.TableSize)},
			{Label: "Has indexes", Value: yn(gen.HasIndexes)},
			{Label: "Partitioned", Value: yn(gen.Partitioned)},
			{Label: "Row level security", Value: yn(gen.RowSecurity)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
	}

	// The detailed column list is fetched once and reused for both the
	// Columns section and the CREATE script preview.
	cols, err := q.GetTableColumnsDetailed(ctx, pgdb.GetTableColumnsDetailedParams{
		Column1: pgtype.Text{String: schemaName, Valid: true},
		Column2: pgtype.Text{String: tableName, Valid: true},
	})
	if err == nil {
		vm.Columns = mapColumnProps(cols)
	} else {
		log.Printf("Failed to load column properties: %v", err)
	}

	vm.loadConstraints(ctx, q, schemaName, tableName)
	vm.loadIndexes(ctx, q, schemaName, tableName)
	vm.loadPrivileges(ctx, q, schemaName, tableName)
	vm.loadDependencies(ctx, q, schemaName, tableName)
	vm.loadStatistics(ctx, q, schemaName, tableName)

	// SQL preview reuses the CREATE script builder so it always matches
	// the "CREATE Script" context-menu output.
	if pkRows, pkErr := q.GetPrimaryKeyColumns(ctx, pgdb.GetPrimaryKeyColumnsParams{
		Nspname: schemaName,
		Relname: tableName,
	}); pkErr == nil {
		owner := ""
		tablespace := "pg_default"
		if info, iErr := q.GetTableInfo(ctx, pgdb.GetTableInfoParams{
			Schemaname: schemaName,
			Tablename:  tableName,
		}); iErr == nil {
			owner = info.Tableowner
			if info.Tablespace.Valid && info.Tablespace.String != "" {
				tablespace = info.Tablespace.String
			}
		}
		vm.SQL = buildCreateTableScript(schemaName, tableName, owner, tablespace, cols, pkRows)
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// handleViewProperties renders the read-only properties panel of a view:
// General, Columns, Privileges, Dependencies and a SQL preview of its CREATE
// OR REPLACE VIEW script. pg_stat_user_tables does not track views, so there
// is no Statistics section.
func (s *Server) handleViewProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, viewName, ok := s.loadViewPool(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetViewGeneral(ctx, pgdb.GetViewGeneralParams{
		Nspname: schemaName,
		Relname: viewName,
	})
	if err != nil {
		propertiesGeneralErr(w, "view", "View not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "view",
		KindLabel: "View",
		Icon:      "🔭",
		Qualified: qualIdent(schemaName, viewName),
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Relation size", Value: formatBytes(gen.RelationSize)},
			{Label: "Number of columns", Value: strconv.FormatInt(gen.ColumnCount, 10)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
	}

	cols, err := q.GetTableColumnsDetailed(ctx, pgdb.GetTableColumnsDetailedParams{
		Column1: pgtype.Text{String: schemaName, Valid: true},
		Column2: pgtype.Text{String: viewName, Valid: true},
	})
	if err == nil {
		vm.Columns = mapColumnProps(cols)
	} else {
		log.Printf("Failed to load view column properties: %v", err)
	}

	vm.loadPrivileges(ctx, q, schemaName, viewName)
	vm.loadDependencies(ctx, q, schemaName, viewName)

	if def, defErr := q.GetViewDefinition(ctx, pgdb.GetViewDefinitionParams{
		Nspname: schemaName,
		Relname: viewName,
	}); defErr == nil {
		vm.SQL = buildCreateViewScript(schemaName, viewName, def)
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Section loaders (best-effort: a failing section stays empty)
// -----------------------------------------------------------------------------

func (vm *propertiesVM) loadConstraints(ctx context.Context, q *pgdb.Queries, schemaName, tableName string) {
	rows, err := q.GetTableConstraints(ctx, pgdb.GetTableConstraintsParams{
		Nspname: schemaName,
		Relname: tableName,
	})
	if err != nil {
		log.Printf("Failed to load constraints: %v", err)
		return
	}
	vm.Constraints = make([]propConstraint, 0, len(rows))
	for _, c := range rows {
		vm.Constraints = append(vm.Constraints, propConstraint{
			Name:       c.Conname,
			Type:       c.ConstraintType,
			Definition: c.Definition,
			Deferrable: yn(c.Condeferrable),
		})
	}
}

func (vm *propertiesVM) loadIndexes(ctx context.Context, q *pgdb.Queries, schemaName, tableName string) {
	rows, err := q.GetTableIndexesDetailed(ctx, pgdb.GetTableIndexesDetailedParams{
		Nspname: schemaName,
		Relname: tableName,
	})
	if err != nil {
		log.Printf("Failed to load indexes: %v", err)
		return
	}
	vm.Indexes = make([]propIndex, 0, len(rows))
	for _, c := range rows {
		vm.Indexes = append(vm.Indexes, propIndex{
			Name:       c.IndexName,
			Definition: c.Definition,
			Unique:     yn(c.IsUnique),
			Tablespace: c.Tablespace,
			Comment:    emptyDash(getString(c.Comment)),
		})
	}
}

func (vm *propertiesVM) loadPrivileges(ctx context.Context, q *pgdb.Queries, schemaName, name string) {
	rows, err := q.GetObjectPrivileges(ctx, pgdb.GetObjectPrivilegesParams{
		TableSchema: schemaName,
		TableName:   name,
	})
	if err != nil {
		log.Printf("Failed to load privileges: %v", err)
		return
	}
	vm.Privileges = make([]propPrivilege, 0, len(rows))
	for _, c := range rows {
		vm.Privileges = append(vm.Privileges, propPrivilege{
			Grantee:   getString(c.Grantee),
			Privilege: getString(c.PrivilegeType),
			Grantable: getString(c.IsGrantable),
		})
	}
}

func (vm *propertiesVM) loadDependencies(ctx context.Context, q *pgdb.Queries, schemaName, name string) {
	rows, err := q.GetObjectDependencies(ctx, pgdb.GetObjectDependenciesParams{
		Nspname: schemaName,
		Relname: name,
	})
	if err != nil {
		log.Printf("Failed to load dependencies: %v", err)
		return
	}
	vm.Dependencies = make([]propDependency, 0, len(rows))
	for _, c := range rows {
		vm.Dependencies = append(vm.Dependencies, propDependency{
			Kind:   c.Kind,
			Name:   getString(c.Name),
			Detail: c.Detail,
		})
	}
}

func (vm *propertiesVM) loadStatistics(ctx context.Context, q *pgdb.Queries, schemaName, tableName string) {
	row, err := q.GetTableStatistics(ctx, pgdb.GetTableStatisticsParams{
		Schemaname: schemaName,
		Relname:    tableName,
	})
	if err != nil {
		log.Printf("Failed to load statistics: %v", err)
		return
	}
	vm.Stats = []propKV{
		{Label: "Sequential scans", Value: strconv.FormatInt(row.SeqScan, 10)},
		{Label: "Sequential tuples read", Value: strconv.FormatInt(row.SeqTupRead, 10)},
		{Label: "Index scans", Value: strconv.FormatInt(row.IdxScan, 10)},
		{Label: "Index tuples fetched", Value: strconv.FormatInt(row.IdxTupFetch, 10)},
		{Label: "Rows inserted", Value: strconv.FormatInt(row.NTupIns, 10)},
		{Label: "Rows updated", Value: strconv.FormatInt(row.NTupUpd, 10)},
		{Label: "Rows deleted", Value: strconv.FormatInt(row.NTupDel, 10)},
		{Label: "Live rows", Value: strconv.FormatInt(row.NLiveTup, 10)},
		{Label: "Dead rows", Value: strconv.FormatInt(row.NDeadTup, 10)},
		{Label: "Last vacuum", Value: fmtTime(row.LastVacuum)},
		{Label: "Last autovacuum", Value: fmtTime(row.LastAutovacuum)},
		{Label: "Last analyze", Value: fmtTime(row.LastAnalyze)},
		{Label: "Last autoanalyze", Value: fmtTime(row.LastAutoanalyze)},
	}
}

// -----------------------------------------------------------------------------
// Formatting helpers
// -----------------------------------------------------------------------------

// mapColumnProps converts the detailed sqlc column rows into the view model.
func mapColumnProps(rows []pgdb.GetTableColumnsDetailedRow) []propColumn {
	cols := make([]propColumn, 0, len(rows))
	for _, c := range rows {
		cols = append(cols, propColumn{
			Name:      getString(c.ColumnName),
			DataType:  getString(c.DataType),
			Length:    optString(c.CharacterMaximumLength),
			Nullable:  getString(c.IsNullable),
			Default:   emptyDash(optString(c.ColumnDefault)),
			Collation: getString(c.Collation),
			Comment:   emptyDash(optString(c.Comment)),
		})
	}
	return cols
}

// mapMatViewColumnProps is the catalog-based equivalent of mapColumnProps for
// materialized views, whose columns information_schema does not expose.
func mapMatViewColumnProps(rows []pgdb.GetMatViewColumnsDetailedRow) []propColumn {
	cols := make([]propColumn, 0, len(rows))
	for _, c := range rows {
		cols = append(cols, propColumn{
			Name:      c.ColumnName,
			DataType:  c.DataType,
			Length:    optString(c.CharacterMaximumLength),
			Nullable:  c.IsNullable,
			Default:   emptyDash(optString(c.ColumnDefault)),
			Collation: c.Collation,
			Comment:   emptyDash(optString(c.Comment)),
		})
	}
	return cols
}

// formatBytes renders a byte count as "1.5 kB", "3.2 MB", ... like clients
// such as pgAdmin display relation sizes.
func formatBytes(n int64) string {
	if n == 0 {
		return "0 bytes"
	}
	div, exp := int64(1024), 0
	for m := n / 1024; m >= 1024; m /= 1024 {
		div *= 1024
		exp++
	}
	if exp < 0 {
		exp = 0
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// yn renders a bool as the "Yes"/"No" wording pgAdmin uses in property tables.
func yn(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// fmtTime formats a possibly-null timestamp as a display string.
func fmtTime(t pgtype.Timestamptz) string {
	if !t.Valid {
		return "—"
	}
	return t.Time.Format("2006-01-02 15:04:05")
}

// emptyDash renders "" as an em dash so empty values read as "not set".
func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// formatInt8 renders a possibly-null 64-bit integer as its plain value.
func formatInt8(v pgtype.Int8) string {
	if !v.Valid {
		return "—"
	}
	return strconv.FormatInt(v.Int64, 10)
}

// formatBool renders a possibly-null boolean as Yes/No.
func formatBool(v pgtype.Bool) string {
	if !v.Valid {
		return "—"
	}
	return yn(v.Bool)
}

// textVal unwraps a possibly-null sqlc text value into a plain string.
func textVal(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

// -----------------------------------------------------------------------------
// Materialized view properties
// -----------------------------------------------------------------------------

// handleMatViewProperties renders the read-only properties panel of a
// materialized view: General, Columns, Indexes, Privileges, Statistics,
// Dependencies and a SQL preview of its CREATE MATERIALIZED VIEW script.
func (s *Server) handleMatViewProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}
	mvName := chi.URLParam(r, "mvName")
	if mvName == "" {
		http.Error(w, "Invalid materialized view name", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetMatViewGeneral(ctx, pgdb.GetMatViewGeneralParams{
		Nspname: schemaName,
		Relname: mvName,
	})
	if err != nil {
		propertiesGeneralErr(w, "materialized view", "Materialized view not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "materialized-view",
		KindLabel: "Materialized View",
		Icon:      "🧊",
		Qualified: qualIdent(schemaName, mvName),
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Row estimate", Value: strconv.FormatInt(gen.RowEstimate, 10)},
			{Label: "Total size", Value: formatBytes(gen.RelationSize)},
			{Label: "Has indexes", Value: yn(gen.HasIndexes)},
			{Label: "Number of columns", Value: strconv.FormatInt(gen.ColumnCount, 10)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
	}

	cols, err := q.GetMatViewColumnsDetailed(ctx, pgdb.GetMatViewColumnsDetailedParams{
		Nspname: schemaName,
		Relname: mvName,
	})
	if err == nil {
		vm.Columns = mapMatViewColumnProps(cols)
	} else {
		log.Printf("Failed to load materialized view column properties: %v", err)
	}

	vm.loadIndexes(ctx, q, schemaName, mvName)
	vm.loadPrivileges(ctx, q, schemaName, mvName)
	vm.loadDependencies(ctx, q, schemaName, mvName)
	vm.loadStatistics(ctx, q, schemaName, mvName)

	if def, defErr := q.GetMatViewDefinition(ctx, pgdb.GetMatViewDefinitionParams{
		Nspname: schemaName,
		Relname: mvName,
	}); defErr == nil {
		vm.SQL = buildCreateMatViewScript(schemaName, mvName, def)
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Sequence properties
// -----------------------------------------------------------------------------

// handleSequenceProperties renders the read-only properties panel of a
// sequence: General, Privileges and a SQL preview of its CREATE SEQUENCE
// script.
func (s *Server) handleSequenceProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}
	seqName := chi.URLParam(r, "seqName")
	if seqName == "" {
		http.Error(w, "Invalid sequence name", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetSequenceGeneral(ctx, pgdb.GetSequenceGeneralParams{
		Schemaname:   schemaName,
		Sequencename: seqName,
	})
	if err != nil {
		propertiesGeneralErr(w, "sequence", "Sequence not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "sequence",
		KindLabel: "Sequence",
		Icon:      "🔢",
		Qualified: qualIdent(schemaName, seqName),
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Data type", Value: emptyDash(textVal(gen.DataType))},
			{Label: "Start value", Value: formatInt8(gen.StartValue)},
			{Label: "Increment", Value: formatInt8(gen.IncrementBy)},
			{Label: "Minimum value", Value: formatInt8(gen.MinValue)},
			{Label: "Maximum value", Value: formatInt8(gen.MaxValue)},
			{Label: "Cache size", Value: formatInt8(gen.CacheSize)},
			{Label: "Last value", Value: formatInt8(gen.LastValue)},
			{Label: "Cycles", Value: formatBool(gen.Cycle)},
			{Label: "Relation size", Value: formatBytes(gen.RelationSize)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
	}

	vm.loadPrivileges(ctx, q, schemaName, seqName)
	vm.SQL = buildCreateSequenceScript(schemaName, seqName, gen)

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Function properties
// -----------------------------------------------------------------------------

// handleFunctionProperties renders the read-only properties panel of a
// function: General (return type, language, volatility, ...) and a SQL preview
// of its definition. The route param is the URL-escaped name(signature) label
// shown by the tree, which is also the lookup key in pg_proc.
func (s *Server) handleFunctionProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}
	fnName := chi.URLParam(r, "funcName")
	if fnName == "" {
		http.Error(w, "Invalid function name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(fnName); err == nil {
		fnName = unescaped
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetFunctionGeneral(ctx, pgdb.GetFunctionGeneralParams{
		Nspname: schemaName,
		Proname: fnName,
	})
	if err != nil {
		propertiesGeneralErr(w, "function", "Function not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "function",
		KindLabel: "Function",
		Icon:      "⚙️",
		Qualified: quoteIfNeeded(schemaName) + "." + fnName,
		General: []propKV{
			{Label: "Kind", Value: gen.Kind},
			{Label: "Owner", Value: gen.Owner},
			{Label: "Return type", Value: gen.ReturnType},
			{Label: "Arguments", Value: gen.Arguments},
			{Label: "Language", Value: gen.Language},
			{Label: "Volatility", Value: gen.Volatility},
			{Label: "Parallel", Value: gen.Parallel},
			{Label: "Security definer", Value: yn(gen.SecurityDefiner)},
			{Label: "Strict", Value: yn(gen.Strict)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
	}

	if def, defErr := q.GetFunctionDefinition(ctx, pgdb.GetFunctionDefinitionParams{
		Nspname: schemaName,
		Proname: fnName,
	}); defErr == nil {
		vm.SQL = def
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Index properties
// -----------------------------------------------------------------------------

// handleIndexProperties renders the read-only properties panel of one index of
// a table: General (definition, uniqueness, tablespace, ...), the indexed
// columns and a SQL preview of its CREATE INDEX script.
func (s *Server) handleIndexProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}
	indexName := chi.URLParam(r, "indexName")
	if indexName == "" {
		http.Error(w, "Invalid index name", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetIndexGeneral(ctx, pgdb.GetIndexGeneralParams{
		Nspname:   schemaName,
		Relname:   tableName,
		Relname_2: indexName,
	})
	if err != nil {
		propertiesGeneralErr(w, "index", "Index not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "index",
		KindLabel: "Index",
		Icon:      "📇",
		Qualified: qualIdent(schemaName, tableName) + "." + quoteIfNeeded(gen.IndexName),
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Access method", Value: gen.AccessMethod},
			{Label: "Tablespace", Value: gen.Tablespace},
			{Label: "Unique", Value: yn(gen.IsUnique)},
			{Label: "Relation size", Value: formatBytes(gen.RelationSize)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: strings.TrimRight(gen.Definition, " \t\r\n") + ";",
	}

	if cols, cErr := q.GetIndexColumns(ctx, pgdb.GetIndexColumnsParams{
		Nspname:   schemaName,
		Relname:   tableName,
		Relname_2: indexName,
	}); cErr == nil {
		vm.IndexColumns = make([]string, 0, len(cols))
		for _, c := range cols {
			vm.IndexColumns = append(vm.IndexColumns, getString(c))
		}
	} else {
		log.Printf("Failed to load index columns: %v", cErr)
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Trigger properties
// -----------------------------------------------------------------------------

// handleTriggerProperties renders the read-only properties panel of one
// trigger of a table: General (timing, events, function, ...) and a SQL
// preview of its CREATE TRIGGER script.
func (s *Server) handleTriggerProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}
	triggerName := chi.URLParam(r, "triggerName")
	if triggerName == "" {
		http.Error(w, "Invalid trigger name", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetTriggerGeneral(ctx, pgdb.GetTriggerGeneralParams{
		Nspname: schemaName,
		Relname: tableName,
		Tgname:  triggerName,
	})
	if err != nil {
		propertiesGeneralErr(w, "trigger", "Trigger not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "trigger",
		KindLabel: "Trigger",
		Icon:      "💥",
		Qualified: qualIdent(schemaName, tableName) + "." + quoteIfNeeded(gen.Tgname),
		General: []propKV{
			{Label: "Table", Value: emptyDash(getString(gen.TableName))},
			{Label: "Timing", Value: gen.Timing},
			{Label: "Event", Value: emptyDash(getString(gen.Events))},
			{Label: "Enabled", Value: emptyDash(getString(gen.Enabled))},
			{Label: "Function", Value: gen.FunctionName},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: strings.TrimRight(gen.Definition, " \t\r\n;") + ";",
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Schema properties
// -----------------------------------------------------------------------------

// handleSchemaProperties renders the read-only properties panel of a schema:
// General (owner, comment), its privileges expanded from the namespace ACL and
// a simple CREATE SCHEMA preview.
func (s *Server) handleSchemaProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetSchemaGeneral(ctx, schemaName)
	if err != nil {
		propertiesGeneralErr(w, "schema", "Schema not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "schema",
		KindLabel: "Schema",
		Icon:      "🗂️",
		Qualified: quoteIfNeeded(schemaName),
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: buildCreateSchemaScript(schemaName, gen.Owner, getString(gen.Comment)),
	}

	if privs, pErr := s.loadSchemaPrivileges(ctx, pool, schemaName); pErr == nil {
		vm.Privileges = privs
	} else {
		log.Printf("Failed to load schema privileges: %v", pErr)
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// loadSchemaPrivileges expands a schema's namespace ACL into per-grantee
// privilege rows. information_schema does not cover schema privileges, so the
// catalog ACL is exploded directly (aclexplode is a C function sqlc cannot
// type, hence the raw query here).
func (s *Server) loadSchemaPrivileges(ctx context.Context, pool *pgxpool.Pool, schemaName string) ([]propPrivilege, error) {
	const q = `SELECT COALESCE(r.rolname, 'PUBLIC') AS grantee,
    a.privilege_type,
    a.is_grantable
FROM pg_namespace n
JOIN LATERAL aclexplode(n.nspacl) AS a ON true
LEFT JOIN pg_roles r ON r.oid = a.grantee
WHERE n.nspname = $1
ORDER BY 1, 2`

	rows, err := pool.Query(ctx, q, schemaName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var privs []propPrivilege
	for rows.Next() {
		var grantee, privilegeType string
		var isGrantable bool
		if err := rows.Scan(&grantee, &privilegeType, &isGrantable); err != nil {
			return nil, err
		}
		privs = append(privs, propPrivilege{
			Grantee:   grantee,
			Privilege: privilegeType,
			Grantable: yn(isGrantable),
		})
	}
	return privs, rows.Err()
}

// -----------------------------------------------------------------------------
// Object-specific CREATE script builders
// -----------------------------------------------------------------------------

// buildCreateSequenceScript renders a pgAdmin-style CREATE SEQUENCE script
// from the sequence's live catalog settings.
func buildCreateSequenceScript(schemaName, seqName string, gen pgdb.GetSequenceGeneralRow) string {
	var b strings.Builder
	qualified := qualIdent(schemaName, seqName)

	b.WriteString("-- Sequence: ")
	b.WriteString(qualified)
	b.WriteString("\n\n-- DROP SEQUENCE IF EXISTS ")
	b.WriteString(qualified)
	b.WriteString(";\n\nCREATE SEQUENCE IF NOT EXISTS ")
	b.WriteString(qualified)

	if dt := textVal(gen.DataType); dt != "" {
		b.WriteString("\n    AS ")
		b.WriteString(dt)
	}
	if gen.StartValue.Valid {
		b.WriteString("\n    START WITH ")
		b.WriteString(strconv.FormatInt(gen.StartValue.Int64, 10))
	}
	if gen.IncrementBy.Valid {
		b.WriteString("\n    INCREMENT BY ")
		b.WriteString(strconv.FormatInt(gen.IncrementBy.Int64, 10))
	}
	if gen.MinValue.Valid {
		b.WriteString("\n    MINVALUE ")
		b.WriteString(strconv.FormatInt(gen.MinValue.Int64, 10))
	}
	if gen.MaxValue.Valid {
		b.WriteString("\n    MAXVALUE ")
		b.WriteString(strconv.FormatInt(gen.MaxValue.Int64, 10))
	}
	if gen.CacheSize.Valid {
		b.WriteString("\n    CACHE ")
		b.WriteString(strconv.FormatInt(gen.CacheSize.Int64, 10))
	}
	b.WriteString("\n    ")
	if gen.Cycle.Valid && gen.Cycle.Bool {
		b.WriteString("CYCLE")
	} else {
		b.WriteString("NO CYCLE")
	}
	b.WriteString(";\n")

	if getString(gen.Comment) != "" {
		b.WriteString("\nCOMMENT ON SEQUENCE ")
		b.WriteString(qualified)
		b.WriteString(" IS ")
		b.WriteString(quoteLiteral(getString(gen.Comment)))
		b.WriteString(";")
	}

	return b.String()
}

// buildCreateSchemaScript renders a pgAdmin-style CREATE SCHEMA script.
func buildCreateSchemaScript(schemaName, owner, comment string) string {
	var b strings.Builder
	b.WriteString("-- Schema: ")
	b.WriteString(quoteIfNeeded(schemaName))
	b.WriteString("\n\n-- DROP SCHEMA IF EXISTS ")
	b.WriteString(quoteIfNeeded(schemaName))
	b.WriteString(";\n\nCREATE SCHEMA ")
	b.WriteString(quoteIfNeeded(schemaName))

	if owner != "" {
		b.WriteString("\n    AUTHORIZATION ")
		b.WriteString(quoteIfNeeded(owner))
	}
	b.WriteString(";\n")

	if comment != "" {
		b.WriteString("\nCOMMENT ON SCHEMA ")
		b.WriteString(quoteIfNeeded(schemaName))
		b.WriteString(" IS ")
		b.WriteString(quoteLiteral(comment))
		b.WriteString(";")
	}

	return b.String()
}
