package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Uktupus/schemadmin"
)

// The helpers and check* functions in this file are shared by the tests of
// every dialect. A check* function is the body of a test that must hold for
// any database; the per-dialect files supply the database.

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// mustIntrospect introspects db. Unless the test supplies a logger of its
// own, warnings are discarded rather than written to the test output.
func mustIntrospect(t *testing.T, d schemadmin.Dialect, db *sql.DB, opts schemadmin.IntrospectOptions) *schemadmin.Schema {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	schema, err := d.Introspect(context.Background(), db, opts)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	return schema
}

// mustTable returns the table with the given name, whatever its schema.
func mustTable(t *testing.T, schema *schemadmin.Schema, name string) *schemadmin.Table {
	t.Helper()
	for _, table := range schema.Tables {
		if table.Name == name {
			return table
		}
	}
	t.Fatalf("table %q was not introspected", name)
	return nil
}

// tableRef returns the quoted, schema-qualified name of a table.
func tableRef(d schemadmin.Dialect, table *schemadmin.Table) string {
	if table.Schema == "" {
		return d.QuoteIdent(table.Name)
	}
	return d.QuoteIdent(table.Schema) + "." + d.QuoteIdent(table.Name)
}

// insertStatement builds an INSERT that supplies a plausible value for each
// of the given columns and nothing else.
func insertStatement(d schemadmin.Dialect, table *schemadmin.Table, columns []*schemadmin.Column) (string, []any) {
	if len(columns) == 0 {
		if d.Name() == "mysql" {
			return "INSERT INTO " + tableRef(d, table) + " () VALUES ()", nil
		}
		return "INSERT INTO " + tableRef(d, table) + " DEFAULT VALUES", nil
	}
	var names, marks []string
	var args []any
	for i, c := range columns {
		names = append(names, d.QuoteIdent(c.Name))
		marks = append(marks, d.Placeholder(i+1))
		args = append(args, sampleValue(c))
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		tableRef(d, table), strings.Join(names, ", "), strings.Join(marks, ", ")), args
}

func sampleValue(c *schemadmin.Column) any {
	switch c.Kind {
	case schemadmin.KindInt:
		return 1
	case schemadmin.KindFloat:
		return 1.5
	case schemadmin.KindDecimal:
		return "1.50"
	case schemadmin.KindBool:
		return true
	case schemadmin.KindDate:
		return "2024-01-02"
	case schemadmin.KindTime:
		return "03:04:05"
	case schemadmin.KindDateTime, schemadmin.KindDateTimeTZ:
		return "2024-01-02 03:04:05"
	case schemadmin.KindJSON:
		return "{}"
	case schemadmin.KindUUID:
		return "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	case schemadmin.KindBytes:
		return []byte{1, 2, 3}
	case schemadmin.KindEnum:
		return c.EnumValues[0]
	}
	return "x"
}

// scanValues runs a single-row query and returns its columns as the driver
// delivers them.
func scanValues(t *testing.T, db *sql.DB, n int, query string, args ...any) []any {
	t.Helper()
	values := make([]any, n)
	targets := make([]any, n)
	for i := range values {
		targets[i] = &values[i]
	}
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(targets...); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	return values
}

// logRecorder collects what a dialect logs during introspection.
type logRecorder struct {
	buf bytes.Buffer
}

func (r *logRecorder) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&r.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// records returns the logged records as decoded JSON objects.
func (r *logRecorder) records(t *testing.T) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(r.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		delete(record, "time")
		records = append(records, record)
	}
	return records
}

// checkGolden introspects db and compares the result with a golden file.
func checkGolden(t *testing.T, d schemadmin.Dialect, db *sql.DB, opts schemadmin.IntrospectOptions, golden string) {
	t.Helper()
	got := dumpSchema(mustIntrospect(t, d, db, opts))
	if diff := diffLines(readTestdata(t, golden), got); diff != "" {
		t.Errorf("introspected schema differs from testdata/%s:\n%s", golden, diff)
	}
}

// nullProbe describes one attempt to store NULL in a column of a table
// named t.
type nullProbe struct {
	name   string
	ddl    string
	column string
	insert string
}

// checkNullability checks Column.Nullable against what the database really
// does: a column is nullable exactly when a NULL can be stored in it. Each
// probe runs in a database of its own, obtained from open.
func checkNullability(t *testing.T, d schemadmin.Dialect, open func(t *testing.T) *sql.DB, probes []nullProbe) {
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			db := open(t)
			mustExec(t, db, probe.ddl)
			table := mustTable(t, mustIntrospect(t, d, db, schemadmin.IntrospectOptions{}), "t")
			column := table.Column(probe.column)
			if column == nil {
				t.Fatalf("column %q was not introspected", probe.column)
			}

			_, insertErr := db.Exec(probe.insert)
			stored := countRows(t, db, "SELECT count(*) FROM "+tableRef(d, table)+" WHERE "+d.QuoteIdent(probe.column)+" IS NULL") > 0
			if column.Nullable != stored {
				t.Errorf("Nullable = %v, but the database stored a NULL: %v (insert error: %v)",
					column.Nullable, stored, insertErr)
			}
		})
	}
}

// checkRequiredColumns checks the model's claims about which columns an
// INSERT must supply. For every table, giving values for only the columns
// that are NOT NULL, have no default and are not generated must succeed and
// must leave no NULL in a non-nullable column; writing a generated column
// must be refused, and not because of some constraint.
//
// The database must not enforce foreign keys, since the values are samples.
func checkRequiredColumns(t *testing.T, d schemadmin.Dialect, db *sql.DB, schema *schemadmin.Schema) {
	for _, table := range schema.Tables {
		t.Run(qualified(table.Schema, table.Name), func(t *testing.T) {
			if table.View {
				t.Skip("views are read-only")
			}
			var required, generated []*schemadmin.Column
			for _, c := range table.Columns {
				switch {
				case c.Generated:
					generated = append(generated, c)
				case !c.Nullable && !c.HasDefault:
					required = append(required, c)
				}
			}

			query, args := insertStatement(d, table, required)
			if _, err := db.Exec(query, args...); err != nil {
				t.Fatalf("insert supplying only the required columns failed: %v\n%s", err, query)
			}
			for _, c := range table.Columns {
				if c.Nullable {
					continue
				}
				nulls := countRows(t, db, "SELECT count(*) FROM "+tableRef(d, table)+" WHERE "+d.QuoteIdent(c.Name)+" IS NULL")
				if nulls != 0 {
					t.Errorf("column %q is reported NOT NULL but holds NULL after the insert", c.Name)
				}
			}

			for _, c := range generated {
				query, args := insertStatement(d, table, append(slices.Clone(required), c))
				_, err := db.Exec(query, args...)
				switch {
				case err == nil:
					t.Errorf("column %q is reported generated but accepted a written value", c.Name)
				case d.ClassifyError(err) != schemadmin.ConstraintNone:
					t.Errorf("column %q: the write failed on a constraint, not because the column is generated: %v", c.Name, err)
				}
			}
		})
	}
}

// hostileColumnNames are identifiers containing quotes, SQL and markup.
// The first is the primary key and the last references it; the rest are
// text columns.
var hostileColumnNames = []string{
	"id",
	"col with space",
	`quote"d`,
	`""`,
	"semi;colon",
	"<script>alert(1)</script>",
	"select",
	"single'quote",
	"back`tick",
	"brack]et",
	"ünïcödé",
	"new\nline",
	"percent%_under",
	`back\slash`,
	"?",
	"$1",
	`parent "ref"`,
}

const (
	hostileTableName  = `we"ird "table" <b>; DROP TABLE canary; --`
	hostileSchemaName = `sch"ema; DROP SCHEMA public; --`
)

// checkHostileIdentifiers creates a table whose name and column names are
// hostile, next to a table named canary, and checks that the names survive
// introspection unchanged and that the dialect's quoting makes every one of
// them usable. schemaName is "" for databases without schemas.
func checkHostileIdentifiers(t *testing.T, d schemadmin.Dialect, db *sql.DB, schemaName string) {
	qualify := func(table string) string {
		if schemaName == "" {
			return d.QuoteIdent(table)
		}
		return d.QuoteIdent(schemaName) + "." + d.QuoteIdent(table)
	}
	opts := schemadmin.IntrospectOptions{}
	if schemaName != "" {
		switch d.Name() {
		case "mysql":
			mustExec(t, db, "CREATE DATABASE IF NOT EXISTS "+d.QuoteIdent(schemaName))
		default:
			mustExec(t, db, "CREATE SCHEMA "+d.QuoteIdent(schemaName))
		}
		opts.Schemas = []string{schemaName}
	}
	mustExec(t, db, "CREATE TABLE "+qualify("canary")+" (id INTEGER PRIMARY KEY)")

	last := len(hostileColumnNames) - 1
	var defs []string
	for i, name := range hostileColumnNames {
		switch i {
		case 0:
			defs = append(defs, d.QuoteIdent(name)+" INTEGER PRIMARY KEY")
		case last:
			defs = append(defs, d.QuoteIdent(name)+" INTEGER REFERENCES "+qualify(hostileTableName)+" ("+d.QuoteIdent(hostileColumnNames[0])+")")
		default:
			defs = append(defs, d.QuoteIdent(name)+" TEXT")
		}
	}
	mustExec(t, db, "CREATE TABLE "+qualify(hostileTableName)+" ("+strings.Join(defs, ", ")+")")

	schema := mustIntrospect(t, d, db, opts)
	table := mustTable(t, schema, hostileTableName)
	mustTable(t, schema, "canary")
	if table.Schema != schemaName {
		t.Errorf("table schema = %q, want %q", table.Schema, schemaName)
	}

	var gotNames []string
	for _, c := range table.Columns {
		gotNames = append(gotNames, c.Name)
	}
	if !slices.Equal(gotNames, hostileColumnNames) {
		t.Fatalf("column names\n got %q\nwant %q", gotNames, hostileColumnNames)
	}
	if len(table.ForeignKeys) != 1 {
		t.Fatalf("got %d foreign keys, want 1", len(table.ForeignKeys))
	}
	fk := table.ForeignKeys[0]
	if !slices.Equal(fk.Columns, []string{hostileColumnNames[last]}) ||
		fk.RefSchema != schemaName || fk.RefTable != hostileTableName ||
		!slices.Equal(fk.RefColumns, []string{hostileColumnNames[0]}) {
		t.Errorf("foreign key = %+v", fk)
	}

	// Write and read every column through quoted identifiers only.
	query, args := insertStatement(d, table, table.Columns)
	mustExec(t, db, query, args...)
	for _, c := range table.Columns {
		query := fmt.Sprintf("SELECT %s FROM %s WHERE %s",
			d.SelectExpr(d.QuoteIdent(c.Name), c),
			tableRef(d, table),
			d.ContainsExpr(d.QuoteIdent(c.Name), d.Placeholder(1)))
		var got any
		if err := db.QueryRow(query, "%").Scan(&got); err != nil {
			t.Errorf("column %q: %v\n%s", c.Name, err, query)
			continue
		}
		want := any("x")
		if c.Kind == schemadmin.KindInt {
			want = int64(1)
		}
		if b, ok := got.([]byte); ok {
			got = string(b)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("column %q: read %#v, want %#v", c.Name, got, want)
		}
	}

	if n := countRows(t, db, "SELECT count(*) FROM "+qualify("canary")); n != 0 {
		t.Errorf("the canary table holds %d rows, want it present and empty", n)
	}
}

// checkContainsExpr checks case-insensitive matching and that escaped
// wildcards in the bound pattern match literally.
func checkContainsExpr(t *testing.T, d schemadmin.Dialect, db *sql.DB) {
	mustExec(t, db, `CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	names := []string{"Alice", "ALBERT", "bob", "100%", "100x", "a_b", "axb", `back\slash`, "backslash"}
	insert := fmt.Sprintf("INSERT INTO people (id, name, age) VALUES (%s, %s, %s)", d.Placeholder(1), d.Placeholder(2), d.Placeholder(3))
	for i, name := range names {
		mustExec(t, db, insert, i+1, name, 1230+i)
	}
	mustExec(t, db, "INSERT INTO people (id) VALUES (100)")

	tests := []struct {
		column  string
		pattern string
		want    []string
	}{
		{"name", "%al%", []string{"Alice", "ALBERT"}},
		{"name", "%AL%", []string{"Alice", "ALBERT"}},
		{"name", "%BOB%", []string{"bob"}},
		{"name", `%100\%%`, []string{"100%"}},
		{"name", "%100%", []string{"100%", "100x"}},
		{"name", `%a\_b%`, []string{"a_b"}},
		{"name", "%a_b%", []string{"ALBERT", "a_b", "axb"}}, // "_" matches any one character
		{"name", `%back\\slash%`, []string{`back\slash`}},
		{"name", "%nobody%", nil},
		{"name", "%", names},
		{"age", "%123%", names},
		{"age", "%1238%", []string{"backslash"}},
	}
	for _, tt := range tests {
		t.Run(tt.column+" "+tt.pattern, func(t *testing.T) {
			query := "SELECT name FROM people WHERE " + d.ContainsExpr(d.QuoteIdent(tt.column), d.Placeholder(1)) + " ORDER BY id"
			rows, err := db.Query(query, tt.pattern)
			if err != nil {
				t.Fatalf("%v\n%s", err, query)
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatal(err)
				}
				got = append(got, name)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("pattern %q matched %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

// constraintCase is a statement and the kind of violation it must cause.
type constraintCase struct {
	name  string
	query string
	want  schemadmin.ConstraintKind
	ok    bool // the statement succeeds
}

// checkClassifyError feeds ClassifyError the errors a real driver returns
// for real violations.
func checkClassifyError(t *testing.T, d schemadmin.Dialect, db *sql.DB, cases []constraintCase) {
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.Exec(tt.query)
			if tt.ok != (err == nil) {
				t.Fatalf("exec %q: unexpected result: %v", tt.query, err)
			}
			if got := d.ClassifyError(err); got != tt.want {
				t.Errorf("ClassifyError(%v) = %v, want %v", err, got, tt.want)
			}
		})
	}
}
