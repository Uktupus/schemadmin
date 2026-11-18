package schemadmin

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestBuildStatements(t *testing.T) {
	meta := testMeta(t, Config{})
	users, members := meta["users"], meta["memberships"]
	d := fakeDialect{}

	const userCols = "READ(‹id›), READ(‹name›), READ(‹email›), CASE WHEN ‹password_hash› IS NULL THEN NULL ELSE 1 END, " +
		"READ(‹role›), READ(‹active›), READ(‹avatar›), READ(‹team_id›), READ(‹joined›)"

	tests := []struct {
		name     string
		sql      string
		args     []any
		wantSQL  string
		wantArgs []any
	}{}
	add := func(name, sql string, args []any, wantSQL string, wantArgs ...any) {
		tests = append(tests, struct {
			name     string
			sql      string
			args     []any
			wantSQL  string
			wantArgs []any
		}{name, sql, args, wantSQL, wantArgs})
	}

	q := &listQuery{limit: 51, offset: 100, sort: []*colMeta{users.byName["id"]}}
	sql, args := buildList(d, users, q)
	add("list", sql, args,
		"SELECT "+userCols+" FROM ‹users› ORDER BY ‹id› LIMIT $1 OFFSET $2", int64(51), int64(100))

	q = &listQuery{
		search:     `50%_\`,
		searchCols: []*colMeta{users.byName["name"], users.byName["email"]},
		exact:      []filter{{col: users.byName["id"], op: opEq, arg: int64(50)}},
		filters: []filter{
			{col: users.byName["role"], op: opEq, arg: "admin"},
			{col: users.byName["team_id"], op: opIsNull},
			{col: users.byName["email"], op: opNotNull},
			{col: users.byName["joined"], op: opGTE, arg: "2024-01-01"},
			{col: users.byName["joined"], op: opLTE, arg: "2024-12-31"},
		},
		sort:  []*colMeta{users.byName["name"], users.byName["id"]},
		desc:  true,
		limit: 11,
	}
	const where = ` WHERE (‹name› CONTAINS $1 OR ‹email› CONTAINS $2 OR ‹id› = $3) AND ‹role› = $4 AND ‹team_id› IS NULL` +
		` AND ‹email› IS NOT NULL AND ‹joined› >= $5 AND ‹joined› <= $6`
	sql, args = buildList(d, users, q)
	add("list with search, filters and order", sql, args,
		"SELECT "+userCols+" FROM ‹users›"+where+" ORDER BY ‹name› DESC, ‹id› LIMIT $7 OFFSET $8",
		`%50\%\_\\%`, `%50\%\_\\%`, int64(50), "admin", "2024-01-01", "2024-12-31", int64(11), int64(0))

	sql, args = buildCount(d, users, q)
	add("count", sql, args, "SELECT COUNT(*) FROM ‹users›"+where,
		`%50\%\_\\%`, `%50\%\_\\%`, int64(50), "admin", "2024-01-01", "2024-12-31")

	sql, args = buildCount(d, users, &listQuery{search: "x"})
	add("search with nothing to search in matches nothing", sql, args,
		"SELECT COUNT(*) FROM ‹users› WHERE (1 = 0)")

	sql, args = buildGet(d, members, []any{int64(1), int64(2)})
	add("get by composite key", sql, args,
		"SELECT READ(‹user_id›), READ(‹team_id›), READ(‹note›) FROM ‹memberships› WHERE ‹user_id› = $1 AND ‹team_id› = $2",
		int64(1), int64(2))

	sql, args = buildInsert(d, users, []*Column{users.byName["name"].c, users.byName["email"].c}, []any{"Ada", nil}, users.pk)
	add("insert", sql, args,
		"INSERT INTO ‹users› (‹name›, ‹email›) VALUES ($1, $2) RETURNING READ(‹id›)", "Ada", nil)

	sql, args = buildInsert(d, users, nil, nil, nil)
	add("insert of nothing", sql, args, "INSERT INTO ‹users› DEFAULT VALUES")

	sql, args = buildUpdate(d, members, []*Column{members.byName["note"].c}, []any{"x"}, []any{int64(1), int64(2)})
	add("update", sql, args,
		"UPDATE ‹memberships› SET ‹note› = $1 WHERE ‹user_id› = $2 AND ‹team_id› = $3", "x", int64(1), int64(2))

	sql, args = buildDelete(d, members, []any{int64(1), int64(2)})
	add("delete", sql, args,
		"DELETE FROM ‹memberships› WHERE ‹user_id› = $1 AND ‹team_id› = $2", int64(1), int64(2))

	sql, args = buildCountWhere(d, members.t, []*Column{members.byName["user_id"].c}, []any{int64(7)})
	add("dependents", sql, args, "SELECT COUNT(*) FROM ‹memberships› WHERE ‹user_id› = $1", int64(7))

	sql, args = buildLabels(d, users, users.byName["id"], users.byName["name"], []any{int64(1), int64(2)})
	add("labels", sql, args,
		"SELECT READ(‹id›), READ(‹name›) FROM ‹users› WHERE ‹id› IN ($1, $2)", int64(1), int64(2))

	sql, args = buildOptions(d, users, []*colMeta{users.byName["id"]}, users.byName["name"], "ad", 20)
	add("options", sql, args,
		"SELECT READ(‹id›), READ(‹name›) FROM ‹users› WHERE ‹id› CONTAINS $1 OR ‹name› CONTAINS $2 ORDER BY ‹name›, ‹id› LIMIT $3",
		"%ad%", "%ad%", int64(20))

	selectExprOutsideList := regexp.MustCompile(`( FROM | WHERE | ORDER BY | SET ).*READ\(`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.sql != tt.wantSQL {
				t.Errorf("sql:\n got %s\nwant %s", tt.sql, tt.wantSQL)
			}
			if len(tt.args) != len(tt.wantArgs) || (len(tt.args) > 0 && !reflect.DeepEqual(tt.args, tt.wantArgs)) {
				t.Errorf("args:\n got %#v\nwant %#v", tt.args, tt.wantArgs)
			}
			// The rule: SelectExpr is for select lists. After FROM, WHERE,
			// ORDER BY or SET the column must be named bare. RETURNING is
			// a select list and is cut off before checking.
			body, _, _ := strings.Cut(tt.sql, " RETURNING ")
			if selectExprOutsideList.MatchString(body) {
				t.Errorf("SelectExpr used outside the select list: %s", tt.sql)
			}
			// Every value is bound: no argument text appears in the SQL.
			for _, a := range tt.args {
				if s, ok := a.(string); ok && s != "" && strings.Contains(tt.sql, s) {
					t.Errorf("argument %q appears in the statement text", s)
				}
			}
		})
	}
}

// Identifiers reach SQL only through QuoteIdent, whatever they contain.
func TestIdentifiersAreQuoted(t *testing.T) {
	const tableName, colName = `x"; DROP TABLE y; --`, `a' OR '1'='1`
	schema := &Schema{Tables: []*Table{{
		Schema: `s"s`,
		Name:   tableName,
		Columns: []*Column{
			{Name: "id", Kind: KindInt},
			{Name: colName, Kind: KindString, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}}}
	cfg := Config{PageSize: 10}
	_, byKey, err := buildMeta(schema, &cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	tm := byKey[tableName]
	col := tm.byName[colName]
	d := fakeDialect{}
	q := &listQuery{search: "s", searchCols: []*colMeta{col}, filters: []filter{{col: col, op: opEq, arg: "v"}}, sort: []*colMeta{col}, limit: 1}

	var statements []string
	collect := func(sql string, _ []any) { statements = append(statements, sql) }
	collect(buildList(d, tm, q))
	collect(buildCount(d, tm, q))
	collect(buildGet(d, tm, []any{int64(1)}))
	collect(buildInsert(d, tm, []*Column{col.c}, []any{"v"}, tm.pk))
	collect(buildUpdate(d, tm, []*Column{col.c}, []any{"v"}, []any{int64(1)}))
	collect(buildDelete(d, tm, []any{int64(1)}))
	collect(buildCountWhere(d, tm.t, []*Column{col.c}, []any{"v"}))
	collect(buildLabels(d, tm, tm.pk[0], col, []any{int64(1)}))
	collect(buildOptions(d, tm, []*colMeta{tm.pk[0]}, col, "s", 5))

	quoted := regexp.MustCompile(`‹[^›]*›`)
	for _, sql := range statements {
		if !strings.Contains(sql, "‹s\"s›.‹"+tableName+"›") {
			t.Errorf("table is not schema-qualified and quoted: %s", sql)
		}
		rest := quoted.ReplaceAllString(sql, "")
		for _, name := range []string{"DROP", "OR '1'", `s"s`} {
			if strings.Contains(rest, name) {
				t.Errorf("identifier text outside quotes: %s", sql)
			}
		}
	}
}

func TestEscapeLike(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"100%", `100\%`},
		{"a_b", `a\_b`},
		{`c:\dir`, `c:\\dir`},
		{`\%_`, `\\\%\_`},
	}
	for _, tt := range tests {
		if got := escapeLike(tt.in); got != tt.want {
			t.Errorf("escapeLike(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
