---
name: cartograph
description: Workflows for the Pg HTMX Admin repo (Go + HTMX + CodeMirror + Chart.js). Use when working on this project — the DDL framework (create/drop/alter modals, server-generated SQL, preview-then-run), the feature-gap roadmap (docs/pgadmin4-feature-gap.md), template/partial conventions, rebuilding static bundles (CodeMirror / chart.js tree-shaking via esbuild), the Docker production build (docker/Dockerfile), the hot-reload dev container (docker/Dockerfile_dev + docker/docker-compose.dev.yml + docker/air.toml), port config (PORT/.env), or verifying the containerized app.
---

# Pg HTMX Admin — dev/build workflows

Go + HTMX app (PostgreSQL admin UI). Entry point `cmd/server/main.go`; `templates/` and `static/` are embedded into the binary via `//go:embed` (`static.go`), so any change there requires a Go rebuild to take effect.

## Behavioral guidelines

The repo-level behavioral rules live in `.claude/CLAUDE.md` (read it): idiomatic Go/JS/HTMX, small modular functions, keep the front-end bundle small, and never silently delete/replace legacy code — get explicit confirmation first.

## Roadmap & documentation

- `docs/pgadmin4-feature-gap.md` compares this app against pgAdmin4 (P1–P5 by priority) and is the source of truth for what to build next (current suggested next step: #4 View/Edit Data editable grid).
- After implementing a feature: move it under “What this app already has” and shrink the corresponding P item. Keep cited `file:line` references accurate, and the reader-facing docs in sync with reality.

## DDL framework conventions

All DDL (create / drop / alter of databases, roles, tablespaces, schemas, tables, sequences, views, matviews, functions, procedures, extensions, publications, indexes, triggers) lives in `internal/web/ddl.go` and follows fixed rules:

- **Server-generated SQL only.** The client posts form values; the server builds the SQL. The client never sends raw SQL text. Flow is generate → preview → run; a successful run refreshes the tree in place.
- Everything is **map-driven** off `ddlKinds`: `Label`, `Scope` (`ddlScopeServer`/`ddlScopeDB`), `HasForce`, `HasCascade`, `BuildCreateForm`, `BuildAlter`, `BuildDrop`. Adding a kind = map entry + modal partial + `context-menu.js` label entries. Server-scoped kinds run on the maintenance pool; database-scoped kinds (e.g. `table`) must be `ddlScopeDB` and run via `ddlTargetPool(ctx, kind, sid, dbName)`.
- Build SQL with `quoteIdent`/`qualIdent`/`quoteLiteral` (never concatenate user input); collapse multi-line user input with `singleLine`. Multi-statement scripts are executed via `splitStatements` (top-level semicolon split). Validation errors use `errRequired`/`formErr` so the form re-renders with the message.
- ALTER forms use **tri-state selects** (`""`/`on`/`off`): the blank value means “unchanged”. Where a rename is offered, emit attribute changes **before** `RENAME TO` (renaming first invalidates the old name in later statements).
- `renderDDLModal` fills `KindLabel`/`HasForce`/`HasCascade` from the kind map and defaults nil `Values`/`Dropdowns`, so early-error paths can pass empty data safely.
- Create-form defaults (login/inherit/connlimit, owner = current user) are applied in `handleDDLModal` before rendering.
- `alterPrefill` is best-effort: load current catalog values with raw queries, log and continue on failure. **Avoid sqlc regeneration** — prefer raw queries or existing sqlc queries (e.g. `GetSchemaGeneral`) for new catalog access.
- When sqlc regeneration is unavoidable, use **sqlc v1.30.0** (`sqlc version`) — the version this repo's generated code (`internal/sqlc/{postgres,sqlite}/db/`) is pinned to; a different version can reformat unrelated generated code as a side effect. Install with `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0`. Regenerate with `sqlc generate -f internal/sqlc/postgres/sqlc.yml` (and the sqlite counterpart if sqlite queries changed) — see README.md "Regenerating sqlc queries".
- Success protocol: `refreshDDLTree(w, folderID)` responds with `HX-Trigger {"ddl-refresh": "<tree container id>"}`; `static/js/ddl.js` re-fetches that container (folders stay expanded) and closes the modal after ~1.6 s.

## Templates & frontend conventions

- Partials auto-register by glob in `internal/web/views.go` (`InitTemplates`) — no manual registration needed.
- Templates are parsed at **startup only**; `go build` will not catch syntax errors. Verify a new partial with a scratch `template.Parse`+`Execute` against sample data before finishing.
- Modal partials render into `#modal-container` (swap target), disable the submit button via `hx-disabled-elt`, show an error banner, and re-render with the submitted values + live dropdowns on failure. Prefill with `{{ index .Values "field" }}`; dropdowns render via `range (index .Dropdowns "roles")` (keys: `roles`, plus `encodings`/`templates` for databases, `extensions`/`schemas` for extensions, `columns` for indexes).
- **Tailwind is prebuilt/minified** (`static/tailwind.css`, embedded) and `npx tailwindcss` is not available — never add new utility classes. Add custom CSS to the `<style>` block in `templates/layouts/base.html` (e.g. `.ddl-col-row` grid) and only reuse class strings already present in existing templates (be aware that class-presence greps can false-negative on `.` / `/` / `:`).
- Classic `static/js/*.js` scripts are non-module scripts loaded in `base.html` — no `import`; only `sql-editor.js`/`sql-formatter.js`/`chart-entry.js` feed the esbuild bundles. Cross-file wiring uses `window.*` helpers: `openCreateDDLDialog`, `openDropDDLDialog`, `openAlterDDLDialog`, `ddlFolderID(el, isCreate)`, `closeDDL`, `openPropertiesTab`, `openScriptTab`.
- URL-context helpers live in `static/js/app.js`: `parseObjectContext` (tolerates `/children` and `/properties` suffixes and non-db URLs) and `objectNameFromURL` fallback — schema/table nodes carry no `data-name`, so derive the name from the node URL.
- The context menu is `static/js/context-menu.js` `openMenu`: kind labels in the `createLabel` map, drop actions in `DROP_ITEMS`, alter actions in `ALTER_ITEMS`, category folders use `create-<kind>` menus (e.g. `create-table` → kind `table`). Node kinds come from `data-tree-menu` (`database`, `role`, `schema`, `table`, ...).

## Frontend bundling (tree shaking)

`scripts/build-editor.mjs` builds two bundles with **esbuild** (`npm run build:editor`):

- `static/codemirror.bundle.js` — CodeMirror 6 + SQL formatter, from `static/js/sql-editor.js`.
- `static/vendor/chart.bundle.js` — **tree-shaken** Chart.js, from `static/js/chart-entry.js` (imports only LineController, LineElement, PointElement, LinearScale, CategoryScale, Legend, Tooltip, Filler; exposes `window.Chart`).

Rules:

- Regenerate bundles after editing `static/js/sql-editor.js`, `static/js/sql-formatter.js`, `static/js/chart-entry.js`, or bumping esbuild/CodeMirror/Chart.js deps.
- Never edit the generated bundles by hand — they live in `static/` and `static/vendor/`.
- The old `static/vendor/chart.umd.min.js` UMD copy is gone; do not reintroduce it.
- Do not add `import` to any classic `static/js/*.js` script (they run as non-module scripts; `window.Chart`/`window.SqlEditor` globals bridge them to the module bundles).
- After rebundling you must rebuild/restart the server (bundle files are embedded) and hard-refresh the browser to bust the cached JS.

## Configuration

- `.env` is loaded by `internal/env/env.go` (`env.Load()` at startup; existing OS env vars win). Missing `.env` is not an error.
- `PORT` (loaded via `env.Get`) — listen address, default `:8080`. Use e.g. `PORT=:3000` (leading colon). Documented in `.env.example`.
- `PGHTMX_ADMIN_DEFAULT_EMAIL` / `PGHTMX_ADMIN_DEFAULT_PASSWORD` — demo credentials, seeded into SQLite (PBKDF2 hashed).
- SQLite metadata DB `pgadmin4.db` is created in the working directory on first run.

## Production Docker build

`docker/Dockerfile` — builds `./cmd/server`, runs as distroless `nonroot` (uid 65532) with `WORKDIR /data` (writable, holds `pgadmin4.db`). Build context must be the repo root:

```sh
docker build -f docker/Dockerfile -t bos-ui:latest .
```

`.dockerignore` keeps the context lean (excludes `node_modules/`, `.git`, `.env`, `pgadmin4.db`, `working_example/`). Persist the SQLite DB with a volume at `/data`.

## Dev container (hot reload)

`docker/docker-compose.dev.yml` lives in `docker/`, so relative paths use `..` for context and the source mount:

- `build.context: ./..`, `dockerfile: docker/Dockerfile_dev` (from the project root).
- Volume `..:/app` mounts the repo; named volumes hold `node_modules` (Linux esbuild, separate from host win32 deps), `/go/pkg/mod`, and `/root/.cache/go-build`.
- `HOST_PORT` env overrides the published host port (default 8080; the user's `pghtmx-container` often occupies 8080).
- `docker/Dockerfile_dev` — golang:alpine + node/npm + Air (`go install github.com/air-verse/air@latest`, pinned automatically).
- `docker/air.toml` — Air watches `go/html/js/mjs/css/mod/sum` files: `pre_cmd` runs `npm run build:editor` (esbuild + tree shaking) then `go build -o /tmp/air/main ./cmd/server`. Excludes the generated bundles and `pgadmin4.db*` from the watch to avoid rebuild loops. Polling enabled for bind-mounts.

Run:

```sh
docker compose -f docker/docker-compose.dev.yml up [--build]
HOST_PORT=18081 docker compose -f docker/docker-compose.dev.yml up  # if 8080 taken
docker compose -f docker/docker-compose.dev.yml down -v              # reset node_modules/go cache volumes
```

The server logs `Server started on http://localhost:8080` (container port; map to HOST_PORT).

**Do NOT verify the whole application is running when the dev container is up** — the user already watches its console output, so do not curl/login/smoke-test or run additional checks of the app itself. Only inspect `docker compose logs` or `docker ps` status when something is actually failing or the user asks. If the container is stopped or not present, a quick smoke-test is fine.

Air config changes are read on container start; restart the container to apply them.

## Environment & reference infrastructure

- Host shell is PowerShell on win32; `rg` is **not** installed — use the Grep/Glob tools or `Select-String` instead.
- Running side-by-side for comparison/testing: `pgadmin_gui` (reference pgAdmin4 UI on :5050 — its docker logs are NOT the app's), `postgres_db` (:5432) and `postgres_db2` (:5433). The Go dev container is only present when started with `docker compose -f docker/docker-compose.dev.yml up`.

## Verification checklist

- Go compiles: `go build ./cmd/server`.
- Go is clean: `gofmt -w` on the files you touched, then `go vet ./internal/web/`. Note: `gofmt -l internal/web/` reports **pre-existing** unformatted files (`auth.go`, `history_stream.go`, `tree.go`, `workspace.go`) — never reformat those; keep only files you actually edited gofmt-clean.
- Template changes: parse and execute the new/modified partials with a small scratch program (`go run` a temp `template.Parse`+`Execute` check with sample `ddlModalData`), because template errors surface only at runtime startup.
- After JS/asset edits: run `npm run build:editor`, confirm both bundle sizes printed, then restart server.
- Chart bundle should be ~166 KB (a full `chart.js/auto` bundle is ~200 KB) — if it grows to ~200 KB, tree shaking is broken (check `static/js/chart-entry.js` imports).