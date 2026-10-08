// -----------------------------------------------------------------------------
// Alter Table side panel. "Alter Table" opens a script tab whose left 70% is
// covered by a panel with the Alter Table form (internal/web/alter_panel.go,
// templates/partials/alter_table_panel.html). Every change re-runs the shared
// preview endpoint and puts the resulting ALTER statements into the tab's
// editor; the tab's normal Run button executes them. "<<" hides the panel,
// ">>" in the tab toolbar shows it again. The old modal (openAlterDDLDialog in
// ddl.js) is kept as the legacy example.
// -----------------------------------------------------------------------------
(function () {
    "use strict";

    var DEBOUNCE_MS = 400;
    var panelCounter = 0;

    // Text of the <pre> returned by POST /api/ddl/table/preview.
    function previewText(html) {
        var doc = new DOMParser().parseFromString(html, "text/html");
        var pre = doc.querySelector("pre");
        return (pre ? pre.textContent : doc.body.textContent).trim();
    }

    function setStatus(el, msg) {
        var box = el.querySelector("[data-alter-status]");
        box.textContent = msg;
        box.classList.toggle("hidden", !msg);
    }

    // Regenerates the SQL from the form; a stale response is dropped.
    function makeUpdater(tab, el) {
        var seq = 0;
        return function () {
            var mine = ++seq;
            var body = new URLSearchParams(new FormData(el.querySelector("form")));
            fetch("/api/ddl/table/preview", { method: "POST", body: body })
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
        btn.title = "Show the Alter Table panel";
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
        el.querySelector("[data-alter-collapse]").addEventListener("click", function () {
            setCollapsed(el, expandBtn, true);
        });
        el.querySelector("[data-alter-reset]").addEventListener("click", reload);
    }

    // Fetches the panel into the tab; reload() replaces it, keeping its
    // collapsed state and the toolbar's ">>" button.
    function mount(tab, qs, expandBtn) {
        var root = tab.firstElementChild;
        return fetch("/api/ddl/table/alter-panel?" + qs.toString())
            .then(function (r) { if (!r.ok) throw r; return r.text(); })
            .then(function (html) {
                var old = root.querySelector(".alter-panel");
                var collapsed = !!old && old.classList.contains("collapsed");
                if (old) old.remove();
                var holder = document.createElement("div");
                holder.innerHTML = html;
                var el = holder.firstElementChild;
                var toolbar = root.firstElementChild;
                if (toolbar.offsetHeight > 0) el.style.top = toolbar.offsetHeight + "px";
                root.appendChild(el);
                if (window.htmx) window.htmx.process(el);
                wire(tab, el, expandBtn, function () { mount(tab, qs, expandBtn); });
                setCollapsed(el, expandBtn, collapsed);
                expandBtn.onclick = function () { setCollapsed(el, expandBtn, false); };
            });
    }

    window.openAlterTableTab = function (nodeURL, name, folderID) {
        var ctx = parseObjectContext(nodeURL);
        if (!ctx.serverID || !name) return;
        var conn = connectionFromTreeURL(nodeURL);
        var qs = new URLSearchParams({
            server_id: ctx.serverID,
            db: ctx.dbName,
            schema: ctx.schema,
            table: ctx.table,
            name: name,
            folder_id: folderID || "server-" + ctx.serverID,
            panel_id: "alt" + (++panelCounter),
        });
        openTab("ALTER " + name, "",
            conn ? conn.serverID : ctx.serverID,
            conn ? conn.serverName : null,
            conn ? conn.dbName : ctx.dbName)
            .then(function () {
                var tab = document.getElementById("tab-contents").lastElementChild;
                return mount(tab, qs, addExpandButton(tab.firstElementChild.firstElementChild));
            })
            .catch(function () {
                if (window.showToast) window.showToast("Failed to open the Alter Table panel.", "error");
            });
    };
})();
