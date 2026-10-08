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
  grid, cancel running query. Power features: **server-side result cursors**
  (opt-in cursor toggle in the toolbar: a single row-returning statement is
  declared once as a scrollable cursor and pages are `MOVE`/`FETCH`, so the
  query runs once instead of `COUNT(*)` + `LIMIT/OFFSET` per page; the total
  is unknown until the last page is reached or "Last" is clicked; the tab
  stays in a transaction, which any other run, COMMIT/ROLLBACK or 5 minutes
  idle ends; one cursor per tab; `internal/web/cursor.go`,
  `static/js/tabs.js`, tests in `internal/web/cursor_test.go` run against a
  live server when `TEST_PG_DSN` is set), **live LISTEN / NOTIFY** (typing
  `LISTEN channel` in a tab opens a dedicated listener connection outside
  the pool, and notifications stream into the Notifications tab over
  server-sent events until `UNLISTEN`, tab close, server or database
  disconnect, or 30 minutes without a watching browser; at most 10 listening
  tabs; `LISTEN` inside an open transaction takes effect immediately, not at
  commit; `internal/web/listen.go`, `static/js/listen.js`), **multiple
  result sets** (a script with several statements runs them in order on the
  tab's connection and shows one result tab per statement; it stops at the
  first error, and each set is capped at the page limit because scripts are
  not paged; `internal/web/multi.go`, `static/js/results.js`), execute
  selected text, per-run timings (status bar, Messages, history),
  **transaction control** (BEGIN / COMMIT / ROLLBACK buttons, an Auto-commit
  toggle and a state badge; a tab's connection is pinned only while it is
  inside a transaction, so typed `BEGIN`/`SAVEPOINT` work too; open
  transactions are rolled back on tab close, page refresh, server disconnect
  and after 15 minutes idle, and at most `MaxConns - 1` transactions can be
  open per database; `internal/web/session.go`), **NOTICE / WARNING output**
  in the Notifications tab for notices raised during a run
  (`internal/web/notices.go`, `static/js/transactions.js`), **visual
  EXPLAIN** (F7 / Shift+F7: `POST /api/explain` runs `EXPLAIN (FORMAT
  JSON)`, and `internal/web/explain_plan.go` + `templates/partials/explain_plan.html` render a tree (native `<details>`, no script) with exclusive-time
  bars, estimate vs actual rows, hotspot / misestimate / seq-scan-filter /
  spill badges, a sortable table and raw JSON; ANALYZE always runs in a
  transaction that is rolled back, so INSERT/UPDATE/DELETE leave no trace;
  `internal/web/explain.go`), **Download as CSV** (full result streamed via
  `COPY ... TO STDOUT`, `internal/web/export.go`), history recording
  `success` / `error` / `cancelled` (failed and cancelled runs included;
  `internal/web/history.go`), a table's **UPDATE Script**
  (`handleUpdateScript`, `internal/web/script.go`) and a **Scratch Pad**
  that persists per tab with the workspace (`workspace_tabs.scratch_text`,
  its x button clears it). Known limits: EXPLAIN and CSV export use their
  own connection, so they do not see uncommitted work of an open
  transaction; cursor mode applies to a single row-returning statement only
  (scripts with several statements and EXPLAIN keep the normal path).
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
- **DDL side panels**: the Create / Drop / Alter forms for every object kind
  and the Maintenance operations (Vacuum / Analyze / Cluster / Reindex) no
  longer open popups. Each opens a script tab whose left 70% is a panel with
  the form components; any change re-generates the SQL live into the tab's
  editor (the server-side builders behind `POST /api/ddl/{kind}/preview` and
  `/api/maint/{op}/preview`), the tab's Run executes it and refreshes the
  tree (and reloads an Alter form against the new definition), `<<` slides the
  panel away and `>>` in the toolbar brings it back, Reset reloads the form.
  Server-level objects (database, role, tablespace) run on the server's
  maintenance database. Backup / Restore, Storage Manager, Add Server and Save
  Script remain modals (`internal/web/ddl.go` `handleDDLPanel`,
  `internal/web/maintenance.go` `handleMaintPanel`,
  `templates/partials/ddl_*_panel.html`, `maint_panel.html`,
  `static/js/ddl-panel.js`). The forms are htmx all the way: the form posts to
  the SQL builder on every change, Reset and column rows are server round trips,
  and the right-click menu and EXPLAIN view are rendered by Go templates
  (`internal/web/contextmenu.go`, `explain_plan.go`); Chart.js loads only when
  the Monitoring dashboard first draws a chart.
- **DDL forms (Create / Drop / Alter)**: form-based generate-then-preview-then-run (now shown in the side panels above)
  for 15 object kinds — database, role, tablespace, schema, sequence, view,
  materialized view, function, procedure, extension, publication, index,
  trigger, rule and RLS policy — plus a Create Table dialog with columns
  (name, type, nullability, default, primary key / unique keys) and a CHECK
  constraint. Pre-filled `ALTER` dialogs now cover all 15 kinds: rename/owner
  for database, role, tablespace and publication; rename/owner/set-schema for
  table, view, materialized view, function, procedure and sequence (plus
  increment/min/max/cache/restart/cycle for sequences); update-version/
  set-schema for extensions (no owner/rename — Postgres has no such `ALTER
  EXTENSION` form); rename for indexes and rules (Postgres has no other
  `ALTER RULE` form — the event/action/WHERE clause can't be changed in
  place, only dropped and recreated); enable/disable + rename for triggers;
  and roles/using/with-check plus rename for RLS policies (two independent
  statements — Postgres doesn't allow combining a rename with the other
  attribute changes in one `ALTER POLICY`; `permissive`/`FOR <cmd>` aren't
  alterable at all, matching Postgres). **Table's
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
  supported. **Exclusion constraints** (`EXCLUDE USING <method> (<element>
  WITH <operator>, ...) [WHERE (predicate)]`) can also be added from both
  dialogs, as one optional free-text block styled like the CHECK constraint
  field — raw element list/predicate, validated by Postgres at run time, no
  operator/opclass picker (`buildExclusionClause`, `internal/web/ddl.go`); the
  Constraints tab now labels these "exclusion" instead of the raw `x` code
  (`constraintTypeLabel`, `internal/web/tree.go`; `ListConstraints`,
  `internal/sqlc/postgres/queries.sql`). **Generated columns**
  (`GENERATED ALWAYS AS (...) STORED`) are supported too, on both a new
  column in Create Table and a new column added via Alter Table — mutually
  exclusive with a default, which is a form error rather than one silently
  overriding the other (`buildColumnDef`, `internal/web/ddl.go`). Converting
  an *existing* column to generated isn't offered since Postgres itself has
  no such `ALTER COLUMN` form — only a newly added column can be generated.
  **Rules** (`CREATE RULE ... AS ON <event> TO <table> [WHERE (...)] DO
  [ALSO|INSTEAD] <action>`) can now be created, renamed and dropped from the
  Rules folder under a table, same generate-then-preview-then-run flow as
  every other kind — the action clause is raw SQL the admin types themselves
  (`NOTHING` or one or more commands), same trust model as the CHECK
  constraint and exclusion-element fields (`buildCreateRule`,
  `internal/web/ddl.go`). **RLS policy DDL** (`CREATE POLICY` / `ALTER
  POLICY` / `DROP POLICY`) is done too — permissive/restrictive, `FOR
  <cmd>`, a Roles picker (`<select multiple>` sourced from the same roles
  list every dialog already fetches — leaving it empty applies the policy to
  `PUBLIC`), and optional `USING`/`WITH CHECK` expressions
  (`buildCreatePolicy`, `internal/web/ddl.go`); `DROP POLICY` has no
  `CASCADE` clause in Postgres, unlike every other droppable kind.
  **Create Index and Create Trigger are richer now too**: indexes get
  `INCLUDE` columns, `WITH (...)` storage parameters and a partial-index
  `WHERE` predicate (`buildCreateIndex`); triggers get multiple OR'd events
  (was a single-select), a `FOR EACH ROW`/`STATEMENT` level, `INSTEAD OF`
  as a proper timing option, and constraint-trigger support (`CREATE
  CONSTRAINT TRIGGER ... DEFERRABLE [INITIALLY DEFERRED]`, validated to
  require `AFTER` + `FOR EACH ROW`) (`buildCreateTrigger`,
  `internal/web/ddl.go`) — this also fixed two latent bugs, where the
  trigger form's WHEN-expression field and INSTEAD-OF checkbox existed but
  were silently ignored by the SQL builder. **Table partitioning** is
  supported too: Create Table takes an optional `PARTITION BY RANGE/LIST/
  HASH (...)` clause, and Alter Table gets `ATTACH PARTITION` (picking an
  existing table from the same cross-schema `ref_tables` list the foreign
  key dialog uses, plus a free-text bound — `FOR VALUES FROM (...) TO
  (...)`/`IN (...)`/`WITH (MODULUS .., REMAINDER ..)`/`DEFAULT`, whose exact
  shape depends on the parent's own strategy and so isn't guided) and
  `DETACH PARTITION` (with an optional `CONCURRENTLY`) (`buildPartitionByClause`/
  `buildAttachPartitionStmt`/`buildDetachPartitionStmt`, `internal/web/ddl.go`).
  The client never sends raw SQL; a successful create/drop/alter refreshes
  the tree in place (`internal/web/ddl.go`). Drop is now wired for every one of the 15 kinds
  including tables (previously missing only from the context menu, the
  server-side builder already existed). **DROP Script**: any droppable kind
  can also generate its DROP SQL into a read-only script tab without running
  it (`GET /api/ddl/{kind}/drop-script`), blocked the same way the run-it
  Drop dialog already was when the object's server is disconnected.
- **Maintenance dialog**: Vacuum, Analyze, Cluster and Reindex, from the
  tree's right-click menu — Vacuum/Analyze/Cluster/Reindex Table on tables,
  Vacuum/Analyze/Reindex Database on databases, Reindex Index on indexes and
  Reindex Schema on schemas — same generate-then-preview-then-run flow as the
  DDL dialogs, always run against the target database's connection (there is
  no server-wide maintenance-db variant). Options: FULL/FREEZE/ANALYZE/
  VERBOSE for Vacuum, VERBOSE for Analyze, an optional target index plus
  VERBOSE for Cluster, CONCURRENTLY/VERBOSE for Reindex
  (`internal/web/maintenance.go`, `templates/partials/maint_modal.html`).
- **Backup & Restore**: `pg_dump` (database, schema or table; custom/tar/
  plain/directory format, compression, parallel jobs for directory dumps,
  encoding, schema-/data-only, clean/if-exists/create, no-owner/no-privileges,
  role, INSERT mode), `pg_dumpall` for globals (roles and/or tablespaces) and
  restore — `pg_restore` for custom/tar/directory archives (clean/create,
  content, parallel jobs, single-transaction, exit-on-error) and `psql` for
  plain `.sql` files (incl. globals dumps; single-transaction, exit-on-error).
  Offered from the tree's right-click "Backup / Restore" submenu. Same
  generate-preview-run flow: the server builds the argv (no shell, password
  via `PGPASSWORD`) and previews the command.
  Commands run as **background jobs** (`internal/web/jobs.go`): closing the
  dialog doesn't stop them, the panel polls every second showing elapsed
  time, bytes written and the `--verbose` log tail, jobs can be cancelled
  (partial output is deleted) and are listed under "Background Jobs" (in
  memory only, newest 50, lost on restart; 30 min cap per job).
  The **Storage Manager** lists, uploads, downloads and deletes the backups in
  the storage directory (`BACKUP_DIR`, default `./backups`); directory dumps
  show as folders, download as a zip and delete recursively. Needs the
  PostgreSQL 18 client tools, installed in both Dockerfiles
  (`internal/web/backup.go`, `jobs.go`, `storage.go`,
  `templates/partials/backup_*.html`, `storage_modal.html`). Not covered:
  uploading directory dumps, restoring compressed (`.sql.gz`) scripts.
- **Script generation**: SELECT / CREATE / INSERT / DELETE templates for tables;
  SELECT / CREATE / INSERT for views; SELECT for materialized views
  (`internal/web/script.go`).
- **Workspace**: save and restore tab layout from SQLite; single-user cookie
  auth; server registration modal.

## Missing functionality (by priority)

### P1 — Core object management (biggest gap)

- ~~**P1.1.** **Full table DDL and broader ALTER**~~ — **done.** Create
  Table, ALTER TABLE (owner/schema/rename, column-level add/drop/alter/
  rename, foreign key, exclusion and generated-column support, table
  partitioning), Rules (create/rename/drop), RLS policy DDL (create/alter/
  drop) and richer index/trigger create options are all in place; see "What
  this app already has" above.

### P2 — Data editing + maintenance

- **P2.1.** **View/Edit Data tool** — editable grid for tables and views with
  insert/update/delete, in-cell editing, sorting, filtering, pagination and
  CSV copy/export. The current result grid renders text only
  (`internal/web/handlers.go`, `templates/partials/query_result.html`).
- ~~**P2.2.** **Backup & Restore**~~ — **done**; see "What this app already
  has" above.
- ~~**P2.3.** **Query tool power features**~~ — **done**; see "What this app already
  has" above (Query tool).

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

**P1.1 is fully done** (see "What this app already has" above — column-level
table DDL, foreign key/exclusion/generated-column support, table
partitioning, rules, RLS policy DDL and richer index/trigger create options
are all in place, `internal/web/ddl.go`). The highest-leverage next project:

- **P2.1: View/Edit Data** — an editable grid for tables and views
  (insert/update/delete, in-cell editing, sorting, filtering, pagination,
  CSV copy/export). The current result grid renders text only
  (`internal/web/handlers.go`, `templates/partials/query_result.html`).