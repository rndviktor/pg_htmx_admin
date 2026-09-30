package web

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
)

// Storage Manager: list, upload, download and delete the backup files held in
// the storage directory (backupDir). It is deliberately limited to that one
// flat directory and to validated base names (see storageNameRe).

// maxUploadBytes caps one uploaded backup file.
const maxUploadBytes = 4 << 30

type storageData struct {
	Files []backupFile
	Dir   string
	Error string
}

func (s *Server) renderStorage(w http.ResponseWriter, errMsg string) {
	files, err := listBackupFiles(false)
	if err != nil {
		log.Printf("Storage manager: list files: %v", err)
		errMsg = err.Error()
	}
	dir, _ := backupDir()
	RenderPartial(w, "storage_modal.html", storageData{Files: files, Dir: dir, Error: errMsg})
}

func (s *Server) handleStorageModal(w http.ResponseWriter, r *http.Request) {
	s.renderStorage(w, "")
}

func (s *Server) handleStorageDownload(w http.ResponseWriter, r *http.Request) {
	path, err := storagePath(chi.URLParam(r, "name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filepath.Base(path)))
	http.ServeFile(w, r, path)
}

func (s *Server) handleStorageDelete(w http.ResponseWriter, r *http.Request) {
	path, err := storagePath(chi.URLParam(r, "name"))
	if err != nil {
		s.renderStorage(w, err.Error())
		return
	}
	if err := os.Remove(path); err != nil {
		log.Printf("Storage manager: delete %s: %v", path, err)
		s.renderStorage(w, "Could not delete the file.")
		return
	}
	s.renderStorage(w, "")
}

func (s *Server) handleStorageUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		s.renderStorage(w, "Choose a file to upload.")
		return
	}
	defer file.Close()

	path, err := storagePath(filepath.Base(hdr.Filename))
	if err != nil {
		s.renderStorage(w, err.Error())
		return
	}
	// O_EXCL: never overwrite an existing backup.
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		s.renderStorage(w, "A file with that name already exists.")
		return
	}
	_, copyErr := io.Copy(dst, file)
	if closeErr := dst.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		log.Printf("Storage manager: upload %s: %v", path, copyErr)
		os.Remove(path)
		s.renderStorage(w, "Upload failed.")
		return
	}
	s.renderStorage(w, "")
}
