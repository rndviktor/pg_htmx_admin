// -----------------------------------------------------------------------------
// Shared constants and pure helpers used across the classic-script app modules.
// -----------------------------------------------------------------------------

// Number of rows fetched per page by the query tool.
const QUERY_PAGE_LIMIT = "1000";

// Tab / DOM identifiers.
const TAB_DASHBOARD = "dashboard";
const TAB_CONTENT_PREFIX = "tab-content-";
const ID_TAB_BAR = "tab-bar";
const ID_TREE_ROOT = "tree-root";
const ID_SERVERS_GROUP = "servers-group";
const ID_QUERY_EDITOR = "query-editor";

// Hidden form parameter names used to scope a query to a connection.
const PARAM_SERVER_ID = "server_id";
const PARAM_DB_NAME = "db_name";

// -----------------------------------------------------------------------------
// URL helpers for tree-node URLs of the form
//   /api/servers/{id}/databases/{db}/schemas/{schema}/tables/{table}
// -----------------------------------------------------------------------------

// Extracts {serverID, dbName} from the given URL path. Fields are undefined
// when the URL is not database-scoped.
function parseServerDBURL(url) {
    const parts = (url || "").split("/");
    return { serverID: parts[3], dbName: parts[5] };
}

// Looks up the display name of a registered server from the loaded object
// tree (server buttons carry hx-get="/api/servers/{id}/..."). The server
// button renders "[state dot] 🖥️ Name [host line]", so the label is its
// first non-empty text node with the leading icon token stripped. Returns
// null when the tree is not rendered yet.
function serverNameForID(serverID) {
    if (!serverID) return null;
    const btn = document.querySelector(
        '#' + ID_TREE_ROOT + " button[hx-get^='/api/servers/" + serverID + "/']");
    if (!btn) return null;
    const label = Array.from(btn.childNodes)
        .filter((n) => n.nodeType === Node.TEXT_NODE)
        .map((n) => n.textContent.trim())
        .find((t) => t.length > 0);
    return label ? label.replace(/^\S+\s+/, "") : null;
}

// Returns {serverID, serverName, dbName} for a database-scoped URL,
// otherwise null. The server name is resolved from the loaded tree so the
// caller can label connections without an extra lookup.
function connectionFromTreeURL(url) {
    const conn = parseServerDBURL(url);
    if (!conn.serverID || !conn.dbName) return null;
    conn.serverName = serverNameForID(conn.serverID);
    return conn;
}

// Extracts the DDL context {serverID, dbName, schema, table} from a tree URL.
// Handles both folder URLs (e.g. .../databases/{db}/extensions or
// .../tables/{table}/indexes) and object URLs (a trailing /children or
// /properties is tolerated). Fields are empty when absent from the path.
function parseObjectContext(url) {
    const path = (url || "").replace(/\/properties$/, "").replace(/\/children$/, "");
    const parts = path.split("/");
    const ctx = { serverID: parts[3] || "", dbName: parts[5] || "", schema: "", table: "" };
    const schemaIdx = parts.indexOf("schemas");
    if (schemaIdx >= 0 && parts[schemaIdx + 1]) ctx.schema = parts[schemaIdx + 1];
    const tableIdx = parts.indexOf("tables");
    if (tableIdx >= 0 && parts[tableIdx + 1]) ctx.table = parts[tableIdx + 1];
    return ctx;
}

// Derives an object name from its tree URL (last path segment), used as a
// fallback when a node carries no data-name attribute.
function objectNameFromURL(url) {
    const path = (url || "").replace(/\/properties$/, "").replace(/\/children$/, "");
    const parts = path.split("/");
    const last = parts[parts.length - 1];
    return last ? decodeURIComponent(last) : "";
}

// Parses a table URL ending in /.../tables/{table} (with or without a trailing
// /children). Returns the base URL, the table name and the connection parts.
function tableURLParts(tableURL) {
    const baseURL = tableURL.replace(/\/children$/, "");
    const parts = baseURL.split("/");
    return {
        url: baseURL,
        tableName: parts.pop(),
        serverID: parts[3],
        serverName: serverNameForID(parts[3]),
        dbName: parts[5],
    };
}

// -----------------------------------------------------------------------------
// Misc formatting helpers.
// -----------------------------------------------------------------------------

// Formats an elapsed-time delta (ms) as a seconds string.
function elapsedSeconds(t0) {
    return ((performance.now() - t0) / 1000).toFixed(3);
}

// Pluralizes a row count label ("0 rows", "1 row", "2 rows").
function rowLabel(n) {
    return n + " row" + (n !== 1 ? "s" : "");
}