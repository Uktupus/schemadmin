// Package sqlite implements the schemadmin Dialect for SQLite 3.37 and later.
//
// It works with any database/sql driver for SQLite and imports none.
package sqlite

import (
	"strings"

	"github.com/Uktupus/schemadmin"
)

// Dialect is the SQLite dialect. The zero value is ready to use.
type Dialect struct{}

var _ schemadmin.Dialect = Dialect{}

// New returns the SQLite dialect.
func New() Dialect { return Dialect{} }

// Name returns "sqlite".
func (Dialect) Name() string { return "sqlite" }

// QuoteIdent wraps name in double quotes, doubling any it contains.
func (Dialect) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Placeholder returns "?"; SQLite binds parameters by position.
func (Dialect) Placeholder(int) string { return "?" }

// SelectExpr reads the column through a unary plus. That is a no-op on the
// value but makes the result an expression rather than a column, so drivers
// see no declared type and cannot reinterpret the value: what comes back is
// exactly what is stored (integer, real, text or blob), whichever driver is
// in use.
//
// The plus also hides the column from the query planner, which is why the
// expression must stay in the select list: in a WHERE, ORDER BY or join
// condition it would keep SQLite from using an index on the column.
func (Dialect) SelectExpr(quoted string, _ *schemadmin.Column) string {
	return "+" + quoted
}

// ContainsExpr matches with LIKE, which SQLite evaluates case-insensitively
// for ASCII characters.
func (Dialect) ContainsExpr(quoted, placeholder string) string {
	return "CAST(" + quoted + " AS TEXT) LIKE " + placeholder + ` ESCAPE '\'`
}

// ClassifyError recognises constraint violations by SQLite's own message
// text. Drivers expose error codes in incompatible ways, but all of them
// carry the message the engine produced, and its wording is stable.
func (Dialect) ClassifyError(err error) schemadmin.ConstraintKind {
	if err == nil {
		return schemadmin.ConstraintNone
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"),
		strings.Contains(msg, "PRIMARY KEY must be unique"):
		return schemadmin.ConstraintUnique
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return schemadmin.ConstraintForeignKey
	case strings.Contains(msg, "NOT NULL constraint failed"):
		return schemadmin.ConstraintNotNull
	case strings.Contains(msg, "CHECK constraint failed"):
		return schemadmin.ConstraintCheck
	}
	return schemadmin.ConstraintNone
}
