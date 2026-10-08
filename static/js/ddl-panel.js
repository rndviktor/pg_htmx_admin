// -----------------------------------------------------------------------------
// DDL side panels. Create / Alter / Drop of every object kind and the
// maintenance operations (Vacuum / Analyze / Cluster / Reindex) open a script
// tab whose left 70% is a panel holding the form (internal/web/ddl.go
// handleDDLPanel, internal/web/maintenance.go handleMaintPanel, templates
// ddl_*_panel.html / maint_panel.html). Every change re-runs the server-side
// SQL builder through the panel's preview endpoint and puts the statements into
// the tab's editor; the tab's Run button executes them. "<<" hides the panel,
// ">>" in the tab toolbar shows it again.
// -----------------------------------------------------------------------------
(function () {
    "use strict";

    var DEBOUNCE_MS = 400;
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

    // Text of the <pre> returned by the preview endpoints.
    function previewText(html) {
        var doc = new DOMParser().parseFromString(html, "text/html");
        var pre = doc.querySelector("pre");
        return (pre ? pre.textContent : doc.body.textContent).trim();
    }

    function setStatus(el, msg) {
        var box = el.querySelector("[data-panel-status]");
        box.textContent = msg;
        box.classList.toggle("hidden", !msg);
    }

    // Regenerates the SQL from the form; a stale response is dropped.
    function makeUpdater(tab, el) {
        var seq = 0;
        return function () {
            var mine = ++seq;
            var body = new URLSearchParams(new FormData(el.querySelector("form")));
            fetch(el.dataset.preview, { method: "POST", body: body })
                .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.text(); })
                .then(function (html) {
                    if (mine !== seq) return;
                    var text = previewText(html);
                    var isError = text.indexOf("Error:") === 0;
                    var noChange = isError && text.indexOf("No changes requested") >= 0;
                    setStatus(el, isError && !noChange ? text.replace(/^Error:\s*/, "") : "");
                    if (isError && !noChange) return; // keep the last valid SQL
                    var sql = noChange ? "" : text;
                    if (window.SqlEditor && window.SqlEditor.value(tab) !== sql) window.SqlEditor.set(tab, sql);
                })
                .catch(function (e) { if (mine === seq) setStatus(el, "Preview failed: " + e.message); });
        };
    }

    function setCollapsed(el, expandBtn, collapsed) {
        el.classList.toggle("collapsed", collapsed);
        expandBtn.classList.toggle("hidden", !collapsed);
    }

    function addExpandButton(toolbar) {
        var btn = document.createElement("button");
        btn.type = "button";
        btn.textContent = ">>";
        btn.title = "Show the form panel";
        btn.className = "hidden px-2 py-1 text-blue-400 hover:text-blue-300 hover:bg-gray-700 border border-gray-600 rounded text-sm inline-flex items-center justify-center";
        toolbar.insertBefore(btn, toolbar.firstChild);
        return btn;
    }

    function wire(tab, el, expandBtn, reload) {
        var update = makeUpdater(tab, el);
        var timer = null;
        var schedule = function () { clearTimeout(timer); timer = setTimeout(update, DEBOUNCE_MS); };
        el.addEventListener("input", schedule);
        el.addEventListener("change", schedule);
        el.addEventListener("htmx:afterSwap", schedule); // FK referenced columns reloaded
        el.addEventListener("click", function (e) {
            if (e.target.closest("button[type='button'][onclick]")) schedule(); // add / remove column
        });
        el.querySelector("[data-panel-collapse]").addEventListener("click", function () {
            setCollapsed(el, expandBtn, true);
        });
        el.querySelector("[data-panel-reset]").addEventListener("click", reload);
        // Drop and maintenance forms are valid as opened: show their SQL at once.
        if (el.dataset.action !== "create") update();
    }

    // Puts the panel element into the tab; `reload` re-fetches it.
    function mount(tab, el, expandBtn, reload, collapsed) {
        var root = tab.firstElementChild;
        var toolbar = root.firstElementChild;
        if (toolbar.offsetHeight > 0) el.style.top = toolbar.offsetHeight + "px";
        root.appendChild(el);
        if (window.htmx) window.htmx.process(el);
        wire(tab, el, expandBtn, reload);
        setCollapsed(el, expandBtn, collapsed);
        expandBtn.onclick = function () { setCollapsed(el, expandBtn, false); };
    }

    function fetchPanel(url, qs) {
        return fetch(url + "?" + qs.toString())
            .then(function (r) { if (!r.ok) throw r; return r.text(); })
            .then(function (html) {
                var holder = document.createElement("div");
                holder.innerHTML = html;
                return holder.firstElementChild;
            });
    }

    // Opens a script tab on the panel's database and mounts the panel in it.
    function openPanelTab(url, qs, label, nodeURL) {
        var serverID = qs.get("server_id");
        if (!serverID) return;
        fetchPanel(url, qs).then(function (el) {
            var db = el.dataset.db;
            if (!db) throw new Error("no database to run the SQL on");
            var conn = connectionFromTreeURL(nodeURL);
            return openTab(label, "", serverID, conn ? conn.serverName : serverNameForID(serverID), db)
                .then(function () {
                    var tab = document.getElementById("tab-contents").lastElementChild;
                    var expandBtn = addExpandButton(tab.firstElementChild.firstElementChild);
                    var reload = function () {
                        fetchPanel(url, qs).then(function (fresh) {
                            var old = tab.querySelector(".ddl-panel");
                            var collapsed = !!old && old.classList.contains("collapsed");
                            if (old) old.remove();
                            mount(tab, fresh, expandBtn, reload, collapsed);
                        });
                    };
                    mount(tab, el, expandBtn, reload, false);
                });
        }).catch(function () {
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

    // Called by tabs.js after a run succeeded: refresh the tree folder the
    // panel belongs to and, for Alter, reload the form against the new
    // definition so the next change is diffed against it.
    window.ddlPanelAfterRun = function (tab) {
        var el = tab.querySelector(".ddl-panel");
        if (!el || el.dataset.action === "maint") return;
        if (el.dataset.folder && window.htmx) {
            window.htmx.trigger(document.body, "ddl-refresh", { value: el.dataset.folder });
        }
        if (el.dataset.action === "alter") el.querySelector("[data-panel-reset]").click();
    };
})();
