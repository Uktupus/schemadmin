package sqlite

import (
	"reflect"
	"strings"
	"testing"
)

func TestCheckEnums(t *testing.T) {
	tests := []struct {
		name string
		ddl  string
		want map[string][]string
	}{
		{
			name: "column constraint",
			ddl:  `CREATE TABLE t (status TEXT CHECK (status IN ('draft', 'published')))`,
			want: map[string][]string{"status": {"draft", "published"}},
		},
		{
			name: "table constraint with a name",
			ddl:  `CREATE TABLE t (status TEXT, CONSTRAINT status_ok CHECK (status IN ('a','b','c')))`,
			want: map[string][]string{"status": {"a", "b", "c"}},
		},
		{
			name: "keywords in any case and odd spacing",
			ddl:  "create table t (status text check(\n\tstatus  in('a' ,'b')\n))",
			want: map[string][]string{"status": {"a", "b"}},
		},
		{
			name: "column name is folded to lower case",
			ddl:  `CREATE TABLE t (Status TEXT CHECK (STATUS IN ('a')))`,
			want: map[string][]string{"status": {"a"}},
		},
		{
			name: "quoted column names",
			ddl:  "CREATE TABLE t (\"my col\" TEXT CHECK (\"my col\" IN ('a')), `b` TEXT CHECK (`b` IN ('x')), [c d] TEXT CHECK ([c d] IN ('y')))",
			want: map[string][]string{"my col": {"a"}, "b": {"x"}, "c d": {"y"}},
		},
		{
			name: "escaped quotes, commas and parentheses inside values",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ('it''s', 'a,b', 'c)d', '')))`,
			want: map[string][]string{"s": {"it's", "a,b", "c)d", ""}},
		},
		{
			name: "duplicate values collapse",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ('a', 'b', 'a')))`,
			want: map[string][]string{"s": {"a", "b"}},
		},
		{
			name: "several columns",
			ddl:  `CREATE TABLE t (a TEXT CHECK (a IN ('x')), b TEXT CHECK (b IN ('y', 'z')))`,
			want: map[string][]string{"a": {"x"}, "b": {"y", "z"}},
		},
		{
			name: "comments are skipped",
			ddl:  "CREATE TABLE t (\n  s TEXT -- CHECK (s IN ('no'))\n  /* CHECK (s IN ('nope')) */ CHECK (s IN ('yes'))\n)",
			want: map[string][]string{"s": {"yes"}},
		},
		{
			name: "a CHECK inside a string literal is not a constraint",
			ddl:  `CREATE TABLE t (s TEXT DEFAULT 'CHECK (s IN (''a''))')`,
			want: map[string][]string{},
		},
		{
			name: "a column named check is not the keyword",
			ddl:  `CREATE TABLE t ("check" TEXT, s TEXT)`,
			want: map[string][]string{},
		},

		// Shapes that are deliberately not recognised.
		{
			name: "NOT IN",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s NOT IN ('a', 'b')))`,
			want: map[string][]string{},
		},
		{
			name: "list combined with another condition",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ('a', 'b') OR s IS NULL))`,
			want: map[string][]string{},
		},
		{
			name: "condition before the list",
			ddl:  `CREATE TABLE t (s TEXT, n INT CHECK (n > 0 AND s IN ('a', 'b')))`,
			want: map[string][]string{},
		},
		{
			name: "numeric values",
			ddl:  `CREATE TABLE t (n INT CHECK (n IN (1, 2, 3)))`,
			want: map[string][]string{},
		},
		{
			name: "mixed string and number",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ('a', 1)))`,
			want: map[string][]string{},
		},
		{
			name: "blob literal",
			ddl:  `CREATE TABLE t (s BLOB CHECK (s IN (x'00', x'01')))`,
			want: map[string][]string{},
		},
		{
			name: "double-quoted values are identifiers, not strings",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ("a", "b")))`,
			want: map[string][]string{},
		},
		{
			name: "expression on the left",
			ddl:  `CREATE TABLE t (s TEXT CHECK (lower(s) IN ('a', 'b')))`,
			want: map[string][]string{},
		},
		{
			name: "subquery",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN (SELECT 'a')))`,
			want: map[string][]string{},
		},
		{
			name: "empty list",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ()))`,
			want: map[string][]string{},
		},
		{
			name: "trailing comma",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ('a',)))`,
			want: map[string][]string{},
		},
		{
			name: "two lists on one column",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ('a', 'b')) CHECK (s IN ('b', 'c')))`,
			want: map[string][]string{},
		},
		{
			name: "truncated statement",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ('a', 'b'`,
			want: map[string][]string{},
		},
		{
			name: "unterminated string",
			ddl:  `CREATE TABLE t (s TEXT CHECK (s IN ('a', 'b)))`,
			want: map[string][]string{},
		},
		{name: "empty", ddl: "", want: map[string][]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkEnums(tt.ddl); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("checkEnums(%s)\n got %v\nwant %v", tt.ddl, got, tt.want)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []token
	}{
		{
			name: "words, punctuation and quoting styles",
			sql:  "a \"b c\" `d` [e f] 'g''h' (1.5);",
			want: []token{
				{tokWord, "a"}, {tokIdent, "b c"}, {tokIdent, "d"}, {tokIdent, "e f"}, {tokString, "g'h"},
				{tokPunct, "("}, {tokWord, "1"}, {tokPunct, "."}, {tokWord, "5"}, {tokPunct, ")"}, {tokPunct, ";"},
			},
		},
		{
			name: "doubled delimiters",
			sql:  "\"a\"\"b\" `c``d`",
			want: []token{{tokIdent, `a"b`}, {tokIdent, "c`d"}},
		},
		{
			name: "comments",
			sql:  "a -- line\nb /* block\nstill */ c",
			want: []token{{tokWord, "a"}, {tokWord, "b"}, {tokWord, "c"}},
		},
		{
			name: "lone minus and slash are punctuation",
			sql:  "a - b / c",
			want: []token{{tokWord, "a"}, {tokPunct, "-"}, {tokWord, "b"}, {tokPunct, "/"}, {tokWord, "c"}},
		},
		{
			name: "non-ASCII identifiers",
			sql:  "größe INT",
			want: []token{{tokWord, "größe"}, {tokWord, "INT"}},
		},
		{name: "unterminated string stops the scan", sql: "a 'b", want: []token{{tokWord, "a"}}},
		{name: "unterminated bracket stops the scan", sql: "a [b", want: []token{{tokWord, "a"}}},
		{name: "unterminated block comment stops the scan", sql: "a /* b", want: []token{{tokWord, "a"}}},
		{name: "trailing line comment", sql: "a -- b", want: []token{{tokWord, "a"}}},
		{name: "empty", sql: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenize(tt.sql); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tokenize(%q)\n got %v\nwant %v", tt.sql, got, tt.want)
			}
		})
	}
}

// FuzzCheckEnums feeds arbitrary text through the DDL scanner. It must never
// panic, and every value it reports must come from a single-quoted literal
// in the input.
func FuzzCheckEnums(f *testing.F) {
	seeds := []string{
		`CREATE TABLE t (status TEXT CHECK (status IN ('draft', 'published')))`,
		`CREATE TABLE t (s TEXT CHECK (s IN ('it''s')))`,
		`CHECK (`, `CHECK (a IN (`, `'`, `"`, "`", `[`, `/*`, `--`, `CHECK(a IN('x'))`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ddl string) {
		for column, values := range checkEnums(ddl) {
			if column != foldASCII(column) {
				t.Fatalf("column key %q is not folded", column)
			}
			if len(values) == 0 {
				t.Fatalf("column %q reported with no values", column)
			}
			for _, v := range values {
				literal := "'" + strings.ReplaceAll(v, "'", "''") + "'"
				if !strings.Contains(ddl, literal) {
					t.Fatalf("value %q does not appear as a literal in %q", v, ddl)
				}
			}
		}
	})
}
