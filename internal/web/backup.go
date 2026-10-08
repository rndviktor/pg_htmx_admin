package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
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
// (pg_dump / pg_dumpall / pg_restore / psql). Like the DDL and Maintenance
// dialogs the client only posts form values: the server builds the argument
// list, shows it as a preview, and runs it as a background job (jobs.go).
// Commands are executed without a shell (exec.Command with an argv slice),
// every value that could start with "-" is passed in --opt=value form, and the
// password travels in PGPASSWORD, never on the command line. Files are read
// and written only inside the storage directory (see backupDir); file names
// are validated, never paths.

// backupCmd is one fully built client-tool invocation. Out is the file or
// directory a dump creates (removed again if the job fails or is cancelled);
// it is empty for restores.
type backupCmd struct {
	Tool string
	Args []string
	Out  string
}

type backupOp struct {
	Label string
	Tool  string // default tool, shown in the modal before a command is built
	Build func(c pgConn, v map[string]string) (backupCmd, error)
}

var backupOps = map[string]backupOp{
	"backup":         {Label: "Backup", Tool: "pg_dump", Build: buildPgDump},
	"backup-globals": {Label: "Backup Globals", Tool: "pg_dumpall", Build: buildPgDumpAll},
	"restore":        {Label: "Restore", Tool: "pg_restore", Build: buildPgRestore},
}

// backupTimeout bounds one background dump/restore job.
const backupTimeout = 30 * time.Minute

// maxToolOutput is how much of a job's combined output is kept and shown.
const maxToolOutput = 16 << 10

// maxParallelJobs caps --jobs for directory dumps and parallel restores.
const maxParallelJobs = 8

// storageNameRe is the set of file names the Storage Manager will touch: a
// plain base name, no separators, no leading dot or dash.
var storageNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

var encodingRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

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
	dir, err := filepath.Abs(env.Get("BACKUP_DIR", "host_files/backups"))
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
	"custom":    {"custom", ".dump"},
	"tar":       {"tar", ".tar"},
	"plain":     {"plain", ".sql"},
	"directory": {"directory", ""},
}

// jobsFlag validates the optional "parallel jobs" field and returns the
// --jobs flag, or "" when unset.
func jobsFlag(v map[string]string) (string, error) {
	j := strings.TrimSpace(v["jobs"])
	if j == "" {
		return "", nil
	}
	n, err := strconv.Atoi(j)
	if err != nil || n < 1 || n > maxParallelJobs {
		return "", formErr("Parallel jobs must be between 1 and " + strconv.Itoa(maxParallelJobs) + ".")
	}
	return "--jobs=" + j, nil
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

// commonFlags appends the options pg_dump and pg_restore share. --verbose is
// always on: its log lines are the only progress the tools report.
func commonFlags(args []string, v map[string]string) []string {
	args = append(args, "--verbose")
	for _, o := range [][2]string{
		{"no_owner", "--no-owner"}, {"no_privileges", "--no-privileges"},
		{"clean", "--clean"}, {"create", "--create"},
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

func buildPgDump(c pgConn, v map[string]string) (backupCmd, error) {
	f, ok := backupFormats[v["format"]]
	if !ok {
		return backupCmd{}, formErr("Unsupported format.")
	}
	if c.DB == "" {
		return backupCmd{}, formErr("Database is required.")
	}
	if err := validateCleanOpts(v); err != nil {
		return backupCmd{}, err
	}
	name, err := backupFileName(v["filename"], f.Ext)
	if err != nil {
		return backupCmd{}, err
	}
	path, err := storagePath(name)
	if err != nil {
		return backupCmd{}, err
	}

	args := append(c.args(), "--format="+f.Flag, "--file="+path)
	if z := v["compress"]; z != "" && (v["format"] == "custom" || v["format"] == "directory") {
		if n, err := strconv.Atoi(z); err != nil || n < 0 || n > 9 {
			return backupCmd{}, formErr("Compression level must be 0-9.")
		}
		args = append(args, "--compress="+z)
	}
	jf, err := jobsFlag(v)
	if err != nil {
		return backupCmd{}, err
	}
	if jf != "" && v["format"] == "directory" {
		args = append(args, jf)
	}
	if enc := strings.TrimSpace(v["encoding"]); enc != "" {
		if !encodingRe.MatchString(enc) {
			return backupCmd{}, formErr("Invalid encoding.")
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
	return backupCmd{Tool: "pg_dump", Args: append(args, "--dbname="+c.DB), Out: path}, nil
}

func buildPgDumpAll(c pgConn, v map[string]string) (backupCmd, error) {
	name, err := backupFileName(v["filename"], ".sql")
	if err != nil {
		return backupCmd{}, err
	}
	path, err := storagePath(name)
	if err != nil {
		return backupCmd{}, err
	}
	args := append(c.args(), "--file="+path, "--verbose")
	switch v["scope"] {
	case "roles":
		args = append(args, "--roles-only")
	case "tablespaces":
		args = append(args, "--tablespaces-only")
	default:
		args = append(args, "--globals-only")
	}
	return backupCmd{Tool: "pg_dumpall", Args: args, Out: path}, nil
}

// buildPgRestore restores one stored backup. Plain .sql files go through psql
// (pg_restore cannot read them); custom/tar archives and directory dumps go
// through pg_restore.
func buildPgRestore(c pgConn, v map[string]string) (backupCmd, error) {
	if c.DB == "" || strings.ContainsAny(c.DB, "=/") {
		return backupCmd{}, formErr("Invalid database name.")
	}
	if err := validateCleanOpts(v); err != nil {
		return backupCmd{}, err
	}
	path, err := storagePath(v["filename"])
	if err != nil {
		return backupCmd{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return backupCmd{}, formErr("Backup file not found in the storage directory.")
	}
	if !info.IsDir() && strings.HasSuffix(path, ".sql") {
		return buildPsqlRestore(c, v, path), nil
	}

	args := commonFlags(c.args(), v)
	jf, err := jobsFlag(v)
	if err != nil {
		return backupCmd{}, err
	}
	if jf != "" {
		if v["single_transaction"] == "on" {
			return backupCmd{}, formErr("Parallel jobs cannot be combined with a single transaction.")
		}
		args = append(args, jf)
	}
	for _, o := range [][2]string{
		{"single_transaction", "--single-transaction"}, {"exit_on_error", "--exit-on-error"},
	} {
		if v[o[0]] == "on" {
			args = append(args, o[1])
		}
	}
	return backupCmd{Tool: "pg_restore", Args: append(args, "--dbname="+c.DB, path)}, nil
}

// buildPsqlRestore replays a plain SQL script. Only "exit on error" and
// "single transaction" apply; the dump-time options are baked into the script.
func buildPsqlRestore(c pgConn, v map[string]string, path string) backupCmd {
	args := append(c.args(), "--no-psqlrc", "--file="+path)
	if v["exit_on_error"] == "on" {
		args = append(args, "--set=ON_ERROR_STOP=1")
	}
	if v["single_transaction"] == "on" {
		args = append(args, "--single-transaction")
	}
	return backupCmd{Tool: "psql", Args: append(args, "--dbname="+c.DB)}
}

// toolPreview renders the command as shown in the preview panel.
func toolPreview(tool string, args []string) string {
	return strings.TrimSpace(tool + " " + strings.Join(args, " "))
}

// backupFile is one row of the Storage Manager listing.
type backupFile struct {
	Name    string
	Size    string
	ModTime string
	IsDir   bool // a directory-format dump
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

// listBackupFiles lists the stored backups, newest first: regular files plus
// directory-format dumps (subdirectories holding a toc.dat).
func listBackupFiles() ([]backupFile, error) {
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
		if err != nil || !storageNameRe.MatchString(e.Name()) {
			continue
		}
		f := backupFile{Name: e.Name(), ModTime: info.ModTime().Format("2006-01-02 15:04")}
		switch {
		case info.Mode().IsRegular():
			f.Size = humanSize(info.Size())
		case info.IsDir() && isDirDump(filepath.Join(dir, e.Name())):
			f.IsDir, f.Size = true, humanSize(dirSize(filepath.Join(dir, e.Name())))
		default:
			continue
		}
		items = append(items, item{f: f, mod: info.ModTime()})
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

// isDirDump reports whether path is a pg_dump directory-format archive.
func isDirDump(path string) bool {
	_, err := os.Stat(filepath.Join(path, "toc.dat"))
	return err == nil
}

// dirSize sums the size of the regular files under path (or the file itself).
func dirSize(path string) int64 {
	var total int64
	filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
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
}

func (s *Server) renderBackupModal(w http.ResponseWriter, m backupModalData) {
	if m.Values == nil {
		m.Values = map[string]string{}
	}
	if op, ok := backupOps[m.Op]; ok {
		m.OpLabel, m.Tool = op.Label, op.Tool
	}
	if m.Op == "restore" {
		files, err := listBackupFiles()
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
	return unsafeNameRe.ReplaceAllString(base, "_") + "_" + time.Now().Format("20060102_150405")
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
		var cmd backupCmd
		if cmd, err = o.Build(c, v); err == nil {
			contents = toolPreview(cmd.Tool, cmd.Args)
		}
	}
	if err != nil {
		contents = "Error: " + err.Error()
	}
	RenderPartial(w, "ddl_preview.html", map[string]any{"Contents": contents})
}

// handleBackupRun validates the request, starts the command as a background
// job (see jobs.go) and answers with the job's progress panel.
func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	op, o, ok := lookupBackupOp(w, r)
	if !ok {
		return
	}
	c, v, err := s.backupRequest(r)
	sid, _ := strconv.ParseInt(r.FormValue("server_id"), 10, 64)
	rerender := func(errMsg string) {
		s.renderBackupModal(w, backupModalData{
			Op: op, ServerID: sid, DB: r.FormValue("db"), Schema: r.FormValue("schema"),
			Table: r.FormValue("table"), Values: v, Error: errMsg,
		})
	}
	if err != nil {
		rerender(err.Error())
		return
	}
	if s.isDisconnected(sid) {
		rerender("Server is disconnected. Reconnect it first.")
		return
	}
	cmd, err := o.Build(c, v)
	if err != nil {
		rerender(err.Error())
		return
	}
	if cmd.Out != "" {
		if _, statErr := os.Lstat(cmd.Out); statErr == nil {
			rerender("A file with that name already exists in the storage directory.")
			return
		}
	}

	j, err := startJob(o.Label+" "+jobTarget(op, c.DB, v), cmd, c.env())
	if err != nil {
		log.Printf("Backup %s: start failed: %v", op, err)
		rerender(err.Error())
		return
	}
	RenderPartial(w, "backup_job.html", j.view())
}

// jobTarget names what a job acts on, for the jobs list.
func jobTarget(op, dbName string, v map[string]string) string {
	switch {
	case op == "backup-globals":
		return "(cluster globals)"
	case op == "restore":
		return v["filename"] + " -> " + dbName
	case v["table"] != "":
		return dbName + "." + v["schema"] + "." + v["table"]
	case v["schema"] != "":
		return dbName + "." + v["schema"]
	}
	return dbName
}
