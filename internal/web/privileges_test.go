package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBuildPrivileges(t *testing.T) {
	base := func(kv ...string) url.Values {
		f := url.Values{"kind": {"table"}, "schema": {"public"}, "name": {"orders"}, "rows": {"1"},
			"grantee_0": {"alice"}, "orig_0": {"SELECT*,INSERT"}}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Add(kv[i], kv[i+1])
		}
		return f
	}
	tests := []struct {
		name string
		form url.Values
		want []string
		err  string
	}{
		{"unchanged", base("priv_0", "SELECT", "opt_0", "SELECT", "priv_0", "INSERT"), nil, "No changes requested"},
		{"revoke insert", base("priv_0", "SELECT", "opt_0", "SELECT"),
			[]string{`REVOKE INSERT ON TABLE public.orders FROM "alice";`}, ""},
		{"drop grant option", base("priv_0", "SELECT", "priv_0", "INSERT"),
			[]string{`REVOKE GRANT OPTION FOR SELECT ON TABLE public.orders FROM "alice";`}, ""},
		{"add grant option", base("priv_0", "SELECT", "priv_0", "INSERT", "opt_0", "SELECT", "opt_0", "INSERT"),
			[]string{`GRANT INSERT ON TABLE public.orders TO "alice" WITH GRANT OPTION;`}, ""},
		{"new grantee", base("priv_0", "SELECT", "opt_0", "SELECT", "priv_0", "INSERT",
			"newgrantee", "PUBLIC", "priv_new", "SELECT", "priv_new", "DELETE"),
			[]string{`GRANT SELECT, DELETE ON TABLE public.orders TO PUBLIC;`}, ""},
		{"unknown privilege ignored", base("priv_0", "SELECT", "opt_0", "SELECT", "priv_0", "INSERT",
			"newgrantee", "bob", "priv_new", "DROP; --"), nil, "No changes requested"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildPrivileges(tc.form)
			if tc.err != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tc.err) {
					t.Fatalf("want error %q, got %q / %v", tc.err, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestPrivTarget(t *testing.T) {
	tests := []struct {
		kind, schema, name, want string
		bad                      bool
	}{
		{"database", "", "shop", `DATABASE "shop"`, false},
		{"schema", "", "app", `SCHEMA "app"`, false},
		{"function", "public", "add(integer, integer)", `FUNCTION public.add(integer, integer)`, false},
		{"function", "public", "f(); DROP TABLE x", "", true},
		{"function", "public", "f(a'b)", "", true},
		{"table", "", "t", "", true},
	}
	for _, tc := range tests {
		got, err := privTarget(privKinds[tc.kind], tc.schema, tc.name)
		if tc.bad != (err != nil) || got != tc.want {
			t.Errorf("%s %q: got %q, %v", tc.kind, tc.name, got, err)
		}
	}
}

func TestPrivPanelRenders(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	pk := privKinds["table"]
	cells, orig := completeCells(pk, []privCell{{Priv: "SELECT", Has: true, Opt: true}})
	w := httptest.NewRecorder()
	RenderPartial(w, "priv_panel.html", privPanelData{
		PanelID: "p1", Kind: "table", Action: "priv", PreviewURL: "/api/priv/table/preview",
		Name: "orders", Schema: "public", ConnDB: "shop", NewCells: emptyCells(pk),
		Rows:     []privRow{{Index: 0, Grantee: "alice", Orig: orig, Cells: cells}},
		Grantees: []string{"PUBLIC", "bob"},
	})
	body := w.Body.String()
	for _, want := range []string{`id="ddl-panel-p1"`, `hx-post="/api/priv/table/preview"`, `name="orig_0" value="SELECT*"`,
		`name="priv_0" value="SELECT" checked`, `name="rows" value="1"`, `<option value="bob">`, `name="priv_new" value="TRIGGER"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
}
