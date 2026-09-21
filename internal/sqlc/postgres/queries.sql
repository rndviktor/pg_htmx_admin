-- Server-level queries (run against maintenance DB)

-- name: ListDatabases :many
SELECT datname FROM pg_database
WHERE datallowconn AND NOT datistemplate
ORDER BY datname;

-- name: ListRoles :many
SELECT rolname FROM pg_roles
ORDER BY rolname;

-- name: ListTablespaces :many
SELECT spcname FROM pg_tablespace
ORDER BY spcname;

-- name: CountServerObjects :many
SELECT 'databases' AS category, count(*) AS n FROM pg_database d WHERE d.datallowconn AND NOT d.datistemplate
UNION ALL SELECT 'roles', count(*) FROM pg_roles
UNION ALL SELECT 'tablespaces', count(*) FROM pg_tablespace;

-- Database-level category queries (run against a specific database)

-- name: ListCasts :many
SELECT '(' || castsource::regtype || ' AS ' || casttarget::regtype || ')' FROM pg_cast ORDER BY 1;

-- name: ListCatalogs :many
SELECT nspname FROM pg_namespace
WHERE nspname = 'information_schema' OR nspname LIKE 'pg\_%'
ORDER BY 1;

-- name: ListEventTriggers :many
SELECT evtname FROM pg_event_trigger ORDER BY 1;

-- name: ListExtensions :many
SELECT extname FROM pg_extension ORDER BY 1;

-- name: ListForeignDataWrappers :many
SELECT fdwname FROM pg_foreign_data_wrapper ORDER BY 1;

-- name: ListLanguages :many
SELECT lanname FROM pg_language WHERE lanispl ORDER BY 1;

-- name: ListPublications :many
SELECT pubname FROM pg_publication ORDER BY 1;

-- name: ListSchemas :many
SELECT nspname FROM pg_namespace
WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
ORDER BY 1;

-- name: ListSubscriptions :many
SELECT subname FROM pg_subscription
WHERE subdbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY 1;

-- Schema-level category queries ($1 = schema name)

-- name: ListTables :many
SELECT tablename FROM pg_tables WHERE schemaname = $1 ORDER BY 1;

-- name: ListViews :many
SELECT viewname FROM pg_views WHERE schemaname = $1 ORDER BY 1;

-- name: ListMaterializedViews :many
SELECT matviewname FROM pg_matviews WHERE schemaname = $1 ORDER BY 1;

-- name: ListSequences :many
SELECT sequencename FROM pg_sequences WHERE schemaname = $1 ORDER BY 1;

-- name: ListFunctions :many
SELECT p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
WHERE n.nspname = $1 AND p.prokind = 'f'
ORDER BY 1;

-- name: ListProcedures :many
SELECT p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
WHERE n.nspname = $1 AND p.prokind = 'p'
ORDER BY 1;

-- name: ListTypes :many
SELECT t.typname FROM pg_type t
JOIN pg_namespace n ON t.typnamespace = n.oid
WHERE n.nspname = $1 AND t.typtype IN ('c', 'e', 'r')
ORDER BY 1;

-- name: ListDomains :many
SELECT t.typname FROM pg_type t
JOIN pg_namespace n ON t.typnamespace = n.oid
WHERE n.nspname = $1 AND t.typtype = 'd'
ORDER BY 1;

-- name: CountSchemaObjects :many
SELECT 'tables' AS category, count(*) AS n FROM pg_tables t WHERE t.schemaname = $1
UNION ALL SELECT 'views', count(*) FROM pg_views v WHERE v.schemaname = $1
UNION ALL SELECT 'materialized-views', count(*) FROM pg_matviews m WHERE m.schemaname = $1
UNION ALL SELECT 'sequences', count(*) FROM pg_sequences s WHERE s.schemaname = $1
UNION ALL SELECT 'functions', count(*) FROM pg_proc p
  JOIN pg_namespace n ON p.pronamespace = n.oid
  WHERE n.nspname = $1 AND p.prokind = 'f'
UNION ALL SELECT 'procedures', count(*) FROM pg_proc p2
  JOIN pg_namespace n2 ON p2.pronamespace = n2.oid
  WHERE n2.nspname = $1 AND p2.prokind = 'p'
UNION ALL SELECT 'types', count(*) FROM pg_type t1
  JOIN pg_namespace n3 ON t1.typnamespace = n3.oid
  WHERE n3.nspname = $1 AND t1.typtype IN ('c', 'e', 'r')
UNION ALL SELECT 'domains', count(*) FROM pg_type t2
  JOIN pg_namespace n4 ON t2.typnamespace = n4.oid
  WHERE n4.nspname = $1 AND t2.typtype = 'd';

-- name: CountDatabaseObjects :many
SELECT 'casts' AS category, count(*) AS n FROM pg_cast
UNION ALL SELECT 'catalogs', count(*) FROM pg_namespace ns1 WHERE ns1.nspname = 'information_schema' OR ns1.nspname LIKE 'pg\_%'
UNION ALL SELECT 'event-triggers', count(*) FROM pg_event_trigger
UNION ALL SELECT 'extensions', count(*) FROM pg_extension
UNION ALL SELECT 'foreign-data-wrappers', count(*) FROM pg_foreign_data_wrapper
UNION ALL SELECT 'languages', count(*) FROM pg_language l WHERE l.lanispl
UNION ALL SELECT 'publications', count(*) FROM pg_publication
UNION ALL SELECT 'schemas', count(*) FROM pg_namespace ns2 WHERE ns2.nspname NOT LIKE 'pg\_%' AND ns2.nspname <> 'information_schema'
UNION ALL SELECT 'subscriptions', count(*) FROM pg_subscription sub WHERE sub.subdbid = (SELECT oid FROM pg_database WHERE datname = current_database());

-- Table-level category queries ($1 = schema name, $2 = table name)

-- name: GetTableColumns :many
SELECT column_name FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2
ORDER BY ordinal_position;

-- name: GetTableColumnsDetailed :many
SELECT
    c.column_name,
    c.data_type,
    c.is_nullable,
    c.column_default,
    c.character_maximum_length,
    COALESCE(coll.collname, '') AS collation,
    COALESCE(col_description(a.attrelid, a.attnum), '') AS comment
FROM information_schema.columns c
LEFT JOIN pg_attribute a ON a.attrelid = ($1 || '.' || $2)::regclass AND a.attname = c.column_name
LEFT JOIN pg_collation coll ON coll.oid = a.attcollation
WHERE c.table_schema = $1 AND c.table_name = $2
ORDER BY c.ordinal_position;

-- name: GetTableInfo :one
SELECT tableowner, tablespace
FROM pg_tables
WHERE schemaname = $1 AND tablename = $2
LIMIT 1;

-- name: GetViewDefinition :one
SELECT pg_get_viewdef(c.oid, true)::text AS definition
FROM pg_class c
JOIN pg_namespace n ON c.relnamespace = n.oid
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'v'
LIMIT 1;

-- name: GetPrimaryKeyColumns :many
SELECT c.conname, a.attname
FROM pg_constraint c
JOIN pg_class t ON c.conrelid = t.oid
JOIN pg_namespace n ON t.relnamespace = n.oid
JOIN unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
WHERE n.nspname = $1 AND t.relname = $2 AND c.contype = 'p'
ORDER BY k.ord;

-- name: ListTableColumns :many
SELECT column_name || ' ' || data_type FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2
ORDER BY ordinal_position;

-- name: ListConstraints :many
SELECT c.conname || ' (' || CASE c.contype
    WHEN 'p' THEN 'primary key'
    WHEN 'f' THEN 'foreign key'
    WHEN 'u' THEN 'unique'
    WHEN 'c' THEN 'check'
    ELSE c.contype::text
END || ')'
FROM pg_constraint c
JOIN pg_class t ON c.conrelid = t.oid
JOIN pg_namespace n ON t.relnamespace = n.oid
WHERE n.nspname = $1 AND t.relname = $2
ORDER BY 1;

-- name: ListTableIndexes :many
SELECT cls.relname FROM pg_index idx
JOIN pg_class cls ON cls.oid = idx.indexrelid
JOIN pg_class tbl ON tbl.oid = idx.indrelid
JOIN pg_namespace ns ON tbl.relnamespace = ns.oid
WHERE ns.nspname = $1 AND tbl.relname = $2
ORDER BY 1;

-- name: ListPolicies :many
SELECT policyname FROM pg_policies
WHERE schemaname = $1 AND tablename = $2
ORDER BY 1;

-- name: ListTableRules :many
SELECT rulename FROM pg_rules
WHERE schemaname = $1 AND tablename = $2
ORDER BY 1;

-- name: ListTableTriggers :many
SELECT tg.tgname FROM pg_trigger tg
JOIN pg_class t ON tg.tgrelid = t.oid
JOIN pg_namespace n ON t.relnamespace = n.oid
WHERE n.nspname = $1 AND t.relname = $2 AND NOT tg.tgisinternal
ORDER BY 1;

-- name: CountTableObjects :many
SELECT 'columns' AS category, count(*) AS n FROM information_schema.columns col WHERE col.table_schema = $1 AND col.table_name = $2
UNION ALL SELECT 'constraints', count(*) FROM pg_constraint con
  JOIN pg_class cls ON con.conrelid = cls.oid
  JOIN pg_namespace ns ON cls.relnamespace = ns.oid
  WHERE ns.nspname = $1 AND cls.relname = $2
UNION ALL SELECT 'indexes', count(*) FROM pg_index idx
  JOIN pg_class cls ON cls.oid = idx.indexrelid
  JOIN pg_class tbl ON tbl.oid = idx.indrelid
  JOIN pg_namespace ns ON tbl.relnamespace = ns.oid
  WHERE ns.nspname = $1 AND tbl.relname = $2
UNION ALL SELECT 'rls-policies', count(*) FROM pg_policies pol WHERE pol.schemaname = $1 AND pol.tablename = $2
UNION ALL SELECT 'rules', count(*) FROM pg_rules rl WHERE rl.schemaname = $1 AND rl.tablename = $2
UNION ALL SELECT 'triggers', count(*) FROM pg_trigger trg
  JOIN pg_class cls2 ON trg.tgrelid = cls2.oid
  JOIN pg_namespace ns2 ON cls2.relnamespace = ns2.oid
  WHERE ns2.nspname = $1 AND cls2.relname = $2 AND NOT trg.tgisinternal;

-- Object-properties queries (run against a specific database)

-- name: GetTableGeneral :one
SELECT
    t.tableowner,
    COALESCE(t.tablespace, 'pg_default') AS tablespace,
    COALESCE(obj_description(c.oid), '') AS comment,
    c.reltuples::bigint AS row_estimate,
    pg_total_relation_size(c.oid) AS table_size,
    t.hasindexes AS has_indexes,
    c.relkind::text = 'p' AS partitioned,
    c.relrowsecurity AS row_security
FROM pg_tables t
JOIN pg_class c ON c.relname = t.tablename
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = t.schemaname
WHERE t.schemaname = $1 AND t.tablename = $2
LIMIT 1;

-- name: GetViewGeneral :one
SELECT
    pg_get_userbyid(c.relowner) AS owner,
    pg_relation_size(c.oid) AS relation_size,
    COALESCE(obj_description(c.oid), '') AS comment,
    (SELECT count(*) FROM pg_attribute a
     WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped) AS column_count
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'v'
LIMIT 1;

-- name: GetTableConstraints :many
SELECT
    c.conname,
    c.contype::text AS constraint_type,
    pg_get_constraintdef(c.oid, true) AS definition,
    c.condeferrable,
    c.condeferred
FROM pg_constraint c
JOIN pg_class t ON c.conrelid = t.oid
JOIN pg_namespace n ON t.relnamespace = n.oid
WHERE n.nspname = $1 AND t.relname = $2
ORDER BY c.conname;

-- name: GetTableIndexesDetailed :many
SELECT
    c.relname AS index_name,
    pg_get_indexdef(i.indexrelid, 0, true) AS definition,
    i.indisunique AS is_unique,
    COALESCE(t.spcname, 'pg_default') AS tablespace,
    COALESCE(obj_description(i.indexrelid), '') AS comment
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class tc ON tc.oid = i.indrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
LEFT JOIN pg_tablespace t ON t.oid = c.reltablespace
WHERE n.nspname = $1 AND tc.relname = $2
ORDER BY c.relname;

-- name: GetTableStatistics :one
SELECT
    s.seq_scan, s.seq_tup_read, s.idx_scan, s.idx_tup_fetch,
    s.n_tup_ins, s.n_tup_upd, s.n_tup_del,
    s.n_live_tup, s.n_dead_tup,
    s.last_vacuum, s.last_autovacuum, s.last_analyze, s.last_autoanalyze
FROM pg_stat_user_tables s
WHERE s.schemaname = $1 AND s.relname = $2
LIMIT 1;

-- name: GetObjectPrivileges :many
SELECT
    grantee,
    privilege_type,
    is_grantable
FROM information_schema.table_privileges
WHERE table_schema = $1 AND table_name = $2
ORDER BY grantee, privilege_type;

-- name: GetObjectDependencies :many
SELECT 'view' AS kind,
    dep_ns.nspname || '.' || dep_rel.relname AS name,
    '' AS detail
FROM pg_depend d
JOIN pg_rewrite r ON r.oid = d.objid
JOIN pg_class dep_rel ON dep_rel.oid = r.ev_class
JOIN pg_namespace dep_ns ON dep_ns.oid = dep_rel.relnamespace
JOIN pg_class src ON src.oid = d.refobjid
JOIN pg_namespace src_ns ON src_ns.oid = src.relnamespace
WHERE src_ns.nspname = $1 AND src.relname = $2 AND dep_rel.relkind = 'v'
UNION ALL
SELECT 'foreign key',
    ref_ns.nspname || '.' || ref_rel.relname,
    con.conname
FROM pg_constraint con
JOIN pg_class key_rel ON key_rel.oid = con.confrelid
JOIN pg_namespace key_ns ON key_ns.oid = key_rel.relnamespace
JOIN pg_class ref_rel ON ref_rel.oid = con.conrelid
JOIN pg_namespace ref_ns ON ref_ns.oid = ref_rel.relnamespace
WHERE key_ns.nspname = $1 AND key_rel.relname = $2 AND con.contype = 'f'
UNION ALL
SELECT 'index',
    n.nspname || '.' || c.relname,
    ''
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class tc ON tc.oid = i.indrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
WHERE n.nspname = $1 AND tc.relname = $2
ORDER BY 1, 2;
