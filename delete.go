package schemadmin

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
)

type deleteBody struct {
	TableLabel string
	ListURL    string
	Heading    string
	Error      string
	Action     string
	CancelURL  string
	Summary    []summaryItem
	Dependents []dependent
	Hidden     bool   // tables the user may not view refer to this table
	Unenforced bool   // the database does not enforce foreign keys here
	Blocked    bool   // the database will refuse while dependents exist
	Unknown    string // dependents that could not be counted in time
}

type summaryItem struct {
	Label string
	Value cell
}

// dependent is a group of rows in another table that refer to the row.
type dependent struct {
	Table  string
	Count  string
	URL    string
	Effect string
}

// effect says what the database does to dependent rows on delete.
func effect(action FKAction, enforced bool) (text string, blocks bool) {
	if !enforced {
		return "will be left referring to a row that no longer exists", false
	}
	switch action {
	case FKCascade:
		return "will be deleted too", false
	case FKSetNull:
		return "will have this reference set to NULL", false
	case FKSetDefault:
		return "will have this reference reset to its default", false
	}
	return "prevent this row from being deleted", true
}

// dependents finds the rows that refer to row, including those that would
// be removed by a cascade of cascades. It reads other tables, so each is
// subject to ActionView: for a table the request may not view, nothing is
// revealed beyond the fact that something refers to the row.
func (a *Admin) dependents(rq *request, tm *tableMeta, row []value, body *deleteBody) {
	enforced := true
	if checker, ok := a.d.(ForeignKeyChecker); ok && len(tm.refs) > 0 {
		ctx, cancel := rq.query()
		on, err := checker.ForeignKeysEnforced(ctx, a.db)
		cancel()
		if err == nil && !on {
			enforced = false
		}
	}
	type hop struct {
		tm    *tableMeta
		row   []value
		via   string // how we reached this row; empty for the root
		depth int
	}
	seen := map[string]bool{tm.key + "\x00" + encodeKeyFromRow(tm, row): true}
	queue := []hop{{tm: tm, row: row}}
	unknown := false
	// Aggregates per target table: direct dependents keep their effect;
	// transitive ones reached only through CASCADE are counted separately.
	type agg struct {
		ref      reference
		count    int64
		link     url.Values
		linkable bool
		direct   bool
	}
	byTable := map[*tableMeta]*agg{}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, ref := range cur.tm.refs {
			if !rq.can(ActionView, ref.from) {
				body.Hidden = true
				continue
			}
			args := make([]any, len(ref.toCols))
			link := url.Values{}
			usable, linkable, null := true, true, false
			for i, to := range ref.toCols {
				if to == nil || to.masked || to.c.Kind == KindBytes {
					usable = false
					break
				}
				v := cur.row[to.idx]
				if v.Null {
					null = true
					break
				}
				arg, msg := parseInput(ref.fromCols[i], nil, v.S)
				if msg != "" {
					usable = false
					break
				}
				args[i] = arg
				if from := ref.from.byName[ref.fromCols[i].Name]; from != nil && from.comparable() {
					link.Set("eq."+from.c.Name, v.S)
				} else {
					linkable = false
				}
			}
			if null {
				continue
			}
			if !usable {
				unknown = true
				continue
			}
			n, counted := a.countWhere(rq.r.Context(), ref.from.t, ref.fromCols, args)
			if !counted {
				unknown = true
				continue
			}
			if n == 0 {
				continue
			}
			direct := cur.depth == 0
			if a := byTable[ref.from]; a != nil {
				if direct {
					a.direct = true
					a.ref = *ref
					a.link, a.linkable = link, linkable
				}
				if direct {
					a.count = n // prefer the direct count for the listed table
				}
			} else {
				byTable[ref.from] = &agg{ref: *ref, count: n, link: link, linkable: linkable, direct: direct}
			}
			text, blocks := effect(ref.fk.OnDelete, enforced)
			if blocks && direct {
				body.Blocked = true
			}
			if !enforced {
				body.Unenforced = true
			}
			_ = text
			// Walk into cascaded rows so the confirmation can name tables
			// that disappear only because of a cascade chain.
			if enforced && ref.fk.OnDelete == FKCascade && cur.depth < 3 && n <= 20 && len(ref.from.pk) > 0 {
				q := &listQuery{limit: int(n), filters: nil}
				for i, c := range ref.fromCols {
					cm := ref.from.byName[c.Name]
					if cm == nil {
						continue
					}
					q.filters = append(q.filters, filter{col: cm, op: opEq, arg: args[i]})
				}
				children, _, err := a.list(rq.r.Context(), ref.from, q)
				if err != nil {
					unknown = true
					continue
				}
				for _, child := range children {
					id := ref.from.key + "\x00" + encodeKeyFromRow(ref.from, child)
					if seen[id] {
						continue
					}
					seen[id] = true
					queue = append(queue, hop{tm: ref.from, row: child, depth: cur.depth + 1})
				}
			}
		}
	}

	for from, g := range byTable {
		text, blocks := effect(g.ref.fk.OnDelete, enforced)
		if !g.direct {
			text = "will be deleted by a cascade from another table"
			blocks = false
		}
		if blocks {
			body.Blocked = true
		}
		d := dependent{Table: from.label, Count: groupDigits(g.count), Effect: text}
		if g.direct && g.linkable {
			d.URL = withQuery(a.listURL(from), g.link)
		}
		body.Dependents = append(body.Dependents, d)
	}
	if unknown {
		body.Unknown = "Some tables could not be checked for rows that refer to this one."
	}
}

func encodeKeyFromRow(tm *tableMeta, row []value) string {
	if k, ok := tm.rowKey(row); ok {
		return k
	}
	return ""
}

// deleteTarget resolves the table and row of a delete request.
func (a *Admin) deleteTarget(w http.ResponseWriter, rq *request) (*tableMeta, []any, bool) {
	tm := a.table(w, rq, ActionDelete)
	if tm == nil {
		return nil, nil, false
	}
	key, ok := tm.keyArgs(rq.r.PathValue("key"))
	if !ok {
		a.notFound(w, rq)
		return nil, nil, false
	}
	return tm, key, true
}

func (a *Admin) renderDelete(w http.ResponseWriter, rq *request, status int, tm *tableMeta, row []value, message string) {
	key := rq.r.PathValue("key")
	body := deleteBody{
		TableLabel: tm.label,
		ListURL:    a.listURL(tm),
		Heading:    "Delete " + rowTitle(tm, row) + "?",
		Error:      message,
		Action:     a.deleteURL(tm, key),
		CancelURL:  a.rowURL(tm, key),
	}
	for _, c := range tm.cols {
		body.Summary = append(body.Summary, summaryItem{Label: c.label, Value: showValue(c, row[c.idx], 120)})
	}
	a.dependents(rq, tm, row, &body)
	a.render(w, rq, status, "delete", "Delete", tm, body)
}

func (a *Admin) handleConfirmDelete(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm, key, ok := a.deleteTarget(w, rq)
	if !ok {
		return
	}
	ctx, cancel := rq.query()
	defer cancel()
	row, found, err := a.get(ctx, a.db, tm, key)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	if !found {
		a.notFound(w, rq)
		return
	}
	a.renderDelete(w, rq, http.StatusOK, tm, row, "")
}

func (a *Admin) handleDelete(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm, key, ok := a.deleteTarget(w, rq)
	if !ok {
		return
	}
	ctx, cancel := rq.query()
	defer cancel()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	row, found, err := a.get(ctx, tx, tm, key)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	if !found {
		_ = tx.Rollback()
		a.notFound(w, rq)
		return
	}
	query, args := buildDelete(a.d, tm, key)
	err = execOne(ctx, tx, query, args)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		msg := "The row could not be singled out, so nothing was deleted."
		if !errors.Is(err, errRowCount) {
			msg = a.writeError(rq, err, true)
		}
		a.renderDelete(w, rq, http.StatusConflict, tm, row, msg)
		return
	}
	a.log.InfoContext(r.Context(), "schemadmin: row deleted",
		slog.String("table", tm.key), slog.String("key", r.PathValue("key")))
	http.Redirect(w, r, a.listURL(tm)+"?m=deleted", http.StatusSeeOther)
}
