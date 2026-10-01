// -----------------------------------------------------------------------------
// Multiple result sets: a multi-statement run comes back as stacked
// .result-set blocks (templates/partials/query_multi.html). This turns them
// into a tab strip with one tab per statement.
// -----------------------------------------------------------------------------
(function () {
    "use strict";

    // Default tab: the failing statement, else the last one returning rows,
    // else the last statement.
    function defaultIndex(sets) {
        var idx = -1;
        sets.forEach(function (s, i) {
            if (s.dataset.error === "true") idx = i;
        });
        if (idx >= 0) return idx;
        sets.forEach(function (s, i) {
            if (s.dataset.rows === "true") idx = i;
        });
        return idx >= 0 ? idx : sets.length - 1;
    }

    window.decorateMultiResults = function (grid) {
        var sets = Array.prototype.slice.call(grid.querySelectorAll(".result-set"));
        if (sets.length === 0) return;

        var strip = document.createElement("div");
        strip.className = "rs-tabs";
        var buttons = sets.map(function (set, i) {
            var b = document.createElement("button");
            b.type = "button";
            b.className = "rs-tab" + (set.dataset.error === "true" ? " rs-tab-error" : "");
            b.textContent = set.dataset.label || "Result " + (i + 1);
            b.addEventListener("click", function () { show(i); });
            strip.appendChild(b);
            return b;
        });

        function show(index) {
            sets.forEach(function (set, i) { set.hidden = i !== index; });
            buttons.forEach(function (b, i) { b.classList.toggle("rs-tab-active", i === index); });
        }

        grid.insertBefore(strip, grid.firstChild);
        show(defaultIndex(sets));
    };
})();
