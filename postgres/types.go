package postgres

import "github.com/Uktupus/schemadmin"

// typmodHeader is the offset PostgreSQL adds to the declared length of
// varchar(n) and char(n) when storing it as a type modifier.
const typmodHeader = 4

// kindOf maps a column's base type to a Kind and, for length-limited
// character types, the limit. The base type is the column's own type, or
// for a domain the type the domain is ultimately built on.
//
//   - typeSchema and typeName identify the type (pg_type.typname, so the
//     internal names: int4, bpchar, timestamptz)
//   - typType is pg_type.typtype: "e" for an enum
//   - typmod is the type modifier, -1 when there is none
func kindOf(typeSchema, typeName, typType string, typmod int) (kind schemadmin.Kind, maxLength int) {
	if typType == "e" {
		return schemadmin.KindEnum, 0
	}
	// citext is an extension type, installed in whatever schema the
	// extension was created in.
	if typeName == "citext" {
		return schemadmin.KindText, 0
	}
	// Built-in names only mean what they say in pg_catalog.
	if typeSchema != "pg_catalog" {
		return schemadmin.KindOther, 0
	}

	switch typeName {
	case "bool":
		return schemadmin.KindBool, 0
	case "int2", "int4", "int8":
		return schemadmin.KindInt, 0
	case "float4", "float8":
		return schemadmin.KindFloat, 0
	case "numeric":
		return schemadmin.KindDecimal, 0
	case "varchar", "bpchar":
		// Without a length these are unbounded, like text.
		if typmod > typmodHeader {
			return schemadmin.KindString, typmod - typmodHeader
		}
		return schemadmin.KindText, 0
	case "text":
		return schemadmin.KindText, 0
	case "date":
		return schemadmin.KindDate, 0
	case "time":
		return schemadmin.KindTime, 0
	case "timestamp":
		return schemadmin.KindDateTime, 0
	case "timestamptz":
		return schemadmin.KindDateTimeTZ, 0
	case "json", "jsonb":
		return schemadmin.KindJSON, 0
	case "uuid":
		return schemadmin.KindUUID, 0
	case "bytea":
		return schemadmin.KindBytes, 0
	}
	// Arrays, ranges, intervals, network addresses, timetz, money and
	// everything else are handled as opaque text.
	return schemadmin.KindOther, 0
}

// parseAction maps pg_constraint.confdeltype / confupdtype to an FKAction.
func parseAction(code string) schemadmin.FKAction {
	switch code {
	case "r":
		return schemadmin.FKRestrict
	case "c":
		return schemadmin.FKCascade
	case "n":
		return schemadmin.FKSetNull
	case "d":
		return schemadmin.FKSetDefault
	}
	return schemadmin.FKNoAction // "a"
}
