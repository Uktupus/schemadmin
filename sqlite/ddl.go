package sqlite

import "strings"

// SQLite has no enum type. The idiomatic substitute is a CHECK constraint
// that compares a column against a list of string literals:
//
//	status TEXT CHECK (status IN ('draft', 'published'))
//
// Constraints are only available as the original CREATE TABLE text, so this
// file tokenizes that text and recognises exactly the shape above. Anything
// more elaborate is left alone and the column stays ordinary text.

type tokenKind int

const (
	tokWord   tokenKind = iota // bare keyword, identifier or number
	tokIdent                   // quoted identifier: "x", `x` or [x]
	tokString                  // single-quoted string literal
	tokPunct                   // any other single character
)

type token struct {
	kind tokenKind
	text string // for tokIdent and tokString, the unquoted content
}

func (t token) isKeyword(kw string) bool {
	return t.kind == tokWord && strings.EqualFold(t.text, kw)
}

func (t token) isPunct(c byte) bool {
	return t.kind == tokPunct && t.text == string(c)
}

// tokenize splits SQL into tokens, dropping whitespace and comments. Input
// that ends inside a quoted token or comment is truncated at that point.
func tokenize(sql string) []token {
	var toks []token
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case isSpace(c):
			i++

		case c == '-' && strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				return toks
			}
			i += end + 1

		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return toks
			}
			i += 2 + end + 2

		case c == '\'':
			text, n, ok := readQuoted(sql[i:])
			if !ok {
				return toks
			}
			toks = append(toks, token{tokString, text})
			i += n

		case c == '"' || c == '`':
			text, n, ok := readQuoted(sql[i:])
			if !ok {
				return toks
			}
			toks = append(toks, token{tokIdent, text})
			i += n

		case c == '[':
			end := strings.IndexByte(sql[i:], ']')
			if end < 0 {
				return toks
			}
			toks = append(toks, token{tokIdent, sql[i+1 : i+end]})
			i += end + 1

		case isWordByte(c):
			j := i + 1
			for j < len(sql) && isWordByte(sql[j]) {
				j++
			}
			toks = append(toks, token{tokWord, sql[i:j]})
			i = j

		default:
			toks = append(toks, token{tokPunct, sql[i : i+1]})
			i++
		}
	}
	return toks
}

// readQuoted reads a token delimited by the byte s starts with, in which a
// doubled delimiter stands for one literal delimiter. It returns the
// unquoted text and the number of bytes consumed.
func readQuoted(s string) (text string, n int, ok bool) {
	quote := s[0]
	var b strings.Builder
	for j := 1; j < len(s); j++ {
		if s[j] != quote {
			b.WriteByte(s[j])
			continue
		}
		if j+1 < len(s) && s[j+1] == quote {
			b.WriteByte(quote)
			j++
			continue
		}
		return b.String(), j + 1, true
	}
	return "", 0, false
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// checkEnums finds the columns of a CREATE TABLE statement that are
// restricted to a list of strings by a constraint of the exact form
//
//	CHECK ( column IN ( 'a', 'b', ... ) )
//
// and returns the allowed values keyed by the ASCII-lower-cased column name.
// A column constrained by more than one such list is omitted, because the
// effective set is then not simply one of them.
func checkEnums(ddl string) map[string][]string {
	toks := tokenize(ddl)
	found := make(map[string][]string)
	repeated := make(map[string]bool)
	for i, t := range toks {
		if !t.isKeyword("CHECK") {
			continue
		}
		column, values, ok := matchInList(toks[i+1:])
		if !ok {
			continue
		}
		key := foldASCII(column)
		if _, dup := found[key]; dup {
			repeated[key] = true
			continue
		}
		found[key] = values
	}
	for key := range repeated {
		delete(found, key)
	}
	return found
}

// matchInList matches the tokens "( column IN ( 'a' , 'b' ) )" at the start
// of toks.
func matchInList(toks []token) (column string, values []string, ok bool) {
	next := func() (token, bool) {
		if len(toks) == 0 {
			return token{}, false
		}
		t := toks[0]
		toks = toks[1:]
		return t, true
	}

	if t, more := next(); !more || !t.isPunct('(') {
		return "", nil, false
	}
	name, more := next()
	if !more || (name.kind != tokWord && name.kind != tokIdent) {
		return "", nil, false
	}
	if t, more := next(); !more || !t.isKeyword("IN") {
		return "", nil, false
	}
	if t, more := next(); !more || !t.isPunct('(') {
		return "", nil, false
	}

	seen := make(map[string]bool)
	for {
		lit, more := next()
		if !more || lit.kind != tokString {
			return "", nil, false
		}
		if !seen[lit.text] {
			seen[lit.text] = true
			values = append(values, lit.text)
		}
		sep, more := next()
		if !more {
			return "", nil, false
		}
		if sep.isPunct(')') {
			break
		}
		if !sep.isPunct(',') {
			return "", nil, false
		}
	}

	// The list must be the whole constraint: the CHECK closes right here.
	if t, more := next(); !more || !t.isPunct(')') {
		return "", nil, false
	}
	return name.text, values, true
}
