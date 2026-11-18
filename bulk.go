package schemadmin

import (
	"log/slog"
	"net/http"
	"strconv"
)

const (
	defaultBulkLimit = 100
	maxBulkLimit     = 500
)

// handleBulkDelete deletes every selected row of a table. The form posts
// one "key" value per row, each a URL key as on the list.
func (a *Admin) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm := a.table(w, rq, ActionDelete)
	if tm == nil {
		return
	}
	if len(tm.pk) == 0 {
		a.renderError(w, rq, http.StatusBadRequest, "Not allowed", "Rows of this table cannot be addressed, so they cannot be deleted in bulk.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		a.renderError(w, rq, http.StatusBadRequest, "Bad request", "The submitted form could not be read.")
		return
	}
	raw := r.PostForm["key"]
	if len(raw) == 0 {
		http.Redirect(w, r, a.listURL(tm)+"?m=nochange", http.StatusSeeOther)
		return
	}
	limit := a.bulkLimit
	if len(raw) > limit {
		a.renderError(w, rq, http.StatusBadRequest, "Too many rows", "Select at most "+strconv.Itoa(limit)+" rows at a time.")
		return
	}

	var keys [][]any
	seen := map[string]bool{}
	for _, k := range raw {
		if seen[k] {
			continue
		}
		seen[k] = true
		args, ok := tm.keyArgs(k)
		if !ok {
			a.renderError(w, rq, http.StatusBadRequest, "Bad request", "One of the selected rows could not be addressed.")
			return
		}
		keys = append(keys, args)
	}

	ctx, cancel := rq.query()
	defer cancel()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	query, args := buildDeleteKeys(a.d, tm, keys)
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		_ = tx.Rollback()
		a.renderError(w, rq, http.StatusConflict, "Not deleted", a.writeError(rq, err, true))
		return
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		a.renderError(w, rq, http.StatusConflict, "Not deleted", a.writeError(rq, err, true))
		return
	}
	a.log.InfoContext(r.Context(), "schemadmin: rows deleted",
		slog.String("table", tm.key), slog.Int64("count", n))
	http.Redirect(w, r, a.listURL(tm)+"?m=deleted", http.StatusSeeOther)
}
