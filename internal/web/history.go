package web

import (
	"context"
	"database/sql"
	"encoding/base64"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	sqlite "htmx-golang-excercise/internal/sqlc/sqlite/db"
)

// historyItemView is the display-friendly form of a query_history row passed
// to the list template.
type historyItemView struct {
	ID           int64
	QueryB64     string
	QueryShort   string
	Executed     string
	DurationMs   sql.NullInt64
	Status       sql.NullString
	RowsAffected sql.NullInt64
	ServerID     int64
	DBName       string
}

// historyDetailView carries all fields for the detail view rendered into a
// new tab when a history item is clicked.
type historyDetailView struct {
	ID           int64
	QueryText    string
	ExecutedAt   string
	DurationMs   sql.NullInt64
	Status       sql.NullString
	RowsAffected sql.NullInt64
	ServerID     int64
	DBName       string
}

// handleQueryHistory renders the query history panel for a database
// connection. All queries run on this connection are shown, regardless of
// which script tab ran them.
func (s *Server) handleQueryHistory(w http.ResponseWriter, r *http.Request) {
	serverID, _ := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	dbName := r.FormValue("db_name")

	connString := ""
	if serverID > 0 && dbName != "" {
		connString = encodeConnection(serverID, dbName).String
	}

	items, err := s.DB.ListQueryHistory(r.Context(), sqlite.ListQueryHistoryParams{
		UserID:       workspaceUserID(),
		Column2:      connString,
		ConnectionID: sql.NullString{String: connString, Valid: connString != ""},
	})
	if err != nil {
		log.Printf("Failed to load query history: %v", err)
		http.Error(w, "Failed to load query history", http.StatusInternalServerError)
		return
	}

	views := make([]historyItemView, 0, len(items))
	for _, it := range items {
		trimmed := strings.TrimSpace(it.QueryText)
		display := trimmed
		if len(display) > 120 {
			display = display[:120] + "…"
		}
		views = append(views, historyItemView{
			ID:           it.ID,
			QueryB64:     base64.RawStdEncoding.EncodeToString([]byte(it.QueryText)),
			QueryShort:   display,
			Executed:     formatHistoryTime(it.ExecutedAt),
			DurationMs:   it.DurationMs,
			Status:       it.Status,
			RowsAffected: it.RowsAffected,
			ServerID:     serverID,
			DBName:       dbName,
		})
	}

	RenderPartial(w, "query_history.html", map[string]any{
		"Items": views,
	})
}

// handleQueryHistoryDetail renders the full detail view for a single query
// history entry, to be opened in a new tab.
func (s *Server) handleQueryHistoryDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		log.Printf("history detail: invalid id %q on %s", chi.URLParam(r, "id"), r.URL.Path)
		http.Error(w, "Invalid history id", http.StatusBadRequest)
		return
	}

	serverID, _ := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	dbName := r.FormValue("db_name")

	item, err := s.DB.GetQueryHistoryByID(r.Context(), sqlite.GetQueryHistoryByIDParams{
		ID:     id,
		UserID: workspaceUserID(),
	})
	if err != nil {
		log.Printf("history detail: entry %d not found: %v", id, err)
		http.Error(w, "History entry not found", http.StatusNotFound)
		return
	}

	RenderPartial(w, "query_history_detail.html", historyDetailView{
		ID:           item.ID,
		QueryText:    item.QueryText,
		ExecutedAt:   item.ExecutedAt.Time.Format("1/2/2006 3:04:05 PM"),
		DurationMs:   item.DurationMs,
		Status:       item.Status,
		RowsAffected: item.RowsAffected,
		ServerID:     serverID,
		DBName:       dbName,
	})
}

func formatHistoryTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("Jan 02 15:04:05")
}

// recordQueryHistory persists a completed query run to the SQLite
// query_history table so it shows up in the script window's history panel.
func (s *Server) recordQueryHistory(ctx context.Context, serverID int64, dbName, tabID, query string, durationMs int64, rowsAffected int64) {
	if serverID <= 0 || dbName == "" || tabID == "" || query == "" {
		return
	}

	conn := sql.NullString{String: "", Valid: false}
	if c := encodeConnection(serverID, dbName); c.Valid {
		conn = c
	}

	err := s.DB.RecordQueryHistory(ctx, sqlite.RecordQueryHistoryParams{
		UserID:       workspaceUserID(),
		TabID:        sql.NullString{String: tabID, Valid: true},
		ConnectionID: conn,
		QueryText:    query,
		DurationMs:   sql.NullInt64{Int64: durationMs, Valid: true},
		Status:       sql.NullString{String: "success", Valid: true},
		RowsAffected: sql.NullInt64{Int64: rowsAffected, Valid: true},
	})
	if err != nil {
		log.Printf("Failed to record query history: %v", err)
		return
	}

	// Notify open SSE streams for this connection so their history panels
	// repaint without polling.
	if s.historyHub != nil {
		s.historyHub.notify(conn.String)
	}
}
