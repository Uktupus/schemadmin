package mysql

import (
	"strconv"
	"strings"

	"github.com/Uktupus/schemadmin"
)

// kindOf maps information_schema type names to a Kind and, for length-limited
// character types, the limit.
//
//   - dataType is COLUMNS.DATA_TYPE (enum, varchar, int, …)
//   - columnType is COLUMNS.COLUMN_TYPE (the full declaration, e.g. varchar(100))
func kindOf(dataType, columnType string) (kind schemadmin.Kind, maxLength int) {
	dataType = strings.ToLower(strings.TrimSpace(dataType))
	columnType = strings.TrimSpace(columnType)

	switch dataType {
	case "enum":
		return schemadmin.KindEnum, 0
	case "set":
		return schemadmin.KindOther, 0
	case "tinyint":
		if tinyintWidth(columnType) == 1 {
			return schemadmin.KindBool, 0
		}
		return schemadmin.KindInt, 0
	case "smallint", "mediumint", "int", "integer", "bigint":
		return schemadmin.KindInt, 0
	case "float", "double", "real":
		return schemadmin.KindFloat, 0
	case "decimal", "numeric":
		return schemadmin.KindDecimal, 0
	case "char", "varchar", "binary", "varbinary":
		if n := typeLength(columnType); n > 0 {
			if dataType == "binary" || dataType == "varbinary" {
				return schemadmin.KindBytes, 0
			}
			return schemadmin.KindString, n
		}
		if dataType == "binary" || dataType == "varbinary" {
			return schemadmin.KindBytes, 0
		}
		return schemadmin.KindText, 0
	case "tinytext", "text", "mediumtext", "longtext":
		return schemadmin.KindText, 0
	case "tinyblob", "blob", "mediumblob", "longblob":
		return schemadmin.KindBytes, 0
	case "bit":
		return schemadmin.KindBytes, 0
	case "date":
		return schemadmin.KindDate, 0
	case "time":
		return schemadmin.KindTime, 0
	case "datetime":
		return schemadmin.KindDateTime, 0
	case "timestamp":
		return schemadmin.KindDateTimeTZ, 0
	case "json":
		return schemadmin.KindJSON, 0
	case "year":
		return schemadmin.KindOther, 0
	}
	return schemadmin.KindOther, 0
}

// tinyintWidth returns the display width of tinyint(n), or 0 when absent.
func tinyintWidth(columnType string) int {
	columnType = strings.ToLower(columnType)
	if !strings.HasPrefix(columnType, "tinyint") {
		return 0
	}
	return typeLength(columnType)
}

// typeLength reads the length from a type such as varchar(100) or char(2).
func typeLength(columnType string) int {
	i := strings.IndexByte(columnType, '(')
	if i < 0 {
		return 0
	}
	j := strings.IndexByte(columnType[i:], ')')
	if j < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(columnType[i+1 : i+j]))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// parseEnumValues returns the labels of an enum column, in declaration order.
func parseEnumValues(columnType string) []string {
	columnType = strings.TrimSpace(columnType)
	if len(columnType) < 6 || !strings.EqualFold(columnType[:5], "enum(") {
		return nil
	}
	if columnType[len(columnType)-1] != ')' {
		return nil
	}
	inner := columnType[5 : len(columnType)-1]
	var values []string
	for inner != "" {
		if inner[0] != '\'' {
			return nil
		}
		var b strings.Builder
		inner = inner[1:]
		for {
			if inner == "" {
				return nil
			}
			if inner[0] == '\'' {
				if len(inner) > 1 && inner[1] == '\'' {
					b.WriteByte('\'')
					inner = inner[2:]
					continue
				}
				values = append(values, b.String())
				inner = inner[1:]
				if inner == "" {
					return values
				}
				if inner[0] != ',' {
					return nil
				}
				inner = strings.TrimSpace(inner[1:])
				break
			}
			b.WriteByte(inner[0])
			inner = inner[1:]
		}
	}
	return values
}

// parseAction maps referential_rules to an FKAction.
func parseAction(rule string) schemadmin.FKAction {
	switch strings.ToUpper(strings.TrimSpace(rule)) {
	case "RESTRICT":
		return schemadmin.FKRestrict
	case "CASCADE":
		return schemadmin.FKCascade
	case "SET NULL":
		return schemadmin.FKSetNull
	case "SET DEFAULT":
		return schemadmin.FKSetDefault
	}
	return schemadmin.FKNoAction
}
