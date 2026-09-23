// -----------------------------------------------------------------------------
// Database Monitoring Dashboard – KPI cards, live charts, the SSE stream and
// the server-rendered sessions/locks/prepared-transactions tables. Polling
// only runs while the Dashboard tab is active (see updateMonitoringForActiveTab).
// -----------------------------------------------------------------------------
let monitoringTimer = null;
let monitoringBaseURL = null;
let monitoringInitialized = false;
let monitoringConn = null;
let monitoringSource = null;
let monitoringInterval = 5000;
let monitoringState = {
    blksHit: 0, blksRead: 0,
    xactCommit: 0, xactRollback: 0,
    inserts: 0, updates: 0, deletes: 0,
};
const chartSeries = {
    cacheHit: [], replication: [], tps: [], ins: [], upd: [], del: [], time: [],
};
let charts = {};

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB"];

// Shared Chart.js options; the series are plain lines reusing the arrays in
// chartSeries, which are updated in place so the chart instances can be kept.
const CHART_OPTIONS = {
    responsive: true,
    maintainAspectRatio: false,
    animation: false,
    plugins: { legend: { labels: { color: "#cbd5e1", boxWidth: 12, font: { size: 10 } } } },
    scales: {
        x: { ticks: { color: "#64748b", maxTicksLimit: 10 }, grid: { color: "#334155" } },
        y: { ticks: { color: "#64748b" }, grid: { color: "#1e293b", beginAtZero: true } },
    },
};

function formatBytes(bytes) {
    if (bytes === null || bytes === undefined || bytes < 0) return "–";
    return formatUnits(bytes, BYTE_UNITS);
}

function formatUnits(val, units) {
    let i = 0, n = val;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return n.toFixed(i === 0 ? 0 : 1) + " " + units[i];
}

// Creates a chart on first use, then just refreshes it. The datasets passed on
// later calls are ignored because their `data` arrays are chartSeries entries
// that are mutated in place.
function upsertChart(canvasId, datasets) {
    if (charts[canvasId]) {
        charts[canvasId].update("none");
        return;
    }
    const canvas = document.getElementById(canvasId);
    if (!canvas) return;
    charts[canvasId] = new Chart(canvas.getContext("2d"), {
        type: "line",
        data: { labels: chartSeries.time, datasets: datasets },
        options: CHART_OPTIONS,
    });
}

function destroyCharts() {
    Object.values(charts).forEach((c) => c.destroy());
    charts = {};
}

function renderMonitoringCharts() {
    const labels = chartSeries.time;

    // Cache hit ratio (line, %) — computed from cumulative hits/reads deltas.
    upsertChart("chart-cache-hit", [{
        label: "Cache Hit %",
        data: chartSeries.cacheHit,
        borderColor: "#3b82f6", backgroundColor: "rgba(59,130,246,0.1)",
        fill: true, tension: 0.3, pointRadius: 0,
    }]);

    // Replication lag (line, bytes).
    upsertChart("chart-replication", [{
        label: "Lag (bytes)",
        data: chartSeries.replication,
        borderColor: "#a855f7", backgroundColor: "rgba(168,85,247,0.1)",
        fill: true, tension: 0.3, pointRadius: 0,
    }]);

    // Transaction throughput (line, TPS). commits+rollbacks.
    upsertChart("chart-txn", [{
        label: "Commits/s",
        data: chartSeries.tps,
        borderColor: "#22c55e", backgroundColor: "rgba(34,197,94,0.1)",
        fill: true, tension: 0.3, pointRadius: 0,
    }]);

    // Row operations (line). inserts / updates / deletes per second.
    upsertChart("chart-rowops", [
        { label: "Inserts/s", data: chartSeries.ins, borderColor: "#3b82f6", tension: 0.3, pointRadius: 0 },
        { label: "Updates/s", data: chartSeries.upd, borderColor: "#eab308", tension: 0.3, pointRadius: 0 },
        { label: "Deletes/s", data: chartSeries.del, borderColor: "#ef4444", tension: 0.3, pointRadius: 0 },
    ]);
}

function updateKPIs(data) {
    document.getElementById("kpi-active-conn").textContent = data.activeConnections;
    document.getElementById("kpi-max-conn").querySelector(".kpi-max-val").textContent =
        (data.maxConnections || 0).toString();
    document.getElementById("kpi-db-size").textContent = formatBytes(data.dbSizeBytes);
    document.getElementById("kpi-active-tx").textContent = data.activeTx;
    document.getElementById("kpi-idle-tx").textContent = data.idleTx;
    document.getElementById("kpi-idle").textContent = data.idle;
    document.getElementById("kpi-blocked").textContent = data.blockedQueries;

    const lagEl = document.getElementById("kpi-repl-lag");
    if (data.hasReplication) {
        lagEl.textContent = formatUnits(data.replicationLagBytes, BYTE_UNITS);
        document.getElementById("kpi-repl-unit").textContent = "";
    } else {
        lagEl.textContent = "–";
        document.getElementById("kpi-repl-unit").textContent = "no replica";
    }
}

function refreshMonitoring() {
    if (!monitoringBaseURL) return;
    // Same-origin fetch with credentials so the signed auth cookie is
    // sent (matching htmx's default). Without it RequireAuth redirects
    // to /login and fetch reports "Failed to fetch".
    fetch(monitoringBaseURL, { credentials: "same-origin", headers: { "HX-Request": "true" } })
        .then(async (r) => {
            if (!r.ok) {
                let msg = "HTTP " + r.status;
                try { const t = await r.text(); if (t) msg += ": " + t; } catch (_) {}
                throw new Error(msg);
            }
            return r.json();
        })
        .then((data) => {
            updateKPIs(data);
            refreshAdminTables();

            // The first sample only establishes the baseline for the
            // cumulative counters (pg_stat_* hold totals since start),
            // so no time-series point is produced until the 2nd poll.
            if (!monitoringInitialized) {
                monitoringInitialized = true;
                monitoringState.blksHit = data.blksHit;
                monitoringState.blksRead = data.blksRead;
                monitoringState.xactCommit = data.xactCommit;
                monitoringState.xactRollback = data.xactRollback;
                monitoringState.inserts = data.inserts;
                monitoringState.updates = data.updates;
                monitoringState.deletes = data.deletes;
                renderMonitoringCharts();
                return;
            }

            // Cache hit ratio since last sample (%). Use the buffer delta.
            const blksHitDelta = data.blksHit - monitoringState.blksHit;
            const blksReadDelta = data.blksRead - monitoringState.blksRead;
            const total = blksHitDelta + blksReadDelta;
            monitoringState.blksHit = data.blksHit;
            monitoringState.blksRead = data.blksRead;
            const cacheHit = total > 0 ? (blksHitDelta / total) * 100 : 100;
            chartSeries.cacheHit.push(cacheHit);

            // TPS since last sample.
            const commitDelta = data.xactCommit - monitoringState.xactCommit;
            const rollbackDelta = data.xactRollback - monitoringState.xactRollback;
            monitoringState.xactCommit = data.xactCommit;
            monitoringState.xactRollback = data.xactRollback;
            chartSeries.tps.push(commitDelta + rollbackDelta);

            // Row op rates since last sample.
            chartSeries.ins.push(data.inserts - monitoringState.inserts);
            chartSeries.upd.push(data.updates - monitoringState.updates);
            chartSeries.del.push(data.deletes - monitoringState.deletes);
            monitoringState.inserts = data.inserts;
            monitoringState.updates = data.updates;
            monitoringState.deletes = data.deletes;

            // Replication lag.
            chartSeries.replication.push(
                data.hasReplication ? data.replicationLagBytes : 0);

            // Keep a rolling window of ~30 points.
            chartSeries.time.push(new Date().toLocaleTimeString());
            Object.values(chartSeries).forEach((series) => {
                if (series.length > 30) series.shift();
            });

            renderMonitoringCharts();
        })
        .catch((err) => {
            setMonitoringError(err && err.message ? err.message : String(err));
        });
}

function setMonitoringError(msg) {
    const el = document.getElementById("monitoring-error");
    if (!el) return;
    el.textContent = "Monitoring error: " + msg;
    el.classList.remove("hidden");
    clearTimeout(setMonitoringError._t);
    setMonitoringError._t = setTimeout(() => el.classList.add("hidden"), 8000);
}

// Switches the Dashboard tab button between "Dashboard" and "Monitor"
// (with an activity pulse icon) depending on whether monitoring is live.
function setDashboardTabLabel(monitoring) {
    const label = document.getElementById("tab-dashboard-label");
    const icon = document.getElementById("tab-dashboard-icon");
    if (monitoring) {
        // Monitoring mode: single icon only, hide text and spacing.
        if (icon) icon.textContent = "🟢";
        if (label) label.style.display = "none";
    } else {
        if (icon) icon.textContent = "📊";
        if (label) { label.style.display = ""; label.textContent = "Dashboard"; }
    }
}

// Opens the SSE KPI stream for the current connection so KPI cards update
// instantly between the 5s chart polls. Only runs while the Dashboard
// tab is active.
function openMonitoringKPIStream() {
    closeMonitoringKPIStream();
    if (!monitoringConn || activeTabId !== TAB_DASHBOARD) return;
    const src = new EventSource(
        "/api/servers/" + encodeURIComponent(monitoringConn.serverID) +
        "/databases/" + encodeURIComponent(monitoringConn.dbName) + "/monitoring/stream");
    src.onmessage = (e) => {
        let data;
        try { data = JSON.parse(e.data); } catch (_) { return; }
        updateKPIs(data);
    };
    monitoringSource = src;
}

function closeMonitoringKPIStream() {
    if (monitoringSource) { monitoringSource.close(); monitoringSource = null; }
}

function startMonitoring(serverID, dbName) {
    monitoringBaseURL = "/api/servers/" + serverID + "/databases/" + dbName + "/monitoring";
    monitoringConn = { serverID: serverID, dbName: dbName };
    setDashboardTabLabel(true);

    // Reset cumulative counters so the first sample establishes a clean
    // baseline and no historical spike appears. The arrays are emptied in
    // place so the live charts keep referencing them.
    destroyCharts();
    monitoringInitialized = false;
    monitoringState = { blksHit: 0, blksRead: 0, xactCommit: 0, xactRollback: 0, inserts: 0, updates: 0, deletes: 0 };
    Object.values(chartSeries).forEach((series) => { series.length = 0; });

    const defaultView = document.getElementById("dashboard-default");
    const monView = document.getElementById("dashboard-monitoring");
    if (defaultView) defaultView.classList.add("hidden");
    if (monView) monView.classList.remove("hidden");
    const label = document.getElementById("monitoring-conn-label");
    if (label) label.textContent = dbName + " (server " + serverID + ")";

    // Initial render, then poll charts while the Dashboard tab is active.
    renderMonitoringCharts();
    if (activeTabId !== TAB_DASHBOARD) return;
    pollMonitoring();
    openMonitoringKPIStream();
}

function pollMonitoring() {
    if (!monitoringBaseURL) return;
    refreshMonitoring();
    if (monitoringTimer) clearInterval(monitoringTimer);
    monitoringTimer = setInterval(refreshMonitoring, monitoringInterval);
}

// Sets the chart polling interval (ms) and restarts the timer live.
function setMonitoringInterval(val) {
    const ms = parseInt(val, 10);
    if (isNaN(ms) || ms < 1000) return;
    monitoringInterval = ms;
    if (monitoringTimer && monitoringBaseURL) pollMonitoring();
}

function stopMonitoring() {
    if (monitoringTimer) { clearInterval(monitoringTimer); monitoringTimer = null; }
    monitoringBaseURL = null;
    monitoringConn = null;
    closeMonitoringKPIStream();
    destroyCharts();
    setDashboardTabLabel(false);
    const defaultView = document.getElementById("dashboard-default");
    const monView = document.getElementById("dashboard-monitoring");
    if (defaultView) defaultView.classList.remove("hidden");
    if (monView) monView.classList.add("hidden");
}

// Decides, from the clicked tree node's URL, whether a database (or
// anything below it) is selected and the monitoring dashboard should
// be shown. Any node scoped under /databases/{db} shares that database
// connection, so schema, table and their category folders all qualify.
function updateDashboardForTreeSelection(btn) {
    const url = btn ? btn.getAttribute("hx-get") : null;
    const conn = connectionFromTreeURL(url);
    if (conn) {
        startMonitoring(conn.serverID, conn.dbName);
    } else {
        stopMonitoring();
    }
}

// -----------------------------------------------------------------------------
// Sessions / Locks / Prepared Transactions tables. The rows are rendered
// server-side (sessions_rows.html, locks_rows.html, prepared_rows.html) and
// swapped into the tbody, matching the app's HTMX server-rendering approach.
// -----------------------------------------------------------------------------

function adminParams(extra) {
    if (!monitoringConn) return null;
    const params = new URLSearchParams({
        server_id: monitoringConn.serverID,
        db_name: monitoringConn.dbName,
    });
    Object.entries(extra || {}).forEach(([key, value]) => {
        if (value) params.set(key, value);
    });
    return params.toString();
}

function sectionValue(selector) {
    const el = document.querySelector(selector);
    return el ? el.value : "";
}

function emptyRow(colspan, message) {
    const td = document.createElement("td");
    td.colSpan = colspan;
    td.className = "py-6 text-center text-gray-500 font-sans text-xs";
    td.textContent = message;
    const tr = document.createElement("tr");
    tr.appendChild(td);
    return tr;
}

function refreshSection(tbodyId, url, extra, colspan) {
    const tbody = document.getElementById(tbodyId);
    const query = adminParams(extra);
    if (!tbody || !query) return;
    fetch(url + "?" + query, { credentials: "same-origin" })
        .then((r) => { if (!r.ok) throw r; return r.text(); })
        .then((html) => { tbody.innerHTML = html; })
        .catch(async (err) => {
            const msg = err && err.text ? await err.text() : "request failed";
            tbody.replaceChildren(emptyRow(colspan, msg));
        });
}

function refreshSessions() {
    const active = document.querySelector('#sessions-section input[name="active_only"]');
    refreshSection("sessions-tbody", "/api/sessions", {
        active_only: active && active.checked ? "true" : "",
        search: sectionValue('#sessions-section input[name="session_search"]'),
    }, 12);
}

function refreshLocks() {
    refreshSection("locks-tbody", "/api/locks", {
        search: sectionValue('#locks-section input[name="locks_search"]'),
    }, 12);
}

function refreshPrepared() {
    refreshSection("transactions-tbody", "/api/prepared-transactions", {
        search: sectionValue('#prepared-section input[name="prepared_search"]'),
    }, 4);
}

function refreshAdminTables() {
    refreshSessions();
    refreshLocks();
    refreshPrepared();
}

function cancelSession(pid) {
    if (!confirm("Cancel query for PID " + pid + "?")) return;
    const query = adminParams();
    if (!query) return;
    fetch("/api/sessions/" + pid + "/cancel?" + query, { method: "POST", credentials: "same-origin" })
        .then((r) => { if (!r.ok) throw r; refreshSessions(); })
        .catch((err) => sessionActionError("Cancel query failed", err));
}

function terminateSession(pid) {
    if (!confirm("Terminate session PID " + pid + "?")) return;
    const query = adminParams();
    if (!query) return;
    fetch("/api/sessions/" + pid + "?" + query, { method: "DELETE", credentials: "same-origin" })
        .then((r) => { if (!r.ok) throw r; refreshSessions(); })
        .catch((err) => sessionActionError("Terminate session failed", err));
}

// Surface a failed cancel/terminate (the backend answers 500) via toast and
// a red row message so the user sees the request was not honoured.
function sessionActionError(prefix, err) {
    let detail = "";
    if (err && typeof err.text === "function") {
        err.text().then((t) => {
            const msg = prefix + (t ? ": " + t : ".");
            if (window.showToast) window.showToast(msg, "error");
        }).catch(() => {
            if (window.showToast) window.showToast(prefix + ".", "error");
        });
    } else {
        if (window.showToast) window.showToast(prefix + ".", "error");
    }
    refreshSessions();
}

// Search/filter inputs are bound once via delegation so they keep working
// after the auth shell swaps the dashboard markup in.
function initAdminFilters() {
    const timers = {};
    const debounced = (key, fn) => {
        clearTimeout(timers[key]);
        timers[key] = setTimeout(fn, 300);
    };
    document.addEventListener("input", (e) => {
        if (e.target.matches('#sessions-section input[name="session_search"]')) debounced("sessions", refreshSessions);
        else if (e.target.matches('#locks-section input[name="locks_search"]')) debounced("locks", refreshLocks);
        else if (e.target.matches('#prepared-section input[name="prepared_search"]')) debounced("prepared", refreshPrepared);
    });
    document.addEventListener("change", (e) => {
        if (e.target.matches('#sessions-section input[name="active_only"]')) refreshSessions();
    });
}
document.addEventListener("DOMContentLoaded", initAdminFilters);
