package schemadmin

import (
	"context"
	"database/sql"
)

// Dialect isolates everything that differs between databases: how the schema
// is read and the handful of SQL fragments that are not portable.
//
// Implementations must not depend on a particular database/sql driver.
type Dialect interface {
	// Name identifies the dialect, e.g. "sqlite".
	Name() string

	// Introspect reads the tables visible through db. The returned Schema
	// must satisfy Schema.Validate.
	Introspect(ctx context.Context, db *sql.DB, opts IntrospectOptions) (*Schema, error)

	// QuoteIdent quotes name for use as an identifier. It is only ever
	// called with names taken from an introspected Schema.
	QuoteIdent(name string) string

	// Placeholder returns the marker for the n-th bound parameter of a
	// statement, counting from 1.
	Placeholder(n int) string

	// SelectExpr returns the expression that reads column c, given its
	// quoted name. Dialects use it to make the Go type a value scans into
	// independent of the driver in use.
	SelectExpr(quoted string, c *Column) string

	// ContainsExpr returns a boolean expression that is true when the
	// quoted column, read as text, matches the LIKE pattern bound to
	// placeholder, ignoring case. In the bound pattern a backslash escapes
	// '%', '_' and itself.
	ContainsExpr(quoted, placeholder string) string

	// ClassifyError reports which kind of integrity constraint err
	// represents, or ConstraintNone when it is not a constraint violation.
	ClassifyError(err error) ConstraintKind
}

// IntrospectOptions narrows what a Dialect reads.
type IntrospectOptions struct {
	// Schemas lists the namespaces to read. Empty means the dialect's
	// default.
	Schemas []string
}

// ConstraintKind is the category of an integrity-constraint violation.
type ConstraintKind int

const (
	ConstraintNone ConstraintKind = iota
	ConstraintUnique
	ConstraintForeignKey
	ConstraintNotNull
	ConstraintCheck
)

func (k ConstraintKind) String() string {
	switch k {
	case ConstraintNone:
		return "none"
	case ConstraintUnique:
		return "unique"
	case ConstraintForeignKey:
		return "foreign key"
	case ConstraintNotNull:
		return "not null"
	case ConstraintCheck:
		return "check"
	}
	return "unknown"
}
