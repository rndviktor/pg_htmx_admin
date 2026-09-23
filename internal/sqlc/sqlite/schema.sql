-- SQLite Schema for pgAdmin 4 (pgadmin4.db)

CREATE TABLE IF NOT EXISTS user (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email VARCHAR(256) NOT NULL UNIQUE,
    password VARCHAR(256) NOT NULL,
    active BOOLEAN NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    confirmed_at DATETIME,
    session_token VARCHAR(64) NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS servergroup (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS server (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    servergroup_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    host VARCHAR(128) NOT NULL,
    port INTEGER NOT NULL DEFAULT 5432 CHECK (port >= 1 AND port <= 65535),
    maintenance_db VARCHAR(64) NOT NULL,
    username VARCHAR(64) NOT NULL,
    password VARCHAR(256),
    role VARCHAR(64),
    ssl_mode VARCHAR(32) NOT NULL DEFAULT 'prefer',
    comment TEXT,
    discovery_id VARCHAR(128),
    hostaddr VARCHAR(128),
    db_res VARCHAR(64),
    passfile VARCHAR(256),
    sslcert VARCHAR(256),
    keyfile VARCHAR(256),
    rootcert VARCHAR(256),
    crlfile VARCHAR(256),
    service VARCHAR(64),
    bgcolor VARCHAR(10),
    fgcolor VARCHAR(10),
    connect_timeout INTEGER DEFAULT 10,
    use_ssh_tunnel INTEGER DEFAULT 0 CHECK (use_ssh_tunnel IN (0, 1)),
    ssh_host VARCHAR(128),
    ssh_port INTEGER DEFAULT 22,
    ssh_username VARCHAR(64),
    ssh_password VARCHAR(256),
    ssh_keyfile VARCHAR(256),
    shared BOOLEAN DEFAULT 0 CHECK (shared IN (0, 1)),
    disconnected BOOLEAN NOT NULL DEFAULT 0 CHECK (disconnected IN (0, 1)),
    FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE,
    FOREIGN KEY (servergroup_id) REFERENCES servergroup(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS user_preferences (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    preference_id INTEGER NOT NULL,
    value TEXT NOT NULL,
    FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS process (
    pid INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    desc VARCHAR(256) NOT NULL,
    type VARCHAR(64) NOT NULL,
    status INTEGER NOT NULL,
    start_time DATETIME NOT NULL,
    end_time DATETIME,
    log_dir VARCHAR(512),
    utility_path VARCHAR(512),
    FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS setting (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    setting VARCHAR(128) NOT NULL,
    value TEXT,
    FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS user_workspaces (
    user_id INTEGER PRIMARY KEY REFERENCES user(id),
    active_tab_id TEXT NOT NULL,
    layout_metadata TEXT DEFAULT '{}', -- sidebar width, expanded nodes
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS workspace_tabs (
    id TEXT PRIMARY KEY, -- client or server generated UUID
    user_id INTEGER REFERENCES user(id) ON DELETE CASCADE,
    title VARCHAR(255) NOT NULL,
    connection_id TEXT, -- target DB connection
    query_text TEXT DEFAULT '',
    file_path TEXT DEFAULT '',
    tab_order INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS disconnected_database (
    server_id INTEGER NOT NULL,
    db_name VARCHAR(64) NOT NULL,
    PRIMARY KEY (server_id, db_name),
    FOREIGN KEY (server_id) REFERENCES server(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS query_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES user(id) ON DELETE CASCADE,
    tab_id TEXT,
    connection_id TEXT,
    query_text TEXT NOT NULL,
    executed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    duration_ms INTEGER,
    status VARCHAR(50),
    rows_affected INTEGER
);
