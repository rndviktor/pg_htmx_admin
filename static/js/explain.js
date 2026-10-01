// -----------------------------------------------------------------------------
// Visual EXPLAIN: renders the JSON plan returned by POST /api/explain as a
// tree (with exclusive-time bars and warning badges), a flat table and the raw
// JSON. Classic script: exposes window.renderExplainPlan; the pure plan
// analysis (analyzePlan) is also exported for node-based checks.
// -----------------------------------------------------------------------------
(function () {
    "use strict";

    var COLLAPSE_DEPTH = 6;      // nodes deeper than this start collapsed
    var HOTSPOT_PCT = 20;        // slowest node is flagged at/above this share
    var MISESTIMATE_FACTOR = 10; // estimate off by this factor either way
    var MISESTIMATE_MIN_ROWS = 100;
    var SEQ_FILTER_MIN_ROWS = 1000;

    var DETAIL_KEYS = [
        "Join Type", "Index Cond", "Recheck Cond", "Filter", "Hash Cond", "Merge Cond", "Join Filter",
        "Rows Removed by Filter", "Rows Removed by Join Filter", "Rows Removed by Index Recheck",
        "Sort Key", "Sort Method", "Sort Space Used", "Sort Space Type", "Group Key",
        "Hash Batches", "Original Hash Batches", "Peak Memory Usage",
        "Workers Planned", "Workers Launched", "Output",
        "Shared Hit Blocks", "Shared Read Blocks", "Shared Dirtied Blocks", "Shared Written Blocks",
        "Temp Read Blocks", "Temp Written Blocks",
        "Startup Cost", "Total Cost", "Plan Rows", "Plan Width",
        "Actual Startup Time", "Actual Total Time", "Actual Rows", "Actual Loops",
    ];

    function num(v) { return typeof v === "number" && isFinite(v) ? v : 0; }

    // Builds the analysed tree from one raw plan node. hasActual is true when the
    // plan came from EXPLAIN ANALYZE (actual times and rows are present).
    function buildNode(raw, hasActual, depth) {
        var loops = hasActual ? num(raw["Actual Loops"]) || 1 : 1;
        var node = {
            raw: raw,
            depth: depth,
            type: raw["Node Type"] || "?",
            target: nodeTarget(raw),
            loops: loops,
            time: hasActual ? num(raw["Actual Total Time"]) * loops : 0,
            cost: num(raw["Total Cost"]),
            planRows: num(raw["Plan Rows"]),
            actualRows: hasActual ? num(raw["Actual Rows"]) : null,
            children: (raw["Plans"] || []).map(function (c) { return buildNode(c, hasActual, depth + 1); }),
            flags: [],
        };
        var childTime = 0, childCost = 0;
        node.children.forEach(function (c) { childTime += c.time; childCost += c.cost; });
        node.exclusiveTime = Math.max(0, node.time - childTime);
        node.exclusiveCost = Math.max(0, node.cost - childCost);
        node.metric = hasActual ? node.exclusiveTime : node.exclusiveCost;
        return node;
    }

    function nodeTarget(raw) {
        var parts = [];
        if (raw["Relation Name"]) {
            var rel = raw["Schema"] ? raw["Schema"] + "." + raw["Relation Name"] : raw["Relation Name"];
            parts.push("on " + rel + (raw["Alias"] && raw["Alias"] !== raw["Relation Name"] ? " " + raw["Alias"] : ""));
        }
        if (raw["Index Name"]) parts.push("using " + raw["Index Name"]);
        if (raw["CTE Name"]) parts.push("CTE " + raw["CTE Name"]);
        if (raw["Subplan Name"]) parts.push(raw["Subplan Name"]);
        if (raw["Join Type"] && /Join|Loop/.test(raw["Node Type"] || "")) parts.push(raw["Join Type"] + " join");
        return parts.join(" ");
    }

    function flatten(node, out) {
        out.push(node);
        node.children.forEach(function (c) { flatten(c, out); });
        return out;
    }

    // Adds the share of the total and the warning flags to every node.
    function annotate(root, hasActual) {
        var all = flatten(root, []);
        var total = 0;
        all.forEach(function (n) { total += n.metric; });
        var slowest = null;
        all.forEach(function (n) {
            n.pct = total > 0 ? (n.metric / total) * 100 : 0;
            if (!slowest || n.metric > slowest.metric) slowest = n;

            if (hasActual && n.actualRows !== null) {
                var est = Math.max(n.planRows, 1), act = Math.max(n.actualRows, 1);
                n.factor = act / est;
                if (Math.max(n.planRows, n.actualRows) >= MISESTIMATE_MIN_ROWS &&
                    (n.factor >= MISESTIMATE_FACTOR || n.factor <= 1 / MISESTIMATE_FACTOR)) {
                    n.flags.push("Row estimate off " + (n.factor >= 1
                        ? n.factor.toFixed(0) + "x too low"
                        : (1 / n.factor).toFixed(0) + "x too high"));
                }
                var removed = num(n.raw["Rows Removed by Filter"]);
                if (n.type === "Seq Scan" && removed >= SEQ_FILTER_MIN_ROWS && removed > 5 * n.actualRows) {
                    n.flags.push("Seq Scan discards " + removed + " rows per loop");
                }
            }
            var sortMethod = String(n.raw["Sort Method"] || "");
            if (/^external/i.test(sortMethod)) n.flags.push("Sort spilled to disk");
            if (num(n.raw["Hash Batches"]) > 1) n.flags.push("Hash spilled (" + n.raw["Hash Batches"] + " batches)");
            if (num(n.raw["Temp Written Blocks"]) > 0) n.flags.push("Temp files written");
        });
        if (slowest && all.length > 1 && slowest.pct >= HOTSPOT_PCT) slowest.flags.unshift("Hotspot");
        return all;
    }

    // Pure entry point: payload.plan is the EXPLAIN (FORMAT JSON) array.
    function analyzePlan(planJSON) {
        var doc = Array.isArray(planJSON) ? planJSON[0] : planJSON;
        var rootRaw = doc && doc["Plan"];
        if (!rootRaw) return null;
        var hasActual = rootRaw["Actual Total Time"] !== undefined;
        var root = buildNode(rootRaw, hasActual, 0);
        var all = annotate(root, hasActual);
        return { doc: doc, root: root, all: all, hasActual: hasActual };
    }

    // ---- rendering -----------------------------------------------------------

    function el(tag, cls, text) {
        var e = document.createElement(tag);
        if (cls) e.className = cls;
        if (text !== undefined) e.textContent = text;
        return e;
    }

    function fmtMs(ms) {
        if (ms >= 1000) return (ms / 1000).toFixed(2) + " s";
        return (ms >= 10 ? ms.toFixed(1) : ms.toFixed(3)) + " ms";
    }

    function nodeMetricText(n, hasActual) {
        return hasActual ? fmtMs(n.exclusiveTime) : n.exclusiveCost.toFixed(2);
    }

    function rowsText(n) {
        if (n.actualRows === null) return "~" + n.planRows + " rows";
        return n.actualRows + " rows (est " + n.planRows + ")" + (n.loops > 1 ? " x" + n.loops : "");
    }

    function barTier(pct) { return pct >= 50 ? "xp-hot" : pct >= 20 ? "xp-warm" : "xp-cool"; }

    function detailsTable(n) {
        var t = el("table", "xp-details-table");
        DETAIL_KEYS.forEach(function (k) {
            var v = n.raw[k];
            if (v === undefined) return;
            var tr = el("tr");
            tr.appendChild(el("td", "xp-key", k));
            tr.appendChild(el("td", "xp-val", Array.isArray(v) ? v.join(", ") : String(v)));
            t.appendChild(tr);
        });
        return t;
    }

    function badges(n) {
        var box = el("span", "xp-badges");
        n.flags.forEach(function (f) { box.appendChild(el("span", f === "Hotspot" ? "xp-badge xp-badge-hot" : "xp-badge", f)); });
        return box;
    }

    function renderTreeNode(n, hasActual) {
        var wrap = el("div", "xp-node");
        var row = el("div", "xp-row");
        var toggle = el("span", "xp-toggle", n.children.length ? "▾" : " ");
        var title = el("span", "xp-title");
        title.appendChild(el("span", "xp-type", n.type));
        if (n.target) title.appendChild(el("span", "xp-target", " " + n.target));
        var bar = el("span", "xp-bar");
        var fill = el("span", "xp-bar-fill " + barTier(n.pct));
        fill.style.width = Math.max(1, Math.min(100, n.pct)) + "%";
        bar.appendChild(fill);
        row.append(toggle, title, bar,
            el("span", "xp-num", nodeMetricText(n, hasActual) + " · " + n.pct.toFixed(0) + "%"),
            el("span", "xp-num xp-rows", rowsText(n)), badges(n));

        var details = el("div", "xp-details");
        details.hidden = true;
        details.appendChild(detailsTable(n));
        row.addEventListener("click", function () { details.hidden = !details.hidden; });

        var kids = el("div", "xp-children");
        n.children.forEach(function (c) { kids.appendChild(renderTreeNode(c, hasActual)); });
        if (n.depth >= COLLAPSE_DEPTH && n.children.length) {
            kids.hidden = true;
            toggle.textContent = "▸";
        }
        toggle.addEventListener("click", function (e) {
            e.stopPropagation();
            if (!n.children.length) return;
            kids.hidden = !kids.hidden;
            toggle.textContent = kids.hidden ? "▸" : "▾";
        });

        wrap.append(row, details, kids);
        return wrap;
    }

    function renderTable(analysis) {
        var rows = analysis.all.slice().sort(function (a, b) { return b.metric - a.metric; });
        var t = el("table", "xp-table");
        var head = el("tr");
        ["Node", analysis.hasActual ? "Exclusive time" : "Exclusive cost", "%", "Rows", "Loops", "Flags"]
            .forEach(function (h) { head.appendChild(el("th", "", h)); });
        t.appendChild(head);
        rows.forEach(function (n) {
            var tr = el("tr");
            tr.appendChild(el("td", "", n.type + (n.target ? " " + n.target : "")));
            tr.appendChild(el("td", "", nodeMetricText(n, analysis.hasActual)));
            tr.appendChild(el("td", "", n.pct.toFixed(1)));
            tr.appendChild(el("td", "", rowsText(n)));
            tr.appendChild(el("td", "", String(n.loops)));
            tr.appendChild(el("td", "", n.flags.join("; ")));
            t.appendChild(tr);
        });
        return t;
    }

    function renderFooter(analysis, payload) {
        var doc = analysis.doc;
        var parts = [];
        if (doc["Planning Time"] !== undefined) parts.push("Planning " + fmtMs(num(doc["Planning Time"])));
        if (doc["Execution Time"] !== undefined) parts.push("Execution " + fmtMs(num(doc["Execution Time"])));
        (doc["Triggers"] || []).forEach(function (t) {
            parts.push("Trigger " + t["Trigger Name"] + ": " + fmtMs(num(t["Time"])) + " (" + t["Calls"] + " calls)");
        });
        var foot = el("div", "xp-footer", parts.join("  ·  "));
        if (payload.rolled_back) {
            foot.appendChild(el("div", "xp-note",
                "The statement was executed to collect these timings and then rolled back. " +
                "Non-transactional effects (sequences, NOTIFY, external calls) are not undone."));
        }
        return foot;
    }

    function renderExplainPlan(container, payload) {
        container.textContent = "";
        var analysis = analyzePlan(payload && payload.plan);
        if (!analysis) {
            container.appendChild(el("div", "p-4 text-red-400 text-sm", "No plan was returned."));
            return;
        }

        var tabs = el("div", "xp-tabs");
        var body = el("div", "xp-body");
        var views = {
            Tree: function () {
                var f = document.createDocumentFragment();
                f.appendChild(renderTreeNode(analysis.root, analysis.hasActual));
                return f;
            },
            Table: function () { return renderTable(analysis); },
            "Raw JSON": function () { return el("pre", "xp-raw", JSON.stringify(payload.plan, null, 2)); },
        };
        function show(name) {
            body.textContent = "";
            body.appendChild(views[name]());
            Array.prototype.forEach.call(tabs.children, function (b) {
                b.classList.toggle("xp-tab-active", b.textContent === name);
            });
        }
        Object.keys(views).forEach(function (name) {
            var b = el("button", "xp-tab", name);
            b.type = "button";
            b.addEventListener("click", function () { show(name); });
            tabs.appendChild(b);
        });

        container.append(tabs, body, renderFooter(analysis, payload));
        show("Tree");
    }

    if (typeof window !== "undefined") window.renderExplainPlan = renderExplainPlan;
    if (typeof module !== "undefined" && module.exports) module.exports = { analyzePlan: analyzePlan };
})();
