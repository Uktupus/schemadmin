package mysql

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Uktupus/schemadmin"
)

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"users", "`users`"},
		{"Users", "`Users`"},
		{"order items", "`order items`"},
		{"we`ird", "`we``ird`"},
		{"`", "````"},
		{"x`; DROP TABLE users; --", "`x``; DROP TABLE users; --`"},
		{"sel'ect", "`sel'ect`"},
		{`back\slash`, "`back\\slash`"},
		{"$1", "`$1`"},
		{"ünïcode", "`ünïcode`"},
	}
	d := New()
	for _, tt := range tests {
		if got := d.QuoteIdent(tt.name); got != tt.want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", tt.name, got, tt.want)
		}
	}
}

func FuzzQuoteIdent(f *testing.F) {
	for _, seed := range []string{"users", "`a`", "``", "x`; DROP TABLE t; --", "", "a```b"} {
		f.Add(seed)
	}
	d := New()
	f.Fuzz(func(t *testing.T, name string) {
		quoted := d.QuoteIdent(name)
		if len(quoted) < 2 || quoted[0] != '`' || quoted[len(quoted)-1] != '`' {
			t.Fatalf("QuoteIdent(%q) = %s: not wrapped in backticks", name, quoted)
		}
		inner := quoted[1 : len(quoted)-1]
		if strings.Contains(strings.ReplaceAll(inner, "``", ""), "`") {
			t.Fatalf("QuoteIdent(%q) = %s: contains an unpaired backtick", name, quoted)
		}
		if got := strings.ReplaceAll(inner, "``", "`"); got != name {
			t.Fatalf("QuoteIdent(%q) = %s: unquotes to %q", name, quoted, got)
		}
	})
}

func TestPlaceholder(t *testing.T) {
	d := New()
	for n, want := range map[int]string{1: "?", 2: "?", 10: "?"} {
		if got := d.Placeholder(n); got != want {
			t.Errorf("Placeholder(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSelectExpr(t *testing.T) {
	const q = "`col`"
	tests := []struct {
		kind schemadmin.Kind
		want string
	}{
		{schemadmin.KindBool, "`col`"},
		{schemadmin.KindInt, "`col`"},
		{schemadmin.KindBytes, "LOWER(CAST(HEX(`col`) AS CHAR))"},
		{schemadmin.KindDate, "DATE_FORMAT(`col`, '%Y-%m-%d')"},
		{schemadmin.KindDateTime, trimFractionalSeconds("DATE_FORMAT(`col`, '%Y-%m-%dT%H:%i:%s.%f')")},
		{schemadmin.KindDateTimeTZ, trimFractionalSeconds("DATE_FORMAT(CONVERT_TZ(`col`, @@session.time_zone, '+00:00'), '%Y-%m-%dT%H:%i:%s.%f')")},
		{schemadmin.KindTime, trimFractionalSeconds("TIME_FORMAT(`col`, '%H:%i:%s.%f')")},
		{schemadmin.KindString, "CAST(`col` AS CHAR)"},
		{schemadmin.KindText, "CAST(`col` AS CHAR)"},
		{schemadmin.KindFloat, "CAST(`col` AS CHAR)"},
		{schemadmin.KindDecimal, "CAST(`col` AS CHAR)"},
		{schemadmin.KindJSON, "CAST(`col` AS CHAR)"},
		{schemadmin.KindUUID, "CAST(`col` AS CHAR)"},
		{schemadmin.KindEnum, "CAST(`col` AS CHAR)"},
		{schemadmin.KindOther, "CAST(`col` AS CHAR)"},
	}
	d := New()
	var covered []schemadmin.Kind
	for _, tt := range tests {
		covered = append(covered, tt.kind)
		if got := d.SelectExpr(q, &schemadmin.Column{Name: "col", Kind: tt.kind}); got != tt.want {
			t.Errorf("SelectExpr(%v) = %s, want %s", tt.kind, got, tt.want)
		}
	}
	for k := schemadmin.Kind(0); k.Valid(); k++ {
		if !slices.Contains(covered, k) {
			t.Errorf("kind %v has no SelectExpr test case", k)
		}
	}
}

func TestContainsExpr(t *testing.T) {
	got := New().ContainsExpr("`name`", "?")
	if want := "LOWER(CAST(`name` AS CHAR)) LIKE LOWER(?) ESCAPE '\\\\'"; got != want {
		t.Errorf("ContainsExpr = %s, want %s", got, want)
	}
}

type numberError struct{ n uint16 }

func (e *numberError) Error() string  { return fmt.Sprintf("mysql error %d", e.n) }
func (e *numberError) Number() uint16 { return e.n }

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want schemadmin.ConstraintKind
	}{
		{"nil", nil, schemadmin.ConstraintNone},
		{"dup entry", &numberError{1062}, schemadmin.ConstraintUnique},
		{"dup entry named", &numberError{1586}, schemadmin.ConstraintUnique},
		{"fk parent", &numberError{1451}, schemadmin.ConstraintForeignKey},
		{"fk child", &numberError{1452}, schemadmin.ConstraintForeignKey},
		{"not null", &numberError{1048}, schemadmin.ConstraintNotNull},
		{"no default", &numberError{1364}, schemadmin.ConstraintNotNull},
		{"check", &numberError{3819}, schemadmin.ConstraintCheck},
		{"check client", &numberError{4025}, schemadmin.ConstraintCheck},
		{"wrapped", fmt.Errorf("insert: %w", &numberError{1062}), schemadmin.ConstraintUnique},
		{"syntax", &numberError{1064}, schemadmin.ConstraintNone},
		{"driver message", errors.New("Error 1062 (23000): Duplicate entry 'x' for key 't.PRIMARY'"), schemadmin.ConstraintUnique},
		{"no number", errors.New("duplicate entry"), schemadmin.ConstraintNone},
	}
	d := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.ClassifyError(tt.err); got != tt.want {
				t.Errorf("ClassifyError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestKindOf(t *testing.T) {
	tests := []struct {
		dataType, columnType string
		wantKind             schemadmin.Kind
		wantMax              int
	}{
		{"tinyint", "tinyint(1)", schemadmin.KindBool, 0},
		{"tinyint", "tinyint(4)", schemadmin.KindInt, 0},
		{"int", "int", schemadmin.KindInt, 0},
		{"bigint", "bigint unsigned", schemadmin.KindInt, 0},
		{"float", "float", schemadmin.KindFloat, 0},
		{"double", "double", schemadmin.KindFloat, 0},
		{"decimal", "decimal(10,2)", schemadmin.KindDecimal, 0},
		{"varchar", "varchar(100)", schemadmin.KindString, 100},
		{"char", "char(2)", schemadmin.KindString, 2},
		{"text", "text", schemadmin.KindText, 0},
		{"enum", "enum('a','b')", schemadmin.KindEnum, 0},
		{"set", "set('a','b')", schemadmin.KindOther, 0},
		{"date", "date", schemadmin.KindDate, 0},
		{"time", "time", schemadmin.KindTime, 0},
		{"datetime", "datetime(6)", schemadmin.KindDateTime, 0},
		{"timestamp", "timestamp", schemadmin.KindDateTimeTZ, 0},
		{"json", "json", schemadmin.KindJSON, 0},
		{"binary", "binary(16)", schemadmin.KindBytes, 0},
		{"varbinary", "varbinary(255)", schemadmin.KindBytes, 0},
		{"blob", "blob", schemadmin.KindBytes, 0},
		{"bit", "bit(8)", schemadmin.KindBytes, 0},
		{"year", "year", schemadmin.KindOther, 0},
		{"geometry", "point", schemadmin.KindOther, 0},
	}
	for _, tt := range tests {
		kind, maxLength := kindOf(tt.dataType, tt.columnType)
		if kind != tt.wantKind || maxLength != tt.wantMax {
			t.Errorf("kindOf(%q, %q) = %v, %d; want %v, %d",
				tt.dataType, tt.columnType, kind, maxLength, tt.wantKind, tt.wantMax)
		}
	}
}

func TestParseEnumValues(t *testing.T) {
	tests := []struct {
		typ  string
		want []string
	}{
		{"enum('admin','member')", []string{"admin", "member"}},
		{`enum('it''s','ok')`, []string{"it's", "ok"}},
		{"varchar(10)", nil},
	}
	for _, tt := range tests {
		if got := parseEnumValues(tt.typ); !slices.Equal(got, tt.want) {
			t.Errorf("parseEnumValues(%q) = %q, want %q", tt.typ, got, tt.want)
		}
	}
}

func TestParseAction(t *testing.T) {
	tests := []struct {
		rule string
		want schemadmin.FKAction
	}{
		{"NO ACTION", schemadmin.FKNoAction},
		{"RESTRICT", schemadmin.FKRestrict},
		{"CASCADE", schemadmin.FKCascade},
		{"SET NULL", schemadmin.FKSetNull},
		{"SET DEFAULT", schemadmin.FKSetDefault},
	}
	for _, tt := range tests {
		if got := parseAction(tt.rule); got != tt.want {
			t.Errorf("parseAction(%q) = %v, want %v", tt.rule, got, tt.want)
		}
	}
}
