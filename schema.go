package schemadmin

import (
	"fmt"
	"strings"
)

// Kind classifies a column by how its values are validated, edited and
// displayed. It is derived from the database type by the Dialect.
type Kind int

const (
	// KindOther is a type the dialect does not recognise. Values are
	// treated as opaque text.
	KindOther Kind = iota
	// KindString is character data with a declared length (varchar, char).
	KindString
	// KindText is unbounded character data.
	KindText
	KindInt
	KindFloat
	// KindDecimal is exact numeric data; values are never converted to a
	// binary float.
	KindDecimal
	KindBool
	KindDate
	KindTime
	// KindDateTime is a date and time without a time zone (a wall clock).
	KindDateTime
	// KindDateTimeTZ is an absolute point in time.
	KindDateTimeTZ
	KindJSON
	KindUUID
	KindBytes
	// KindEnum is a column restricted to Column.EnumValues.
	KindEnum

	kindCount int = iota
)

var kindNames = [kindCount]string{
	KindOther:      "other",
	KindString:     "string",
	KindText:       "text",
	KindInt:        "int",
	KindFloat:      "float",
	KindDecimal:    "decimal",
	KindBool:       "bool",
	KindDate:       "date",
	KindTime:       "time",
	KindDateTime:   "datetime",
	KindDateTimeTZ: "datetimetz",
	KindJSON:       "json",
	KindUUID:       "uuid",
	KindBytes:      "bytes",
	KindEnum:       "enum",
}

// Valid reports whether k is one of the defined kinds.
func (k Kind) Valid() bool { return k >= 0 && int(k) < kindCount }

func (k Kind) String() string {
	if !k.Valid() {
		return fmt.Sprintf("Kind(%d)", int(k))
	}
	return kindNames[k]
}

// FKAction is the referential action of a foreign key.
type FKAction int

const (
	FKNoAction FKAction = iota
	FKRestrict
	FKCascade
	FKSetNull
	FKSetDefault

	fkActionCount int = iota
)

var fkActionNames = [fkActionCount]string{
	FKNoAction:   "no action",
	FKRestrict:   "restrict",
	FKCascade:    "cascade",
	FKSetNull:    "set null",
	FKSetDefault: "set default",
}

// Valid reports whether a is one of the defined actions.
func (a FKAction) Valid() bool { return a >= 0 && int(a) < fkActionCount }

func (a FKAction) String() string {
	if !a.Valid() {
		return fmt.Sprintf("FKAction(%d)", int(a))
	}
	return fkActionNames[a]
}

// Schema is the set of tables read from a database.
type Schema struct {
	Tables []*Table
}

// Table returns the table with the given schema and name, or nil.
func (s *Schema) Table(schema, name string) *Table {
	for _, t := range s.Tables {
		if t.Schema == schema && t.Name == name {
			return t
		}
	}
	return nil
}

// Table describes one database table or view.
type Table struct {
	// Schema is the namespace the table lives in, or "" for databases
	// without schemas.
	Schema string
	Name   string
	// View reports that this relation is a view. Views are listed and can
	// be read, but the admin never writes to them: inserts, updates and
	// deletes are refused whatever the Authorizer says.
	View bool
	// Columns are in the table's declared order.
	Columns []*Column
	// PrimaryKey lists the key columns in key order. It is empty when the
	// table has no primary key.
	PrimaryKey []string
	// ForeignKeys are the constraints declared on this table, in the order
	// they were declared. Views have none.
	ForeignKeys []*ForeignKey
}

// Column returns the column with the given name, or nil.
func (t *Table) Column(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Column describes one table column.
type Column struct {
	Name string
	// DBType is the type as the database reports it, for display only.
	DBType   string
	Kind     Kind
	Nullable bool
	// HasDefault reports whether an INSERT may omit the column and let the
	// database supply a value: an explicit DEFAULT, or AutoIncrement.
	HasDefault bool
	// Default is the default expression as the database reports it, for
	// display only. It is "" when there is no expression, which includes
	// keys the database assigns without one (identity columns, the SQLite
	// rowid).
	Default string
	// AutoIncrement reports that the database assigns a key when the column
	// is omitted (serial, identity by default, SQLite rowid alias). Such a
	// column may still be written explicitly.
	AutoIncrement bool
	// Generated reports that the database always computes the value
	// (computed columns, identity always). Such a column is never written.
	Generated bool
	// MaxLength is the character limit the database enforces, or 0.
	MaxLength int
	// EnumValues are the permitted values of a KindEnum column, in
	// declaration order.
	EnumValues []string
}

// ForeignKey is a foreign-key constraint declared on a table.
type ForeignKey struct {
	// Name is the constraint name, or "" when the database has none.
	Name string
	// Columns are the referencing columns of the declaring table.
	Columns []string
	// RefSchema and RefTable identify the referenced table. It may be
	// absent from the Schema when it lies outside what was introspected.
	RefSchema string
	RefTable  string
	// RefColumns are the referenced columns, positionally matching Columns.
	RefColumns []string
	OnDelete   FKAction
	OnUpdate   FKAction
}

// Validate checks that the schema is internally consistent. Everything that
// builds SQL from a Schema relies on these invariants, so a Schema produced
// by a Dialect is validated before use.
func (s *Schema) Validate() error {
	type tableID struct{ schema, name string }
	seen := make(map[tableID]bool, len(s.Tables))
	for i, t := range s.Tables {
		if t == nil {
			return fmt.Errorf("schemadmin: invalid schema: table %d is nil", i)
		}
		if err := validateIdent(t.Name); err != nil {
			return fmt.Errorf("schemadmin: invalid schema: table name %q: %w", t.Name, err)
		}
		if t.Schema != "" {
			if err := validateIdent(t.Schema); err != nil {
				return fmt.Errorf("schemadmin: invalid schema: schema name %q: %w", t.Schema, err)
			}
		}
		id := tableID{t.Schema, t.Name}
		if seen[id] {
			return fmt.Errorf("schemadmin: invalid schema: duplicate table %s", tableLabel(t.Schema, t.Name))
		}
		seen[id] = true
	}
	for _, t := range s.Tables {
		if err := s.validateTable(t); err != nil {
			return fmt.Errorf("schemadmin: invalid schema: table %s: %w", tableLabel(t.Schema, t.Name), err)
		}
	}
	return nil
}

func (s *Schema) validateTable(t *Table) error {
	cols := make(map[string]bool, len(t.Columns))
	for i, c := range t.Columns {
		if c == nil {
			return fmt.Errorf("column %d is nil", i)
		}
		if err := validateIdent(c.Name); err != nil {
			return fmt.Errorf("column name %q: %w", c.Name, err)
		}
		if cols[c.Name] {
			return fmt.Errorf("duplicate column %q", c.Name)
		}
		cols[c.Name] = true
		if err := validateColumn(c); err != nil {
			return fmt.Errorf("column %q: %w", c.Name, err)
		}
	}

	if err := validateColumnList(t.PrimaryKey, cols); err != nil {
		return fmt.Errorf("primary key: %w", err)
	}

	for i, fk := range t.ForeignKeys {
		if fk == nil {
			return fmt.Errorf("foreign key %d is nil", i)
		}
		if err := s.validateForeignKey(fk, cols); err != nil {
			return fmt.Errorf("foreign key %d: %w", i, err)
		}
	}
	return nil
}

func validateColumn(c *Column) error {
	if !c.Kind.Valid() {
		return fmt.Errorf("unknown kind %d", int(c.Kind))
	}
	if c.Kind == KindEnum && len(c.EnumValues) == 0 {
		return fmt.Errorf("enum column has no values")
	}
	if c.Kind != KindEnum && len(c.EnumValues) != 0 {
		return fmt.Errorf("%s column has enum values", c.Kind)
	}
	if c.AutoIncrement && !c.HasDefault {
		return fmt.Errorf("auto-increment column must report HasDefault")
	}
	if c.AutoIncrement && c.Generated {
		return fmt.Errorf("column is both auto-increment and generated")
	}
	if c.MaxLength < 0 {
		return fmt.Errorf("negative max length %d", c.MaxLength)
	}
	return nil
}

func (s *Schema) validateForeignKey(fk *ForeignKey, cols map[string]bool) error {
	if len(fk.Columns) == 0 {
		return fmt.Errorf("no columns")
	}
	if err := validateColumnList(fk.Columns, cols); err != nil {
		return err
	}
	if len(fk.RefColumns) != len(fk.Columns) {
		return fmt.Errorf("%d columns reference %d columns", len(fk.Columns), len(fk.RefColumns))
	}
	if err := validateIdent(fk.RefTable); err != nil {
		return fmt.Errorf("referenced table name %q: %w", fk.RefTable, err)
	}
	if !fk.OnDelete.Valid() || !fk.OnUpdate.Valid() {
		return fmt.Errorf("unknown referential action")
	}

	// The referenced table may lie outside the introspected set. Its
	// columns can only be checked when it is present.
	ref := s.Table(fk.RefSchema, fk.RefTable)
	refCols := make(map[string]bool)
	if ref != nil {
		for _, c := range ref.Columns {
			if c != nil {
				refCols[c.Name] = true
			}
		}
	}
	seen := make(map[string]bool, len(fk.RefColumns))
	for _, name := range fk.RefColumns {
		if err := validateIdent(name); err != nil {
			return fmt.Errorf("referenced column name %q: %w", name, err)
		}
		if seen[name] {
			return fmt.Errorf("referenced column %q listed twice", name)
		}
		seen[name] = true
		if ref != nil && !refCols[name] {
			return fmt.Errorf("referenced column %q does not exist in %s", name, tableLabel(fk.RefSchema, fk.RefTable))
		}
	}
	return nil
}

// validateColumnList checks that every name exists in cols and appears once.
func validateColumnList(names []string, cols map[string]bool) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !cols[name] {
			return fmt.Errorf("column %q does not exist", name)
		}
		if seen[name] {
			return fmt.Errorf("column %q listed twice", name)
		}
		seen[name] = true
	}
	return nil
}

// validateIdent rejects names no database can hold and no dialect can quote.
func validateIdent(name string) error {
	if name == "" {
		return fmt.Errorf("empty identifier")
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("identifier contains a NUL byte")
	}
	return nil
}

func tableLabel(schema, name string) string {
	if schema == "" {
		return fmt.Sprintf("%q", name)
	}
	return fmt.Sprintf("%q.%q", schema, name)
}
