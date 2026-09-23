// -----------------------------------------------------------------------------
// Workspace persistence – save UI state to sqlite and restore it after refresh,
// and reset everything when the auth shell swaps between login <-> dashboard.
// -----------------------------------------------------------------------------
let workspaceRestored = false;
let restoringWorkspace = false;
let saveTimer = null;
let selectedTreeId = ID_SERVERS_GROUP;
let pendingTreeState = null;
let treeRestoreApplied = false;

function collectWorkspaceState() {
    const tabs = [];
    document.querySelectorAll(".tab-btn").forEach((btn) => {
        if (btn.dataset.tabId === TAB_DASHBOARD) return;
        // Properties tabs are ephemeral read-only views; they are never
        // restored after a refresh.
        if (btn.dataset.tabKind === "properties") return;
        const panel = document.getElementById(TAB_CONTENT_PREFIX + btn.dataset.tabId);
        if (!panel) return;
        const params = formConnectionParams(queryForm(panel));
        const titleEl = btn.querySelector("span");
        const editorValue = window.SqlEditor && queryForm(panel)
            ? window.SqlEditor.value(queryForm(panel))
            : "";
        tabs.push({
            id: btn.dataset.tabId,
            title: titleEl ? titleEl.textContent : "Query",
            server_id: params ? parseInt(params.sid.value, 10) || 0 : 0,
            db_name: params ? params.db.value : "",
            query: editorValue,
            path: (tabMeta[btn.dataset.tabId] && tabMeta[btn.dataset.tabId].path) || "",
            tab_order: tabs.length,
        });
    });

    const active = Array.from(document.querySelectorAll(".tab-btn"))
        .find((b) => b.classList.contains("bg-gray-900"));
    const sidebar = document.getElementById("sidebar");
    const treeRoot = document.getElementById(ID_TREE_ROOT);
    const expandedTree = [];
    if (treeRoot) {
        treeRoot.querySelectorAll("div[id]").forEach((d) => {
            if (d.childElementCount > 0) expandedTree.push(d.id);
        });
    }

    return {
        active_tab_id: active ? active.dataset.tabId : TAB_DASHBOARD,
        layout: {
            sidebar_width: sidebar ? String(parseInt(sidebar.style.width, 10) || 256) : "",
            selected_tree: selectedTreeId,
            expanded_tree: expandedTree,
        },
        tabs: tabs,
    };
}

function saveWorkspace(beacon) {
    const body = JSON.stringify(collectWorkspaceState());
    if (beacon && navigator.sendBeacon) {
        navigator.sendBeacon(
            "/api/workspace",
            new Blob([body], { type: "application/json" }));
        return;
    }
    fetch("/api/workspace", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: body,
    }).catch(() => {});
}

function scheduleSave() {
    if (!workspaceRestored || restoringWorkspace) return;
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => { saveTimer = null; saveWorkspace(false); }, 500);
}

function restoreWorkspace() {
    if (workspaceRestored || !document.getElementById(ID_TAB_BAR)) return;
    workspaceRestored = true;
    restoringWorkspace = true;

    fetch("/api/workspace")
        .then((r) => r.json())
        .then(async (state) => {
            const layout = state.layout || {};
            if (layout.sidebar_width) {
                const sidebar = document.getElementById("sidebar");
                if (sidebar) sidebar.style.width = layout.sidebar_width + "px";
            }

            pendingTreeState = {
                selected_tree: layout.selected_tree || "",
                expanded_tree: layout.expanded_tree || [],
            };
            waitForTreeRoot(() => applyTreeRestore(), 50);

            const openings = (state.tabs || []).map((tab) =>
                openTab(tab.title, tab.query, tab.server_id, tab.server_name, tab.db_name, tab.id,
                    { path: tab.path || "", pathExists: !!tab.path_exists }));

            await Promise.all(openings);

            const target = document.querySelector(
                '.tab-btn[data-tab-id="' + state.active_tab_id + '"]');
            switchTab(target ? state.active_tab_id : TAB_DASHBOARD);
        })
        .catch(() => {
            if (window.showToast) window.showToast("Could not restore your previous session.", "info");
            switchTab(TAB_DASHBOARD);
        })
        .finally(() => { restoringWorkspace = false; });
}

// The auth shell is swapped in place (#app-root, outerHTML), so the
// DOMContentLoaded restore only runs on the first visit. Watch for the
// login <-> dashboard transitions and re-run / reset accordingly.
function observeAuthLayout() {
    document.addEventListener("htmx:afterSwap", (e) => {
        const elt = e.detail && e.detail.elt;
        if (!elt || elt.id !== "app-root") return;
        if (document.getElementById(ID_TAB_BAR)) {
            if (!workspaceRestored) restoreWorkspace();
            initTabDrag();
            initTabScroll();
        } else if (workspaceRestored) {
            stopMonitoring();
            workspaceRestored = false;
            restoringWorkspace = false;
            treeRestoreApplied = false;
            pendingTreeState = null;
            selectedTreeId = ID_SERVERS_GROUP;
            tabCounter = 0;
            if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
        }
    });
}

document.addEventListener("DOMContentLoaded", restoreWorkspace);
window.addEventListener("pagehide", () => {
    if (workspaceRestored) saveWorkspace(true);
});
document.addEventListener("DOMContentLoaded", observeAuthLayout);