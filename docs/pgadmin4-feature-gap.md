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
  prepared transactions list (`internal/web/activity.go`). Per-server and
  per-database Connect/Disconnect/"Try to reconnect" (the database-level flag
  persists across restarts, mirroring the server-level one), Reload
  Configuration (`pg_reload_conf()`), and named restore points
  (`pg_create_restore_point()`), all from the tree's right-click menu
  (`internal/web/server_manager.go`, `static/js/context-menu.js`).
- **Object properties panels**: tabbed General / Columns / Constraints /
  Indexes / Privileges / Statistics / Dependencies / SQL detail for tables,
  views, materialized views, sequences, functions, indexes, triggers and
  schemas — column types/defaults/nullability, constraints, ownership,
  privileges, comments, dependencies and statistics. Databases, roles,
  tablespaces, procedures, extensions and publications get a read-only
  General + SQL properties panel too (owner/attributes plus a reconstructed
  CREATE script; procedures reuse the function definition query directly).
  **Every remaining "plain leaf" object kind now has its own General + SQL
  panel too**: columns, constraints, RLS policies, rules, types (composite/
  enum/range), domains, casts, catalogs (system schemas), event triggers,
  foreign data wrappers, languages and subscriptions. The Privileges tab is
  also wired up for database, tablespace, function and procedure (role,
  extension and publication are not — Postgres has no ACL/GRANT concept for
  those three object kinds) (`internal/web/properties.go`,
  `templates/partials/properties_panel.html`). Verified live against a real
  server: all of the above render correctly, including empty-state folders
  and a cast whose type name contains a space. One pre-existing display quirk
  (not introduced by this work): the schema-level Types folder's query
  matches `pg_type.typtype IN ('c','e','r')`, which also matches every
  table's/view's own implicit row type, so "Types" often just lists ordinary
  tables again rather than only user-defined `CREATE TYPE` composites/enums/
  ranges — clicking one still opens a correct (if misleadingly-labeled)
  Composite panel showing that table's columns.
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
  rename for indexes; and enable/disable + rename for triggers. **Table's
  ALTER also covers column-level DDL**: add, drop, retype, rename and toggle
  nullability/default on existing columns, plus add new columns, from the
  same dialog (`buildAlterTableForm`, `internal/web/ddl.go`). **Foreign key
  constraints** can now be added from both Create Table and Alter Table:
  local column(s), a cross-schema target-table picker whose referenced-column
  select is fetched live once a target is chosen (`GET
  /api/ddl/table/fk-ref-columns`, the app's first and only cascading/
  dependent dropdown — every other DDL dropdown is still baked once at
  modal-render time), plus MATCH/ON UPDATE/ON DELETE/DEFERRABLE
  (`buildForeignKeyClause`, `internal/web/ddl.go`). One FK per submit
  (matching the single-CHECK-constraint convention), composite keys
  supported. The client never sends raw SQL; a successful create/drop/alter
  refreshes the tree in place (`internal/web/ddl.go`). Drop is now wired for every one of the 13
  kinds including tables (previously missing only from the context menu, the
  server-side builder already existed). **DROP Script**: any droppable kind
  can also generate its DROP SQL into a read-only script tab without running
  it (`GET /api/ddl/{kind}/drop-script`), blocked the same way the run-it
  Drop dialog already was when the object's server is disconnected.
- **Script generation**: SELECT / CREATE / INSERT / DELETE templates for tables;
  SELECT / CREATE / INSERT for views; SELECT for materialized views
  (`internal/web/script.go`).
- **Workspace**: save and restore tab layout from SQLite; single-user cookie
  auth; server registration modal.

## Missing functionality (by priority)

### P1 — Core object management (biggest gap)

- **P1.1.** **Full table DDL and broader ALTER** — **Create Table is now wired**: the
  Tables folder (`internal/web/tree.go:234`) opens a Create Table dialog with
  per-column name, type, nullability, default and primary key / unique keys
  plus a CHECK constraint (`templates/partials/ddl_table_modal.html`).
  **ALTER now covers all 13 DDL kinds**: database, role, tablespace, schema,
  table, view, materialized view, sequence, function, procedure, extension,
  publication, index and trigger all open pre-filled edit-in-place dialogs
  (`internal/web/ddl.go`, `templates/partials/ddl_alter_modal.html`).
  **Table's ALTER now also covers column-level DDL**: add, drop, retype,
  rename and toggle nullability/default on existing columns, plus add new
  columns, all from the same dialog (`buildAlterTableForm`,
  `internal/web/ddl.go`). **Foreign key constraints are now supported** on
  both Create Table and Alter Table — one FK per submit (composite keys
  supported), with MATCH/ON UPDATE/ON DELETE/DEFERRABLE and a cross-schema
  target-table + referenced-column picker (`buildForeignKeyClause`,
  `internal/web/ddl.go`). Still missing: exclusion constraints and generated
  columns (on both Create Table and `ALTER TABLE`), rules, RLS policies,
  table partitioning, and richer index/trigger *create* options (constraint
  options, `USING` storage parameters).

### P2 — Data editing + maintenance

- **P2.1.** **View/Edit Data tool** — editable grid for tables and views with
  insert/update/delete, in-cell editing, sorting, filtering, pagination and
  CSV copy/export. The current result grid renders text only
  (`internal/web/handlers.go`, `templates/partials/query_result.html`).
- **P2.2.** **Backup & Restore** — pg_dump / pg_dumpall / pg_restore dialogs;
  **Maintenance dialog** (VACUUM, ANALYZE, REINDEX, CLUSTER); **Storage
  Manager** for server-side backup files.
- **P2.3.** **Query tool power features** — transaction control (BEGIN / COMMIT /
  ROLLBACK buttons, auto-commit), visual/shaped EXPLAIN (currently plain
  text in `internal/web/handlers.go`), multiple result sets, execute a
  selected statement, query timings, download results as CSV, server-side
  result cursors. Minor query-tool stubs: the Notifications tab is never
  written to (`templates/partials/script_tab_panel.html:227-248`), the
  Scratch Pad is an inert textarea (:199-206), history always records
  `"success"` (`internal/web/history.go:152`), and a table's **UPDATE Script**
  opens an empty tab (`static/js/tabs.js:761-775`).

### P3 — Management depth

- **P3.1.** **Role & privilege management** plus a **Grant Wizard** (grant/revoke
  privileges across objects). Roles can be created, altered (login, superuser,
  createdb/createrole, inherit, replication, connlimit, valid-until,
  password) and dropped, but there is no role-membership editor and no
  privilege-editing UI (properties only *display* ACLs).
- **P3.2.** **Import/Export data dialog** (bulk CSV load/unload).
- **P3.3.** **Richer dashboards** — server-level statistics plus I/O, CPU, memory and
  session graphs. `internal/web/monitoring.go` currently covers ~10 metrics
  for a single database.

### P4 — Developer tools

- **P4.1.** **Global object search** (pgAdmin's `Search objects`).
- **P4.2.** **Schema Diff** — compare and synchronize two databases or schemas and
  generate migration scripts.
- **P4.3.** **ERD tool** and **PSQL terminal tool**.
- **P4.4.** **Function Debugger** (pldebugger integration).

### P5 — Platform, security and coverage

- **P5.1.** **Real authentication & user management** — multiuser accounts, admin
  roles, master password / encrypted stored passwords (currently stored in
  plaintext, `internal/web/server_manager.go`), 2FA, LDAP/OAuth/webserver
  auth sources; the session signing key is a hardcoded placeholder
  (`internal/web/auth.go`).
- **P5.2.** **Fuller object coverage** — foreign tables, user mappings, collations,
  FTS configurations/dictionaries/parsers/templates, operators and operator
  classes/families, statistics objects, aggregates.
- **P5.3.** **Preferences UI, themes, keyboard shortcuts, drag-and-drop of objects into
  the query editor, localization.**

## Suggested starting points

The two highest-leverage projects that build most naturally on the existing
`tree.go` / sqlc structure are:

- **P1.1 follow-up: Exclusion constraints and generated columns**, on both
  Create Table and `ALTER TABLE` — the column DDL and foreign key constraints
  this used to name are now done (`buildAlterTableForm`/
  `buildForeignKeyClause`, `internal/web/ddl.go`), or
- **P2.1: View/Edit Data** editable grid.