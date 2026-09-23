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
- **Object properties panels**: tabbed General / Columns / Constraints /
  Indexes / Privileges / Statistics / Dependencies / SQL detail for tables,
  views, materialized views, sequences, functions, indexes, triggers and
  schemas — column types/defaults/nullability, constraints, ownership,
  privileges, comments, dependencies and statistics
  (`internal/web/properties.go`, `templates/partials/properties_panel.html`).
- **DDL dialogs (Create / Drop / Alter)**: form-based generate-then-preview-then-run
  for 13 object kinds — database, role, tablespace, schema, sequence, view,
  materialized view, function, procedure, extension, publication, index and
  trigger — plus a Create Table dialog with columns (name, type, nullability,
  default, primary key / unique keys) and a CHECK constraint. Pre-filled
  `ALTER` dialogs now cover all 13 kinds: rename/owner for database, role,
  tablespace and publication; rename/owner/set-schema for table, view,
  materialized view, function, procedure and sequence (plus increment/min/max/
  cache/restart/cycle for sequences); update-version/set-schema for
  extensions (no owner/rename — Postgres has no such `ALTER EXTENSION` form);
  rename for indexes; and enable/disable + rename for triggers. The client
  never sends raw SQL; a successful create/drop/alter refreshes the tree in
  place (`internal/web/ddl.go`).
- **Script generation**: SELECT / CREATE / INSERT / DELETE templates for tables;
  SELECT / CREATE / INSERT for views; SELECT for materialized views
  (`internal/web/script.go`).
- **Workspace**: save and restore tab layout from SQLite; single-user cookie
  auth; server registration modal.

## Missing functionality (by priority)

### P1 — Core object management (biggest gap)

1. **Full table DDL and broader ALTER** — **Create Table is now wired**: the
   Tables folder (`internal/web/tree.go:234`) opens a Create Table dialog with
   per-column name, type, nullability, default and primary key / unique keys
   plus a CHECK constraint (`templates/partials/ddl_table_modal.html`).
   **ALTER now covers all 13 DDL kinds**: database, role, tablespace, schema,
   table, view, materialized view, sequence, function, procedure, extension,
   publication, index and trigger all open pre-filled edit-in-place dialogs
   (`internal/web/ddl.go`, `templates/partials/ddl_alter_modal.html`). Table's
   ALTER covers owner/schema/rename only — no column-level changes. Still
   missing: foreign key / exclusion constraints and generated columns (Create
   Table and `ALTER TABLE` column DDL — add/drop/alter column), rules, RLS
   policies, table partitioning, and richer index/trigger *create* options
   (constraint options, `USING` storage parameters).
2. **Properties coverage is partial** — no properties panel at all for
   databases, roles, tablespaces, procedures, extensions and publications, nor
   for the plain leaves (columns, constraints, RLS policies, rules, types,
   domains, casts, catalogs, event triggers, foreign data wrappers, languages,
   subscriptions). Tabs are also partial: Constraints only on tables;
   Privileges only on table/view/materialized-view/sequence/schema;
   Statistics and Dependencies only on table/view/materialized-view.
3. **Remaining context-menu gaps** — Disconnect / Connect / Try to reconnect,
   Create (database/role/tablespace/table), Drop (13 kinds, with CASCADE/FORCE),
   Alter (13 kinds) and Properties/Scripts/Query Tool are all present, but
   still missing: DROP SCRIPT (generate without executing), a Drop action for
   tables specifically (the builder exists in `internal/web/ddl.go` but isn't
   wired into `static/js/context-menu.js`'s `DROP_ITEMS`), per-database
   Connect/Disconnect, Reload configuration, and named restore points.

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
   result cursors. Minor query-tool stubs: the Notifications tab is never
   written to (`templates/partials/script_tab_panel.html:227-248`), the
   Scratch Pad is an inert textarea (:199-206), history always records
   `"success"` (`internal/web/history.go:152`), and a table's **UPDATE Script**
   opens an empty tab (`static/js/tabs.js:761-775`).

### P3 — Management depth

7. **Role & privilege management** plus a **Grant Wizard** (grant/revoke
   privileges across objects). Roles can be created, altered (login, superuser,
   createdb/createrole, inherit, replication, connlimit, valid-until,
   password) and dropped, but there is no role-membership editor and no
   privilege-editing UI (properties only *display* ACLs).
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

- **#1: `ALTER TABLE` column DDL** (add/drop/alter column, foreign key /
  exclusion constraints, generated columns) to round out table ALTER beyond
  owner/schema/rename, plus **#2: properties coverage** for the remaining
  object kinds, or
- **#4: View/Edit Data** editable grid.