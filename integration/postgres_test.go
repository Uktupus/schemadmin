package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/lib/pq"

	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/postgres"
)

// postgresDSNEnv names the environment variable that points the tests at a
// PostgreSQL server. It must hold a URL ("postgres://...") for a role that
// may create databases and set session_replication_role; every test runs in
// a database of its own, created for it and dropped afterwards.
const postgresDSNEnv = "SCHEMADMIN_POSTGRES_DSN"

// postgresDrivers are the database/sql drivers every PostgreSQL test runs
// under. The dialect imports neither, so it must behave the same with both.
var postgresDrivers = []struct{ name, driverName string }{
	{"pgx", "pgx"},
	{"pq", "postgres"},
}

// forEachPostgresDriver runs fn once per driver, as a subtest.
func forEachPostgresDriver(t *testing.T, fn func(t *testing.T, open func(t *testing.T) *sql.DB)) {
	base := os.Getenv(postgresDSNEnv)
	if base == "" {
		t.Skipf("%s is not set: skipping the PostgreSQL tests. Run scripts/pg-test.sh to start a throwaway server and run them.", postgresDSNEnv)
	}
	if !strings.HasPrefix(base, "postgres://") && !strings.HasPrefix(base, "postgresql://") {
		t.Fatalf("%s must be a postgres:// URL", postgresDSNEnv)
	}
	for _, driver := range postgresDrivers {
		t.Run(driver.name, func(t *testing.T) {
			fn(t, func(t *testing.T) *sql.DB { return openPostgres(t, base, driver.driverName) })
		})
	}
}

// openPostgres creates an empty database and returns a handle to it that
// uses the given driver. The database is dropped when the test ends.
func openPostgres(t *testing.T, base, driverName string) *sql.DB {
	t.Helper()
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "schemadmin_test_" + hex.EncodeToString(suffix)

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open(driverName, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Errorf("drop test database: %v", err)
		}
		admin.Close()
	})
	return db
}

func TestPostgresIntrospect(t *testing.T) {
	forEachPostgresDriver(t, func(t *testing.T, open func(*testing.T) *sql.DB) {
		db := open(t)
		mustExec(t, db, readTestdata(t, "postgres_schema.sql"))

		var recorder logRecorder
		opts := schemadmin.IntrospectOptions{Schemas: []string{"app", "billing"}, Logger: recorder.logger()}
		checkGolden(t, postgres.New(), db, opts, "postgres_schema.golden")

		// PostgreSQL has no constraint the model cannot represent, so
		// nothing is left out and nothing is logged.
		if records := recorder.records(t); len(records) != 0 {
			t.Errorf("introspection logged %v, want nothing", records)
		}
	})
}

func TestPostgresIntrospectOptions(t *testing.T) {
	forEachPostgresDriver(t, func(t *testing.T, open func(*testing.T) *sql.DB) {
		db := open(t)
		d := postgres.New()
		ctx := context.Background()

		schema := mustIntrospect(t, d, db, schemadmin.IntrospectOptions{})
		if len(schema.Tables) != 0 {
			t.Errorf("got %d tables from an empty database, want 0", len(schema.Tables))
		}

		mustExec(t, db, `
			CREATE SCHEMA a;
			CREATE SCHEMA b;
			CREATE TABLE public.in_public (id integer PRIMARY KEY);
			CREATE TABLE a.in_a (id integer PRIMARY KEY);
			CREATE TABLE b.in_b (id integer PRIMARY KEY);
		`)
		tests := []struct {
			name    string
			schemas []string
			want    string // "schema.table" names in order
		}{
			{"default is public", nil, "public.in_public"},
			{"one schema", []string{"a"}, "a.in_a"},
			{"schemas in the order given", []string{"b", "public", "a"}, "b.in_b public.in_public a.in_a"},
			{"a repeated schema is read once", []string{"a", "b", "a"}, "a.in_a b.in_b"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				schema := mustIntrospect(t, d, db, schemadmin.IntrospectOptions{Schemas: tt.schemas})
				var names []string
				for _, table := range schema.Tables {
					names = append(names, table.Schema+"."+table.Name)
				}
				if got := strings.Join(names, " "); got != tt.want {
					t.Errorf("tables = %q, want %q", got, tt.want)
				}
			})
		}

		_, err := d.Introspect(ctx, db, schemadmin.IntrospectOptions{Schemas: []string{"a", "typo", "B"}})
		if err == nil || !strings.Contains(err.Error(), `"typo"`) || !strings.Contains(err.Error(), `"B"`) {
			t.Errorf("missing schemas: error = %v, want it to name both missing schemas", err)
		}
		if _, err := d.Introspect(ctx, db, schemadmin.IntrospectOptions{Schemas: []string{""}}); err == nil {
			t.Error("empty schema name: got no error")
		}

		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := d.Introspect(cancelled, db, schemadmin.IntrospectOptions{}); err == nil {
			t.Error("cancelled context: got no error")
		}
	})
}

func TestPostgresNullability(t *testing.T) {
	probes := []nullProbe{
		{"plain column", "CREATE TABLE t (id integer PRIMARY KEY, v text)", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"not null column", "CREATE TABLE t (id integer PRIMARY KEY, v text NOT NULL)", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"not null column with default", "CREATE TABLE t (id integer PRIMARY KEY, v text NOT NULL DEFAULT 'x')", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"nullable column with default", "CREATE TABLE t (id integer PRIMARY KEY, v text DEFAULT 'x')", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"unique column", "CREATE TABLE t (id integer PRIMARY KEY, v text UNIQUE)", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"primary key", "CREATE TABLE t (id integer PRIMARY KEY, v text)", "id", "INSERT INTO t (id, v) VALUES (NULL, 'x')"},
		{"text primary key", "CREATE TABLE t (k text PRIMARY KEY, v text)", "k", "INSERT INTO t (k, v) VALUES (NULL, 'x')"},
		{"composite primary key", "CREATE TABLE t (a integer, b integer, PRIMARY KEY (a, b))", "b", "INSERT INTO t (a, b) VALUES (1, NULL)"},
		{"serial", "CREATE TABLE t (id serial, v text)", "id", "INSERT INTO t (id, v) VALUES (NULL, 'x')"},
		{"identity by default", "CREATE TABLE t (id integer GENERATED BY DEFAULT AS IDENTITY, v text)", "id", "INSERT INTO t (id, v) VALUES (NULL, 'x')"},
		{"identity always", "CREATE TABLE t (id integer GENERATED ALWAYS AS IDENTITY, v text)", "id", "INSERT INTO t (id, v) VALUES (NULL, 'x')"},
		{"generated column", "CREATE TABLE t (id integer PRIMARY KEY, v text, n integer GENERATED ALWAYS AS (length(v)) STORED)", "n", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"not null domain", "CREATE DOMAIN d AS text NOT NULL; CREATE TABLE t (id integer PRIMARY KEY, v d)", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"nullable domain", "CREATE DOMAIN d AS text; CREATE TABLE t (id integer PRIMARY KEY, v d)", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"nullable domain in a not null column", "CREATE DOMAIN d AS text; CREATE TABLE t (id integer PRIMARY KEY, v d NOT NULL)", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"domain over a not null domain", "CREATE DOMAIN inner_d AS text NOT NULL; CREATE DOMAIN d AS inner_d; CREATE TABLE t (id integer PRIMARY KEY, v d)", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"not null domain over a domain", "CREATE DOMAIN inner_d AS text; CREATE DOMAIN d AS inner_d NOT NULL; CREATE TABLE t (id integer PRIMARY KEY, v d)", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
	}
	forEachPostgresDriver(t, func(t *testing.T, open func(*testing.T) *sql.DB) {
		checkNullability(t, postgres.New(), open, probes)
	})
}

func TestPostgresRequiredColumns(t *testing.T) {
	forEachPostgresDriver(t, func(t *testing.T, open func(*testing.T) *sql.DB) {
		db := open(t)
		mustExec(t, db, readTestdata(t, "postgres_schema.sql"))

		// The sample values satisfy no foreign key. Triggers, and with
		// them foreign-key checks, are off for a replica session; the
		// setting belongs to one connection, so the pool is held to it.
		db.SetMaxOpenConns(1)
		mustExec(t, db, "SET session_replication_role = replica")

		d := postgres.New()
		schema := mustIntrospect(t, d, db, schemadmin.IntrospectOptions{Schemas: []string{"app", "billing"}})
		checkRequiredColumns(t, d, db, schema)
	})
}

func TestPostgresHostileIdentifiers(t *testing.T) {
	forEachPostgresDriver(t, func(t *testing.T, open func(*testing.T) *sql.DB) {
		db := open(t)
		d := postgres.New()
		checkHostileIdentifiers(t, d, db, hostileSchemaName)

		// Enum labels are data from the database too.
		labels := []string{`it's`, `<script>alert(1)</script>`, `a|b`, `quote"d`, `semi;colon`, ` `}
		var quoted []string
		for _, label := range labels {
			quoted = append(quoted, "'"+strings.ReplaceAll(label, "'", "''")+"'")
		}
		schemaName := d.QuoteIdent(hostileSchemaName)
		mustExec(t, db, "CREATE TYPE "+schemaName+`."e n""um" AS ENUM (`+strings.Join(quoted, ", ")+")")
		mustExec(t, db, "CREATE TABLE "+schemaName+`.labelled (id integer PRIMARY KEY, v `+schemaName+`."e n""um")`)

		schema := mustIntrospect(t, d, db, schemadmin.IntrospectOptions{Schemas: []string{hostileSchemaName}})
		column := mustTable(t, schema, "labelled").Column("v")
		if column.Kind != schemadmin.KindEnum || !reflect.DeepEqual(column.EnumValues, labels) {
			t.Errorf("enum column: kind %v, values %q; want enum, %q", column.Kind, column.EnumValues, labels)
		}
		if !strings.Contains(column.DBType, `e n""um`) {
			t.Errorf("DBType = %s, want it to name the enum type", column.DBType)
		}
	})
}

func TestPostgresClassifyError(t *testing.T) {
	forEachPostgresDriver(t, func(t *testing.T, open func(*testing.T) *sql.DB) {
		db := open(t)
		mustExec(t, db, `
			CREATE DOMAIN positive AS integer CHECK (VALUE > 0);
			CREATE TABLE parents (
				id   integer PRIMARY KEY,
				name text NOT NULL UNIQUE,
				n    integer CHECK (n > 0),
				kind text CONSTRAINT kind_ok CHECK (kind IN ('a', 'b')),
				p    positive
			);
			CREATE TABLE children (
				id        integer PRIMARY KEY,
				parent_id integer NOT NULL REFERENCES parents (id)
			);
			CREATE TABLE strict_children (
				id        integer PRIMARY KEY,
				parent_id integer REFERENCES parents (id) ON DELETE RESTRICT
			);
			CREATE TABLE pairs (a integer, b integer, PRIMARY KEY (a, b));
			CREATE TABLE bookings (room integer, during int4range, EXCLUDE USING gist (during WITH &&));
			INSERT INTO parents (id, name, n) VALUES (1, 'first', 1), (2, 'restricted', 1);
			INSERT INTO children (id, parent_id) VALUES (1, 1);
			INSERT INTO strict_children (id, parent_id) VALUES (1, 2);
			INSERT INTO pairs VALUES (1, 2);
			INSERT INTO bookings VALUES (1, '[1,5)');
		`)
		checkClassifyError(t, postgres.New(), db, []constraintCase{
			{name: "success", query: "INSERT INTO parents (id, name) VALUES (10, 'second')", want: schemadmin.ConstraintNone, ok: true},
			{name: "unique column", query: "INSERT INTO parents (id, name) VALUES (11, 'first')", want: schemadmin.ConstraintUnique},
			{name: "duplicate primary key", query: "INSERT INTO parents (id, name) VALUES (1, 'third')", want: schemadmin.ConstraintUnique},
			{name: "duplicate composite key", query: "INSERT INTO pairs VALUES (1, 2)", want: schemadmin.ConstraintUnique},
			{name: "exclusion", query: "INSERT INTO bookings VALUES (1, '[3,8)')", want: schemadmin.ConstraintUnique},
			{name: "not null", query: "INSERT INTO parents (id, name) VALUES (12, NULL)", want: schemadmin.ConstraintNotNull},
			{name: "not null by omission", query: "INSERT INTO children (id) VALUES (2)", want: schemadmin.ConstraintNotNull},
			{name: "check", query: "INSERT INTO parents (id, name, n) VALUES (13, 'fourth', 0)", want: schemadmin.ConstraintCheck},
			{name: "named check", query: "INSERT INTO parents (id, name, kind) VALUES (14, 'fifth', 'z')", want: schemadmin.ConstraintCheck},
			{name: "domain check", query: "INSERT INTO parents (id, name, p) VALUES (15, 'sixth', -1)", want: schemadmin.ConstraintCheck},
			{name: "foreign key on insert", query: "INSERT INTO children (id, parent_id) VALUES (3, 999)", want: schemadmin.ConstraintForeignKey},
			{name: "foreign key on update", query: "UPDATE children SET parent_id = 999", want: schemadmin.ConstraintForeignKey},
			{name: "foreign key on delete", query: "DELETE FROM parents WHERE id = 1", want: schemadmin.ConstraintForeignKey},
			{name: "foreign key on delete restrict", query: "DELETE FROM parents WHERE id = 2", want: schemadmin.ConstraintForeignKey},
			{name: "syntax error", query: "INSERT INTO", want: schemadmin.ConstraintNone},
			{name: "missing table", query: "INSERT INTO nowhere VALUES (1)", want: schemadmin.ConstraintNone},
			{name: "missing column", query: "INSERT INTO parents (nope) VALUES (1)", want: schemadmin.ConstraintNone},
			{name: "invalid input", query: "INSERT INTO parents (id, name) VALUES ('abc', 'x')", want: schemadmin.ConstraintNone},
		})
	})
}

// TestPostgresSelectExpr checks that a value read through SelectExpr is the
// same Go value under both drivers, and that it does not change with the
// session settings that alter how PostgreSQL prints dates and times.
func TestPostgresSelectExpr(t *testing.T) {
	forEachPostgresDriver(t, func(t *testing.T, open func(*testing.T) *sql.DB) {
		db := open(t)
		db.SetMaxOpenConns(1) // session settings below belong to one connection
		mustExec(t, db, `
			CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy');
			CREATE TABLE v (
				id     integer PRIMARY KEY,
				flag   boolean,
				small  smallint,
				n      integer,
				big    bigint,
				r      real,
				f      double precision,
				amount numeric(10,2),
				exact  numeric,
				s      text,
				vc     varchar(10),
				ch     char(4),
				d      date,
				tm     time,
				ts     timestamp,
				tz     timestamptz,
				doc    jsonb,
				raw    json,
				u      uuid,
				m      mood,
				b      bytea,
				tags   text[],
				ip     inet,
				span   interval
			);
			INSERT INTO v VALUES (1, true, 7, 42, 9007199254740993, 0.1, 0.1::float8 + 0.2::float8,
				12.30, 123456789012345678901234567890.123456789, 'text', 'abc', 'ab',
				'2024-01-02', '03:04:05.25', '2024-01-02 03:04:05.5', '2024-01-02 03:04:05.123456+00',
				'{"b": 1, "a": [1, 2]}', '{"b":1,  "a":2}', '6BA7B810-9DAD-11D1-80B4-00C04FD430C8', 'ok',
				'\x0102', '{a,b}', '10.0.0.1', '1 day 02:00:00');
			INSERT INTO v (id, r, f, d, ts, tz) VALUES (2, 'NaN', '-Infinity', 'infinity', '-infinity', 'infinity');
			INSERT INTO v (id) VALUES (3);
		`)
		d := postgres.New()
		table := mustTable(t, mustIntrospect(t, d, db, schemadmin.IntrospectOptions{}), "v")

		var exprs, names []string
		for _, c := range table.Columns {
			exprs = append(exprs, d.SelectExpr(d.QuoteIdent(c.Name), c))
			names = append(names, d.QuoteIdent(c.Name))
		}
		query := "SELECT " + strings.Join(exprs, ", ") + " FROM v WHERE id = $1"

		want := []any{
			int64(1),                // id
			true,                    // flag
			int64(7),                // small
			int64(42),               // n
			int64(9007199254740993), // big: beyond what a float64 holds exactly
			"0.1",                   // r: not 0.10000000149011612
			"0.30000000000000004",   // f
			"12.30",                 // amount: the scale is kept
			"123456789012345678901234567890.123456789", // exact
			"text",                                 // s
			"abc",                                  // vc
			"ab",                                   // ch: padding is not part of the value
			"2024-01-02",                           // d
			"03:04:05.25",                          // tm
			"2024-01-02T03:04:05.5",                // ts
			"2024-01-02T03:04:05.123456",           // tz: in UTC
			`{"a": [1, 2], "b": 1}`,                // doc: jsonb normalises
			`{"b":1,  "a":2}`,                      // raw: json keeps the text
			"6ba7b810-9dad-11d1-80b4-00c04fd430c8", // u
			"ok",                                   // m
			[]byte{1, 2},                           // b
			"{a,b}",                                // tags
			"10.0.0.1/32",                          // ip
			"1 day 02:00:00",                       // span
		}
		special := map[string]any{"r": "NaN", "f": "-Infinity", "d": "infinity", "ts": "-infinity", "tz": "infinity"}

		check := func(t *testing.T) {
			t.Helper()
			got := scanValues(t, db, len(want), query, 1)
			for i := range want {
				if !reflect.DeepEqual(got[i], want[i]) {
					t.Errorf("column %q: read %#v (%T), want %#v (%T)", table.Columns[i].Name, got[i], got[i], want[i], want[i])
				}
			}
			for i, v := range scanValues(t, db, len(want), query, 2)[1:] {
				name := table.Columns[i+1].Name
				if !reflect.DeepEqual(v, special[name]) {
					t.Errorf("special value in column %q: read %#v, want %#v", name, v, special[name])
				}
			}
			for i, v := range scanValues(t, db, len(want), query, 3)[1:] {
				if v != nil {
					t.Errorf("column %q: NULL read as %#v", table.Columns[i+1].Name, v)
				}
			}
		}

		t.Run("default settings", check)

		mustExec(t, db, "SET DateStyle = 'German, DMY'")
		mustExec(t, db, "SET TimeZone = 'Asia/Tokyo'")
		t.Run("German date style, Tokyo time zone", check)

		mustExec(t, db, "SET DateStyle = 'SQL, MDY'")
		mustExec(t, db, "SET TimeZone = 'America/New_York'")
		t.Run("SQL date style, New York time zone", check)

		// For the record: what this driver returns when columns are read
		// directly, under the session settings left by the last subtest.
		// Differences from the values above, between the two drivers, and
		// outright failures are what SelectExpr exists to remove.
		direct := make([]any, len(want))
		targets := make([]any, len(want))
		for i := range direct {
			targets[i] = &direct[i]
		}
		if err := db.QueryRow("SELECT "+strings.Join(names, ", ")+" FROM v WHERE id = $1", 1).Scan(targets...); err != nil {
			t.Logf("read directly, the row cannot be read at all: %v", err)
			return
		}
		for i, v := range direct {
			if !reflect.DeepEqual(v, want[i]) {
				t.Logf("read directly, column %q arrives as %T (%v)", table.Columns[i].Name, v, v)
			}
		}
	})
}

func TestPostgresContainsExpr(t *testing.T) {
	forEachPostgresDriver(t, func(t *testing.T, open func(*testing.T) *sql.DB) {
		checkContainsExpr(t, postgres.New(), open(t))
	})
}
