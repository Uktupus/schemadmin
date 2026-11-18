package integration

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/postgres"
	"github.com/Uktupus/schemadmin/sqlite"
)

// backend is a database the admin tests run against.
type backend struct {
	d      schemadmin.Dialect
	sqlite bool
	open   func(t *testing.T) *sql.DB // a fresh database with the admin fixture loaded
}

// forEachBackend runs fn against SQLite and, when a server is configured,
// against PostgreSQL through both drivers.
func forEachBackend(t *testing.T, fn func(t *testing.T, b backend)) {
	t.Run("sqlite", func(t *testing.T) {
		fn(t, backend{d: sqlite.New(), sqlite: true, open: func(t *testing.T) *sql.DB {
			db := openSQLite(t, "_pragma=foreign_keys(1)", "_pragma=busy_timeout(5000)")
			mustExec(t, db, readTestdata(t, "admin_sqlite.sql"))
			return db
		}})
	})
	base := os.Getenv(postgresDSNEnv)
	for _, driver := range postgresDrivers {
		t.Run("postgres-"+driver.name, func(t *testing.T) {
			if base == "" {
				t.Skipf("%s is not set", postgresDSNEnv)
			}
			fn(t, backend{d: postgres.New(), open: func(t *testing.T) *sql.DB {
				db := openPostgres(t, base, driver.driverName)
				mustExec(t, db, readTestdata(t, "admin_postgres.sql"))
				return db
			}})
		})
	}
}

// env is an admin over a fresh fixture database.
type env struct {
	t     *testing.T
	b     backend
	db    *sql.DB
	admin *schemadmin.Admin
	logs  *bytes.Buffer
}

// allowAll is the authorizer of tests that are not about authorization.
var allowAll = schemadmin.AuthorizerFunc(func(*http.Request, schemadmin.Action, string) bool { return true })

// deny returns an authorizer that refuses exactly the given action on the
// given table.
func deny(action schemadmin.Action, table string) schemadmin.Authorizer {
	return schemadmin.AuthorizerFunc(func(_ *http.Request, a schemadmin.Action, t string) bool {
		return !(a == action && t == table)
	})
}

func newEnv(t *testing.T, b backend, configure func(*schemadmin.Config)) *env {
	t.Helper()
	e := &env{t: t, b: b, db: b.open(t), logs: &bytes.Buffer{}}
	cfg := schemadmin.Config{
		DB:         e.db,
		Dialect:    b.d,
		Authorizer: allowAll,
		Logger:     slog.New(slog.NewTextHandler(e.logs, nil)),
	}
	if configure != nil {
		configure(&cfg)
	}
	admin, err := schemadmin.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.admin = admin
	return e
}

func (e *env) get(path string) (int, string) {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.admin.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.String()
}

// page is get that insists on 200.
func (e *env) page(path string) string {
	e.t.Helper()
	code, body := e.get(path)
	if code != http.StatusOK {
		e.t.Fatalf("GET %s: status %d\n%s", path, code, body)
	}
	return body
}

// post submits a form the way a browser does: same-origin.
func (e *env) post(path string, form url.Values) (code int, location, body string) {
	e.t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	e.admin.ServeHTTP(w, r)
	return w.Code, w.Header().Get("Location"), w.Body.String()
}

// column returns the position of a column among a table's columns, which is
// what names its form fields.
func (e *env) column(table, column string) (int, *schemadmin.Column) {
	e.t.Helper()
	for _, t := range e.admin.Schema().Tables {
		if t.Name != table {
			continue
		}
		for i, c := range t.Columns {
			if c.Name == column {
				return i, c
			}
		}
	}
	e.t.Fatalf("no column %s.%s", table, column)
	return 0, nil
}

func (e *env) valueField(table, column string) string {
	i, _ := e.column(table, column)
	return "v." + strconv.Itoa(i)
}

func (e *env) nullField(table, column string) string {
	i, _ := e.column(table, column)
	return "n." + strconv.Itoa(i)
}

// snapshot reads one row straight from the database, each column in a form
// that distinguishes every stored value from every other: NULL from the
// empty string, 1 from '1', 12.3 from 12.30.
func (e *env) snapshot(table, where string, args ...any) map[string]string {
	e.t.Helper()
	var tbl *schemadmin.Table
	for _, t := range e.admin.Schema().Tables {
		if t.Name == table {
			tbl = t
		}
	}
	if tbl == nil {
		e.t.Fatalf("no table %s", table)
	}
	exprs := make([]string, len(tbl.Columns))
	for i, c := range tbl.Columns {
		q := e.b.d.QuoteIdent(c.Name)
		if e.b.sqlite {
			exprs[i] = "typeof(" + q + ") || ':' || quote(" + q + ")"
		} else {
			exprs[i] = "quote_nullable(" + q + "::text)"
		}
	}
	dest := make([]any, len(exprs))
	vals := make([]string, len(exprs))
	for i := range dest {
		dest[i] = &vals[i]
	}
	query := "SELECT " + strings.Join(exprs, ", ") + " FROM " + tableRef(e.b.d, tbl) + " WHERE " + where
	if err := e.db.QueryRow(query, args...).Scan(dest...); err != nil {
		e.t.Fatalf("snapshot of %s where %s: %v", table, where, err)
	}
	out := make(map[string]string, len(vals))
	for i, c := range tbl.Columns {
		out[c.Name] = vals[i]
	}
	return out
}

// scalar runs a query that returns one text value.
func (e *env) scalar(query string, args ...any) string {
	e.t.Helper()
	var s sql.NullString
	if err := e.db.QueryRow(query, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	if !s.Valid {
		return "NULL"
	}
	return s.String
}

// arg is the n-th placeholder of the backend.
func (e *env) arg(n int) string { return e.b.d.Placeholder(n) }

// form is an HTML form as a browser sees it.
type form struct {
	action    string
	values    url.Values
	textareas map[string]bool
}

// parseForm finds the page's POST form and collects what a browser would
// submit from it untouched: the value of every input, the selected option
// of every select, the content of every textarea, and only those
// checkboxes that are ticked.
func parseForm(t *testing.T, page string) form {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	attr := func(n *html.Node, key string) (string, bool) {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val, true
			}
		}
		return "", false
	}
	var text func(n *html.Node) string
	text = func(n *html.Node) string {
		if n.Type == html.TextNode {
			return n.Data
		}
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			b.WriteString(text(c))
		}
		return b.String()
	}

	f := form{values: url.Values{}, textareas: map[string]bool{}}
	var collect func(n *html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.ElementNode {
			name, named := attr(n, "name")
			switch {
			case !named:
			case n.Data == "input":
				typ, _ := attr(n, "type")
				_, checked := attr(n, "checked")
				if typ != "checkbox" || checked {
					v, _ := attr(n, "value")
					f.values.Add(name, v)
				}
			case n.Data == "textarea":
				f.values.Add(name, text(n))
				f.textareas[name] = true
			case n.Data == "select":
				var first, selected *html.Node
				for o := n.FirstChild; o != nil; o = o.NextSibling {
					if o.Type != html.ElementNode || o.Data != "option" {
						continue
					}
					if first == nil {
						first = o
					}
					if _, ok := attr(o, "selected"); ok {
						selected = o
					}
				}
				if selected == nil {
					selected = first
				}
				if selected != nil {
					v, _ := attr(selected, "value")
					f.values.Add(name, v)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	var find func(n *html.Node) bool
	find = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "form" {
			if m, _ := attr(n, "method"); strings.EqualFold(m, "post") {
				f.action, _ = attr(n, "action")
				collect(n)
				return true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if find(c) {
				return true
			}
		}
		return false
	}
	if !find(doc) {
		t.Fatalf("page has no POST form:\n%s", page)
	}
	return f
}

var anyNewline = regexp.MustCompile(`\r\n|\r|\n`)

// submit returns the form's values as a browser sends them.
//
// A native submission rewrites every line break as CRLF. The admin's script
// submits through fetch instead, which sends textarea values with LF.
func (f form) submit(native bool) url.Values {
	out := url.Values{}
	for name, vals := range f.values {
		for _, v := range vals {
			switch {
			case native:
				v = anyNewline.ReplaceAllString(v, "\r\n")
			case f.textareas[name]:
				v = anyNewline.ReplaceAllString(v, "\n")
			}
			out.Add(name, v)
		}
	}
	return out
}

var rowLink = regexp.MustCompile(`data-row="([^"]*)"`)

// rowURLs returns the address of every row of a list page, in order.
func rowURLs(page string) []string {
	var out []string
	for _, m := range rowLink.FindAllStringSubmatch(page, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// rowIDs returns the last path element of every row address of a list.
func rowIDs(page string) string {
	var ids []string
	for _, u := range rowURLs(page) {
		ids = append(ids, u[strings.LastIndex(u, "/")+1:])
	}
	return strings.Join(ids, " ")
}

func mustContain(t *testing.T, page string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func mustNotContain(t *testing.T, page string, bans ...string) {
	t.Helper()
	for _, ban := range bans {
		if i := strings.Index(page, ban); i >= 0 {
			from, to := max(0, i-80), min(len(page), i+len(ban)+80)
			t.Errorf("page contains %q: …%s…", ban, page[from:to])
		}
	}
}
