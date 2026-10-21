package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
)

// -----------------------------------------------------------------------------
// Privilege editor. A side panel (same flow as the maintenance panel) that
// shows the explicit ACL of one object as a grantee x privilege grid. The form
// posts the edited grid plus the state it was loaded with; the server diffs the
// two into GRANT / REVOKE statements. The client never sends SQL.
// -----------------------------------------------------------------------------

// privKind describes one grantable object kind.
type privKind struct {
	Label   string
	Keyword string   // object keyword in GRANT ... ON <keyword> name
	Privs   []string // privilege names, in display order
	// ACL is the aclexplode query returning (grantee, privilege_type,
	// is_grantable). The ACL falls back to acldefault() so objects that were
	// never granted on still show their implicit owner / PUBLIC privileges.
	ACL        string
	UsesSchema bool // true: ACL takes ($1 schema, $2 name); false: ($1 name)
	Server     bool // true: the object is cluster-wide, use the maintenance pool
}

const privACLSelect = `SELECT COALESCE(r.rolname, 'PUBLIC'), a.privilege_type, a.is_grantable
FROM %s
LEFT JOIN pg_roles r ON r.oid = a.grantee
WHERE %s
ORDER BY 1, 2`

func privACL(from, where string) string {
	return fmt.Sprintf(privACLSelect, from, where)
}

var (
	tablePrivs = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}

	relACL = func(objType string) string {
		return privACL(
			"pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace, "+
				"LATERAL aclexplode(COALESCE(c.relacl, acldefault('"+objType+"', c.relowner))) AS a",
			"n.nspname = $1 AND c.relname = $2")
	}
	routineACL = privACL(
		"pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace, "+
			"LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) AS a",
		"n.nspname = $1 AND (p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')') = $2")
)

var privKinds = map[string]privKind{
	"database": {Label: "Database", Keyword: "DATABASE", Server: true,
		Privs: []string{"CONNECT", "CREATE", "TEMPORARY"},
		ACL: privACL("pg_database d, LATERAL aclexplode(COALESCE(d.datacl, acldefault('d', d.datdba))) AS a",
			"d.datname = $1")},
	"tablespace": {Label: "Tablespace", Keyword: "TABLESPACE", Server: true,
		Privs: []string{"CREATE"},
		ACL: privACL("pg_tablespace t, LATERAL aclexplode(COALESCE(t.spcacl, acldefault('t', t.spcowner))) AS a",
			"t.spcname = $1")},
	"schema": {Label: "Schema", Keyword: "SCHEMA",
		Privs: []string{"USAGE", "CREATE"},
		ACL: privACL("pg_namespace s, LATERAL aclexplode(COALESCE(s.nspacl, acldefault('n', s.nspowner))) AS a",
			"s.nspname = $1")},
	"table":     {Label: "Table", Keyword: "TABLE", Privs: tablePrivs, UsesSchema: true, ACL: relACL("r")},
	"view":      {Label: "View", Keyword: "TABLE", Privs: tablePrivs, UsesSchema: true, ACL: relACL("r")},
	"matview":   {Label: "Materialized View", Keyword: "TABLE", Privs: tablePrivs, UsesSchema: true, ACL: relACL("r")},
	"sequence":  {Label: "Sequence", Keyword: "SEQUENCE", Privs: []string{"USAGE", "SELECT", "UPDATE"}, UsesSchema: true, ACL: relACL("s")},
	"function":  {Label: "Function", Keyword: "FUNCTION", Privs: []string{"EXECUTE"}, UsesSchema: true, ACL: routineACL},
	"procedure": {Label: "Procedure", Keyword: "PROCEDURE", Privs: []string{"EXECUTE"}, UsesSchema: true, ACL: routineACL},
}

// privCell is one checkbox pair of the grid: the privilege and its grant option.
type privCell struct {
	Priv     string
	Has, Opt bool
}

// privRow is one grantee row. Orig encodes the state the row was loaded with
// ("SELECT*,INSERT": * marks WITH GRANT OPTION) and travels as a hidden field.
type privRow struct {
	Index   int
	Grantee string
	Orig    string
	Cells   []privCell
}

// privPanelData is the view model of priv_panel.html.
type privPanelData struct {
	PanelID, Kind, Title, PreviewURL, ResetURL, ConnDB, Action, FolderID string
	ServerID                                                             int64
	DB, Schema, Name, Error                                              string
	Rows                                                                 []privRow
	NewCells                                                             []privCell
	Grantees                                                             []string
}

// routineArgsPattern restricts the "(identity arguments)" part of a routine
// name, which is spliced into the SQL as typed. It must come from
// pg_get_function_identity_arguments: no statement separators, quotes other
// than identifier quotes, comments or nested parentheses.
var routineArgsPattern = regexp.MustCompile(`^\([A-Za-z0-9_ ,."\[\]]*\)$`)

// privTarget renders "<KEYWORD> <object>" for the GRANT / REVOKE statements.
func privTarget(pk privKind, schema, name string) (string, error) {
	if name == "" {
		return "", errRequired("Object name")
	}
	if pk.Keyword == "FUNCTION" || pk.Keyword == "PROCEDURE" {
		if i := strings.IndexByte(name, '('); i < 0 || !routineArgsPattern.MatchString(name[i:]) || strings.Count(name, `"`)%2 != 0 {
			return "", formErr("Invalid routine signature.")
		}
	}
	switch {
	case pk.Server || pk.Keyword == "SCHEMA":
		return pk.Keyword + " " + quoteIdent(name), nil
	case schema == "":
		return "", errRequired("Schema")
	case pk.Keyword == "FUNCTION" || pk.Keyword == "PROCEDURE":
		return pk.Keyword + " " + qualifiedRoutine(schema, name), nil
	}
	return pk.Keyword + " " + qualIdent(schema, name), nil
}

// parsePrivState decodes "SELECT*,INSERT" into privilege -> has grant option.
func parsePrivState(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range splitList(s) {
		p, opt := strings.CutSuffix(p, "*")
		m[p] = opt
	}
	return m
}

// granteeSQL renders a grantee: PUBLIC is a keyword, anything else an identifier.
func granteeSQL(g string) string {
	if g == "PUBLIC" {
		return g
	}
	return quoteIdent(g)
}

const maxPrivRows = 500

// buildPrivileges diffs the submitted grid against the state it was loaded
// with and emits REVOKE / GRANT statements per grantee.
func buildPrivileges(form url.Values) (string, error) {
	v := formValues(form)
	pk, ok := privKinds[v["kind"]]
	if !ok {
		return "", formErr("Unsupported object kind.")
	}
	target, err := privTarget(pk, v["schema"], v["name"])
	if err != nil {
		return "", err
	}
	rows, _ := strconv.Atoi(v["rows"])
	if rows < 0 || rows > maxPrivRows {
		return "", formErr("Too many grantees.")
	}

	var stmts []string
	for i := 0; i <= rows; i++ {
		suffix, grantee, orig := strconv.Itoa(i), v["grantee_"+strconv.Itoa(i)], v["orig_"+strconv.Itoa(i)]
		if i == rows { // the "add grantee" row
			suffix, grantee, orig = "new", v["newgrantee"], ""
		}
		if grantee == "" {
			continue
		}
		cur := map[string]bool{}
		for _, p := range form["priv_"+suffix] {
			if slices.Contains(pk.Privs, p) {
				cur[p] = slices.Contains(form["opt_"+suffix], p)
			}
		}
		stmts = append(stmts, privDiff(pk, target, granteeSQL(grantee), parsePrivState(orig), cur)...)
	}
	if len(stmts) == 0 {
		return "", formErr("No changes requested.")
	}
	return joinStatements(stmts), nil
}

// privDiff emits the statements that turn orig into cur for one grantee,
// grouping privileges that need the same statement. Revokes come first.
func privDiff(pk privKind, target, grantee string, orig, cur map[string]bool) []string {
	var revoke, dropOpt, grant, grantOpt []string
	for _, p := range pk.Privs {
		o, inOrig := orig[p]
		c, inCur := cur[p]
		switch {
		case inOrig && !inCur:
			revoke = append(revoke, p)
		case inCur && !inOrig:
			if c {
				grantOpt = append(grantOpt, p)
			} else {
				grant = append(grant, p)
			}
		case inCur && c && !o:
			grantOpt = append(grantOpt, p)
		case inCur && !c && o:
			dropOpt = append(dropOpt, p)
		}
	}
	var out []string
	emit := func(verb, prep, suffix string, privs []string) {
		if len(privs) > 0 {
			out = append(out, verb+" "+strings.Join(privs, ", ")+" ON "+target+" "+prep+" "+grantee+suffix)
		}
	}
	emit("REVOKE", "FROM", "", revoke)
	emit("REVOKE GRANT OPTION FOR", "FROM", "", dropOpt)
	emit("GRANT", "TO", "", grant)
	emit("GRANT", "TO", " WITH GRANT OPTION", grantOpt)
	return out
}

// loadPrivRows reads the object's current ACL and shapes it as grid rows.
func loadPrivRows(ctx context.Context, pool *pgxpool.Pool, pk privKind, schema, name string) ([]privRow, error) {
	args := []any{name}
	if pk.UsesSchema {
		args = []any{schema, name}
	}
	privs, err := queryPrivileges(ctx, pool, pk.ACL, args...)
	if err != nil {
		return nil, err
	}
	var rows []privRow
	index := map[string]int{}
	for _, p := range privs {
		i, seen := index[p.Grantee]
		if !seen {
			i = len(rows)
			index[p.Grantee] = i
			rows = append(rows, privRow{Index: i, Grantee: p.Grantee})
		}
		opt := p.Grantable == "Yes"
		rows[i].Cells = append(rows[i].Cells, privCell{Priv: p.Privilege, Has: true, Opt: opt})
	}
	for i := range rows {
		rows[i].Cells, rows[i].Orig = completeCells(pk, rows[i].Cells)
	}
	return rows, nil
}

// completeCells lays the granted cells out over every privilege of the kind
// (in display order) and returns the encoded original state.
func completeCells(pk privKind, granted []privCell) ([]privCell, string) {
	var cells []privCell
	var orig []string
	for _, p := range pk.Privs {
		c := privCell{Priv: p}
		for _, g := range granted {
			if g.Priv == p {
				c = g
				enc := p
				if g.Opt {
					enc += "*"
				}
				orig = append(orig, enc)
			}
		}
		cells = append(cells, c)
	}
	return cells, strings.Join(orig, ",")
}

func emptyCells(pk privKind) []privCell {
	cells, _ := completeCells(pk, nil)
	return cells
}

func (s *Server) privPool(ctx context.Context, pk privKind, sid int64, db string) (*pgxpool.Pool, error) {
	if pk.Server || db == "" {
		return s.getOrCreatePool(ctx, sid)
	}
	return s.getOrCreateDbPool(ctx, sid, db)
}

func (s *Server) handlePrivPanel(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	pk, ok := privKinds[kind]
	if !ok {
		log.Printf("Privilege panel: unknown kind %q on %s", kind, r.URL.Path)
		http.Error(w, "Unknown object kind", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	panelID := q.Get("panel_id")
	if !panelIDPattern.MatchString(panelID) {
		http.Error(w, "Invalid panel id", http.StatusBadRequest)
		return
	}
	sid, _ := strconv.ParseInt(q.Get("server_id"), 10, 64)
	m := privPanelData{
		PanelID: panelID, Kind: kind, ServerID: sid, DB: q.Get("db"), Schema: q.Get("schema"), Name: q.Get("name"),
		Action: "priv", PreviewURL: "/api/priv/" + kind + "/preview", ResetURL: r.URL.RequestURI(),
		Title: "Privileges: " + pk.Label + " " + q.Get("name"), NewCells: emptyCells(pk),
	}
	m.ConnDB = s.panelConnDB(r.Context(), sid, m.DB)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	pool, err := s.privPool(ctx, pk, sid, m.DB)
	if err == nil {
		m.Rows, err = loadPrivRows(ctx, pool, pk, m.Schema, m.Name)
	}
	if err != nil {
		log.Printf("Privilege panel %s %q: %v", kind, m.Name, err)
		m.Error = "Cannot load privileges: " + err.Error()
	} else if roles, rerr := pgdb.New(pool).ListRoles(ctx); rerr != nil {
		log.Printf("Privilege panel roles: %v", rerr)
	} else {
		m.Grantees = append([]string{"PUBLIC"}, roles...)
	}
	RenderPartial(w, "priv_panel.html", m)
}

func (s *Server) handlePrivPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := privKinds[chi.URLParam(r, "kind")]; !ok {
		http.Error(w, "Unknown object kind", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	r.Form.Set("kind", chi.URLParam(r, "kind"))
	sqlStr, err := buildPrivileges(r.Form)
	renderPreview(w, sqlStr, err)
}
