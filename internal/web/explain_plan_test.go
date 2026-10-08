package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

const samplePlan = `[{"Plan":{"Node Type":"Hash Join","Join Type":"Inner","Total Cost":500,"Plan Rows":10,
 "Actual Total Time":100,"Actual Rows":5000,"Actual Loops":1,
 "Plans":[
  {"Node Type":"Seq Scan","Relation Name":"orders","Schema":"public","Alias":"o","Total Cost":300,"Plan Rows":100,
   "Actual Total Time":90,"Actual Rows":10,"Actual Loops":1,"Rows Removed by Filter":50000,"Filter":"(status = 'x'::text)"},
  {"Node Type":"Sort","Total Cost":50,"Plan Rows":10,"Actual Total Time":5,"Actual Rows":10,"Actual Loops":1,"Sort Method":"external merge"}
 ]},"Planning Time":0.2,"Execution Time":101.5}]`

func TestBuildPlanView(t *testing.T) {
	v, err := buildPlanView([]byte(samplePlan), true, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.HasActual || v.MetricName != "Exclusive time" || v.Root.Type != "Hash Join" || len(v.Root.Children) != 2 {
		t.Fatalf("unexpected view: %+v", v)
	}
	seq := v.Table[0] // slowest: the Seq Scan, 90 ms of 100
	if seq.Type != "Seq Scan" || seq.Target != "on public.orders o" {
		t.Fatalf("slowest = %s %q", seq.Type, seq.Target)
	}
	flags := strings.Join(seq.Flags, "|")
	for _, want := range []string{"Hotspot", "Row estimate off", "Seq Scan discards 50000 rows per loop"} {
		if !strings.Contains(flags, want) {
			t.Errorf("Seq Scan flags %q missing %q", flags, want)
		}
	}
	if !strings.Contains(strings.Join(v.Root.Children[1].Flags, "|"), "Sort spilled to disk") {
		t.Error("external sort not flagged")
	}
	if v.Footer != "Planning 0.200 ms  ·  Execution 101.5 ms" {
		t.Errorf("footer = %q", v.Footer)
	}
}

func TestExplainPlanRenders(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	v, err := buildPlanView([]byte(samplePlan), true, "t1")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	RenderPartial(w, "explain_plan.html", v)
	body := w.Body.String()
	for _, want := range []string{`name="xp-t1"`, `<details class="xp-node" open>`, `Seq Scan`, `on public.orders o`,
		`xp-badge xp-badge-hot`, `Filter</td><td class="xp-val">(status = &#39;x&#39;::text)`, `rolled back`, `Raw JSON`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if _, err := buildPlanView([]byte(`[]`), false, "x"); err == nil {
		t.Error("empty plan accepted")
	}
}
