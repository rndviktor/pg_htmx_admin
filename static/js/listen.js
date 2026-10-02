// -----------------------------------------------------------------------------
// LISTEN / NOTIFY streaming. After a run that left the tab listening
// (data-listening on the result), subscribe to the tab's server-sent events
// (internal/web/listen.go) and append each notification to the Notifications
// tab until the server reports the listener closed.
// -----------------------------------------------------------------------------
(function () {
    "use strict";

    var streams = {}; // tab id -> EventSource

    function stop(id) {
        var src = streams[id];
        if (src) src.close();
        delete streams[id];
    }

    window.startListenStream = function (panel) {
        var id = panelTabId(panel);
        if (streams[id]) return;
        var src = new EventSource("/api/listen/stream?tab_id=" + encodeURIComponent(id));
        streams[id] = src;

        src.onmessage = function (e) {
            var ev;
            try { ev = JSON.parse(e.data); } catch (err) { return; }
            var text = "NOTIFY " + ev.channel + (ev.payload ? ": " + ev.payload : "") + "  (from pid " + ev.pid + ")";
            applyNotices(panel, JSON.stringify([text]));
        };
        src.addEventListener("closed", function () {
            stop(id);
            logMessage(panel, "info", "Stopped listening: the listener was closed.");
        });
        src.onerror = function () {
            // A refused stream (404: nothing listening any more) is final; a
            // dropped one reconnects by itself.
            if (src.readyState === EventSource.CLOSED) delete streams[id];
        };
    };

    // Tab closed: stop the stream and drop the server-side listener connection.
    window.listenCloseTab = function (id) {
        if (!streams[id]) return;
        stop(id);
        fetch("/api/listen/close", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ tab_id: id }),
            keepalive: true,
        }).catch(function () {});
    };

    window.addEventListener("pagehide", function () {
        Object.keys(streams).forEach(function (id) {
            if (navigator.sendBeacon) {
                navigator.sendBeacon("/api/listen/close",
                    new Blob([JSON.stringify({ tab_id: id })], { type: "application/json" }));
            }
        });
    });
})();
