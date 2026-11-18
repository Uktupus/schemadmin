package schemadmin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
)

// Reload re-reads the database schema and replaces the admin's table
// metadata. In-flight requests keep the previous view until they finish.
// Config that names unknown tables or columns still fails the reload.
func (a *Admin) Reload(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reloadLocked(ctx)
}

func (a *Admin) reloadLocked(ctx context.Context) error {
	ictx, cancel := context.WithTimeout(ctx, a.queryTimeout)
	defer cancel()
	schema, err := a.d.Introspect(ictx, a.db, IntrospectOptions{Schemas: a.schemas, Logger: a.log})
	if err != nil {
		return fmt.Errorf("schemadmin: introspect: %w", err)
	}
	if err := schema.Validate(); err != nil {
		return fmt.Errorf("schemadmin: introspected schema is invalid: %w", err)
	}
	tables, byKey, err := buildMeta(schema, a.cfg, a.log)
	if err != nil {
		return err
	}
	bySeg := map[string]*tableMeta{}
	for _, tm := range byKey {
		bySeg[tm.seg] = tm
	}
	a.schema = schema
	a.tables = tables
	a.bySeg = bySeg
	a.log.InfoContext(ctx, "schemadmin: schema reloaded", slog.Int("tables", len(tables)))
	return nil
}

// handleReload re-reads the schema when Config.AllowReload is set. The
// Authorizer must allow ActionView on the empty table name, which every
// request already needs to use the admin.
func (a *Admin) handleReload(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	if !a.allowReload {
		a.notFound(w, rq)
		return
	}
	a.mu.Lock()
	err := a.reloadLocked(r.Context())
	a.mu.Unlock()
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	http.Redirect(w, r, a.indexURL()+"?m=reloaded", http.StatusSeeOther)
}
