# Pg HTMX Admin application

A pgAdmin-style web application for browsing and managing PostgreSQL servers, built as a learning exercise with **Go**, **HTMX** and server-rendered HTML templates.

## Features

- **Object explorer** — a lazily-loaded tree of servers, databases, schemas, tables and views, with live object counts and connection health indicators.
- **SQL query editor** — multiple tabs powered by a bundled CodeMirror 6 editor with SQL syntax highlighting, autocomplete, find & replace and SQL formatting.
- **Query result grid** — paginated result sets, `EXPLAIN` plan output and command feedback for DDL/DML statements.
- **Query history** — persisted per-tab history with detail views and live streaming updates.
- **Monitoring dashboard** — live KPIs and time-series charts (Chart.js) of connections, transactions, cache hit ratio, replication lag and DML activity, fed by polling plus a Server-Sent Events stream.
- **Server administration** — list and cancel/terminate active sessions, view locks, and list prepared transactions.
- **Script generation** — generate `SELECT`, `CREATE`, `INSERT` and `DELETE` scripts for tables and views.
- **Workspace** — save and restore the full UI layout from SQLite.
- **Register PostgreSQL servers** — connect to arbitrary servers via a modal form, storing credentials (demo only) in SQLite.

## Tech stack

| Layer      | Technology                                                              |
| ---------- | ----------------------------------------------------------------------- |
| Language   | Go 1.27                                                                  |
| Routing    | [chi](https://github.com/go-chi/chi)                                     |
| PostgreSQL | [pgx / pgxpool](https://github.com/jackc/pgx)                            |
| Metadata   | [modernc.org/sqlite](https://gitlab.com/cznic/sqlite)                    |
| Frontend   | [HTMX](https://htmx.org/), Tailwind CSS, vanilla JS                      |
| Editor     | CodeMirror 6 (bundled with esbuild)                                      |
| Charts     | Chart.js                                                                 |
| SQL codegen| [sqlc](https://sqlc.dev/) (both SQLite and PostgreSQL)                   |

## Project layout

```
cmd/server/          Application entry point
internal/db/         SQLite connection, schema migration and seed data
internal/env/        Minimal .env file loader
internal/passwords/  PBKDF2 password hashing (stdlib, no external deps)
internal/sqlc/       sqlc-generated queries (postgres + sqlite)
internal/web/        HTTP handlers, templates, auth, SSE streams
templates/           Go html/template layouts, pages and partials
static/              Embedded assets (JS, CSS, CodeMirror bundle, vendor)
static/js/           Frontend modules (tree, tabs, editor, monitoring, ...)
scripts/             Build tooling for static assets
working_example/     docker-compose for a local Postgres + pgAdmin playground
```

## Getting started

### 1. Requirements

- Go 1.27+
- Node.js (for rebuilding frontend bundles — optional)
- A reachable PostgreSQL server (or use `working_example/docker-compose.yml`)

### 2. Configure credentials

Copy `.env.example` to `.env` and adjust as needed:

```sh
PGHTMX_ADMIN_DEFAULT_EMAIL=admin
PGHTMX_ADMIN_DEFAULT_PASSWORD=secret
```

On every startup the demo user is seeded (or refreshed) from these values. The password is stored in SQLite only as a PBKDF2 hash, never in plain text.

### 3. Run the server

```sh
go run ./cmd/server
```

The server listens on `:8080` by default. Override with the `ADDR` environment variable:

```sh
ADDR=:9000 go run ./cmd/server
```

Open http://localhost:8080 and sign in with the email/password from `.env`. A failed login always shows `Wrong email or password.`, regardless of whether the email exists.

### 4. Register a PostgreSQL server

Use the **add server** modal (✦ icon in the sidebar) and point it at your Postgres instance. For a quick local playground:

```sh
docker compose -f working_example/docker-compose.yml up -d
```

This starts:

- `postgres` on `localhost:5432` — user `root` / `rootpassword`, database `mydatabase`
- `pgadmin` on `localhost:5050` (the reference UI) — email `admin@admin.com` / `adminpassword`

Then register `localhost:5432` with those credentials and browse the tree.

### 5. Rebuilding static assets (optional)

The CodeMirror editor and Chart.js are vendorized into `static/`. The editor bundle (`static/codemirror.bundle.js`) is built with esbuild **from `static/js/sql-editor.js`, inlining `static/js/sql-formatter.js`** (the SQL formatter behind the Format button) and the CodeMirror packages. To rebuild after editing any of those files:

```sh
npm install              # first run only (installs esbuild + CodeMirror deps)
npm run build:editor     # esbuild JS bundle + copy chart.js
```

Important: the built assets are embedded into the Go binary via `//go:embed`, so after rebuilding the bundle you must also rebuild/restart the server (`go run ./cmd/server`) and hard-refresh the browser (Ctrl+F5) — otherwise the old cached JS keeps running.

Tailwind is compiled separately (see `tailwind.config.js` / `tailwind.input.css`); the compiled `static/tailwind.css` is embedded with the rest of the assets via `//go:embed`.

### 6. Regenerating sqlc queries

Requires the [sqlc](https://sqlc.dev/) CLI (this repo is developed against **sqlc v1.30.0**):

```sh
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
sqlc version   # confirm v1.30.0
```

```sh
# After editing queries in internal/sqlc/{postgres,sqlite}/queries.sql
sqlc generate -f internal/sqlc/postgres/sqlc.yml
sqlc generate -f internal/sqlc/sqlite/sqlc.yml
```

## Configuration

- `ADDR` — listen address (default `:8080`).
- `PGHTMX_ADMIN_DEFAULT_EMAIL` / `PGHTMX_ADMIN_DEFAULT_PASSWORD` — demo credentials, loaded from `.env` (defaults `admin@admin.com` / `secret`).
- SQLite metadata DB is created at `pgadmin4.db` on first run (auto-migrated and seeded).
- Session secret is a hardcoded placeholder (`auth.go`) — replace before any real deployment.

## Notes

This is an exercise project. Authentication is a demo, registered server credentials are stored in plaintext in SQLite, and the session signing key is a placeholder. Do not expose it to untrusted networks without hardening.