package web

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"htmx-golang-excercise/internal/db"
	sqlite "htmx-golang-excercise/internal/sqlc/sqlite/db"
)

type ServerConfig struct {
	ID       string
	Name     string
	Host     string
	Port     int
	DBName   string
	Username string
	Password string
	SslMode  string
}

func (c ServerConfig) DSN() string {
	sslMode := c.SslMode
	if sslMode == "" {
		sslMode = "disable"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.Username, c.Password, c.Host, c.Port, c.DBName, sslMode)
}

// validSslModes are the libpq sslmode values accepted from the register form.
var validSslModes = map[string]bool{"disable": true, "prefer": true, "require": true}

// Global/Server-level pool storage map
var (
	serverPools = make(map[string]*pgxpool.Pool)
	// serverConfigs holds in-memory configs registered during this session.
	serverConfigs = make(map[string]ServerConfig)
	// dbPools caches pools for the maintenance DB, keyed by SQLite server row id.
	dbPools = make(map[int64]*pgxpool.Pool)
	// dbSpecificPools caches pools connected to an individual database of a
	// server (database-level tree nodes must query that database's catalogs,
	// not the server's maintenance DB).
	dbSpecificPools = make(map[dbPoolKey]*pgxpool.Pool)
	// disconnectedServers tracks servers the user explicitly disconnected
	// from. They show a gray dot, cannot be expanded, and are not re-connected
	// automatically on page refresh until the user reconnects them. The flag is
	// also persisted in the server table so it survives across restarts.
	disconnectedServers = make(map[int64]bool)
	mu                  sync.RWMutex
)

type dbPoolKey struct {
	ServerID int64
	Database string
}

// getOrCreatePool returns a cached pgx pool for the SQLite server row id,
// creating it on first use from the stored connection settings. Rows created
// before passwords were persisted have a NULL password; for those, fall back
// to a password captured in-memory when the server was registered.
func (s *Server) getOrCreatePool(ctx context.Context, id int64) (*pgxpool.Pool, error) {
	mu.RLock()
	pool, ok := dbPools[id]
	mu.RUnlock()
	if ok {
		return pool, nil
	}

	srv, err := s.DB.GetServerByID(ctx, sqlite.GetServerByIDParams{ID: id, UserID: db.DefaultUserID})
	if err != nil {
		return nil, fmt.Errorf("server %d not found", id)
	}

	pool, err = dialServer(ctx, srv, srv.MaintenanceDb)
	if err != nil {
		return nil, err
	}

	mu.Lock()
	if existing, ok := dbPools[id]; ok {
		mu.Unlock()
		pool.Close()
		return existing, nil
	}
	dbPools[id] = pool
	mu.Unlock()
	log.Printf("Connected to server %d (%s:%d/%s)", id, srv.Host, srv.Port, srv.MaintenanceDb)

	return pool, nil
}

// probeServer reports whether a live connection can be established to the
// maintenance database of a registered server. It reuses the cached pool when
// one exists so repeated probes in the sidebar stay cheap.
func (s *Server) probeServer(ctx context.Context, id int64) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pool, err := s.getOrCreatePool(probeCtx, id)
	if err != nil {
		return false
	}
	return pool.Ping(probeCtx) == nil
}

// peekCachedPool returns the cached pgx pool for a registered server without
// dialing it. It returns nil when the server has not been connected to yet.
// Used by server-level tree endpoints to enrich folders with counts without
// incurring connection latency (or blocking on unreachable servers).
func (s *Server) peekCachedPool(id int64) *pgxpool.Pool {
	mu.RLock()
	defer mu.RUnlock()
	return dbPools[id]
}

// getOrCreateDbPool returns a cached pgx pool connected to one specific
// database of a registered server. Used by database-level tree endpoints,
// whose catalog queries must run against that database itself.
func (s *Server) getOrCreateDbPool(ctx context.Context, id int64, database string) (*pgxpool.Pool, error) {
	key := dbPoolKey{ServerID: id, Database: database}
	mu.RLock()
	pool, ok := dbSpecificPools[key]
	mu.RUnlock()
	if ok {
		return pool, nil
	}

	srv, err := s.DB.GetServerByID(ctx, sqlite.GetServerByIDParams{ID: id, UserID: db.DefaultUserID})
	if err != nil {
		return nil, fmt.Errorf("server %d not found", id)
	}

	pool, err = dialServer(ctx, srv, database)
	if err != nil {
		return nil, err
	}

	mu.Lock()
	if existing, ok := dbSpecificPools[key]; ok {
		mu.Unlock()
		pool.Close()
		return existing, nil
	}
	dbSpecificPools[key] = pool
	mu.Unlock()
	log.Printf("Connected to server %d database %q", id, database)

	return pool, nil
}

// isDisconnected reports whether the user explicitly disconnected the server.
func (s *Server) isDisconnected(id int64) bool {
	mu.RLock()
	defer mu.RUnlock()
	return disconnectedServers[id]
}

// setDisconnected marks a server as explicitly disconnected (true) or
// reconnected (false). The flag is persisted in the server table so it
// survives across application restarts.
func (s *Server) setDisconnected(id int64, disconnected bool) {
	v := 0
	if disconnected {
		v = 1
	}
	if _, err := s.sqliteDB.ExecContext(context.Background(),
		`UPDATE server SET disconnected = ? WHERE id = ? AND user_id = ?`,
		v, id, db.DefaultUserID); err != nil {
		log.Printf("Failed to persist disconnected state for server %d: %v", id, err)
	}
	mu.Lock()
	if disconnected {
		disconnectedServers[id] = true
	} else {
		delete(disconnectedServers, id)
	}
	mu.Unlock()
}

// loadDisconnectedServers reads the persistently stored disconnected flags
// into the in-memory map so a server stays gray across restarts.
func (s *Server) loadDisconnectedServers() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := s.sqliteDB.QueryContext(ctx,
		`SELECT id FROM server WHERE user_id = ? AND disconnected = 1`, db.DefaultUserID)
	if err != nil {
		log.Printf("Failed to load disconnected servers: %v", err)
		return
	}
	defer rows.Close()
	mu.Lock()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			disconnectedServers[id] = true
		}
	}
	mu.Unlock()
	if err := rows.Err(); err != nil {
		log.Printf("Failed to load disconnected servers: %v", err)
	}
}

// dropServerPools closes and removes the cached pools of a server.
func (s *Server) dropServerPools(id int64) {
	mu.Lock()
	if p := dbPools[id]; p != nil {
		delete(dbPools, id)
		p.Close()
	}
	for key := range dbSpecificPools {
		if key.ServerID == id {
			p := dbSpecificPools[key]
			delete(dbSpecificPools, key)
			p.Close()
		}
	}
	mu.Unlock()
}

// ensureServerConnection returns a healthy pool for the server, dropping and
// re-dialing a stale cached pool if needed. Returns nil on failure.
func (s *Server) ensureServerConnection(ctx context.Context, id int64) *pgxpool.Pool {
	if pool := s.peekCachedPool(id); pool != nil {
		if pool.Ping(ctx) == nil {
			return pool
		}
		s.dropServerPools(id)
	}
	pool, err := s.getOrCreatePool(ctx, id)
	if err != nil {
		return nil
	}
	return pool
}

// dialServer opens and pings a new pgx pool to dbname on the given registered
// server, reusing the stored credentials (or the in-memory fallback for rows
// created before passwords were persisted).
func dialServer(ctx context.Context, srv sqlite.Server, dbname string) (*pgxpool.Pool, error) {
	password := ""
	if srv.Password.Valid {
		password = srv.Password.String
	} else {
		password = cachedPassword(srv.Name, srv.Host, srv.Port)
	}

	sslMode := srv.SslMode
	if sslMode == "" {
		sslMode = "prefer"
	}

	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(srv.Username, password),
		Host:   net.JoinHostPort(srv.Host, strconv.FormatInt(srv.Port, 10)),
		Path:   "/" + dbname,
	}
	q := u.Query()
	q.Set("sslmode", sslMode)
	q.Set("connect_timeout", "5")
	u.RawQuery = q.Encode()

	connectCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	pool, err := pgxpool.New(connectCtx, u.String())
	if err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		if password == "" {
			return nil, fmt.Errorf(
				"connection failed and no password is stored for this server "+
					"(remove it and register again to save credentials): %w", err)
		}
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	return pool, nil
}

// cachedPassword looks up a password from the in-memory configs registered
// during this session, matching on name/host/port.
func cachedPassword(name, host string, port int64) string {
	mu.RLock()
	defer mu.RUnlock()
	for _, cfg := range serverConfigs {
		if cfg.Name == name && cfg.Host == host && int64(cfg.Port) == port {
			return cfg.Password
		}
	}
	return ""
}

func (s *Server) handleAddServer(w http.ResponseWriter, r *http.Request) {
	// Chi handlers use the standard http.HandlerFunc signature; the chi route
	// context lives inside r.Context(). URL params are read via
	// chi.URLParam(r, "name") once a route declares them, e.g. /api/servers/{serverID}.

	// 1. Parse Form
	port, _ := strconv.Atoi(r.FormValue("port"))
	if port == 0 {
		port = 5432
	}

	sslMode := r.FormValue("sslmode")
	if !validSslModes[sslMode] {
		sslMode = "disable"
	}

	cfg := ServerConfig{
		ID:       fmt.Sprintf("server-%d", time.Now().UnixNano()),
		Name:     r.FormValue("name"),
		Host:     r.FormValue("host"),
		Port:     port,
		DBName:   r.FormValue("dbname"),
		Username: r.FormValue("username"),
		Password: r.FormValue("password"),
		SslMode:  sslMode,
	}

	// 2. Test PostgreSQL Connection with pgxpool
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DSN())
	if err != nil {
		log.Printf("[servers] Configuration error registering %q (%s:%d): %v", cfg.Name, cfg.Host, cfg.Port, err)
		s.renderModalError(w, r, fmt.Sprintf("Configuration error: %v", err))
		return
	}

	// Ping database to verify credentials & connectivity
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		log.Printf("[servers] Connection failed registering %q (%s:%d): %v", cfg.Name, cfg.Host, cfg.Port, err)
		s.renderModalError(w, r, fmt.Sprintf("Connection failed: %v", err))
		return
	}

	// 3. Save Connection & Pool
	mu.Lock()
	serverConfigs[cfg.ID] = cfg
	serverPools[cfg.ID] = pool
	mu.Unlock()

	// 4. Persist verified server to SQLite
	created, err := s.DB.CreateServer(ctx, sqlite.CreateServerParams{
		UserID:        db.DefaultUserID,
		ServergroupID: db.DefaultServerGroupID,
		Name:          cfg.Name,
		Host:          cfg.Host,
		Port:          int64(cfg.Port),
		MaintenanceDb: cfg.DBName,
		Username:      cfg.Username,
		SslMode:       cfg.SslMode,
	})
	if err != nil {
		mu.Lock()
		delete(serverConfigs, cfg.ID)
		delete(serverPools, cfg.ID)
		mu.Unlock()
		pool.Close()
		log.Printf("[servers] Failed to store server %q (%s:%d): %v", cfg.Name, cfg.Host, cfg.Port, err)
		s.renderModalError(w, r, fmt.Sprintf("Failed to store server: %v", err))
		return
	}

	// 4b. Persist the password too (the sqlc-generated CreateServer does not
	//     cover it). Without it the tree browser cannot reconnect after a
	//     restart and fails SASL auth with an empty password.
	if _, err := s.sqliteDB.ExecContext(ctx,
		`UPDATE server SET password = ? WHERE id = ?`, cfg.Password, created.ID); err != nil {
		log.Printf("Failed to persist password for server %d: %v", created.ID, err)
	}

	// 5. Success: the empty 204 body swaps into #modal-container, which closes
	//    the modal. The HX-Trigger event makes #tree-root re-fetch /api/tree,
	//    re-rendering the sidebar with the newly added server.
	w.Header().Set("HX-Trigger", "server-added")
	w.WriteHeader(http.StatusNoContent)
}

// renderModalError re-renders the Register Server modal with the submitted
// values preserved and an inline error banner. It responds with 200 so htmx
// (whose default response handling drops non-2xx bodies) swaps it into
// #modal-container and the user can fix the form.
func (s *Server) renderModalError(w http.ResponseWriter, r *http.Request, errorMsg string) {
	port := r.FormValue("port")
	if port == "" {
		port = "5432"
	}
	sslMode := r.FormValue("sslmode")
	if !validSslModes[sslMode] {
		sslMode = "disable"
	}
	RenderPartial(w, "add_server_modal.html", map[string]any{
		"Error": errorMsg,
		"Values": map[string]any{
			"name":     r.FormValue("name"),
			"host":     r.FormValue("host"),
			"port":     port,
			"dbname":   r.FormValue("dbname"),
			"username": r.FormValue("username"),
			"password": r.FormValue("password"),
			"sslmode":  sslMode,
		},
	})
}
