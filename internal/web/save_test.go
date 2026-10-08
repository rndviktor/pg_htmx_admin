package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveDefaultPathUsesScriptsDir(t *testing.T) {
	want := filepath.Join(t.TempDir(), "scripts")
	t.Setenv("SCRIPTS_DIR", want)
	w := httptest.NewRecorder()
	(&Server{}).handleSaveDefaultPath(w, httptest.NewRequest("GET", "/api/save-default-path", nil))
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["dir"] != want || got["sep"] != string(filepath.Separator) {
		t.Fatalf("got %v, want dir %q", got, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("scripts dir not created: %v", err)
	}
}
