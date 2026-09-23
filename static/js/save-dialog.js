// -----------------------------------------------------------------------------
// Save Script dialog – self-contained component.
//
// The markup lives in the server partial (templates/partials/save_script_modal
// .html, served at GET /api/save-script/modal); this module injects it into
// #modal-container, prefills the path with the server process folder, and
// hands the chosen path back to the caller via SaveDialog.open({onSave}).
// Exposes window.SaveDialog and the global confirmSaveDialog/closeSaveDialog
// that the partial's inline handlers call.
// -----------------------------------------------------------------------------

(function () {
    const MODAL_URL = "/api/save-script/modal";
    const DEFAULT_PATH_URL = "/api/save-default-path";

    let dialog = null; // {id, name, onSave}

    // SaveDialog.open({id, name, onSave}) shows the dialog. onSave(path)
    // runs on confirm and is responsible for persisting the query; the
    // dialog closes immediately and lets the caller report the outcome in
    // its own UI (e.g. the tab panel's Messages box).
    window.SaveDialog = {
        open(opts) {
            dialog = {
                id: opts && opts.id,
                name: (opts && opts.name) || "script.sql",
                onSave: opts && opts.onSave,
            };
            const container = document.getElementById("modal-container");
            if (!container) return;

            fetch(MODAL_URL)
                .then((r) => { if (!r.ok) throw r; return r.text(); })
                .then((html) => {
                    container.innerHTML = html;
                    const input = container.querySelector("input[name='save-path']");
                    if (!input) return;
                    prefetchDefaultPath(input, dialog.name);
                })
                .catch(async (httpErr) => {
                    if (httpErr && httpErr.status === 401) return;
                    let detail = httpErr ? (httpErr.statusText || "HTTP " + httpErr.status) : "";
                    if (httpErr && typeof httpErr.text === "function") {
                        try { detail = (await httpErr.text()) || detail; } catch (e) { /* ignore */ }
                    }
                    const msg = "Failed to open the Save window" + (detail ? ": " + detail : ".");
                    if (window.showToast) window.showToast(msg, "error");
                });
        },
    };

    function prefetchDefaultPath(input, name) {
        fetch(DEFAULT_PATH_URL)
            .then((r) => { if (!r.ok) throw r; return r.json(); })
            .then((d) => {
                const dir = String(d.cwd || "").replace(/[\\/]+$/, "");
                input.value = dir ? dir + "\\" + name : name;
            })
            .catch(() => { input.value = name; });
    }

    // Global handlers referenced by the partial's inline onclicks.
    window.confirmSaveDialog = function confirmSaveDialog() {
        const input = document.querySelector("#modal-container input[name='save-path']");
        const err = document.querySelector("#modal-container .save-dialog-error");
        if (!input || !input.value.trim()) {
            if (err) { err.textContent = "Path is required."; err.classList.remove("hidden"); }
            return;
        }
        const path = input.value.trim();
        if (dialog && dialog.onSave) dialog.onSave(path);
        closeSaveDialog();
    };

    window.closeSaveDialog = function closeSaveDialog() {
        dialog = null;
        const container = document.getElementById("modal-container");
        if (container) container.innerHTML = "";
    };
})();