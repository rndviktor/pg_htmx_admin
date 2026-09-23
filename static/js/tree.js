// -----------------------------------------------------------------------------
// Object explorer tree: lazy expansion, selection highlighting and restoring
// the expanded/saved state after a refresh.
// -----------------------------------------------------------------------------

// Collapses an expanded tree node. Returns true when the node was
// open (caller should cancel the pending htmx request), false when
// it should be fetched/expanded.
function toggleTreeChildren(containerId) {
    const el = document.getElementById(containerId);
    if (el && el.childElementCount > 0) {
        el.innerHTML = "";
        return true;
    }
    return false;
}

// Highlights the currently selected tree node (the one whose children
// container matches selectedTreeId).
function highlightTreeSelection() {
    document.querySelectorAll("#" + ID_TREE_ROOT + " button[hx-get]").forEach((b) => {
        const active = b.getAttribute("hx-target") === "#" + selectedTreeId;
        b.classList.toggle("text-blue-400", active);
        b.classList.toggle("font-semibold", active);
    });
}

// Fetches a tree fragment and injects it into a container, running htmx
// processing on the injected markup. Resolves true on success, false on any
// network/HTTP error so callers can react without try/catch.
function fetchInto(container, url) {
    return fetch(url)
        .then((r) => { if (!r.ok) throw r; return r.text(); })
        .then((html) => {
            container.innerHTML = html;
            if (window.htmx && htmx.process) htmx.process(container);
            return true;
        })
        .catch(() => false);
}

// Expands one lazily-loaded tree node by fetching its children exactly
// like htmx would (the button and its container are siblings).
// Resolves with true when the node was (or already is) expanded,
// false when its container does not exist yet (parent not loaded).
function expandTreeContainer(id) {
    const container = document.getElementById(id);
    if (container && container.childElementCount > 0) return Promise.resolve(true);
    if (!container) return Promise.resolve(false);
    const btn = container.previousElementSibling;
    if (!btn || !btn.hasAttribute("hx-get")) return Promise.resolve(false);
    return fetchInto(container, btn.getAttribute("hx-get"));
}

// Expands a list of tree-node container ids in dependency order: a parent
// must be open before its children can load, so ids that cannot be expanded
// yet are retried in further rounds until no node makes progress. Resolves
// once expansion has converged.
function expandRanges(ids) {
    const remaining = ids.slice();
    function step() {
        const pending = remaining.slice();
        remaining.length = 0;
        let progressed = false;
        let chain = Promise.resolve();
        pending.forEach((id) => {
            chain = chain.then(() =>
                expandTreeContainer(id).then((done) => {
                    if (done) progressed = true;
                    else remaining.push(id);
                }));
        });
        return chain.then(() => {
            if (progressed && remaining.length > 0) return step();
        });
    }
    return step();
}

// Switches the status dot of a server tree node (el is the server's <li>).
// state is "on" (green, connected), "off" (red, unavailable) or "gray"
// (deliberately disconnected, shown as a hollow gray circle). Styling is
// inline/class-based because the precompiled tailwind.css only carries the
// green and red fillers. The state is also mirrored on the <li> via
// data-tree-state so the context menu does not depend on the dot's visual.
function setServerDot(el, state) {
    el.setAttribute("data-tree-state", state);
    const dot = el.querySelector("button span.rounded-full");
    if (!dot) return;
    dot.classList.remove("bg-green-500", "bg-red-500");
    dot.style.border = "";
    dot.style.backgroundColor = "";
    if (state === "on") {
        dot.classList.add("bg-green-500");
    } else if (state === "off") {
        dot.classList.add("bg-red-500");
    } else {
        dot.style.border = "2px solid #94a3b8";
    }
}

// Refreshes one lazily-loaded tree node in place: re-fetches its children and
// then re-expands every descendant that was expanded before the refresh, so
// the previously visible subtree stays open and is re-populated. el is the
// node's <li> element carrying the expand button. For a server node the
// refresh goes through /reconnect, rendering the children on success and the
// "not available" hint otherwise. Resolves with true when the node was
// refreshed, false otherwise.
function refreshTreeNode(el) {
    const btn = el.querySelector("button[hx-get]");
    if (!btn) return Promise.resolve(false);
    const target = btn.getAttribute("hx-target");
    const container = target && document.querySelector(target);
    if (!container) return Promise.resolve(false);

    // Remember which descendant containers are currently expanded so they can
    // be re-fetched after the node's children are replaced in place.
    const expanded = [];
    container.querySelectorAll("[id]").forEach((c) => {
        if (c.childElementCount > 0) expanded.push(c.id);
    });

    // Server and database nodes refresh by reconnecting: /reconnect returns
    // the folders on success and the "not available" hint when the
    // connection cannot be made. Any other node just re-fetches its children.
    const kind = el.getAttribute("data-tree-menu") || "";
    const reconnects = kind === "server" || kind === "database";
    let url = btn.getAttribute("hx-get");
    if (reconnects) url = url.replace(/\/children$/, "/reconnect");

    return fetchInto(container, url).then((ok) => {
        // A reconnected node turns its dot green; a failed attempt leaves it
        // red (unavailable, and probed again at the next application start).
        if (reconnects && ok) {
            setServerDot(el, container.querySelector("ul button[hx-get]") ? "on" : "off");
        }
        return ok ? expandRanges(expanded).then(() => true) : false;
    });
}

// Re-expands the saved tree state after a page refresh. Parent nodes
// are fetched before children by retrying in rounds until no pending
// node can be expanded, then the saved selection is highlighted.
function applyTreeRestore() {
    if (treeRestoreApplied || !pendingTreeState) return;
    if (!document.getElementById(ID_SERVERS_GROUP)) return;
    treeRestoreApplied = true;
    const state = pendingTreeState;
    pendingTreeState = null;

    const remaining = (state.expanded_tree || []).slice();
    if (state.selected_tree && !remaining.includes(state.selected_tree)) {
        remaining.push(state.selected_tree);
    }

    expandRanges(remaining).then(() => {
        if (state.selected_tree) selectedTreeId = state.selected_tree;
        highlightTreeSelection();
        // Selecting a database (or anything below it) via the
        // restored workspace should show monitoring, just like
        // a real click.
        const btn = document.querySelector(
            '#' + ID_TREE_ROOT + ' button[hx-get][hx-target="#' + selectedTreeId + '"]');
        if (btn) updateDashboardForTreeSelection(btn);
    });
}

// Waits (polling) until the root tree node exists, i.e. the tree has
// been loaded, then invokes cb.
function waitForTreeRoot(cb, tries) {
    const el = document.getElementById(ID_SERVERS_GROUP);
    if (el) { cb(); return; }
    if (tries <= 0) return;
    setTimeout(() => waitForTreeRoot(cb, tries - 1), 100);
}

// Remembers the clicked tree node and persists the selection.
document.addEventListener("click", (e) => {
    const btn = e.target.closest("#" + ID_TREE_ROOT + " button[hx-get]");
    if (!btn) return;
    const target = btn.getAttribute("hx-target");
    if (!target) return;
    selectedTreeId = target.replace(/^#/, "");
    highlightTreeSelection();
    scheduleSave();
    updateDashboardForTreeSelection(btn);
});