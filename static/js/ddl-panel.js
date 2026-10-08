// -----------------------------------------------------------------------------
// DDL side panels. Create / Alter / Drop forms and the maintenance operations
// open a script tab with the form panel next to the editor (internal/web/ddl.go
// handleDDLPanel, maintenance.go handleMaintPanel). The panel is htmx all the
// way: the form posts to the server's SQL builder on every change, Reset and
// the column rows are server round trips. This file only opens the tab, copies
// the generated SQL into the editor, toggles the panel and refreshes the tree.
// -----------------------------------------------------------------------------
(function () {
    "use strict";

    var panelCounter = 0;

    // Query string from the node's URL context (server/db/schema/table), the
    // tree folder to refresh and the panel id that keeps element ids unique.
    function panelQuery(nodeURL, folderID) {
        var ctx = parseObjectContext(nodeURL);
        var qs = new URLSearchParams();
        if (ctx.serverID) qs.set("server_id", ctx.serverID);
        if (ctx.dbName) qs.set("db", ctx.dbName);
        if (ctx.schema) qs.set("schema", ctx.schema);
        if (ctx.table) qs.set("table", ctx.table);
        qs.set("folder_id", folderID || (ctx.serverID ? "server-" + ctx.serverID : ""));
        qs.set("panel_id", "pnl" + (++panelCounter));
        return qs;
    }

    // Opens a script tab on the panel's database and mounts the panel in it.
    function openPanelTab(url, qs, label, nodeURL) {
        var serverID = qs.get("server_id");
        if (!serverID) return;
        fetch(url + "?" + qs.toString())
            .then(function (r) { if (!r.ok) throw r; return r.text(); })
            .then(function (html) {
                var holder = document.createElement("div");
                holder.innerHTML = html;
                var el = holder.firstElementChild;
                if (!el.dataset.db) throw new Error("no database to run the SQL on");
                var conn = connectionFromTreeURL(nodeURL);
                return openTab(label, "", serverID, conn ? conn.serverName : serverNameForID(serverID), el.dataset.db)
                    .then(function () {
                        var root = document.getElementById("tab-contents").lastElementChild.firstElementChild;
                        root.appendChild(el);
                        htmx.process(el);
                    });
            })
            .catch(function () {
                if (window.showToast) window.showToast("Failed to open the " + label + " panel.", "error");
            });
    }

    window.openDDLPanel = function (action, kind, nodeURL, name, folderID) {
        var qs = panelQuery(nodeURL, folderID);
        qs.set("action", action);
        if (name) qs.set("name", name);
        openPanelTab("/api/ddl/" + kind + "/panel", qs,
            action.charAt(0).toUpperCase() + action.slice(1) + " " + (name || kind), nodeURL);
    };

    // extra carries the op-specific fixed fields (target, reindex_target, name).
    window.openMaintPanel = function (op, nodeURL, extra) {
        var qs = panelQuery(nodeURL, "");
        Object.keys(extra || {}).forEach(function (k) { if (extra[k]) qs.set(k, extra[k]); });
        openPanelTab("/api/maint/" + op + "/panel", qs, op.charAt(0).toUpperCase() + op.slice(1), nodeURL);
    };

    // The panel's SQL arrives as <pre data-sql> in its output slot: copy it
    // into the tab's editor (errors arrive as [data-panel-error] and are only
    // shown).
    document.addEventListener("htmx:afterSwap", function (e) {
        var out = e.detail && e.detail.target;
        if (!out || !out.classList || !out.classList.contains("ddl-out")) return;
        var pre = out.querySelector("pre[data-sql]");
        var tab = out.closest("[id^='tab-content']");
        if (!pre || !tab || !window.SqlEditor) return;
        var sql = pre.textContent.trim();
        if (window.SqlEditor.value(tab) !== sql) window.SqlEditor.set(tab, sql);
    });

    // "<<" in the panel and ">>" in the tab toolbar.
    document.addEventListener("click", function (e) {
        var toggle = e.target.closest("[data-panel-toggle]");
        var tab = toggle && toggle.closest("[id^='tab-content']");
        if (tab) tab.firstElementChild.classList.toggle("ddl-collapsed");
    });

    // Called by tabs.js after a run succeeded: refresh the tree folder the
    // panel belongs to and, for Alter, reload the form against the new
    // definition so the next change is diffed against it.
    window.ddlPanelAfterRun = function (tab) {
        var el = tab.querySelector(".ddl-panel");
        if (!el || el.dataset.action === "maint") return;
        if (el.dataset.folder) htmx.trigger(document.body, "ddl-refresh", { value: el.dataset.folder });
        if (el.dataset.action === "alter") htmx.trigger(el.querySelector("[data-panel-reset]"), "click");
    };
})();
