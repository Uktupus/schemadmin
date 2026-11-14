package integration

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/sqlite"
)

// openSQLite opens a fresh database file. Extra DSN parameters, such as
// "_pragma=foreign_keys(1)", are appended as given.
func openSQLite(t *testing.T, params ...string) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "test.db")
	if len(params) > 0 {
		dsn += "?" + strings.Join(params, "&")
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func introspectSQLite(t *testing.T, db *sql.DB) *schemadmin.Schema {
	t.Helper()
	schema, err := sqlite.New().Introspect(context.Background(), db, schemadmin.IntrospectOptions{})
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	return schema
}

func mustTable(t *testing.T, schema *schemadmin.Schema, name string) *schemadmin.Table {
	t.Helper()
	table := schema.Table("", name)
	if table == nil {
		t.Fatalf("table %q was not introspected", name)
	}
	return table
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func TestSQLiteIntrospect(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, readTestdata(t, "sqlite_schema.sql"))

	got := dumpSchema(introspectSQLite(t, db))
	want := readTestdata(t, "sqlite_schema.golden")
	if diff := diffLines(want, got); diff != "" {
		t.Errorf("introspected schema differs from testdata/sqlite_schema.golden:\n%s", diff)
	}
}

func TestSQLiteIntrospectEmptyDatabase(t *testing.T) {
	schema := introspectSQLite(t, openSQLite(t))
	if len(schema.Tables) != 0 {
		t.Errorf("got %d tables from an empty database, want 0", len(schema.Tables))
	}
}

func TestSQLiteIntrospectOptions(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	d := sqlite.New()

	for _, schemas := range [][]string{nil, {}, {"main"}} {
		schema, err := d.Introspect(context.Background(), db, schemadmin.IntrospectOptions{Schemas: schemas})
		if err != nil {
			t.Errorf("Schemas %v: %v", schemas, err)
			continue
		}
		if len(schema.Tables) != 1 {
			t.Errorf("Schemas %v: got %d tables, want 1", schemas, len(schema.Tables))
		}
	}

	if _, err := d.Introspect(context.Background(), db, schemadmin.IntrospectOptions{Schemas: []string{"other"}}); err == nil {
		t.Error(`Schemas ["other"]: got no error for a schema the dialect cannot read`)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Introspect(ctx, db, schemadmin.IntrospectOptions{}); err == nil {
		t.Error("cancelled context: got no error")
	}
}

// TestSQLiteOnlyMainSchema checks that tables of the temp schema and of
// attached databases are neither listed nor mistaken for main tables that
// share their name.
func TestSQLiteOnlyMainSchema(t *testing.T) {
	db := openSQLite(t)
	// Temp tables and attachments belong to one connection.
	db.SetMaxOpenConns(1)

	mustExec(t, db, "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)")
	mustExec(t, db, "CREATE TEMP TABLE users (shadow TEXT, columns TEXT)")
	mustExec(t, db, "CREATE TEMP TABLE scratch (x)")
	mustExec(t, db, "ATTACH DATABASE ? AS other", filepath.Join(t.TempDir(), "other.db"))
	mustExec(t, db, "CREATE TABLE other.users (elsewhere TEXT)")
	mustExec(t, db, "CREATE TABLE other.extra (y)")

	got := dumpSchema(introspectSQLite(t, db))
	want := "table users\n  pk id\n  col id INTEGER int notnull auto\n  col email TEXT text null\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestSQLiteNullability checks Column.Nullable against what the database
// really does: a column is nullable exactly when a NULL can be stored in it.
// SQLite's rules for primary keys are irregular, which is why they are
// probed rather than assumed.
func TestSQLiteNullability(t *testing.T) {
	tests := []struct {
		name   string
		ddl    string
		column string
		insert string // tries to store NULL in the column
	}{
		{"rowid alias", "CREATE TABLE t (id INTEGER PRIMARY KEY, v)", "id", "INSERT INTO t (id, v) VALUES (NULL, 1)"},
		{"autoincrement", "CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, v)", "id", "INSERT INTO t (id, v) VALUES (NULL, 1)"},
		{"rowid alias declared as a table constraint", "CREATE TABLE t (id INTEGER, v, PRIMARY KEY (id))", "id", "INSERT INTO t (id, v) VALUES (NULL, 1)"},
		{"integer primary key desc", "CREATE TABLE t (id INTEGER PRIMARY KEY DESC, v)", "id", "INSERT INTO t (id, v) VALUES (NULL, 1)"},
		{"int primary key", "CREATE TABLE t (id INT PRIMARY KEY, v)", "id", "INSERT INTO t (id, v) VALUES (NULL, 1)"},
		{"text primary key", "CREATE TABLE t (k TEXT PRIMARY KEY, v)", "k", "INSERT INTO t (k, v) VALUES (NULL, 1)"},
		{"text primary key not null", "CREATE TABLE t (k TEXT PRIMARY KEY NOT NULL, v)", "k", "INSERT INTO t (k, v) VALUES (NULL, 1)"},
		{"composite primary key", "CREATE TABLE t (a, b, PRIMARY KEY (a, b))", "a", "INSERT INTO t (a, b) VALUES (NULL, 1)"},
		{"without rowid", "CREATE TABLE t (k TEXT PRIMARY KEY, v) WITHOUT ROWID", "k", "INSERT INTO t (k, v) VALUES (NULL, 1)"},
		{"without rowid composite", "CREATE TABLE t (a, b, PRIMARY KEY (a, b)) WITHOUT ROWID", "b", "INSERT INTO t (a, b) VALUES (1, NULL)"},
		{"strict int primary key", "CREATE TABLE t (id INT PRIMARY KEY, v ANY) STRICT", "id", "INSERT INTO t (id, v) VALUES (NULL, 1)"},
		{"strict rowid alias", "CREATE TABLE t (id INTEGER PRIMARY KEY, v ANY) STRICT", "id", "INSERT INTO t (id, v) VALUES (NULL, 1)"},
		{"strict text primary key", "CREATE TABLE t (k TEXT PRIMARY KEY, v ANY) STRICT", "k", "INSERT INTO t (k, v) VALUES (NULL, 1)"},
		{"plain column", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", "v", "INSERT INTO t (v) VALUES (NULL)"},
		{"not null column", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT NOT NULL)", "v", "INSERT INTO t (v) VALUES (NULL)"},
		{"not null column with default", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT NOT NULL DEFAULT 'x')", "v", "INSERT INTO t (v) VALUES (NULL)"},
		{"nullable column with default", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT DEFAULT 'x')", "v", "INSERT INTO t (v) VALUES (NULL)"},
	}
	d := sqlite.New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openSQLite(t)
			mustExec(t, db, tt.ddl)
			column := mustTable(t, introspectSQLite(t, db), "t").Column(tt.column)
			if column == nil {
				t.Fatalf("column %q was not introspected", tt.column)
			}

			_, insertErr := db.Exec(tt.insert)
			stored := countRows(t, db, "SELECT count(*) FROM t WHERE "+d.QuoteIdent(tt.column)+" IS NULL") > 0
			if column.Nullable != stored {
				t.Errorf("Nullable = %v, but the database stored a NULL: %v (insert error: %v)",
					column.Nullable, stored, insertErr)
			}
		})
	}
}

// TestSQLiteRequiredColumns checks the model's claims about which columns
// an INSERT must supply, against the fixture: giving values for only the
// columns that are NOT NULL, have no default and are not generated must
// succeed, must leave no NULL in a non-nullable column, and writing a
// generated column must fail.
func TestSQLiteRequiredColumns(t *testing.T) {
	db := openSQLite(t) // foreign keys are not enforced, so dummy keys are fine
	mustExec(t, db, readTestdata(t, "sqlite_schema.sql"))
	d := sqlite.New()

	for _, table := range introspectSQLite(t, db).Tables {
		t.Run(table.Name, func(t *testing.T) {
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
				nulls := countRows(t, db, "SELECT count(*) FROM "+d.QuoteIdent(table.Name)+" WHERE "+d.QuoteIdent(c.Name)+" IS NULL")
				if nulls != 0 {
					t.Errorf("column %q is reported NOT NULL but holds NULL after the insert", c.Name)
				}
			}

			for _, c := range generated {
				query, args := insertStatement(d, table, append(slices.Clone(required), c))
				if _, err := db.Exec(query, args...); err == nil {
					t.Errorf("column %q is reported generated but accepted a written value", c.Name)
				}
			}
		})
	}
}

// insertStatement builds an INSERT that supplies a plausible value for each
// of the given columns and nothing else.
func insertStatement(d schemadmin.Dialect, table *schemadmin.Table, columns []*schemadmin.Column) (string, []any) {
	if len(columns) == 0 {
		return "INSERT INTO " + d.QuoteIdent(table.Name) + " DEFAULT VALUES", nil
	}
	var names, marks []string
	var args []any
	for i, c := range columns {
		names = append(names, d.QuoteIdent(c.Name))
		marks = append(marks, d.Placeholder(i+1))
		args = append(args, sampleValue(c))
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		d.QuoteIdent(table.Name), strings.Join(names, ", "), strings.Join(marks, ", ")), args
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
		return 1
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

// TestSQLiteHostileIdentifiers creates a table whose name and column names
// contain quotes, SQL and markup, and checks that the names survive
// introspection unchanged and that QuoteIdent makes them usable.
func TestSQLiteHostileIdentifiers(t *testing.T) {
	const tableName = `we"ird "table" <b>; DROP TABLE canary; --`
	columnNames := []string{
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
	}

	db := openSQLite(t)
	d := sqlite.New()
	mustExec(t, db, "CREATE TABLE canary (id INTEGER PRIMARY KEY)")

	var defs []string
	for i, name := range columnNames {
		def := d.QuoteIdent(name) + " TEXT"
		if i == 0 {
			def = d.QuoteIdent(name) + " INTEGER PRIMARY KEY"
		}
		defs = append(defs, def)
	}
	defs = append(defs, fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)",
		d.QuoteIdent(columnNames[2]), d.QuoteIdent(tableName), d.QuoteIdent(columnNames[0])))
	mustExec(t, db, "CREATE TABLE "+d.QuoteIdent(tableName)+" ("+strings.Join(defs, ", ")+")")

	schema := introspectSQLite(t, db)
	table := mustTable(t, schema, tableName)
	mustTable(t, schema, "canary")

	var gotNames []string
	for _, c := range table.Columns {
		gotNames = append(gotNames, c.Name)
	}
	if !slices.Equal(gotNames, columnNames) {
		t.Fatalf("column names\n got %q\nwant %q", gotNames, columnNames)
	}
	wantFK := &schemadmin.ForeignKey{
		Columns:    []string{columnNames[2]},
		RefTable:   tableName,
		RefColumns: []string{columnNames[0]},
	}
	if len(table.ForeignKeys) != 1 || !reflect.DeepEqual(table.ForeignKeys[0], wantFK) {
		t.Errorf("foreign keys = %+v, want [%+v]", table.ForeignKeys, wantFK)
	}

	// Write and read every column through quoted identifiers only.
	query, args := insertStatement(d, table, table.Columns)
	mustExec(t, db, query, args...)
	for _, c := range table.Columns {
		query := fmt.Sprintf("SELECT %s FROM %s WHERE %s",
			d.SelectExpr(d.QuoteIdent(c.Name), c),
			d.QuoteIdent(table.Name),
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
		if !reflect.DeepEqual(got, want) {
			t.Errorf("column %q: read %#v, want %#v", c.Name, got, want)
		}
	}

	if n := countRows(t, db, "SELECT count(*) FROM sqlite_master WHERE name = 'canary'"); n != 1 {
		t.Error("the canary table is gone: an identifier escaped its quotes")
	}
}

// TestSQLiteClassifyError feeds ClassifyError the errors a real driver
// returns for real violations.
func TestSQLiteClassifyError(t *testing.T) {
	db := openSQLite(t, "_pragma=foreign_keys(1)")
	mustExec(t, db, `
		CREATE TABLE parents (
			id   INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			n    INTEGER CHECK (n > 0),
			kind TEXT CONSTRAINT kind_ok CHECK (kind IN ('a', 'b'))
		);
		CREATE TABLE children (
			id        INTEGER PRIMARY KEY,
			parent_id INTEGER NOT NULL REFERENCES parents (id)
		);
		CREATE TABLE codes (code TEXT PRIMARY KEY) WITHOUT ROWID;
		CREATE TABLE pairs (a INTEGER, b INTEGER, PRIMARY KEY (a, b));
		INSERT INTO parents (id, name, n) VALUES (1, 'first', 1);
		INSERT INTO children (id, parent_id) VALUES (1, 1);
		INSERT INTO codes VALUES ('x');
		INSERT INTO pairs VALUES (1, 2);
	`)

	tests := []struct {
		name  string
		query string
		want  schemadmin.ConstraintKind
	}{
		{"success", "INSERT INTO parents (name) VALUES ('second')", schemadmin.ConstraintNone},
		{"unique column", "INSERT INTO parents (name) VALUES ('first')", schemadmin.ConstraintUnique},
		{"duplicate rowid", "INSERT INTO parents (id, name) VALUES (1, 'third')", schemadmin.ConstraintUnique},
		{"duplicate key without rowid", "INSERT INTO codes VALUES ('x')", schemadmin.ConstraintUnique},
		{"duplicate composite key", "INSERT INTO pairs VALUES (1, 2)", schemadmin.ConstraintUnique},
		{"not null", "INSERT INTO parents (name) VALUES (NULL)", schemadmin.ConstraintNotNull},
		{"not null by omission", "INSERT INTO children (id) VALUES (2)", schemadmin.ConstraintNotNull},
		{"check", "INSERT INTO parents (name, n) VALUES ('fourth', 0)", schemadmin.ConstraintCheck},
		{"named check", "INSERT INTO parents (name, kind) VALUES ('fifth', 'z')", schemadmin.ConstraintCheck},
		{"foreign key on insert", "INSERT INTO children (parent_id) VALUES (999)", schemadmin.ConstraintForeignKey},
		{"foreign key on update", "UPDATE children SET parent_id = 999", schemadmin.ConstraintForeignKey},
		{"foreign key on delete", "DELETE FROM parents WHERE id = 1", schemadmin.ConstraintForeignKey},
		{"syntax error", "INSERT INTO", schemadmin.ConstraintNone},
		{"missing table", "INSERT INTO nowhere VALUES (1)", schemadmin.ConstraintNone},
		{"missing column", "INSERT INTO parents (nope) VALUES (1)", schemadmin.ConstraintNone},
	}
	d := sqlite.New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.Exec(tt.query)
			if (tt.name == "success") != (err == nil) {
				t.Fatalf("exec %q: unexpected result: %v", tt.query, err)
			}
			if got := d.ClassifyError(err); got != tt.want {
				t.Errorf("ClassifyError(%v) = %v, want %v", err, got, tt.want)
			}
		})
	}
}

// TestSQLiteSelectExpr checks that values read through SelectExpr arrive as
// SQLite stored them, with no driver-specific reinterpretation of columns
// declared BOOLEAN, DATE, DATETIME or TIMESTAMP.
func TestSQLiteSelectExpr(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, `
		CREATE TABLE v (
			id      INTEGER PRIMARY KEY,
			flag    BOOLEAN,
			d       DATE,
			at      DATETIME,
			ts      TIMESTAMP,
			unix_at DATETIME,
			n       INTEGER,
			r       REAL,
			s       TEXT,
			b       BLOB,
			amount  DECIMAL(10,2),
			doc     JSON
		);
		INSERT INTO v VALUES (1, 1, '2024-01-02', '2024-01-02 03:04:05', '2024-01-02T03:04:05Z',
			1704164645, 42, 1.5, 'text', x'0102', '12.30', '{"a":1}');
		INSERT INTO v (id) VALUES (2);
	`)
	table := mustTable(t, introspectSQLite(t, db), "v")
	d := sqlite.New()

	var exprs []string
	for _, c := range table.Columns {
		exprs = append(exprs, d.SelectExpr(d.QuoteIdent(c.Name), c))
	}
	query := "SELECT " + strings.Join(exprs, ", ") + " FROM " + d.QuoteIdent(table.Name) + " WHERE id = " + d.Placeholder(1)

	scanRow := func(t *testing.T, query string, id int) []any {
		t.Helper()
		values := make([]any, len(table.Columns))
		targets := make([]any, len(values))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := db.QueryRow(query, id).Scan(targets...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
		return values
	}

	want := []any{
		int64(1),               // id
		int64(1),               // flag: stored as an integer
		"2024-01-02",           // d
		"2024-01-02 03:04:05",  // at
		"2024-01-02T03:04:05Z", // ts
		int64(1704164645),      // unix_at: a timestamp stored as a number stays a number
		int64(42),              // n
		1.5,                    // r
		"text",                 // s
		[]byte{1, 2},           // b
		12.3,                   // amount: NUMERIC affinity turned the text '12.30' into a real
		`{"a":1}`,              // doc
	}
	if got := scanRow(t, query, 1); !reflect.DeepEqual(got, want) {
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("column %q: read %#v (%T), want %#v (%T)", table.Columns[i].Name, got[i], got[i], want[i], want[i])
			}
		}
	}

	for i, v := range scanRow(t, query, 2)[1:] {
		if v != nil {
			t.Errorf("column %q: NULL read as %#v", table.Columns[i+1].Name, v)
		}
	}

	// For the record: what the driver returns when columns are read
	// directly. Differences from the values above are the reinterpretation
	// SelectExpr exists to avoid.
	var names []string
	for _, c := range table.Columns {
		names = append(names, d.QuoteIdent(c.Name))
	}
	direct := scanRow(t, "SELECT "+strings.Join(names, ", ")+" FROM v WHERE id = ?", 1)
	for i, v := range direct {
		if !reflect.DeepEqual(v, want[i]) {
			t.Logf("read directly, column %q arrives as %T (%v) instead of %T", table.Columns[i].Name, v, v, want[i])
		}
	}
}

// TestSQLiteContainsExpr checks case-insensitive matching and that escaped
// wildcards in the bound pattern match literally.
func TestSQLiteContainsExpr(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, `CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	names := []string{"Alice", "ALBERT", "bob", "100%", "100x", "a_b", "axb", `back\slash`, "backslash"}
	for i, name := range names {
		mustExec(t, db, "INSERT INTO people (id, name, age) VALUES (?, ?, ?)", i+1, name, 1230+i)
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
	d := sqlite.New()
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
