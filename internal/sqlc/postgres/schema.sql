-- Minimal stubs for PostgreSQL system catalog tables referenced by queries.sql.
-- These tables exist in every PostgreSQL instance; the stubs let sqlc parse the
-- queries and generate correct Go return types.

CREATE TABLE pg_database (
    oid SERIAL PRIMARY KEY,
    datname TEXT NOT NULL,
    datallowconn BOOLEAN NOT NULL,
    datistemplate BOOLEAN NOT NULL
);

CREATE TABLE pg_roles (
    rolname TEXT NOT NULL
);

CREATE TABLE pg_tablespace (
    oid SERIAL PRIMARY KEY,
    spcname TEXT NOT NULL
);

CREATE TABLE pg_cast (
    castsource INTEGER NOT NULL,
    casttarget INTEGER NOT NULL
);

CREATE TABLE pg_namespace (
    oid SERIAL PRIMARY KEY,
    nspname TEXT NOT NULL,
    nspowner INTEGER NOT NULL,
    nspacl aclitem[]
);

CREATE TABLE pg_event_trigger (
    evtname TEXT NOT NULL
);

CREATE TABLE pg_extension (
    extname TEXT NOT NULL
);

CREATE TABLE pg_available_extensions (
    name TEXT NOT NULL,
    default_version TEXT NOT NULL
);

CREATE TABLE pg_foreign_data_wrapper (
    fdwname TEXT NOT NULL
);

CREATE TABLE pg_language (
    lanname TEXT NOT NULL,
    lanispl BOOLEAN NOT NULL
);

CREATE TABLE pg_publication (
    pubname TEXT NOT NULL
);

CREATE TABLE pg_subscription (
    subname TEXT NOT NULL,
    subdbid INTEGER NOT NULL
);

CREATE TABLE pg_tables (
    tablename TEXT NOT NULL,
    schemaname TEXT NOT NULL,
    tableowner TEXT NOT NULL,
    tablespace TEXT,
    hasindexes BOOLEAN NOT NULL
);

CREATE TABLE pg_views (
    viewname TEXT NOT NULL,
    schemaname TEXT NOT NULL
);

CREATE TABLE pg_matviews (
    matviewname TEXT NOT NULL,
    schemaname TEXT NOT NULL
);

CREATE TABLE pg_sequences (
    sequencename TEXT NOT NULL,
    schemaname TEXT NOT NULL,
    data_type TEXT,
    start_value BIGINT,
    min_value BIGINT,
    max_value BIGINT,
    increment_by BIGINT,
    cycle BOOLEAN,
    cache_size BIGINT,
    last_value BIGINT
);

CREATE TABLE pg_proc (
    oid SERIAL PRIMARY KEY,
    proname TEXT NOT NULL,
    pronamespace INTEGER NOT NULL,
    prokind CHAR NOT NULL,
    prolang INTEGER NOT NULL,
    provolatile CHAR NOT NULL,
    prosecdef BOOLEAN NOT NULL,
    proisstrict BOOLEAN NOT NULL,
    proparallel CHAR NOT NULL,
    proowner INTEGER NOT NULL
);

CREATE TABLE pg_type (
    typname TEXT NOT NULL,
    typtype CHAR NOT NULL,
    typnamespace INTEGER NOT NULL
);

CREATE TABLE pg_class (
    oid SERIAL PRIMARY KEY,
    relname TEXT NOT NULL,
    relnamespace INTEGER NOT NULL,
    relkind CHAR NOT NULL,
    relowner INTEGER NOT NULL,
    reltablespace INTEGER NOT NULL,
    relam INTEGER NOT NULL,
    reltuples DOUBLE PRECISION NOT NULL,
    relhasindex BOOLEAN NOT NULL,
    relrowsecurity BOOLEAN NOT NULL,
    relacl aclitem[]
);

CREATE TABLE pg_constraint (
    oid SERIAL PRIMARY KEY,
    conname TEXT NOT NULL,
    contype CHAR NOT NULL,
    conrelid INTEGER NOT NULL,
    confrelid INTEGER NOT NULL,
    conkey INTEGER[],
    condeferrable BOOLEAN NOT NULL,
    condeferred BOOLEAN NOT NULL
);

CREATE TABLE pg_attribute (
    attrelid INTEGER NOT NULL,
    attname TEXT NOT NULL,
    attnum INTEGER NOT NULL,
    atttypid INTEGER NOT NULL,
    attcollation INTEGER NOT NULL,
    attnotnull BOOLEAN NOT NULL,
    atttypmod INTEGER NOT NULL,
    attisdropped BOOLEAN NOT NULL
);

CREATE TABLE pg_attrdef (
    adrelid INTEGER NOT NULL,
    adnum INTEGER NOT NULL,
    adbin TEXT NOT NULL
);

CREATE TABLE pg_index (
    indexrelid INTEGER NOT NULL,
    indrelid INTEGER NOT NULL,
    indisunique BOOLEAN NOT NULL,
    indisprimary BOOLEAN NOT NULL,
    indnkeyatts INTEGER NOT NULL
);

CREATE TABLE pg_depend (
    classid INTEGER NOT NULL,
    objid INTEGER NOT NULL,
    refclassid INTEGER NOT NULL,
    refobjid INTEGER NOT NULL,
    refobjsubid INTEGER NOT NULL,
    deptype CHAR NOT NULL
);

CREATE TABLE pg_rewrite (
    oid INTEGER NOT NULL,
    ev_class INTEGER NOT NULL
);

CREATE TABLE pg_stat_user_tables (
    schemaname TEXT NOT NULL,
    relname TEXT NOT NULL,
    seq_scan BIGINT NOT NULL,
    seq_tup_read BIGINT NOT NULL,
    idx_scan BIGINT NOT NULL,
    idx_tup_fetch BIGINT NOT NULL,
    n_tup_ins BIGINT NOT NULL,
    n_tup_upd BIGINT NOT NULL,
    n_tup_del BIGINT NOT NULL,
    n_live_tup BIGINT NOT NULL,
    n_dead_tup BIGINT NOT NULL,
    last_vacuum TIMESTAMPTZ,
    last_autovacuum TIMESTAMPTZ,
    last_analyze TIMESTAMPTZ,
    last_autoanalyze TIMESTAMPTZ
);

CREATE TABLE pg_collation (
    oid INTEGER NOT NULL,
    collname TEXT NOT NULL
);

CREATE TABLE pg_trigger (
    tgname TEXT NOT NULL,
    tgrelid INTEGER NOT NULL,
    tgisinternal BOOLEAN NOT NULL,
    tgtype INTEGER NOT NULL,
    tgenabled CHAR NOT NULL,
    tgfoid INTEGER NOT NULL
);

CREATE TABLE pg_am (
    oid INTEGER NOT NULL,
    amname TEXT NOT NULL
);

CREATE TABLE pg_policies (
    policyname TEXT NOT NULL,
    schemaname TEXT NOT NULL,
    tablename TEXT NOT NULL
);

CREATE TABLE pg_rules (
    rulename TEXT NOT NULL,
    schemaname TEXT NOT NULL,
    tablename TEXT NOT NULL
);

-- Maps PostgreSQL internal encoding numbers to character set names, used to
-- offer the cluster's available encodings as a dropdown in the CREATE DATABASE
-- dialog.
CREATE TABLE pg_character_set (
    encoding INTEGER NOT NULL,
    name TEXT NOT NULL
);


