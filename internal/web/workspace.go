package web

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"htmx-golang-excercise/internal/db"
	sqlite "htmx-golang-excercise/internal/sqlc/sqlite/db"
)

// workspaceTabJSON mirrors one open script tab of a user. Path is where the
// script was last saved ("" for never-saved tabs); PathExists tells the
// frontend whether that file still exists so a deleted script can be
// re-flagged as dirty after a refresh.
type workspaceTabJSON struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	DBName     string `json:"db_name"`
	Query      string `json:"query"`
	Path       string `json:"path"`
	PathExists bool   `json:"path_exists"`
	Scratch    string `json:"scratch"`
	TabOrder   int    `json:"tab_order"`
}

// workspaceLayoutJSON holds the layout bits that survive a refresh.
type workspaceLayoutJSON struct {
	SidebarWidth string   `json:"sidebar_width"`
	SelectedTree string   `json:"selected_tree"`
	ExpandedTree []string `json:"expanded_tree"`
}

// workspaceStateJSON is the full persisted UI state of a user.
type workspaceStateJSON struct {
	ActiveTabID string              `json:"active_tab_id"`
	Layout      workspaceLayoutJSON `json:"layout"`
	Tabs        []workspaceTabJSON  `json:"tabs"`
}

func workspaceUserID() sql.NullInt64 {
	return sql.NullInt64{Int64: db.DefaultUserID, Valid: true}
}

// encodeConnection packs server id and database name into the single
// connection_id column of workspace_tabs.
func encodeConnection(serverID int64, dbName string) sql.NullString {
	if serverID <= 0 || dbName == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: strconv.FormatInt(serverID, 10) + ":" + dbName, Valid: true}
}

func decodeConnection(c sql.NullString) (int64, string) {
	if !c.Valid || c.String == "" {
		return 0, ""
	}
	parts := strings.SplitN(c.String, ":", 2)
	if len(parts) != 2 {
		return 0, c.String
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, parts[1]
	}
	return id, parts[1]
}

// handleWorkspaceGet returns the persisted UI state of the current user so the
// frontend can rebuild tabs, the active tab and the layout after a refresh.
func (s *Server) handleWorkspaceGet(w http.ResponseWriter, r *http.Request) {
	state := workspaceStateJSON{
		ActiveTabID: "dashboard",
		Layout:      workspaceLayoutJSON{},
		Tabs:        []workspaceTabJSON{},
	}

	ws, err := s.DB.GetUserWorkspace(r.Context(), db.DefaultUserID)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("Failed to load workspace: %v", err)
		http.Error(w, "Failed to load workspace", http.StatusInternalServerError)
		return
	}
	if err == nil {
		state.ActiveTabID = ws.ActiveTabID
		if ws.LayoutMetadata.Valid {
			_ = json.Unmarshal([]byte(ws.LayoutMetadata.String), &state.Layout)
		}
	}

	tabs, err := s.DB.ListWorkspaceTabs(r.Context(), workspaceUserID())
	if err != nil {
		log.Printf("Failed to load workspace tabs: %v", err)
		http.Error(w, "Failed to load workspace tabs", http.StatusInternalServerError)
		return
	}
	scratch := s.loadScratchTexts(r)
	for _, t := range tabs {
		serverID, dbName := decodeConnection(t.ConnectionID)

		path := t.FilePath.String
		pathExists := false
		if path != "" {
			if _, err := os.Stat(path); err == nil {
				pathExists = true
			}
		}

		// The name travels together with the id so the frontend can label
		// the tab's connection without a follow-up lookup.
		serverName := ""
		if serverID > 0 {
			srv, err := s.DB.GetServerByID(r.Context(), sqlite.GetServerByIDParams{
				ID:     serverID,
				UserID: db.DefaultUserID,
			})
			if err == nil {
				serverName = srv.Name
			}
		}

		state.Tabs = append(state.Tabs, workspaceTabJSON{
			ID:         t.ID,
			Title:      t.Title,
			ServerID:   serverID,
			ServerName: serverName,
			DBName:     dbName,
			Query:      t.QueryText.String,
			Path:       path,
			PathExists: pathExists,
			Scratch:    scratch[t.ID],
			TabOrder:   int(t.TabOrder),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(state); err != nil {
		log.Printf("Failed to encode workspace: %v", err)
	}
}

// handleWorkspaceSave replaces the persisted UI state of the current user.
func (s *Server) handleWorkspaceSave(w http.ResponseWriter, r *http.Request) {
	var state workspaceStateJSON
	if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
		http.Error(w, "Invalid workspace state", http.StatusBadRequest)
		return
	}

	layoutJSON, err := json.Marshal(state.Layout)
	if err != nil {
		http.Error(w, "Invalid layout state", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	uid := workspaceUserID()

	if err := s.DB.SaveUserWorkspace(ctx, sqlite.SaveUserWorkspaceParams{
		UserID:         db.DefaultUserID,
		ActiveTabID:    state.ActiveTabID,
		LayoutMetadata: sql.NullString{String: string(layoutJSON), Valid: true},
	}); err != nil {
		log.Printf("Failed to save workspace: %v", err)
		http.Error(w, "Failed to save workspace", http.StatusInternalServerError)
		return
	}

	if err := s.DB.ReplaceWorkspaceTabs(ctx, uid); err != nil {
		log.Printf("Failed to replace workspace tabs: %v", err)
		http.Error(w, "Failed to save workspace", http.StatusInternalServerError)
		return
	}

	for _, t := range state.Tabs {
		if err := s.DB.InsertWorkspaceTab(ctx, sqlite.InsertWorkspaceTabParams{
			ID:           t.ID,
			UserID:       uid,
			Title:        t.Title,
			ConnectionID: encodeConnection(t.ServerID, t.DBName),
			QueryText:    sql.NullString{String: t.Query, Valid: t.Query != ""},
			FilePath:     sql.NullString{String: t.Path, Valid: t.Path != ""},
			TabOrder:     int64(t.TabOrder),
		}); err != nil {
			log.Printf("Failed to save workspace tab %s: %v", t.ID, err)
			http.Error(w, "Failed to save workspace", http.StatusInternalServerError)
			return
		}
		if t.Scratch != "" {
			if _, err := s.sqliteDB.ExecContext(ctx,
				`UPDATE workspace_tabs SET scratch_text = ? WHERE id = ? AND user_id = ?`,
				t.Scratch, t.ID, db.DefaultUserID); err != nil {
				log.Printf("Failed to save scratch pad of tab %s: %v", t.ID, err)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// loadScratchTexts returns each saved tab's Scratch Pad text keyed by tab id.
// The column was added after the sqlc queries were generated, so it is read
// with a plain query; a failure only loses the scratch text, never the tabs.
func (s *Server) loadScratchTexts(r *http.Request) map[string]string {
	texts := map[string]string{}
	rows, err := s.sqliteDB.QueryContext(r.Context(),
		`SELECT id, COALESCE(scratch_text, '') FROM workspace_tabs WHERE user_id = ?`, db.DefaultUserID)
	if err != nil {
		log.Printf("Failed to load scratch pads: %v", err)
		return texts
	}
	defer rows.Close()
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err == nil {
			texts[id] = text
		}
	}
	return texts
}
