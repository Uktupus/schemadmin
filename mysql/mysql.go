// Package mysql implements the schemadmin Dialect for MySQL 8.0 and later.
//
// It works with any database/sql driver for MySQL and imports none.
package mysql

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/Uktupus/schemadmin"
)

// Dialect is the MySQL dialect. The zero value is ready to use.
type Dialect struct{}

var (
	_ schemadmin.Dialect            = Dialect{}
	_ schemadmin.ReturningDialect   = Dialect{}
	_ schemadmin.EmptyInsertDialect = Dialect{}
)

// New returns the MySQL dialect.
func New() Dialect { return Dialect{} }

// SupportsReturning reports that MySQL has no INSERT … RETURNING.
func (Dialect) SupportsReturning() bool { return false }

// EmptyInsert is the MySQL form of an INSERT that supplies no columns.
func (Dialect) EmptyInsert(quotedTable string) string {
	return "INSERT INTO " + quotedTable + " () VALUES ()"
}

// Name returns "mysql".
func (Dialect) Name() string { return "mysql" }

// QuoteIdent wraps name in backticks, doubling any it contains.
func (Dialect) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// Placeholder returns "?"; MySQL binds parameters by position.
func (Dialect) Placeholder(int) string { return "?" }

// SelectExpr makes every value arrive as bool, int64, or string, whichever
// driver is in use. Dates and times are formatted to ISO-like strings so
// the text does not depend on session settings. Timestamp columns are read
// in UTC.
func (Dialect) SelectExpr(quoted string, c *schemadmin.Column) string {
	switch c.Kind {
	case schemadmin.KindBool, schemadmin.KindInt:
		return quoted
	case schemadmin.KindBytes:
		// HEX() turns NULL into NULL; CAST keeps the result a string.
		return "LOWER(CAST(HEX(" + quoted + ") AS CHAR))"
	case schemadmin.KindTime:
		return trimFractionalSeconds("TIME_FORMAT(" + quoted + ", '%H:%i:%s.%f')")
	case schemadmin.KindDate:
		return "DATE_FORMAT(" + quoted + ", '%Y-%m-%d')"
	case schemadmin.KindDateTime:
		return trimFractionalSeconds("DATE_FORMAT(" + quoted + ", '%Y-%m-%dT%H:%i:%s.%f')")
	case schemadmin.KindDateTimeTZ:
		utc := "CONVERT_TZ(" + quoted + ", @@session.time_zone, '+00:00')"
		return trimFractionalSeconds("DATE_FORMAT(" + utc + ", '%Y-%m-%dT%H:%i:%s.%f')")
	}
	return "CAST(" + quoted + " AS CHAR)"
}

// trimFractionalSeconds wraps an expression that yields a datetime string
// with six-digit fractional seconds and trims trailing zeros and the dot
// when the fraction is zero.
func trimFractionalSeconds(inner string) string {
	return "NULLIF(TRIM(TRAILING '.' FROM TRIM(TRAILING '0' FROM " + inner + ")), '')"
}

// ContainsExpr matches with a case-insensitive LIKE. The backslash is the
// escape character for wildcards in the bound pattern.
func (Dialect) ContainsExpr(quoted, placeholder string) string {
	return "LOWER(CAST(" + quoted + " AS CHAR)) LIKE LOWER(" + placeholder + ") ESCAPE '\\\\'"
}

// ClassifyError recognises constraint violations by the MySQL error number.
// Common drivers expose it through a Number method on their error type.
var mysqlErrNumberRE = regexp.MustCompile(`Error (\d+)`)

func (Dialect) ClassifyError(err error) schemadmin.ConstraintKind {
	n, ok := mysqlErrorNumber(err)
	if ok {
		switch n {
		case 1062, 1586: // ER_DUP_ENTRY, ER_DUP_ENTRY_WITH_KEY_NAME
			return schemadmin.ConstraintUnique
		case 1451, 1452, 1217: // foreign key violations
			return schemadmin.ConstraintForeignKey
		case 1048, 1364: // ER_BAD_NULL_ERROR, ER_NO_DEFAULT_FOR_FIELD
			return schemadmin.ConstraintNotNull
		case 3819, 4025: // CHECK constraint
			return schemadmin.ConstraintCheck
		}
	}
	return schemadmin.ConstraintNone
}

func mysqlErrorNumber(err error) (uint16, bool) {
	var numbered interface{ Number() uint16 }
	if errors.As(err, &numbered) {
		return numbered.Number(), true
	}
	if err == nil {
		return 0, false
	}
	m := mysqlErrNumberRE.FindStringSubmatch(err.Error())
	if len(m) != 2 {
		return 0, false
	}
	v, convErr := strconv.ParseUint(m[1], 10, 16)
	if convErr != nil {
		return 0, false
	}
	return uint16(v), true
}
