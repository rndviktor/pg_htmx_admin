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

	if privs, pErr := s.loadFunctionPrivileges(ctx, pool, schemaName, fnName); pErr == nil {
		vm.Privileges = privs
	} else {
		log.Printf("Failed to load function privileges: %v", pErr)
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

// -----------------------------------------------------------------------------
// Database properties
// -----------------------------------------------------------------------------

// handleDatabaseProperties renders the read-only properties panel of a
// database: General (owner, encoding, collation, tablespace, connection
// limit, size, ...) and a SQL preview of its CREATE DATABASE script.
func (s *Server) handleDatabaseProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, dbName, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}

	ctx := r.Context()

	const q = `SELECT pg_get_userbyid(d.datdba) AS owner,
    pg_encoding_to_char(d.encoding) AS encoding,
    d.datcollate AS collate,
    d.datctype AS ctype,
    COALESCE(t.spcname, 'pg_default') AS tablespace,
    d.datconnlimit AS connlimit,
    d.datallowconn AS allowconn,
    pg_database_size(d.datname) AS size,
    COALESCE(shobj_description(d.oid, 'pg_database'), '') AS comment
FROM pg_database d
LEFT JOIN pg_tablespace t ON t.oid = d.dattablespace
WHERE d.datname = $1`

	var owner, encoding, collate, ctype, tablespace, comment string
	var connLimit int
	var allowConn bool
	var size int64
	err := pool.QueryRow(ctx, q, dbName).Scan(
		&owner, &encoding, &collate, &ctype, &tablespace, &connLimit, &allowConn, &size, &comment,
	)
	if err != nil {
		propertiesGeneralErr(w, "database", "Database not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "database",
		KindLabel: "Database",
		Icon:      "🗄️",
		Qualified: quoteIfNeeded(dbName),
		General: []propKV{
			{Label: "Owner", Value: owner},
			{Label: "Encoding", Value: encoding},
			{Label: "Collation", Value: collate},
			{Label: "Character type", Value: ctype},
			{Label: "Tablespace", Value: tablespace},
			{Label: "Connection limit", Value: strconv.Itoa(connLimit)},
			{Label: "Allow connections", Value: yn(allowConn)},
			{Label: "Database size", Value: formatBytes(size)},
			{Label: "Comment", Value: emptyDash(comment)},
		},
		SQL: buildCreateDatabaseScript(dbName, owner, encoding, collate, ctype, tablespace, connLimit, comment),
	}

	if privs, pErr := s.loadDatabasePrivileges(ctx, pool, dbName); pErr == nil {
		vm.Privileges = privs
	} else {
		log.Printf("Failed to load database privileges: %v", pErr)
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreateDatabaseScript renders a pgAdmin-style CREATE DATABASE script
// from the database's live catalog settings.
func buildCreateDatabaseScript(name, owner, encoding, collate, ctype, tablespace string, connLimit int, comment string) string {
	var b strings.Builder
	b.WriteString("-- Database: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\n-- DROP DATABASE IF EXISTS ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString(";\n\nCREATE DATABASE ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n    WITH")
	if owner != "" {
		b.WriteString("\n    OWNER = ")
		b.WriteString(quoteIfNeeded(owner))
	}
	if encoding != "" {
		b.WriteString("\n    ENCODING = ")
		b.WriteString(quoteLiteral(encoding))
	}
	if collate != "" {
		b.WriteString("\n    LC_COLLATE = ")
		b.WriteString(quoteLiteral(collate))
	}
	if ctype != "" {
		b.WriteString("\n    LC_CTYPE = ")
		b.WriteString(quoteLiteral(ctype))
	}
	if tablespace != "" {
		b.WriteString("\n    TABLESPACE = ")
		b.WriteString(quoteIfNeeded(tablespace))
	}
	b.WriteString("\n    CONNECTION LIMIT = ")
	b.WriteString(strconv.Itoa(connLimit))
	b.WriteString(";\n")

	if comment != "" {
		b.WriteString("\nCOMMENT ON DATABASE ")
		b.WriteString(quoteIfNeeded(name))
		b.WriteString(" IS ")
		b.WriteString(quoteLiteral(comment))
		b.WriteString(";")
	}

	return b.String()
}

// -----------------------------------------------------------------------------
// Role properties
// -----------------------------------------------------------------------------

// handleRoleProperties renders the read-only properties panel of a role:
// General (privileges, connection limit, valid until, ...) and a SQL preview
// of its CREATE ROLE script. The catalog never exposes a password hash, so
// the script omits one, matching pgAdmin's own generated scripts.
func (s *Server) handleRoleProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := s.loadServerPool(w, r)
	if !ok {
		return
	}
	roleName := chi.URLParam(r, "roleName")
	if roleName == "" {
		http.Error(w, "Invalid role name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(roleName); err == nil {
		roleName = unescaped
	}

	ctx := r.Context()

	const q = `SELECT rolsuper, rolcreatedb, rolcreaterole, rolcanlogin, rolinherit,
    rolreplication, rolconnlimit,
    COALESCE(to_char(NULLIF(rolvaliduntil, 'infinity'::timestamptz), 'YYYY-MM-DD"T"HH24:MI'), ''),
    COALESCE(shobj_description(oid, 'pg_authid'), '')
FROM pg_roles WHERE rolname = $1`

	var super, createdb, createrole, canLogin, inherit, repl bool
	var connLimit int
	var validUntil, comment string
	err := pool.QueryRow(ctx, q, roleName).Scan(
		&super, &createdb, &createrole, &canLogin, &inherit, &repl, &connLimit, &validUntil, &comment,
	)
	if err != nil {
		propertiesGeneralErr(w, "role", "Role not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "role",
		KindLabel: "Role",
		Icon:      "👤",
		Qualified: quoteIfNeeded(roleName),
		General: []propKV{
			{Label: "Superuser", Value: yn(super)},
			{Label: "Can login", Value: yn(canLogin)},
			{Label: "Create databases", Value: yn(createdb)},
			{Label: "Create roles", Value: yn(createrole)},
			{Label: "Inherit", Value: yn(inherit)},
			{Label: "Replication", Value: yn(repl)},
			{Label: "Connection limit", Value: strconv.Itoa(connLimit)},
			{Label: "Valid until", Value: emptyDash(validUntil)},
			{Label: "Comment", Value: emptyDash(comment)},
		},
		SQL: buildCreateRoleScript(roleName, super, createdb, createrole, canLogin, inherit, repl, connLimit, validUntil, comment),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreateRoleScript renders a pgAdmin-style CREATE ROLE script.
func buildCreateRoleScript(name string, super, createdb, createrole, canLogin, inherit, replication bool, connLimit int, validUntil, comment string) string {
	tri := func(b bool, on, off string) string {
		if b {
			return on
		}
		return off
	}

	var b strings.Builder
	b.WriteString("-- Role: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\n-- DROP ROLE IF EXISTS ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString(";\n\nCREATE ROLE ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString(" WITH\n    ")
	b.WriteString(tri(super, "SUPERUSER", "NOSUPERUSER"))
	b.WriteString("\n    ")
	b.WriteString(tri(createdb, "CREATEDB", "NOCREATEDB"))
	b.WriteString("\n    ")
	b.WriteString(tri(createrole, "CREATEROLE", "NOCREATEROLE"))
	b.WriteString("\n    ")
	b.WriteString(tri(inherit, "INHERIT", "NOINHERIT"))
	b.WriteString("\n    ")
	b.WriteString(tri(canLogin, "LOGIN", "NOLOGIN"))
	b.WriteString("\n    ")
	b.WriteString(tri(replication, "REPLICATION", "NOREPLICATION"))
	b.WriteString("\n    CONNECTION LIMIT ")
	b.WriteString(strconv.Itoa(connLimit))
	if validUntil != "" {
		b.WriteString("\n    VALID UNTIL ")
		b.WriteString(quoteLiteral(validUntil))
	}
	b.WriteString(";\n")

	if comment != "" {
		b.WriteString("\nCOMMENT ON ROLE ")
		b.WriteString(quoteIfNeeded(name))
		b.WriteString(" IS ")
		b.WriteString(quoteLiteral(comment))
		b.WriteString(";")
	}

	return b.String()
}

// -----------------------------------------------------------------------------
// Tablespace properties
// -----------------------------------------------------------------------------

// handleTablespaceProperties renders the read-only properties panel of a
// tablespace: General (owner, filesystem location, ...) and a SQL preview of
// its CREATE TABLESPACE script.
func (s *Server) handleTablespaceProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := s.loadServerPool(w, r)
	if !ok {
		return
	}
	tsName := chi.URLParam(r, "tsName")
	if tsName == "" {
		http.Error(w, "Invalid tablespace name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(tsName); err == nil {
		tsName = unescaped
	}

	ctx := r.Context()

	const q = `SELECT pg_get_userbyid(spcowner),
    COALESCE(pg_tablespace_location(oid), ''),
    COALESCE(shobj_description(oid, 'pg_tablespace'), '')
FROM pg_tablespace WHERE spcname = $1`

	var owner, location, comment string
	err := pool.QueryRow(ctx, q, tsName).Scan(&owner, &location, &comment)
	if err != nil {
		propertiesGeneralErr(w, "tablespace", "Tablespace not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "tablespace",
		KindLabel: "Tablespace",
		Icon:      "📀",
		Qualified: quoteIfNeeded(tsName),
		General: []propKV{
			{Label: "Owner", Value: owner},
			{Label: "Location", Value: emptyDash(location)},
			{Label: "Comment", Value: emptyDash(comment)},
		},
		SQL: buildCreateTablespaceScript(tsName, owner, location, comment),
	}

	if privs, pErr := s.loadTablespacePrivileges(ctx, pool, tsName); pErr == nil {
		vm.Privileges = privs
	} else {
		log.Printf("Failed to load tablespace privileges: %v", pErr)
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreateTablespaceScript renders a pgAdmin-style CREATE TABLESPACE script.
func buildCreateTablespaceScript(name, owner, location, comment string) string {
	var b strings.Builder
	b.WriteString("-- Tablespace: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\n-- DROP TABLESPACE IF EXISTS ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString(";\n\nCREATE TABLESPACE ")
	b.WriteString(quoteIfNeeded(name))
	if owner != "" {
		b.WriteString("\n    OWNER ")
		b.WriteString(quoteIfNeeded(owner))
	}
	b.WriteString("\n    LOCATION ")
	b.WriteString(quoteLiteral(location))
	b.WriteString(";\n")

	if comment != "" {
		b.WriteString("\nCOMMENT ON TABLESPACE ")
		b.WriteString(quoteIfNeeded(name))
		b.WriteString(" IS ")
		b.WriteString(quoteLiteral(comment))
		b.WriteString(";")
	}

	return b.String()
}

// -----------------------------------------------------------------------------
// Procedure properties
// -----------------------------------------------------------------------------

// handleProcedureProperties renders the read-only properties panel of a
// procedure: General (owner, arguments, language, ...) and a SQL preview of
// its definition. Procedures share pg_proc with functions, so this reuses
// GetFunctionGeneral/GetFunctionDefinition verbatim (neither query filters on
// prokind) rather than adding new catalog queries.
func (s *Server) handleProcedureProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}
	procName := chi.URLParam(r, "procName")
	if procName == "" {
		http.Error(w, "Invalid procedure name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(procName); err == nil {
		procName = unescaped
	}

	ctx := r.Context()
	q := pgdb.New(pool)

	gen, err := q.GetFunctionGeneral(ctx, pgdb.GetFunctionGeneralParams{
		Nspname: schemaName,
		Proname: procName,
	})
	if err != nil {
		propertiesGeneralErr(w, "procedure", "Procedure not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "procedure",
		KindLabel: "Procedure",
		Icon:      "🛠️",
		Qualified: quoteIfNeeded(schemaName) + "." + procName,
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Arguments", Value: gen.Arguments},
			{Label: "Language", Value: gen.Language},
			{Label: "Security definer", Value: yn(gen.SecurityDefiner)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
	}

	if def, defErr := q.GetFunctionDefinition(ctx, pgdb.GetFunctionDefinitionParams{
		Nspname: schemaName,
		Proname: procName,
	}); defErr == nil {
		vm.SQL = def
	}

	if privs, pErr := s.loadFunctionPrivileges(ctx, pool, schemaName, procName); pErr == nil {
		vm.Privileges = privs
	} else {
		log.Printf("Failed to load procedure privileges: %v", pErr)
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Extension properties
// -----------------------------------------------------------------------------

// handleExtensionProperties renders the read-only properties panel of an
// extension: General (version, install schema, ...) and a SQL preview of its
// CREATE EXTENSION script.
func (s *Server) handleExtensionProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}
	extName := chi.URLParam(r, "extName")
	if extName == "" {
		http.Error(w, "Invalid extension name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(extName); err == nil {
		extName = unescaped
	}

	ctx := r.Context()

	const q = `SELECT e.extversion,
    n.nspname,
    COALESCE(obj_description(e.oid, 'pg_extension'), '')
FROM pg_extension e
JOIN pg_namespace n ON n.oid = e.extnamespace
WHERE e.extname = $1`

	var version, schemaName, comment string
	err := pool.QueryRow(ctx, q, extName).Scan(&version, &schemaName, &comment)
	if err != nil {
		propertiesGeneralErr(w, "extension", "Extension not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "extension",
		KindLabel: "Extension",
		Icon:      "🧩",
		Qualified: quoteIfNeeded(extName),
		General: []propKV{
			{Label: "Version", Value: version},
			{Label: "Schema", Value: schemaName},
			{Label: "Comment", Value: emptyDash(comment)},
		},
		SQL: buildCreateExtensionScript(extName, schemaName, version, comment),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreateExtensionScript renders a pgAdmin-style CREATE EXTENSION script.
func buildCreateExtensionScript(name, schemaName, version, comment string) string {
	var b strings.Builder
	b.WriteString("-- Extension: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\n-- DROP EXTENSION IF EXISTS ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString(";\n\nCREATE EXTENSION IF NOT EXISTS ")
	b.WriteString(quoteIfNeeded(name))
	if schemaName != "" {
		b.WriteString("\n    SCHEMA ")
		b.WriteString(quoteIfNeeded(schemaName))
	}
	if version != "" {
		b.WriteString("\n    VERSION ")
		b.WriteString(quoteLiteral(version))
	}
	b.WriteString(";\n")

	if comment != "" {
		b.WriteString("\nCOMMENT ON EXTENSION ")
		b.WriteString(quoteIfNeeded(name))
		b.WriteString(" IS ")
		b.WriteString(quoteLiteral(comment))
		b.WriteString(";")
	}

	return b.String()
}

// -----------------------------------------------------------------------------
// Publication properties
// -----------------------------------------------------------------------------

// handlePublicationProperties renders the read-only properties panel of a
// publication: General (owner, publish options, ...) and a SQL preview of its
// CREATE PUBLICATION script.
func (s *Server) handlePublicationProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}
	pubName := chi.URLParam(r, "pubName")
	if pubName == "" {
		http.Error(w, "Invalid publication name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(pubName); err == nil {
		pubName = unescaped
	}

	ctx := r.Context()

	const q = `SELECT pg_get_userbyid(pubowner),
    puballtables, pubinsert, pubupdate, pubdelete, pubtruncate, pubviaroot,
    COALESCE(obj_description(oid, 'pg_publication'), '')
FROM pg_publication WHERE pubname = $1`

	var owner, comment string
	var allTables, insert, update, del, truncate, viaRoot bool
	err := pool.QueryRow(ctx, q, pubName).Scan(
		&owner, &allTables, &insert, &update, &del, &truncate, &viaRoot, &comment,
	)
	if err != nil {
		propertiesGeneralErr(w, "publication", "Publication not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "publication",
		KindLabel: "Publication",
		Icon:      "📢",
		Qualified: quoteIfNeeded(pubName),
		General: []propKV{
			{Label: "Owner", Value: owner},
			{Label: "All tables", Value: yn(allTables)},
			{Label: "Publish insert", Value: yn(insert)},
			{Label: "Publish update", Value: yn(update)},
			{Label: "Publish delete", Value: yn(del)},
			{Label: "Publish truncate", Value: yn(truncate)},
			{Label: "Publish via partition root", Value: yn(viaRoot)},
			{Label: "Comment", Value: emptyDash(comment)},
		},
		SQL: buildCreatePublicationScript(pubName, owner, allTables, insert, update, del, truncate, viaRoot, comment),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreatePublicationScript renders a pgAdmin-style CREATE PUBLICATION
// script. Member tables are not enumerated here (that would need a separate
// pg_publication_tables query out of scope for this General/SQL panel).
func buildCreatePublicationScript(name, owner string, allTables, insert, update, del, truncate, viaRoot bool, comment string) string {
	var b strings.Builder
	b.WriteString("-- Publication: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\n-- DROP PUBLICATION IF EXISTS ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString(";\n\nCREATE PUBLICATION ")
	b.WriteString(quoteIfNeeded(name))
	if allTables {
		b.WriteString("\n    FOR ALL TABLES")
	} else {
		b.WriteString("\n    -- FOR TABLE ... (member tables not shown here)")
	}

	var opts []string
	if insert {
		opts = append(opts, "insert")
	}
	if update {
		opts = append(opts, "update")
	}
	if del {
		opts = append(opts, "delete")
	}
	if truncate {
		opts = append(opts, "truncate")
	}
	withOpts := []string{"publish = " + quoteLiteral(strings.Join(opts, ", "))}
	if viaRoot {
		withOpts = append(withOpts, "publish_via_partition_root = true")
	}
	b.WriteString("\n    WITH (")
	b.WriteString(strings.Join(withOpts, ", "))
	b.WriteString(");\n")

	if owner != "" {
		b.WriteString("\nALTER PUBLICATION ")
		b.WriteString(quoteIfNeeded(name))
		b.WriteString(" OWNER TO ")
		b.WriteString(quoteIfNeeded(owner))
		b.WriteString(";")
	}

	if comment != "" {
		b.WriteString("\nCOMMENT ON PUBLICATION ")
		b.WriteString(quoteIfNeeded(name))
		b.WriteString(" IS ")
		b.WriteString(quoteLiteral(comment))
		b.WriteString(";")
	}

	return b.String()
}

// -----------------------------------------------------------------------------
// Column properties (a single leaf under a table's Columns folder)
// -----------------------------------------------------------------------------

func (s *Server) handleColumnProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}
	colName := chi.URLParam(r, "columnName")
	if colName == "" {
		http.Error(w, "Invalid column name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(colName); err == nil {
		colName = unescaped
	}

	ctx := r.Context()
	col, err := pgdb.New(pool).GetColumnDetail(ctx, pgdb.GetColumnDetailParams{
		Column1:    pgtype.Text{String: schemaName, Valid: true},
		Column2:    pgtype.Text{String: tableName, Valid: true},
		ColumnName: colName,
	})
	if err != nil {
		propertiesGeneralErr(w, "column", "Column not found.", err)
		return
	}

	dataType := getString(col.DataType)
	notNull := getString(col.IsNullable) == "NO"
	def := optString(col.ColumnDefault)

	vm := propertiesVM{
		Kind:      "column",
		KindLabel: "Column",
		Icon:      "📊",
		Qualified: quoteIfNeeded(tableName) + "." + quoteIfNeeded(colName),
		General: []propKV{
			{Label: "Type", Value: dataType},
			{Label: "Length", Value: emptyDash(optString(col.CharacterMaximumLength))},
			{Label: "Nullable", Value: getString(col.IsNullable)},
			{Label: "Default", Value: emptyDash(def)},
			{Label: "Collation", Value: emptyDash(col.Collation)},
			{Label: "Comment", Value: emptyDash(optString(col.Comment))},
		},
		SQL: buildColumnDeclarationScript(colName, dataType, notNull, def),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildColumnDeclarationScript renders the column's declaration line, the
// same fragment that would appear in a CREATE TABLE / ADD COLUMN statement.
func buildColumnDeclarationScript(name, dataType string, notNull bool, def string) string {
	var b strings.Builder
	b.WriteString("-- Column: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\n")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString(" ")
	b.WriteString(dataType)
	if notNull {
		b.WriteString(" NOT NULL")
	}
	if def != "" {
		b.WriteString(" DEFAULT ")
		b.WriteString(def)
	}
	b.WriteString(";")
	return b.String()
}

// -----------------------------------------------------------------------------
// Constraint properties (a single leaf under a table's Constraints folder)
// -----------------------------------------------------------------------------

func (s *Server) handleConstraintProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}
	conName := chi.URLParam(r, "constraintName")
	if conName == "" {
		http.Error(w, "Invalid constraint name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(conName); err == nil {
		conName = unescaped
	}

	ctx := r.Context()
	con, err := pgdb.New(pool).GetConstraintDetail(ctx, pgdb.GetConstraintDetailParams{
		Nspname: schemaName,
		Relname: tableName,
		Conname: conName,
	})
	if err != nil {
		propertiesGeneralErr(w, "constraint", "Constraint not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "constraint",
		KindLabel: "Constraint",
		Icon:      "🔒",
		Qualified: quoteIfNeeded(tableName) + "." + quoteIfNeeded(conName),
		General: []propKV{
			{Label: "Type", Value: con.ConstraintType},
			{Label: "Deferrable", Value: yn(con.Condeferrable)},
			{Label: "Deferred", Value: yn(con.Condeferred)},
		},
		SQL: "-- Constraint: " + quoteIfNeeded(conName) + "\n\nALTER TABLE " + quoteIfNeeded(schemaName) + "." +
			quoteIfNeeded(tableName) + "\n    ADD CONSTRAINT " + quoteIfNeeded(conName) + " " + con.Definition + ";",
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// RLS policy properties (a single leaf under a table's RLS Policies folder)
// -----------------------------------------------------------------------------

func (s *Server) handlePolicyProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}
	polName := chi.URLParam(r, "policyName")
	if polName == "" {
		http.Error(w, "Invalid policy name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(polName); err == nil {
		polName = unescaped
	}

	ctx := r.Context()
	pol, err := pgdb.New(pool).GetPolicyGeneral(ctx, pgdb.GetPolicyGeneralParams{
		Schemaname: schemaName,
		Tablename:  tableName,
		Policyname: polName,
	})
	if err != nil {
		propertiesGeneralErr(w, "RLS policy", "Policy not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "rls-policy",
		KindLabel: "RLS Policy",
		Icon:      "🛡️",
		Qualified: quoteIfNeeded(tableName) + "." + quoteIfNeeded(polName),
		General: []propKV{
			{Label: "Permissive", Value: pol.Permissive},
			{Label: "Command", Value: pol.Cmd},
			{Label: "Roles", Value: emptyDash(pol.Roles)},
		},
		SQL: buildCreatePolicyScript(schemaName, tableName, polName, pol.Permissive, pol.Cmd, pol.Roles, pol.UsingExpr, pol.WithCheckExpr),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreatePolicyScript renders a pgAdmin-style CREATE POLICY script.
func buildCreatePolicyScript(schemaName, tableName, name, permissive, cmd, roles, usingExpr, withCheckExpr string) string {
	var b strings.Builder
	b.WriteString("-- Policy: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\nCREATE POLICY ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString(" ON ")
	b.WriteString(quoteIfNeeded(schemaName))
	b.WriteString(".")
	b.WriteString(quoteIfNeeded(tableName))
	if permissive != "PERMISSIVE" {
		b.WriteString("\n    AS RESTRICTIVE")
	}
	if cmd != "" && cmd != "ALL" {
		b.WriteString("\n    FOR ")
		b.WriteString(cmd)
	}
	if roles != "" {
		b.WriteString("\n    TO ")
		b.WriteString(roles)
	}
	if usingExpr != "" {
		b.WriteString("\n    USING (")
		b.WriteString(usingExpr)
		b.WriteString(")")
	}
	if withCheckExpr != "" {
		b.WriteString("\n    WITH CHECK (")
		b.WriteString(withCheckExpr)
		b.WriteString(")")
	}
	b.WriteString(";")
	return b.String()
}

// -----------------------------------------------------------------------------
// Rule properties (a single leaf under a table's Rules folder)
// -----------------------------------------------------------------------------

// handleRuleProperties renders the read-only properties panel of a rewrite
// rule. pg_rules.definition already returns a complete CREATE RULE
// statement, so the SQL tab needs no builder.
func (s *Server) handleRuleProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}
	ruleName := chi.URLParam(r, "ruleName")
	if ruleName == "" {
		http.Error(w, "Invalid rule name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(ruleName); err == nil {
		ruleName = unescaped
	}

	ctx := r.Context()
	definition, err := pgdb.New(pool).GetRuleDefinition(ctx, pgdb.GetRuleDefinitionParams{
		Schemaname: schemaName,
		Tablename:  tableName,
		Rulename:   ruleName,
	})
	if err != nil {
		propertiesGeneralErr(w, "rule", "Rule not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "rule",
		KindLabel: "Rule",
		Icon:      "📜",
		Qualified: quoteIfNeeded(tableName) + "." + quoteIfNeeded(ruleName),
		General: []propKV{
			{Label: "Table", Value: quoteIfNeeded(schemaName) + "." + quoteIfNeeded(tableName)},
		},
		SQL: definition,
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Type properties (composite / enum / range types under a schema)
// -----------------------------------------------------------------------------

func (s *Server) handleTypeProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}
	typeName := chi.URLParam(r, "typeName")
	if typeName == "" {
		http.Error(w, "Invalid type name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(typeName); err == nil {
		typeName = unescaped
	}

	ctx := r.Context()
	q := pgdb.New(pool)
	gen, err := q.GetTypeGeneral(ctx, pgdb.GetTypeGeneralParams{Nspname: schemaName, Typname: typeName})
	if err != nil {
		propertiesGeneralErr(w, "type", "Type not found.", err)
		return
	}

	category := "Composite"
	var extra propKV
	var body string
	switch gen.TypeCategory {
	case "e":
		category = "Enum"
		labels, lerr := q.ListEnumLabels(ctx, gen.TypeOid)
		if lerr != nil {
			log.Printf("Failed to load enum labels for type %s.%s: %v", schemaName, typeName, lerr)
		}
		extra = propKV{Label: "Labels", Value: strings.Join(labels, ", ")}
		quoted := make([]string, len(labels))
		for i, l := range labels {
			quoted[i] = quoteLiteral(l)
		}
		body = "AS ENUM (\n    " + strings.Join(quoted, ",\n    ") + "\n)"
	case "r":
		category = "Range"
		subtype, serr := q.GetRangeSubtype(ctx, gen.TypeOid)
		if serr != nil {
			log.Printf("Failed to load range subtype for type %s.%s: %v", schemaName, typeName, serr)
		}
		extra = propKV{Label: "Subtype", Value: subtype}
		body = "AS RANGE (\n    SUBTYPE = " + subtype + "\n)"
	default:
		attrs, aerr := q.ListCompositeAttributes(ctx, gen.TypeOid)
		if aerr != nil {
			log.Printf("Failed to load composite attributes for type %s.%s: %v", schemaName, typeName, aerr)
		}
		lines := make([]string, len(attrs))
		for i, a := range attrs {
			lines[i] = quoteIfNeeded(a.Attname) + " " + a.DataType
		}
		extra = propKV{Label: "Attributes", Value: strings.Join(lines, ", ")}
		body = "AS (\n    " + strings.Join(lines, ",\n    ") + "\n)"
	}

	vm := propertiesVM{
		Kind:      "type",
		KindLabel: "Type",
		Icon:      "🏷️",
		Qualified: quoteIfNeeded(schemaName) + "." + quoteIfNeeded(typeName),
		General: []propKV{
			{Label: "Category", Value: category},
			{Label: "Owner", Value: gen.Owner},
			extra,
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: "-- Type: " + quoteIfNeeded(typeName) + "\n\nCREATE TYPE " + quoteIfNeeded(schemaName) + "." +
			quoteIfNeeded(typeName) + " " + body + ";",
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Domain properties
// -----------------------------------------------------------------------------

func (s *Server) handleDomainProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}
	domainName := chi.URLParam(r, "domainName")
	if domainName == "" {
		http.Error(w, "Invalid domain name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(domainName); err == nil {
		domainName = unescaped
	}

	ctx := r.Context()
	q := pgdb.New(pool)
	gen, err := q.GetDomainGeneral(ctx, pgdb.GetDomainGeneralParams{Nspname: schemaName, Typname: domainName})
	if err != nil {
		propertiesGeneralErr(w, "domain", "Domain not found.", err)
		return
	}

	checks, cerr := q.ListDomainConstraints(ctx, gen.TypeOid)
	if cerr != nil {
		log.Printf("Failed to load domain constraints for %s.%s: %v", schemaName, domainName, cerr)
	}

	general := []propKV{
		{Label: "Base type", Value: gen.BaseType},
		{Label: "Not null", Value: yn(gen.NotNull)},
		{Label: "Default", Value: emptyDash(gen.DefaultValue)},
		{Label: "Owner", Value: gen.Owner},
	}
	for _, c := range checks {
		general = append(general, propKV{Label: "Check (" + c.Conname + ")", Value: c.Definition})
	}
	general = append(general, propKV{Label: "Comment", Value: emptyDash(getString(gen.Comment))})

	var b strings.Builder
	b.WriteString("-- Domain: ")
	b.WriteString(quoteIfNeeded(domainName))
	b.WriteString("\n\nCREATE DOMAIN ")
	b.WriteString(quoteIfNeeded(schemaName))
	b.WriteString(".")
	b.WriteString(quoteIfNeeded(domainName))
	b.WriteString("\n    AS ")
	b.WriteString(gen.BaseType)
	if gen.DefaultValue != "" {
		b.WriteString("\n    DEFAULT ")
		b.WriteString(gen.DefaultValue)
	}
	if gen.NotNull {
		b.WriteString("\n    NOT NULL")
	}
	for _, c := range checks {
		b.WriteString("\n    CONSTRAINT ")
		b.WriteString(quoteIfNeeded(c.Conname))
		b.WriteString(" ")
		b.WriteString(c.Definition)
	}
	b.WriteString(";")

	vm := propertiesVM{
		Kind:      "domain",
		KindLabel: "Domain",
		Icon:      "📐",
		Qualified: quoteIfNeeded(schemaName) + "." + quoteIfNeeded(domainName),
		General:   general,
		SQL:       b.String(),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Cast properties (identified by a (source, target) type pair, not a name)
// -----------------------------------------------------------------------------

func (s *Server) handleCastProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}
	source := chi.URLParam(r, "source")
	target := chi.URLParam(r, "target")
	if source == "" || target == "" {
		http.Error(w, "Invalid cast", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(source); err == nil {
		source = unescaped
	}
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}

	ctx := r.Context()
	gen, err := pgdb.New(pool).GetCastGeneral(ctx, pgdb.GetCastGeneralParams{
		Column1: source,
		Column2: target,
	})
	if err != nil {
		propertiesGeneralErr(w, "cast", "Cast not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "cast",
		KindLabel: "Cast",
		Icon:      "🔁",
		Qualified: "(" + source + " AS " + target + ")",
		General: []propKV{
			{Label: "Source type", Value: source},
			{Label: "Target type", Value: target},
			{Label: "Context", Value: gen.Context},
			{Label: "Method", Value: gen.Method},
			{Label: "Function", Value: emptyDash(gen.FunctionName)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: buildCreateCastScript(source, target, gen.Method, gen.FunctionName, gen.Context),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreateCastScript renders a pgAdmin-style CREATE CAST script.
func buildCreateCastScript(source, target, method, functionName, context string) string {
	var b strings.Builder
	b.WriteString("-- Cast: (")
	b.WriteString(source)
	b.WriteString(" AS ")
	b.WriteString(target)
	b.WriteString(")\n\nCREATE CAST (")
	b.WriteString(source)
	b.WriteString(" AS ")
	b.WriteString(target)
	b.WriteString(")")
	switch method {
	case "function":
		b.WriteString("\n    WITH FUNCTION ")
		b.WriteString(functionName)
	case "inout":
		b.WriteString("\n    WITH INOUT")
	default:
		b.WriteString("\n    WITHOUT FUNCTION")
	}
	switch context {
	case "assignment":
		b.WriteString("\n    AS ASSIGNMENT")
	case "implicit":
		b.WriteString("\n    AS IMPLICIT")
	}
	b.WriteString(";")
	return b.String()
}

// -----------------------------------------------------------------------------
// Catalog (system schema) properties — reuses the same General query as a
// user schema, since a system schema is still just a pg_namespace row.
// -----------------------------------------------------------------------------

func (s *Server) handleCatalogProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}
	nspName := chi.URLParam(r, "catalogName")
	if nspName == "" {
		http.Error(w, "Invalid catalog name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(nspName); err == nil {
		nspName = unescaped
	}

	ctx := r.Context()
	gen, err := pgdb.New(pool).GetSchemaGeneral(ctx, nspName)
	if err != nil {
		propertiesGeneralErr(w, "catalog", "Catalog schema not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "catalog",
		KindLabel: "Catalog",
		Icon:      "📚",
		Qualified: quoteIfNeeded(nspName),
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: "-- System schema " + quoteIfNeeded(nspName) + " is built into every PostgreSQL database and is not user-creatable.",
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Event trigger properties
// -----------------------------------------------------------------------------

func (s *Server) handleEventTriggerProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}
	evtName := chi.URLParam(r, "evtName")
	if evtName == "" {
		http.Error(w, "Invalid event trigger name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(evtName); err == nil {
		evtName = unescaped
	}

	ctx := r.Context()
	gen, err := pgdb.New(pool).GetEventTriggerGeneral(ctx, evtName)
	if err != nil {
		propertiesGeneralErr(w, "event trigger", "Event trigger not found.", err)
		return
	}
	tags := getString(gen.Tags)

	vm := propertiesVM{
		Kind:      "event-trigger",
		KindLabel: "Event Trigger",
		Icon:      "⚡",
		Qualified: quoteIfNeeded(evtName),
		General: []propKV{
			{Label: "Event", Value: gen.Evtevent},
			{Label: "Enabled", Value: gen.Enabled},
			{Label: "Owner", Value: gen.Owner},
			{Label: "Function", Value: gen.FunctionName},
			{Label: "Tags", Value: emptyDash(tags)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: buildCreateEventTriggerScript(evtName, gen.Evtevent, gen.FunctionName, tags),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreateEventTriggerScript renders a pgAdmin-style CREATE EVENT TRIGGER script.
func buildCreateEventTriggerScript(name, event, functionName, tags string) string {
	var b strings.Builder
	b.WriteString("-- Event Trigger: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\nCREATE EVENT TRIGGER ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n    ON ")
	b.WriteString(event)
	if tags != "" {
		parts := strings.Split(tags, ", ")
		quoted := make([]string, len(parts))
		for i, p := range parts {
			quoted[i] = quoteLiteral(p)
		}
		b.WriteString("\n    WHEN TAG IN (")
		b.WriteString(strings.Join(quoted, ", "))
		b.WriteString(")")
	}
	b.WriteString("\n    EXECUTE FUNCTION ")
	b.WriteString(functionName)
	b.WriteString("();")
	return b.String()
}

// -----------------------------------------------------------------------------
// Foreign data wrapper properties
// -----------------------------------------------------------------------------

func (s *Server) handleForeignDataWrapperProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}
	fdwName := chi.URLParam(r, "fdwName")
	if fdwName == "" {
		http.Error(w, "Invalid foreign data wrapper name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(fdwName); err == nil {
		fdwName = unescaped
	}

	ctx := r.Context()
	gen, err := pgdb.New(pool).GetForeignDataWrapperGeneral(ctx, fdwName)
	if err != nil {
		propertiesGeneralErr(w, "foreign data wrapper", "Foreign data wrapper not found.", err)
		return
	}
	options := getString(gen.Options)

	vm := propertiesVM{
		Kind:      "foreign-data-wrapper",
		KindLabel: "Foreign Data Wrapper",
		Icon:      "🌐",
		Qualified: quoteIfNeeded(fdwName),
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Handler", Value: gen.Handler},
			{Label: "Validator", Value: gen.Validator},
			{Label: "Options", Value: emptyDash(options)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: buildCreateForeignDataWrapperScript(fdwName, gen.Handler, gen.Validator, options),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreateForeignDataWrapperScript renders a pgAdmin-style CREATE FOREIGN
// DATA WRAPPER script. A "-" handler/validator (Postgres's regproc rendering
// of OID 0) means none was set.
func buildCreateForeignDataWrapperScript(name, handler, validator, options string) string {
	var b strings.Builder
	b.WriteString("-- Foreign Data Wrapper: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\nCREATE FOREIGN DATA WRAPPER ")
	b.WriteString(quoteIfNeeded(name))
	if handler != "" && handler != "-" {
		b.WriteString("\n    HANDLER ")
		b.WriteString(handler)
	}
	if validator != "" && validator != "-" {
		b.WriteString("\n    VALIDATOR ")
		b.WriteString(validator)
	}
	if options != "" {
		b.WriteString("\n    OPTIONS (")
		b.WriteString(options)
		b.WriteString(")")
	}
	b.WriteString(";")
	return b.String()
}

// -----------------------------------------------------------------------------
// Language properties
// -----------------------------------------------------------------------------

func (s *Server) handleLanguageProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}
	lanName := chi.URLParam(r, "lanName")
	if lanName == "" {
		http.Error(w, "Invalid language name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(lanName); err == nil {
		lanName = unescaped
	}

	ctx := r.Context()
	gen, err := pgdb.New(pool).GetLanguageGeneral(ctx, lanName)
	if err != nil {
		propertiesGeneralErr(w, "language", "Language not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "language",
		KindLabel: "Language",
		Icon:      "🗣️",
		Qualified: quoteIfNeeded(lanName),
		General: []propKV{
			{Label: "Trusted", Value: yn(gen.Trusted)},
			{Label: "Owner", Value: gen.Owner},
			{Label: "Call handler", Value: gen.CallHandler},
			{Label: "Inline handler", Value: gen.InlineHandler},
			{Label: "Validator", Value: gen.Validator},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: buildCreateLanguageScript(lanName, gen.Trusted, gen.CallHandler, gen.InlineHandler, gen.Validator),
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// buildCreateLanguageScript renders a pgAdmin-style CREATE LANGUAGE script.
func buildCreateLanguageScript(name string, trusted bool, callHandler, inlineHandler, validator string) string {
	var b strings.Builder
	b.WriteString("-- Language: ")
	b.WriteString(quoteIfNeeded(name))
	b.WriteString("\n\nCREATE ")
	if trusted {
		b.WriteString("TRUSTED ")
	}
	b.WriteString("LANGUAGE ")
	b.WriteString(quoteIfNeeded(name))
	if callHandler != "" && callHandler != "-" {
		b.WriteString("\n    HANDLER ")
		b.WriteString(callHandler)
	}
	if inlineHandler != "" && inlineHandler != "-" {
		b.WriteString("\n    INLINE ")
		b.WriteString(inlineHandler)
	}
	if validator != "" && validator != "-" {
		b.WriteString("\n    VALIDATOR ")
		b.WriteString(validator)
	}
	b.WriteString(";")
	return b.String()
}

// -----------------------------------------------------------------------------
// Subscription properties
// -----------------------------------------------------------------------------

// handleSubscriptionProperties renders the read-only properties panel of a
// subscription. subconninfo is deliberately never queried (it can carry a
// plaintext password), matching the defensive stance the app already takes
// toward stored server passwords.
func (s *Server) handleSubscriptionProperties(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}
	subName := chi.URLParam(r, "subName")
	if subName == "" {
		http.Error(w, "Invalid subscription name", http.StatusBadRequest)
		return
	}
	if unescaped, err := url.PathUnescape(subName); err == nil {
		subName = unescaped
	}

	ctx := r.Context()
	gen, err := pgdb.New(pool).GetSubscriptionGeneral(ctx, subName)
	if err != nil {
		propertiesGeneralErr(w, "subscription", "Subscription not found.", err)
		return
	}

	vm := propertiesVM{
		Kind:      "subscription",
		KindLabel: "Subscription",
		Icon:      "📥",
		Qualified: quoteIfNeeded(subName),
		General: []propKV{
			{Label: "Owner", Value: gen.Owner},
			{Label: "Enabled", Value: yn(gen.Enabled)},
			{Label: "Publications", Value: gen.Publications},
			{Label: "Slot name", Value: emptyDash(gen.SlotName)},
			{Label: "Comment", Value: emptyDash(getString(gen.Comment))},
		},
		SQL: "-- Subscription: " + quoteIfNeeded(subName) +
			"\n\n-- Connection info is not shown here (may contain credentials).\nCREATE SUBSCRIPTION " +
			quoteIfNeeded(subName) + "\n    CONNECTION '...'\n    PUBLICATION " + gen.Publications + ";",
	}

	RenderPartial(w, "properties_panel.html", vm)
}

// -----------------------------------------------------------------------------
// Privileges for the ACL-bearing kinds that did not have a Privileges tab yet
// (database, tablespace, function, procedure). Role, extension and
// publication are deliberately not covered here: Postgres has no ACL/GRANT
// concept for those object kinds (pg_authid/pg_extension/pg_publication carry
// no *acl column), so there is nothing to explode.
// -----------------------------------------------------------------------------

func (s *Server) loadDatabasePrivileges(ctx context.Context, pool *pgxpool.Pool, dbName string) ([]propPrivilege, error) {
	const q = `SELECT COALESCE(r.rolname, 'PUBLIC') AS grantee,
    a.privilege_type,
    a.is_grantable
FROM pg_database d
JOIN LATERAL aclexplode(d.datacl) AS a ON true
LEFT JOIN pg_roles r ON r.oid = a.grantee
WHERE d.datname = $1
ORDER BY 1, 2`
	return queryPrivileges(ctx, pool, q, dbName)
}

func (s *Server) loadTablespacePrivileges(ctx context.Context, pool *pgxpool.Pool, tsName string) ([]propPrivilege, error) {
	const q = `SELECT COALESCE(r.rolname, 'PUBLIC') AS grantee,
    a.privilege_type,
    a.is_grantable
FROM pg_tablespace t
JOIN LATERAL aclexplode(t.spcacl) AS a ON true
LEFT JOIN pg_roles r ON r.oid = a.grantee
WHERE t.spcname = $1
ORDER BY 1, 2`
	return queryPrivileges(ctx, pool, q, tsName)
}

// loadFunctionPrivileges covers both functions and procedures: both live in
// pg_proc and are addressed by the same "name(identity_arguments)" form used
// throughout properties.go (see GetFunctionGeneral).
func (s *Server) loadFunctionPrivileges(ctx context.Context, pool *pgxpool.Pool, schemaName, identity string) ([]propPrivilege, error) {
	const q = `SELECT COALESCE(r.rolname, 'PUBLIC') AS grantee,
    a.privilege_type,
    a.is_grantable
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
JOIN LATERAL aclexplode(p.proacl) AS a ON true
LEFT JOIN pg_roles r ON r.oid = a.grantee
WHERE n.nspname = $1 AND (p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')') = $2
ORDER BY 1, 2`
	return queryPrivileges(ctx, pool, q, schemaName, identity)
}

// queryPrivileges runs a raw aclexplode-based privilege query (aclexplode is
// a C function sqlc cannot type, same reasoning as loadSchemaPrivileges) and
// scans the standard (grantee, privilege_type, is_grantable) row shape.
func queryPrivileges(ctx context.Context, pool *pgxpool.Pool, q string, args ...any) ([]propPrivilege, error) {
	rows, err := pool.Query(ctx, q, args...)
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
