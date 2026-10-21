package web

import (
	"context"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// -----------------------------------------------------------------------------
// Import / Export data dialog for one table. The file travels through the
// browser: export streams COPY ... TO STDOUT as a download, import streams the
// uploaded file into COPY ... FROM STDIN (no temp file). The COPY statement is
// built server-side from validated options and can be previewed first.
// -----------------------------------------------------------------------------

const (
	copyImport = "import"
	copyExport = "export"
)

var (
	copyEncodingRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	copyExtensions = map[string]string{"csv": ".csv", "text": ".txt", "binary": ".bin"}
	copyTypes      = map[string]string{"csv": "text/csv; charset=utf-8", "text": "text/plain; charset=utf-8", "binary": "application/octet-stream"}
)

// copyChar validates a one-byte COPY option (delimiter, quote, escape) and
// renders it as a SQL literal. "tab" stands for the tab character.
func copyChar(label, s string) (string, error) {
	if s == "tab" || s == `\t` {
		return `E'\t'`, nil
	}
	if len(s) != 1 || s == "\n" || s == "\r" {
		return "", formErr(label + " must be a single character (or \"tab\").")
	}
	return quoteLiteral(s), nil
}

// buildCopySQL renders the COPY statement of one direction. cols is the
// optional column subset.
func buildCopySQL(dir string, v map[string]string, cols []string) (string, error) {
	if dir != copyImport && dir != copyExport {
		return "", formErr("Unknown direction.")
	}
	schema, table := strings.TrimSpace(v["schema"]), strings.TrimSpace(v["table"])
	if schema == "" || table == "" {
		return "", errRequired("Table")
	}
	format := v["format"]
	if format == "" {
		format = "csv"
	}
	if _, ok := copyExtensions[format]; !ok {
		return "", formErr("Unsupported format.")
	}

	opts := []string{"FORMAT " + format}
	add := func(opt, label, value string, allowed bool) error {
		if value == "" || !allowed {
			return nil
		}
		lit, err := copyChar(label, value)
		if err != nil {
			return err
		}
		opts = append(opts, opt+" "+lit)
		return nil
	}
	text := format != "binary"
	if format == "csv" && v["header"] == "on" {
		opts = append(opts, "HEADER true")
	}
	if err := add("DELIMITER", "Delimiter", v["delimiter"], text); err != nil {
		return "", err
	}
	if text && v["null"] != "" {
		opts = append(opts, "NULL "+quoteLiteral(v["null"]))
	}
	if err := add("QUOTE", "Quote", v["quote"], format == "csv"); err != nil {
		return "", err
	}
	if err := add("ESCAPE", "Escape", v["escape"], format == "csv"); err != nil {
		return "", err
	}
	if enc := v["encoding"]; enc != "" {
		if !copyEncodingRe.MatchString(enc) {
			return "", formErr("Invalid encoding.")
		}
		opts = append(opts, "ENCODING "+quoteLiteral(enc))
	}

	target := qualIdent(schema, table)
	var quoted []string
	for _, c := range cols {
		if c = strings.TrimSpace(c); c != "" {
			quoted = append(quoted, quoteIdent(c))
		}
	}
	if len(quoted) > 0 {
		target += " (" + strings.Join(quoted, ", ") + ")"
	}
	endpoint := "FROM STDIN"
	if dir == copyExport {
		endpoint = "TO STDOUT"
	}
	return "COPY " + target + " " + endpoint + " WITH (" + strings.Join(opts, ", ") + ")", nil
}

// readImportRequest reads the multipart body up to the file part and returns
// the text fields seen before it together with the still-open file part. The
// file input must come last in the form, so the upload can be streamed.
func readImportRequest(mr *multipart.Reader) (url.Values, *multipart.Part, error) {
	fields := url.Values{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, nil, formErr("Choose a file to import.")
		}
		if err != nil {
			return nil, nil, formErr("Invalid upload: " + err.Error())
		}
		if part.FormName() == "file" {
			if part.FileName() == "" {
				return nil, nil, formErr("Choose a file to import.")
			}
			return fields, part, nil
		}
		val, err := io.ReadAll(io.LimitReader(part, 64<<10))
		if err != nil {
			return nil, nil, formErr("Invalid upload: " + err.Error())
		}
		fields.Add(part.FormName(), string(val))
	}
}

type copyModalData struct {
	Dir      string
	ServerID int64
	DB       string
	Schema   string
	Table    string
	Columns  []string
	Values   map[string]string
	Error    string
	Message  string
}

// copyColumns lists the table's non-generated columns, best-effort: a failure
// is logged and the dialog simply offers no column picker.
func (s *Server) copyColumns(ctx context.Context, sid int64, db, schema, table string) []string {
	pool, err := s.getOrCreateDbPool(ctx, sid, db)
	if err != nil {
		log.Printf("Copy dialog columns pool: %v", err)
		return nil
	}
	rows, err := pool.Query(ctx, `SELECT a.attname FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated::text = ''
ORDER BY a.attnum`, schema, table)
	if err != nil {
		log.Printf("Copy dialog columns: %v", err)
		return nil
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			cols = append(cols, c)
		}
	}
	return cols
}

func (s *Server) renderCopyModal(w http.ResponseWriter, r *http.Request, m copyModalData) {
	if m.Values == nil {
		m.Values = map[string]string{"format": "csv", "header": "on"}
	}
	m.Columns = s.copyColumns(r.Context(), m.ServerID, m.DB, m.Schema, m.Table)
	RenderPartial(w, "import_export_modal.html", m)
}

func copyDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	dir := chi.URLParam(r, "dir")
	if dir != copyImport && dir != copyExport {
		http.Error(w, "Unknown direction", http.StatusNotFound)
		return "", false
	}
	return dir, true
}

func (s *Server) handleCopyModal(w http.ResponseWriter, r *http.Request) {
	dir, ok := copyDir(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sid, _ := strconv.ParseInt(q.Get("server_id"), 10, 64)
	s.renderCopyModal(w, r, copyModalData{Dir: dir, ServerID: sid, DB: q.Get("db"), Schema: q.Get("schema"), Table: q.Get("table")})
}

func (s *Server) handleCopyPreview(w http.ResponseWriter, r *http.Request) {
	dir, ok := copyDir(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	sqlStr, err := buildCopySQL(dir, formValues(r.Form), r.Form["col"])
	if err != nil {
		sqlStr = "Error: " + err.Error()
	}
	RenderPartial(w, "ddl_preview.html", map[string]any{"Contents": sqlStr})
}

// copyConn acquires a connection on the target database for a COPY.
func (s *Server) copyConn(ctx context.Context, sid int64, db string) (*pgxpool.Conn, error) {
	if s.isDisconnected(sid) {
		return nil, formErr("Server is disconnected. Reconnect it first.")
	}
	pool, err := s.getOrCreateDbPool(ctx, sid, db)
	if err != nil {
		return nil, err
	}
	return pool.Acquire(ctx)
}

func (s *Server) handleCopyExport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	v := formValues(r.Form)
	sid, _ := strconv.ParseInt(v["server_id"], 10, 64)
	sqlStr, err := buildCopySQL(copyExport, v, r.Form["col"])
	if err != nil || sid < 1 || v["db"] == "" {
		if err == nil {
			err = formErr("Missing server or database.")
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backupTimeout)
	defer cancel()
	conn, err := s.copyConn(ctx, sid, v["db"])
	if err != nil {
		http.Error(w, "Cannot connect to database: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Release()

	format := v["format"]
	if format == "" {
		format = "csv"
	}
	name := unsafeNameRe.ReplaceAllString(v["table"], "_") + copyExtensions[format]
	out := &firstWriteHeaders{w: w, filename: name, contentType: copyTypes[format]}
	if _, err := conn.Conn().PgConn().CopyTo(ctx, out, sqlStr); err != nil {
		log.Printf("[copy] export of %s.%s failed: %v", v["schema"], v["table"], err)
		if !out.started {
			http.Error(w, "Export failed: "+err.Error(), http.StatusBadRequest)
		}
	}
}

func (s *Server) handleCopyImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	m := copyModalData{Dir: copyImport}
	fail := func(err error) {
		m.Error = err.Error()
		s.renderCopyModal(w, r, m)
	}

	mr, err := r.MultipartReader()
	if err != nil {
		fail(formErr("Invalid upload."))
		return
	}
	fields, file, err := readImportRequest(mr)
	v := formValues(fields)
	m.ServerID, _ = strconv.ParseInt(v["server_id"], 10, 64)
	m.DB, m.Schema, m.Table, m.Values = v["db"], v["schema"], v["table"], v
	if err != nil {
		fail(err)
		return
	}
	sqlStr, err := buildCopySQL(copyImport, v, fields["col"])
	if err != nil {
		fail(err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), backupTimeout)
	defer cancel()
	conn, err := s.copyConn(ctx, m.ServerID, m.DB)
	if err != nil {
		fail(err)
		return
	}
	defer conn.Release()
	tag, err := conn.Conn().PgConn().CopyFrom(ctx, file, sqlStr)
	if err != nil {
		log.Printf("[copy] import into %s.%s failed: %v", m.Schema, m.Table, err)
		fail(err)
		return
	}
	m.Message = "Imported " + strconv.FormatInt(tag.RowsAffected(), 10) + " rows into " + m.Schema + "." + m.Table + "."
	s.renderCopyModal(w, r, m)
}
