package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"htmx-golang-excercise/internal/db"
	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
	sqlite "htmx-golang-excercise/internal/sqlc/sqlite/db"
)

// treeNode is one row of the object explorer tree. Nodes with a URL are
// lazy-loaded expanders whose children are fetched from that endpoint;
// nodes without a URL render as plain leaves.
type treeNode struct {
	ID    string // DOM id of the children container (expanders only)
	Icon  string
	Label string
	Badge string // optional counter shown next to the label
	Sub   string // optional secondary line under the label
	URL   string // htmx GET url for lazy children; empty means leaf node
	Menu  string // context menu kind for right click (e.g. "table"); empty = none
	// DataName is the object name exposed as data-name on the node, so the
	// context menu can act on it without parsing the label (e.g. Drop for
	// roles/tablespaces leaves and the database expander).
	DataName string
	// Disabled marks a node that has nothing beneath it (e.g. an empty
	// category folder). It renders grayed out and cannot be expanded.
	Disabled bool
	// State is the live connection status of a server node: "on" shows a
	// green dot, "off" a red dot, and any other value (or empty) shows none.
	State string
}

// expander builds a lazy-loadable tree node whose children are fetched from
// url into the container named id.
func expander(id, icon, label, url string) treeNode {
	return treeNode{ID: id, Icon: icon, Label: label, URL: url}
}

// leaves converts plain item names into non-expandable tree nodes.
func leaves(icon string, names []string) []treeNode {
	nodes := make([]treeNode, 0, len(names))
	for _, name := range names {
		nodes = append(nodes, treeNode{Icon: icon, Label: name})
	}
	return nodes
}

// menuLeaves is leaves but every row also carries a right-click kind and its
// own object name, so object leaves such as roles and tablespaces expose a
// context menu (Drop, ...) without a URL to derive the name from.
func menuLeaves(icon string, names []string, kind string) []treeNode {
	nodes := make([]treeNode, 0, len(names))
	for _, name := range names {
		nodes = append(nodes, treeNode{Icon: icon, Label: name, Menu: kind, DataName: name})
	}
	return nodes
}

// categoryFolders builds the expander nodes for the category folders under a
// parent node. prefix is the DOM id prefix and baseURL the route prefix of
// the parent, e.g. "database-1-postgres" and "/api/servers/1/databases/postgres".
func categoryFolders(prefix, baseURL string, cats []category) []treeNode {
	nodes := make([]treeNode, 0, len(cats))
	for _, c := range cats {
		nodes = append(nodes, expander(
			prefix+"-"+c.Slug,
			c.Icon,
			c.Label,
			baseURL+"/"+c.Slug,
		))
	}
	return nodes
}

// categoryFoldersWithCounts is categoryFolders but tags each expander with a
// badge showing the live item count for that category. categories with a
// count of zero render as grayed-out, non-expanding leaves instead of
// expanders; categories missing from the map (unknown count) render as plain
// expanders without a badge. counts is keyed by category slug.
func categoryFoldersWithCounts(prefix, baseURL string, cats []category, counts map[string]int64) []treeNode {
	nodes := make([]treeNode, 0, len(cats))
	for _, c := range cats {
		cnt, counted := counts[c.Slug]
		if counted && cnt == 0 {
			nodes = append(nodes, treeNode{Icon: c.Icon, Label: c.Label, Disabled: true})
			continue
		}
		n := expander(prefix+"-"+c.Slug, c.Icon, c.Label, baseURL+"/"+c.Slug)
		if counted && cnt > 0 {
			n.Badge = strconv.FormatInt(cnt, 10)
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// renderTree responds with the single tree fragment shared by all tree endpoints.
func renderTree(w http.ResponseWriter, nodes []treeNode, emptyMessage string) {
	RenderPartial(w, "tree_nodes.html", map[string]any{
		"Nodes":        nodes,
		"EmptyMessage": emptyMessage,
	})
}

// listServers fetches the registered servers of the default user group.
// On failure it writes the error response itself and returns ok=false.
func (s *Server) listServers(w http.ResponseWriter, r *http.Request) ([]sqlite.ListServersByGroupRow, bool) {
	servers, err := s.DB.ListServersByGroup(r.Context(), db.DefaultUserID)
	if err != nil {
		log.Printf("Failed to list servers: %v", err)
		http.Error(w, "Failed to load servers", http.StatusInternalServerError)
		return nil, false
	}
	return servers, true
}

// category describes one expandable folder in the tree together with the
// live sqlc query used to list its contents. ListNames runs the named query
// against pool and returns the item labels as strings. Args holds the
// positional arguments required by the query ordering: database categories
// take none, schema categories take the schema name as arg 0, and table
// categories take the schema name as arg 0 and the table name as arg 1.
type category struct {
	Slug      string
	Label     string
	Icon      string
	Empty     string
	NumArgs   int
	ListNames func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error)
}

// q builds a sqlc Queries over pool and converts its results (which may be
// []interface{} for expression-based selects) into []string.
func q(pool *pgxpool.Pool) *pgdb.Queries {
	return pgdb.New(pool)
}

func toNames[T any](items []T, fmtFn func(T) string) []string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, fmtFn(it))
	}
	return names
}

func getString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

var dbCategories = []category{
	{Slug: "casts", Label: "Casts", Icon: "🔁", Empty: "No casts found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			items, err := q(pool).ListCasts(ctx)
			return toNames(items, getString), err
		}},
	{Slug: "catalogs", Label: "Catalogs", Icon: "📚", Empty: "No catalogs found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			return q(pool).ListCatalogs(ctx)
		}},
	{Slug: "event-triggers", Label: "Event Triggers", Icon: "⚡", Empty: "No event triggers found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			return q(pool).ListEventTriggers(ctx)
		}},
	{Slug: "extensions", Label: "Extensions", Icon: "🧩", Empty: "No extensions found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			return q(pool).ListExtensions(ctx)
		}},
	{Slug: "foreign-data-wrappers", Label: "Foreign Data Wrappers", Icon: "🌐", Empty: "No foreign data wrappers found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			return q(pool).ListForeignDataWrappers(ctx)
		}},
	{Slug: "languages", Label: "Languages", Icon: "🗣️", Empty: "No languages found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			return q(pool).ListLanguages(ctx)
		}},
	{Slug: "publications", Label: "Publications", Icon: "📢", Empty: "No publications found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			return q(pool).ListPublications(ctx)
		}},
	{Slug: "schemas", Label: "Schemas", Icon: "📁", Empty: "No schemas found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			return q(pool).ListSchemas(ctx)
		}},
	{Slug: "subscriptions", Label: "Subscriptions", Icon: "📥", Empty: "No subscriptions found.", NumArgs: 0,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, _ ...string) ([]string, error) {
			return q(pool).ListSubscriptions(ctx)
		}},
}

var schemaCategories = []category{
	{Slug: "tables", Label: "Tables", Icon: "📋", Empty: "No tables found.", NumArgs: 1,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListTables(ctx, args[0])
		}},
	{Slug: "views", Label: "Views", Icon: "🔭", Empty: "No views found.", NumArgs: 1,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListViews(ctx, args[0])
		}},
	{Slug: "materialized-views", Label: "Materialized Views", Icon: "🧊", Empty: "No materialized views found.", NumArgs: 1,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListMaterializedViews(ctx, args[0])
		}},
	{Slug: "sequences", Label: "Sequences", Icon: "🔢", Empty: "No sequences found.", NumArgs: 1,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListSequences(ctx, args[0])
		}},
	{Slug: "functions", Label: "Functions", Icon: "⚙️", Empty: "No functions found.", NumArgs: 1,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			items, err := q(pool).ListFunctions(ctx, args[0])
			return toNames(items, getString), err
		}},
	{Slug: "procedures", Label: "Procedures", Icon: "🛠️", Empty: "No procedures found.", NumArgs: 1,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			items, err := q(pool).ListProcedures(ctx, args[0])
			return toNames(items, getString), err
		}},
	{Slug: "types", Label: "Types", Icon: "🏷️", Empty: "No types found.", NumArgs: 1,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListTypes(ctx, args[0])
		}},
	{Slug: "domains", Label: "Domains", Icon: "📐", Empty: "No domains found.", NumArgs: 1,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListDomains(ctx, args[0])
		}},
}

// tableCategories lists the object folders shown when a table node is
// expanded. Every query takes the schema name as arg 0 and the table name as
// arg 1.
var tableCategories = []category{
	{Slug: "columns", Label: "Columns", Icon: "📊", Empty: "No columns found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			items, err := q(pool).ListTableColumns(ctx, pgdb.ListTableColumnsParams{TableSchema: args[0], TableName: args[1]})
			return toNames(items, getString), err
		}},
	{Slug: "constraints", Label: "Constraints", Icon: "🔒", Empty: "No constraints found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			items, err := q(pool).ListConstraints(ctx, pgdb.ListConstraintsParams{Nspname: args[0], Relname: args[1]})
			return toNames(items, getString), err
		}},
	{Slug: "indexes", Label: "Indexes", Icon: "📇", Empty: "No indexes found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListTableIndexes(ctx, pgdb.ListTableIndexesParams{Nspname: args[0], Relname: args[1]})
		}},
	{Slug: "rls-policies", Label: "RLS Policies", Icon: "🛡️", Empty: "No RLS policies found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListPolicies(ctx, pgdb.ListPoliciesParams{Schemaname: args[0], Tablename: args[1]})
		}},
	{Slug: "rules", Label: "Rules", Icon: "📜", Empty: "No rules found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListTableRules(ctx, pgdb.ListTableRulesParams{Schemaname: args[0], Tablename: args[1]})
		}},
	{Slug: "triggers", Label: "Triggers", Icon: "💥", Empty: "No triggers found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListTableTriggers(ctx, pgdb.ListTableTriggersParams{Nspname: args[0], Relname: args[1]})
		}},
}

func findCategory(cats []category, slug string) *category {
	for i := range cats {
		if cats[i].Slug == slug {
			return &cats[i]
		}
	}
	return nil
}

// viewCategories lists the object folders shown when a view node is expanded.
// A view exposes columns (information_schema reports view columns too), rules
// and triggers (pg_rules and pg_trigger cover views as well). Every query
// takes the schema name as arg 0 and the view name as arg 1.
var viewCategories = []category{
	{Slug: "columns", Label: "Columns", Icon: "📊", Empty: "No columns found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			items, err := q(pool).ListTableColumns(ctx, pgdb.ListTableColumnsParams{TableSchema: args[0], TableName: args[1]})
			return toNames(items, getString), err
		}},
	{Slug: "rules", Label: "Rules", Icon: "📜", Empty: "No rules found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListTableRules(ctx, pgdb.ListTableRulesParams{Schemaname: args[0], Tablename: args[1]})
		}},
	{Slug: "triggers", Label: "Triggers", Icon: "💥", Empty: "No triggers found.", NumArgs: 2,
		ListNames: func(ctx context.Context, pool *pgxpool.Pool, args ...string) ([]string, error) {
			return q(pool).ListTableTriggers(ctx, pgdb.ListTableTriggersParams{Nspname: args[0], Relname: args[1]})
		}},
}
