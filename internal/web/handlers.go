package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"htmx-golang-excercise/internal/db"
	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
	sqlite "htmx-golang-excercise/internal/sqlc/sqlite/db"
)

type Server struct {
	DB             *sqlite.Queries
	sqliteDB       *sql.DB
	historyHub     *historyHub
	queryMu        sync.Mutex
	runningQueries map[string]*runningQuery
}

func NewServer() (*Server, error) {
	database, queries, err := db.Open("pgadmin4.db")
	if err != nil {
		return nil, err
	}

	s := &Server{
		DB:             queries,
		sqliteDB:       database,
		historyHub:     newHistoryHub(),
		runningQueries: make(map[string]*runningQuery),
	}
	s.loadDisconnectedServers()
	return s, nil
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()

	r.Get("/login", s.handleLoginGet)
	r.Post("/login", s.handleLoginPost)
	r.Post("/logout", s.handleLogout)

	r.Handle("/static/*", StaticHandler)

	r.Group(func(r chi.Router) {
		r.Use(s.RequireAuth)

		r.Get("/", s.handleIndex)
		r.Get("/api/tree", s.handleTree)
		r.Get("/api/tabs/script-panel", s.handleScriptTabPanel)
		r.Get("/api/save-default-path", s.handleSaveDefaultPath)
		r.Post("/api/save-script", s.handleSaveScript)
		r.Get("/api/save-script/modal", s.handleSaveScriptModal)
		r.Get("/api/query-history", s.handleQueryHistory)
		r.Get("/api/query-history/stream", s.handleHistoryStream)
		r.Get("/api/query-history/{id}", s.handleQueryHistoryDetail)
		r.Post("/api/execute-query", s.handleExecuteQuery)
		r.Post("/api/cancel-query", s.handleCancelQuery)

		r.Get("/api/ddl/{kind}/modal", s.handleDDLModal)
		r.Post("/api/ddl/{kind}/preview", s.handleDDLPreview)
		r.Post("/api/ddl/{kind}/create", s.handleDDLCreate)
		r.Post("/api/ddl/{kind}/drop", s.handleDDLDrop)

		r.Get("/api/sessions", s.handleSessions)
		r.Post("/api/sessions/{pid}/cancel", s.handleSessionCancel)
		r.Delete("/api/sessions/{pid}", s.handleSessionTerminate)
		r.Get("/api/locks", s.handleLocks)
		r.Get("/api/prepared-transactions", s.handlePreparedTransactions)

		r.Route("/api/servers", func(r chi.Router) {
			r.Get("/", s.handleServerList)
			r.Get("/new", s.handleNewServerModal)
			r.Post("/", s.handleAddServer)

			r.Route("/{serverID}", func(r chi.Router) {
				r.Post("/disconnect", s.handleServerDisconnect)
				r.Get("/reconnect", s.handleServerReconnect)
				r.Get("/children", s.handleServerChildren)
				r.Get("/databases", s.handleServerDatabases)
				r.Get("/roles", s.handleServerRoles)
				r.Get("/tablespaces", s.handleServerTablespaces)

				r.Route("/databases/{dbName}", func(r chi.Router) {
					r.Get("/children", s.handleDatabaseChildren)
					r.Get("/{category}", s.handleDatabaseCategory)
					r.Get("/monitoring", s.handleMonitoring)
					r.Get("/monitoring/stream", s.handleMonitoringStream)
					r.Get("/autocomplete-schema", s.handleAutocompleteSchema)

					r.Route("/schemas/{schemaName}", func(r chi.Router) {
						r.Get("/children", s.handleSchemaChildren)
						r.Get("/{category}", s.handleSchemaCategory)

						r.Route("/tables/{tableName}", func(r chi.Router) {
							r.Get("/children", s.handleTableChildren)
							r.Get("/properties", s.handleTableProperties)
							r.Get("/{category}", s.handleTableCategory)
							r.Get("/columns-script", s.handleTableColumns)
							r.Get("/create-script", s.handleCreateScript)
							r.Get("/insert-script", s.handleInsertScript)
							r.Get("/delete-script", s.handleDeleteScript)
						})

						r.Route("/views/{viewName}", func(r chi.Router) {
							r.Get("/children", s.handleViewChildren)
							r.Get("/properties", s.handleViewProperties)
							r.Get("/{category}", s.handleViewCategory)
							r.Get("/columns-script", s.handleSelectViewScript)
							r.Get("/create-script", s.handleCreateViewScript)
							r.Get("/insert-script", s.handleInsertViewScript)
						})
					})
				})
			})
		})

		r.Route("/api/workspace", func(r chi.Router) {
			r.Get("/", s.handleWorkspaceGet)
			r.Post("/", s.handleWorkspaceSave)
		})
	})

	return r
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	Render(w, "index.html", map[string]any{
		"Title":         "The Main Dashboard",
		"Authenticated": true,
		"Username":      userFromContext(r),
	})
}

// handleTree renders the root of the object explorer: the server group node,
// labeled with the number of registered servers. Its children are
// lazy-loaded on click via GET /api/servers.
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	servers, ok := s.listServers(w, r)
	if !ok {
		return
	}

	badge := ""
	if len(servers) > 0 {
		badge = strconv.Itoa(len(servers))
	}

	renderTree(w, []treeNode{{
		ID:    "servers-group",
		Icon:  "🐘",
		Label: "Servers",
		Badge: badge,
		URL:   "/api/servers",
	}}, "")
}

func (s *Server) handleServerList(w http.ResponseWriter, r *http.Request) {
	servers, ok := s.listServers(w, r)
	if !ok {
		return
	}

	nodes := make([]treeNode, 0, len(servers))
	for _, srv := range servers {
		// Gray = deliberately disconnected by the user. Red = not available
		// (probe failed), whether it was connected earlier or never reached;
		// green = connected and pinging.
		state := "off"
		switch {
		case s.isDisconnected(srv.ID):
			state = "gray"
		case s.probeServer(r.Context(), srv.ID):
			state = "on"
		}
		nodes = append(nodes, treeNode{
			ID:    fmt.Sprintf("server-%d", srv.ID),
			Icon:  "🖥️",
			Label: srv.Name,
			Sub:   fmt.Sprintf("%s:%d / %s", srv.Host, srv.Port, srv.MaintenanceDb),
			URL:   fmt.Sprintf("/api/servers/%d/children", srv.ID),
			State: state,
			Menu:  "server",
		})
	}

	renderTree(w, nodes, "No servers registered yet.")
}

// handleServerChildren renders the category folders shown when a server node
// is expanded in the tree. A deliberately disconnected server cannot be
// expanded and renders nothing. A server that is not available (whether it was
// connected earlier or never reached) renders the reconnect hint instead.
func (s *Server) handleServerChildren(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireServer(w, r)
	if !ok {
		return
	}

	if s.isDisconnected(id) {
		renderTree(w, nil, "")
		return
	}

	// No cached pool means the server is unreachable; a cached pool that no
	// longer answers means it was connected earlier but is down now. Both are
	// "not available" and show the reconnect hint instead of stale folders.
	pool := s.peekCachedPool(id)
	if pool == nil {
		s.renderServerUnavailable(w)
		return
	}
	pingCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	reachable := pool.Ping(pingCtx) == nil
	cancel()
	if !reachable {
		s.renderServerUnavailable(w)
		return
	}

	s.renderServerFolders(w, r, id)
}

// renderServerFolders renders the category folders shown when a server node is
// expanded. The caller has already validated the server (see requireServer).
// pool-less servers render without live counts (never dialing, so unreachable
// servers stay fast); any cached pool is only peeked at.
func (s *Server) renderServerFolders(w http.ResponseWriter, r *http.Request, id int64) {
	base := fmt.Sprintf("/api/servers/%d", id)

	// Folder counts (databases / roles / tablespaces) come from the cached
	// maintenance pool only — never dial here — so rendering the folders is
	// never delayed by an unreachable server. Without a live pool the folders
	// render with no badges, exactly as before.
	counts := make(map[string]int64)
	if pool := s.peekCachedPool(id); pool != nil {
		rows, cerr := pgdb.New(pool).CountServerObjects(r.Context())
		if cerr != nil {
			log.Printf("Failed to count server objects: %v", cerr)
		} else {
			for _, row := range rows {
				counts[row.Category] = row.N
			}
		}
	}

	folder := func(cid, slug, icon, label, url string) treeNode {
		if cnt, ok := counts[slug]; ok && cnt == 0 {
			return treeNode{Icon: icon, Label: label, Disabled: true}
		}
		n := expander(cid, icon, label, url)
		if cnt, ok := counts[slug]; ok && cnt > 0 {
			n.Badge = strconv.FormatInt(cnt, 10)
		}
		return n
	}

	renderTree(w, []treeNode{
		folder(fmt.Sprintf("server-%d-databases", id), "databases", "🗃️", "Databases", base+"/databases"),
		folder(fmt.Sprintf("server-%d-roles", id), "roles", "👥", "Login/Group Roles", base+"/roles"),
		folder(fmt.Sprintf("server-%d-tablespaces", id), "tablespaces", "💽", "Tablespaces", base+"/tablespaces"),
	}, "")
}

// handleServerDisconnect marks the server as intentionally disconnected: it
// shows a gray dot, cannot be expanded, and is not re-connected on page
// refresh. The user reconnects via the node's Refresh action.
func (s *Server) handleServerDisconnect(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireServer(w, r)
	if !ok {
		return
	}

	s.setDisconnected(id, true)
	s.dropServerPools(id)
	w.WriteHeader(http.StatusNoContent)
}

// handleServerReconnect restores the connection of a (possibly disconnected)
// server. On success it un-marks the server and renders its children; on
// failure it renders the "not available" hint so the user can retry.
func (s *Server) handleServerReconnect(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireServer(w, r)
	if !ok {
		return
	}

	connectCtx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if s.ensureServerConnection(connectCtx, id) == nil {
		// The reconnection attempt failed: the server is no longer
		// "disconnected by the user" but simply unavailable, so it is probed
		// again (including on the next application start) instead of staying
		// gray until somebody disconnects it again.
		s.setDisconnected(id, false)
		s.renderServerUnavailable(w)
		return
	}

	s.setDisconnected(id, false)
	s.renderServerFolders(w, r, id)
}

// renderServerUnavailable renders the hint shown inside a server node when it
// is disconnected or its connection is unavailable: the server cannot be
// expanded and must be reconnected via the node's Refresh action.
func (s *Server) renderServerUnavailable(w http.ResponseWriter) {
	renderTree(w, nil, "Server is not available. Right-click the server and choose \"Try to reconnect\".")
}

// parseServerID validates the {serverID} route param. On failure it writes the
// error response itself and returns ok=false.
func parseServerID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "serverID"), 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "Invalid server id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// requireServer validates the {serverID} route param and that it belongs to
// the default user. On failure it writes the error response itself.
func (s *Server) requireServer(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := parseServerID(w, r)
	if !ok {
		return 0, false
	}
	if _, err := s.DB.GetServerByID(r.Context(), sqlite.GetServerByIDParams{
		ID:     id,
		UserID: db.DefaultUserID,
	}); err != nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return 0, false
	}
	return id, true
}

// loadServerPool validates the {serverID} route param and returns a live
// pgx pool for that registered server. On failure it writes the error
// response itself and returns ok=false.
func (s *Server) loadServerPool(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, int64, bool) {
	id, ok := parseServerID(w, r)
	if !ok {
		return nil, 0, false
	}

	pool, err := s.getOrCreatePool(r.Context(), id)
	if err != nil {
		log.Printf("Failed to connect to server %d: %v", id, err)
		http.Error(w, "Cannot connect to server: "+err.Error(), http.StatusBadGateway)
		return nil, 0, false
	}

	return pool, id, true
}

// handleServerDatabases lists the databases of a registered server queried
// live from its maintenance database.
func (s *Server) handleServerDatabases(w http.ResponseWriter, r *http.Request) {
	pool, id, ok := s.loadServerPool(w, r)
	if !ok {
		return
	}

	names, err := pgdb.New(pool).ListDatabases(r.Context())
	if err != nil {
		log.Printf("Failed to list databases: %v", err)
		http.Error(w, "Failed to load databases", http.StatusInternalServerError)
		return
	}

	nodes := make([]treeNode, 0, len(names))
	for _, name := range names {
		nodes = append(nodes, treeNode{
			ID:       fmt.Sprintf("database-%d-%s", id, name),
			Icon:     "🗄️",
			Label:    name,
			URL:      fmt.Sprintf("/api/servers/%d/databases/%s/children", id, name),
			Menu:     "database",
			DataName: name,
		})
	}

	renderTree(w, nodes, "No databases found.")
}

// handleDatabaseChildren renders the category folders shown when a database
// node is expanded in the tree.
func (s *Server) handleDatabaseChildren(w http.ResponseWriter, r *http.Request) {
	pool, id, dbName, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}

	// Live counts tag each category folder with a badge. Best-effort: on
	// error the folders still render, just without badges.
	counts, err := pgdb.New(pool).CountDatabaseObjects(r.Context())
	if err != nil {
		log.Printf("Failed to count database objects: %v", err)
		renderTree(w, categoryFolders(
			fmt.Sprintf("database-%d-%s", id, dbName),
			fmt.Sprintf("/api/servers/%d/databases/%s", id, dbName),
			dbCategories,
		), "")
		return
	}

	bySlug := make(map[string]int64, len(counts))
	for _, c := range counts {
		bySlug[c.Category] = c.N
	}

	renderTree(w, categoryFoldersWithCounts(
		fmt.Sprintf("database-%d-%s", id, dbName),
		fmt.Sprintf("/api/servers/%d/databases/%s", id, dbName),
		dbCategories,
		bySlug,
	), "")
}

// handleDatabaseCategory lists the contents of one database category folder
// (casts, extensions, schemas, ...) queried live from that database.
func (s *Server) handleDatabaseCategory(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "category")
	cat := findCategory(dbCategories, slug)
	if cat == nil {
		http.Error(w, "Unknown database item category", http.StatusNotFound)
		return
	}

	pool, id, dbName, ok := s.loadDatabasePool(w, r)
	if !ok {
		return
	}

	names, err := cat.ListNames(r.Context(), pool)
	if err != nil {
		log.Printf("Failed to load %s: %v", cat.Label, err)
		http.Error(w, "Failed to load "+cat.Label, http.StatusInternalServerError)
		return
	}

	// Schemas are not leaves: each one is an expandable node whose children
	// are the schema's own object folders (tables, views, ...).
	if slug == "schemas" {
		nodes := make([]treeNode, 0, len(names))
		for _, name := range names {
			nodes = append(nodes, treeNode{
				ID:    fmt.Sprintf("schema-%d-%s-%s", id, dbName, name),
				Icon:  "🗂️",
				Label: name,
				URL:   fmt.Sprintf("/api/servers/%d/databases/%s/schemas/%s/children", id, dbName, name),
				Menu:  "schema",
			})
		}
		renderTree(w, nodes, cat.Empty)
		return
	}

	renderTree(w, leaves(cat.Icon, names), cat.Empty)
}

// loadDatabasePool validates the {serverID}/{dbName} route params and returns
// a live pgx pool connected to that specific database. On failure it writes
// the error response itself and returns ok=false.
func (s *Server) loadDatabasePool(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, int64, string, bool) {
	id, ok := parseServerID(w, r)
	if !ok {
		return nil, 0, "", false
	}

	dbName := chi.URLParam(r, "dbName")
	if dbName == "" {
		http.Error(w, "Invalid database name", http.StatusBadRequest)
		return nil, 0, "", false
	}

	pool, err := s.getOrCreateDbPool(r.Context(), id, dbName)
	if err != nil {
		log.Printf("Failed to connect to server %d database %q: %v", id, dbName, err)
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return nil, 0, "", false
	}

	return pool, id, dbName, true
}

// handleSchemaChildren renders the object folders shown when a schema node is
// expanded in the tree.
func (s *Server) handleSchemaChildren(w http.ResponseWriter, r *http.Request) {
	pool, id, dbName, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}

	// Live item counts tag each folder with a badge. Best-effort: if the
	// count query fails the folders still render, just without badges.
	counts, err := pgdb.New(pool).CountSchemaObjects(r.Context(), schemaName)
	if err != nil {
		log.Printf("Failed to count schema objects: %v", err)
		renderTree(w, categoryFolders(
			fmt.Sprintf("schema-%d-%s-%s", id, dbName, schemaName),
			fmt.Sprintf("/api/servers/%d/databases/%s/schemas/%s", id, dbName, schemaName),
			schemaCategories,
		), "")
		return
	}

	bySlug := make(map[string]int64, len(counts))
	for _, c := range counts {
		bySlug[c.Category] = c.N
	}

	renderTree(w, categoryFoldersWithCounts(
		fmt.Sprintf("schema-%d-%s-%s", id, dbName, schemaName),
		fmt.Sprintf("/api/servers/%d/databases/%s/schemas/%s", id, dbName, schemaName),
		schemaCategories,
		bySlug,
	), "")
}

// handleSchemaCategory lists the contents of one folder inside a schema
// (tables, views, sequences, ...) queried live from that database.
func (s *Server) handleSchemaCategory(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "category")
	cat := findCategory(schemaCategories, slug)
	if cat == nil {
		http.Error(w, "Unknown schema item category", http.StatusNotFound)
		return
	}

	pool, id, dbName, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return
	}

	names, err := cat.ListNames(r.Context(), pool, schemaName)
	if err != nil {
		log.Printf("Failed to load %s: %v", cat.Label, err)
		http.Error(w, "Failed to load "+cat.Label, http.StatusInternalServerError)
		return
	}

	// Tables are not leaves either: each one expands into its own object
	// folders (columns, constraints, indexes, ...) and carries a right-click
	// context menu.
	if slug == "tables" {
		nodes := make([]treeNode, 0, len(names))
		for _, name := range names {
			tablePath := fmt.Sprintf("/api/servers/%d/databases/%s/schemas/%s/tables/%s", id, dbName, schemaName, name)
			nodes = append(nodes, treeNode{
				ID:    fmt.Sprintf("table-%d-%s-%s-%s", id, dbName, schemaName, name),
				Icon:  cat.Icon,
				Label: name,
				URL:   tablePath + "/children",
				Menu:  "table",
			})
		}
		renderTree(w, nodes, cat.Empty)
		return
	}

	// Views are not leaves either: each one expands into its own object
	// folders (columns, rules, triggers) and carries a right-click context
	// menu with the view scripts.
	if slug == "views" {
		nodes := make([]treeNode, 0, len(names))
		for _, name := range names {
			viewPath := fmt.Sprintf("/api/servers/%d/databases/%s/schemas/%s/views/%s", id, dbName, schemaName, name)
			nodes = append(nodes, treeNode{
				ID:    fmt.Sprintf("view-%d-%s-%s-%s", id, dbName, schemaName, name),
				Icon:  cat.Icon,
				Label: name,
				URL:   viewPath + "/children",
				Menu:  "view",
			})
		}
		renderTree(w, nodes, cat.Empty)
		return
	}

	renderTree(w, leaves(cat.Icon, names), cat.Empty)
}

// loadSchemaPool validates the {serverID}/{dbName}/{schemaName} route params
// and returns a live pgx pool connected to that database. On failure it writes
// the error response itself and returns ok=false.
func (s *Server) loadSchemaPool(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, int64, string, string, bool) {
	id, ok := parseServerID(w, r)
	if !ok {
		return nil, 0, "", "", false
	}

	dbName := chi.URLParam(r, "dbName")
	schemaName := chi.URLParam(r, "schemaName")
	if dbName == "" || schemaName == "" {
		http.Error(w, "Invalid database or schema name", http.StatusBadRequest)
		return nil, 0, "", "", false
	}

	pool, err := s.getOrCreateDbPool(r.Context(), id, dbName)
	if err != nil {
		log.Printf("Failed to connect to server %d database %q: %v", id, dbName, err)
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return nil, 0, "", "", false
	}

	return pool, id, dbName, schemaName, true
}

// loadTablePool validates the {serverID}/{dbName}/{schemaName}/{tableName}
// route params and returns a live pgx pool connected to that database. On
// failure it writes the error response itself and returns ok=false.
func (s *Server) loadTablePool(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, int64, string, string, string, bool) {
	pool, id, dbName, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return nil, 0, "", "", "", false
	}

	tableName := chi.URLParam(r, "tableName")
	if tableName == "" {
		http.Error(w, "Invalid table name", http.StatusBadRequest)
		return nil, 0, "", "", "", false
	}

	return pool, id, dbName, schemaName, tableName, true
}

// handleTableChildren renders the object folders shown when a table node is
// expanded in the tree.
func (s *Server) handleTableChildren(w http.ResponseWriter, r *http.Request) {
	// Connecting validates the server, database and schema before the folders
	// are rendered.
	pool, id, dbName, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}

	base := fmt.Sprintf("/api/servers/%d/databases/%s/schemas/%s/tables/%s", id, dbName, schemaName, tableName)
	nodeID := fmt.Sprintf("table-%d-%s-%s-%s", id, dbName, schemaName, tableName)

	// Live counts tag each category folder with a badge. Best-effort: on
	// error the folders still render, just without badges.
	counts, err := pgdb.New(pool).CountTableObjects(r.Context(), pgdb.CountTableObjectsParams{
		TableSchema: schemaName,
		TableName:   tableName,
	})
	if err != nil {
		log.Printf("Failed to count table objects: %v", err)
		renderTree(w, categoryFolders(nodeID, base, tableCategories), "")
		return
	}

	bySlug := make(map[string]int64, len(counts))
	for _, c := range counts {
		bySlug[c.Category] = c.N
	}

	renderTree(w, categoryFoldersWithCounts(nodeID, base, tableCategories, bySlug), "")
}

// handleTableCategory lists the contents of one folder inside a table
// (columns, constraints, indexes, ...) queried live from that database.
func (s *Server) handleTableCategory(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "category")
	cat := findCategory(tableCategories, slug)
	if cat == nil {
		http.Error(w, "Unknown table item category", http.StatusNotFound)
		return
	}

	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}

	names, err := cat.ListNames(r.Context(), pool, schemaName, tableName)
	if err != nil {
		log.Printf("Failed to load %s: %v", cat.Label, err)
		http.Error(w, "Failed to load "+cat.Label, http.StatusInternalServerError)
		return
	}

	renderTree(w, leaves(cat.Icon, names), cat.Empty)
}

// loadViewPool validates the {serverID}/{dbName}/{schemaName}/{viewName}
// route params and returns a live pgx pool connected to that database. On
// failure it writes the error response itself and returns ok=false.
func (s *Server) loadViewPool(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, int64, string, string, string, bool) {
	pool, id, dbName, schemaName, ok := s.loadSchemaPool(w, r)
	if !ok {
		return nil, 0, "", "", "", false
	}

	viewName := chi.URLParam(r, "viewName")
	if viewName == "" {
		http.Error(w, "Invalid view name", http.StatusBadRequest)
		return nil, 0, "", "", "", false
	}

	return pool, id, dbName, schemaName, viewName, true
}

// handleViewChildren renders the object folders shown when a view node is
// expanded in the tree.
func (s *Server) handleViewChildren(w http.ResponseWriter, r *http.Request) {
	// Connecting validates the server, database and schema before the folders
	// are rendered.
	_, id, dbName, schemaName, viewName, ok := s.loadViewPool(w, r)
	if !ok {
		return
	}

	renderTree(w, categoryFolders(
		fmt.Sprintf("view-%d-%s-%s-%s", id, dbName, schemaName, viewName),
		fmt.Sprintf("/api/servers/%d/databases/%s/schemas/%s/views/%s", id, dbName, schemaName, viewName),
		viewCategories,
	), "")
}

// handleViewCategory lists the contents of one folder inside a view
// (columns, rules, triggers) queried live from that database.
func (s *Server) handleViewCategory(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "category")
	cat := findCategory(viewCategories, slug)
	if cat == nil {
		http.Error(w, "Unknown view item category", http.StatusNotFound)
		return
	}

	pool, _, _, schemaName, viewName, ok := s.loadViewPool(w, r)
	if !ok {
		return
	}

	names, err := cat.ListNames(r.Context(), pool, schemaName, viewName)
	if err != nil {
		log.Printf("Failed to load %s: %v", cat.Label, err)
		http.Error(w, "Failed to load "+cat.Label, http.StatusInternalServerError)
		return
	}

	renderTree(w, leaves(cat.Icon, names), cat.Empty)
}

func (s *Server) handleServerRoles(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := s.loadServerPool(w, r)
	if !ok {
		return
	}

	names, err := pgdb.New(pool).ListRoles(r.Context())
	if err != nil {
		log.Printf("Failed to list roles: %v", err)
		http.Error(w, "Failed to load roles", http.StatusInternalServerError)
		return
	}

	renderTree(w, menuLeaves("👤", names, "role"), "No roles found.")
}

func (s *Server) handleServerTablespaces(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := s.loadServerPool(w, r)
	if !ok {
		return
	}

	names, err := pgdb.New(pool).ListTablespaces(r.Context())
	if err != nil {
		log.Printf("Failed to list tablespaces: %v", err)
		http.Error(w, "Failed to load tablespaces", http.StatusInternalServerError)
		return
	}

	renderTree(w, menuLeaves("📀", names, "tablespace"), "No tablespaces found.")
}

func (s *Server) handleNewServerModal(w http.ResponseWriter, r *http.Request) {
	RenderPartial(w, "add_server_modal.html", nil)
}

func (s *Server) handleScriptTabPanel(w http.ResponseWriter, r *http.Request) {
	RenderPartial(w, "script_tab_panel.html", nil)
}

func (s *Server) handleExecuteQuery(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	query := r.FormValue("sql_query")
	serverIDStr := r.FormValue("server_id")
	dbName := r.FormValue("db_name")
	tabID := r.FormValue("tab_id")

	if query == "" || serverIDStr == "" || dbName == "" {
		http.Error(w, "Missing query, server_id, or db_name", http.StatusBadRequest)
		return
	}

	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil || serverID < 1 {
		http.Error(w, "Invalid server id", http.StatusBadRequest)
		return
	}

	page, _ := strconv.Atoi(r.FormValue("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.FormValue("limit"))
	if limit < 1 {
		limit = 1000
	}
	offset := (page - 1) * limit

	query = strings.TrimRight(query, " \t\n\r;")

	pool, err := s.getOrCreateDbPool(r.Context(), serverID, dbName)
	if err != nil {
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return
	}

	start := time.Now()

	// Hold a pool connection for the whole statement so the backend PID can be
	// registered and the running query cancelled via the Stop button.
	conn, err := pool.Acquire(r.Context())
	if err != nil {
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Release()

	s.registerQuery(tabID, conn.Conn().PgConn().PID(), pool)
	defer s.unregisterQuery(tabID)

	// EXPLAIN returns rows but cannot be used as a subquery, so it must be
	// executed directly rather than wrapped for count/pagination.
	if isExplain(query) {
		s.renderExplain(w, r, conn, query, serverID, dbName, tabID, start, limit)
		return
	}

	// Statements that return rows (SELECT and friends) can be wrapped for
	// count + pagination. Everything else (CREATE/DROP/ALTER/INSERT/UPDATE/
	// DELETE/...) is executed directly and returns a command tag instead.
	if !isRowReturning(query) {
		exec, err := conn.Exec(r.Context(), query)
		elapsed := time.Since(start).Seconds()

		message := "OK"
		var rowsAffected int64
		if err != nil {
			message = err.Error()
		} else {
			message = exec.String()
			rowsAffected = exec.RowsAffected()
		}

		s.recordQueryHistory(r.Context(), serverID, dbName, tabID, query, int64(elapsed*1000), rowsAffected)

		RenderPartial(w, "query_result.html", map[string]any{
			"Headers":    []string{},
			"Rows":       [][]string{},
			"Page":       1,
			"Total":      0,
			"TotalPages": 1,
			"Limit":      limit,
			"Offset":     0,
			"Elapsed":    elapsed,
			"Message":    message,
		})
		return
	}

	// Send the count query and the paginated fetch in a single batch so they
	// execute on one connection in one network round trip instead of two.
	batch := &pgx.Batch{}
	batch.Queue("SELECT COUNT(*) FROM (" + query + ") _cnt")
	batch.Queue("SELECT * FROM ("+query+") _q LIMIT $1 OFFSET $2", limit, offset)

	br := conn.SendBatch(r.Context(), batch)
	defer br.Close()

	// Count total rows
	var total int
	if err := br.QueryRow().Scan(&total); err != nil {
		http.Error(w, "Count query failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Fetch paginated rows
	rows, err := br.Query()
	if err != nil {
		http.Error(w, "Query failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	colCount := len(fieldDescriptions)

	var headers []string
	for _, fd := range fieldDescriptions {
		headers = append(headers, fd.Name)
	}

	var rowsData [][]string
	for rows.Next() {
		vals := make([]any, colCount)
		valPtrs := make([]any, colCount)
		for i := range vals {
			valPtrs[i] = &vals[i]
		}
		if err := rows.Scan(valPtrs...); err != nil {
			http.Error(w, "Row scan error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		row := make([]string, colCount)
		for i, v := range vals {
			if v == nil {
				row[i] = "NULL"
			} else {
				row[i] = fmt.Sprintf("%v", v)
			}
		}
		rowsData = append(rowsData, row)
	}

	elapsed := time.Since(start).Seconds()

	s.recordQueryHistory(r.Context(), serverID, dbName, tabID, query, int64(elapsed*1000), int64(total))

	totalPages := (total + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}

	RenderPartial(w, "query_result.html", map[string]any{
		"Headers":    headers,
		"Rows":       rowsData,
		"Page":       page,
		"Total":      total,
		"TotalPages": totalPages,
		"Limit":      limit,
		"Offset":     offset,
		"Elapsed":    elapsed,
		"Message":    "",
	})
}

// isRowReturning reports whether the trimmed query is one that returns a
// result set (and can therefore be wrapped for count/pagination). All other
// statements (DDL/DML) are executed directly by handleExecuteQuery.
// stripLeadingComments removes leading "--" comment lines from an upper-cased,
// trimmed SQL statement, returning "" when only comments remain.
func stripLeadingComments(query string) string {
	for strings.HasPrefix(query, "--") {
		idx := strings.Index(query, "\n")
		if idx < 0 {
			return ""
		}
		query = strings.TrimSpace(query[idx+1:])
	}
	return query
}

func isRowReturning(query string) bool {
	trimmed := stripLeadingComments(strings.TrimSpace(strings.ToUpper(query)))
	if trimmed == "" {
		return false
	}
	// Advance past an optional leading "WITH x AS (...) " CTE to the final
	// statement keyword.
	idx := strings.IndexAny(trimmed, " \t\n\r")
	if idx < 0 {
		idx = len(trimmed)
	}
	first := trimmed[:idx]
	switch first {
	case "SELECT", "VALUES", "TABLE", "SHOW", "EXPLAIN":
		return true
	}
	// A query that starts with WITH needs inspection: a trailing SELECT
	// returns rows, while a trailing INSERT/UPDATE/DELETE does not.
	if first == "WITH" {
		return strings.HasSuffix(trimmed, "SELECT") || strings.HasSuffix(trimmed, ")")
	}
	return false
}

// isExplain reports whether the trimmed query starts with EXPLAIN (or
// EXPLAIN ANALYZE). Such queries return rows but cannot be wrapped as a
// subquery, so they are executed directly.
func isExplain(query string) bool {
	trimmed := stripLeadingComments(strings.TrimSpace(strings.ToUpper(query)))
	return strings.HasPrefix(trimmed, "EXPLAIN")
}

// renderExplain executes an EXPLAIN/EXPLAIN ANALYZE statement directly and
// renders its plan rows into the explain output panel. conn is the already
// acquired (and registration-tracked) pool connection running the query.
func (s *Server) renderExplain(w http.ResponseWriter, r *http.Request, conn *pgxpool.Conn, query string, serverID int64, dbName, tabID string, start time.Time, limit int) {
	rows, err := conn.Query(r.Context(), query)
	elapsed := time.Since(start).Seconds()

	message := ""
	if err != nil {
		message = err.Error()
	}

	var headers []string
	var rowsData [][]string
	if err == nil {
		defer rows.Close()
		fieldDescriptions := rows.FieldDescriptions()
		for _, fd := range fieldDescriptions {
			headers = append(headers, fd.Name)
		}
		colCount := len(fieldDescriptions)
		rowCount := 0
		for rows.Next() && rowCount < limit {
			vals := make([]any, colCount)
			valPtrs := make([]any, colCount)
			for i := range vals {
				valPtrs[i] = &vals[i]
			}
			if err := rows.Scan(valPtrs...); err != nil {
				message = err.Error()
				break
			}
			row := make([]string, colCount)
			for i, v := range vals {
				if v == nil {
					row[i] = "NULL"
				} else {
					row[i] = fmt.Sprintf("%v", v)
				}
			}
			rowsData = append(rowsData, row)
			rowCount++
		}
		if err := rows.Err(); err != nil && message == "" {
			message = err.Error()
		}
	}

	s.recordQueryHistory(r.Context(), serverID, dbName, tabID, query, int64(elapsed*1000), int64(len(rowsData)))

	RenderPartial(w, "query_result.html", map[string]any{
		"Headers":    headers,
		"Rows":       rowsData,
		"Page":       1,
		"Total":      len(rowsData),
		"TotalPages": 1,
		"Limit":      limit,
		"Offset":     0,
		"Elapsed":    elapsed,
		"Message":    message,
	})
}

func (s *Server) handleTableColumns(w http.ResponseWriter, r *http.Request) {
	pool, _, _, schemaName, tableName, ok := s.loadTablePool(w, r)
	if !ok {
		return
	}

	items, err := pgdb.New(pool).GetTableColumns(r.Context(), pgdb.GetTableColumnsParams{
		TableSchema: schemaName,
		TableName:   tableName,
	})
	if err != nil {
		http.Error(w, "Failed to query columns", http.StatusInternalServerError)
		return
	}

	var cols []string
	for _, it := range items {
		cols = append(cols, getString(it))
	}

	query := selectColumns(cols) + "\nFROM " + tableName + ";"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"query": query})
}
