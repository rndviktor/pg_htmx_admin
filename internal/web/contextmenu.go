package web

import (
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// The tree's right-click menu is built here and fetched as an HTML fragment
// (static/js/context-menu.js only positions it and dispatches the clicks that
// need script). Items carry either hx-* attributes (modals, POSTs) or a
// data-act that the dispatcher maps to a window function.

type ctxAttr struct{ Name, Value string }

// HTML renders the attribute; html/template does not accept dynamic attribute
// names, so the pair is escaped here and passed through as one HTMLAttr.
func (a ctxAttr) HTML() template.HTMLAttr {
	return template.HTMLAttr(a.Name + `="` + html.EscapeString(a.Value) + `"`)
}

type ctxItem struct {
	Label   string
	Danger  bool
	Divider bool
	Attrs   []ctxAttr
	Sub     []ctxItem // non-empty: a hover submenu
}

type ctxMenuData struct {
	NodeURL, TableURL, PropsURL string
	Items                       []ctxItem
}

// ctxNode is what the client knows about the right-clicked tree node.
type ctxNode struct {
	kind, tableURL, propsURL, dataURL, name, state string
	hasChildren                                    bool
}

func (n ctxNode) nodeURL() string {
	for _, u := range []string{n.tableURL, n.propsURL, n.dataURL} {
		if u != "" {
			return u
		}
	}
	return ""
}

// treeCtx mirrors parseObjectContext (app.js): the server, database, schema
// and table encoded in a tree URL.
func treeCtx(u string) (server, db, schema, table string) {
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/properties"), "/children")
	parts := strings.Split(u, "/")
	at := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	server, db = at(3), at(5)
	for i, p := range parts {
		if p == "schemas" && schema == "" {
			schema = at(i + 1)
		}
		if p == "tables" && table == "" {
			table = at(i + 1)
		}
	}
	return
}

func (n ctxNode) objectName() string {
	if n.name != "" {
		return n.name
	}
	u := strings.TrimSuffix(strings.TrimSuffix(n.nodeURL(), "/properties"), "/children")
	last := u[strings.LastIndex(u, "/")+1:]
	if name, err := url.PathUnescape(last); err == nil {
		return name
	}
	return last
}

func act(label, act string, kv ...string) ctxItem {
	attrs := []ctxAttr{{"data-act", act}}
	for i := 0; i+1 < len(kv); i += 2 {
		attrs = append(attrs, ctxAttr{kv[i], kv[i+1]})
	}
	return ctxItem{Label: label, Attrs: attrs}
}

func divider() ctxItem { return ctxItem{Divider: true} }

var ctxCreateLabels = map[string]string{
	"schema": "Schema", "table": "Table", "sequence": "Sequence", "view": "View",
	"matview": "Materialized View", "function": "Function", "procedure": "Procedure",
	"extension": "Extension", "publication": "Publication",
	"index": "Index", "trigger": "Trigger", "rule": "Rule", "policy": "RLS Policy",
}

// ctxObjectLabels names the drop / alter target of each node kind.
var ctxObjectLabels = map[string]string{
	"database": "Database", "role": "Role", "tablespace": "Tablespace", "schema": "Schema",
	"table": "Table", "view": "View", "materialized-view": "Materialized View",
	"sequence": "Sequence", "function": "Function", "procedure": "Procedure",
	"extension": "Extension", "publication": "Publication", "index": "Index",
	"trigger": "Trigger", "rule": "Rule", "rls-policy": "RLS Policy",
}

// Node kinds whose DDL route kind differs from the tree-menu kind.
var ctxDDLKinds = map[string]string{"materialized-view": "matview", "rls-policy": "policy"}

func (n ctxNode) connectionItems() []ctxItem {
	server, db, _, _ := treeCtx(n.tableURL)
	var disconnect string
	switch n.kind {
	case "server":
		disconnect = "/api/servers/" + server + "/disconnect"
	case "database":
		server, db, _, _ = treeCtx(n.nodeURL())
		disconnect = "/api/servers/" + server + "/databases/" + db + "/disconnect"
	}
	if n.state == "on" {
		label := "Disconnect"
		if n.kind == "server" {
			label = "Disconnect from server"
		}
		it := act(label, "disconnect", "data-url", disconnect)
		it.Danger = true
		return []ctxItem{it}
	}
	label := "Connect"
	if n.state == "off" {
		label = "Try to reconnect"
	}
	return []ctxItem{act(label, "refresh")}
}

func (n ctxNode) buildMenu() []ctxItem {
	var items []ctxItem
	add := func(group ...ctxItem) {
		if len(group) > 0 {
			items = append(items, divider())
			items = append(items, group...)
		}
	}
	if n.hasChildren {
		items = append(items, act("Refresh", "refresh"))
	}
	items = append(items, act("Query Tool", "query-tool"))

	server, db, schema, table := treeCtx(n.nodeURL())
	if n.kind == "server" {
		add(n.connectionItems()...)
		prefix := "/api/servers/" + server
		add(
			ctxItem{Label: "Reload Configuration", Attrs: []ctxAttr{
				{"hx-post", prefix + "/reload-config"}, {"hx-confirm", "Reload configuration on this server?"}, {"hx-swap", "none"}}},
			ctxItem{Label: "Create Restore Point", Attrs: []ctxAttr{
				{"hx-post", prefix + "/restore-point"}, {"hx-prompt", "Restore point name:"}, {"hx-swap", "none"}}},
		)
		add(ctxItem{Label: "Create", Sub: []ctxItem{
			act("Database", "panel", "data-action", "create", "data-kind", "database"),
			act("Role", "panel", "data-action", "create", "data-kind", "role"),
			act("Tablespace", "panel", "data-action", "create", "data-kind", "tablespace"),
		}})
	}
	if n.kind == "database" {
		add(n.connectionItems()...)
	}
	if kind, ok := strings.CutPrefix(n.kind, "create-"); ok {
		if label := ctxCreateLabels[kind]; label != "" {
			add(act("Create "+label, "panel", "data-action", "create", "data-kind", kind, "data-folder", "create"))
		}
	}

	if label, ok := ctxObjectLabels[n.kind]; ok {
		kind := n.kind
		if k, ok := ctxDDLKinds[kind]; ok {
			kind = k
		}
		name := n.objectName()
		drop := act("Drop "+label, "panel", "data-action", "drop", "data-kind", kind, "data-name", name, "data-folder", "node")
		drop.Danger = true
		add(drop, act("DROP Script", "drop-script", "data-kind", kind, "data-name", name))
		add(act("Alter "+label, "panel", "data-action", "alter", "data-kind", kind, "data-name", name, "data-folder", "node"))
	}

	backup := func(label, op string) ctxItem {
		q := url.Values{"server_id": {server}, "db": {db}, "schema": {schema}, "table": {table}}
		return ctxItem{Label: label, Attrs: []ctxAttr{
			{"hx-get", "/api/backup/" + op + "/modal?" + q.Encode()}, {"hx-target", "#modal-container"}}}
	}
	maint := func(label, op, extra string) ctxItem {
		return act(label, "maint", "data-op", op, "data-extra", extra)
	}
	modal := func(label, path string) ctxItem {
		return ctxItem{Label: label, Attrs: []ctxAttr{{"hx-get", path}, {"hx-target", "#modal-container"}}}
	}
	switch n.kind {
	case "server":
		add(ctxItem{Label: "Backup / Restore", Sub: []ctxItem{
			backup("Backup Globals...", "backup-globals"),
			modal("Storage Manager", "/api/storage"),
			modal("Background Jobs", "/api/backup/jobs"),
		}})
	case "schema":
		add(ctxItem{Label: "Backup / Restore", Sub: []ctxItem{backup("Backup...", "backup")}},
			maint("Reindex Schema", "reindex", "reindex_target=schema"))
	case "table":
		add(ctxItem{Label: "Backup / Restore", Sub: []ctxItem{backup("Backup...", "backup")}},
			ctxItem{Label: "Maintenance", Sub: []ctxItem{
				maint("Vacuum", "vacuum", "target=table"),
				maint("Analyze", "analyze", "target=table"),
				maint("Cluster", "cluster", ""),
				maint("Reindex Table", "reindex", "reindex_target=table"),
			}})
	case "database":
		add(ctxItem{Label: "Backup / Restore", Sub: []ctxItem{
			backup("Backup...", "backup"), backup("Restore...", "restore"),
		}}, ctxItem{Label: "Maintenance", Sub: []ctxItem{
			maint("Vacuum", "vacuum", "target=database"),
			maint("Analyze", "analyze", "target=database"),
			maint("Reindex Database", "reindex", "reindex_target=database"),
		}})
	case "index":
		add(maint("Reindex Index", "reindex", "reindex_target=index&name="+url.QueryEscape(n.objectName())))
	}

	// Tables and views derive /properties from their children URL; other
	// leaves carry it directly.
	props := n.propsURL
	if n.kind == "table" || n.kind == "view" {
		props = ""
		if n.tableURL != "" {
			props = strings.TrimSuffix(n.tableURL, "/children") + "/properties"
		}
	}
	if props != "" {
		add(act("Properties", "properties", "data-url", props, "data-kind", n.kind))
	}

	script := func(label, base string) ctxItem { return act(label, "script", "data-label", label, "data-url", base) }
	switch n.kind {
	case "table":
		var sub []ctxItem
		for _, l := range []string{"CREATE Script", "DELETE Script", "INSERT Script", "SELECT Script", "UPDATE Script"} {
			sub = append(sub, script(l, n.tableURL))
		}
		add(ctxItem{Label: "Scripts", Sub: sub})
	case "view":
		var sub []ctxItem
		for _, l := range []string{"CREATE Script", "INSERT Script", "SELECT Script"} {
			sub = append(sub, script(l, n.tableURL))
		}
		add(ctxItem{Label: "Scripts", Sub: sub})
	case "materialized-view":
		add(ctxItem{Label: "Scripts", Sub: []ctxItem{
			script("SELECT Script", strings.TrimSuffix(n.propsURL, "/properties"))}})
	}
	return items
}

func (s *Server) handleContextMenu(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n := ctxNode{
		kind: q.Get("kind"), tableURL: q.Get("table_url"), propsURL: q.Get("props_url"),
		dataURL: q.Get("data_url"), name: q.Get("name"), state: q.Get("state"),
		hasChildren: q.Get("has_children") == "1",
	}
	RenderPartial(w, "context_menu.html", ctxMenuData{
		NodeURL: n.nodeURL(), TableURL: n.tableURL, PropsURL: n.propsURL, Items: n.buildMenu(),
	})
}
