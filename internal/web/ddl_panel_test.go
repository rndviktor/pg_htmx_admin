package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Renders every DDL side panel (create / alter / drop per kind, plus the
// maintenance ops) so a template error or a leaked global id shows up here
// rather than in the browser.
func TestAllPanelsRender(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	s := &Server{}
	r := httptest.NewRequest("GET", "/api/ddl/x/panel", nil)
	data := func(kind, action, id string) ddlModalData {
		return ddlModalData{
			Partial: "ddl_" + kind + "_panel.html", Action: action, Kind: kind, ServerID: 1, DB: "db",
			Schema: "public", Table: "t", Name: "obj", PanelID: id,
			Values:          map[string]string{"owner": "bob"},
			Dropdowns:       map[string][]string{"roles": {"bob"}, "schemas": {"public"}, "ref_tables": {"public.o"}},
			ExistingColumns: []existingColumn{{OrigName: "id", Type: "integer", TypeOrig: "integer", Nullable: "NO", NullableOrig: "NO"}},
		}
	}
	for kind, k := range ddlKinds {
		actions := map[string]string{"create": "ddl_" + kind + "_panel.html", "drop": "ddl_drop_panel.html"}
		if k.hasAlter() {
			actions["alter"] = "ddl_alter_panel.html"
		}
		for action, partial := range actions {
			m := data(kind, action, "p1")
			m.Partial = partial
			w := httptest.NewRecorder()
			s.renderDDLPanel(w, r, m)
			body := w.Body.String()
			if w.Code != 200 || !strings.Contains(body, `id="ddl-panel-p1"`) {
				t.Errorf("%s %s: status %d, body %.200q", kind, action, w.Code, body)
				continue
			}
			for _, bad := range []string{`id="ddl-columns"`, `id="fk-ref-cols"`, `id="ddl-col-types"`, `id="ddl-add-columns"`, `id="ddl-preview"`, "closeDDL"} {
				if strings.Contains(body, bad) {
					t.Errorf("%s %s: panel still contains %s", kind, action, bad)
				}
			}
			if !strings.Contains(body, `hx-post="/api/ddl/`+kind+`/preview"`) {
				t.Errorf("%s %s: form does not post to the preview endpoint", kind, action)
			}
			if strings.Contains(body, "<template") || strings.Contains(body, "onclick=") {
				t.Errorf("%s %s: panel still carries script (template/onclick)", kind, action)
			}
		}
	}

	for op := range maintOps {
		w := httptest.NewRecorder()
		s.renderMaintPanel(w, maintModalData{Op: op, ServerID: 1, DB: "db", Schema: "public", Table: "t",
			ReindexTarget: "table", PanelID: "p2", Values: map[string]string{"target": "table"}, Indexes: []string{"i"}})
		if w.Code != 200 || !strings.Contains(w.Body.String(), `hx-post="/api/maint/`+op+`/preview"`) {
			t.Errorf("maint %s: status %d, body %.200q", op, w.Code, w.Body.String())
		}
	}
}

func TestPanelIDPattern(t *testing.T) {
	for id, want := range map[string]bool{"pnl1": true, "a_b-C9": true, "": false, "a b": false, `a"><x`: false} {
		if panelIDPattern.MatchString(id) != want {
			t.Errorf("panelIDPattern(%q) != %v", id, want)
		}
	}
}

func TestColumnRowAndPreview(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	s := &Server{}
	for variant, want := range map[string]string{"create": `name="col_key"`, "add": `name="add_col_generated"`} {
		w := httptest.NewRecorder()
		s.handleDDLColumnRow(w, httptest.NewRequest("GET", "/api/ddl/table/column-row?variant="+variant+"&panel_id=p9", nil))
		if !strings.Contains(w.Body.String(), want) || !strings.Contains(w.Body.String(), "p9") {
			t.Errorf("variant %s: %s", variant, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.handleDDLColumnRow(w, httptest.NewRequest("GET", "/api/ddl/table/column-row?variant=remove", nil))
	if w.Body.Len() != 0 || w.Header().Get("HX-Trigger") != "panel-changed" {
		t.Errorf("remove: body %q trigger %q", w.Body.String(), w.Header().Get("HX-Trigger"))
	}
	w = httptest.NewRecorder()
	s.handleDDLColumnRow(w, httptest.NewRequest("GET", "/api/ddl/table/column-row?variant=add&panel_id=%22%3E", nil))
	if w.Code != 400 {
		t.Errorf("bad panel id accepted: %d", w.Code)
	}

	for _, c := range []struct {
		sql, want string
		err       error
		notWant   string
	}{
		{"DROP TABLE t", `<pre data-sql>DROP TABLE t</pre>`, nil, "data-panel-error"},
		{"", `data-panel-error>Name is required.`, formErr("Name is required."), "<pre"},
		{"", `<pre data-sql></pre>`, formErr("No changes requested."), "data-panel-error"},
	} {
		w := httptest.NewRecorder()
		renderPreview(w, c.sql, c.err)
		if !strings.Contains(w.Body.String(), c.want) || strings.Contains(w.Body.String(), c.notWant) {
			t.Errorf("renderPreview(%q, %v) = %s", c.sql, c.err, w.Body.String())
		}
	}
}

// The backup / restore dialogs are still real modals; render each op so a
// template/struct mismatch fails here instead of in the browser.
func TestBackupModalsRender(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	t.Setenv("BACKUP_DIR", t.TempDir())
	s := &Server{}
	for op := range backupOps {
		w := httptest.NewRecorder()
		s.renderBackupModal(w, backupModalData{Op: op, ServerID: 1, DB: "db", Schema: "public", Table: "t",
			Values: map[string]string{"format": "custom", "content": "all", "scope": "globals", "filename": "x"}})
		if w.Code != 200 || !strings.Contains(w.Body.String(), "/api/backup/"+op+"/run") {
			t.Errorf("backup %s: status %d, body %.200q", op, w.Code, w.Body.String())
		}
	}
}
