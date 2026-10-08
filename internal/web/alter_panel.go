package web

import (
	"context"
	"maps"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// The Alter Table side panel (static/js/alter-table.js) sits inside a script
// tab. It shows the same form components as the legacy Alter Table modal
// (handleDDLModal / ddl_alter_modal.html, kept as is), but the SQL comes from
// the shared preview endpoint on every change and lands in the tab's editor.

var panelIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// handleAlterTablePanel renders the panel for one table, pre-filled with its
// current owner and columns.
func (s *Server) handleAlterTablePanel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	panelID := q.Get("panel_id")
	if !panelIDPattern.MatchString(panelID) {
		http.Error(w, "Invalid panel id", http.StatusBadRequest)
		return
	}
	sid, _ := strconv.ParseInt(q.Get("server_id"), 10, 64)
	db, schema, name := q.Get("db"), q.Get("schema"), q.Get("name")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	data := ddlModalData{
		Partial:  "alter_table_panel.html",
		Kind:     "table",
		ServerID: sid,
		FolderID: q.Get("folder_id"),
		DB:       db,
		Schema:   schema,
		Table:    q.Get("table"),
		Name:     name,
		Values:   map[string]string{"name": name},
		PanelID:  panelID,
	}
	pool, err := s.ddlTargetPool(ctx, "table", sid, db)
	if err != nil {
		data.Error = "Cannot reach the target database: " + err.Error()
		s.renderDDLModal(w, data)
		return
	}
	data.Dropdowns, _ = s.ddlDropdowns(ctx, "table", pool, schema, data.Table)
	maps.Copy(data.Values, s.alterPrefill(ctx, pool, "table", name, schema, data.Table))
	data.ExistingColumns = s.tableExistingColumns(ctx, pool, schema, name)
	s.renderDDLModal(w, data)
}
