package sqlite

import (
	"testing"

	"github.com/Uktupus/schemadmin"
)

func TestKindOf(t *testing.T) {
	tests := []struct {
		declType string
		want     schemadmin.Kind
	}{
		// Well-known names.
		{"BOOLEAN", schemadmin.KindBool},
		{"bool", schemadmin.KindBool},
		{"DATE", schemadmin.KindDate},
		{"TIME", schemadmin.KindTime},
		{"DATETIME", schemadmin.KindDateTime},
		{"datetime(6)", schemadmin.KindDateTime},
		{"TIMESTAMP", schemadmin.KindDateTime},
		{"TIMESTAMP WITH TIME ZONE", schemadmin.KindDateTime},
		{"timestamptz", schemadmin.KindDateTime},
		{"JSON", schemadmin.KindJSON},
		{"UUID", schemadmin.KindUUID},
		{"guid", schemadmin.KindUUID},
		{"DECIMAL(10,2)", schemadmin.KindDecimal},
		{"NUMERIC", schemadmin.KindDecimal},
		{"numeric(12, 4)", schemadmin.KindDecimal},

		// INTEGER affinity.
		{"INTEGER", schemadmin.KindInt},
		{"INT", schemadmin.KindInt},
		{"int", schemadmin.KindInt},
		{"BIGINT", schemadmin.KindInt},
		{"TINYINT(1)", schemadmin.KindInt},
		{"UNSIGNED BIG INT", schemadmin.KindInt},
		{"INT8", schemadmin.KindInt},

		// TEXT affinity: a declared length means a short string.
		{"VARCHAR(255)", schemadmin.KindString},
		{"varchar", schemadmin.KindString},
		{"CHARACTER(20)", schemadmin.KindString},
		{"NCHAR(55)", schemadmin.KindString},
		{"VARYING CHARACTER(255)", schemadmin.KindString},
		{"NVARCHAR(100)", schemadmin.KindString},
		{"TEXT", schemadmin.KindText},
		{"text", schemadmin.KindText},
		{"CLOB", schemadmin.KindText},
		{"MEDIUMTEXT", schemadmin.KindText},

		// BLOB affinity.
		{"BLOB", schemadmin.KindBytes},
		{"blob", schemadmin.KindBytes},
		{"", schemadmin.KindOther},
		{"   ", schemadmin.KindOther},

		// REAL affinity.
		{"REAL", schemadmin.KindFloat},
		{"DOUBLE", schemadmin.KindFloat},
		{"DOUBLE PRECISION", schemadmin.KindFloat},
		{"FLOAT", schemadmin.KindFloat},

		// NUMERIC affinity with no recognisable name.
		{"ANY", schemadmin.KindOther},
		{"STRING", schemadmin.KindOther},
		{"JSONB", schemadmin.KindOther},
		{"MONEY", schemadmin.KindOther},

		// SQLite's affinity rules look for substrings; these follow them.
		{"POINT", schemadmin.KindInt},
		{"CHARINT", schemadmin.KindInt},
	}
	for _, tt := range tests {
		if got := kindOf(tt.declType); got != tt.want {
			t.Errorf("kindOf(%q) = %v, want %v", tt.declType, got, tt.want)
		}
	}
}

func TestBaseTypeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"varchar(255)", "VARCHAR"},
		{"DECIMAL ( 10 , 2 )", "DECIMAL"},
		{"  double   precision ", "DOUBLE PRECISION"},
		{"VARCHAR(10) COLLATE NOCASE", "VARCHAR COLLATE NOCASE"},
		{"WEIRD((nested))TYPE", "WEIRD TYPE"},
		{"unbalanced)", "UNBALANCED"},
		{"unclosed(10", "UNCLOSED"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := baseTypeName(tt.in); got != tt.want {
			t.Errorf("baseTypeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStripGenerated(t *testing.T) {
	tests := []struct{ in, want string }{
		{"INT GENERATED ALWAYS", "INT"},
		{"TEXT generated always", "TEXT"},
		{"GENERATED ALWAYS", ""},
		{"  INTEGER  ", "INTEGER"},
		{"INT", "INT"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := stripGenerated(tt.in); got != tt.want {
			t.Errorf("stripGenerated(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFoldASCII(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Users", "users"},
		{"already_lower", "already_lower"},
		{"MiXeD_123", "mixed_123"},
		{"ÄBC", "Äbc"}, // only ASCII letters fold, as in SQLite
		{"", ""},
	}
	for _, tt := range tests {
		if got := foldASCII(tt.in); got != tt.want {
			t.Errorf("foldASCII(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
