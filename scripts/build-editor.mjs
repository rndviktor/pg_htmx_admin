import { mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

// In the dev container the toolchain is installed outside the project
// (FRONTEND_DIR=/frontend) so no node_modules folder shows up on the host and
// a host-installed esbuild binary (another OS) is never picked up.
const frontendDir = process.env.FRONTEND_DIR;
let build;
try {
    ({ build } = createRequire(frontendDir ? resolve(frontendDir, "x.js") : import.meta.url)("esbuild"));
} catch {
    console.error(frontendDir
        ? "esbuild not found in " + frontendDir + ": rebuild the dev image (docker compose -f docker/docker-compose.dev.yml up --build)."
        : "esbuild not found: run `npm install` first.");
    process.exit(1);
}

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

await build({
    entryPoints: [resolve(root, "static/js/sql-editor.js")],
    bundle: true,
    format: "esm",
    minify: true,
    target: "es2022",
    outfile: resolve(root, "static/codemirror.bundle.js"),
    sourcemap: false,
    logLevel: "info",
});

const vendorDir = resolve(root, "static/vendor");
mkdirSync(vendorDir, { recursive: true });

await build({
    entryPoints: [resolve(root, "static/js/chart-entry.js")],
    bundle: true,
    format: "esm",
    minify: true,
    target: "es2022",
    outfile: resolve(vendorDir, "chart.bundle.js"),
    sourcemap: false,
    logLevel: "info",
});
console.log("built tree-shaken chart.js -> static/vendor/chart.bundle.js");