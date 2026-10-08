package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"htmx-golang-excercise/internal/env"
)

type saveScriptRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// handleSaveScriptModal serves the Save Script dialog (a self-contained
// partial) that the client injects into #modal-container.
func (s *Server) handleSaveScriptModal(w http.ResponseWriter, r *http.Request) {
	RenderPartial(w, "save_script_modal.html", nil)
}

// scriptsDir returns the folder the Save As dialog starts in (SCRIPTS_DIR,
// default ./host_files/scripts), creating it on first use. Like BACKUP_DIR it lives in host_files/, the
// one folder shared with the host when the app runs in Docker.
func scriptsDir() (string, error) {
	dir, err := filepath.Abs(env.Get("SCRIPTS_DIR", "host_files/scripts"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("scripts directory %s: %w", dir, err)
	}
	return dir, nil
}

// handleSaveDefaultPath reports the Save As dialog's starting folder and the
// server's path separator, so the client builds a path that is valid on the
// server's OS (the app may run in a Linux container).
func (s *Server) handleSaveDefaultPath(w http.ResponseWriter, r *http.Request) {
	dir, err := scriptsDir()
	if err != nil {
		log.Printf("[save] Failed to resolve scripts directory: %v", err)
		http.Error(w, "Failed to resolve scripts directory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"dir": dir, "sep": string(filepath.Separator)})
}

// handleSaveScript writes the submitted content to the given path. The
// parent folder must already exist; relative paths resolve against the
// process working directory.
func (s *Server) handleSaveScript(w http.ResponseWriter, r *http.Request) {
	var req saveScriptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[save] Invalid request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	abs, err := filepath.Abs(filepath.Clean(req.Path))
	if err != nil {
		log.Printf("[save] Invalid save path %q: %v", req.Path, err)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	dir := filepath.Dir(abs)
	info, err := os.Stat(dir)
	if err != nil {
		log.Printf("[save] Directory does not exist for path %q: %v", abs, err)
		http.Error(w, "Directory does not exist: "+dir, http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		log.Printf("[save] Not a directory: %s", dir)
		http.Error(w, "Not a directory: "+dir, http.StatusBadRequest)
		return
	}

	if err := os.WriteFile(abs, []byte(req.Content), 0644); err != nil {
		log.Printf("[save] WriteFile %q failed: %v", abs, err)
		http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"saved": true, "path": abs})
}
