package integration

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/sqlite"
)

// The round trip: open the edit form of a row and submit it untouched. No
// column may change, whatever its type and whatever it holds, NULL
// included. The admin must not even issue an UPDATE.
func TestAdminRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		ids := []string{"1", "2", "3", "4"}
		if b.sqlite {
			ids = append(ids, "5")
		}
		for _, id := range ids {
			for _, native := range []bool{true, false} {
				name := "row " + id + " native submit"
				if !native {
					name = "row " + id + " script submit"
				}
				t.Run(name, func(t *testing.T) {
					n, _ := strconv.Atoi(id)
					before := e.snapshot("kinds", "id = "+e.arg(1), n)
					f := parseForm(t, e.page("/t/kinds/r/"+id))

					// The test means nothing unless the form really
					// carries the columns. Rows 1 to 3 hold only values a
					// form can carry, so every column but the binary one
					// must have a field.
					if id <= "3" {
						for _, c := range e.admin.Schema().Table(tableSchema(b), "kinds").Columns {
							if c.Kind == schemadmin.KindBytes {
								continue
							}
							if !f.values.Has(e.valueField("kinds", c.Name)) {
								t.Errorf("form has no field for %s", c.Name)
							}
						}
					}

					code, location, body := e.post(f.action, f.submit(native))
					if code != http.StatusSeeOther || !strings.HasSuffix(location, "?m=nochange") {
						t.Fatalf("status %d, Location %q; want a redirect saying nothing changed\n%s", code, location, body)
					}
					if after := e.snapshot("kinds", "id = "+e.arg(1), n); !reflect.DeepEqual(before, after) {
						for col := range before {
							if before[col] != after[col] {
								t.Errorf("%s changed: %s -> %s", col, before[col], after[col])
							}
						}
					}
				})
			}
		}
	})
}

func tableSchema(b backend) string {
	if b.sqlite {
		return ""
	}
	return "public"
}

// Changing one field writes that column and no other.
func TestAdminEditOneColumn(t *testing.T) {
	byKind := map[schemadmin.Kind]string{
		schemadmin.KindString:     "changed",
		schemadmin.KindText:       "changed\ntext",
		schemadmin.KindInt:        "7",
		schemadmin.KindFloat:      "2.5",
		schemadmin.KindDecimal:    "99.5",
		schemadmin.KindBool:       "false",
		schemadmin.KindDate:       "2025-05-05",
		schemadmin.KindTime:       "10:11:12",
		schemadmin.KindDateTime:   "2025-05-05 10:11:12",
		schemadmin.KindDateTimeTZ: "2025-05-05 10:11:12+00:00",
		schemadmin.KindJSON:       `{"z": 1}`,
		schemadmin.KindUUID:       "00000000-0000-4000-8000-000000000000",
		schemadmin.KindEnum:       "ok",
	}
	byName := map[string]string{
		"k_array": "{9}", "k_inet": "10.1.1.1", "k_char": "zz", "k_enum": "ok",
		"k_uuid": "00000000-0000-4000-8000-000000000000",
	}

	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		for _, c := range e.admin.Schema().Table(tableSchema(b), "kinds").Columns {
			if c.Name == "id" || c.Kind == schemadmin.KindBytes {
				continue
			}
			t.Run(c.Name, func(t *testing.T) {
				value, ok := byName[c.Name]
				if !ok {
					value, ok = byKind[c.Kind]
				}
				if !ok {
					t.Fatalf("no test value for %s (%s)", c.Name, c.Kind)
				}
				before := e.snapshot("kinds", "id = 1")
				f := parseForm(t, e.page("/t/kinds/r/1"))
				f.values.Set(e.valueField("kinds", c.Name), value)
				code, location, body := e.post(f.action, f.submit(true))
				if code != http.StatusSeeOther || !strings.HasSuffix(location, "?m=saved") {
					t.Fatalf("status %d, Location %q\n%s", code, location, body)
				}
				after := e.snapshot("kinds", "id = 1")
				for col := range before {
					switch changed := before[col] != after[col]; {
					case col == c.Name && !changed:
						t.Errorf("%s was not written: still %s", col, after[col])
					case col != c.Name && changed:
						t.Errorf("%s was written too: %s -> %s", col, before[col], after[col])
					}
				}
			})
		}
	})
}

// NULL and the empty string are different values, and the form can set
// either.
func TestAdminNullAndEmpty(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		null, empty := "NULL", "''"
		if b.sqlite {
			null, empty = "null:NULL", "text:''"
		}
		save := func(edit func(f form)) string {
			t.Helper()
			f := parseForm(t, e.page("/t/kinds/r/2"))
			edit(f)
			code, location, body := e.post(f.action, f.submit(true))
			if code != http.StatusSeeOther {
				t.Fatalf("status %d\n%s", code, body)
			}
			return location[strings.Index(location, "?m=")+3:]
		}
		textNull := e.nullField("kinds", "k_text")
		num, numNull := e.valueField("kinds", "k_int"), e.nullField("kinds", "k_int")

		if f := parseForm(t, e.page("/t/kinds/r/2")); !f.values.Has(textNull) || !f.values.Has(numNull) {
			t.Fatal("the NULL boxes of a NULL row are not ticked")
		}

		// Unticking NULL on a text column and leaving it empty stores "".
		if got := save(func(f form) { f.values.Del(textNull) }); got != "saved" {
			t.Errorf("NULL -> empty: %s", got)
		}
		if got := e.snapshot("kinds", "id = 2")["k_text"]; got != empty {
			t.Errorf("k_text = %s, want the empty string", got)
		}
		if f := parseForm(t, e.page("/t/kinds/r/2")); f.values.Has(textNull) {
			t.Error("the NULL box of an empty string is ticked")
		}
		// Ticking it again stores NULL.
		if got := save(func(f form) { f.values.Set(textNull, "1") }); got != "saved" {
			t.Errorf("empty -> NULL: %s", got)
		}
		if got := e.snapshot("kinds", "id = 2")["k_text"]; got != null {
			t.Errorf("k_text = %s, want NULL", got)
		}
		// A number has no empty value: unticked and empty is still NULL,
		// and nothing is written.
		if got := save(func(f form) { f.values.Del(numNull) }); got != "nochange" {
			t.Errorf("empty number: %s", got)
		}
		// The NULL box wins over whatever is in the field.
		if got := save(func(f form) { f.values.Set(num, "5") }); got != "nochange" {
			t.Errorf("NULL box ticked with a value: %s", got)
		}
		if got := save(func(f form) { f.values.Del(numNull); f.values.Set(num, "5") }); got != "saved" {
			t.Errorf("NULL -> 5: %s", got)
		}
		if got := save(func(f form) { f.values.Set(numNull, "1") }); got != "saved" {
			t.Errorf("5 -> NULL: %s", got)
		}
		if got := e.snapshot("kinds", "id = 2")["k_int"]; got != null {
			t.Errorf("k_int = %s, want NULL", got)
		}
	})
}

// A column someone else changed in the meantime is not overwritten by a
// form that did not touch it, and is not silently overwritten by one that
// did.
func TestAdminConcurrentEdits(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		title := e.valueField("posts", "title")

		f := parseForm(t, e.page("/t/posts/r/1"))
		mustExec(t, e.db, "UPDATE posts SET views = 999 WHERE id = 1")
		f.values.Set(title, "Renamed")
		if code, _, body := e.post(f.action, f.submit(true)); code != http.StatusSeeOther {
			t.Fatalf("status %d\n%s", code, body)
		}
		if got := e.scalar("SELECT title || '/' || views FROM posts WHERE id = 1"); got != "Renamed/999" {
			t.Errorf("row is %s, want Renamed/999: the untouched column was written", got)
		}

		f = parseForm(t, e.page("/t/posts/r/1"))
		mustExec(t, e.db, "UPDATE posts SET title = 'Theirs' WHERE id = 1")
		f.values.Set(title, "Mine")
		code, _, body := e.post(f.action, f.submit(true))
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, "changed by someone else") {
			t.Fatalf("status %d, want 422 with a conflict message\n%s", code, body)
		}
		if got := e.scalar("SELECT title FROM posts WHERE id = 1"); got != "Theirs" {
			t.Errorf("title is %q, want Theirs", got)
		}
	})
}

func TestAdminCreate(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		field := func(col string) string { return e.valueField("posts", col) }
		newPost := func(edit func(f form)) (int, string, string) {
			f := parseForm(t, e.page("/t/posts/new"))
			edit(f)
			return e.post(f.action, f.submit(true))
		}

		t.Run("defaults are left to the database", func(t *testing.T) {
			code, location, body := newPost(func(f form) {
				f.values.Set(field("title"), "Brand new")
				f.values.Set(field("author_id"), "1")
			})
			if code != http.StatusSeeOther || location != "/t/posts/r/6?m=created" {
				t.Fatalf("status %d, Location %q\n%s", code, location, body)
			}
			got := e.scalar("SELECT title || '/' || status || '/' || views || '/' || (published_on IS NULL) FROM posts WHERE id = 6")
			if got != "Brand new/draft/0/1" && got != "Brand new/draft/0/true" {
				t.Errorf("row is %s", got)
			}
			mustContain(t, e.page(location), "Row created.")
		})

		before := e.scalar("SELECT count(*) FROM posts")
		rejected := []struct {
			name string
			edit func(f form)
			want string
		}{
			{"required value missing", func(f form) { f.values.Set(field("title"), "x") }, "Enter a value."},
			{"not a number", func(f form) {
				f.values.Set(field("author_id"), "1")
				f.values.Set(field("views"), "many")
			}, "Enter a whole number."},
			{"not one of the choices", func(f form) {
				f.values.Set(field("author_id"), "1")
				f.values.Set(field("status"), "bogus")
			}, "Choose one of the listed values."},
			{"not a date", func(f form) {
				f.values.Set(field("author_id"), "1")
				f.values.Del(e.nullField("posts", "published_on"))
				f.values.Set(field("published_on"), "yesterday")
			}, "Enter a date as YYYY-MM-DD."},
			{"foreign key violation", func(f form) { f.values.Set(field("author_id"), "9999") }, "refers to a row that does not exist"},
			{"check violation", func(f form) {
				f.values.Set(field("author_id"), "1")
				f.values.Set(field("views"), "-1")
			}, "break a rule defined in the database"},
		}
		for _, tt := range rejected {
			t.Run(tt.name, func(t *testing.T) {
				code, _, body := newPost(tt.edit)
				if code != http.StatusUnprocessableEntity {
					t.Fatalf("status %d, want 422\n%s", code, body)
				}
				mustContain(t, body, tt.want)
				mustNotContain(t, body, "constraint", "violates", "INSERT", "SQLSTATE", "posts_")
			})
		}
		t.Run("unique violation", func(t *testing.T) {
			f := parseForm(t, e.page("/t/authors/new"))
			f.values.Set(e.valueField("authors", "name"), "Twin")
			f.values.Del(e.nullField("authors", "email"))
			f.values.Set(e.valueField("authors", "email"), "ada@example.com")
			code, _, body := e.post(f.action, f.submit(true))
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422", code)
			}
			mustContain(t, body, "must be unique")
			// What was typed is still there to correct, except secrets.
			mustContain(t, body, "Twin", "ada@example.com")
		})
		if after := e.scalar("SELECT count(*) FROM posts"); after != before {
			t.Errorf("rejected forms changed the number of posts from %s to %s", before, after)
		}

		t.Run("an explicit NULL overrides nothing and a typed secret is stored", func(t *testing.T) {
			f := parseForm(t, e.page("/t/authors/new"))
			f.values.Set(e.valueField("authors", "name"), "Nullable")
			f.values.Set(e.valueField("authors", "password_hash"), "typed-secret")
			code, location, body := e.post(f.action, f.submit(true))
			if code != http.StatusSeeOther {
				t.Fatalf("status %d\n%s", code, body)
			}
			id := strings.TrimSuffix(strings.TrimPrefix(location, "/t/authors/r/"), "?m=created")
			got := e.scalar("SELECT coalesce(email, 'NULL') || '/' || password_hash || '/' || coalesce(bio, 'NULL') FROM authors WHERE id = " + id)
			if got != "NULL/typed-secret/NULL" {
				t.Errorf("row is %s", got)
			}
		})
	})
}

func TestAdminList(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		tests := []struct {
			name  string
			query string
			want  string // row ids, in order
		}{
			{"everything, by primary key", "", "1 2 3 4 5"},
			{"search is case-insensitive", "q=HELLO", "1"},
			{"search for a percent sign finds only it", "q=%25", "3"},
			{"search for an underscore finds only it", "q=_", "3"},
			{"search for a number finds the row with that key", "q=4", "4"},
			{"search that matches nothing", "q=zzz", ""},
			{"equals", "eq.status=draft", "2 4"},
			{"equals on a foreign key", "eq.author_id=1", "1 2"},
			{"contains is case-insensitive", "has.title=TH", "4 5"},
			{"contains treats wildcards literally", "has.title=%25", "3"},
			{"range", "from.views=5&to.views=7", "3 4"},
			{"date range", "from.published_on=2024-02-01&to.published_on=2024-06-30", "3 4"},
			{"is null", "is.published_on=null", "2 5"},
			{"is not null", "is.published_on=notnull", "1 3 4"},
			{"filters combine", "eq.status=published&is.published_on=notnull&q=l", "1 3"},
			{"sort descending", "sort=views&dir=desc", "1 4 3 5 2"},
			{"sort ascending", "sort=title", "3 2 5 4 1"},
			{"sort by something that is not a column", "sort=id%3BDROP+TABLE+posts", "1 2 3 4 5"},
			{"filter on something that is not a column", "eq.nope=1&eq.id%3BDROP=1", "1 2 3 4 5"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := rowIDs(e.page("/t/posts?" + tt.query)); got != tt.want {
					t.Errorf("rows %q, want %q", got, tt.want)
				}
			})
		}
		t.Run("an invalid filter value is reported and ignored", func(t *testing.T) {
			page := e.page("/t/posts?eq.views=abc")
			mustContain(t, page, "The filter on Views was ignored")
			if got := rowIDs(page); got != "1 2 3 4 5" {
				t.Errorf("rows %q", got)
			}
		})
		t.Run("foreign keys show the row they point to", func(t *testing.T) {
			mustContain(t, e.page("/t/posts"), `href="/t/authors/r/1"`, "Ada", "Grace")
		})
		t.Run("search in a table with a composite key", func(t *testing.T) {
			if got := len(rowURLs(e.page("/t/post_tags?q=GO"))); got != 2 {
				t.Errorf("%d rows, want 2", got)
			}
		})
		if got := e.scalar("SELECT count(*) FROM posts"); got != "5" {
			t.Errorf("posts has %s rows after the list tests", got)
		}
	})
}

func TestAdminPagination(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, func(c *schemadmin.Config) { c.PageSize = 2 })
		tests := []struct {
			query, rows, summary string
			prev, next           bool
		}{
			{"", "1 2", "1–2 of 5", false, true},
			{"page=2", "3 4", "3–4 of 5", true, true},
			{"page=3", "5", "5–5 of 5", true, false},
			{"page=4", "", "No rows on this page", true, false},
			{"eq.status=published", "1 3", "1–2 of 3", false, true},
			{"eq.status=published&page=2", "5", "3–3 of 3", true, false},
			{"page=-1", "1 2", "1–2 of 5", false, true},
			{"page=abc", "1 2", "1–2 of 5", false, true},
		}
		for _, tt := range tests {
			page := e.page("/t/posts?" + tt.query)
			if got := rowIDs(page); got != tt.rows {
				t.Errorf("%q: rows %q, want %q", tt.query, got, tt.rows)
			}
			mustContain(t, page, ">"+tt.summary+"<")
			if got := strings.Contains(page, `rel="prev"`); got != tt.prev {
				t.Errorf("%q: previous link present = %v", tt.query, got)
			}
			if got := strings.Contains(page, `rel="next"`); got != tt.next {
				t.Errorf("%q: next link present = %v", tt.query, got)
			}
		}
	})
}

// When the count cannot be had in time the list still works: it shows the
// page and whether another follows, without a total.
func TestAdminCountTimeout(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, func(c *schemadmin.Config) {
			c.PageSize = 2
			c.CountTimeout = time.Nanosecond
		})
		page := e.page("/t/posts")
		if got := rowIDs(page); got != "1 2" {
			t.Errorf("rows %q", got)
		}
		mustContain(t, page, ">1–2<", `rel="next"`)
		mustNotContain(t, page, " of 5")
		// The last page needs no count at all.
		mustContain(t, e.page("/t/posts?page=3"), ">5–5 of 5<")
	})
}

func TestAdminDelete(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)

		t.Run("the confirmation lists what depends on the row", func(t *testing.T) {
			page := e.page("/t/posts/r/1/delete")
			mustContain(t, page,
				`<strong>2</strong> in <a href="/t/comments?eq.post_id=1">Comments</a> prevent this row from being deleted.`,
				`<strong>2</strong> in <a href="/t/post_tags?eq.post_id=1">Post tags</a> will be deleted too.`,
				"The database will refuse this delete")
			page = e.page("/t/authors/r/1/delete")
			mustContain(t, page, `<strong>2</strong> in <a href="/t/posts?eq.author_id=1">Posts</a> will be deleted too.`)
			// A row nothing refers to lists nothing.
			mustNotContain(t, e.page("/t/posts/r/2/delete"), "Rows that refer to this row")
		})
		t.Run("a GET deletes nothing", func(t *testing.T) {
			e.page("/t/posts/r/2/delete")
			if got := e.scalar("SELECT count(*) FROM posts"); got != "5" {
				t.Errorf("posts has %s rows", got)
			}
		})
		t.Run("a delete the database refuses is explained", func(t *testing.T) {
			code, _, body := e.post("/t/posts/r/1/delete", url.Values{})
			if code != http.StatusConflict {
				t.Fatalf("status %d, want 409", code)
			}
			mustContain(t, body, "Other rows still refer to this row.")
			mustNotContain(t, body, "constraint", "violates", "DELETE FROM", "comments_post_id")
			if got := e.scalar("SELECT count(*) FROM posts WHERE id = 1"); got != "1" {
				t.Error("the post is gone")
			}
		})
		t.Run("delete", func(t *testing.T) {
			code, location, _ := e.post("/t/posts/r/2/delete", url.Values{})
			if code != http.StatusSeeOther || location != "/t/posts?m=deleted" {
				t.Fatalf("status %d, Location %q", code, location)
			}
			if got := e.scalar("SELECT count(*) FROM posts WHERE id = 2"); got != "0" {
				t.Error("the post is still there")
			}
			mustContain(t, e.page(location), "Row deleted.")
			if code, _ := e.get("/t/posts/r/2"); code != http.StatusNotFound {
				t.Errorf("deleted row: status %d, want 404", code)
			}
			if code, _, _ := e.post("/t/posts/r/2/delete", url.Values{}); code != http.StatusNotFound {
				t.Errorf("deleting it again: status %d, want 404", code)
			}
		})
		t.Run("delete cascades as announced", func(t *testing.T) {
			if code, _, _ := e.post("/t/comments/r/3/delete", url.Values{}); code != http.StatusSeeOther {
				t.Fatalf("deleting the comment: status %d", code)
			}
			if code, _, body := e.post("/t/authors/r/3/delete", url.Values{}); code != http.StatusSeeOther {
				t.Fatalf("deleting the author: status %d\n%s", code, body)
			}
			if got := e.scalar("SELECT count(*) FROM posts WHERE author_id = 3"); got != "0" {
				t.Errorf("%s posts of the deleted author remain", got)
			}
		})
	})
}

// SQLite enforces foreign keys only when the connection asks it to. When
// it does not, the delete page must not promise cascades.
func TestAdminDeleteWithoutForeignKeyEnforcement(t *testing.T) {
	db := openSQLite(t)
	mustExec(t, db, readTestdata(t, "admin_sqlite.sql"))
	b := backend{d: sqlite.New(), sqlite: true, open: func(*testing.T) *sql.DB { return db }}
	e := newEnv(t, b, nil)
	page := e.page("/t/authors/r/1/delete")
	mustContain(t, page, "will be left referring to a row that no longer exists", "Foreign keys are not enforced")
	mustNotContain(t, page, "will be deleted too")
}

// Pages that read a second table apply ActionView to it.
func TestAdminIndirectReadsAreAuthorized(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		t.Run("foreign-key target hidden", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) { c.Authorizer = deny(schemadmin.ActionView, "authors") })
			for _, path := range []string{"/t/posts", "/t/posts/r/1", "/t/posts/new", "/t/posts/r/1/delete", "/"} {
				mustNotContain(t, e.page(path), "Ada", "Grace", "/t/authors", "Authors")
			}
			// The key itself belongs to posts and is shown, as a plain field.
			f := parseForm(t, e.page("/t/posts/r/1"))
			if got := f.values.Get(e.valueField("posts", "author_id")); got != "1" {
				t.Errorf("author_id field is %q", got)
			}
			for _, path := range []string{"/t/authors", "/t/authors/r/1", "/t/authors/options?col=id", "/t/authors/options?col=id&q=a"} {
				if code, body := e.get(path); code != http.StatusNotFound || strings.Contains(body, "Ada") {
					t.Errorf("GET %s: status %d", path, code)
				}
			}
		})
		t.Run("foreign-key target visible", func(t *testing.T) {
			e := newEnv(t, b, nil)
			mustContain(t, e.page("/t/posts/r/1"), ">Ada (1)</option>")
			code, body := e.get("/t/authors/options?col=id&q=gra")
			if code != http.StatusOK || !strings.Contains(body, `{"value":"3","label":"Grace (3)"}`) {
				t.Errorf("options: status %d: %s", code, body)
			}
		})
		t.Run("dependent table hidden", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) { c.Authorizer = deny(schemadmin.ActionView, "comments") })
			page := e.page("/t/posts/r/1/delete")
			mustNotContain(t, page, "Comments", "/t/comments", "<strong>2</strong> in Comments")
			mustContain(t, page, "Tables you cannot view also refer to this table", "Post tags")
		})
	})
}

func TestAdminActionsAreAuthorized(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		t.Run("update refused", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) { c.Authorizer = deny(schemadmin.ActionUpdate, "posts") })
			page := e.page("/t/posts/r/1")
			mustContain(t, page, "Hello")
			mustNotContain(t, page, `type="submit"`, `name="v.`)
			code, _, _ := e.post("/t/posts/r/1", url.Values{e.valueField("posts", "title"): {"Hacked"}, "o.2": {"x"}})
			if code != http.StatusForbidden {
				t.Errorf("status %d, want 403", code)
			}
			if got := e.scalar("SELECT title FROM posts WHERE id = 1"); got != "Hello" {
				t.Errorf("title is %q", got)
			}
			// Other tables are unaffected.
			mustContain(t, e.page("/t/authors/r/1"), `type="submit"`)
		})
		t.Run("delete refused", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) { c.Authorizer = deny(schemadmin.ActionDelete, "posts") })
			mustNotContain(t, e.page("/t/posts/r/2"), "/delete")
			if code, _ := e.get("/t/posts/r/2/delete"); code != http.StatusForbidden {
				t.Errorf("GET: status %d, want 403", code)
			}
			if code, _, _ := e.post("/t/posts/r/2/delete", url.Values{}); code != http.StatusForbidden {
				t.Errorf("POST: status %d, want 403", code)
			}
			if got := e.scalar("SELECT count(*) FROM posts"); got != "5" {
				t.Errorf("posts has %s rows", got)
			}
		})
		t.Run("create refused", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) { c.Authorizer = deny(schemadmin.ActionCreate, "posts") })
			mustNotContain(t, e.page("/t/posts"), "New row", "/t/posts/new")
			code, _, _ := e.post("/t/posts/new", url.Values{e.valueField("posts", "title"): {"x"}, e.valueField("posts", "author_id"): {"1"}})
			if code != http.StatusForbidden {
				t.Errorf("status %d, want 403", code)
			}
			if got := e.scalar("SELECT count(*) FROM posts"); got != "5" {
				t.Errorf("posts has %s rows", got)
			}
		})
		t.Run("read-only table", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) {
				c.Tables = map[string]schemadmin.TableConfig{"posts": {ReadOnly: true}}
			})
			mustNotContain(t, e.page("/t/posts"), "New row")
			mustNotContain(t, e.page("/t/posts/r/1"), `type="submit"`, "/delete")
			for _, path := range []string{"/t/posts/new", "/t/posts/r/1", "/t/posts/r/1/delete"} {
				if code, _, _ := e.post(path, url.Values{}); code != http.StatusForbidden {
					t.Errorf("POST %s: status %d, want 403", path, code)
				}
			}
		})
		t.Run("cross-origin write refused", func(t *testing.T) {
			e := newEnv(t, b, nil)
			r := httptest.NewRequest(http.MethodPost, "/t/posts/r/2/delete", nil)
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			w := httptest.NewRecorder()
			e.admin.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("status %d, want 403", w.Code)
			}
			if got := e.scalar("SELECT count(*) FROM posts"); got != "5" {
				t.Errorf("posts has %s rows", got)
			}
		})
	})
}

// A masked column's values never leave the database: not in lists, forms,
// confirmations or suggestions, and not through search, sort or filter.
func TestAdminMasking(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		paths := []string{
			"/t/authors", "/t/authors/r/1", "/t/authors/r/1/delete", "/t/authors/new",
			"/t/authors/options?col=id&q=HASH", "/t/authors/options?col=password_hash",
			"/t/authors?q=HASH", "/t/authors?sort=password_hash", "/t/authors?has.password_hash=HASH",
			"/t/authors?eq.password_hash=HASH-ADA-SECRET", "/t/posts", "/t/posts/r/1",
			"/t/kinds", "/t/kinds/r/1", "/t/kinds/r/1/delete",
		}
		for _, path := range paths {
			_, body := e.get(path)
			mustNotContain(t, body, "HASH-", "TOKEN-SECRET")
		}
		if got := rowIDs(e.page("/t/authors?q=HASH-ADA")); got != "" {
			t.Errorf("search looked inside the masked column: rows %q", got)
		}
		if got := rowIDs(e.page("/t/authors?eq.password_hash=HASH-ADA-SECRET")); got != "1 2 3" {
			t.Errorf("a filter on the masked column was applied: rows %q", got)
		}
		mustContain(t, e.page("/t/authors"), "••••••")

		t.Run("write-only", func(t *testing.T) {
			f := parseForm(t, e.page("/t/authors/r/1"))
			field := e.valueField("authors", "password_hash")
			if got := f.values.Get(field); got != "" {
				t.Fatalf("the form carries %q in the masked field", got)
			}
			if _, location, _ := e.post(f.action, f.submit(true)); !strings.HasSuffix(location, "?m=nochange") {
				t.Errorf("untouched form: Location %q", location)
			}
			f.values.Set(field, "new-secret")
			if _, location, _ := e.post(f.action, f.submit(true)); !strings.HasSuffix(location, "?m=saved") {
				t.Errorf("new secret: Location %q", location)
			}
			if got := e.scalar("SELECT password_hash FROM authors WHERE id = 1"); got != "new-secret" {
				t.Errorf("password_hash is %q", got)
			}
			// A rejected form does not echo the secret back.
			f = parseForm(t, e.page("/t/authors/r/1"))
			f.values.Set(field, "typed-but-rejected")
			f.values.Del(e.nullField("authors", "email"))
			f.values.Set(e.valueField("authors", "email"), "grace@example.com")
			code, _, body := e.post(f.action, f.submit(true))
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422", code)
			}
			mustNotContain(t, body, "typed-but-rejected")
		})
		t.Run("the logs name rows, never values", func(t *testing.T) {
			if logs := e.logs.String(); strings.Contains(logs, "new-secret") || !strings.Contains(logs, "row updated") {
				t.Errorf("unexpected log:\n%s", logs)
			}
		})
		t.Run("opt out", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) {
				c.Tables = map[string]schemadmin.TableConfig{"authors": {
					Columns: map[string]schemadmin.ColumnConfig{"password_hash": {Mask: schemadmin.MaskNever}},
				}}
			})
			mustContain(t, e.page("/t/authors"), "HASH-ADA-SECRET")
		})
		t.Run("opt in", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) {
				c.Tables = map[string]schemadmin.TableConfig{"authors": {
					Columns: map[string]schemadmin.ColumnConfig{"email": {Mask: schemadmin.MaskAlways}},
				}}
			})
			for _, path := range []string{"/t/authors", "/t/authors/r/1", "/t/authors?q=ada@"} {
				mustNotContain(t, e.page(path), "ada@example.com")
			}
		})
		t.Run("hidden column", func(t *testing.T) {
			e := newEnv(t, b, func(c *schemadmin.Config) {
				c.Tables = map[string]schemadmin.TableConfig{"authors": {
					Columns: map[string]schemadmin.ColumnConfig{"bio": {Hidden: true}},
				}}
			})
			for _, path := range []string{"/t/authors", "/t/authors/r/1", "/t/authors/r/1/delete"} {
				mustNotContain(t, e.page(path), "Line one", "Bio")
			}
		})
	})
}

// Text from the database is text on the page, whatever it looks like.
func TestAdminEscapesDatabaseContent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		for _, path := range []string{
			"/t/authors", "/t/authors/r/2", "/t/authors/r/2/delete", "/t/authors?q=script",
			"/t/posts", "/t/posts/r/5", "/t/posts/new",
			"/t/kinds", "/t/kinds/r/1", "/t/kinds/r/1/delete",
		} {
			page := e.page(path)
			mustNotContain(t, page, "<script>alert", "<img src=x", `<b>"quoted"`)
			if !strings.Contains(page, "&lt;script&gt;alert(1)") && !strings.Contains(page, "&lt;b&gt;") {
				t.Errorf("GET %s: the hostile text is not on the page in escaped form", path)
			}
		}
		_, suggestions := e.get("/t/authors/options?col=id&q=script")
		mustNotContain(t, suggestions, "<script>", "<img")
		mustContain(t, suggestions, `\u003cscript\u003e`)
	})
}

// Table and column names that look like SQL or HTML are just names.
func TestAdminHostileIdentifiers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		const name = `weird "table" <b>x</b>`
		index := e.page("/")
		mustNotContain(t, index, "<b>x</b>")
		mustContain(t, index, "&lt;b&gt;x&lt;/b&gt;")
		// The address is whatever the index links to: one path segment.
		link := regexp.MustCompile(`href="(/t/weird[^"/]*)"`).FindStringSubmatch(index)
		if link == nil {
			t.Fatalf("the index does not link to the table:\n%s", index)
		}
		list := link[1]

		page := e.page(list)
		mustNotContain(t, page, "<script>alert(1)</script>")

		f := parseForm(t, e.page(list+"/new"))
		for _, col := range []string{"col with space", "<script>alert(1)</script>", "semi;colon--"} {
			f.values.Del(e.nullField(name, col))
			f.values.Set(e.valueField(name, col), "value of "+col)
		}
		code, location, body := e.post(f.action, f.submit(true))
		if code != http.StatusSeeOther {
			t.Fatalf("create: status %d\n%s", code, body)
		}
		row := strings.TrimSuffix(location, "?m=created")
		f = parseForm(t, e.page(row))
		f.values.Set(e.valueField(name, "semi;colon--"), "'; DROP TABLE authors; --")
		if code, _, body := e.post(f.action, f.submit(true)); code != http.StatusSeeOther {
			t.Fatalf("update: status %d\n%s", code, body)
		}
		got := e.scalar(`SELECT "semi;colon--" FROM ` + b.d.QuoteIdent(name))
		if got != "'; DROP TABLE authors; --" {
			t.Errorf("stored %q", got)
		}
		page = e.page(list + "?" + url.Values{"sort": {"semi;colon--"}, "has.col with space": {"value"}, "q": {"DROP"}}.Encode())
		if n := len(rowURLs(page)); n != 1 {
			t.Errorf("filtered and sorted list has %d rows, want 1", n)
		}
		if code, _, _ := e.post(row+"/delete", url.Values{}); code != http.StatusSeeOther {
			t.Errorf("delete: status %d", code)
		}
		if got := e.scalar("SELECT count(*) FROM authors"); got != "3" {
			t.Errorf("authors has %s rows", got)
		}
	})
}

// A row is addressed by its whole key, and any key value survives the URL.
func TestAdminCompositeKeys(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, nil)
		rows := rowURLs(e.page("/t/post_tags"))
		if len(rows) != 3 {
			t.Fatalf("list has %d addressable rows, want 3", len(rows))
		}
		var awkward string
		for _, r := range rows {
			if strings.Contains(r, "~c") {
				awkward = r
			}
		}
		if awkward == "" || strings.Count(strings.TrimPrefix(awkward, "/t/post_tags/r/"), "/") != 0 {
			t.Fatalf("no single-segment address for the awkward key among %q", rows)
		}
		f := parseForm(t, e.page(awkward))
		tag := e.valueField("post_tags", "tag")
		if got := f.values.Get(tag); got != "a,b/c ~x" {
			t.Fatalf("form shows tag %q", got)
		}
		// Editing part of the key moves the row; the response says where.
		f.values.Set(tag, ".hidden/../x")
		code, location, body := e.post(f.action, f.submit(true))
		if code != http.StatusSeeOther {
			t.Fatalf("status %d\n%s", code, body)
		}
		moved := strings.TrimSuffix(location, "?m=saved")
		if got := parseForm(t, e.page(moved)).values.Get(tag); got != ".hidden/../x" {
			t.Errorf("row at the new address has tag %q", got)
		}
		if code, _ := e.get(awkward); code != http.StatusNotFound {
			t.Errorf("old address: status %d, want 404", code)
		}
		if got := e.scalar("SELECT count(*) FROM post_tags WHERE post_id = 1"); got != "2" {
			t.Errorf("post 1 has %s tags, want 2: the edit touched another row", got)
		}
		// The other row of the same post is untouched by a delete.
		if code, _, _ := e.post(moved+"/delete", url.Values{}); code != http.StatusSeeOther {
			t.Errorf("delete: status %d", code)
		}
		if got := e.scalar("SELECT tag FROM post_tags WHERE post_id = 1"); got != "go" {
			t.Errorf("remaining tag is %q", got)
		}
		// A key with too few or too many parts addresses nothing.
		for _, path := range []string{"/t/post_tags/r/1", "/t/post_tags/r/1,go,x", "/t/post_tags/r/x,go"} {
			if code, _ := e.get(path); code != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 404", path, code)
			}
		}
	})
}

// SQLite lets a primary key hold NULL, several times over. Such rows are
// listed and cannot be opened, edited or deleted.
func TestAdminNullPrimaryKeysAreReadOnly(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		if !b.sqlite {
			t.Skip("PostgreSQL does not allow NULL in a primary key")
		}
		e := newEnv(t, b, nil)
		page := e.page("/t/nullkeys")
		if got := strings.Count(page, "<tr"); got != 5 { // header + 4 rows
			t.Errorf("list has %d table rows, want 5", got)
		}
		rows := rowURLs(page)
		if len(rows) != 2 {
			t.Fatalf("addressable rows %q, want the two with a key", rows)
		}
		// The empty string is a key like any other.
		if got := parseForm(t, e.page("/t/nullkeys/r/~e")).values.Get(e.valueField("nullkeys", "v")); got != "d" {
			t.Errorf("the row keyed by the empty string shows %q", got)
		}
		for _, path := range []string{"/t/nullkeys/r/NULL", "/t/nullkeys/r/", "/t/nullkeys/r/~n"} {
			if code, _ := e.get(path); code == http.StatusOK {
				t.Errorf("GET %s: a NULL key was addressed", path)
			}
		}
		if code, _, _ := e.post("/t/nullkeys/r/NULL/delete", url.Values{}); code != http.StatusNotFound {
			t.Errorf("delete by NULL: status %d", code)
		}
		if got := e.scalar("SELECT count(*) FROM nullkeys"); got != "4" {
			t.Errorf("nullkeys has %s rows", got)
		}
	})
}

// With several schemas, tables are addressed as schema.table everywhere.
func TestAdminMultipleSchemas(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		if b.sqlite {
			t.Skip("SQLite has one schema")
		}
		var asked []string
		e := newEnv(t, backend{d: b.d, open: func(t *testing.T) *sql.DB {
			db := b.open(t)
			mustExec(t, db, `
				CREATE SCHEMA app;
				CREATE SCHEMA billing;
				CREATE TABLE app.users (id integer PRIMARY KEY, name text NOT NULL);
				CREATE TABLE billing.users (id integer PRIMARY KEY, name text NOT NULL);
				CREATE TABLE billing.invoices (id integer PRIMARY KEY, user_id integer NOT NULL REFERENCES app.users (id));
				INSERT INTO app.users VALUES (1, 'App Ada');
				INSERT INTO billing.users VALUES (1, 'Billing Bob');
				INSERT INTO billing.invoices VALUES (10, 1);`)
			return db
		}}, func(c *schemadmin.Config) {
			c.Schemas = []string{"app", "billing"}
			c.Tables = map[string]schemadmin.TableConfig{"billing.invoices": {Label: "Bills"}}
			c.Authorizer = schemadmin.AuthorizerFunc(func(_ *http.Request, _ schemadmin.Action, table string) bool {
				asked = append(asked, table)
				return true
			})
		})
		index := e.page("/")
		mustContain(t, index, `href="/t/app.users"`, `href="/t/billing.users"`, `href="/t/billing.invoices"`, "Bills")
		mustNotContain(t, index, "/t/authors", `href="/t/users"`)

		mustContain(t, e.page("/t/app.users"), "App Ada")
		mustContain(t, e.page("/t/billing.users"), "Billing Bob")
		// The foreign key crosses schemas and links to the right table.
		mustContain(t, e.page("/t/billing.invoices"), `href="/t/app.users/r/1"`, "App Ada")
		mustContain(t, e.page("/t/app.users/r/1/delete"), `href="/t/billing.invoices?eq.user_id=1"`)
		if code, _ := e.get("/t/users"); code != http.StatusNotFound {
			t.Errorf("bare table name: status %d, want 404", code)
		}
		for _, want := range []string{"app.users", "billing.users", "billing.invoices"} {
			found := false
			for _, got := range asked {
				found = found || got == want
			}
			if !found {
				t.Errorf("the Authorizer was never asked about %q", want)
			}
		}
	})
}

// Per-table configuration changes what is shown, not what is stored.
func TestAdminTableConfig(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		e := newEnv(t, b, func(c *schemadmin.Config) {
			c.Title = "Back office"
			c.Tables = map[string]schemadmin.TableConfig{
				"posts": {
					Label:         "Articles",
					ListColumns:   []string{"title", "status"},
					SearchColumns: []string{"status"},
					DefaultSort:   schemadmin.Sort{Column: "views", Desc: true},
					PageSize:      3,
					Columns: map[string]schemadmin.ColumnConfig{
						"title":  {Label: "Headline", Help: "Shown on the front page."},
						"views":  {ReadOnly: true},
						"status": {Choices: []string{"draft", "published"}},
					},
				},
				"authors":  {DisplayColumn: "email"},
				"comments": {Hidden: true},
			}
		})
		index := e.page("/")
		mustContain(t, index, "Back office", "Articles")
		mustNotContain(t, index, "Comments")

		page := e.page("/t/posts")
		mustContain(t, page, "Headline", ">1–3 of 5<")
		mustNotContain(t, page, "Published on", "Author id")
		if got := rowIDs(page); got != "1 4 3" {
			t.Errorf("default sort: rows %q", got)
		}
		if got := rowIDs(e.page("/t/posts?q=draft")); got != "4 2" {
			t.Errorf("search in configured columns: rows %q", got)
		}
		if got := rowIDs(e.page("/t/posts?q=Hello")); got != "" {
			t.Errorf("search looked outside the configured columns: rows %q", got)
		}

		form := e.page("/t/posts/r/1")
		mustContain(t, form, "Headline", "Shown on the front page.", "ada@example.com (1)")
		f := parseForm(t, form)
		if f.values.Has(e.valueField("posts", "views")) {
			t.Error("a read-only column has a field")
		}
		// A read-only column cannot be written by forging the field.
		f.values.Set(e.valueField("posts", "views"), "12345")
		f.values.Set("o."+strings.TrimPrefix(e.valueField("posts", "views"), "v."), "x")
		e.post(f.action, f.submit(true))
		if got := e.scalar("SELECT views FROM posts WHERE id = 1"); got != "10" {
			t.Errorf("views is %s: the read-only column was written", got)
		}
		// A hidden table is gone, also as a dependent.
		if code, _ := e.get("/t/comments"); code != http.StatusNotFound {
			t.Errorf("hidden table: status %d", code)
		}
		mustNotContain(t, e.page("/t/posts/r/1/delete"), "Comments")
	})
}
