package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContextMenuRenders(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	s := &Server{}
	const tbl = "/api/servers/3/databases/shop/schemas/public/tables/orders/children"
	cases := []struct {
		query string
		want  []string
		not   []string
	}{
		{"kind=table&has_children=1&table_url=" + tbl + "&name=orders",
			[]string{`>Refresh<`, `data-act="panel"`, `data-action="alter"`, `data-kind="table"`, `data-name="orders"`,
				`hx-get="/api/backup/backup/modal?db=shop&amp;schema=public&amp;server_id=3&amp;table=orders"`,
				`data-act="maint"`, `data-extra="target=table"`, `Scripts`, `data-label="UPDATE Script"`,
				`data-url="/api/servers/3/databases/shop/schemas/public/tables/orders/properties"`}, nil},
		{"kind=server&state=on&table_url=/api/servers/3/children",
			[]string{`Disconnect from server`, `data-url="/api/servers/3/disconnect"`, `hx-post="/api/servers/3/restore-point"`,
				`hx-prompt="Restore point name:"`, `hx-confirm="Reload configuration on this server?"`, `data-kind="tablespace"`,
				`hx-get="/api/storage"`}, []string{`>Refresh<`}},
		{"kind=create-index&has_children=1&table_url=" + tbl,
			[]string{`Create Index`, `data-folder="create"`}, nil},
		{"kind=materialized-view&props_url=/api/servers/3/databases/shop/schemas/public/matviews/mv/properties",
			[]string{`data-kind="matview"`, `data-name="mv"`, `SELECT Script`}, []string{`Maintenance`}},
		{"kind=table&table_url=" + tbl + "&name=orders",
			[]string{`>Privileges...<`, `data-act="priv"`}, nil},
		{"kind=table&table_url=" + tbl + "&name=orders",
			[]string{`>Import / Export<`, `hx-get="/api/copy/import/modal?db=shop&amp;schema=public&amp;server_id=3&amp;table=orders"`,
				`hx-get="/api/copy/export/modal?`}, nil},
		{"kind=view&props_url=/api/servers/3/databases/shop/schemas/public/views/v/properties", nil, []string{`Import / Export`}},
		{"kind=schema&has_children=1&table_url=/api/servers/3/databases/shop/schemas/public/children",
			[]string{`>Grant Wizard...<`, `data-act="grantwiz"`, `data-name="public"`}, nil},
		{"kind=table&table_url=" + tbl + "&name=orders", nil, []string{`Grant Wizard`}},
		{"kind=rule&props_url=/api/servers/3/databases/shop/schemas/public/tables/orders/rules/r/properties",
			nil, []string{`Privileges...`}},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		s.handleContextMenu(w, httptest.NewRequest("GET", "/api/context-menu?"+c.query, nil))
		body := w.Body.String()
		if w.Code != 200 {
			t.Fatalf("%s: status %d: %s", c.query, w.Code, body)
		}
		for _, want := range c.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q in\n%s", c.query, want, body)
			}
		}
		for _, bad := range c.not {
			if strings.Contains(body, bad) {
				t.Errorf("%s: unexpected %q", c.query, bad)
			}
		}
	}
}
