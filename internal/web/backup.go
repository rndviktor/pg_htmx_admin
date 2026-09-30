package web

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"htmx-golang-excercise/internal/db"
	"htmx-golang-excercise/internal/env"
	sqlite "htmx-golang-excercise/internal/sqlc/sqlite/db"
)

// Backup & Restore dialogs shell out to the PostgreSQL client tools
// (pg_dump / pg_dumpall / pg_restore). Like the DDL and Maintenance dialogs
// the client only posts form values: the server builds the argument list,
// shows it as a preview, and runs it. Commands are executed without a shell
// (exec.Command with an argv slice), every value that could start with "-"
// is passed in --opt=value form, and the password travels in PGPASSWORD,
// never on the command line. Files are read and written only inside the
// storage directory (see backupDir); file names are validated, never paths.

type backupOp struct {
	Label string
	Tool  string
	Build func(c pgConn, v map[string]string) (args []string, outFile string, err error)
}

var backupOps = map[string]backupOp{
	"backup":         {Label: "Backup", Tool: "pg_dump", Build: buildPgDump},
	"backup-globals": {Label: "Backup Globals", Tool: "pg_dumpall", Build: buildPgDumpAll},
	"restore":        {Label: "Restore", Tool: "pg_restore", Build: buildPgRestore},
}

// backupTimeout bounds one dump/restore; large databases need far longer than
// a DDL statement, but the request must not hang forever.
const backupTimeout = 30 * time.Minute

// maxToolOutput is how much of a tool's combined output is shown to the user.
const maxToolOutput = 16 << 10

// storageNameRe is the set of file names the Storage Manager will touch: a
// plain base name, no separators, no leading dot or dash.
var storageNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

var encodingRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

// pgConn carries the connection settings handed to a client tool.
type pgConn struct {
	Host, User, Password, SSLMode string
	Port                          int64
	DB                            string
}

// args returns the non-secret connection flags shared by every tool.
func (c pgConn) args() []string {
	return []string{
		"--host=" + c.Host,
		"--port=" + strconv.FormatInt(c.Port, 10),
		"--username=" + c.User,
		"--no-password",
	}
}

func (c pgConn) env() []string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "prefer"
	}
	return append(os.Environ(), "PGPASSWORD="+c.Password, "PGSSLMODE="+sslMode)
}

// backupDir returns the storage directory, creating it on first use.
func backupDir() (string, error) {
	dir, err := filepath.Abs(env.Get("BACKUP_DIR", "backups"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("storage directory %s: %w", dir, err)
	}
	return dir, nil
}

// storagePath resolves a validated base name inside the storage directory.
func storagePath(name string) (string, error) {
	if !storageNameRe.MatchString(name) {
		return "", formErr("Invalid file name. Use letters, digits, '.', '_' and '-' only.")
	}
	dir, err := backupDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// serverConn loads the stored credentials of a registered server. The
// password falls back to the in-memory one for rows saved before passwords
// were persisted, as in dialServer.
func (s *Server) serverConn(ctx context.Context, sid int64, dbName string) (pgConn, error) {
	srv, err := s.DB.GetServerByID(ctx, sqlite.GetServerByIDParams{ID: sid, UserID: db.DefaultUserID})
	if err != nil {
		return pgConn{}, fmt.Errorf("server %d not found", sid)
	}
	password := cachedPassword(srv.Name, srv.Host, srv.Port)
	if srv.Password.Valid {
		password = srv.Password.String
	}
	return pgConn{
		Host: srv.Host, Port: srv.Port, User: srv.Username,
		Password: password, SSLMode: srv.SslMode, DB: dbName,
	}, nil
}

// backupFileName validates the user-typed name and appends the extension for
// the chosen format when missing.
func backupFileName(name, ext string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", formErr("File name is required.")
	}
	if !strings.HasSuffix(name, ext) {
		name += ext
	}
	if !storageNameRe.MatchString(name) {
		return "", formErr("Invalid file name. Use letters, digits, '.', '_' and '-' only.")
	}
	return name, nil
}

var backupFormats = map[string]struct{ Flag, Ext string }{
	"custom": {"custom", ".dump"},
	"tar":    {"tar", ".tar"},
	"plain":  {"plain", ".sql"},
}

// contentFlag maps the all/data/schema radio to its pg_dump/pg_restore flag.
func contentFlag(v map[string]string) string {
	switch v["content"] {
	case "data":
		return "--data-only"
	case "schema":
		return "--schema-only"
	}
	return ""
}

// commonFlags appends the checkbox options pg_dump and pg_restore share.
func commonFlags(args []string, v map[string]string) []string {
	for _, o := range [][2]string{
		{"no_owner", "--no-owner"}, {"no_privileges", "--no-privileges"},
		{"clean", "--clean"}, {"create", "--create"}, {"verbose", "--verbose"},
	} {
		if v[o[0]] == "on" {
			args = append(args, o[1])
		}
	}
	if v["if_exists"] == "on" {
		args = append(args, "--if-exists")
	}
	if f := contentFlag(v); f != "" {
		args = append(args, f)
	}
	if role := strings.TrimSpace(v["role"]); role != "" {
		args = append(args, "--role="+role)
	}
	return args
}

// validateCleanOpts enforces pg_dump/pg_restore's rule that --if-exists only
// makes sense together with --clean.
func validateCleanOpts(v map[string]string) error {
	if v["if_exists"] == "on" && v["clean"] != "on" {
		return formErr("\"Use IF EXISTS\" only works together with \"Clean\"; tick Clean too.")
	}
	return nil
}

func buildPgDump(c pgConn, v map[string]string) ([]string, string, error) {
	f, ok := backupFormats[v["format"]]
	if !ok {
		return nil, "", formErr("Unsupported format.")
	}
	if c.DB == "" {
		return nil, "", formErr("Database is required.")
	}
	if err := validateCleanOpts(v); err != nil {
		return nil, "", err
	}
	name, err := backupFileName(v["filename"], f.Ext)
	if err != nil {
		return nil, "", err
	}
	path, err := storagePath(name)
	if err != nil {
		return nil, "", err
	}

	args := append(c.args(), "--format="+f.Flag, "--file="+path)
	if z := v["compress"]; z != "" && v["format"] == "custom" {
		if n, err := strconv.Atoi(z); err != nil || n < 0 || n > 9 {
			return nil, "", formErr("Compression level must be 0-9.")
		}
		args = append(args, "--compress="+z)
	}
	if enc := strings.TrimSpace(v["encoding"]); enc != "" {
		if !encodingRe.MatchString(enc) {
			return nil, "", formErr("Invalid encoding.")
		}
		args = append(args, "--encoding="+enc)
	}
	if v["inserts"] == "on" {
		args = append(args, "--inserts")
	}
	args = commonFlags(args, v)
	switch {
	case v["table"] != "":
		args = append(args, "--table="+qualIdent(v["schema"], v["table"]))
	case v["schema"] != "":
		args = append(args, "--schema="+quoteIdent(v["schema"]))
	}
	return append(args, "--dbname="+c.DB), path, nil
}

func buildPgDumpAll(c pgConn, v map[string]string) ([]string, string, error) {
	name, err := backupFileName(v["filename"], ".sql")
	if err != nil {
		return nil, "", err
	}
	path, err := storagePath(name)
	if err != nil {
		return nil, "", err
	}
	args := append(c.args(), "--file="+path)
	switch v["scope"] {
	case "roles":
		args = append(args, "--roles-only")
	case "tablespaces":
		args = append(args, "--tablespaces-only")
	default:
		args = append(args, "--globals-only")
	}
	if v["verbose"] == "on" {
		args = append(args, "--verbose")
	}
	return args, path, nil
}

func buildPgRestore(c pgConn, v map[string]string) ([]string, string, error) {
	if c.DB == "" || strings.ContainsAny(c.DB, "=/") {
		return nil, "", formErr("Invalid database name.")
	}
	if err := validateCleanOpts(v); err != nil {
		return nil, "", err
	}
	path, err := storagePath(v["filename"])
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, "", formErr("Backup file not found in the storage directory.")
	}
	args := c.args()
	args = commonFlags(args, v)
	for _, o := range [][2]string{
		{"single_transaction", "--single-transaction"}, {"exit_on_error", "--exit-on-error"},
	} {
		if v[o[0]] == "on" {
			args = append(args, o[1])
		}
	}
	return append(args, "--dbname="+c.DB, path), "", nil
}

// toolPreview renders the command as shown in the preview panel.
func toolPreview(tool string, args []string) string {
	return strings.TrimSpace(tool + " " + strings.Join(args, " "))
}

// runTool executes a client tool and returns its (truncated) combined output.
func runTool(ctx context.Context, tool string, args, environ []string) (string, error) {
	bin, err := exec.LookPath(tool)
	if err != nil {
		return "", fmt.Errorf("%s was not found on PATH; install the PostgreSQL client tools", tool)
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = environ
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	text := out.String()
	if len(text) > maxToolOutput {
		text = "...\n" + text[len(text)-maxToolOutput:]
	}
	return strings.TrimSpace(text), err
}

// backupFile is one row of the Storage Manager listing.
type backupFile struct {
	Name    string
	Size    string
	ModTime string
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// listBackupFiles lists regular files in the storage directory, newest first.
// With skipPlain set, plain-SQL dumps are omitted (pg_restore can't read them).
func listBackupFiles(skipPlain bool) ([]backupFile, error) {
	dir, err := backupDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type item struct {
		f   backupFile
		mod time.Time
	}
	var items []item
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || !storageNameRe.MatchString(e.Name()) {
			continue
		}
		if skipPlain && strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		items = append(items, item{
			f:   backupFile{Name: e.Name(), Size: humanSize(info.Size()), ModTime: info.ModTime().Format("2006-01-02 15:04")},
			mod: info.ModTime(),
		})
	}
	for i := 1; i < len(items); i++ { // insertion sort, newest first; lists are small
		for j := i; j > 0 && items[j].mod.After(items[j-1].mod); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	files := make([]backupFile, len(items))
	for i, it := range items {
		files[i] = it.f
	}
	return files, nil
}

// backupModalData is the view model for the shared backup/restore partial.
type backupModalData struct {
	Op       string
	OpLabel  string
	Tool     string
	ServerID int64
	DB       string
	Schema   string
	Table    string
	Values   map[string]string
	Files    []backupFile
	Error    string
	Output   string
}

func (s *Server) renderBackupModal(w http.ResponseWriter, m backupModalData) {
	if m.Values == nil {
		m.Values = map[string]string{}
	}
	if op, ok := backupOps[m.Op]; ok {
		m.OpLabel, m.Tool = op.Label, op.Tool
	}
	if m.Op == "restore" {
		files, err := listBackupFiles(true)
		if err != nil {
			log.Printf("Backup modal: list files: %v", err)
		}
		m.Files = files
	}
	RenderPartial(w, "backup_modal.html", m)
}

// defaultBackupName suggests <object>_<timestamp> for the file name field.
func defaultBackupName(op, db, schema, table string) string {
	base := "globals"
	if op != "backup-globals" {
		base = db
		if table != "" {
			base = table
		} else if schema != "" {
			base = schema
		}
	}
	base = regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(base, "_")
	return base + "_" + time.Now().Format("20060102_150405")
}

func lookupBackupOp(w http.ResponseWriter, r *http.Request) (string, backupOp, bool) {
	op := chi.URLParam(r, "op")
	o, ok := backupOps[op]
	if !ok {
		log.Printf("Backup: unknown op %q on %s", op, r.URL.Path)
		http.Error(w, "Unknown backup operation", http.StatusNotFound)
	}
	return op, o, ok
}

func (s *Server) handleBackupModal(w http.ResponseWriter, r *http.Request) {
	op, _, ok := lookupBackupOp(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sid, _ := strconv.ParseInt(q.Get("server_id"), 10, 64)
	m := backupModalData{
		Op: op, ServerID: sid, DB: q.Get("db"), Schema: q.Get("schema"), Table: q.Get("table"),
		Values: map[string]string{
			"format": "custom", "content": "all", "scope": "globals",
			"filename": defaultBackupName(op, q.Get("db"), q.Get("schema"), q.Get("table")),
		},
	}
	s.renderBackupModal(w, m)
}

// backupRequest parses the posted form shared by preview and run.
func (s *Server) backupRequest(r *http.Request) (pgConn, map[string]string, error) {
	if err := r.ParseForm(); err != nil {
		return pgConn{}, nil, formErr("Invalid form data.")
	}
	sid, err := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	if err != nil || sid < 1 {
		return pgConn{}, nil, formErr("Missing or invalid server id.")
	}
	c, err := s.serverConn(r.Context(), sid, r.FormValue("db"))
	return c, formValues(r.Form), err
}

func (s *Server) handleBackupPreview(w http.ResponseWriter, r *http.Request) {
	_, o, ok := lookupBackupOp(w, r)
	if !ok {
		return
	}
	contents := ""
	c, v, err := s.backupRequest(r)
	if err == nil {
		var args []string
		if args, _, err = o.Build(c, v); err == nil {
			contents = toolPreview(o.Tool, args)
		}
	}
	if err != nil {
		contents = "Error: " + err.Error()
	}
	RenderPartial(w, "ddl_preview.html", map[string]any{"Contents": contents})
}

func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	op, o, ok := lookupBackupOp(w, r)
	if !ok {
		return
	}
	c, v, err := s.backupRequest(r)
	sid, _ := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	rerender := func(errMsg, output string) {
		s.renderBackupModal(w, backupModalData{
			Op: op, ServerID: sid, DB: r.FormValue("db"), Schema: r.FormValue("schema"),
			Table: r.FormValue("table"), Values: v, Error: errMsg, Output: output,
		})
	}
	if err != nil {
		rerender(err.Error(), "")
		return
	}
	if s.isDisconnected(sid) {
		rerender("Server is disconnected. Reconnect it first.", "")
		return
	}
	args, outFile, err := o.Build(c, v)
	if err != nil {
		rerender(err.Error(), "")
		return
	}
	if outFile != "" {
		if _, statErr := os.Stat(outFile); statErr == nil {
			rerender("A file with that name already exists in the storage directory.", "")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), backupTimeout)
	defer cancel()
	output, err := runTool(ctx, o.Tool, args, c.env())
	if err != nil {
		log.Printf("Backup %s failed: %v", op, err)
		if outFile != "" {
			os.Remove(outFile) // never leave a truncated dump behind
		}
		rerender(o.Tool+" failed: "+err.Error(), output)
		return
	}
	file := v["filename"]
	if outFile != "" {
		file = filepath.Base(outFile)
	}
	RenderPartial(w, "backup_result.html", map[string]any{
		"Label": o.Label, "File": file, "Output": output,
		"Restore": op == "restore", "DB": c.DB,
	})
}
