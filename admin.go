package schemadmin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// ErrNoAuthorizer is returned by New when Config.Authorizer is nil. The
// admin never runs without one.
var ErrNoAuthorizer = errors.New("schemadmin: Config.Authorizer is required")

const (
	defaultPageSize     = 50
	defaultQueryTimeout = 10 * time.Second
	defaultCountTimeout = 2 * time.Second
	maxFormBytes        = 8 << 20
	slowCountPause      = 5 * time.Minute
)

// Admin is the admin panel. It is an http.Handler and is safe for
// concurrent use. The schema is read once, in New; restart the process to
// pick up schema changes.
type Admin struct {
	db     *sql.DB
	d      Dialect
	auth   Authorizer
	base   string
	title  string
	log    *slog.Logger
	schema *Schema

	tables []*tableMeta
	bySeg  map[string]*tableMeta

	queryTimeout time.Duration
	countTimeout time.Duration

	csrf  *http.CrossOriginProtection
	mux   *http.ServeMux
	pages *pageSet

	// slowCount remembers, per table, until when counting is skipped
	// because it recently ran out of time.
	slowCount sync.Map
}

// New introspects the database and returns the admin.
func New(ctx context.Context, cfg Config) (*Admin, error) {
	switch {
	case cfg.DB == nil:
		return nil, errors.New("schemadmin: Config.DB is required")
	case cfg.Dialect == nil:
		return nil, errors.New("schemadmin: Config.Dialect is required")
	case cfg.Authorizer == nil:
		return nil, ErrNoAuthorizer
	}
	base := strings.TrimRight(cfg.BasePath, "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		return nil, fmt.Errorf("schemadmin: Config.BasePath %q must start with a slash", cfg.BasePath)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Title == "" {
		cfg.Title = "Admin"
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = defaultPageSize
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = defaultQueryTimeout
	}
	if cfg.CountTimeout <= 0 {
		cfg.CountTimeout = defaultCountTimeout
	}

	ictx, cancel := context.WithTimeout(ctx, cfg.QueryTimeout)
	defer cancel()
	schema, err := cfg.Dialect.Introspect(ictx, cfg.DB, IntrospectOptions{Schemas: cfg.Schemas, Logger: cfg.Logger})
	if err != nil {
		return nil, fmt.Errorf("schemadmin: introspect: %w", err)
	}
	if err := schema.Validate(); err != nil {
		return nil, fmt.Errorf("schemadmin: introspected schema is invalid: %w", err)
	}
	tables, byKey, err := buildMeta(schema, &cfg, cfg.Logger)
	if err != nil {
		return nil, err
	}

	csrf := http.NewCrossOriginProtection()
	for _, origin := range cfg.TrustedOrigins {
		if err := csrf.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("schemadmin: Config.TrustedOrigins: %w", err)
		}
	}
	pages, err := loadPages()
	if err != nil {
		return nil, err
	}

	a := &Admin{
		db:           cfg.DB,
		d:            cfg.Dialect,
		auth:         cfg.Authorizer,
		base:         base,
		title:        cfg.Title,
		log:          cfg.Logger,
		schema:       schema,
		tables:       tables,
		bySeg:        map[string]*tableMeta{},
		queryTimeout: cfg.QueryTimeout,
		countTimeout: cfg.CountTimeout,
		csrf:         csrf,
		pages:        pages,
	}
	for _, tm := range byKey {
		a.bySeg[tm.seg] = tm
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.handleIndex)
	mux.HandleFunc("GET /static/{file}", a.handleStatic)
	mux.HandleFunc("GET /t/{table}", a.handleList)
	mux.HandleFunc("GET /t/{table}/options", a.handleOptions)
	mux.HandleFunc("GET /t/{table}/new", a.handleNew)
	mux.HandleFunc("POST /t/{table}/new", a.handleCreate)
	mux.HandleFunc("GET /t/{table}/r/{key}", a.handleEdit)
	mux.HandleFunc("POST /t/{table}/r/{key}", a.handleUpdate)
	mux.HandleFunc("GET /t/{table}/r/{key}/delete", a.handleConfirmDelete)
	mux.HandleFunc("POST /t/{table}/r/{key}/delete", a.handleDelete)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.notFound(w, a.request(r))
	})
	a.mux = mux
	return a, nil
}

// Schema returns the schema the admin read at startup. It must not be
// modified.
func (a *Admin) Schema() *Schema { return a.schema }

// ServeHTTP serves the admin. Every request is checked in this order: it
// must be same-origin if it can change anything, and the Authorizer must
// allow it to see the admin at all. Only then is it routed.
func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rq := &request{a: a, answers: map[authKey]bool{}}
	r = r.WithContext(context.WithValue(r.Context(), requestKey{}, rq))
	rq.r, rq.orig = r, r
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			// No navigation on this page: building it asks the
			// Authorizer, which may be what panicked.
			ref := a.logError(rq, fmt.Errorf("panic: %v\n%s", p, debug.Stack()))
			a.plainError(w, http.StatusInternalServerError, "Error", "Something went wrong on our side. Reference: "+ref)
		}
	}()

	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")

	if a.base != "" {
		if r.URL.Path == a.base {
			http.Redirect(w, r, a.base+"/", http.StatusPermanentRedirect)
			return
		}
		if !strings.HasPrefix(r.URL.Path, a.base+"/") {
			a.plainError(w, http.StatusNotFound, "Not found", "There is nothing at this address.")
			return
		}
	}
	if err := a.csrf.Check(r); err != nil {
		a.log.WarnContext(r.Context(), "schemadmin: cross-origin request refused", slog.String("path", r.URL.Path))
		a.plainError(w, http.StatusForbidden, "Request refused", "This request came from another site and was not carried out.")
		return
	}
	if !a.auth.Authorize(r, ActionView, "") {
		a.plainError(w, http.StatusForbidden, "Not allowed", "You are not allowed to use this admin.")
		return
	}
	http.StripPrefix(a.base, a.mux).ServeHTTP(w, r)
}

// request carries per-request state: the request and what the Authorizer
// has answered so far, so it is asked once per table and action.
type request struct {
	a       *Admin
	r       *http.Request // as routed: path values set, BasePath stripped
	orig    *http.Request // as received; this is what the Authorizer sees
	answers map[authKey]bool
}

type authKey struct {
	action Action
	table  *tableMeta
}

type requestKey struct{}

// request returns the state ServeHTTP attached to r.
func (a *Admin) request(r *http.Request) *request {
	rq, _ := r.Context().Value(requestKey{}).(*request)
	if rq == nil {
		rq = &request{a: a, orig: r, answers: map[authKey]bool{}}
	}
	rq.r = r
	return rq
}

// can asks the Authorizer. A read-only table refuses every write whatever
// the Authorizer says.
func (rq *request) can(action Action, tm *tableMeta) bool {
	if action != ActionView && (tm.readOnly || !rq.can(ActionView, tm)) {
		return false
	}
	k := authKey{action, tm}
	if ok, done := rq.answers[k]; done {
		return ok
	}
	ok := rq.a.auth.Authorize(rq.orig, action, tm.key)
	rq.answers[k] = ok
	return ok
}

// query returns a context limited by the query timeout.
func (rq *request) query() (context.Context, context.CancelFunc) {
	return context.WithTimeout(rq.r.Context(), rq.a.queryTimeout)
}

// table resolves the {table} path value for a request that wants to
// perform action. It writes the response and returns nil when the table
// does not exist, may not be seen, or may not be acted on. A table the
// user may not view looks exactly like one that does not exist.
func (a *Admin) table(w http.ResponseWriter, rq *request, action Action) *tableMeta {
	tm := a.bySeg[rq.r.PathValue("table")]
	if tm == nil || !rq.can(ActionView, tm) {
		a.notFound(w, rq)
		return nil
	}
	if action != ActionView && !rq.can(action, tm) {
		a.renderError(w, rq, http.StatusForbidden, "Not allowed", "You are not allowed to "+action.String()+" rows in this table.")
		return nil
	}
	return tm
}

// URLs. Every path the admin links to is built here.

func (a *Admin) indexURL() string { return a.base + "/" }

func (a *Admin) staticURL(file string) string {
	return a.base + "/static/" + file + "?v=" + a.pages.version
}

func (a *Admin) listURL(tm *tableMeta) string {
	return a.base + "/t/" + url.PathEscape(tm.seg)
}

func (a *Admin) newURL(tm *tableMeta) string { return a.listURL(tm) + "/new" }

func (a *Admin) rowURL(tm *tableMeta, key string) string {
	return a.listURL(tm) + "/r/" + url.PathEscape(key)
}

func (a *Admin) deleteURL(tm *tableMeta, key string) string {
	return a.rowURL(tm, key) + "/delete"
}

func (a *Admin) optionsURL(tm *tableMeta, col *colMeta) string {
	return a.listURL(tm) + "/options?col=" + url.QueryEscape(col.c.Name)
}

// Errors.

func (a *Admin) notFound(w http.ResponseWriter, rq *request) {
	a.renderError(w, rq, http.StatusNotFound, "Not found", "There is nothing at this address.")
}

// fail reports an unexpected error. The cause goes to the log under a
// reference; the response carries only the reference.
func (a *Admin) fail(w http.ResponseWriter, rq *request, err error) {
	status, msg := http.StatusInternalServerError, "Something went wrong on our side."
	if errors.Is(err, context.DeadlineExceeded) {
		status, msg = http.StatusGatewayTimeout, "The database took too long to answer."
	}
	if errors.Is(err, context.Canceled) && rq.r.Context().Err() != nil {
		return // the client went away
	}
	ref := a.logError(rq, err)
	a.renderError(w, rq, status, "Error", msg+" Reference: "+ref)
}

// logError logs err and returns the reference it was logged under.
func (a *Admin) logError(rq *request, err error) string {
	ref := newReference()
	a.log.ErrorContext(rq.r.Context(), "schemadmin: request failed",
		slog.String("ref", ref),
		slog.String("method", rq.r.Method),
		slog.String("path", rq.r.URL.Path),
		slog.Any("error", err))
	return ref
}

func newReference() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

// constraintMessage explains a constraint violation without quoting the
// database.
func constraintMessage(kind ConstraintKind, deleting bool) string {
	switch kind {
	case ConstraintUnique:
		return "Another row already has these values, and they must be unique."
	case ConstraintForeignKey:
		if deleting {
			return "Other rows still refer to this row."
		}
		return "A value refers to a row that does not exist."
	case ConstraintNotNull:
		return "A required value is missing."
	case ConstraintCheck:
		return "The values break a rule defined in the database."
	}
	return ""
}

// writeError turns a failed write into a message for the form. Constraint
// violations are explained; anything else is logged and referenced.
func (a *Admin) writeError(rq *request, err error, deleting bool) string {
	if msg := constraintMessage(a.d.ClassifyError(err), deleting); msg != "" {
		return msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The database took too long to answer. Nothing was changed. Reference: " + a.logError(rq, err)
	}
	return "The database did not accept this. Nothing was changed. Reference: " + a.logError(rq, err)
}
