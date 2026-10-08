package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAlterTablePanelRendersWithUniqueIDs(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	s := &Server{}
	render := func(id string) string {
		w := httptest.NewRecorder()
		s.renderDDLModal(w, ddlModalData{
			Partial: "alter_table_panel.html", Kind: "table", ServerID: 1, DB: "db",
			Schema: "public", Table: "t", Name: "t", PanelID: id,
			Values:          map[string]string{"owner": "bob"},
			Dropdowns:       map[string][]string{"roles": {"bob"}, "schemas": {"public"}, "ref_tables": {"public.o"}},
			ExistingColumns: []existingColumn{{OrigName: "id", Type: "integer", TypeOrig: "integer", Nullable: "NO", NullableOrig: "NO"}},
		})
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	a, b := render("alt1"), render("alt2")
	for _, want := range []string{
		`id="alter-panel-alt1"`, `id="alt-addcols-alt1"`, `id="alt-addcol-tpl-alt1"`,
		`list="alt-coltypes-alt1"`, `name="col_orig_name"`, `value="id"`, `data-alter-collapse`,
		`hx-target="#alter-panel-alt1 select[name='fk_ref_cols']"`,
	} {
		if !strings.Contains(a, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	if strings.Contains(a, "alt2") || strings.Contains(b, "alt1") {
		t.Error("panel ids leak between panels")
	}
	for _, bad := range []string{`id="ddl-add-columns"`, `id="fk-ref-cols"`, `id="ddl-col-types"`} {
		if strings.Contains(a, bad) {
			t.Errorf("panel still uses the global id %s", bad)
		}
	}
}

func TestPanelIDPattern(t *testing.T) {
	for id, want := range map[string]bool{"alt1": true, "a_b-C9": true, "": false, "a b": false, `a"><x`: false} {
		if panelIDPattern.MatchString(id) != want {
			t.Errorf("panelIDPattern(%q) != %v", id, want)
		}
	}
}
