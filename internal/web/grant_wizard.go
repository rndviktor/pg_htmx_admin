package web

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pgdb "htmx-golang-excercise/internal/sqlc/postgres/db"
)

// -----------------------------------------------------------------------------
// Grant Wizard. A schema-level side panel that GRANTs / REVOKEs the chosen
// privileges to the chosen roles on many objects at once: individually ticked
// objects, or every table / sequence / function / procedure of the schema via
// GRANT ... ON ALL ... IN SCHEMA. Same flow as the privilege editor: the form
// posts to /preview, the server returns the SQL, the script tab runs it.
// -----------------------------------------------------------------------------

// grantType is one object group of the wizard. AllKeyword is the plural used
// by "ON ALL <AllKeyword> IN SCHEMA" ("" when Postgres has no such form for
// the group: views and materialized views are covered by ALL TABLES).
type grantType struct {
	Kind       string // key into privKinds
	Label      string
	AllKeyword string
	AllLabel   string
	relkinds   []string // pg_class.relkind / pg_proc.prokind values listing the group
	routine    bool
}

var grantTypes = []grantType{
	{Kind: "table", Label: "Tables", AllKeyword: "TABLES", AllLabel: "All tables, views and materialized views", relkinds: []string{"r", "p"}},
	{Kind: "view", Label: "Views", relkinds: []string{"v"}},
	{Kind: "matview", Label: "Materialized views", relkinds: []string{"m"}},
	{Kind: "sequence", Label: "Sequences", AllKeyword: "SEQUENCES", AllLabel: "All sequences", relkinds: []string{"S"}},
	{Kind: "function", Label: "Functions", AllKeyword: "FUNCTIONS", AllLabel: "All functions", relkinds: []string{"f", "a", "w"}, routine: true},
	{Kind: "procedure", Label: "Procedures", AllKeyword: "PROCEDURES", AllLabel: "All procedures", relkinds: []string{"p"}, routine: true},
}

const maxGrantObjects = 2000

// wizardPrivs is every privilege any wizard group accepts, in display order.
func wizardPrivs() []string {
	var out []string
	for _, gt := range grantTypes {
		for _, p := range privKinds[gt.Kind].Privs {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// buildGrantWizard turns the wizard form into GRANT / REVOKE statements. Each
// group only receives the selected privileges it supports.
func buildGrantWizard(form url.Values) (string, error) {
	v := formValues(form)
	schema := strings.TrimSpace(v["schema"])
	if schema == "" {
		return "", errRequired("Schema")
	}
	var grantees []string
	for _, g := range form["grantee"] {
		if g != "" && !slices.Contains(grantees, g) {
			grantees = append(grantees, granteeSQL(g))
		}
	}
	revoke := v["mode"] == "revoke"
	allTables := v["all_table"] == "on"

	var stmts []string
	total := 0
	for _, gt := range grantTypes {
		pk := privKinds[gt.Kind]
		var privs []string
		for _, p := range pk.Privs {
			if slices.Contains(form["priv"], p) {
				privs = append(privs, p)
			}
		}
		var scope string
		switch {
		case gt.AllKeyword != "" && v["all_"+gt.Kind] == "on":
			scope = "ALL " + gt.AllKeyword + " IN SCHEMA " + quoteIdent(schema)
		case allTables && (gt.Kind == "view" || gt.Kind == "matview"):
			continue // already covered by ALL TABLES
		default:
			var refs []string
			for _, name := range form["obj_"+gt.Kind] {
				target, err := privTarget(pk, schema, name)
				if err != nil {
					return "", err
				}
				refs = append(refs, strings.TrimPrefix(target, pk.Keyword+" "))
			}
			if total += len(refs); total > maxGrantObjects {
				return "", formErr("Too many objects selected.")
			}
			if len(refs) > 0 {
				scope = pk.Keyword + " " + strings.Join(refs, ", ")
			}
		}
		if scope == "" || len(privs) == 0 || len(grantees) == 0 {
			continue
		}
		list := strings.Join(grantees, ", ")
		if revoke {
			stmt := "REVOKE " + strings.Join(privs, ", ") + " ON " + scope + " FROM " + list
			if v["cascade"] == "on" {
				stmt += " CASCADE"
			}
			stmts = append(stmts, stmt)
			continue
		}
		stmt := "GRANT " + strings.Join(privs, ", ") + " ON " + scope + " TO " + list
		if v["grantopt"] == "on" {
			stmt += " WITH GRANT OPTION"
		}
		stmts = append(stmts, stmt)
	}
	if len(stmts) == 0 {
		return "", formErr("No changes requested. Pick objects, privileges and grantees.")
	}
	return joinStatements(stmts), nil
}

// listGrantObjects returns the schema's objects of one wizard group. Routines
// are named "name(identity arguments)", the form privTarget expects.
func listGrantObjects(ctx context.Context, pool *pgxpool.Pool, gt grantType, schema string) ([]string, error) {
	q := `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind::text = ANY($2) ORDER BY 1`
	if gt.routine {
		q = `SELECT p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.prokind::text = ANY($2) ORDER BY 1`
	}
	rows, err := pool.Query(ctx, q, schema, gt.relkinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

type grantWizardData struct {
	PanelID, Title, PreviewURL, ObjectsURL, ResetURL, ConnDB, Action, FolderID string
	ServerID                                                                   int64
	DB, Schema, Error                                                          string
	Grantees, Privs                                                            []string
}

type grantGroup struct {
	grantType
	Objects []string
}

type grantObjectsData struct {
	PanelID string
	Groups  []grantGroup
	Error   string
}

// grantParams reads and validates the query parameters shared by the wizard's
// panel and object-list endpoints.
func grantParams(w http.ResponseWriter, r *http.Request) (sid int64, db, schema, panelID string, ok bool) {
	q := r.URL.Query()
	sid, _ = strconv.ParseInt(q.Get("server_id"), 10, 64)
	db, schema, panelID = q.Get("db"), q.Get("schema"), q.Get("panel_id")
	if !panelIDPattern.MatchString(panelID) || db == "" || schema == "" || sid < 1 {
		http.Error(w, "Invalid grant wizard request", http.StatusBadRequest)
		return 0, "", "", "", false
	}
	return sid, db, schema, panelID, true
}

func (s *Server) handleGrantPanel(w http.ResponseWriter, r *http.Request) {
	sid, db, schema, panelID, ok := grantParams(w, r)
	if !ok {
		return
	}
	m := grantWizardData{
		PanelID: panelID, ServerID: sid, DB: db, Schema: schema, Action: "grant", ConnDB: db,
		Title: "Grant Wizard: " + schema, PreviewURL: "/api/grant/preview", ResetURL: r.URL.RequestURI(),
		Privs: wizardPrivs(),
		ObjectsURL: "/api/grant/objects?" + url.Values{
			"server_id": {strconv.FormatInt(sid, 10)}, "db": {db}, "schema": {schema}, "panel_id": {panelID},
		}.Encode(),
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	pool, err := s.getOrCreateDbPool(ctx, sid, db)
	if err == nil {
		var roles []string
		if roles, err = pgdb.New(pool).ListRoles(ctx); err == nil {
			m.Grantees = append([]string{"PUBLIC"}, roles...)
		}
	}
	if err != nil {
		log.Printf("Grant wizard panel %s.%s: %v", db, schema, err)
		m.Error = "Cannot load roles: " + err.Error()
	}
	RenderPartial(w, "grant_wizard_panel.html", m)
}

func (s *Server) handleGrantObjects(w http.ResponseWriter, r *http.Request) {
	sid, db, schema, panelID, ok := grantParams(w, r)
	if !ok {
		return
	}
	m := grantObjectsData{PanelID: panelID}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	pool, err := s.getOrCreateDbPool(ctx, sid, db)
	for _, gt := range grantTypes {
		if err != nil {
			break
		}
		var names []string
		if names, err = listGrantObjects(ctx, pool, gt, schema); err == nil {
			m.Groups = append(m.Groups, grantGroup{grantType: gt, Objects: names})
		}
	}
	if err != nil {
		log.Printf("Grant wizard objects %s.%s: %v", db, schema, err)
		m.Error = "Cannot list objects: " + err.Error()
	}
	RenderPartial(w, "grant_wizard_objects.html", m)
}

func (s *Server) handleGrantPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	sqlStr, err := buildGrantWizard(r.Form)
	renderPreview(w, sqlStr, err)
}
