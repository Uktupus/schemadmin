package sqlite

import (
	"errors"
	"fmt"
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
		{"order items", `"order items"`},
		{`we"ird`, `"we""ird"`},
		{`"`, `""""`},
		{`""`, `""""""`},
		{`x"; DROP TABLE users; --`, `"x""; DROP TABLE users; --"`},
		{"sel'ect", `"sel'ect"`},
		{"tab\tname", "\"tab\tname\""},
		{"ünïcode", `"ünïcode"`},
		{"", `""`},
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

func TestSQLFragments(t *testing.T) {
	d := New()
	if got := d.Name(); got != "sqlite" {
		t.Errorf("Name() = %q, want %q", got, "sqlite")
	}
	for _, n := range []int{1, 2, 17} {
		if got := d.Placeholder(n); got != "?" {
			t.Errorf("Placeholder(%d) = %q, want %q", n, got, "?")
		}
	}
	col := &schemadmin.Column{Name: "created_at", Kind: schemadmin.KindDateTime}
	if got, want := d.SelectExpr(`"created_at"`, col), `+"created_at"`; got != want {
		t.Errorf("SelectExpr = %s, want %s", got, want)
	}
	if got, want := d.ContainsExpr(`"name"`, "?"), `CAST("name" AS TEXT) LIKE ? ESCAPE '\'`; got != want {
		t.Errorf("ContainsExpr = %s, want %s", got, want)
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want schemadmin.ConstraintKind
	}{
		{"nil", nil, schemadmin.ConstraintNone},
		{"unique", errors.New("UNIQUE constraint failed: users.email"), schemadmin.ConstraintUnique},
		{"unique with driver decoration", errors.New("constraint failed: UNIQUE constraint failed: users.email (2067)"), schemadmin.ConstraintUnique},
		{"legacy primary key wording", errors.New("PRIMARY KEY must be unique"), schemadmin.ConstraintUnique},
		{"foreign key", errors.New("FOREIGN KEY constraint failed"), schemadmin.ConstraintForeignKey},
		{"not null", errors.New("NOT NULL constraint failed: users.email"), schemadmin.ConstraintNotNull},
		{"check", errors.New("CHECK constraint failed: role"), schemadmin.ConstraintCheck},
		{"wrapped", fmt.Errorf("insert user: %w", errors.New("NOT NULL constraint failed: users.email")), schemadmin.ConstraintNotNull},
		{"syntax error", errors.New(`near "FROM": syntax error`), schemadmin.ConstraintNone},
		{"busy", errors.New("database is locked (5) (SQLITE_BUSY)"), schemadmin.ConstraintNone},
		{"missing table", errors.New("no such table: users"), schemadmin.ConstraintNone},
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
