package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"reflect"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/mysql"
)

const mysqlDSNEnv = "SCHEMADMIN_MYSQL_DSN"

func TestMysqlIntrospect(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set: skipping the MySQL tests. Run scripts/mysql-test.sh to start a throwaway server and run them.", mysqlDSNEnv)
	}
	admin := openMysqlAdmin(t, base)
	loadMysqlFixture(t, admin)

	var recorder logRecorder
	opts := schemadmin.IntrospectOptions{Schemas: []string{"app", "billing"}, Logger: recorder.logger()}
	checkGolden(t, mysql.New(), admin, opts, "mysql_schema.golden")

	if records := recorder.records(t); len(records) != 0 {
		t.Errorf("introspection logged %v, want nothing", records)
	}
}

func TestMysqlIntrospectOptions(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set", mysqlDSNEnv)
	}
	db := openMysql(t, base)
	admin := openMysqlAdmin(t, base)
	d := mysql.New()
	ctx := context.Background()

	schema := mustIntrospect(t, d, db, schemadmin.IntrospectOptions{})
	if len(schema.Tables) != 0 {
		t.Errorf("got %d tables from an empty database, want 0", len(schema.Tables))
	}

	mustExec(t, admin, "CREATE DATABASE a")
	mustExec(t, admin, "CREATE DATABASE b")
	mustExec(t, admin, "CREATE TABLE a.t (id INT PRIMARY KEY) ENGINE=InnoDB")
	mustExec(t, admin, "CREATE TABLE b.t (id INT PRIMARY KEY) ENGINE=InnoDB")

	tests := []struct {
		name    string
		schemas []string
		want    string
	}{
		{"default is connection database", nil, ""},
		{"one schema", []string{"a"}, "a.t"},
		{"schemas in the order given", []string{"b", "a"}, "b.t a.t"},
		{"a repeated schema is read once", []string{"a", "b", "a"}, "a.t b.t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "default is connection database" {
				// openMysql selects a test database; empty Schemas reads it.
				testDB := openMysql(t, base)
				mustExec(t, testDB, "CREATE TABLE only_here (id INT PRIMARY KEY) ENGINE=InnoDB")
				schema := mustIntrospect(t, d, testDB, schemadmin.IntrospectOptions{})
				var names []string
				for _, table := range schema.Tables {
					names = append(names, table.Schema+"."+table.Name)
				}
				if len(names) != 1 || names[0] == "" {
					t.Fatalf("tables = %q", names)
				}
				return
			}
			schema := mustIntrospect(t, d, admin, schemadmin.IntrospectOptions{Schemas: tt.schemas})
			var names []string
			for _, table := range schema.Tables {
				names = append(names, table.Schema+"."+table.Name)
			}
			if got := strings.Join(names, " "); got != tt.want {
				t.Errorf("tables = %q, want %q", got, tt.want)
			}
		})
	}

	_, err := d.Introspect(ctx, admin, schemadmin.IntrospectOptions{Schemas: []string{"a", "typo"}})
	if err == nil || !strings.Contains(err.Error(), `"typo"`) {
		t.Errorf("missing schema: error = %v, want it to name the missing schema", err)
	}
	if _, err := d.Introspect(ctx, admin, schemadmin.IntrospectOptions{Schemas: []string{""}}); err == nil {
		t.Error("empty schema name: got no error")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Introspect(cancelled, admin, schemadmin.IntrospectOptions{}); err == nil {
		t.Error("cancelled context: got no error")
	}
}

func TestMysqlNullability(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set", mysqlDSNEnv)
	}
	probes := []nullProbe{
		{"plain column", "CREATE TABLE t (id INT PRIMARY KEY, v TEXT) ENGINE=InnoDB", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"not null column", "CREATE TABLE t (id INT PRIMARY KEY, v TEXT NOT NULL) ENGINE=InnoDB", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"nullable column with default", "CREATE TABLE t (id INT PRIMARY KEY, v VARCHAR(32) DEFAULT 'x') ENGINE=InnoDB", "v", "INSERT INTO t (id, v) VALUES (1, NULL)"},
		{"primary key", "CREATE TABLE t (id INT PRIMARY KEY, v TEXT) ENGINE=InnoDB", "id", "INSERT INTO t (id, v) VALUES (NULL, 'x')"},
		{"auto increment", "CREATE TABLE t (id INT AUTO_INCREMENT PRIMARY KEY, v TEXT) ENGINE=InnoDB", "id", "INSERT INTO t (id, v) VALUES (NULL, 'x')"},
		{"generated column", "CREATE TABLE t (id INT PRIMARY KEY, v TEXT, n INT GENERATED ALWAYS AS (CHAR_LENGTH(v)) STORED) ENGINE=InnoDB", "n", "INSERT INTO t (id, v) VALUES (1, NULL)"},
	}
	checkNullability(t, mysql.New(), func(t *testing.T) *sql.DB { return openMysql(t, base) }, probes)
}

func TestMysqlRequiredColumns(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set", mysqlDSNEnv)
	}
	admin := openMysqlAdmin(t, base)
	loadMysqlFixture(t, admin)

	d := mysql.New()
	opts := schemadmin.IntrospectOptions{Schemas: []string{"app", "billing"}}
	schema := mustIntrospect(t, d, admin, opts)

	admin.Exec("SET FOREIGN_KEY_CHECKS=0")
	checkRequiredColumns(t, d, admin, schema)
}

func TestMysqlHostileIdentifiers(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set", mysqlDSNEnv)
	}
	db := openMysql(t, base)
	var dbName string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	checkHostileIdentifiers(t, mysql.New(), db, dbName)
}

func TestMysqlHostileSchema(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set", mysqlDSNEnv)
	}
	admin := openMysqlAdmin(t, base)
	checkHostileIdentifiers(t, mysql.New(), admin, hostileSchemaName)
}

func TestMysqlClassifyError(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set", mysqlDSNEnv)
	}
	db := openMysql(t, base)
	mustExec(t, db, `
		CREATE TABLE parents (
			id   INT PRIMARY KEY,
			name VARCHAR(64) NOT NULL UNIQUE,
			n    INT,
			kind VARCHAR(8),
			CONSTRAINT parents_n CHECK (n > 0),
			CONSTRAINT parents_kind CHECK (kind IN ('a', 'b'))
		) ENGINE=InnoDB;
		CREATE TABLE children (
			id        INT PRIMARY KEY,
			parent_id INT NOT NULL,
			FOREIGN KEY (parent_id) REFERENCES parents (id)
		) ENGINE=InnoDB;
		CREATE TABLE strict_children (
			id        INT PRIMARY KEY,
			parent_id INT,
			FOREIGN KEY (parent_id) REFERENCES parents (id) ON DELETE RESTRICT
		) ENGINE=InnoDB;
		CREATE TABLE pairs (a INT, b INT, PRIMARY KEY (a, b)) ENGINE=InnoDB;
		INSERT INTO parents (id, name, n) VALUES (1, 'first', 1), (2, 'restricted', 1);
		INSERT INTO children (id, parent_id) VALUES (1, 1);
		INSERT INTO strict_children (id, parent_id) VALUES (1, 2);
		INSERT INTO pairs VALUES (1, 2);
	`)
	checkClassifyError(t, mysql.New(), db, []constraintCase{
		{name: "success", query: "INSERT INTO parents (id, name) VALUES (10, 'second')", want: schemadmin.ConstraintNone, ok: true},
		{name: "unique column", query: "INSERT INTO parents (id, name) VALUES (11, 'first')", want: schemadmin.ConstraintUnique},
		{name: "duplicate primary key", query: "INSERT INTO parents (id, name) VALUES (1, 'third')", want: schemadmin.ConstraintUnique},
		{name: "duplicate composite key", query: "INSERT INTO pairs VALUES (1, 2)", want: schemadmin.ConstraintUnique},
		{name: "not null", query: "INSERT INTO parents (id, name) VALUES (12, NULL)", want: schemadmin.ConstraintNotNull},
		{name: "not null by omission", query: "INSERT INTO children (id) VALUES (2)", want: schemadmin.ConstraintNotNull},
		{name: "check", query: "INSERT INTO parents (id, name, n) VALUES (13, 'fourth', 0)", want: schemadmin.ConstraintCheck},
		{name: "named check", query: "INSERT INTO parents (id, name, kind) VALUES (14, 'fifth', 'z')", want: schemadmin.ConstraintCheck},
		{name: "foreign key on insert", query: "INSERT INTO children (id, parent_id) VALUES (3, 999)", want: schemadmin.ConstraintForeignKey},
		{name: "foreign key on update", query: "UPDATE children SET parent_id = 999", want: schemadmin.ConstraintForeignKey},
		{name: "foreign key on delete", query: "DELETE FROM parents WHERE id = 1", want: schemadmin.ConstraintForeignKey},
		{name: "foreign key on delete restrict", query: "DELETE FROM parents WHERE id = 2", want: schemadmin.ConstraintForeignKey},
		{name: "syntax error", query: "INSERT INTO", want: schemadmin.ConstraintNone},
		{name: "missing table", query: "INSERT INTO nowhere VALUES (1)", want: schemadmin.ConstraintNone},
	})
}

func TestMysqlSelectExpr(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set", mysqlDSNEnv)
	}
	db := openMysql(t, base)
	mustExec(t, db, "SET time_zone = '+00:00'")
	mustExec(t, db, `
		CREATE TABLE v (
			id     INT PRIMARY KEY,
			flag   TINYINT(1),
			n      INT,
			big    BIGINT,
			r      FLOAT,
			f      DOUBLE,
			amount DECIMAL(10,2),
			s      TEXT,
			vc     VARCHAR(10),
			ch     CHAR(4),
			d      DATE,
			tm     TIME(6),
			ts     DATETIME(6),
			tz     TIMESTAMP(6),
			doc    JSON,
			e      ENUM('sad','ok','happy'),
			b      BLOB
		) ENGINE=InnoDB;
		INSERT INTO v VALUES (1, 1, 42, 9007199254740993, 0.1, 0.3,
			12.30, 'text', 'abc', 'ab  ',
			'2024-01-02', '03:04:05.25', '2024-01-02 03:04:05.5', '2024-01-02 03:04:05.123456',
			'{"b": 1, "a": [1, 2]}', 'ok', 0x0102);
		INSERT INTO v (id) VALUES (2);
	`)
	d := mysql.New()
	table := mustTable(t, mustIntrospect(t, d, db, schemadmin.IntrospectOptions{}), "v")

	var exprs []string
	for _, c := range table.Columns {
		exprs = append(exprs, d.SelectExpr(d.QuoteIdent(c.Name), c))
	}
	query := "SELECT " + strings.Join(exprs, ", ") + " FROM v WHERE id = ?"

	want := []any{
		int64(1),
		int64(1),
		int64(42),
		int64(9007199254740993),
		"0.1",
		"0.3",
		"12.30",
		"text",
		"abc",
		"ab",
		"2024-01-02",
		"03:04:05.25",
		"2024-01-02T03:04:05.5",
		"2024-01-02T03:04:05.123456",
		`{"a": [1, 2], "b": 1}`,
		"ok",
		[]byte("0102"), // hex text; the driver may still return CHAR as []byte
	}
	got := scanValues(t, db, len(want), query, 1)
	for i := range want {
		if table.Columns[i].Kind != schemadmin.KindBytes {
			got[i] = normalizeMysqlValue(got[i])
		}
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("column %q: read %#v (%T), want %#v (%T)", table.Columns[i].Name, got[i], got[i], want[i], want[i])
		}
	}
	for i, v := range scanValues(t, db, len(want), query, 2)[1:] {
		if v != nil {
			t.Errorf("column %q: NULL read as %#v", table.Columns[i+1].Name, v)
		}
	}
}

func TestMysqlContainsExpr(t *testing.T) {
	base := os.Getenv(mysqlDSNEnv)
	if base == "" {
		t.Skipf("%s is not set", mysqlDSNEnv)
	}
	checkContainsExpr(t, mysql.New(), openMysql(t, base))
}

func ensureMysqlDSN(base string) string {
	if strings.Contains(base, "multiStatements=") {
		return base
	}
	if strings.Contains(base, "?") {
		return base + "&multiStatements=true"
	}
	if strings.HasSuffix(base, "/") {
		return base + "mysql?multiStatements=true"
	}
	return base + "?multiStatements=true"
}

func openMysqlAdmin(t *testing.T, base string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", ensureMysqlDSN(base))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func openMysql(t *testing.T, base string) *sql.DB {
	t.Helper()
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "schemadmin_test_" + hex.EncodeToString(suffix)

	admin, err := sql.Open("mysql", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}

	dsn := ensureMysqlDSN(withMysqlDatabase(base, name))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec("DROP DATABASE " + name); err != nil {
			t.Errorf("drop test database: %v", err)
		}
		admin.Close()
	})
	return db
}

func withMysqlDatabase(base, dbName string) string {
	query := ""
	prefix := base
	if i := strings.Index(base, "?"); i >= 0 {
		prefix, query = base[:i], base[i:]
	}
	slash := strings.LastIndex(prefix, "/")
	if slash < 0 {
		return prefix + "/" + dbName + query
	}
	return prefix[:slash+1] + dbName + query
}

func loadMysqlFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range splitSQLStatements(readTestdata(t, "mysql_schema.sql")) {
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture: exec %q: %v", stmt, err)
		}
	}
}

func normalizeMysqlValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func splitSQLStatements(sqlText string) []string {
	var out []string
	var b strings.Builder
	lines := strings.Split(sqlText, "\n")
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		out = append(out, s)
	}
	return out
}
