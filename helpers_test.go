package schemadmin

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeDialect returns a canned schema and writes SQL in a way that makes
// the origin of every fragment visible: identifiers are wrapped in ‹›,
// column reads in READ(), text matches use CONTAINS.
type fakeDialect struct {
	schema *Schema
}

func (fakeDialect) Name() string { return "fake" }
func (f fakeDialect) Introspect(context.Context, *sql.DB, IntrospectOptions) (*Schema, error) {
	return f.schema, nil
}
func (fakeDialect) QuoteIdent(name string) string             { return "‹" + name + "›" }
func (fakeDialect) Placeholder(n int) string                  { return "$" + strconv.Itoa(n) }
func (fakeDialect) SelectExpr(q string, _ *Column) string     { return "READ(" + q + ")" }
func (fakeDialect) ContainsExpr(q, placeholder string) string { return q + " CONTAINS " + placeholder }
func (fakeDialect) ClassifyError(error) ConstraintKind        { return ConstraintNone }

// stubDB is a database in which every statement fails with a message that
// must never reach a page, and which counts how often it was asked.
type stubDB struct {
	calls atomic.Int64
}

const stubSecret = `pq: syntax error at or near "SELECT secret_column FROM users"`

type stubConn struct{ db *stubDB }

func (c stubConn) Prepare(string) (driver.Stmt, error) {
	c.db.calls.Add(1)
	return nil, errors.New(stubSecret)
}
func (c stubConn) Close() error { return nil }
func (c stubConn) Begin() (driver.Tx, error) {
	c.db.calls.Add(1)
	return nil, errors.New(stubSecret)
}

type stubConnector struct{ db *stubDB }

func (c stubConnector) Connect(context.Context) (driver.Conn, error) { return stubConn(c), nil }
func (c stubConnector) Driver() driver.Driver                        { return stubDriver{} }

type stubDriver struct{}

func (stubDriver) Open(string) (driver.Conn, error) { return nil, errors.New("not used") }

func (s *stubDB) open(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(stubConnector{s})
	t.Cleanup(func() { db.Close() })
	return db
}

// testSchema is a small schema with the features the unit tests need: a
// credential column, a binary column, a foreign key and a composite key.
func testSchema() *Schema {
	return &Schema{Tables: []*Table{
		{
			Name: "teams",
			Columns: []*Column{
				{Name: "id", DBType: "integer", Kind: KindInt, HasDefault: true, AutoIncrement: true},
				{Name: "name", DBType: "varchar(40)", Kind: KindString, MaxLength: 40},
			},
			PrimaryKey: []string{"id"},
		},
		{
			Name: "users",
			Columns: []*Column{
				{Name: "id", DBType: "integer", Kind: KindInt, HasDefault: true, AutoIncrement: true},
				{Name: "name", DBType: "text", Kind: KindText},
				{Name: "email", DBType: "varchar(80)", Kind: KindString, MaxLength: 80, Nullable: true},
				{Name: "password_hash", DBType: "text", Kind: KindText},
				{Name: "role", DBType: "role", Kind: KindEnum, EnumValues: []string{"admin", "member"}, HasDefault: true, Default: "'member'"},
				{Name: "active", DBType: "boolean", Kind: KindBool},
				{Name: "avatar", DBType: "bytea", Kind: KindBytes, Nullable: true},
				{Name: "team_id", DBType: "integer", Kind: KindInt, Nullable: true},
				{Name: "joined", DBType: "date", Kind: KindDate, Nullable: true},
			},
			PrimaryKey: []string{"id"},
			ForeignKeys: []*ForeignKey{
				{Name: "users_team", Columns: []string{"team_id"}, RefTable: "teams", RefColumns: []string{"id"}, OnDelete: FKSetNull},
			},
		},
		{
			Name: "memberships",
			Columns: []*Column{
				{Name: "user_id", DBType: "integer", Kind: KindInt},
				{Name: "team_id", DBType: "integer", Kind: KindInt},
				{Name: "note", DBType: "text", Kind: KindText, Nullable: true},
			},
			PrimaryKey: []string{"user_id", "team_id"},
			ForeignKeys: []*ForeignKey{
				{Name: "m_user", Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"}, OnDelete: FKCascade},
				{Name: "m_team", Columns: []string{"team_id"}, RefTable: "teams", RefColumns: []string{"id"}},
			},
		},
	}}
}

func testMeta(t *testing.T, cfg Config) map[string]*tableMeta {
	t.Helper()
	if cfg.PageSize == 0 {
		cfg.PageSize = defaultPageSize
	}
	_, byKey, err := buildMeta(testSchema(), &cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return byKey
}

// allow is an Authorizer built from a predicate, counting its calls.
type allow struct {
	fn    func(action Action, table string) bool
	calls atomic.Int64
}

func (a *allow) Authorize(_ *http.Request, action Action, table string) bool {
	a.calls.Add(1)
	return a.fn(action, table)
}

func allowAll(Action, string) bool { return true }

// logBuffer collects log output for inspection.
type logBuffer struct{ bytes.Buffer }

func (b *logBuffer) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&b.Buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (b *logBuffer) contains(s string) bool { return strings.Contains(b.String(), s) }
