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

-- name: ListEncodings :many
SELECT name FROM pg_character_set
ORDER BY name;

-- name: ListDatabaseTemplates :many
SELECT datname FROM pg_database
WHERE datistemplate
ORDER BY datname;

-- name: GetCurrentUser :one
SELECT current_user;

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

-- name: ListAvailableExtensions :many
SELECT name FROM pg_available_extensions
WHERE name NOT IN (SELECT extname FROM pg_extension)
ORDER BY 1;

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

-- name: GetMatViewColumnsDetailed :many
SELECT
    a.attname AS column_name,
    format_type(a.atttypid, a.atttypmod) AS data_type,
    CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END AS is_nullable,
    COALESCE(pg_get_expr(d.adbin, d.adrelid), '') AS column_default,
    CASE
        WHEN a.atttypid IN ('varchar'::regtype, 'bpchar'::regtype) THEN NULLIF(a.atttypmod, -1) - 4
        ELSE NULL
    END AS character_maximum_length,
    COALESCE(colsh.collname, '') AS collation,
    COALESCE(col_description(a.attrelid, a.attnum), '') AS comment
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
LEFT JOIN pg_collation colsh ON colsh.oid = a.attcollation
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'm'
  AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum;

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

-- name: GetMatViewGeneral :one
SELECT
    pg_get_userbyid(c.relowner) AS owner,
    pg_total_relation_size(c.oid) AS relation_size,
    c.reltuples::bigint AS row_estimate,
    c.relhasindex AS has_indexes,
    COALESCE(obj_description(c.oid), '') AS comment,
    (SELECT count(*) FROM pg_attribute a
     WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped) AS column_count
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'm'
LIMIT 1;

-- name: GetMatViewDefinition :one
SELECT pg_get_viewdef(c.oid, true)::text AS definition
FROM pg_class c
JOIN pg_namespace n ON c.relnamespace = n.oid
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'm'
LIMIT 1;

-- name: GetSequenceGeneral :one
SELECT
    s.data_type,
    s.start_value,
    s.min_value,
    s.max_value,
    s.increment_by,
    s.cycle,
    s.cache_size,
    s.last_value,
    pg_get_userbyid(c.relowner) AS owner,
    pg_relation_size(c.oid) AS relation_size,
    COALESCE(obj_description(c.oid), '') AS comment
FROM pg_sequences s
JOIN pg_class c ON c.relname = s.sequencename AND c.relkind = 'S'
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = s.schemaname
WHERE s.schemaname = $1 AND s.sequencename = $2
LIMIT 1;

-- name: GetFunctionGeneral :one
SELECT
    p.prokind::text AS kind,
    pg_get_function_identity_arguments(p.oid) AS identity_arguments,
    pg_get_function_arguments(p.oid) AS arguments,
    pg_get_function_result(p.oid) AS return_type,
    l.lanname AS language,
    CASE p.provolatile WHEN 'i' THEN 'immutable' WHEN 's' THEN 'stable' ELSE 'volatile' END AS volatility,
    p.prosecdef AS security_definer,
    p.proisstrict AS strict,
    CASE p.proparallel WHEN 's' THEN 'safe' WHEN 'r' THEN 'restricted' ELSE 'unsafe' END AS parallel,
    pg_get_userbyid(p.proowner) AS owner,
    COALESCE(obj_description(p.oid, 'pg_proc'), '') AS comment
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
JOIN pg_language l ON l.oid = p.prolang
WHERE n.nspname = $1 AND (p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')') = $2
LIMIT 1;

-- name: GetFunctionDefinition :one
SELECT pg_get_functiondef(p.oid) AS definition
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
WHERE n.nspname = $1 AND (p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')') = $2
LIMIT 1;

-- name: GetIndexGeneral :one
SELECT
    c.relname AS index_name,
    pg_get_indexdef(i.indexrelid, 0, true) AS definition,
    i.indisunique AS is_unique,
    COALESCE(t.spcname, 'pg_default') AS tablespace,
    am.amname AS access_method,
    pg_get_userbyid(c.relowner) AS owner,
    pg_relation_size(i.indexrelid) AS relation_size,
    COALESCE(obj_description(i.indexrelid), '') AS comment
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class tc ON tc.oid = i.indrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
LEFT JOIN pg_tablespace t ON t.oid = c.reltablespace
JOIN pg_am am ON am.oid = c.relam
WHERE n.nspname = $1 AND tc.relname = $2 AND c.relname = $3
LIMIT 1;

-- name: GetIndexColumns :many
SELECT
    COALESCE(pg_get_indexdef(i.indexrelid, g.ord::int, true), '') AS column_def
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class tc ON tc.oid = i.indrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
JOIN generate_series(1, i.indnkeyatts) AS g(ord) ON true
WHERE n.nspname = $1 AND tc.relname = $2 AND c.relname = $3
ORDER BY g.ord;

-- name: GetTriggerGeneral :one
SELECT
    t.tgname,
    CASE
        WHEN t.tgtype & 2 = 2 THEN 'BEFORE'
        WHEN t.tgtype & 64 = 64 THEN 'INSTEAD OF'
        ELSE 'AFTER'
    END AS timing,
    rtrim(
        CASE WHEN t.tgtype & 4 = 4 THEN 'INSERT ' ELSE '' END ||
        CASE WHEN t.tgtype & 8 = 8 THEN 'DELETE ' ELSE '' END ||
        CASE WHEN t.tgtype & 16 = 16 THEN 'UPDATE ' ELSE '' END ||
        CASE WHEN t.tgtype & 32 = 32 THEN 'TRUNCATE ' ELSE '' END
    ) AS events,
    CASE t.tgenabled
        WHEN 'O' THEN 'origin'
        WHEN 'D' THEN 'disabled'
        WHEN 'R' THEN 'replica'
        WHEN 'A' THEN 'always'
        ELSE t.tgenabled::text
    END AS enabled,
    t.tgfoid::regproc::text AS function_name,
    quote_ident(n.nspname) || '.' || quote_ident(tc.relname) AS table_name,
    pg_get_triggerdef(t.oid) AS definition,
    COALESCE(obj_description(t.oid, 'pg_trigger'), '') AS comment
FROM pg_trigger t
JOIN pg_class tc ON tc.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
WHERE n.nspname = $1 AND tc.relname = $2 AND t.tgname = $3 AND NOT t.tgisinternal
LIMIT 1;

-- name: GetSchemaGeneral :one
SELECT
    pg_get_userbyid(n.nspowner) AS owner,
    COALESCE(obj_description(n.oid), '') AS comment
FROM pg_namespace n
WHERE n.nspname = $1
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

-- Plain-leaf object properties queries ("General + SQL" panel kinds without
-- their own DDL yet: columns, constraints, RLS policies, rules, types,
-- domains, casts, catalogs, event triggers, foreign data wrappers, languages,
-- subscriptions).

-- name: GetColumnDetail :one
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
WHERE c.table_schema = $1 AND c.table_name = $2 AND c.column_name = $3
LIMIT 1;

-- name: GetConstraintDetail :one
SELECT
    c.conname,
    c.contype::text AS constraint_type,
    pg_get_constraintdef(c.oid, true) AS definition,
    c.condeferrable,
    c.condeferred
FROM pg_constraint c
JOIN pg_class t ON c.conrelid = t.oid
JOIN pg_namespace n ON t.relnamespace = n.oid
WHERE n.nspname = $1 AND t.relname = $2 AND c.conname = $3
LIMIT 1;

-- name: GetPolicyGeneral :one
SELECT
    p.permissive,
    array_to_string(p.roles, ', ') AS roles,
    p.cmd,
    COALESCE(p.qual, '') AS using_expr,
    COALESCE(p.with_check, '') AS with_check_expr
FROM pg_policies p
WHERE p.schemaname = $1 AND p.tablename = $2 AND p.policyname = $3
LIMIT 1;

-- name: GetRuleDefinition :one
SELECT r.definition
FROM pg_rules r
WHERE r.schemaname = $1 AND r.tablename = $2 AND r.rulename = $3
LIMIT 1;

-- name: GetTypeGeneral :one
SELECT
    t.oid AS type_oid,
    t.typtype::text AS type_category,
    pg_get_userbyid(t.typowner) AS owner,
    COALESCE(obj_description(t.oid, 'pg_type'), '') AS comment
FROM pg_type t
JOIN pg_namespace n ON t.typnamespace = n.oid
WHERE n.nspname = $1 AND t.typname = $2 AND t.typtype IN ('c', 'e', 'r')
LIMIT 1;

-- name: ListEnumLabels :many
SELECT e.enumlabel FROM pg_enum e
WHERE e.enumtypid = $1
ORDER BY e.enumsortorder;

-- name: ListCompositeAttributes :many
SELECT a.attname, format_type(a.atttypid, a.atttypmod) AS data_type
FROM pg_attribute a
WHERE a.attrelid = (SELECT typrelid FROM pg_type WHERE oid = $1)
  AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum;

-- name: GetRangeSubtype :one
SELECT format_type(r.rngsubtype, NULL) AS subtype
FROM pg_range r
WHERE r.rngtypid = $1
LIMIT 1;

-- name: GetDomainGeneral :one
SELECT
    t.oid AS type_oid,
    format_type(t.typbasetype, t.typtypmod) AS base_type,
    t.typnotnull AS not_null,
    COALESCE(t.typdefault, '') AS default_value,
    pg_get_userbyid(t.typowner) AS owner,
    COALESCE(obj_description(t.oid, 'pg_type'), '') AS comment
FROM pg_type t
JOIN pg_namespace n ON t.typnamespace = n.oid
WHERE n.nspname = $1 AND t.typname = $2 AND t.typtype = 'd'
LIMIT 1;

-- name: ListDomainConstraints :many
SELECT c.conname, pg_get_constraintdef(c.oid, true) AS definition
FROM pg_constraint c
WHERE c.contypid = $1
ORDER BY c.conname;

-- name: ListCastsDetailed :many
SELECT castsource::regtype::text AS source_type, casttarget::regtype::text AS target_type
FROM pg_cast
ORDER BY 1, 2;

-- name: GetCastGeneral :one
SELECT
    CASE c.castcontext WHEN 'e' THEN 'explicit' WHEN 'a' THEN 'assignment' ELSE 'implicit' END AS context,
    CASE c.castmethod WHEN 'f' THEN 'function' WHEN 'i' THEN 'inout' ELSE 'binary coercible' END AS method,
    c.castfunc::regproc::text AS function_name,
    COALESCE(obj_description(c.oid, 'pg_cast'), '') AS comment
FROM pg_cast c
WHERE c.castsource = $1::regtype AND c.casttarget = $2::regtype
LIMIT 1;

-- name: GetEventTriggerGeneral :one
SELECT
    t.evtevent,
    CASE t.evtenabled
        WHEN 'O' THEN 'origin' WHEN 'D' THEN 'disabled'
        WHEN 'R' THEN 'replica' WHEN 'A' THEN 'always'
        ELSE t.evtenabled::text
    END AS enabled,
    pg_get_userbyid(t.evtowner) AS owner,
    t.evtfoid::regproc::text AS function_name,
    COALESCE(array_to_string(t.evttags, ', '), '') AS tags,
    COALESCE(obj_description(t.oid, 'pg_event_trigger'), '') AS comment
FROM pg_event_trigger t
WHERE t.evtname = $1
LIMIT 1;

-- name: GetForeignDataWrapperGeneral :one
SELECT
    pg_get_userbyid(f.fdwowner) AS owner,
    f.fdwhandler::regproc::text AS handler,
    f.fdwvalidator::regproc::text AS validator,
    COALESCE(array_to_string(f.fdwoptions, ', '), '') AS options,
    COALESCE(obj_description(f.oid, 'pg_foreign_data_wrapper'), '') AS comment
FROM pg_foreign_data_wrapper f
WHERE f.fdwname = $1
LIMIT 1;

-- name: GetLanguageGeneral :one
SELECT
    l.lanpltrusted AS trusted,
    pg_get_userbyid(l.lanowner) AS owner,
    l.lanplcallfoid::regproc::text AS call_handler,
    l.laninline::regproc::text AS inline_handler,
    l.lanvalidator::regproc::text AS validator,
    COALESCE(obj_description(l.oid, 'pg_language'), '') AS comment
FROM pg_language l
WHERE l.lanname = $1
LIMIT 1;

-- name: GetSubscriptionGeneral :one
SELECT
    pg_get_userbyid(s.subowner) AS owner,
    s.subenabled AS enabled,
    array_to_string(s.subpublications, ', ') AS publications,
    COALESCE(s.subslotname, '') AS slot_name,
    COALESCE(obj_description(s.oid, 'pg_subscription'), '') AS comment
FROM pg_subscription s
WHERE s.subdbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND s.subname = $1
LIMIT 1;
