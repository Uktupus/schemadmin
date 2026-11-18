package schemadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// value is a database value as the admin carries it: NULL, or text. The text
// of a given stored value is always the same, so two values can be compared
// by comparing their text.
type value struct {
	Null bool
	S    string
}

// scanValue converts what a driver returned for a column of the given kind.
// Dialect.SelectExpr has already reduced the possibilities to a few Go
// types; this covers the leftovers drivers still return.
func scanValue(kind Kind, raw any) value {
	var s string
	switch v := raw.(type) {
	case nil:
		return value{Null: true}
	case bool:
		s = strconv.FormatBool(v)
	case int64:
		switch {
		case kind == KindBool && v == 0:
			s = "false"
		case kind == KindBool && v == 1:
			s = "true"
		default:
			s = strconv.FormatInt(v, 10)
		}
	case float64:
		s = strconv.FormatFloat(v, 'g', -1, 64)
	case string:
		s = v
	case []byte:
		if kind == KindBytes {
			// SelectExpr returns hex text; some drivers still hand it
			// over as []byte. Raw blobs (no SelectExpr) are encoded.
			if isHexDigits(v) {
				s = strings.ToLower(string(v))
			} else {
				s = strings.ToLower(hex.EncodeToString(v))
			}
		} else {
			s = string(v)
		}
	case time.Time:
		s = v.Format(time.RFC3339Nano)
	default:
		s = fmt.Sprint(v)
	}
	if kind == KindBytes {
		s = strings.ToLower(strings.TrimSpace(s))
		if strings.HasPrefix(s, "\\x") { // PostgreSQL bytea hex escape, if ever scanned raw
			s = s[2:]
		}
	}
	// PostgreSQL hands timestamptz over in UTC without an offset. Say so,
	// so the text means the same instant when it is sent back.
	if kind == KindDateTimeTZ && isZonelessDateTime(s) {
		s += "Z"
	}
	return value{S: s}
}

// representable reports whether s survives a trip through an HTML form
// unchanged. A value that does not is shown but never offered for editing.
func representable(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// normalizeNewlines maps CRLF and lone CR to LF. Browsers submit every line
// break as CRLF whatever was stored, so values are compared in this form.
func normalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// fingerprint identifies a value without carrying it. Edit forms hold the
// fingerprint of each field as loaded; a submitted field with the same
// fingerprint was not changed and is left out of the UPDATE.
func fingerprint(v value) string {
	if v.Null {
		return "n"
	}
	sum := sha256.Sum256([]byte(normalizeNewlines(v.S)))
	return "v" + hex.EncodeToString(sum[:16])
}

var (
	dateTimeLayouts = []string{
		"2006-01-02 15:04:05", "2006-01-02T15:04:05",
		"2006-01-02 15:04", "2006-01-02T15:04",
		"2006-01-02",
	}
	timeLayouts = []string{"15:04:05", "15:04"}
	zoneLayouts = []string{"Z07:00", "Z07", " Z07:00", " Z07", "Z0700"}
)

func parsesWith(s string, layouts, zones []string) bool {
	for _, l := range layouts {
		if _, err := time.Parse(l, s); err == nil {
			return true
		}
		for _, z := range zones {
			if _, err := time.Parse(l+z, s); err == nil {
				return true
			}
		}
	}
	return false
}

func isZonelessDateTime(s string) bool {
	return parsesWith(s, dateTimeLayouts[:4], nil)
}

func isDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil && len(s) == 10
}

func isDecimal(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		exp := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			exp++
		}
		if exp == 0 {
			return false
		}
	}
	return i == len(s)
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// parseInput validates text entered for a column and returns the argument
// to bind. The second result is a message for the user, empty on success.
//
// Integers, floats and booleans are bound as Go values of that type and
// everything else as text, which every supported database converts to the
// column's type itself.
func parseInput(c *Column, choices []string, s string) (any, string) {
	if len(choices) > 0 {
		found := false
		for _, v := range choices {
			if v == s {
				found = true
				break
			}
		}
		if !found {
			return nil, "Choose one of the listed values."
		}
	}
	switch c.Kind {
	case KindInt:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, "Enter a whole number."
		}
		return n, ""
	case KindFloat:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, "Enter a number."
		}
		return f, ""
	case KindDecimal:
		t := strings.TrimSpace(s)
		if !isDecimal(t) {
			return nil, "Enter a number."
		}
		return t, ""
	case KindBool:
		switch strings.TrimSpace(s) {
		case "true", "1":
			return true, ""
		case "false", "0":
			return false, ""
		}
		return nil, "Choose true or false."
	case KindDate:
		t := strings.TrimSpace(s)
		if !isDate(t) {
			return nil, "Enter a date as YYYY-MM-DD."
		}
		return t, ""
	case KindTime:
		t := strings.TrimSpace(s)
		if !parsesWith(t, timeLayouts, zoneLayouts) {
			return nil, "Enter a time as HH:MM:SS."
		}
		return t, ""
	case KindDateTime:
		t := strings.TrimSpace(s)
		if !parsesWith(t, dateTimeLayouts, zoneLayouts) {
			return nil, "Enter a date and time as YYYY-MM-DD HH:MM:SS."
		}
		return t, ""
	case KindDateTimeTZ:
		t := strings.TrimSpace(s)
		if !parsesWith(t, dateTimeLayouts, zoneLayouts) {
			return nil, "Enter a date and time as YYYY-MM-DD HH:MM:SS, optionally followed by an offset such as +02:00."
		}
		// Without an offset the database would apply the session's time
		// zone, which the admin does not control. Read it as UTC.
		if parsesWith(t, dateTimeLayouts, nil) {
			if len(t) == 10 {
				t += " 00:00:00"
			}
			t += "Z"
		}
		return t, ""
	case KindJSON:
		if !json.Valid([]byte(s)) {
			return nil, "Enter valid JSON."
		}
		return s, ""
	case KindUUID:
		t := strings.TrimSpace(s)
		if !isUUID(t) {
			return nil, "Enter a UUID such as 6ba7b810-9dad-11d1-80b4-00c04fd430c8."
		}
		return t, ""
	case KindBytes:
		b, msg := parseHexBytes(s)
		if msg != "" {
			return nil, msg
		}
		return b, ""
	case KindString:
		if c.MaxLength > 0 && utf8.RuneCountInString(s) > c.MaxLength {
			return nil, "Enter at most " + strconv.Itoa(c.MaxLength) + " characters."
		}
	}
	if !representable(s) {
		return nil, "The value contains characters that cannot be stored."
	}
	return s, ""
}

func isHexDigits(b []byte) bool {
	if len(b)%2 != 0 {
		return false
	}
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// parseHexBytes accepts an empty value, a contiguous hex string, or hex
// with spaces/colons/newlines between bytes. It returns the decoded bytes.
func parseHexBytes(s string) ([]byte, string) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ':' || r == '-':
			continue
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			b.WriteRune(r)
		default:
			return nil, "Enter binary data as hexadecimal (e.g. 00ff10)."
		}
	}
	hexStr := b.String()
	if len(hexStr)%2 != 0 {
		return nil, "Hexadecimal values need an even number of digits."
	}
	out, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, "Enter binary data as hexadecimal (e.g. 00ff10)."
	}
	return out, ""
}

// Row keys.
//
// A row is addressed in URLs by the text of its primary-key values joined
// with commas. Inside a component "~" introduces an escape, so that any
// value, including an empty one, has exactly one encoding and that encoding
// is a single safe path segment. There is deliberately no encoding for
// NULL: a row with a NULL key value has no URL.

func escapeKeyPart(s string) string {
	if s == "" {
		return "~e"
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '~':
			b.WriteString("~t")
		case c == ',':
			b.WriteString("~c")
		case c == '/':
			b.WriteString("~s")
		case c == '.' && i == 0:
			b.WriteString("~d")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func unescapeKeyPart(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	if s == "~e" {
		return "", true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '~' {
			b.WriteByte(c)
			continue
		}
		i++
		if i == len(s) {
			return "", false
		}
		switch s[i] {
		case 't':
			b.WriteByte('~')
		case 'c':
			b.WriteByte(',')
		case 's':
			b.WriteByte('/')
		case 'd':
			b.WriteByte('.')
		default:
			return "", false
		}
	}
	return b.String(), true
}

func encodeKey(parts []string) string {
	esc := make([]string, len(parts))
	for i, p := range parts {
		esc[i] = escapeKeyPart(p)
	}
	return strings.Join(esc, ",")
}

// decodeKey splits a row key into exactly n components.
func decodeKey(s string, n int) ([]string, bool) {
	parts := strings.Split(s, ",")
	if len(parts) != n {
		return nil, false
	}
	for i, p := range parts {
		u, ok := unescapeKeyPart(p)
		if !ok {
			return nil, false
		}
		parts[i] = u
	}
	return parts, true
}
