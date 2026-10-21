package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBuildGrantWizard(t *testing.T) {
	form := func(kv ...string) url.Values {
		f := url.Values{"schema": {"app"}}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Add(kv[i], kv[i+1])
		}
		return f
	}
	tests := []struct {
		name string
		form url.Values
		want []string
		not  []string
		err  string
	}{
		{"objects and two grantees", form("grantee", "alice", "grantee", "PUBLIC", "priv", "SELECT", "priv", "UPDATE",
			"obj_table", "orders", "obj_table", "items", "obj_sequence", "orders_id_seq"),
			[]string{`GRANT SELECT, UPDATE ON TABLE app.orders, app.items TO "alice", PUBLIC;`,
				`GRANT SELECT, UPDATE ON SEQUENCE app.orders_id_seq TO "alice", PUBLIC;`}, nil, ""},
		{"all tables covers views", form("grantee", "bob", "priv", "SELECT", "all_table", "on", "obj_view", "v1"),
			[]string{`GRANT SELECT ON ALL TABLES IN SCHEMA "app" TO "bob";`}, []string{"VIEW", "v1"}, ""},
		{"privilege intersection", form("grantee", "bob", "priv", "SELECT", "priv", "EXECUTE", "obj_function", "f(integer)", "all_sequence", "on"),
			[]string{`GRANT EXECUTE ON FUNCTION app.f(integer) TO "bob";`, `GRANT SELECT ON ALL SEQUENCES IN SCHEMA "app" TO "bob";`},
			[]string{"SELECT ON FUNCTION", "EXECUTE ON ALL"}, ""},
		{"revoke cascade", form("mode", "revoke", "cascade", "on", "grantee", "bob", "priv", "INSERT", "obj_table", "orders"),
			[]string{`REVOKE INSERT ON TABLE app.orders FROM "bob" CASCADE;`}, nil, ""},
		{"grant option", form("grantopt", "on", "grantee", "bob", "priv", "SELECT", "obj_table", "orders"),
			[]string{`TO "bob" WITH GRANT OPTION;`}, nil, ""},
		{"nothing selected", form("grantee", "bob", "priv", "SELECT"), nil, nil, "No changes requested"},
		{"no grantee", form("priv", "SELECT", "obj_table", "orders"), nil, nil, "No changes requested"},
		{"injected signature", form("grantee", "bob", "priv", "EXECUTE", "obj_function", "f(); DROP TABLE x"), nil, nil, "Invalid routine"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildGrantWizard(tc.form)
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
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("unexpected %q in:\n%s", n, got)
				}
			}
		})
	}
}

func TestGrantWizardRenders(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	w := httptest.NewRecorder()
	RenderPartial(w, "grant_wizard_panel.html", grantWizardData{
		PanelID: "p1", Action: "grant", PreviewURL: "/api/grant/preview", ObjectsURL: "/api/grant/objects?x=1",
		Schema: "app", Grantees: []string{"PUBLIC", "bob"}, Privs: wizardPrivs(),
	})
	body := w.Body.String()
	for _, want := range []string{`id="grant-form-p1"`, `hx-post="/api/grant/preview"`, `id="grant-objs-p1"`,
		`name="priv" value="EXECUTE"`, `<option value="bob">`, `change from:#grant-objs-p1`} {
		if !strings.Contains(body, want) {
			t.Errorf("panel: missing %q in\n%s", want, body)
		}
	}

	w = httptest.NewRecorder()
	RenderPartial(w, "grant_wizard_objects.html", grantObjectsData{PanelID: "p1", Groups: []grantGroup{
		{grantType: grantTypes[0], Objects: []string{"orders"}},
		{grantType: grantTypes[1]},
	}})
	body = w.Body.String()
	for _, want := range []string{`form="grant-form-p1" name="all_table"`, `name="obj_table" value="orders"`, "None in this schema."} {
		if !strings.Contains(body, want) {
			t.Errorf("objects: missing %q in\n%s", want, body)
		}
	}
}
