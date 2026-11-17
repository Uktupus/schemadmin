package sqlite

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/Uktupus/schemadmin"
)

func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"3.37.0", true},
		{"3.37", true},
		{"3.51.0", true},
		{"3.100.1", true},
		{"4.0.0", true},
		{"3.36.9", false},
		{"3.8.11", false},
		{"2.99.0", false},
		{"3", false},
		{"", false},
		{"three.forty", false},
		{"3.x.0", false},
	}
	for _, tt := range tests {
		if got := versionAtLeast(tt.version, 3, 37); got != tt.want {
			t.Errorf("versionAtLeast(%q, 3, 37) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestParseAction(t *testing.T) {
	tests := []struct {
		in   string
		want schemadmin.FKAction
	}{
		{"NO ACTION", schemadmin.FKNoAction},
		{"RESTRICT", schemadmin.FKRestrict},
		{"CASCADE", schemadmin.FKCascade},
		{"SET NULL", schemadmin.FKSetNull},
		{"SET DEFAULT", schemadmin.FKSetDefault},
		{"cascade", schemadmin.FKCascade},
		{" set null ", schemadmin.FKSetNull},
		{"", schemadmin.FKNoAction},
		{"NONE", schemadmin.FKNoAction},
	}
	for _, tt := range tests {
		if got := parseAction(tt.in); got != tt.want {
			t.Errorf("parseAction(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestResolveForeignKey(t *testing.T) {
	col := func(name string) *schemadmin.Column { return &schemadmin.Column{Name: name} }
	parent := &schemadmin.Table{
		Name:       "Parents",
		Columns:    []*schemadmin.Column{col("ID"), col("code")},
		PrimaryKey: []string{"ID"},
	}
	pair := &schemadmin.Table{
		Name:       "pairs",
		Columns:    []*schemadmin.Column{col("a"), col("b")},
		PrimaryKey: []string{"b", "a"},
	}
	keyless := &schemadmin.Table{Name: "keyless", Columns: []*schemadmin.Column{col("x")}}
	child := &schemadmin.Table{
		Name:    "children",
		Columns: []*schemadmin.Column{col("parent_id"), col("A"), col("B")},
	}
	schema := &schemadmin.Schema{Tables: []*schemadmin.Table{parent, pair, keyless, child}}

	named := func(names ...string) []sql.NullString {
		out := make([]sql.NullString, len(names))
		for i, n := range names {
			out[i] = sql.NullString{String: n, Valid: true}
		}
		return out
	}
	implied := func(n int) []sql.NullString { return make([]sql.NullString, n) }

	tests := []struct {
		name string
		raw  foreignKeyInfo
		want *schemadmin.ForeignKey // nil means the key is dropped
		// wantReason is a substring of the reason given for dropping it.
		wantReason string
	}{
		{
			name: "explicit column",
			raw:  foreignKeyInfo{refTable: "Parents", from: []string{"parent_id"}, to: named("ID"), onDelete: "CASCADE", onUpdate: "NO ACTION"},
			want: &schemadmin.ForeignKey{Columns: []string{"parent_id"}, RefTable: "Parents", RefColumns: []string{"ID"}, OnDelete: schemadmin.FKCascade},
		},
		{
			name: "names are corrected to their real case",
			raw:  foreignKeyInfo{refTable: "PARENTS", from: []string{"PARENT_ID"}, to: named("id"), onDelete: "SET NULL", onUpdate: "CASCADE"},
			want: &schemadmin.ForeignKey{Columns: []string{"parent_id"}, RefTable: "Parents", RefColumns: []string{"ID"}, OnDelete: schemadmin.FKSetNull, OnUpdate: schemadmin.FKCascade},
		},
		{
			name: "implied primary key",
			raw:  foreignKeyInfo{refTable: "parents", from: []string{"parent_id"}, to: implied(1)},
			want: &schemadmin.ForeignKey{Columns: []string{"parent_id"}, RefTable: "Parents", RefColumns: []string{"ID"}},
		},
		{
			name: "implied composite primary key follows key order",
			raw:  foreignKeyInfo{refTable: "pairs", from: []string{"a", "b"}, to: implied(2)},
			want: &schemadmin.ForeignKey{Columns: []string{"A", "B"}, RefTable: "pairs", RefColumns: []string{"b", "a"}},
		},
		{
			name: "explicit composite",
			raw:  foreignKeyInfo{refTable: "pairs", from: []string{"A", "B"}, to: named("a", "b")},
			want: &schemadmin.ForeignKey{Columns: []string{"A", "B"}, RefTable: "pairs", RefColumns: []string{"a", "b"}},
		},
		{
			name: "parent outside the schema is kept as written",
			raw:  foreignKeyInfo{refTable: "elsewhere", from: []string{"parent_id"}, to: named("id")},
			want: &schemadmin.ForeignKey{Columns: []string{"parent_id"}, RefTable: "elsewhere", RefColumns: []string{"id"}},
		},

		{
			name:       "implied key of a missing parent",
			raw:        foreignKeyInfo{refTable: "elsewhere", from: []string{"parent_id"}, to: implied(1)},
			wantReason: `referenced table "elsewhere" does not exist`,
		},
		{
			name:       "implied key of a parent without a primary key",
			raw:        foreignKeyInfo{refTable: "keyless", from: []string{"parent_id"}, to: implied(1)},
			wantReason: `primary key of "keyless", which has none`,
		},
		{
			name:       "implied key of a different width",
			raw:        foreignKeyInfo{refTable: "pairs", from: []string{"parent_id"}, to: implied(1)},
			wantReason: `primary key of "pairs", which has 2 columns where the constraint has 1`,
		},
		{
			name:       "missing parent column",
			raw:        foreignKeyInfo{refTable: "parents", from: []string{"parent_id"}, to: named("nope")},
			wantReason: `referenced column "nope" does not exist in "Parents"`,
		},
		{
			name:       "missing child column",
			raw:        foreignKeyInfo{refTable: "parents", from: []string{"nope"}, to: named("ID")},
			wantReason: `column "nope" does not exist in the table`,
		},
		{
			name:       "repeated child column",
			raw:        foreignKeyInfo{refTable: "pairs", from: []string{"A", "a"}, to: named("a", "b")},
			wantReason: `lists a column twice`,
		},
		{
			name:       "repeated parent column",
			raw:        foreignKeyInfo{refTable: "pairs", from: []string{"A", "B"}, to: named("a", "A")},
			wantReason: `lists a referenced column twice`,
		},
		{
			name:       "empty parent name",
			raw:        foreignKeyInfo{refTable: "", from: []string{"parent_id"}, to: named("id")},
			wantReason: `referenced table name is empty`,
		},
		{
			name:       "empty parent column outside the schema",
			raw:        foreignKeyInfo{refTable: "elsewhere", from: []string{"parent_id"}, to: named("")},
			wantReason: `referenced column name is empty`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := resolveForeignKey(schema, child, &tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveForeignKey() = %+v, want %+v", got, tt.want)
			}
			switch {
			case tt.want != nil && reason != "":
				t.Errorf("a resolved key came with the reason %q", reason)
			case tt.want == nil && (reason == "" || !strings.Contains(reason, tt.wantReason)):
				t.Errorf("reason = %q, want it to contain %q", reason, tt.wantReason)
			}
		})
	}
}
