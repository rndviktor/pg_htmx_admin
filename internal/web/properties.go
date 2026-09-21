package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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
// which in-panel section tabs are rendered ("table" or "view"). Errored
// sections simply stay empty, matching how tree folders degrade gracefully.
type propertiesVM struct {
	Kind         string
	KindLabel    string
	Icon         string
	Qualified    string
	General      []propKV
	Columns      []propColumn
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
		if errors.Is(err, pgx.ErrNoRows) {
			RenderPartial(w, "properties_panel.html", propertiesVM{Kind: "table", Err: "Table not found."})
			return
		}
		log.Printf("Failed to load table general properties: %v", err)
		http.Error(w, "Failed to load table properties", http.StatusInternalServerError)
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
		if errors.Is(err, pgx.ErrNoRows) {
			RenderPartial(w, "properties_panel.html", propertiesVM{Kind: "view", Err: "View not found."})
			return
		}
		log.Printf("Failed to load view general properties: %v", err)
		http.Error(w, "Failed to load view properties", http.StatusInternalServerError)
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
