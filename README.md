# schemadmin

[![CI](https://github.com/Uktupus/schemadmin/actions/workflows/ci.yml/badge.svg)](https://github.com/Uktupus/schemadmin/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Uktupus/schemadmin.svg)](https://pkg.go.dev/github.com/Uktupus/schemadmin)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

An admin panel for any Go application that has a `*sql.DB`.

schemadmin reads the database schema at startup (tables, columns, types,
nullability, defaults, primary keys, foreign keys, enums) and serves a
CRUD interface for it as a plain `http.Handler`. There is no ORM, no model
registration and no code generation: the database is the source of truth.

- PostgreSQL, SQLite and MySQL, behind a `Dialect` interface.
- The core module imports only the Go standard library. It never imports a
  database driver; you bring your own.
- Server-rendered HTML, one CSS file, one small script. No build step. Every
  page also works with JavaScript switched off.
- Go 1.26 or later.

## Install

```sh
go get github.com/Uktupus/schemadmin@latest
```

Import a dialect next to the core package:

```go
import (
	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/postgres" // or sqlite, mysql
)
```

## Quick start

```go
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"

	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/postgres"
	// "github.com/Uktupus/schemadmin/sqlite"
	// "github.com/Uktupus/schemadmin/mysql"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	db, err := sql.Open("pgx", "postgres://localhost/app")
	if err != nil {
		log.Fatal(err)
	}

	admin, err := schemadmin.New(context.Background(), schemadmin.Config{
		DB:       db,
		Dialect:  postgres.New(), // or sqlite.New(), mysql.New()
		BasePath: "/admin",
		Authorizer: schemadmin.AuthorizerFunc(func(r *http.Request, action schemadmin.Action, table string) bool {
			user := currentUser(r) // your application's session
			return user != nil && user.IsStaff
		}),
	})
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/admin/", admin)
	log.Fatal(http.ListenAndServe("localhost:8080", mux))
}
```

To try it without a database of your own:

```sh
cd example && go run .
# open http://localhost:8080/admin/
```

The example seeds a small shop into a temporary SQLite file. It authorizes
loopback only; wire your own session check before exposing anything else.

## What you get

- **Index** of the tables the request may view.
- **List** with search, sorting, pagination and a filter menu behind every
  column header: equals, contains, a range for numbers and dates, NULL or
  not NULL, and one-click values for enums and booleans. The whole state of
  a list lives in its URL, so a filtered view can be bookmarked or shared.
- **Export** downloads the current filter as CSV. **Bulk delete** selects
  rows on the page and removes them in one request (capped by
  `Config.BulkLimit`).
- **Create and edit** forms generated from column types, with validation
  before the database is asked, and the database's own constraints reported
  in plain language after. Foreign keys (including composite ones) are a
  select of the referenced rows, or a search-as-you-type field when there
  are more than 100. Binary columns are edited as hexadecimal.
- **Delete** behind a confirmation that counts the rows referring to the
  one being deleted, including those removed only by a cascade chain, and
  says what the database will do with them.
- **Views** appear alongside tables and are always read-only.
- **Reload schema** (`Config.AllowReload` or `Admin.Reload`) re-runs
  introspection without restarting the process.
- Rows open in a dialog over the list. Without JavaScript they are ordinary
  pages at the same addresses.

## Security

schemadmin is meant to sit inside an application that already knows who its
users are. It does not log anyone in. What it does:

- **Fails closed.** `New` returns `ErrNoAuthorizer` without an `Authorizer`,
  and anything but `true` from it is a refusal. It is asked on every
  request, assets included, with the action (`ActionView`, `ActionCreate`,
  `ActionUpdate`, `ActionDelete`) and the table.
- **Authorizes indirect reads.** A page that reads a second table (the
  options and labels of a foreign key, the dependent rows on the delete
  page) asks for `ActionView` on that table too, and shows nothing of it
  when refused. A table the request may not view is answered exactly like
  one that does not exist.
- **Binds every value.** No value from a request is ever part of SQL text.
  Table and column names come only from the introspected schema, never from
  the request, and are quoted by the dialect.
- **Refuses cross-site writes** with `http.CrossOriginProtection`. Only POST
  changes anything. Add origins to `Config.TrustedOrigins` if the admin is
  reached through more than one.
- **Escapes everything.** Database content reaches the page only through
  `html/template`. The Content-Security-Policy allows the admin's own
  stylesheet and script and nothing else: no inline script, no framing.
- **Does not leak errors.** An error page carries a reference such as
  `3f9c2ab81d04`; the SQL, the driver's message and the stack trace go to
  the logger under that reference.
- **Masks secrets.** A column whose name contains `password`, `passwd`,
  `secret`, `token`, `api_key`, `private_key`, `salt` or `hash` is never
  selected. Lists and forms show only whether it is set; search, sorting
  and filters ignore it; a form writes it only when a new value is typed.
  Override per column with `ColumnConfig.Mask`.
- **Bounds every query** with `Config.QueryTimeout` (10s by default). The
  row count of a list has its own shorter limit and is skipped when it
  cannot be had in time.

Authentication, TLS and rate limiting belong to the host application. Give
the admin a database role that can do only what you want admins to do.

## Editing is conservative

An admin tool must not change data nobody meant to change.

- NULL and the empty string are different values. Every nullable column has
  its own NULL box.
- Saving writes only the fields that were changed. Opening a row and
  pressing Save without touching anything issues no UPDATE, for every
  column type. The test suite checks this against real databases.
- If someone else changed a field while the form was open, your edit of
  that field is refused instead of overwriting theirs. Fields you did not
  touch keep whatever they now hold.
- On create, a field left blank whose column has a default is left to the
  database. Generated columns are never written.
- A row is updated or deleted by its full primary key, in a transaction
  that rolls back unless exactly one row was affected.

## Configuration

Everything is optional except `DB`, `Dialect` and `Authorizer`.

```go
schemadmin.Config{
	Title:   "Back office",
	Schemas: []string{"public", "billing"}, // tables become "billing.invoices"
	Tables: map[string]schemadmin.TableConfig{
		"orders": {
			Label:         "Orders",
			ListColumns:   []string{"id", "customer_id", "status", "placed_at"},
			SearchColumns: []string{"reference"},
			FilterColumns: []string{"status", "placed_at"},
			DefaultSort:   schemadmin.Sort{Column: "placed_at", Desc: true},
			Columns: map[string]schemadmin.ColumnConfig{
				"status": {Choices: []string{"new", "paid", "shipped"}},
				"notes":  {Widget: schemadmin.WidgetTextarea, Help: "Internal only."},
				"total":  {ReadOnly: true},
			},
		},
		"customers":  {DisplayColumn: "email"},
		"audit_log":  {ReadOnly: true},
		"migrations": {Hidden: true},
	},
}
```

A table or column named in the configuration that does not exist is an
error from `New`, so a typo cannot silently do nothing. See the
documentation of `Config`, `TableConfig` and `ColumnConfig` for the rest.

### List URLs

| Parameter     | Meaning                                      |
| ------------- | -------------------------------------------- |
| `q`           | search text                                  |
| `sort`, `dir` | column to order by; `dir=desc` reverses      |
| `page`        | page number, from 1                          |
| `eq.COLUMN`   | column equals the value                      |
| `has.COLUMN`  | column contains the text                     |
| `from.COLUMN` | column is at least the value                 |
| `to.COLUMN`   | column is at most the value                  |
| `is.COLUMN`   | `null`, `notnull` or `empty`                 |

A parameter that names no column is ignored. A value that is not valid for
its column is ignored and reported on the page.

## Limits

- A row whose primary key holds NULL (SQLite permits this), and any row of
  a table without a primary key, can be read but not edited or deleted,
  because it cannot be addressed safely.
- Views cannot be written, even when the database would allow it.
- A composite foreign key with more than 100 target rows is edited as a
  typed encoded key, not a searchable select.
- Export is CSV only, capped by `Config.ExportMax`.
- Bulk actions are delete only.

## Contributing

Bug reports, dialect fixes and small, well-tested features are welcome.
See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, tests, project layout and
the security rules a change must not weaken.

```sh
go test ./...                        # unit tests: no database, no dependencies
cd integration && go test ./...      # SQLite; Postgres/MySQL when a server is configured
scripts/pg-test.sh                   # throwaway PostgreSQL server
scripts/mysql-test.sh                # throwaway MySQL server
```

## License

MIT
