package sqlite

import (
	"strings"

	"github.com/Uktupus/schemadmin"
)

// kindOf maps a declared column type to a Kind.
//
// SQLite accepts any text as a type name, so well-known names are matched
// first and everything else follows SQLite's own type-affinity rules
// (https://www.sqlite.org/datatype3.html#determination_of_column_affinity).
func kindOf(declType string) schemadmin.Kind {
	name := baseTypeName(declType)

	switch name {
	case "BOOL", "BOOLEAN":
		return schemadmin.KindBool
	case "DATE":
		return schemadmin.KindDate
	case "TIME":
		return schemadmin.KindTime
	case "DATETIME":
		return schemadmin.KindDateTime
	case "JSON":
		return schemadmin.KindJSON
	case "UUID", "GUID":
		return schemadmin.KindUUID
	case "DECIMAL", "NUMERIC":
		return schemadmin.KindDecimal
	}
	// SQLite stores no zone information, so every timestamp flavour is a
	// wall-clock value.
	if strings.HasPrefix(name, "TIMESTAMP") {
		return schemadmin.KindDateTime
	}

	switch {
	case strings.Contains(name, "INT"):
		return schemadmin.KindInt
	case strings.Contains(name, "CHAR"):
		return schemadmin.KindString
	case strings.Contains(name, "CLOB"), strings.Contains(name, "TEXT"):
		return schemadmin.KindText
	case strings.Contains(name, "BLOB"):
		return schemadmin.KindBytes
	case strings.Contains(name, "REAL"), strings.Contains(name, "FLOA"), strings.Contains(name, "DOUB"):
		return schemadmin.KindFloat
	}
	return schemadmin.KindOther
}

// baseTypeName upper-cases a declared type, removes parenthesised arguments
// such as the "(10, 2)" of DECIMAL(10, 2), and collapses whitespace.
func baseTypeName(declType string) string {
	var b strings.Builder
	depth := 0
	for _, r := range declType {
		switch {
		case r == '(':
			depth++
			b.WriteByte(' ')
		case r == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return strings.ToUpper(strings.Join(strings.Fields(b.String()), " "))
}

// stripGenerated removes the "GENERATED ALWAYS" that older SQLite versions
// leave at the end of a generated column's reported type.
func stripGenerated(declType string) string {
	const suffix = "GENERATED ALWAYS"
	t := strings.TrimSpace(declType)
	if len(t) >= len(suffix) && strings.EqualFold(t[len(t)-len(suffix):], suffix) {
		return strings.TrimSpace(t[:len(t)-len(suffix)])
	}
	return t
}

// foldASCII lower-cases ASCII letters only, which is how SQLite compares
// identifiers.
func foldASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}
