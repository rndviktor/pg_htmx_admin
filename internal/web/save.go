package web

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
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

// handleSaveDefaultPath reports the server process's working directory.
// The client uses it as the starting folder for the Save As dialog
// ("nearest available to the process folder").
func (s *Server) handleSaveDefaultPath(w http.ResponseWriter, r *http.Request) {
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("[save] Failed to resolve working directory: %v", err)
		http.Error(w, "Failed to resolve working directory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"cwd": filepath.Clean(cwd)})
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
