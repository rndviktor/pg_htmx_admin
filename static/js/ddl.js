// -----------------------------------------------------------------------------
// Create / Drop DDL dialogs (Phase 1: database, role, tablespace).
// -----------------------------------------------------------------------------
(function () {
    function closeDDL() {
        const container = document.getElementById("modal-container");
        if (container) container.innerHTML = "";
    }

    // The modal is always swapped into #modal-container (see
    // templates/partials/ddl_*.html) and closed with this helper.
    window.closeDDL = closeDDL;

    function parseServerID(url) {
        const m = /^\/api\/servers\/(\d+)/.exec(url || "");
        return m ? m[1] : "";
    }

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

    // Server-level objects refresh the server node itself: its folders
    // (databases / roles / tablespaces) re-fetch with new names and badges.
    window.openCreateDDLDialog = function (kind, nodeURL) {
        const serverID = parseServerID(nodeURL);
        if (!serverID) return;
        openModal("/api/ddl/" + kind + "/modal?server_id=" + serverID +
            "&folder_id=server-" + serverID + "&action=create");
    };

    window.openDropDDLDialog = function (kind, nodeURL, name) {
        const serverID = parseServerID(nodeURL);
        if (!serverID) return;
        openModal("/api/ddl/" + kind + "/modal?server_id=" + serverID +
            "&folder_id=server-" + serverID + "&action=drop&name=" +
            encodeURIComponent(name || ""));
    };

    // After a successful create/drop the backend responds with an
    // HX-Trigger: {"ddl-refresh": "<server folder id>"}. Re-fetch that tree
    // node (folders stay expanded, refreshed in place), then close the modal
    // after a short delay so the command tag stays visible.
    document.addEventListener("ddl-refresh", (e) => {
        const folderID = e.detail && e.detail.value;
        if (folderID) {
            const li = document.querySelector(
                'li[data-tree-menu] button[hx-get][hx-target="#' + folderID + '"]');
            if (li) {
                const node = li.closest("li");
                if (node) refreshTreeNode(node);
            }
        }
        setTimeout(closeDDL, 1600);
    });
})();