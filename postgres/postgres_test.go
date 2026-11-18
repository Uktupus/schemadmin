package postgres

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
		{"users", `"users"`},
		{"Users", `"Users"`},
		{"order items", `"order items"`},
		{`we"ird`, `"we""ird"`},
		{`"`, `""""`},
		{`x"; DROP TABLE users; --`, `"x""; DROP TABLE users; --"`},
		{"sel'ect", `"sel'ect"`},
		{`back\slash`, `"back\slash"`},
		{"$1", `"$1"`},
		{"ünïcode", `"ünïcode"`},
	}
	d := New()
	for _, tt := range tests {
		if got := d.QuoteIdent(tt.name); got != tt.want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", tt.name, got, tt.want)
		}
	}
}

// FuzzQuoteIdent checks the property that makes quoting safe: the result is
// one double-quoted token whose only inner quotes are doubled pairs, and
// undoubling them gives back the name.
func FuzzQuoteIdent(f *testing.F) {
	for _, seed := range []string{"users", `a"b`, `"`, `""`, `x"; DROP TABLE t; --`, "", "a\"\"\"b"} {
		f.Add(seed)
	}
	d := New()
	f.Fuzz(func(t *testing.T, name string) {
		quoted := d.QuoteIdent(name)
		if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
			t.Fatalf("QuoteIdent(%q) = %s: not wrapped in double quotes", name, quoted)
		}
		inner := quoted[1 : len(quoted)-1]
		if strings.Contains(strings.ReplaceAll(inner, `""`, ""), `"`) {
			t.Fatalf("QuoteIdent(%q) = %s: contains an unpaired quote", name, quoted)
		}
		if got := strings.ReplaceAll(inner, `""`, `"`); got != name {
			t.Fatalf("QuoteIdent(%q) = %s: unquotes to %q", name, quoted, got)
		}
	})
}

func TestPlaceholder(t *testing.T) {
	d := New()
	for n, want := range map[int]string{1: "$1", 2: "$2", 10: "$10", 123: "$123"} {
		if got := d.Placeholder(n); got != want {
			t.Errorf("Placeholder(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSelectExpr(t *testing.T) {
	const q = `"col"`
	tests := []struct {
		kind schemadmin.Kind
		want string
	}{
		{schemadmin.KindBool, `"col"`},
		{schemadmin.KindInt, `"col"`},
		{schemadmin.KindBytes, `encode("col", 'hex')`}, // unchanged
		{schemadmin.KindDate, `(to_json("col") #>> '{}')`},
		{schemadmin.KindDateTime, `(to_json("col") #>> '{}')`},
		{schemadmin.KindDateTimeTZ, `(to_json("col" AT TIME ZONE 'UTC') #>> '{}')`},
		{schemadmin.KindTime, `"col"::text`},
		{schemadmin.KindString, `"col"::text`},
		{schemadmin.KindText, `"col"::text`},
		{schemadmin.KindFloat, `"col"::text`},
		{schemadmin.KindDecimal, `"col"::text`},
		{schemadmin.KindJSON, `"col"::text`},
		{schemadmin.KindUUID, `"col"::text`},
		{schemadmin.KindEnum, `"col"::text`},
		{schemadmin.KindOther, `"col"::text`},
	}
	d := New()
	var covered []schemadmin.Kind
	for _, tt := range tests {
		covered = append(covered, tt.kind)
		if got := d.SelectExpr(q, &schemadmin.Column{Name: "col", Kind: tt.kind}); got != tt.want {
			t.Errorf("SelectExpr(%v) = %s, want %s", tt.kind, got, tt.want)
		}
	}
	// A kind added later must be given a deliberate expression here.
	for k := schemadmin.Kind(0); k.Valid(); k++ {
		if !slices.Contains(covered, k) {
			t.Errorf("kind %v has no SelectExpr test case", k)
		}
	}
}

func TestContainsExpr(t *testing.T) {
	got := New().ContainsExpr(`"name"`, "$3")
	if want := `"name"::text ILIKE $3`; got != want {
		t.Errorf("ContainsExpr = %s, want %s", got, want)
	}
}

// stateError mimics how the PostgreSQL drivers expose SQLSTATE.
type stateError struct{ code string }

func (e *stateError) Error() string    { return "driver error " + e.code }
func (e *stateError) SQLState() string { return e.code }

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want schemadmin.ConstraintKind
	}{
		{"nil", nil, schemadmin.ConstraintNone},
		{"unique_violation", &stateError{"23505"}, schemadmin.ConstraintUnique},
		{"exclusion_violation", &stateError{"23P01"}, schemadmin.ConstraintUnique},
		{"foreign_key_violation", &stateError{"23503"}, schemadmin.ConstraintForeignKey},
		{"restrict_violation", &stateError{"23001"}, schemadmin.ConstraintForeignKey},
		{"not_null_violation", &stateError{"23502"}, schemadmin.ConstraintNotNull},
		{"check_violation", &stateError{"23514"}, schemadmin.ConstraintCheck},
		{"wrapped", fmt.Errorf("insert: %w", &stateError{"23505"}), schemadmin.ConstraintUnique},
		{"integrity_constraint_violation class only", &stateError{"23000"}, schemadmin.ConstraintNone},
		{"syntax_error", &stateError{"42601"}, schemadmin.ConstraintNone},
		{"undefined_table", &stateError{"42P01"}, schemadmin.ConstraintNone},
		{"serialization_failure", &stateError{"40001"}, schemadmin.ConstraintNone},
		{"empty state", &stateError{""}, schemadmin.ConstraintNone},
		{"error without a state", errors.New(`duplicate key value violates unique constraint "users_email_key"`), schemadmin.ConstraintNone},
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
	const pg = "pg_catalog"
	tests := []struct {
		schema, name, typType string
		typmod                int
		wantKind              schemadmin.Kind
		wantMax               int
	}{
		{pg, "bool", "b", -1, schemadmin.KindBool, 0},
		{pg, "int2", "b", -1, schemadmin.KindInt, 0},
		{pg, "int4", "b", -1, schemadmin.KindInt, 0},
		{pg, "int8", "b", -1, schemadmin.KindInt, 0},
		{pg, "float4", "b", -1, schemadmin.KindFloat, 0},
		{pg, "float8", "b", -1, schemadmin.KindFloat, 0},
		{pg, "numeric", "b", -1, schemadmin.KindDecimal, 0},
		{pg, "numeric", "b", 655366, schemadmin.KindDecimal, 0}, // numeric(10,2): the modifier is not a length
		{pg, "varchar", "b", 104, schemadmin.KindString, 100},
		{pg, "varchar", "b", 5, schemadmin.KindString, 1},
		{pg, "varchar", "b", -1, schemadmin.KindText, 0},
		{pg, "bpchar", "b", 6, schemadmin.KindString, 2},
		{pg, "bpchar", "b", -1, schemadmin.KindText, 0},
		{pg, "text", "b", -1, schemadmin.KindText, 0},
		{pg, "date", "b", -1, schemadmin.KindDate, 0},
		{pg, "time", "b", -1, schemadmin.KindTime, 0},
		{pg, "time", "b", 3, schemadmin.KindTime, 0},
		{pg, "timestamp", "b", -1, schemadmin.KindDateTime, 0},
		{pg, "timestamptz", "b", -1, schemadmin.KindDateTimeTZ, 0},
		{pg, "json", "b", -1, schemadmin.KindJSON, 0},
		{pg, "jsonb", "b", -1, schemadmin.KindJSON, 0},
		{pg, "uuid", "b", -1, schemadmin.KindUUID, 0},
		{pg, "bytea", "b", -1, schemadmin.KindBytes, 0},

		{"app", "user_role", "e", -1, schemadmin.KindEnum, 0},
		{"public", "citext", "b", -1, schemadmin.KindText, 0},
		{"extensions", "citext", "b", -1, schemadmin.KindText, 0},

		// Opaque types.
		{pg, "timetz", "b", -1, schemadmin.KindOther, 0},
		{pg, "interval", "b", -1, schemadmin.KindOther, 0},
		{pg, "inet", "b", -1, schemadmin.KindOther, 0},
		{pg, "money", "b", -1, schemadmin.KindOther, 0},
		{pg, "_text", "b", -1, schemadmin.KindOther, 0}, // text[]
		{pg, "_int4", "b", -1, schemadmin.KindOther, 0}, // integer[]
		{pg, "int4range", "r", -1, schemadmin.KindOther, 0},
		{pg, "oid", "b", -1, schemadmin.KindOther, 0},
		{pg, "name", "b", -1, schemadmin.KindOther, 0},
		{pg, "xml", "b", -1, schemadmin.KindOther, 0},
		{"app", "address", "c", -1, schemadmin.KindOther, 0}, // composite type

		// A user type that borrows a built-in name is not that built-in.
		{"app", "bool", "b", -1, schemadmin.KindOther, 0},
		{"app", "text", "b", -1, schemadmin.KindOther, 0},
	}
	for _, tt := range tests {
		kind, maxLength := kindOf(tt.schema, tt.name, tt.typType, tt.typmod)
		if kind != tt.wantKind || maxLength != tt.wantMax {
			t.Errorf("kindOf(%q, %q, %q, %d) = %v, %d; want %v, %d",
				tt.schema, tt.name, tt.typType, tt.typmod, kind, maxLength, tt.wantKind, tt.wantMax)
		}
	}
}

func TestParseAction(t *testing.T) {
	tests := []struct {
		code string
		want schemadmin.FKAction
	}{
		{"a", schemadmin.FKNoAction},
		{"r", schemadmin.FKRestrict},
		{"c", schemadmin.FKCascade},
		{"n", schemadmin.FKSetNull},
		{"d", schemadmin.FKSetDefault},
		{"", schemadmin.FKNoAction},
		{"?", schemadmin.FKNoAction},
	}
	for _, tt := range tests {
		if got := parseAction(tt.code); got != tt.want {
			t.Errorf("parseAction(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestSchemaNames(t *testing.T) {
	tests := []struct {
		name      string
		requested []string
		want      []string
		wantErr   bool
	}{
		{"default", nil, []string{"public"}, false},
		{"empty list", []string{}, []string{"public"}, false},
		{"order is kept", []string{"billing", "app"}, []string{"billing", "app"}, false},
		{"repetitions are dropped", []string{"app", "billing", "app"}, []string{"app", "billing"}, false},
		{"names are case-sensitive", []string{"App", "app"}, []string{"App", "app"}, false},
		{"empty name", []string{"app", ""}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := schemaNames(tt.requested)
			if (err != nil) != tt.wantErr {
				t.Fatalf("schemaNames(%q) error = %v, wantErr %v", tt.requested, err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("schemaNames(%q) = %q, want %q", tt.requested, got, tt.want)
			}
		})
	}
}
