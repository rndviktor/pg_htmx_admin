// -----------------------------------------------------------------------------
// Query tool transactions and notices. The server pins a tab's connection
// while it is inside a transaction (internal/web/session.go); this file drives
// the toolbar (BEGIN / COMMIT / ROLLBACK, Auto-commit), shows the transaction
// state badge and writes NOTICE/WARNING output to the Notifications tab.
// -----------------------------------------------------------------------------
(function () {
    "use strict";

    // tab id -> "active" | "aborted" while that tab has an open transaction.
    var openTx = {};
    var noticeCounts = {};

    function setState(panel, id, state) {
        if (state === "active" || state === "aborted") openTx[id] = state;
        else delete openTx[id];

        var badge = panel.querySelector(".tx-badge");
        if (!badge) return;
        badge.classList.toggle("hidden", !openTx[id]);
        badge.classList.toggle("tx-aborted", state === "aborted");
        badge.classList.toggle("tx-active", state === "active");
        badge.title = state === "aborted"
            ? "Transaction aborted by an error: ROLLBACK to continue"
            : "In a transaction: COMMIT or ROLLBACK to end it";
    }

    // Asks the server for the tab's transaction state (an in-memory lookup) and
    // shows any note about a transaction it ended (e.g. idle timeout).
    window.refreshTxState = function (panel) {
        var id = panelTabId(panel);
        fetch("/api/tx/status?tab_id=" + encodeURIComponent(id))
            .then(function (r) { return r.ok ? r.json() : null; })
            .then(function (s) {
                if (!s) return;
                setState(panel, id, s.state);
                if (s.note) logMessage(panel, "warn", s.note);
            })
            .catch(function () { /* the badge just stays as it was */ });
    };

    // Adds autocommit=off to a run's form body when the toggle is unchecked.
    window.txApplyAutocommit = function (panel, body) {
        var toggle = panel.querySelector(".autocommit-toggle");
        if (toggle && !toggle.checked) body.set("autocommit", "off");
    };

    // Runs a transaction control statement through the normal execute path.
    window.txCommand = function (btn, sql) {
        var panel = btn.closest("[id^='tab-content']");
        if (!panel) return;
        var form = queryForm(panel);
        var status = panel.querySelector(".query-status");
        var params = form && formConnectionParams(form);
        if (!params) {
            reportNotConnected(panel, status);
            return;
        }
        var body = new URLSearchParams({
            sql_query: sql,
            server_id: params.sid.value,
            db_name: params.db.value,
            tab_id: panelTabId(panel),
            page: "1",
            limit: QUERY_PAGE_LIMIT,
        });
        fetch("/api/execute-query", { method: "POST", body: body })
            .then(function (r) { if (!r.ok) throw r; return r.text(); })
            .then(function (html) {
                var tmp = document.createElement("div");
                tmp.innerHTML = html;
                var result = tmp.querySelector(".query-result");
                var data = (result && result.dataset) || {};
                var isError = data.isError === "true";
                var msg = data.message || sql;
                if (status) {
                    status.textContent = msg;
                    status.classList.toggle("text-red-400", isError);
                }
                logMessage(panel, isError ? "error" : "success", msg);
                applyNotices(panel, data.notices);
            })
            .catch(async function (r) {
                var msg = r && typeof r.text === "function" ? await r.text() : "Request failed";
                if (status) status.textContent = sql + " failed";
                logMessage(panel, "error", sql + " failed: " + msg);
            })
            .finally(function () { window.refreshTxState(panel); });
    };

    // Turning auto-commit back on while a transaction is open: ask whether to
    // commit it, and revert the toggle if the user declines.
    window.autoCommitChanged = function (cb) {
        var panel = cb.closest("[id^='tab-content']");
        if (panel && cb.checked && openTx[panelTabId(panel)]) {
            if (window.confirm("Commit the open transaction before turning auto-commit on?")) {
                window.txCommand(cb, "COMMIT");
            } else {
                cb.checked = false;
            }
        }
        syncAutoCommitHint(cb);
    };

    // The toggle is an icon, so its state is spelled out in the hint.
    function syncAutoCommitHint(cb) {
        var label = cb.closest(".ac-toggle");
        if (label) label.title = cb.checked
            ? "Auto-commit is on: each statement commits by itself. Click to turn it off and run statements inside a transaction until you COMMIT or ROLLBACK."
            : "Auto-commit is off: statements run inside a transaction until you COMMIT or ROLLBACK. Click to turn it back on.";
    }

    // Appends the NOTICE/WARNING messages of a run (JSON array string from the
    // result's data-notices attribute) to the Notifications panel.
    window.applyNotices = function (panel, json) {
        if (!json) return;
        var list;
        try { list = JSON.parse(json); } catch (e) { return; }
        if (!list || !list.length) return;

        var box = panel.querySelector("#notifications-panel > div");
        if (!box) return;
        var id = panelTabId(panel);
        if (!noticeCounts[id]) {
            box.textContent = "";
            box.className = "p-3 text-xs font-mono text-gray-300";
        }
        var ts = new Date().toLocaleTimeString();
        list.forEach(function (text) {
            var line = document.createElement("div");
            line.className = "py-0.5 whitespace-pre-wrap";
            line.textContent = "[" + ts + "] " + text;
            box.appendChild(line);
        });
        noticeCounts[id] = (noticeCounts[id] || 0) + list.length;

        var tab = panel.querySelector('[data-output-tab="notifications"]');
        if (tab) tab.textContent = "Notifications (" + noticeCounts[id] + ")";
    };

    // Tab closed: roll back its open transaction and release the connection.
    window.txCloseTab = function (id) {
        delete noticeCounts[id];
        if (!openTx[id]) return;
        delete openTx[id];
        fetch("/api/tx/close", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ tab_id: id }),
            keepalive: true,
        }).catch(function () {});
    };

    // Page refresh / close: connections do not survive it, so end the open
    // transactions now instead of leaving their locks to the idle timeout.
    window.addEventListener("pagehide", function () {
        Object.keys(openTx).forEach(function (id) {
            if (navigator.sendBeacon) {
                navigator.sendBeacon("/api/tx/close",
                    new Blob([JSON.stringify({ tab_id: id })], { type: "application/json" }));
            }
        });
    });
})();
