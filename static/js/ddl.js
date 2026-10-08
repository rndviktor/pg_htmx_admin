// -----------------------------------------------------------------------------
// Shared modal helpers (Backup & Restore, Storage Manager), the DROP Script
// tab, the tree-refresh listener and the column-row helpers used by the DDL
// side panels (static/js/ddl-panel.js).
// -----------------------------------------------------------------------------
(function () {
    function closeDDL() {
        const container = document.getElementById("modal-container");
        if (container) container.innerHTML = "";
    }

    // Modals are swapped into #modal-container and closed with this helper.
    window.closeDDL = closeDDL;

    function openModal(url) {
        fetch(url)
            .then((r) => { if (!r.ok) throw r; return r.text(); })
            .then((html) => {
                const container = document.getElementById("modal-container");
                if (!container) return;
                container.innerHTML = html;
                if (window.htmx && htmx.process) htmx.process(container);
            })
            .catch(async (httpErr) => {
                if (httpErr && httpErr.status === 401) return;
                let detail = httpErr ? (httpErr.statusText || "HTTP " + httpErr.status) : "";
                if (httpErr && typeof httpErr.text === "function") {
                    try { detail = (await httpErr.text()) || detail; } catch (e) { /* ignore */ }
                }
                const msg = "Failed to open dialog" + (detail ? ": " + detail : ".");
                if (window.showToast) window.showToast(msg, "error");
            });
    }

    // Resolves the tree container id that a create/drop must refresh.
    // For create the caller right-clicked the category folder itself, so its
    // expander's hx-target *is* the container; for drop the node is a leaf
    // inside a folder container, so the closest ancestor div[id] is used.
    // Falls back to the server node so server-scoped actions always work.
    function ddlFolderID(el, isCreate) {
        if (!el) return "";
        if (isCreate) {
            const btn = el.querySelector("button[hx-get][hx-target]");
            if (btn) {
                const target = btn.getAttribute("hx-target");
                if (target) return target.replace(/^#/, "");
            }
        }
        const container = el.closest("div[id]");
        return container ? container.id : "";
    }
    window.ddlFolderID = ddlFolderID;

    // Backup & Restore dialogs (pg_dump / pg_dumpall / pg_restore) take the
    // node's server/db/schema/table context.
    window.openBackupDialog = function (op, nodeURL) {
        const ctx = parseObjectContext(nodeURL);
        const qs = new URLSearchParams();
        if (ctx.serverID) qs.set("server_id", ctx.serverID);
        if (ctx.dbName) qs.set("db", ctx.dbName);
        if (ctx.schema) qs.set("schema", ctx.schema);
        if (ctx.table) qs.set("table", ctx.table);
        if (!qs.get("server_id")) return;
        openModal("/api/backup/" + op + "/modal?" + qs.toString());
    };

    window.openStorageManager = function () { openModal("/api/storage"); };
    window.openBackupJobs = function () { openModal("/api/backup/jobs"); };

    // "DROP Script": fetches the DROP SQL for any droppable object kind and
    // opens it in a read-only script tab, without running it (unlike
    // the Drop side panel, which builds the statement from a form).
    window.openDropScriptTab = function (kind, nodeURL, name) {
        const ctx = parseObjectContext(nodeURL);
        if (!ctx.serverID || !name) return;
        const qs = new URLSearchParams();
        qs.set("server_id", ctx.serverID);
        if (ctx.schema) qs.set("schema", ctx.schema);
        if (ctx.table) qs.set("table", ctx.table);
        qs.set("name", name);
        fetch("/api/ddl/" + kind + "/drop-script?" + qs.toString())
            .then((r) => { if (!r.ok) throw r; return r.json(); })
            .then((data) => {
                const conn = connectionFromTreeURL(nodeURL);
                openTab("DROP " + name, data.query,
                    conn ? conn.serverID : ctx.serverID,
                    conn ? conn.serverName : null,
                    conn ? conn.dbName : ctx.dbName);
            })
            .catch(async (httpErr) => {
                let detail = httpErr ? (httpErr.statusText || "HTTP " + httpErr.status) : "";
                if (httpErr && typeof httpErr.text === "function") {
                    try { detail = (await httpErr.text()) || detail; } catch (e) { /* ignore */ }
                }
                const msg = "Failed to generate DROP script" + (detail ? ": " + detail : ".");
                if (window.showToast) window.showToast(msg, "error");
            });
    };

    // Creates a fresh blank column row from the hidden template. The row is a
    // full width (6-field) copy so at least one empty column placeholder never
    // forces input fields onto separate lines. wrapID/tplID default to the
    // Create Table modal's ids; the Alter Table modal's "Add columns" section
    // passes its own so the two never collide.
    window.ddlAddColumnRow = function (wrapID, tplID) {
        const tpl = document.getElementById(tplID || "ddl-col-row-template");
        const wrap = document.getElementById(wrapID || "ddl-columns");
        if (!tpl || !wrap) return;
        wrap.appendChild(tpl.content.cloneNode(true));
    };

    // Removes a column row; the first (heading) row is kept so the columns
    // block never ends up empty.
    window.ddlRemoveColumnRow = function (btn, wrapID) {
        const wrap = document.getElementById(wrapID || "ddl-columns");
        const row = btn && btn.closest("[data-col-row]");
        if (!wrap || !row) return;
        const rows = wrap.querySelectorAll("[data-col-row]");
        if (rows.length <= 1) return;
        row.remove();
    };

    // After a DDL panel's SQL ran (ddl-panel.js) the "ddl-refresh" event
    // carries the tree container id to re-fetch (folders stay expanded and are
    // refreshed in place).
    document.addEventListener("ddl-refresh", (e) => {
        const folderID = e.detail && e.detail.value;
        if (folderID) {
            const btn = document.querySelector(
                'button[hx-get][hx-target="#' + folderID + '"]');
            if (btn) {
                const li = btn.closest("li");
                if (li && refreshTreeNode) refreshTreeNode(li);
            } else {
                // The container has no expander any more (e.g. the folder
                // re-rendered as a plain leaf): clear it so stale children
                // do not linger.
                const container = document.getElementById(folderID);
                if (container) container.innerHTML = "";
            }
        }
    });
})();