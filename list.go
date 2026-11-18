package schemadmin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// listState is everything about a list page that lives in its URL:
//
//	q            search text
//	sort, dir    column to order by, "desc" for descending
//	page         page number, from 1
//	eq.COL       the column equals this value
//	has.COL      the column, as text, contains this
//	from.COL     lower bound, inclusive
//	to.COL       upper bound, inclusive
//	is.COL       "null", "notnull" or "empty"
type listState struct {
	q       string
	sort    *colMeta
	desc    bool
	page    int
	filters []stateFilter
}

type stateFilter struct {
	param string // "eq", "has", "from", "to" or "is"
	col   *colMeta
	raw   string
}

func (f stateFilter) name() string { return f.param + "." + f.col.c.Name }

var filterParams = []string{"eq", "has", "from", "to", "is"}

// accepts reports whether a filter parameter applies to a column. This is
// about what the database can evaluate, not about what the header offers:
// links between tables filter on columns whether or not they have a menu.
func accepts(param string, c *colMeta, raw string) bool {
	if !c.readable() {
		return false
	}
	switch param {
	case "eq":
		return c.comparable()
	case "has":
		return true
	case "from", "to":
		switch c.c.Kind {
		case KindInt, KindFloat, KindDecimal, KindDate, KindTime, KindDateTime, KindDateTimeTZ:
			return true
		}
	case "is":
		switch raw {
		case "null", "notnull":
			return true
		case "empty":
			return c.c.Kind == KindString || c.c.Kind == KindText
		}
	}
	return false
}

const maxPage = 1_000_000

func parseListState(tm *tableMeta, v url.Values) *listState {
	st := &listState{q: strings.TrimSpace(v.Get("q")), page: 1}
	if !tm.searchable() {
		st.q = ""
	}
	if c := tm.byName[v.Get("sort")]; c != nil && c.comparable() {
		st.sort, st.desc = c, v.Get("dir") == "desc"
	}
	if n, err := strconv.Atoi(v.Get("page")); err == nil && n > 1 && n <= maxPage {
		st.page = n
	}
	for _, c := range tm.cols {
		for _, param := range filterParams {
			raw := v.Get(param + "." + c.c.Name)
			if param != "has" {
				raw = strings.TrimSpace(raw)
			}
			if raw != "" && accepts(param, c, raw) {
				st.filters = append(st.filters, stateFilter{param: param, col: c, raw: raw})
			}
		}
	}
	return st
}

// values is the query string of the state. Page 1 and defaults are left
// out so that URLs stay short.
func (st *listState) values() url.Values {
	v := url.Values{}
	if st.q != "" {
		v.Set("q", st.q)
	}
	if st.sort != nil {
		v.Set("sort", st.sort.c.Name)
		if st.desc {
			v.Set("dir", "desc")
		}
	}
	for _, f := range st.filters {
		v.Set(f.name(), f.raw)
	}
	if st.page > 1 {
		v.Set("page", strconv.Itoa(st.page))
	}
	return v
}

// with returns a copy of the state after change, as a URL under path.
func (st *listState) with(path string, change func(*listState)) string {
	c := *st
	c.filters = append([]stateFilter(nil), st.filters...)
	change(&c)
	return withQuery(path, c.values())
}

// without drops every filter on col.
func (st *listState) without(col *colMeta) {
	var out []stateFilter
	for _, f := range st.filters {
		if f.col != col {
			out = append(out, f)
		}
	}
	st.filters = out
}

func (st *listState) filter(param string, col *colMeta) string {
	for _, f := range st.filters {
		if f.param == param && f.col == col {
			return f.raw
		}
	}
	return ""
}

func withQuery(path string, v url.Values) string {
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

// query turns the state into a listQuery. Filters whose value is not
// valid for their column are dropped from the state and reported.
func (st *listState) query(tm *tableMeta) (*listQuery, []string) {
	q := &listQuery{search: st.q, searchCols: tm.searchCols, limit: tm.pageSize, offset: (st.page - 1) * tm.pageSize}
	var warnings []string

	// A search for "42" should find row 42.
	if pk := tm.searchKey(); pk != nil && st.q != "" {
		if arg, msg := parseInput(pk.c, nil, st.q); msg == "" {
			q.exact = append(q.exact, filter{col: pk, op: opEq, arg: arg})
		}
	}

	kept := st.filters[:0]
	for _, f := range st.filters {
		out := filter{col: f.col}
		var msg string
		switch f.param {
		case "eq":
			out.op = opEq
			out.arg, msg = parseInput(f.col.c, nil, f.raw)
		case "has":
			out.op, out.arg = opContains, f.raw
		case "from":
			out.op = opGTE
			out.arg, msg = parseInput(f.col.c, nil, f.raw)
		case "to":
			raw := f.raw
			// A day given as the upper bound of a timestamp includes
			// the whole day.
			if (f.col.c.Kind == KindDateTime || f.col.c.Kind == KindDateTimeTZ) && isDate(raw) {
				raw += " 23:59:59.999999"
			}
			out.op = opLTE
			out.arg, msg = parseInput(f.col.c, nil, raw)
		case "is":
			switch f.raw {
			case "null":
				out.op = opIsNull
			case "notnull":
				out.op = opNotNull
			default:
				out.op, out.arg = opEq, ""
			}
		}
		if msg != "" {
			warnings = append(warnings, "The filter on "+f.col.label+" was ignored: its value is not valid for that column.")
			continue
		}
		kept = append(kept, f)
		q.filters = append(q.filters, out)
	}
	st.filters = kept

	// Order by the chosen column, then by the primary key so that pages
	// do not overlap.
	sortCol, desc := st.sort, st.desc
	if sortCol == nil {
		sortCol, desc = tm.sort, tm.sortDesc
	}
	if sortCol != nil {
		q.sort, q.desc = append(q.sort, sortCol), desc
	}
	for _, c := range tm.pk {
		if c != sortCol && c.comparable() {
			q.sort = append(q.sort, c)
		}
	}
	if len(q.sort) == 0 {
		for _, c := range tm.cols {
			if c.comparable() {
				q.sort = append(q.sort, c)
				break
			}
		}
	}
	return q, warnings
}

type listBody struct {
	Label    string
	Name     string
	Notice   string
	Warnings []string
	NewURL   string

	Action     string
	Keep       []hiddenField
	Searchable bool
	Search     string
	Chips      []chip
	ClearURL   string

	Columns []headCell
	Rows    []listRow
	Span    int

	Range   string
	PrevURL string
	NextURL string
}

type hiddenField struct{ Name, Value string }

type chip struct {
	Label     string
	RemoveURL string
}

type listRow struct {
	URL   string // the row's own page; empty when the row cannot be addressed
	Cells []cell
}

type headCell struct {
	Label    string
	Num      bool
	Sort     string // "ascending" or "descending" when this column orders the list
	Filtered bool
	Menu     *colMenu
}

// colMenu is the menu behind a column header: ordering and filtering.
type colMenu struct {
	ID       string
	Label    string
	AscURL   string
	DescURL  string
	Asc      bool
	Desc     bool
	Action   string
	Keep     []hiddenField // the rest of the state, for the menu's form
	Choices  []menuLink
	HasName  string
	Has      string
	EqName   string
	Eq       string
	FromName string
	From     string
	ToName   string
	To       string
	Dates    bool // the bounds are dates
	Links    []menuLink
	ClearURL string
}

func (m *colMenu) HasForm() bool { return m.HasName != "" || m.EqName != "" || m.FromName != "" }

type menuLink struct {
	Label   string
	URL     string
	Current bool
}

var notices = map[string]string{
	"created":  "Row created.",
	"saved":    "Changes saved.",
	"deleted":  "Row deleted.",
	"nochange": "Nothing was changed.",
}

// columnMenu builds the header menu of a column, or nil when there is
// nothing to offer.
func columnMenu(path string, st *listState, c *colMeta, sorted *colMeta, desc bool) *colMenu {
	m := &colMenu{ID: "m" + strconv.Itoa(c.idx), Label: c.label, Action: path}
	if c.comparable() {
		m.Asc, m.Desc = sorted == c && !desc, sorted == c && desc
		m.AscURL = st.with(path, func(s *listState) { s.sort, s.desc, s.page = c, false, 1 })
		m.DescURL = st.with(path, func(s *listState) { s.sort, s.desc, s.page = c, true, 1 })
	}
	if c.filter {
		set := func(param, raw string) string {
			return st.with(path, func(s *listState) {
				s.without(c)
				s.filters = append(s.filters, stateFilter{param: param, col: c, raw: raw})
				s.page = 1
			})
		}
		name := c.c.Name
		eq, is := st.filter("eq", c), st.filter("is", c)
		switch {
		case len(c.choices) > 0 || c.c.Kind == KindBool:
			vals := c.choices
			if len(vals) == 0 {
				vals = []string{"true", "false"}
			}
			for _, v := range vals {
				if v != "" {
					m.Choices = append(m.Choices, menuLink{Label: v, URL: set("eq", v), Current: eq == v})
				}
			}
		case c.ranged():
			m.FromName, m.From = "from."+name, st.filter("from", c)
			m.ToName, m.To = "to."+name, st.filter("to", c)
			switch c.c.Kind {
			case KindDate, KindDateTime, KindDateTimeTZ:
				m.Dates = (m.From == "" || isDate(m.From)) && (m.To == "" || isDate(m.To))
			}
		case c.matchable():
			m.HasName, m.Has = "has."+name, st.filter("has", c)
		case c.comparable():
			m.EqName, m.Eq = "eq."+name, eq
		}
		if c.c.Nullable {
			m.Links = append(m.Links,
				menuLink{Label: "Only NULL", URL: set("is", "null"), Current: is == "null"},
				menuLink{Label: "Not NULL", URL: set("is", "notnull"), Current: is == "notnull"})
		}
		if accepts("is", c, "empty") && len(c.choices) == 0 {
			m.Links = append(m.Links, menuLink{Label: "Only empty", URL: set("is", "empty"), Current: is == "empty"})
		}
		// The menu's form replaces every filter on its column and keeps
		// everything else.
		rest := *st
		rest.filters = append([]stateFilter(nil), st.filters...)
		rest.without(c)
		rest.page = 1
		for key, vals := range rest.values() {
			m.Keep = append(m.Keep, hiddenField{key, vals[0]})
		}
		sort.Slice(m.Keep, func(i, j int) bool { return m.Keep[i].Name < m.Keep[j].Name })
		for _, f := range st.filters {
			if f.col == c {
				m.ClearURL = withQuery(path, rest.values())
				break
			}
		}
	}
	if m.AscURL == "" && len(m.Choices) == 0 && !m.HasForm() && len(m.Links) == 0 {
		return nil
	}
	return m
}

func (a *Admin) handleList(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm := a.table(w, rq, ActionView)
	if tm == nil {
		return
	}
	params := r.URL.Query()
	st := parseListState(tm, params)
	q, warnings := st.query(tm)

	rows, more, err := a.list(r.Context(), tm, q)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	total, counted := int64(0), false
	switch {
	case !more && st.page == 1:
		total, counted = int64(len(rows)), true // the page is the whole result
	case !more && len(rows) > 0:
		total, counted = int64(q.offset+len(rows)), true
	default:
		total, counted = a.count(r.Context(), tm, q)
	}

	path := a.listURL(tm)
	body := listBody{
		Label:      tm.label,
		Name:       tm.key,
		Notice:     notices[params.Get("m")],
		Warnings:   warnings,
		Action:     path,
		Searchable: tm.searchable(),
		Search:     st.q,
		Span:       len(tm.listCols),
	}
	if rq.can(ActionCreate, tm) {
		body.NewURL = a.newURL(tm)
	}

	// The search form keeps the ordering and the filters.
	keep := *st
	keep.q, keep.page = "", 1
	for key, vals := range keep.values() {
		body.Keep = append(body.Keep, hiddenField{key, vals[0]})
	}
	sort.Slice(body.Keep, func(i, j int) bool { return body.Keep[i].Name < body.Keep[j].Name })

	filtered := map[*colMeta]bool{}
	for _, f := range st.filters {
		f := f
		filtered[f.col] = true
		body.Chips = append(body.Chips, chip{
			Label: chipLabel(f),
			RemoveURL: st.with(path, func(c *listState) {
				c.page = 1
				c.filters = removeFilter(c.filters, f.name())
			}),
		})
	}
	if st.q != "" || len(st.filters) > 0 {
		body.ClearURL = st.with(path, func(c *listState) { c.q, c.filters, c.page = "", nil, 1 })
	}

	// What the list is actually ordered by, for the header arrows.
	var sorted *colMeta
	if len(q.sort) > 0 && (st.sort != nil || tm.sort != nil) {
		sorted = q.sort[0]
	}
	for _, c := range tm.listCols {
		h := headCell{Label: c.label, Num: isNumeric(c), Filtered: filtered[c], Menu: columnMenu(path, st, c, sorted, q.desc)}
		if sorted == c {
			h.Sort = "ascending"
			if q.desc {
				h.Sort = "descending"
			}
		}
		body.Columns = append(body.Columns, h)
	}

	fkLabels := a.listLabels(rq, tm, rows)
	for _, row := range rows {
		lr := listRow{Cells: make([]cell, len(tm.listCols))}
		if key, ok := tm.rowKey(row); ok {
			lr.URL = a.rowURL(tm, key)
		}
		for i, c := range tm.listCols {
			v := row[c.idx]
			cl := showValue(c, v, 80)
			if !v.Null && v.S != "" && (len(c.choices) > 0 || c.c.Kind == KindBool) && c.readable() {
				cl.State = "tag"
			}
			if c.fk != nil && !v.Null && rq.can(ActionView, c.fk.target) {
				cl.URL, cl.Modal = a.refURL(c.fk, v.S)
				cl.Extra = truncate(fkLabels[c][v.S], 60)
			}
			if i == 0 && lr.URL != "" {
				cl.URL, cl.Modal = lr.URL, true
			}
			lr.Cells[i] = cl
		}
		body.Rows = append(body.Rows, lr)
	}

	first, last := q.offset+1, q.offset+len(rows)
	switch {
	case len(rows) == 0 && st.page == 1:
		body.Range = "No rows"
	case len(rows) == 0:
		body.Range = "No rows on this page"
	case counted:
		body.Range = groupDigits(int64(first)) + "–" + groupDigits(int64(last)) + " of " + groupDigits(total)
	default:
		body.Range = groupDigits(int64(first)) + "–" + groupDigits(int64(last))
	}
	if st.page > 1 {
		body.PrevURL = st.with(path, func(c *listState) { c.page-- })
	}
	if more {
		body.NextURL = st.with(path, func(c *listState) { c.page++ })
	}
	a.render(w, rq, http.StatusOK, "list", tm.label, tm, body)
}

func isNumeric(c *colMeta) bool {
	switch c.c.Kind {
	case KindInt, KindFloat, KindDecimal:
		return c.fk == nil && !c.masked
	}
	return false
}

func removeFilter(fs []stateFilter, name string) []stateFilter {
	var out []stateFilter
	for _, f := range fs {
		if f.name() != name {
			out = append(out, f)
		}
	}
	return out
}

func chipLabel(f stateFilter) string {
	val := truncate(f.raw, 40)
	switch f.param {
	case "has":
		return f.col.label + " contains " + val
	case "from":
		return f.col.label + " from " + val
	case "to":
		return f.col.label + " to " + val
	case "is":
		switch f.raw {
		case "null":
			return f.col.label + " is NULL"
		case "notnull":
			return f.col.label + " is not NULL"
		}
		return f.col.label + " is empty"
	}
	return f.col.label + " is " + val
}

// refURL is where a foreign-key value links to: the row it names when
// that row is addressed by the referenced column (row is then true),
// otherwise the target list filtered to it.
func (a *Admin) refURL(fk *fkMeta, val string) (link string, row bool) {
	t := fk.target
	if len(t.pk) == 1 && t.pk[0] == fk.col {
		if _, msg := parseInput(fk.col.c, nil, val); msg == "" {
			return a.rowURL(t, encodeKey([]string{val})), true
		}
		return "", false
	}
	if !fk.col.comparable() {
		return "", false
	}
	return withQuery(a.listURL(t), url.Values{"eq." + fk.col.c.Name: {val}}), false
}

// listLabels fetches, for each foreign-key column of the list, the display
// value of the rows referred to. Only targets the request may view are
// read. Labels are a convenience: if they cannot be had the list is shown
// without them.
func (a *Admin) listLabels(rq *request, tm *tableMeta, rows [][]value) map[*colMeta]map[string]string {
	out := map[*colMeta]map[string]string{}
	for _, c := range tm.listCols {
		if c.fk == nil || c.fk.target.display == nil || !rq.can(ActionView, c.fk.target) {
			continue
		}
		seen := map[string]bool{}
		var vals []string
		for _, row := range rows {
			if v := row[c.idx]; !v.Null && !seen[v.S] {
				seen[v.S] = true
				vals = append(vals, v.S)
			}
		}
		labels, err := a.labels(rq.r.Context(), c.fk, vals)
		if err != nil {
			a.log.WarnContext(rq.r.Context(), "schemadmin: foreign-key labels unavailable",
				slog.String("table", tm.key), slog.String("column", c.c.Name), slog.Any("error", err))
			continue
		}
		out[c] = labels
	}
	return out
}

const optionsLimit = 20

// handleOptions answers the foreign-key typeahead: rows of this table
// matching q, as JSON. It reads this table, so it requires ActionView on
// it, like the list does.
func (a *Admin) handleOptions(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm := a.table(w, rq, ActionView)
	if tm == nil {
		return
	}
	col := tm.byName[r.URL.Query().Get("col")]
	if col == nil || col.masked || col.c.Kind == KindBytes {
		a.notFound(w, rq)
		return
	}
	opts, err := a.options(r.Context(), &fkMeta{target: tm, col: col}, strings.TrimSpace(r.URL.Query().Get("q")), optionsLimit)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	type item struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	items := make([]item, len(opts))
	for i, o := range opts {
		items[i] = item{o.Value, o.Label}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(items)
}
