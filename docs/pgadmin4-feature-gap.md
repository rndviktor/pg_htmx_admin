# Feature gap: this app vs pgAdmin4

A comparison of the Go + HTMX Pg HTMX Admin exercise app against
[pgAdmin4](https://github.com/pgadmin-org/pgadmin4), listing the functionality
that is missing, ordered by priority.

## What this app already has

- **Object explorer** (read-only browse): servers, databases, 9 database-level
  collections (casts, catalogs, event triggers, extensions, foreign data
  wrappers, languages, publications, schemas, subscriptions), schemas (tables,
  views, materialized views, sequences, functions, procedures, types, domains),
  tables and views with their own sub-collections, roles and tablespaces
  (`internal/web/handlers.go`, `internal/web/tree.go`).
- **Query tool**: CodeMirror 6 editor, multiple tabs, schema-based
  autocomplete, find & replace, SQL formatting, paginated read-only result
  grid, plain-text `EXPLAIN` output, cancel running query.
- **Query history**: persisted per-tab history with detail views and live
  SSE updates.
- **Monitoring dashboard**: KPI cards and Chart.js time-series graphs
  (connections, transactions, cache hit ratio, replication lag, DML activity)
  fed by polling plus an SSE stream (`internal/web/monitoring.go`).
- **Server administration**: sessions list with cancel/terminate, locks list,
  prepared transactions list (`internal/web/activity.go`).
- **Script generation**: SELECT / CREATE / INSERT / DELETE templates for tables;
  SELECT / CREATE / INSERT for views (`internal/web/script.go`).
- **Workspace**: save and restore tab layout from SQLite; single-user cookie
  auth; server registration modal.

## Missing functionality (by priority)

### P1 — Core object management (biggest gap)

1. **Object properties panels** — view full metadata for any object: columns
   with types/defaults/nullability, constraints, indexes, ownership,
   privileges, comments, dependencies and statistics tabs. Today the tree only
   shows object *names*, never their details.
2. **Create / Alter / Drop of objects** — pgAdmin's centerpiece. Form dialogs
   that generate DDL for databases, schemas, tables (columns, primary
   key/foreign key/unique/check/exclusion constraints, indexes, triggers,
   rules, RLS policies, partitions), views, materialized views, sequences,
   functions, procedures, roles, tablespaces, extensions, publications. Today
   everything is read-only and scripts are only templates pasted into the
   editor.
3. **Full server/database/object context-menu actions** — DROP / DROP CASCADE,
   DROP script, CREATE script, Connect/Disconnect database, Reload
   configuration, add named restore point. Currently the tree's only server
   action is "Try to reconnect" (`internal/web/server_manager.go`).

### P2 — Data editing + maintenance

4. **View/Edit Data tool** — editable grid for tables and views with
   insert/update/delete, in-cell editing, sorting, filtering, pagination and
   CSV copy/export. The current result grid renders text only
   (`internal/web/handlers.go`, `templates/partials/query_result.html`).
5. **Backup & Restore** — pg_dump / pg_dumpall / pg_restore dialogs;
   **Maintenance dialog** (VACUUM, ANALYZE, REINDEX, CLUSTER); **Storage
   Manager** for server-side backup files.
6. **Query tool power features** — transaction control (BEGIN / COMMIT /
   ROLLBACK buttons, auto-commit), visual/shaped EXPLAIN (currently plain
   text in `internal/web/handlers.go`), multiple result sets, execute a
   selected statement, query timings, download results as CSV, server-side
   result cursors.

### P3 — Management depth

7. **Role & privilege management** plus a **Grant Wizard** (grant/revoke
   privileges across objects). pgAdmin's security model is central; this app
   only lists roles.
8. **Import/Export data dialog** (bulk CSV load/unload).
9. **Richer dashboards** — server-level statistics plus I/O, CPU, memory and
   session graphs. `internal/web/monitoring.go` currently covers ~10 metrics
   for a single database.

### P4 — Developer tools

10. **Global object search** (pgAdmin's `Search objects`).
11. **Schema Diff** — compare and synchronize two databases or schemas and
    generate migration scripts.
12. **ERD tool** and **PSQL terminal tool**.
13. **Function Debugger** (pldebugger integration).

### P5 — Platform, security and coverage

14. **Real authentication & user management** — multiuser accounts, admin
    roles, master password / encrypted stored passwords (currently stored in
    plaintext, `internal/web/server_manager.go`), 2FA, LDAP/OAuth/webserver
    auth sources; the session signing key is a hardcoded placeholder
    (`internal/web/auth.go`).
15. **Fuller object coverage** — foreign tables, user mappings, collations,
    FTS configurations/dictionaries/parsers/templates, operators and operator
    classes/families, statistics objects, aggregates.
16. **Preferences UI, themes, keyboard shortcuts, drag-and-drop of objects into
    the query editor, localization.**

## Suggested starting points

The two highest-leverage projects that build most naturally on the existing
`tree.go` / sqlc structure are:

- **#1 + #2: object properties panels and DDL CRUD** (tables, schemas,
  databases and roles first), or
- **#4: View/Edit Data** editable grid.