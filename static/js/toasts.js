// -----------------------------------------------------------------------------
// Toast notifications – a pgAdmin-style bottom-left stack that surfaces
// server errors to the user. window.showToast(message, type) is the public
// API used by the fetch()-based handlers; htmx request failures (non-2xx
// responses and transport errors) are wired up globally so any endpoint that
// answers with an error status shows a toast automatically.
// -----------------------------------------------------------------------------
(function () {
    const TOAST_DURATION = 10000;
    const MAX_TOASTS = 5;

    let container = null;

    function ensureContainer() {
        if (container) return container;

        const style = document.createElement("style");
        style.textContent = `
            #pg-toasts {
                position: fixed; bottom: 16px; left: 16px; z-index: 9999;
                display: flex; flex-direction: column; gap: 8px; max-width: 360px;
            }
            .pg-toast {
                display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px;
                border-radius: 8px; font-size: 12px; font-family: inherit; color: #e5e7eb;
                background: #1f2937; border: 1px solid #4b5563;
                box-shadow: 0 10px 25px rgba(0, 0, 0, 0.5);
                animation: pg-toast-in 0.15s ease-out;
            }
            .pg-toast-error { background: #2a1517; border-color: #7f1d1d; }
            .pg-toast-warning { background: #2a2415; border-color: #78350f; }
            .pg-toast-success { background: #12251a; border-color: #14532d; }
            .pg-toast-info { background: #16232e; border-color: #1e3a8a; }
            .pg-toast-icon { flex: 0 0 auto; font-size: 14px; line-height: 1.2; }
            .pg-toast-body { flex: 1 1 auto; min-width: 0; word-break: break-word; white-space: pre-wrap; }
            .pg-toast-close { flex: 0 0 auto; cursor: pointer; color: #9ca3af; font-size: 12px;
                line-height: 1.2; border: 0; background: none; padding: 0 2px; }
            .pg-toast-close:hover { color: #fff; }
            @keyframes pg-toast-in {
                from { opacity: 0; transform: translateY(8px); }
                to { opacity: 1; transform: none; }
            }
            @media (prefers-reduced-motion: reduce) { .pg-toast { animation: none; } }
        `;
        document.head.appendChild(style);

        container = document.createElement("div");
        container.id = "pg-toasts";
        container.setAttribute("role", "status");
        container.setAttribute("aria-live", "polite");
        document.body.appendChild(container);
        return container;
    }

    const ICONS = { error: "❌", warning: "⚠️", success: "✅", info: "ℹ️" };

    function dismiss(el) {
        if (el.timer) clearTimeout(el.timer);
        if (el.parentNode) el.parentNode.removeChild(el);
    }

    function showToast(message, type) {
        const kind = typeof type === "string" && ICONS[type] ? type : "info";
        const text = message == null || message === "" ? "Unknown error" : String(message);
        const stack = ensureContainer();

        while (stack.children.length >= MAX_TOASTS) {
            dismiss(stack.firstChild);
        }

        const el = document.createElement("div");
        el.className = "pg-toast pg-toast-" + kind;

        const icon = document.createElement("span");
        icon.className = "pg-toast-icon";
        icon.textContent = ICONS[kind];

        const body = document.createElement("span");
        body.className = "pg-toast-body";
        body.textContent = text;

        const close = document.createElement("button");
        close.className = "pg-toast-close";
        close.innerHTML = "&times;";
        close.title = "Dismiss";
        close.addEventListener("click", function () { dismiss(el); });

        el.appendChild(icon);
        el.appendChild(body);
        el.appendChild(close);
        stack.appendChild(el);

        el.timer = setTimeout(function () { dismiss(el); }, TOAST_DURATION);
        return el;
    }

    window.showToast = showToast;

    // Cap long error bodies: keep a few meaningful lines so the toast matches
    // what the server logged without flooding the UI.
    function tidyErrorBody(text) {
        const cleaned = String(text || "").trim();
        if (!cleaned) return "";
        const lines = cleaned.split(/\r?\n/).map(function (l) { return l.trim(); }).filter(Boolean);
        if (lines.length <= 6) return cleaned;
        return lines.slice(0, 6).join(" ") + " …";
    }

    // Global htmx wiring: any htmx request that fails gets surfaced as a
    // toast. HX-Redirect responses (session expiry, logout) are skipped
    // because the page navigates away anyway. Full HTML error pages are
    // collapsed to a status line so the user is not shown markup.
    function wireHtmx() {
        if (!window.htmx) return;

        document.body.addEventListener("htmx:responseError", function (evt) {
            const xhr = evt.detail && evt.detail.xhr;
            if (!xhr) return;
            if (xhr.getResponseHeader("HX-Redirect")) return;
            const status = xhr.status || "";
            const body = xhr.responseText || xhr.statusText || "Request failed";
            const isHtml = /<[a-z][\s\S]*>/i.test(body.trim());
            const msg = isHtml ? "HTTP " + status + " – " + (xhr.statusText || "error") : tidyErrorBody(body);
            showToast(msg, "error");
        });

        document.body.addEventListener("htmx:sendError", function () {
            showToast("Network error: could not reach the server.", "error");
        });

        document.body.addEventListener("htmx:timeoutError", function () {
            showToast("Request timed out.", "error");
        });
    }

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", wireHtmx);
    } else {
        wireHtmx();
    }
})();