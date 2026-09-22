// -----------------------------------------------------------------------------
// Create / Drop DDL dialogs for server- and database-scoped object kinds.
// -----------------------------------------------------------------------------
(function () {
    function closeDDL() {
        const container = document.getElementById("modal-container");
        if (container) container.innerHTML = "";
    }

    // The modal is always swapped into #modal-container (see
    // templates/partials/ddl_*.html) and closed with this helper.
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
            .catch(() => {});
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

    // Builds the modal URL from the node's URL context (server/db/schema/table)
    // plus the refresh target. Server-scoped kinds only need the server id.
    function modalQuery(action, kind, nodeURL, folderID, name) {
        const ctx = parseObjectContext(nodeURL);
        const qs = new URLSearchParams();
        if (ctx.serverID) qs.set("server_id", ctx.serverID);
        if (ctx.dbName) qs.set("db", ctx.dbName);
        if (ctx.schema) qs.set("schema", ctx.schema);
        if (ctx.table) qs.set("table", ctx.table);
        qs.set("folder_id", folderID || (ctx.serverID ? "server-" + ctx.serverID : ""));
        qs.set("action", action);
        if (name) qs.set("name", name);
        return qs;
    }

    window.openCreateDDLDialog = function (kind, nodeURL, folderID) {
        const qs = modalQuery("create", kind, nodeURL, folderID);
        if (!qs.get("server_id")) return;
        openModal("/api/ddl/" + kind + "/modal?" + qs.toString());
    };

    window.openDropDDLDialog = function (kind, nodeURL, name, folderID) {
        const qs = modalQuery("drop", kind, nodeURL, folderID, name || "");
        if (!qs.get("server_id")) return;
        openModal("/api/ddl/" + kind + "/modal?" + qs.toString());
    };

    // After a successful create/drop the backend responds with an
    // HX-Trigger: {"ddl-refresh": "<tree container id>"}. Re-fetch that tree
    // container's node (folders stay expanded, refreshed in place), then close
    // the modal after a short delay so the command tag stays visible.
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
        setTimeout(closeDDL, 1600);
    });
})();