package schemadmin

import (
	"strings"
)

// The functions in this file build every statement the admin runs. They
// keep three rules:
//
//   - identifiers come from the introspected schema (a *Table or *Column)
//     and pass through Dialect.QuoteIdent; no caller supplies a name;
//   - every value is a bound parameter;
//   - Dialect.SelectExpr appears in select lists only. Conditions and
//     ordering name the bare column, so the database can use its indexes.

// stmt accumulates a statement and its arguments.
type stmt struct {
	d    Dialect
	sql  strings.Builder
	args []any
}

func newStmt(d Dialect) *stmt { return &stmt{d: d} }

func (s *stmt) write(parts ...string) {
	for _, p := range parts {
		s.sql.WriteString(p)
	}
}

// bind adds v as a parameter and returns its placeholder.
func (s *stmt) bind(v any) string {
	s.args = append(s.args, v)
	return s.d.Placeholder(len(s.args))
}

func (s *stmt) table(t *Table) string {
	if t.Schema == "" {
		return s.d.QuoteIdent(t.Name)
	}
	return s.d.QuoteIdent(t.Schema) + "." + s.d.QuoteIdent(t.Name)
}

func (s *stmt) col(c *Column) string { return s.d.QuoteIdent(c.Name) }

// readExpr is the select-list expression for a column. A masked column is
// read only as "is it set"; a binary column only as its length.
func (s *stmt) readExpr(c *colMeta) string {
	q := s.col(c.c)
	switch {
	case c.masked:
		return "CASE WHEN " + q + " IS NULL THEN NULL ELSE 1 END"
	case c.c.Kind == KindBytes:
		return "length(" + q + ")"
	}
	return s.d.SelectExpr(q, c.c)
}

func (s *stmt) selectList(cols []*colMeta) {
	for i, c := range cols {
		if i > 0 {
			s.write(", ")
		}
		s.write(s.readExpr(c))
	}
}

// keyCondition writes "k1 = ? AND k2 = ?".
func (s *stmt) keyCondition(cols []*Column, vals []any) {
	for i, c := range cols {
		if i > 0 {
			s.write(" AND ")
		}
		s.write(s.col(c), " = ", s.bind(vals[i]))
	}
}

type filterOp int

const (
	opEq filterOp = iota
	opIsNull
	opNotNull
	opGTE
	opLTE
	opContains // arg is the text to find, not yet a pattern
)

type filter struct {
	col *colMeta
	op  filterOp
	arg any
}

// listQuery describes one page of a list view.
type listQuery struct {
	search     string     // text to look for; empty for none
	searchCols []*colMeta // columns searched as text
	exact      []filter   // key columns the search text also matches exactly
	filters    []filter
	sort       []*colMeta // ordering, most significant first
	desc       bool       // direction of sort[0]
	limit      int
	offset     int
}

// escapeLike escapes s for use inside a LIKE pattern in which a backslash
// is the escape character.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *stmt) where(q *listQuery) {
	var conds []string
	if q.search != "" {
		pattern := "%" + escapeLike(q.search) + "%"
		var alts []string
		for _, c := range q.searchCols {
			alts = append(alts, s.d.ContainsExpr(s.col(c.c), s.bind(pattern)))
		}
		for _, f := range q.exact {
			alts = append(alts, s.col(f.col.c)+" = "+s.bind(f.arg))
		}
		if len(alts) == 0 {
			alts = []string{"1 = 0"} // nothing to search in: nothing matches
		}
		conds = append(conds, "("+strings.Join(alts, " OR ")+")")
	}
	for _, f := range q.filters {
		c := s.col(f.col.c)
		switch f.op {
		case opEq:
			conds = append(conds, c+" = "+s.bind(f.arg))
		case opIsNull:
			conds = append(conds, c+" IS NULL")
		case opNotNull:
			conds = append(conds, c+" IS NOT NULL")
		case opGTE:
			conds = append(conds, c+" >= "+s.bind(f.arg))
		case opLTE:
			conds = append(conds, c+" <= "+s.bind(f.arg))
		case opContains:
			text, _ := f.arg.(string)
			conds = append(conds, s.d.ContainsExpr(c, s.bind("%"+escapeLike(text)+"%")))
		}
	}
	if len(conds) > 0 {
		s.write(" WHERE ", strings.Join(conds, " AND "))
	}
}

func buildList(d Dialect, tm *tableMeta, q *listQuery) (string, []any) {
	s := newStmt(d)
	s.write("SELECT ")
	s.selectList(tm.cols)
	s.write(" FROM ", s.table(tm.t))
	s.where(q)
	for i, c := range q.sort {
		if i == 0 {
			s.write(" ORDER BY ")
		} else {
			s.write(", ")
		}
		s.write(s.col(c.c))
		if i == 0 && q.desc {
			s.write(" DESC")
		}
	}
	s.write(" LIMIT ", s.bind(int64(q.limit)), " OFFSET ", s.bind(int64(q.offset)))
	return s.sql.String(), s.args
}

func buildCount(d Dialect, tm *tableMeta, q *listQuery) (string, []any) {
	s := newStmt(d)
	s.write("SELECT COUNT(*) FROM ", s.table(tm.t))
	s.where(q)
	return s.sql.String(), s.args
}

func columnsOf(cols []*colMeta) []*Column {
	out := make([]*Column, len(cols))
	for i, c := range cols {
		out[i] = c.c
	}
	return out
}

func buildGet(d Dialect, tm *tableMeta, key []any) (string, []any) {
	s := newStmt(d)
	s.write("SELECT ")
	s.selectList(tm.cols)
	s.write(" FROM ", s.table(tm.t), " WHERE ")
	s.keyCondition(columnsOf(tm.pk), key)
	return s.sql.String(), s.args
}

// buildInsert writes the given columns and, when returning is not empty,
// reads those columns of the new row back.
func buildInsert(d Dialect, tm *tableMeta, cols []*Column, vals []any, returning []*colMeta) (string, []any) {
	s := newStmt(d)
	s.write("INSERT INTO ", s.table(tm.t))
	if len(cols) == 0 {
		s.write(" DEFAULT VALUES")
	} else {
		s.write(" (")
		for i, c := range cols {
			if i > 0 {
				s.write(", ")
			}
			s.write(s.col(c))
		}
		s.write(") VALUES (")
		for i := range cols {
			if i > 0 {
				s.write(", ")
			}
			s.write(s.bind(vals[i]))
		}
		s.write(")")
	}
	if len(returning) > 0 {
		s.write(" RETURNING ")
		s.selectList(returning)
	}
	return s.sql.String(), s.args
}

func buildUpdate(d Dialect, tm *tableMeta, cols []*Column, vals []any, key []any) (string, []any) {
	s := newStmt(d)
	s.write("UPDATE ", s.table(tm.t), " SET ")
	for i, c := range cols {
		if i > 0 {
			s.write(", ")
		}
		s.write(s.col(c), " = ", s.bind(vals[i]))
	}
	s.write(" WHERE ")
	s.keyCondition(columnsOf(tm.pk), key)
	return s.sql.String(), s.args
}

func buildDelete(d Dialect, tm *tableMeta, key []any) (string, []any) {
	s := newStmt(d)
	s.write("DELETE FROM ", s.table(tm.t), " WHERE ")
	s.keyCondition(columnsOf(tm.pk), key)
	return s.sql.String(), s.args
}

// buildCountWhere counts the rows of t in which cols equal vals.
func buildCountWhere(d Dialect, t *Table, cols []*Column, vals []any) (string, []any) {
	s := newStmt(d)
	s.write("SELECT COUNT(*) FROM ", s.table(t), " WHERE ")
	s.keyCondition(cols, vals)
	return s.sql.String(), s.args
}

// buildLabels reads the label column of the rows whose value column is one
// of vals.
func buildLabels(d Dialect, tm *tableMeta, valueCol, labelCol *colMeta, vals []any) (string, []any) {
	s := newStmt(d)
	s.write("SELECT ")
	s.selectList([]*colMeta{valueCol, labelCol})
	s.write(" FROM ", s.table(tm.t), " WHERE ", s.col(valueCol.c), " IN (")
	for i, v := range vals {
		if i > 0 {
			s.write(", ")
		}
		s.write(s.bind(v))
	}
	s.write(")")
	return s.sql.String(), s.args
}

// buildOptions reads up to limit (value, label) pairs for a foreign-key
// control, optionally narrowed to those containing search.
func buildOptions(d Dialect, tm *tableMeta, valueCol, labelCol *colMeta, search string, limit int) (string, []any) {
	s := newStmt(d)
	s.write("SELECT ")
	s.selectList([]*colMeta{valueCol, labelCol})
	s.write(" FROM ", s.table(tm.t))
	if search != "" {
		pattern := "%" + escapeLike(search) + "%"
		s.write(" WHERE ", s.d.ContainsExpr(s.col(valueCol.c), s.bind(pattern)))
		if labelCol != valueCol {
			s.write(" OR ", s.d.ContainsExpr(s.col(labelCol.c), s.bind(pattern)))
		}
	}
	var order []string
	if labelCol != valueCol && labelCol.comparable() {
		order = append(order, s.col(labelCol.c))
	}
	if valueCol.comparable() {
		order = append(order, s.col(valueCol.c))
	}
	if len(order) > 0 {
		s.write(" ORDER BY ", strings.Join(order, ", "))
	}
	s.write(" LIMIT ", s.bind(int64(limit)))
	return s.sql.String(), s.args
}
