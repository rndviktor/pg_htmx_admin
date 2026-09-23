// -----------------------------------------------------------------------------
// Object Properties tabs. "Properties" from the tree context menu opens a
// read-only tab whose content is fetched from the /properties endpoint. The
// panel has its own in-panel section tabs (General, Columns, ...) that toggle
// sections without leaving the tab.
//
// Properties tabs are ephemeral: they carry data-tab-kind="properties" so
// collectWorkspaceState() skips them and they never initialize a CodeMirror
// editor.
// -----------------------------------------------------------------------------

// Tab labels per context-menu kind (the kind drives the section set rendered
// server-side from the /properties endpoint).
const PROP_KIND_LABELS = {
    "table": "Table",
    "view": "View",
    "materialized-view": "Materialized View",
    "sequence": "Sequence",
    "function": "Function",
    "index": "Index",
    "trigger": "Trigger",
    "schema": "Schema",
    "database": "Database",
    "role": "Role",
    "tablespace": "Tablespace",
    "procedure": "Procedure",
    "extension": "Extension",
    "publication": "Publication",
};

// Opens a properties tab for any object carrying a /properties endpoint.
// propsURL is the full endpoint; kind is the context-menu kind used to name
// the tab. Properties tabs are ephemeral: they carry data-tab-kind=
// "properties" so collectWorkspaceState() skips them.
function openPropertiesTab(propsURL, kind) {
    const id = newTabId();
    tabMeta[id] = { name: (PROP_KIND_LABELS[kind] || "Object") + " Properties", path: "", dirty: false };

    // Create tab button (mirrors openTab, without the script-tab save state).
    const btn = document.createElement("button");
    btn.className = "tab-btn flex items-center space-x-2 px-4 py-2 font-medium rounded-t text-gray-400 hover:text-gray-200";
    btn.draggable = true;
    btn.dataset.tabId = id;
    btn.dataset.tabKind = "properties";
    btn.innerHTML = '<span>' + tabMeta[id].name + '</span>' +
        '<span class="text-xs text-gray-500 hover:text-gray-300" onclick="closeTab(\'' + id + '\', event)">\u2715</span>';
    btn.addEventListener("click", (e) => {
        if (e.target.textContent === "\u2715") return;
        switchTab(id);
    });
    const tabBar = document.getElementById(ID_TAB_BAR);
    const historyToggle = document.getElementById("history-toggle");
    if (historyToggle) tabBar.insertBefore(btn, historyToggle);
    else tabBar.appendChild(btn);

    // Create the content panel and fetch the properties fragment into it.
    const panel = document.createElement("div");
    panel.id = TAB_CONTENT_PREFIX + id;
    panel.className = "tab-panel flex-1 flex flex-col overflow-hidden bg-gray-900 text-gray-200";
    panel.style.display = "none";
    document.getElementById("tab-contents").appendChild(panel);

    switchTab(id);
    scheduleSave();

    return fetch(propsURL)
        .then((r) => { if (!r.ok) throw r; return r.text(); })
        .then((html) => { panel.innerHTML = html; })
        .catch(() => {
            panel.innerHTML = '<div class="flex-1 p-6 text-sm text-red-400">Failed to load properties.</div>';
        });
}

// Toggles the in-panel property sections (delegated; panels are injected after
// page load). Mirrors switchOutputTab styling.
document.addEventListener("click", (e) => {
    const tab = e.target.closest("[data-prop-tab]");
    if (!tab) return;
    const panel = tab.closest("[data-properties-panel]");
    if (!panel) return;
    const name = tab.dataset.propTab;

    panel.querySelectorAll("[data-prop-tab]").forEach((t) => {
        const active = t.dataset.propTab === name;
        t.classList.toggle("text-blue-400", active);
        t.classList.toggle("font-bold", active);
        t.classList.toggle("border-t-blue-500", active);
        t.classList.toggle("text-gray-400", !active);
        t.classList.toggle("font-normal", !active);
        t.classList.toggle("border-t-transparent", !active);
    });
    panel.querySelectorAll("[data-prop-pane]").forEach((p) => {
        p.classList.toggle("hidden", p.dataset.propPane !== name);
    });
});