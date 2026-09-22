// -----------------------------------------------------------------------------
// Right-click context menu for tree nodes carrying [data-tree-menu].
// -----------------------------------------------------------------------------
function initContextMenu() {
    const menu = document.getElementById("ctx-menu");
    if (!menu) return;

    let currentTableURL = null;
    let currentMenuKind = "";
    let currentPropsURL = "";

    function hideMenu() {
        menu.classList.add("hidden");
        currentTableURL = null;
        currentMenuKind = "";
        currentPropsURL = "";
    }

    function menuItem(label, danger) {
        const b = document.createElement("button");
        b.className = "block w-full text-left px-3 py-1.5 hover:bg-gray-700 " +
            (danger ? "text-red-400 hover:text-red-300" : "");
        b.textContent = label;
        return b;
    }

    function divider() {
        const d = document.createElement("div");
        d.className = "my-1 border-t border-gray-700";
        return d;
    }

    // Builds the hover-revealed "Scripts" submenu used by table/view and
    // materialized-view nodes. entries is [{label, onPick}].
    function scriptsSubmenu(entries) {
        const row = document.createElement("div");
        row.className = "relative group";
        const trigger = document.createElement("button");
        trigger.className = "w-full text-left px-3 py-1.5 hover:bg-gray-700 flex items-center justify-between";
        trigger.innerHTML = '<span>Scripts</span><span class="text-xs text-gray-500">\u25B8</span>';
        const sub = document.createElement("div");
        sub.className = "absolute left-full top-0 hidden group-hover:block bg-gray-800 border border-gray-600 rounded shadow-xl py-1 min-w-[12rem]";
        entries.forEach((entry) => {
            const item = menuItem(entry.label, false);
            item.addEventListener("click", entry.onPick);
            sub.appendChild(item);
        });
        row.append(trigger, sub);
        return row;
    }

    function openMenu(x, y, el) {
        menu.innerHTML = "";
        currentTableURL = null;
        currentMenuKind = el.getAttribute("data-tree-menu") || "";
        currentPropsURL = el.getAttribute("data-props-url") || "";

        const btn = el.querySelector("button[hx-get]");
        if (btn) currentTableURL = btn.getAttribute("hx-get");

        // "Refresh" re-fetches the node's children. The node stays expanded and
        // previously expanded descendants are re-populated as well.
        const refreshItem = menuItem("Refresh", false);
        refreshItem.addEventListener("click", () => {
            if (!btn || !btn.getAttribute("hx-target")) {
                openTab("Refresh");
            } else {
                refreshTreeNode(el);
            }
        });
        menu.appendChild(refreshItem);

        // "Query Tool" opens an empty script tab connected to the
        // node's database (valid for database, schema and table). Leaf
        // objects (materialized views, sequences, functions, indexes,
        // triggers) carry no children button, so their /properties URL —
        // which is still database-scoped — supplies the connection instead.
        const qtItem = menuItem("Query Tool", false);
        qtItem.addEventListener("click", () => {
            const conn = connectionFromTreeURL(currentTableURL) || connectionFromTreeURL(currentPropsURL);
            if (conn) {
                openQueryToolTab(conn.serverID, conn.serverName, conn.dbName);
            } else {
                openTab("Query Tool");
            }
        });
        menu.appendChild(qtItem);

        // Server nodes react to their state: connected (green) offers
        // "Disconnect from server", disconnected-by-user (gray) offers
        // "Connect", and unavailable (red) offers "Try to reconnect".
        // The two reconnecting actions go through /reconnect and turn the
        // dot green on success.
        if (currentMenuKind === "server") {
            menu.appendChild(divider());

            const state = el.getAttribute("data-tree-state");

            if (state === "on") {
                const discItem = menuItem("Disconnect from server", true);
                discItem.addEventListener("click", () => {
                    const serverID = parseServerDBURL(currentTableURL).serverID;
                    const target = btn && btn.getAttribute("hx-target");
                    if (target) {
                        const container = document.querySelector(target);
                        if (container) container.innerHTML = "";
                    }
                    // Gray immediately; the backend keeps it disconnected so a page
                    // refresh does not re-connect the server.
                    setServerDot(el, "gray");
                    if (serverID) {
                        fetch("/api/servers/" + serverID + "/disconnect", { method: "POST" });
                    }
                });
                menu.appendChild(discItem);
            } else {
                // Gray (disconnected by me) -> Connect, red (unavailable) -> retry.
                const item = menuItem(state === "off" ? "Try to reconnect" : "Connect", false);
                item.addEventListener("click", () => refreshTreeNode(el));
                menu.appendChild(item);
            }
        }

        // Servers can create cluster-level objects (database, role,
        // tablespace). All generate DDL and run it on the maintenance db.
        if (currentMenuKind === "server") {
            menu.appendChild(divider());

            const row = document.createElement("div");
            row.className = "relative group";
            const trigger = document.createElement("button");
            trigger.className = "w-full text-left px-3 py-1.5 hover:bg-gray-700 flex items-center justify-between";
            trigger.innerHTML = '<span>Create</span><span class="text-xs text-gray-500">\u25B8</span>';
            const sub = document.createElement("div");
            sub.className = "absolute left-full top-0 hidden group-hover:block bg-gray-800 border border-gray-600 rounded shadow-xl py-1 min-w-[10rem]";
            [["Database", "database"], ["Role", "role"], ["Tablespace", "tablespace"]].forEach((entry) => {
                const item = menuItem(entry[0], false);
                item.addEventListener("click", () => openCreateDDLDialog(entry[1], currentTableURL));
                sub.appendChild(item);
            });
            row.append(trigger, sub);
            menu.appendChild(row);
        }

        // Server-level objects (database, role, tablespace) can be dropped.
        if (currentMenuKind === "database" || currentMenuKind === "role" || currentMenuKind === "tablespace") {
            menu.appendChild(divider());

            const label = currentMenuKind === "database" ? "Drop Database"
                : currentMenuKind === "role" ? "Drop Role" : "Drop Tablespace";
            const dropItem = menuItem(label, true);
            dropItem.addEventListener("click", () => {
                openDropDDLDialog(currentMenuKind, currentTableURL, (el.dataset.name || "").trim());
            });
            menu.appendChild(dropItem);
        }

        // "Properties": tables/views derive their /properties endpoint from
        // the children URL; leaf objects (materialized views, sequences,
        // functions, indexes, triggers, schemas) carry it directly as
        // data-props-url.
        const propsURL = currentMenuKind === "table" || currentMenuKind === "view"
            ? (currentTableURL ? currentTableURL.replace(/\/children$/, "") + "/properties" : "")
            : currentPropsURL;
        if (propsURL) {
            menu.appendChild(divider());
            const propsItem = menuItem("Properties", false);
            propsItem.addEventListener("click", () => openPropertiesTab(propsURL, currentMenuKind));
            menu.appendChild(propsItem);
        }

        // "Scripts" submenu entries per node kind. Tables and views fetch the
        // script from their children URL; materialized views serve a read-only
        // SELECT script from the same columns-script endpoint.
        const scriptEntries = [];
        if (currentMenuKind === "table") {
            ["CREATE Script", "DELETE Script", "INSERT Script", "SELECT Script", "UPDATE Script"]
                .forEach((label) => scriptEntries.push({ label, onPick: () => openScriptTab(label, currentTableURL) }));
        } else if (currentMenuKind === "view") {
            ["CREATE Script", "INSERT Script", "SELECT Script"]
                .forEach((label) => scriptEntries.push({ label, onPick: () => openScriptTab(label, currentTableURL) }));
        } else if (currentMenuKind === "materialized-view") {
            scriptEntries.push({
                label: "SELECT Script",
                onPick: () => openScriptTab("SELECT Script", currentPropsURL.replace(/\/properties$/, "")),
            });
        }
        if (scriptEntries.length > 0) {
            menu.appendChild(divider());
            menu.appendChild(scriptsSubmenu(scriptEntries));
        }

        menu.classList.remove("hidden");

        // Keep the menu inside the viewport.
        menu.style.left = "0px";
        menu.style.top = "0px";
        const rect = menu.getBoundingClientRect();
        menu.style.left = Math.max(0, Math.min(x, window.innerWidth - rect.width - 4)) + "px";
        menu.style.top = Math.max(0, Math.min(y, window.innerHeight - rect.height - 4)) + "px";
    }

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