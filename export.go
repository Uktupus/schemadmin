package schemadmin

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultExportMax = 10_000
	maxExportMax     = 100_000
)

// handleExport streams the current list filter as CSV. Masked columns are
// included as a header but every cell is empty. Binary columns are hex.
func (a *Admin) handleExport(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm := a.table(w, rq, ActionView)
	if tm == nil {
		return
	}
	st := parseListState(tm, r.URL.Query())
	q, _ := st.query(tm)
	q.limit = a.exportMax
	q.offset = 0

	ctx, cancel := rq.query()
	defer cancel()
	// Fetch one extra row so we can tell the user when the export was capped.
	page := *q
	page.limit = q.limit + 1
	rows, err := func() ([][]value, error) {
		query, args := buildList(a.d, tm, &page)
		return readRows(ctx, a.db, tm.cols, query, args)
	}()
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	capped := len(rows) > q.limit
	if capped {
		rows = rows[:q.limit]
	}

	name := strings.ReplaceAll(tm.key, ".", "_") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if capped {
		w.Header().Set("X-Schemadmin-Export-Capped", strconv.Itoa(q.limit))
	}
	cw := csv.NewWriter(w)
	header := make([]string, len(tm.cols))
	for i, c := range tm.cols {
		header[i] = c.c.Name
	}
	if err := cw.Write(header); err != nil {
		return
	}
	for _, row := range rows {
		rec := make([]string, len(tm.cols))
		for i, c := range tm.cols {
			v := row[c.idx]
			switch {
			case c.masked:
				rec[i] = ""
			case v.Null:
				rec[i] = ""
			default:
				rec[i] = v.S
			}
		}
		if err := cw.Write(rec); err != nil {
			return
		}
	}
	cw.Flush()
}
