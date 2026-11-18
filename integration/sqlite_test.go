package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
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

func introspectSQLite(t *testing.T, db *sql.DB) *schemadmin.Schema {
	t.Helper()
	return mustIntrospect(t, sqlite.New(), db, schemadmin.IntrospectOptions{})
}

func TestSQLiteIntrospect(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, readTestdata(t, "sqlite_schema.sql"))
	checkGolden(t, sqlite.New(), db, schemadmin.IntrospectOptions{}, "sqlite_schema.golden")
}

// TestSQLiteIgnoredForeignKeysAreLogged checks that each foreign key left
// out of the schema is reported at warn level with its table, columns and
// the reason, and that nothing else is logged.
func TestSQLiteIgnoredForeignKeysAreLogged(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, readTestdata(t, "sqlite_schema.sql"))

	var recorder logRecorder
	mustIntrospect(t, sqlite.New(), db, schemadmin.IntrospectOptions{Logger: recorder.logger()})

	want := []map[string]any{
		{
			"level":      "WARN",
			"msg":        "schemadmin: foreign key ignored",
			"dialect":    "sqlite",
			"table":      "imports",
			"columns":    []any{"batch_id"},
			"references": "batches",
			"reason":     `referenced table "batches" does not exist and the constraint names no columns of it`,
		},
		{
			"level":      "WARN",
			"msg":        "schemadmin: foreign key ignored",
			"dialect":    "sqlite",
			"table":      "imports",
			"columns":    []any{"user_email"},
			"references": "users",
			"reason":     `referenced column "no_such_column" does not exist in "users"`,
		},
	}
	got := recorder.records(t)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("logged records\n got %v\nwant %v", got, want)
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

// SQLite's rules for primary keys are irregular, which is why nullability
// is probed rather than assumed.
func TestSQLiteNullability(t *testing.T) {
	probes := []nullProbe{
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
	checkNullability(t, sqlite.New(), func(t *testing.T) *sql.DB { return openSQLite(t) }, probes)
}

func TestSQLiteRequiredColumns(t *testing.T) {
	db := openSQLite(t) // foreign keys are not enforced unless switched on
	mustExec(t, db, readTestdata(t, "sqlite_schema.sql"))
	checkRequiredColumns(t, sqlite.New(), db, introspectSQLite(t, db))
}

func TestSQLiteHostileIdentifiers(t *testing.T) {
	checkHostileIdentifiers(t, sqlite.New(), openSQLite(t), "")
}

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
	checkClassifyError(t, sqlite.New(), db, []constraintCase{
		{name: "success", query: "INSERT INTO parents (name) VALUES ('second')", want: schemadmin.ConstraintNone, ok: true},
		{name: "unique column", query: "INSERT INTO parents (name) VALUES ('first')", want: schemadmin.ConstraintUnique},
		{name: "duplicate rowid", query: "INSERT INTO parents (id, name) VALUES (1, 'third')", want: schemadmin.ConstraintUnique},
		{name: "duplicate key without rowid", query: "INSERT INTO codes VALUES ('x')", want: schemadmin.ConstraintUnique},
		{name: "duplicate composite key", query: "INSERT INTO pairs VALUES (1, 2)", want: schemadmin.ConstraintUnique},
		{name: "not null", query: "INSERT INTO parents (name) VALUES (NULL)", want: schemadmin.ConstraintNotNull},
		{name: "not null by omission", query: "INSERT INTO children (id) VALUES (2)", want: schemadmin.ConstraintNotNull},
		{name: "check", query: "INSERT INTO parents (name, n) VALUES ('fourth', 0)", want: schemadmin.ConstraintCheck},
		{name: "named check", query: "INSERT INTO parents (name, kind) VALUES ('fifth', 'z')", want: schemadmin.ConstraintCheck},
		{name: "foreign key on insert", query: "INSERT INTO children (parent_id) VALUES (999)", want: schemadmin.ConstraintForeignKey},
		{name: "foreign key on update", query: "UPDATE children SET parent_id = 999", want: schemadmin.ConstraintForeignKey},
		{name: "foreign key on delete", query: "DELETE FROM parents WHERE id = 1", want: schemadmin.ConstraintForeignKey},
		{name: "syntax error", query: "INSERT INTO", want: schemadmin.ConstraintNone},
		{name: "missing table", query: "INSERT INTO nowhere VALUES (1)", want: schemadmin.ConstraintNone},
		{name: "missing column", query: "INSERT INTO parents (nope) VALUES (1)", want: schemadmin.ConstraintNone},
	})
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

	var exprs, names []string
	for _, c := range table.Columns {
		exprs = append(exprs, d.SelectExpr(d.QuoteIdent(c.Name), c))
		names = append(names, d.QuoteIdent(c.Name))
	}
	query := "SELECT " + strings.Join(exprs, ", ") + " FROM v WHERE id = ?"

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
		"0102",                 // b: hex text from SelectExpr
		12.3,                   // amount: NUMERIC affinity turned the text '12.30' into a real
		`{"a":1}`,              // doc
	}
	got := scanValues(t, db, len(want), query, 1)
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("column %q: read %#v (%T), want %#v (%T)", table.Columns[i].Name, got[i], got[i], want[i], want[i])
		}
	}

	for i, v := range scanValues(t, db, len(want), query, 2)[1:] {
		if v != nil {
			t.Errorf("column %q: NULL read as %#v", table.Columns[i+1].Name, v)
		}
	}

	// For the record: what the driver returns when columns are read
	// directly. Differences from the values above are the reinterpretation
	// SelectExpr exists to avoid.
	direct := scanValues(t, db, len(want), "SELECT "+strings.Join(names, ", ")+" FROM v WHERE id = ?", 1)
	for i, v := range direct {
		if !reflect.DeepEqual(v, want[i]) {
			t.Logf("read directly, column %q arrives as %T (%v) instead of %T", table.Columns[i].Name, v, v, want[i])
		}
	}
}

func TestSQLiteContainsExpr(t *testing.T) {
	checkContainsExpr(t, sqlite.New(), openSQLite(t))
}
