package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Visual EXPLAIN. handleExplain returns the EXPLAIN (FORMAT JSON) document
// analysed and rendered as HTML (templates/partials/explain_plan.html): a tree
// of <details> with exclusive-time bars and warning badges, a flat table and
// the raw JSON. Collapsing and switching views needs no script.

const (
	explainCollapseDepth   = 6    // nodes deeper than this start collapsed
	explainHotspotPct      = 20.0 // slowest node is flagged at/above this share
	explainMisestimate     = 10.0 // estimate off by this factor either way
	explainMisestimateRows = 100
	explainSeqFilterRows   = 1000
)

// Plan properties listed in a node's details, in display order.
var explainDetailKeys = []string{
	"Join Type", "Index Cond", "Recheck Cond", "Filter", "Hash Cond", "Merge Cond", "Join Filter",
	"Rows Removed by Filter", "Rows Removed by Join Filter", "Rows Removed by Index Recheck",
	"Sort Key", "Sort Method", "Sort Space Used", "Sort Space Type", "Group Key",
	"Hash Batches", "Original Hash Batches", "Peak Memory Usage",
	"Workers Planned", "Workers Launched", "Output",
	"Shared Hit Blocks", "Shared Read Blocks", "Shared Dirtied Blocks", "Shared Written Blocks",
	"Temp Read Blocks", "Temp Written Blocks",
	"Startup Cost", "Total Cost", "Plan Rows", "Plan Width",
	"Actual Startup Time", "Actual Total Time", "Actual Rows", "Actual Loops",
}

type planDetail struct{ Key, Value string }

type planNode struct {
	raw        map[string]any
	Type       string
	Target     string
	Open       bool
	Children   []*planNode
	Flags      []string
	Details    []planDetail
	Loops      float64
	Pct        float64
	BarWidth   int
	BarTier    string
	MetricText string
	RowsText   string

	depth                int
	time, cost, planRows float64
	actualRows           float64
	hasActualRows        bool
	exclusiveTime        float64
	exclusiveCost        float64
	metric               float64
}

type planView struct {
	Root       *planNode
	Table      []*planNode // slowest first
	HasActual  bool
	MetricName string
	Footer     string
	RolledBack bool
	RawJSON    string
	Group      string // unique radio group, keeps the views of two tabs apart
}

func num(v any) float64 {
	if f, ok := v.(float64); ok && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return f
	}
	return 0
}

func fixed(v float64, digits int) string { return strconv.FormatFloat(v, 'f', digits, 64) }

func fmtMs(ms float64) string {
	switch {
	case ms >= 1000:
		return fixed(ms/1000, 2) + " s"
	case ms >= 10:
		return fixed(ms, 1) + " ms"
	}
	return fixed(ms, 3) + " ms"
}

var joinNodePattern = regexp.MustCompile(`Join|Loop`)

func planTarget(raw map[string]any) string {
	str := func(k string) string { s, _ := raw[k].(string); return s }
	var parts []string
	if rel := str("Relation Name"); rel != "" {
		if schema := str("Schema"); schema != "" {
			rel = schema + "." + rel
		}
		on := "on " + rel
		if alias := str("Alias"); alias != "" && alias != str("Relation Name") {
			on += " " + alias
		}
		parts = append(parts, on)
	}
	if v := str("Index Name"); v != "" {
		parts = append(parts, "using "+v)
	}
	if v := str("CTE Name"); v != "" {
		parts = append(parts, "CTE "+v)
	}
	if v := str("Subplan Name"); v != "" {
		parts = append(parts, v)
	}
	if v := str("Join Type"); v != "" && joinNodePattern.MatchString(str("Node Type")) {
		parts = append(parts, v+" join")
	}
	return strings.Join(parts, " ")
}

func buildPlanNode(raw map[string]any, hasActual bool, depth int) *planNode {
	n := &planNode{raw: raw, depth: depth, Loops: 1}
	n.Type, _ = raw["Node Type"].(string)
	if n.Type == "" {
		n.Type = "?"
	}
	n.Target = planTarget(raw)
	if hasActual {
		if n.Loops = num(raw["Actual Loops"]); n.Loops == 0 {
			n.Loops = 1
		}
		n.time = num(raw["Actual Total Time"]) * n.Loops
		n.actualRows, n.hasActualRows = num(raw["Actual Rows"]), true
	}
	n.cost, n.planRows = num(raw["Total Cost"]), num(raw["Plan Rows"])
	var childTime, childCost float64
	if plans, ok := raw["Plans"].([]any); ok {
		for _, p := range plans {
			if m, ok := p.(map[string]any); ok {
				c := buildPlanNode(m, hasActual, depth+1)
				n.Children = append(n.Children, c)
				childTime += c.time
				childCost += c.cost
			}
		}
	}
	n.exclusiveTime = math.Max(0, n.time-childTime)
	n.exclusiveCost = math.Max(0, n.cost-childCost)
	n.metric = n.exclusiveCost
	if hasActual {
		n.metric = n.exclusiveTime
	}
	return n
}

func flattenPlan(n *planNode, out []*planNode) []*planNode {
	out = append(out, n)
	for _, c := range n.Children {
		out = flattenPlan(c, out)
	}
	return out
}

func detailText(v any) string {
	switch t := v.(type) {
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = detailText(e)
		}
		return strings.Join(parts, ", ")
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// flagPlanNode appends the node's warning flags.
func flagPlanNode(n *planNode) {
	if n.hasActualRows {
		factor := math.Max(n.actualRows, 1) / math.Max(n.planRows, 1)
		if math.Max(n.planRows, n.actualRows) >= explainMisestimateRows &&
			(factor >= explainMisestimate || factor <= 1/explainMisestimate) {
			if factor >= 1 {
				n.Flags = append(n.Flags, "Row estimate off "+fixed(factor, 0)+"x too low")
			} else {
				n.Flags = append(n.Flags, "Row estimate off "+fixed(1/factor, 0)+"x too high")
			}
		}
		removed := num(n.raw["Rows Removed by Filter"])
		if n.Type == "Seq Scan" && removed >= explainSeqFilterRows && removed > 5*n.actualRows {
			n.Flags = append(n.Flags, "Seq Scan discards "+fixed(removed, 0)+" rows per loop")
		}
	}
	if m, _ := n.raw["Sort Method"].(string); strings.HasPrefix(strings.ToLower(m), "external") {
		n.Flags = append(n.Flags, "Sort spilled to disk")
	}
	if b := num(n.raw["Hash Batches"]); b > 1 {
		n.Flags = append(n.Flags, "Hash spilled ("+fixed(b, 0)+" batches)")
	}
	if num(n.raw["Temp Written Blocks"]) > 0 {
		n.Flags = append(n.Flags, "Temp files written")
	}
}

// annotatePlan adds each node's share of the total, its flags and the texts
// the template prints.
func annotatePlan(all []*planNode, hasActual bool) {
	var total float64
	for _, n := range all {
		total += n.metric
	}
	var slowest *planNode
	for _, n := range all {
		if total > 0 {
			n.Pct = n.metric / total * 100
		}
		if slowest == nil || n.metric > slowest.metric {
			slowest = n
		}
		flagPlanNode(n)

		n.BarWidth = int(math.Max(1, math.Min(100, math.Round(n.Pct))))
		switch {
		case n.Pct >= 50:
			n.BarTier = "xp-hot"
		case n.Pct >= 20:
			n.BarTier = "xp-warm"
		default:
			n.BarTier = "xp-cool"
		}
		n.Open = n.depth < explainCollapseDepth
		if hasActual {
			n.MetricText = fmtMs(n.exclusiveTime)
			n.RowsText = fixed(n.actualRows, 0) + " rows (est " + fixed(n.planRows, 0) + ")"
			if n.Loops > 1 {
				n.RowsText += " x" + fixed(n.Loops, 0)
			}
		} else {
			n.MetricText = fixed(n.exclusiveCost, 2)
			n.RowsText = "~" + fixed(n.planRows, 0) + " rows"
		}
		for _, k := range explainDetailKeys {
			if v, ok := n.raw[k]; ok {
				n.Details = append(n.Details, planDetail{k, detailText(v)})
			}
		}
	}
	if slowest != nil && len(all) > 1 && slowest.Pct >= explainHotspotPct {
		slowest.Flags = append([]string{"Hotspot"}, slowest.Flags...)
	}
}

// Label is the node's type with its target, for the flat table.
func (n *planNode) Label() string {
	if n.Target == "" {
		return n.Type
	}
	return n.Type + " " + n.Target
}

func (n *planNode) Joined() string    { return strings.Join(n.Flags, "; ") }
func (n *planNode) PctText() string   { return fixed(n.Pct, 1) }
func (n *planNode) PctRound() string  { return fixed(n.Pct, 0) }
func (n *planNode) LoopsText() string { return fixed(n.Loops, 0) }

func planFooter(doc map[string]any) string {
	var parts []string
	if _, ok := doc["Planning Time"]; ok {
		parts = append(parts, "Planning "+fmtMs(num(doc["Planning Time"])))
	}
	if _, ok := doc["Execution Time"]; ok {
		parts = append(parts, "Execution "+fmtMs(num(doc["Execution Time"])))
	}
	if triggers, ok := doc["Triggers"].([]any); ok {
		for _, t := range triggers {
			if m, ok := t.(map[string]any); ok {
				parts = append(parts, fmt.Sprintf("Trigger %v: %s (%v calls)", m["Trigger Name"], fmtMs(num(m["Time"])), m["Calls"]))
			}
		}
	}
	return strings.Join(parts, "  ·  ")
}

// buildPlanView analyses the raw EXPLAIN (FORMAT JSON) output.
func buildPlanView(plan []byte, rolledBack bool, group string) (*planView, error) {
	var docs []map[string]any
	if err := json.Unmarshal(plan, &docs); err != nil || len(docs) == 0 {
		return nil, fmt.Errorf("unexpected plan format")
	}
	rootRaw, ok := docs[0]["Plan"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("no plan was returned")
	}
	_, hasActual := rootRaw["Actual Total Time"]
	root := buildPlanNode(rootRaw, hasActual, 0)
	all := flattenPlan(root, nil)
	annotatePlan(all, hasActual)

	table := append([]*planNode(nil), all...)
	sort.SliceStable(table, func(i, j int) bool { return table[i].metric > table[j].metric })

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, plan, "", "  "); err != nil {
		pretty.Reset()
		pretty.Write(plan)
	}
	v := &planView{Root: root, Table: table, HasActual: hasActual, MetricName: "Exclusive cost",
		Footer: planFooter(docs[0]), RolledBack: rolledBack, RawJSON: pretty.String(), Group: group}
	if hasActual {
		v.MetricName = "Exclusive time"
	}
	return v, nil
}
