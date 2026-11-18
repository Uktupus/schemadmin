package schemadmin

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// errRowCount is returned when a statement addressed by key touched a
// number of rows other than one. The transaction is rolled back.
var errRowCount = errors.New("schemadmin: statement did not affect exactly one row")

// queryer is the part of *sql.DB and *sql.Tx used for reading.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readRows runs a query whose select list was built from cols and returns
// each row as values in the same order.
func readRows(ctx context.Context, q queryer, cols []*colMeta, query string, args []any) ([][]value, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	raw := make([]any, len(cols))
	dest := make([]any, len(cols))
	for i := range raw {
		dest[i] = &raw[i]
	}
	var out [][]value
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		vals := make([]value, len(cols))
		for i, c := range cols {
			kind := c.c.Kind
			if c.masked || kind == KindBytes {
				kind = KindInt // a marker or a length, not the content
			}
			vals[i] = scanValue(kind, raw[i])
		}
		out = append(out, vals)
	}
	return out, rows.Err()
}

// list reads one page and reports whether another follows.
func (a *Admin) list(ctx context.Context, tm *tableMeta, q *listQuery) ([][]value, bool, error) {
	page := *q
	page.limit = q.limit + 1
	query, args := buildList(a.d, tm, &page)
	ctx, cancel := context.WithTimeout(ctx, a.queryTimeout)
	defer cancel()
	rows, err := readRows(ctx, a.db, tm.cols, query, args)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > q.limit {
		return rows[:q.limit], true, nil
	}
	return rows, false, nil
}

// count returns the number of rows matching q, or false when it could not
// be had in time. A table whose count timed out is not counted again for a
// while: the list works without a total, and the database is spared.
func (a *Admin) count(ctx context.Context, tm *tableMeta, q *listQuery) (int64, bool) {
	if until, ok := a.slowCount.Load(tm); ok && time.Now().Before(until.(time.Time)) {
		return 0, false
	}
	query, args := buildCount(a.d, tm, q)
	ctx, cancel := context.WithTimeout(ctx, a.countTimeout)
	defer cancel()
	var n int64
	if err := a.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		if ctx.Err() != nil {
			a.slowCount.Store(tm, time.Now().Add(slowCountPause))
		}
		return 0, false
	}
	return n, true
}

// get reads the row with the given key. The second result is false when
// there is none.
func (a *Admin) get(ctx context.Context, q queryer, tm *tableMeta, key []any) ([]value, bool, error) {
	query, args := buildGet(a.d, tm, key)
	rows, err := readRows(ctx, q, tm.cols, query, args)
	if err != nil {
		return nil, false, err
	}
	if len(rows) != 1 {
		// More than one row under one primary key cannot be addressed
		// safely, so it is treated as absent.
		return nil, false, nil
	}
	return rows[0], true, nil
}

// execOne runs a statement addressed by key inside tx and insists that it
// touched exactly one row.
func execOne(ctx context.Context, tx *sql.Tx, query string, args []any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errRowCount
	}
	return nil
}

// countWhere counts rows of t in which cols equal vals, within the count
// timeout.
func (a *Admin) countWhere(ctx context.Context, t *Table, cols []*Column, vals []any) (int64, bool) {
	query, args := buildCountWhere(a.d, t, cols, vals)
	ctx, cancel := context.WithTimeout(ctx, a.countTimeout)
	defer cancel()
	var n int64
	if err := a.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}

// option is a (value, label) pair of a referenced row.
type option struct {
	Value    string
	Label    string
	Selected bool
}

// optionLabel is how a referenced row is named: its display value, with
// the key after it when that adds something.
func optionLabel(val, label value) string {
	if label.Null || label.S == "" || label.S == val.S {
		return val.S
	}
	return truncate(label.S, 80) + " (" + val.S + ")"
}

// labelColumn is the column that names rows of a foreign key's target.
func (fk *fkMeta) labelColumn() *colMeta {
	if fk.target.display != nil {
		return fk.target.display
	}
	return fk.col
}

// options reads up to limit rows of a foreign key's target.
func (a *Admin) options(ctx context.Context, fk *fkMeta, search string, limit int) ([]option, error) {
	labelCol := fk.labelColumn()
	query, args := buildOptions(a.d, fk.target, fk.col, labelCol, search, limit)
	ctx, cancel := context.WithTimeout(ctx, a.queryTimeout)
	defer cancel()
	rows, err := readRows(ctx, a.db, []*colMeta{fk.col, labelCol}, query, args)
	if err != nil {
		return nil, err
	}
	out := make([]option, 0, len(rows))
	for _, row := range rows {
		if row[0].Null || !representable(row[0].S) {
			continue
		}
		out = append(out, option{Value: row[0].S, Label: optionLabel(row[0], row[1])})
	}
	return out, nil
}

// labels reads the display value of the target rows that vals refer to.
func (a *Admin) labels(ctx context.Context, fk *fkMeta, vals []string) (map[string]string, error) {
	if fk.target.display == nil || len(vals) == 0 {
		return nil, nil
	}
	var args []any
	for _, v := range vals {
		if arg, msg := parseInput(fk.col.c, nil, v); msg == "" {
			args = append(args, arg)
		}
	}
	if len(args) == 0 {
		return nil, nil
	}
	query, qargs := buildLabels(a.d, fk.target, fk.col, fk.target.display, args)
	ctx, cancel := context.WithTimeout(ctx, a.queryTimeout)
	defer cancel()
	rows, err := readRows(ctx, a.db, []*colMeta{fk.col, fk.target.display}, query, qargs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		if !row[0].Null && !row[1].Null {
			out[row[0].S] = row[1].S
		}
	}
	return out, nil
}

// rowKey returns the URL key of a row, or false when the row cannot be
// addressed: the table has no usable primary key, or a key value is NULL
// or is not valid for its column's type.
func (tm *tableMeta) rowKey(row []value) (string, bool) {
	vals := make([]value, len(tm.pk))
	for i, c := range tm.pk {
		vals[i] = row[c.idx]
	}
	return tm.keyOf(vals)
}

// keyOf is rowKey for values given in primary-key order.
func (tm *tableMeta) keyOf(vals []value) (string, bool) {
	if len(tm.pk) == 0 || len(vals) != len(tm.pk) {
		return "", false
	}
	parts := make([]string, len(tm.pk))
	for i, c := range tm.pk {
		if vals[i].Null {
			return "", false
		}
		if _, msg := parseInput(c.c, nil, vals[i].S); msg != "" {
			return "", false
		}
		parts[i] = vals[i].S
	}
	return encodeKey(parts), true
}

// keyArgs decodes a URL key into the arguments that address the row.
func (tm *tableMeta) keyArgs(key string) ([]any, bool) {
	if len(tm.pk) == 0 {
		return nil, false
	}
	parts, ok := decodeKey(key, len(tm.pk))
	if !ok {
		return nil, false
	}
	args := make([]any, len(parts))
	for i, p := range parts {
		arg, msg := parseInput(tm.pk[i].c, nil, p)
		if msg != "" {
			return nil, false
		}
		args[i] = arg
	}
	return args, true
}

func truncate(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i] + "…"
		}
		count++
	}
	return s
}
