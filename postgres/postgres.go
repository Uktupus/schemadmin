// Package postgres implements the schemadmin Dialect for PostgreSQL 12 and
// later.
//
// It works with any database/sql driver for PostgreSQL and imports none.
package postgres

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Uktupus/schemadmin"
)

// Dialect is the PostgreSQL dialect. The zero value is ready to use.
type Dialect struct{}

var _ schemadmin.Dialect = Dialect{}

// New returns the PostgreSQL dialect.
func New() Dialect { return Dialect{} }

// Name returns "postgres".
func (Dialect) Name() string { return "postgres" }

// QuoteIdent wraps name in double quotes, doubling any it contains.
func (Dialect) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Placeholder returns "$n".
func (Dialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

// SelectExpr makes every value arrive in one of three Go types, whichever
// driver is in use: bool, int64, or string. Drivers agree on booleans and
// integers; for everything else they differ (one returns a string where
// another returns bytes or a time.Time), so those columns are read as text.
//
// The text must not depend on session settings either. A plain cast to text
// formats dates according to DateStyle and shifts timestamptz to the
// session's TimeZone, so the date and time kinds go through to_json, which
// always produces ISO 8601, and timestamptz is first converted to UTC:
//
//	date         2024-01-02
//	timestamp    2024-01-02T03:04:05.123456
//	timestamptz  2024-01-02T03:04:05.123456   (UTC, no offset)
//
// Binary columns are returned as they are; drivers agree on []byte.
func (Dialect) SelectExpr(quoted string, c *schemadmin.Column) string {
	switch c.Kind {
	case schemadmin.KindBool, schemadmin.KindInt:
		return quoted
	case schemadmin.KindBytes:
		return "encode(" + quoted + ", 'hex')" // NULL stays NULL
	case schemadmin.KindDate, schemadmin.KindDateTime:
		return "(to_json(" + quoted + ") #>> '{}')"
	case schemadmin.KindDateTimeTZ:
		return "(to_json(" + quoted + " AT TIME ZONE 'UTC') #>> '{}')"
	}
	return quoted + "::text"
}

// ContainsExpr matches with ILIKE. No ESCAPE clause is given because the
// backslash is already PostgreSQL's default escape character for patterns.
func (Dialect) ContainsExpr(quoted, placeholder string) string {
	return quoted + "::text ILIKE " + placeholder
}

// ClassifyError recognises constraint violations by SQLSTATE. Both common
// drivers expose it through a SQLState method on their error type, so no
// driver needs to be imported to read it.
func (Dialect) ClassifyError(err error) schemadmin.ConstraintKind {
	var stater interface{ SQLState() string }
	if !errors.As(err, &stater) {
		return schemadmin.ConstraintNone
	}
	switch stater.SQLState() {
	case "23505", // unique_violation
		"23P01": // exclusion_violation
		return schemadmin.ConstraintUnique
	case "23503", // foreign_key_violation
		"23001": // restrict_violation
		return schemadmin.ConstraintForeignKey
	case "23502": // not_null_violation
		return schemadmin.ConstraintNotNull
	case "23514": // check_violation
		return schemadmin.ConstraintCheck
	}
	return schemadmin.ConstraintNone
}
