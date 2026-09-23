// -----------------------------------------------------------------------------
// Right-click context menu for tree nodes carrying [data-tree-menu].
// -----------------------------------------------------------------------------
function initContextMenu() {
    const menu = document.getElementById("ctx-menu");
    if (!menu) return;

    let currentTableURL = null;
    let currentMenuKind = "";
    let currentPropsURL = "";
    let currentDataURL = "";

    function hideMenu() {
        menu.classList.add("hidden");
        currentTableURL = null;
        currentMenuKind = "";
        currentPropsURL = "";
        currentDataURL = "";
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

    // Label for the "Create" action of a category folder (e.g. "schema" ->
    // "Create Schema"). Only kinds wired into the DDL framework are offered.
    function createLabel(kind) {
        const labels = {
            schema: "Schema", table: "Table", sequence: "Sequence", view: "View",
            matview: "Materialized View", function: "Function", procedure: "Procedure",
            extension: "Extension", publication: "Publication",
            index: "Index", trigger: "Trigger",
        };
        return labels.hasOwnProperty(kind) ? "Create " + labels[kind] : "";
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
        currentDataURL = el.getAttribute("data-url") || "";

        const btn = el.querySelector("button[hx-get]");
        if (btn) currentTableURL = btn.getAttribute("hx-get");

        // URL to derive the DDL context (server/db/schema/table) from. Menu
        // leaves without a lazy-load button fall back to their properties URL
        // and finally to the explicit data-url.
        const nodeURL = currentTableURL || currentPropsURL || currentDataURL;

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
                        fetch("/api/servers/" + serverID + "/disconnect", { method: "POST" })
                            .then((r) => { if (!r.ok) throw r; })
                            .catch(() => {
                                if (window.showToast) window.showToast("Failed to disconnect the server.", "error");
                            });
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
                item.addEventListener("click", () => openCreateDDLDialog(entry[1], nodeURL, ""));
                sub.appendChild(item);
            });
            row.append(trigger, sub);
            menu.appendChild(row);
        }

        // Category folders (e.g. the Schemas or Indexes folder) carry a
        // Menu of the form "create-<kind>" and offer a single Create action.
        if (currentMenuKind.indexOf("create-") === 0) {
            const kind = currentMenuKind.slice("create-".length);
            const label = createLabel(kind);
            if (label) {
                menu.appendChild(divider());

                const createItem = menuItem(label, false);
                createItem.addEventListener("click", () => {
                    openCreateDDLDialog(kind, nodeURL, ddlFolderID(el, true));
                });
                menu.appendChild(createItem);
            }
        }

        // Dropable objects: server-level kinds (database, role, tablespace)
        // plus every Phase A database-scoped leaf/expander kind.
        const DROP_ITEMS = {
            database: "Drop Database", role: "Drop Role", tablespace: "Drop Tablespace",
            schema: "Drop Schema", view: "Drop View", "materialized-view": "Drop Materialized View",
            sequence: "Drop Sequence", function: "Drop Function", procedure: "Drop Procedure",
            extension: "Drop Extension", publication: "Drop Publication",
            index: "Drop Index", trigger: "Drop Trigger",
        };
        // Some tree-menu kinds use a different DDL route kind.
        const DDL_KINDS = { "materialized-view": "matview" };

        if (DROP_ITEMS.hasOwnProperty(currentMenuKind)) {
            menu.appendChild(divider());

            const dropItem = menuItem(DROP_ITEMS[currentMenuKind], true);
            dropItem.addEventListener("click", () => {
                const kind = DDL_KINDS[currentMenuKind] || currentMenuKind;
                const name = (el.dataset.name || "").trim() || objectNameFromURL(nodeURL);
                openDropDDLDialog(kind, nodeURL, name, ddlFolderID(el, false));
            });
            menu.appendChild(dropItem);
        }

        // Edit-in-place via ALTER: every DDL-managed kind opens a pre-filled
        // alter dialog (name, owner, schema, privilege flags, ...).
        const ALTER_ITEMS = {
            database: "Alter Database", role: "Alter Role", schema: "Alter Schema",
            tablespace: "Alter Tablespace", table: "Alter Table", view: "Alter View",
            "materialized-view": "Alter Materialized View", sequence: "Alter Sequence",
            function: "Alter Function", procedure: "Alter Procedure",
            extension: "Alter Extension", publication: "Alter Publication",
            index: "Alter Index", trigger: "Alter Trigger",
        };
        if (ALTER_ITEMS.hasOwnProperty(currentMenuKind)) {
            menu.appendChild(divider());

            const alterItem = menuItem(ALTER_ITEMS[currentMenuKind], false);
            alterItem.addEventListener("click", () => {
                const kind = DDL_KINDS[currentMenuKind] || currentMenuKind;
                const name = (el.dataset.name || "").trim() || objectNameFromURL(nodeURL);
                openAlterDDLDialog(kind, nodeURL, name, ddlFolderID(el, false));
            });
            menu.appendChild(alterItem);
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