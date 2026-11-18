package schemadmin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestAdmin(t *testing.T, db *stubDB, auth Authorizer, logs *logBuffer, tables map[string]TableConfig) *Admin {
	t.Helper()
	cfg := Config{
		DB:         db.open(t),
		Dialect:    fakeDialect{schema: testSchema()},
		Authorizer: auth,
		BasePath:   "/admin",
		Tables:     tables,
	}
	if logs != nil {
		cfg.Logger = logs.logger()
	}
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func do(a *Admin, method, target string, headers ...string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader("v.1=x&v.3=p&v.5=true")
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, body)
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestNewValidatesConfig(t *testing.T) {
	db := &stubDB{}
	base := func() Config {
		return Config{
			DB:         db.open(t),
			Dialect:    fakeDialect{schema: testSchema()},
			Authorizer: &allow{fn: allowAll},
		}
	}
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"no database", func(c *Config) { c.DB = nil }, "Config.DB is required"},
		{"no dialect", func(c *Config) { c.Dialect = nil }, "Config.Dialect is required"},
		{"no authorizer", func(c *Config) { c.Authorizer = nil }, "Config.Authorizer is required"},
		{"relative base path", func(c *Config) { c.BasePath = "admin" }, "must start with a slash"},
		{"bad trusted origin", func(c *Config) { c.TrustedOrigins = []string{"not an origin"} }, "TrustedOrigins"},
		{"unknown table", func(c *Config) { c.Tables = map[string]TableConfig{"nope": {}} }, `unknown table "nope"`},
		{"unknown column", func(c *Config) {
			c.Tables = map[string]TableConfig{"users": {Columns: map[string]ColumnConfig{"nope": {}}}}
		}, `unknown column "nope"`},
		{"unknown list column", func(c *Config) {
			c.Tables = map[string]TableConfig{"users": {ListColumns: []string{"nope"}}}
		}, `ListColumns names unknown or hidden column "nope"`},
		{"searching a masked column", func(c *Config) {
			c.Tables = map[string]TableConfig{"users": {SearchColumns: []string{"password_hash"}}}
		}, "masked or binary"},
		{"filtering a masked column", func(c *Config) {
			c.Tables = map[string]TableConfig{"users": {FilterColumns: []string{"password_hash"}}}
		}, "masked or binary"},
		{"masked display column", func(c *Config) {
			c.Tables = map[string]TableConfig{"users": {DisplayColumn: "password_hash"}}
		}, "DisplayColumn"},
		{"sorting on a binary column", func(c *Config) {
			c.Tables = map[string]TableConfig{"users": {DefaultSort: Sort{Column: "avatar"}}}
		}, "DefaultSort"},
		{"every column hidden", func(c *Config) {
			c.Tables = map[string]TableConfig{"teams": {Columns: map[string]ColumnConfig{"id": {Hidden: true}, "name": {Hidden: true}}}}
		}, "hides every column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.edit(&cfg)
			_, err := New(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want one containing %q", err, tt.want)
			}
		})
	}
	t.Run("no authorizer is its own error", func(t *testing.T) {
		cfg := base()
		cfg.Authorizer = nil
		if _, err := New(context.Background(), cfg); !errors.Is(err, ErrNoAuthorizer) {
			t.Fatalf("error %v, want ErrNoAuthorizer", err)
		}
	})
	if n := db.calls.Load(); n != 0 {
		t.Errorf("New ran %d statements against a database whose schema the dialect supplies", n)
	}
}

var allRoutes = []struct{ method, path string }{
	{"GET", "/admin/"},
	{"GET", "/admin/static/admin.css"},
	{"GET", "/admin/static/admin.js"},
	{"GET", "/admin/t/users"},
	{"GET", "/admin/t/users/options?col=id"},
	{"GET", "/admin/t/users/new"},
	{"POST", "/admin/t/users/new"},
	{"GET", "/admin/t/users/r/1"},
	{"POST", "/admin/t/users/r/1"},
	{"GET", "/admin/t/users/r/1/delete"},
	{"POST", "/admin/t/users/r/1/delete"},
	{"GET", "/admin/t/memberships/r/1,2"},
	{"GET", "/admin/nothing/here"},
}

// With an Authorizer that refuses, nothing is served and the database is
// never touched: not a page, not an asset, not a table name.
func TestRefusedRequestsGetNothing(t *testing.T) {
	db := &stubDB{}
	a := newTestAdmin(t, db, &allow{fn: func(Action, string) bool { return false }}, nil, nil)
	for _, rt := range allRoutes {
		w := do(a, rt.method, rt.path)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", rt.method, rt.path, w.Code)
		}
		body := w.Body.String()
		for _, leak := range []string{"Users", "users", "Teams", "password"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s %s: refusal mentions %q", rt.method, rt.path, leak)
			}
		}
	}
	if n := db.calls.Load(); n != 0 {
		t.Errorf("refused requests reached the database %d times", n)
	}
}

// A table the request may not view is indistinguishable from one that
// does not exist, and is not listed anywhere.
func TestHiddenTableLooksAbsent(t *testing.T) {
	db := &stubDB{}
	auth := &allow{fn: func(_ Action, table string) bool { return table != "users" }}
	a := newTestAdmin(t, db, auth, nil, nil)

	missing := do(a, "GET", "/admin/t/nonexistent")
	for _, rt := range allRoutes {
		if !strings.Contains(rt.path, "/t/users") {
			continue
		}
		w := do(a, rt.method, rt.path)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 404", rt.method, rt.path, w.Code)
		}
		if w.Body.String() != missing.Body.String() {
			t.Errorf("%s %s: response differs from that for a table that does not exist", rt.method, rt.path)
		}
	}
	if n := db.calls.Load(); n != 0 {
		t.Errorf("requests for a hidden table reached the database %d times", n)
	}
	index := do(a, "GET", "/admin/").Body.String()
	if strings.Contains(index, "Users") || !strings.Contains(index, "Teams") {
		t.Errorf("index should list Teams and not Users:\n%s", index)
	}
}

func TestWritesNeedTheirOwnPermission(t *testing.T) {
	writes := []struct {
		method, path string
		action       Action
	}{
		{"GET", "/admin/t/users/new", ActionCreate},
		{"POST", "/admin/t/users/new", ActionCreate},
		{"POST", "/admin/t/users/r/1", ActionUpdate},
		{"GET", "/admin/t/users/r/1/delete", ActionDelete},
		{"POST", "/admin/t/users/r/1/delete", ActionDelete},
	}
	t.Run("refused by the authorizer", func(t *testing.T) {
		for _, wr := range writes {
			db := &stubDB{}
			auth := &allow{fn: func(action Action, _ string) bool { return action != wr.action }}
			a := newTestAdmin(t, db, auth, nil, nil)
			if w := do(a, wr.method, wr.path); w.Code != http.StatusForbidden {
				t.Errorf("%s %s without %v: status %d, want 403", wr.method, wr.path, wr.action, w.Code)
			}
			if n := db.calls.Load(); n != 0 {
				t.Errorf("%s %s without %v reached the database", wr.method, wr.path, wr.action)
			}
		}
	})
	t.Run("refused on a read-only table", func(t *testing.T) {
		db := &stubDB{}
		a := newTestAdmin(t, db, &allow{fn: allowAll}, nil, map[string]TableConfig{"users": {ReadOnly: true}})
		for _, wr := range writes {
			if w := do(a, wr.method, wr.path); w.Code != http.StatusForbidden {
				t.Errorf("%s %s: status %d, want 403", wr.method, wr.path, w.Code)
			}
		}
		if n := db.calls.Load(); n != 0 {
			t.Errorf("writes to a read-only table reached the database %d times", n)
		}
	})
	t.Run("table hidden by configuration", func(t *testing.T) {
		a := newTestAdmin(t, &stubDB{}, &allow{fn: allowAll}, nil, map[string]TableConfig{"users": {Hidden: true}})
		if w := do(a, "GET", "/admin/t/users"); w.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", w.Code)
		}
	})
}

// State-changing requests from another origin are refused before the
// Authorizer is even asked.
func TestCrossOriginWritesAreRefused(t *testing.T) {
	db := &stubDB{}
	auth := &allow{fn: allowAll}
	a := newTestAdmin(t, db, auth, nil, nil)
	tests := []struct {
		name    string
		headers []string
		want    int
	}{
		{"cross-site fetch metadata", []string{"Sec-Fetch-Site", "cross-site"}, http.StatusForbidden},
		{"same-site is not same-origin", []string{"Sec-Fetch-Site", "same-site"}, http.StatusForbidden},
		{"foreign origin header", []string{"Origin", "https://evil.example"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := auth.calls.Load()
			w := do(a, "POST", "/admin/t/users/r/1/delete", tt.headers...)
			if w.Code != tt.want {
				t.Errorf("status %d, want %d", w.Code, tt.want)
			}
			if auth.calls.Load() != before {
				t.Error("the Authorizer was consulted for a cross-origin request")
			}
		})
	}
	if n := db.calls.Load(); n != 0 {
		t.Errorf("cross-origin requests reached the database %d times", n)
	}
	// The same request from the admin's own origin gets through to the
	// handler (and then fails on the stub database).
	if w := do(a, "POST", "/admin/t/users/r/1/delete", "Sec-Fetch-Site", "same-origin"); w.Code == http.StatusForbidden {
		t.Error("a same-origin request was refused")
	}
	// A trusted origin is let through as well.
	trusted, err := New(context.Background(), Config{
		DB: db.open(t), Dialect: fakeDialect{schema: testSchema()}, Authorizer: auth,
		TrustedOrigins: []string{"https://ops.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := do(trusted, "POST", "/t/users/r/1/delete", "Origin", "https://ops.example", "Sec-Fetch-Site", "cross-site"); w.Code == http.StatusForbidden {
		t.Error("a trusted origin was refused")
	}
}

// When the database fails, the page carries a reference and nothing else;
// the cause is in the log under the same reference.
func TestErrorPagesDoNotLeak(t *testing.T) {
	routes := []struct{ method, path string }{
		{"GET", "/admin/t/users"},
		{"GET", "/admin/t/users?q=x&eq.role=admin&sort=name"},
		{"GET", "/admin/t/users/options?col=id&q=a"},
		{"GET", "/admin/t/users/r/1"},
		{"POST", "/admin/t/users/r/1"},
		{"POST", "/admin/t/users/new"},
		{"GET", "/admin/t/users/r/1/delete"},
		{"POST", "/admin/t/users/r/1/delete"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			db, logs := &stubDB{}, &logBuffer{}
			a := newTestAdmin(t, db, &allow{fn: allowAll}, logs, nil)
			w := do(a, rt.method, rt.path)
			body := w.Body.String()
			if w.Code < 400 {
				t.Fatalf("status %d for a request whose every statement fails", w.Code)
			}
			for _, leak := range []string{"secret_column", "SELECT", "syntax error", "pq:", "goroutine", ".go:"} {
				if strings.Contains(body, leak) {
					t.Errorf("response contains %q:\n%s", leak, body)
				}
			}
			_, after, found := strings.Cut(body, "Reference: ")
			if !found || len(after) < 12 {
				t.Fatalf("response has no reference:\n%s", body)
			}
			ref := after[:12]
			if !logs.contains("ref="+ref) || !logs.contains("secret_column") {
				t.Errorf("log does not record the cause under reference %s:\n%s", ref, logs.String())
			}
		})
	}
}

func TestPanicsBecomeErrorPages(t *testing.T) {
	logs := &logBuffer{}
	panicky := AuthorizerFunc(func(_ *http.Request, _ Action, table string) bool {
		if table == "users" {
			panic("authorizer exploded: token=hunter2")
		}
		return true
	})
	a := newTestAdmin(t, &stubDB{}, panicky, logs, nil)
	w := do(a, "GET", "/admin/t/users")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "hunter2") || strings.Contains(body, "goroutine") {
		t.Errorf("panic details reached the page:\n%s", body)
	}
	if !logs.contains("hunter2") {
		t.Error("panic was not logged")
	}
}

func TestResponseHeaders(t *testing.T) {
	a := newTestAdmin(t, &stubDB{}, &allow{fn: allowAll}, nil, nil)
	w := do(a, "GET", "/admin/")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	h := w.Header()
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "form-action 'self'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe") {
		t.Errorf("Content-Security-Policy allows something unsafe: %s", csp)
	}
	for name, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
		"Cache-Control":          "no-store",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	body := w.Body.String()
	for _, inline := range []string{"<script>", "onclick=", "style=\"", "javascript:"} {
		if strings.Contains(body, inline) {
			t.Errorf("page contains inline %q, which the policy forbids", inline)
		}
	}
}

func TestRouting(t *testing.T) {
	a := newTestAdmin(t, &stubDB{}, &allow{fn: allowAll}, nil, nil)
	tests := []struct {
		method, path string
		want         int
	}{
		{"GET", "/admin", http.StatusPermanentRedirect},
		{"GET", "/admin/", http.StatusOK},
		{"GET", "/administrator", http.StatusNotFound},
		{"GET", "/elsewhere", http.StatusNotFound},
		{"GET", "/admin/static/admin.css", http.StatusOK},
		{"GET", "/admin/static/admin.js", http.StatusOK},
		{"GET", "/admin/static/../admin.go", http.StatusNotFound},
		{"GET", "/admin/static/nope.css", http.StatusNotFound},
		{"GET", "/admin/t/nope", http.StatusNotFound},
		{"GET", "/admin/t/users/r/abc", http.StatusNotFound},         // not a valid integer key
		{"GET", "/admin/t/memberships/r/1", http.StatusNotFound},     // too few key parts
		{"GET", "/admin/t/memberships/r/1,2,3", http.StatusNotFound}, // too many
		{"DELETE", "/admin/t/users/r/1", http.StatusNotFound},        // no such route
		{"POST", "/admin/t/users", http.StatusNotFound},
		{"GET", "/admin/t/users/new", http.StatusOK}, // needs no query
	}
	for _, tt := range tests {
		w := do(a, tt.method, tt.path)
		got := w.Code
		if got >= 300 && got < 400 && strings.Contains(tt.path, "..") {
			continue // the mux cleans the path before anything is looked up
		}
		if got != tt.want {
			t.Errorf("%s %s: status %d, want %d", tt.method, tt.path, got, tt.want)
		}
	}
	// The create form renders from the schema alone and masks credentials.
	form := do(a, "GET", "/admin/t/users/new").Body.String()
	if !strings.Contains(form, `type="password"`) {
		t.Error("the credential column is not a password field")
	}
	if !strings.Contains(form, "Avatar") || !strings.Contains(form, "Hexadecimal bytes") {
		t.Error("the binary column is not a hex field")
	}
	// Asked for by the script, a page comes without its frame.
	fragment := do(a, "GET", "/admin/t/users/new", modalHeader, "1").Body.String()
	if strings.Contains(fragment, "<html") || !strings.Contains(fragment, "<form") {
		t.Errorf("fragment is not just the form:\n%s", fragment)
	}
}
