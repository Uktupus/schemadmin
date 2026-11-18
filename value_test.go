package schemadmin

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestScanValue(t *testing.T) {
	tests := []struct {
		name string
		kind Kind
		raw  any
		want value
	}{
		{"null", KindString, nil, value{Null: true}},
		{"string", KindString, "a", value{S: "a"}},
		{"empty string is not null", KindString, "", value{S: ""}},
		{"bytes as text", KindText, []byte("a"), value{S: "a"}},
		{"int", KindInt, int64(-7), value{S: "-7"}},
		{"bool", KindBool, true, value{S: "true"}},
		{"bool stored as 1", KindBool, int64(1), value{S: "true"}},
		{"bool stored as 0", KindBool, int64(0), value{S: "false"}},
		{"bool stored as something else", KindBool, int64(2), value{S: "2"}},
		{"float", KindFloat, 12.5, value{S: "12.5"}},
		{"float is exact", KindFloat, 0.1, value{S: "0.1"}},
		{"timestamptz gains its zone", KindDateTimeTZ, "2024-01-02T03:04:05.123456", value{S: "2024-01-02T03:04:05.123456Z"}},
		{"timestamptz with a zone is left alone", KindDateTimeTZ, "2024-01-02 03:04:05+02:00", value{S: "2024-01-02 03:04:05+02:00"}},
		{"timestamptz that is not a timestamp is left alone", KindDateTimeTZ, "infinity", value{S: "infinity"}},
		{"timestamp gains nothing", KindDateTime, "2024-01-02T03:04:05", value{S: "2024-01-02T03:04:05"}},
		{"time.Time", KindDateTime, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), value{S: "2024-01-02T03:04:05Z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanValue(tt.kind, tt.raw); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseInput(t *testing.T) {
	col := func(kind Kind) *Column { return &Column{Name: "c", Kind: kind} }
	short := &Column{Name: "c", Kind: KindString, MaxLength: 3}
	tests := []struct {
		name    string
		col     *Column
		choices []string
		in      string
		want    any
		ok      bool
	}{
		{"string", col(KindString), nil, " a ", " a ", true},
		{"empty string", col(KindString), nil, "", "", true},
		{"string at max length counts characters", short, nil, "äöü", "äöü", true},
		{"string over max length", short, nil, "abcd", nil, false},
		{"string with NUL", col(KindText), nil, "a\x00b", nil, false},
		{"string with invalid UTF-8", col(KindText), nil, "a\xffb", nil, false},
		{"unknown type passes through", col(KindOther), nil, "{1,2}", "{1,2}", true},

		{"int", col(KindInt), nil, " 42 ", int64(42), true},
		{"negative int", col(KindInt), nil, "-1", int64(-1), true},
		{"int overflow", col(KindInt), nil, "9223372036854775808", nil, false},
		{"int with fraction", col(KindInt), nil, "1.0", nil, false},
		{"int from words", col(KindInt), nil, "1 OR 1=1", nil, false},

		{"float", col(KindFloat), nil, "1.5e3", 1500.0, true},
		{"float from words", col(KindFloat), nil, "abc", nil, false},

		{"decimal keeps its digits", col(KindDecimal), nil, " 12.30 ", "12.30", true},
		{"decimal with sign and exponent", col(KindDecimal), nil, "-1.5e-3", "-1.5e-3", true},
		{"decimal without leading digit", col(KindDecimal), nil, ".5", ".5", true},
		{"decimal with two points", col(KindDecimal), nil, "1.2.3", nil, false},
		{"decimal that is only a sign", col(KindDecimal), nil, "-", nil, false},
		{"decimal with a dangling exponent", col(KindDecimal), nil, "1e", nil, false},

		{"bool true", col(KindBool), nil, "true", true, true},
		{"bool false", col(KindBool), nil, "false", false, true},
		{"bool 1", col(KindBool), nil, "1", true, true},
		{"bool yes", col(KindBool), nil, "yes", nil, false},

		{"date", col(KindDate), nil, "2024-02-29", "2024-02-29", true},
		{"date that does not exist", col(KindDate), nil, "2023-02-29", nil, false},
		{"date in another order", col(KindDate), nil, "29/02/2024", nil, false},

		{"time", col(KindTime), nil, "03:04:05.123456", "03:04:05.123456", true},
		{"time without seconds", col(KindTime), nil, "03:04", "03:04", true},
		{"time with zone", col(KindTime), nil, "03:04:05+02", "03:04:05+02", true},
		{"time out of range", col(KindTime), nil, "25:00", nil, false},

		{"datetime with space", col(KindDateTime), nil, "2024-01-02 03:04:05", "2024-01-02 03:04:05", true},
		{"datetime with T and fraction", col(KindDateTime), nil, "2024-01-02T03:04:05.123456", "2024-01-02T03:04:05.123456", true},
		{"datetime that is a date", col(KindDateTime), nil, "2024-01-02", "2024-01-02", true},
		{"datetime from words", col(KindDateTime), nil, "tomorrow", nil, false},

		{"timestamptz without a zone is UTC", col(KindDateTimeTZ), nil, "2024-01-02 03:04:05", "2024-01-02 03:04:05Z", true},
		{"timestamptz date is midnight UTC", col(KindDateTimeTZ), nil, "2024-01-02", "2024-01-02 00:00:00Z", true},
		{"timestamptz keeps Z", col(KindDateTimeTZ), nil, "2024-01-02T03:04:05.5Z", "2024-01-02T03:04:05.5Z", true},
		{"timestamptz keeps an offset", col(KindDateTimeTZ), nil, "2024-01-02 03:04:05+02:00", "2024-01-02 03:04:05+02:00", true},

		{"json", col(KindJSON), nil, ` {"a": [1, 2]} `, ` {"a": [1, 2]} `, true},
		{"json that is not", col(KindJSON), nil, "{a: 1}", nil, false},

		{"uuid", col(KindUUID), nil, "6BA7B810-9dad-11d1-80b4-00c04fd430c8", "6BA7B810-9dad-11d1-80b4-00c04fd430c8", true},
		{"uuid too short", col(KindUUID), nil, "6ba7b810", nil, false},
		{"uuid with a wrong character", col(KindUUID), nil, "6ba7b810-9dad-11d1-80b4-00c04fd430cg", nil, false},

		{"binary", col(KindBytes), nil, "abc", nil, false},

		{"choice", col(KindEnum), []string{"a", "b"}, "b", "b", true},
		{"not a choice", col(KindEnum), []string{"a", "b"}, "c", nil, false},
		{"choice is still checked against the type", col(KindInt), []string{"1", "x"}, "x", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := parseInput(tt.col, tt.choices, tt.in)
			if (msg == "") != tt.ok {
				t.Fatalf("message %q, want ok=%v", msg, tt.ok)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	same := [][2]value{
		{{S: "a\nb"}, {S: "a\r\nb"}},
		{{S: "a\rb"}, {S: "a\r\nb"}},
		{{Null: true}, {Null: true, S: "ignored"}},
	}
	for _, pair := range same {
		if fingerprint(pair[0]) != fingerprint(pair[1]) {
			t.Errorf("%+v and %+v should have the same fingerprint", pair[0], pair[1])
		}
	}
	different := [][2]value{
		{{Null: true}, {S: ""}},
		{{S: ""}, {S: " "}},
		{{S: "a"}, {S: "A"}},
		{{S: "a\n"}, {S: "a"}},
		{{S: "n"}, {Null: true}},
	}
	for _, pair := range different {
		if fingerprint(pair[0]) == fingerprint(pair[1]) {
			t.Errorf("%+v and %+v should have different fingerprints", pair[0], pair[1])
		}
	}
}

func TestKeyRoundTrip(t *testing.T) {
	tests := [][]string{
		{"1"},
		{""},
		{"", ""},
		{"a,b", "c"},
		{"~", "~e", "~~"},
		{"a/b", "../..", "."},
		{".", ".."},
		{"per%cent", "sp ace", "q?x=1#f"},
		{"ünï", "日本"},
	}
	for _, parts := range tests {
		key := encodeKey(parts)
		if strings.Contains(key, "/") || key == "." || key == ".." || key == "" {
			t.Errorf("encodeKey(%q) = %q is not a safe path segment", parts, key)
		}
		got, ok := decodeKey(key, len(parts))
		if !ok || !reflect.DeepEqual(got, parts) {
			t.Errorf("decodeKey(encodeKey(%q)) = %q, %v", parts, got, ok)
		}
	}
}

func TestDecodeKeyRejects(t *testing.T) {
	tests := []struct {
		key string
		n   int
	}{
		{"", 1},
		{"1", 2},
		{"1,2", 1},
		{"1,", 2},
		{"~", 1},
		{"~x", 1},
		{"a~", 1},
	}
	for _, tt := range tests {
		if got, ok := decodeKey(tt.key, tt.n); ok {
			t.Errorf("decodeKey(%q, %d) = %q, want rejection", tt.key, tt.n, got)
		}
	}
}

func FuzzKeyRoundTrip(f *testing.F) {
	f.Add("a", "b")
	f.Add("", ",")
	f.Add("~", "/")
	f.Add(".", "..")
	f.Fuzz(func(t *testing.T, a, b string) {
		key := encodeKey([]string{a, b})
		if strings.ContainsAny(key, "/") || strings.HasPrefix(key, ".") {
			t.Fatalf("unsafe key %q", key)
		}
		got, ok := decodeKey(key, 2)
		if !ok || got[0] != a || got[1] != b {
			t.Fatalf("round trip of (%q, %q) through %q gave %q, %v", a, b, key, got, ok)
		}
	})
}

func TestInterpret(t *testing.T) {
	text := &colMeta{c: &Column{Kind: KindText}}
	textNull := &colMeta{c: &Column{Kind: KindText, Nullable: true}}
	textDefault := &colMeta{c: &Column{Kind: KindText, HasDefault: true}}
	num := &colMeta{c: &Column{Kind: KindInt}}
	numNull := &colMeta{c: &Column{Kind: KindInt, Nullable: true}}
	numDefault := &colMeta{c: &Column{Kind: KindInt, HasDefault: true}}
	choice := &colMeta{c: &Column{Kind: KindText, Nullable: true}, choices: []string{"a"}}

	tests := []struct {
		name     string
		col      *colMeta
		creating bool
		text     string
		null     bool
		want     value
		omit     bool
		ok       bool
	}{
		{"text", text, false, "a", false, value{S: "a"}, false, true},
		{"empty text is the empty string", text, false, "", false, value{S: ""}, false, true},
		{"spaces are text", text, false, "  ", false, value{S: "  "}, false, true},
		{"NULL box on a nullable column", textNull, false, "ignored", true, value{Null: true}, false, true},
		{"empty text on a nullable column is still the empty string", textNull, false, "", false, value{S: ""}, false, true},
		{"NULL box on a NOT NULL column is ignored", text, false, "a", true, value{S: "a"}, false, true},
		{"empty number on a nullable column is NULL", numNull, false, " ", false, value{Null: true}, false, true},
		{"empty number on a NOT NULL column is an error", num, false, "", false, value{}, false, false},
		{"empty choice is NULL, not the empty string", choice, false, "", false, value{Null: true}, false, true},
		{"create: empty with a default is left to the database", textDefault, true, "", false, value{}, true, true},
		{"create: empty number with a default is left to the database", numDefault, true, "", false, value{}, true, true},
		{"create: a value overrides the default", numDefault, true, "5", false, value{S: "5"}, false, true},
		{"create: empty text without a default is the empty string", text, true, "", false, value{S: ""}, false, true},
		{"edit: empty with a default is not left out", textDefault, false, "", false, value{S: ""}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, omit, msg := interpret(tt.col, tt.creating, tt.text, tt.null)
			if (msg == "") != tt.ok {
				t.Fatalf("message %q, want ok=%v", msg, tt.ok)
			}
			if got != tt.want || omit != tt.omit {
				t.Errorf("got %+v omit=%v, want %+v omit=%v", got, omit, tt.want, tt.omit)
			}
		})
	}
}

func TestArgumentLineEndings(t *testing.T) {
	c := &colMeta{c: &Column{Kind: KindText}}
	lf, crlf, null := value{S: "x\ny"}, value{S: "x\r\ny"}, value{Null: true}
	tests := []struct {
		name    string
		current *value
		want    string
	}{
		{"new row", nil, "a\nb"},
		{"replacing NULL", &null, "a\nb"},
		{"replacing LF text", &lf, "a\nb"},
		{"replacing CRLF text keeps CRLF", &crlf, "a\r\nb"},
	}
	for _, tt := range tests {
		got, msg := argument(c, value{S: "a\r\nb"}, tt.current)
		if msg != "" || got != tt.want {
			t.Errorf("%s: got %q (%s), want %q", tt.name, got, msg, tt.want)
		}
	}
}

func TestSensitiveName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"password", true}, {"PasswordHash", true}, {"user_passwd", true}, {"client_secret", true},
		{"reset_token", true}, {"api_key", true}, {"apiKey", true}, {"API-KEY", true},
		{"private_key", true}, {"privatekey", true}, {"salt", true}, {"sha_hash", true},
		{"name", false}, {"email", false}, {"key", false}, {"public_key", false}, {"created_at", false},
	}
	for _, tt := range tests {
		if got := sensitiveName(tt.name); got != tt.want {
			t.Errorf("sensitiveName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestShowValue(t *testing.T) {
	text := &colMeta{c: &Column{Kind: KindText}}
	masked := &colMeta{c: &Column{Kind: KindText}, masked: true}
	stamp := &colMeta{c: &Column{Kind: KindDateTimeTZ}}
	blob := &colMeta{c: &Column{Kind: KindBytes}}
	tests := []struct {
		name string
		col  *colMeta
		v    value
		want cell
	}{
		{"null", text, value{Null: true}, cell{Text: "NULL", State: "null"}},
		{"empty", text, value{S: ""}, cell{Text: "empty", State: "empty"}},
		{"masked shows nothing of the value", masked, value{S: "1"}, cell{Text: "••••••", State: "masked"}},
		{"masked null", masked, value{Null: true}, cell{Text: "NULL", State: "null"}},
		{"line breaks", text, value{S: "a\r\nb"}, cell{Text: "a b"}},
		{"long", text, value{S: strings.Repeat("é", 100)}, cell{Text: strings.Repeat("é", 80) + "…"}},
		{"timestamp", stamp, value{S: "2024-01-02T03:04:05Z"}, cell{Text: "2024-01-02 03:04:05 UTC"}},
		{"binary", blob, value{S: "12"}, cell{Text: "12 bytes", State: "muted"}},
	}
	for _, tt := range tests {
		if got := showValue(tt.col, tt.v, 80); got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := humanize("created_at"); got != "Created at" {
		t.Errorf("humanize = %q", got)
	}
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := groupDigits(in); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", in, got, want)
		}
	}
}
