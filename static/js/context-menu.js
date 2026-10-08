// -----------------------------------------------------------------------------
// Right-click context menu for tree nodes carrying [data-tree-menu]. The menu
// itself is rendered by the server (GET /api/context-menu, internal/web/
// contextmenu.go); this file positions it and maps the data-act items that
// need script to the window functions that open tabs and panels. Items with
// hx-* attributes (modals, POSTs) are handled by htmx.
// -----------------------------------------------------------------------------
function initContextMenu() {
    const menu = document.getElementById("ctx-menu");
    if (!menu) return;
    let node = null; // the right-clicked tree <li>

    function hideMenu() {
        menu.classList.add("hidden");
    }

    function query(el) {
        const btn = el.querySelector("button[hx-get]");
        return new URLSearchParams({
            kind: el.getAttribute("data-tree-menu") || "",
            table_url: btn ? btn.getAttribute("hx-get") : "",
            props_url: el.getAttribute("data-props-url") || "",
            data_url: el.getAttribute("data-url") || "",
            name: (el.dataset.name || "").trim(),
            state: el.getAttribute("data-tree-state") || "",
            has_children: btn && btn.getAttribute("hx-target") ? "1" : "",
        });
    }

    function openMenu(x, y, el) {
        node = el;
        fetch("/api/context-menu?" + query(el))
            .then((r) => { if (!r.ok) throw r; return r.text(); })
            .then((html) => {
                menu.innerHTML = html;
                menu.classList.remove("hidden");
                if (window.htmx) htmx.process(menu);
                // Keep the menu inside the viewport.
                menu.style.left = "0px";
                menu.style.top = "0px";
                const rect = menu.getBoundingClientRect();
                menu.style.left = Math.max(0, Math.min(x, window.innerWidth - rect.width - 4)) + "px";
                menu.style.top = Math.max(0, Math.min(y, window.innerHeight - rect.height - 4)) + "px";
            })
            .catch(() => {});
    }

    // Collapses the node's children and greys its status dot, then asks the
    // server to disconnect it (the backend keeps it disconnected across reloads).
    function disconnect(url) {
        const btn = node.querySelector("button[hx-get]");
        const target = btn && btn.getAttribute("hx-target");
        const container = target && document.querySelector(target);
        if (container) container.innerHTML = "";
        setServerDot(node, "gray");
        htmx.ajax("POST", url, { swap: "none" });
    }

    const actions = {
        refresh: () => refreshTreeNode(node),
        "query-tool": (d, ctx) => {
            const conn = connectionFromTreeURL(ctx.tableUrl) || connectionFromTreeURL(ctx.propsUrl);
            if (conn) openQueryToolTab(conn.serverID, conn.serverName, conn.dbName);
            else openTab("Query Tool");
        },
        panel: (d, ctx) => openDDLPanel(d.action, d.kind, ctx.nodeUrl, d.name || "",
            d.folder ? ddlFolderID(node, d.folder === "create") : ""),
        maint: (d, ctx) => openMaintPanel(d.op, ctx.nodeUrl, Object.fromEntries(new URLSearchParams(d.extra || ""))),
        "drop-script": (d, ctx) => openDropScriptTab(d.kind, ctx.nodeUrl, d.name),
        properties: (d) => openPropertiesTab(d.url, d.kind),
        script: (d) => openScriptTab(d.label, d.url),
        disconnect: (d) => disconnect(d.url),
    };

    menu.addEventListener("click", (e) => {
        const item = e.target.closest("[data-act]");
        const root = menu.firstElementChild;
        if (item && root && actions[item.dataset.act]) actions[item.dataset.act](item.dataset, root.dataset);
    });

    document.addEventListener("contextmenu", (e) => {
        const item = e.target.closest("li[data-tree-menu]");
        if (!item) {
            hideMenu();
            return;
        }
        // The menu only belongs to the node itself. Child nodes (e.g. the
        // Columns folder or a column leaf under a table) live inside the
        // node's lazy-load container, so a right-click there must not
        // surface the parent table's menu.
        const childrenShell = item.querySelector(":scope > div");
        if (childrenShell && childrenShell.contains(e.target)) {
            hideMenu();
            return;
        }
        e.preventDefault();
        openMenu(e.clientX, e.clientY, item);
    });
    document.addEventListener("click", hideMenu);
    document.addEventListener("keydown", (e) => {
        if (e.key === "Escape") hideMenu();
    });
    window.addEventListener("blur", hideMenu);
}
document.addEventListener("DOMContentLoaded", initContextMenu);
