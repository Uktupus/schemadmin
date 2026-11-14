package schemadmin

import (
	"strings"
	"testing"
)

// validSchema returns a small, consistent schema that each test case then
// breaks in one specific way.
func validSchema() *Schema {
	return &Schema{Tables: []*Table{
		{
			Name: "users",
			Columns: []*Column{
				{Name: "id", Kind: KindInt, HasDefault: true, AutoIncrement: true},
				{Name: "email", Kind: KindString, MaxLength: 255},
				{Name: "role", Kind: KindEnum, EnumValues: []string{"admin", "member"}},
			},
			PrimaryKey: []string{"id"},
		},
		{
			Name: "memberships",
			Columns: []*Column{
				{Name: "org", Kind: KindText},
				{Name: "user_id", Kind: KindInt},
				{Name: "invited_by", Kind: KindInt, Nullable: true},
			},
			PrimaryKey: []string{"org", "user_id"},
			ForeignKeys: []*ForeignKey{
				{Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"}, OnDelete: FKCascade},
				{Columns: []string{"invited_by"}, RefTable: "users", RefColumns: []string{"id"}, OnDelete: FKSetNull},
			},
		},
	}}
}

func TestSchemaValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(s *Schema)
		wantErr string // substring of the error; "" means valid
	}{
		{"valid", func(*Schema) {}, ""},
		{"empty schema", func(s *Schema) { s.Tables = nil }, ""},
		{"table without columns", func(s *Schema) {
			s.Tables = append(s.Tables, &Table{Name: "empty"})
		}, ""},
		{"same table name in two schemas", func(s *Schema) {
			s.Tables[0].Schema = "a"
			s.Tables = append(s.Tables, &Table{Schema: "b", Name: "users"})
			s.Tables[1].ForeignKeys = nil
		}, ""},
		{"foreign key to a table outside the schema", func(s *Schema) {
			s.Tables[1].ForeignKeys[0].RefTable = "elsewhere"
			s.Tables[1].ForeignKeys[0].RefColumns = []string{"anything"}
		}, ""},

		{"nil table", func(s *Schema) { s.Tables[0] = nil }, "table 0 is nil"},
		{"empty table name", func(s *Schema) { s.Tables[0].Name = "" }, "empty identifier"},
		{"NUL in table name", func(s *Schema) { s.Tables[0].Name = "us\x00ers" }, "NUL byte"},
		{"NUL in schema name", func(s *Schema) { s.Tables[0].Schema = "a\x00" }, "NUL byte"},
		{"duplicate table", func(s *Schema) { s.Tables[1].Name = "users" }, "duplicate table"},
		{"nil column", func(s *Schema) { s.Tables[0].Columns[1] = nil }, "column 1 is nil"},
		{"empty column name", func(s *Schema) { s.Tables[0].Columns[1].Name = "" }, "empty identifier"},
		{"NUL in column name", func(s *Schema) { s.Tables[0].Columns[1].Name = "e\x00" }, "NUL byte"},
		{"duplicate column", func(s *Schema) { s.Tables[0].Columns[1].Name = "id" }, `duplicate column "id"`},
		{"unknown kind", func(s *Schema) { s.Tables[0].Columns[1].Kind = Kind(999) }, "unknown kind"},
		{"negative kind", func(s *Schema) { s.Tables[0].Columns[1].Kind = Kind(-1) }, "unknown kind"},
		{"enum without values", func(s *Schema) { s.Tables[0].Columns[2].EnumValues = nil }, "no values"},
		{"values on a non-enum", func(s *Schema) {
			s.Tables[0].Columns[1].EnumValues = []string{"x"}
		}, "has enum values"},
		{"auto-increment without default", func(s *Schema) {
			s.Tables[0].Columns[0].HasDefault = false
		}, "must report HasDefault"},
		{"auto-increment and generated", func(s *Schema) {
			s.Tables[0].Columns[0].Generated = true
		}, "both auto-increment and generated"},
		{"negative max length", func(s *Schema) { s.Tables[0].Columns[1].MaxLength = -1 }, "negative max length"},
		{"primary key names a missing column", func(s *Schema) {
			s.Tables[0].PrimaryKey = []string{"nope"}
		}, `primary key: column "nope" does not exist`},
		{"primary key repeats a column", func(s *Schema) {
			s.Tables[1].PrimaryKey = []string{"org", "org"}
		}, `primary key: column "org" listed twice`},
		{"nil foreign key", func(s *Schema) { s.Tables[1].ForeignKeys[0] = nil }, "foreign key 0 is nil"},
		{"foreign key without columns", func(s *Schema) {
			s.Tables[1].ForeignKeys[0].Columns = nil
		}, "no columns"},
		{"foreign key from a missing column", func(s *Schema) {
			s.Tables[1].ForeignKeys[0].Columns = []string{"nope"}
		}, `column "nope" does not exist`},
		{"foreign key repeats a column", func(s *Schema) {
			fk := s.Tables[1].ForeignKeys[0]
			fk.Columns = []string{"user_id", "user_id"}
			fk.RefColumns = []string{"id", "email"}
		}, `column "user_id" listed twice`},
		{"foreign key width mismatch", func(s *Schema) {
			s.Tables[1].ForeignKeys[0].RefColumns = []string{"id", "email"}
		}, "1 columns reference 2 columns"},
		{"foreign key without a target", func(s *Schema) {
			s.Tables[1].ForeignKeys[0].RefTable = ""
		}, "referenced table name"},
		{"foreign key to a missing column of a known table", func(s *Schema) {
			s.Tables[1].ForeignKeys[0].RefColumns = []string{"nope"}
		}, `referenced column "nope" does not exist`},
		{"foreign key repeats a referenced column", func(s *Schema) {
			fk := s.Tables[1].ForeignKeys[0]
			fk.Columns = []string{"user_id", "invited_by"}
			fk.RefColumns = []string{"id", "id"}
		}, `referenced column "id" listed twice`},
		{"foreign key with an empty referenced column", func(s *Schema) {
			s.Tables[1].ForeignKeys[0].RefColumns = []string{""}
		}, "referenced column name"},
		{"unknown referential action", func(s *Schema) {
			s.Tables[1].ForeignKeys[0].OnDelete = FKAction(42)
		}, "unknown referential action"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSchema()
			tt.mutate(s)
			err := s.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Validate() = %q, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSchemaLookups(t *testing.T) {
	s := validSchema()
	s.Tables[0].Schema = "app"

	if got := s.Table("app", "users"); got != s.Tables[0] {
		t.Errorf(`Table("app", "users") = %v, want the users table`, got)
	}
	if got := s.Table("", "users"); got != nil {
		t.Errorf(`Table("", "users") = %v, want nil: the schema must match`, got)
	}
	if got := s.Table("", "memberships"); got != s.Tables[1] {
		t.Errorf(`Table("", "memberships") = %v, want the memberships table`, got)
	}
	if got := s.Tables[1].Column("user_id"); got != s.Tables[1].Columns[1] {
		t.Errorf(`Column("user_id") = %v, want the user_id column`, got)
	}
	if got := s.Tables[1].Column("USER_ID"); got != nil {
		t.Errorf(`Column("USER_ID") = %v, want nil: lookups are exact`, got)
	}
}

func TestKindString(t *testing.T) {
	seen := make(map[string]Kind)
	for k := Kind(0); k.Valid(); k++ {
		name := k.String()
		if name == "" || strings.HasPrefix(name, "Kind(") {
			t.Errorf("Kind(%d) has no name", int(k))
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("Kind(%d) and Kind(%d) share the name %q", int(prev), int(k), name)
		}
		seen[name] = k
	}
	if len(seen) != kindCount {
		t.Errorf("named %d kinds, want %d", len(seen), kindCount)
	}
	if got := Kind(-1).String(); got != "Kind(-1)" {
		t.Errorf("Kind(-1).String() = %q", got)
	}
	if got := Kind(kindCount).String(); !strings.HasPrefix(got, "Kind(") {
		t.Errorf("out-of-range kind printed as %q", got)
	}
}

func TestFKActionString(t *testing.T) {
	tests := []struct {
		action FKAction
		want   string
	}{
		{FKNoAction, "no action"},
		{FKRestrict, "restrict"},
		{FKCascade, "cascade"},
		{FKSetNull, "set null"},
		{FKSetDefault, "set default"},
		{FKAction(-1), "FKAction(-1)"},
		{FKAction(99), "FKAction(99)"},
	}
	for _, tt := range tests {
		if got := tt.action.String(); got != tt.want {
			t.Errorf("FKAction(%d).String() = %q, want %q", int(tt.action), got, tt.want)
		}
	}
}

func TestConstraintKindString(t *testing.T) {
	tests := []struct {
		kind ConstraintKind
		want string
	}{
		{ConstraintNone, "none"},
		{ConstraintUnique, "unique"},
		{ConstraintForeignKey, "foreign key"},
		{ConstraintNotNull, "not null"},
		{ConstraintCheck, "check"},
		{ConstraintKind(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("ConstraintKind(%d).String() = %q, want %q", int(tt.kind), got, tt.want)
		}
	}
}
