-- name: GetServerByID :one
SELECT * FROM server
WHERE id = ? AND user_id = ?
LIMIT 1;

-- name: GetUserByEmail :one
SELECT id, email, password, active, confirmed_at
FROM "user"
WHERE email = ?
LIMIT 1;

-- name: GetUserByToken :one
SELECT id, email, password, active, confirmed_at
FROM "user"
WHERE session_token = ?
  AND session_token != ''
LIMIT 1;

-- name: SetUserSessionToken :exec
UPDATE "user" SET session_token = ?
WHERE id = ?;

-- name: ListServersByGroup :many
SELECT 
    s.id, 
    s.name, 
    s.host, 
    s.port, 
    s.maintenance_db, 
    s.username, 
    sg.name AS group_name
FROM server s
INNER JOIN servergroup sg ON s.servergroup_id = sg.id
WHERE s.user_id = ?
ORDER BY sg.name, s.name;

-- name: CreateServer :one
INSERT INTO server (
    user_id, servergroup_id, name, host, port, maintenance_db, username, ssl_mode
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetUserWorkspace :one
SELECT * FROM user_workspaces
WHERE user_id = ?
LIMIT 1;

-- name: SaveUserWorkspace :exec
INSERT INTO user_workspaces (user_id, active_tab_id, layout_metadata, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id) DO UPDATE SET
    active_tab_id = excluded.active_tab_id,
    layout_metadata = excluded.layout_metadata,
    updated_at = CURRENT_TIMESTAMP;

-- name: ListWorkspaceTabs :many
SELECT * FROM workspace_tabs
WHERE user_id = ?
ORDER BY tab_order, created_at;

-- name: ReplaceWorkspaceTabs :exec
DELETE FROM workspace_tabs
WHERE user_id = ?;

-- name: InsertWorkspaceTab :exec
INSERT INTO workspace_tabs (id, user_id, title, connection_id, query_text, file_path, tab_order, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: ListQueryHistory :many
SELECT id, user_id, tab_id, connection_id, query_text, executed_at, duration_ms, status, rows_affected
FROM query_history
WHERE user_id = ?
  AND (? = '' OR connection_id = ?)
ORDER BY executed_at DESC, id DESC
LIMIT 50;

-- name: GetQueryHistoryByID :one
SELECT id, user_id, tab_id, connection_id, query_text, executed_at, duration_ms, status, rows_affected
FROM query_history
WHERE id = ? AND user_id = ?
LIMIT 1;

-- name: RecordQueryHistory :exec
INSERT INTO query_history (user_id, tab_id, connection_id, query_text, executed_at, duration_ms, status, rows_affected)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?);

-- name: SetDatabaseDisconnected :exec
INSERT INTO disconnected_database (server_id, db_name)
VALUES (?, ?)
ON CONFLICT (server_id, db_name) DO NOTHING;

-- name: ClearDatabaseDisconnected :exec
DELETE FROM disconnected_database WHERE server_id = ? AND db_name = ?;

-- name: ClearDatabaseDisconnectedForServer :exec
DELETE FROM disconnected_database WHERE server_id = ?;

-- name: ListDisconnectedDatabases :many
SELECT server_id, db_name FROM disconnected_database;