# Contributing to schemadmin

How the project is developed day to day. Read it before opening a pull
request.

## What this project is

schemadmin is a small, security-conscious admin UI over `*sql.DB`. The
database schema is the source of truth. The core module uses only the Go
standard library. Drivers live in the host application; dialects live in
`postgres/`, `sqlite/` and `mysql/`.

If a change fights those constraints, it almost certainly does not belong
here. Prefer opening an issue first for anything larger than a bug fix.

## Setup

You need Go 1.26 or later.

```sh
git clone https://github.com/Uktupus/schemadmin.git
cd schemadmin
go test ./...
```

Unit tests need no database and no module dependencies. That is
intentional: keep it that way.

To try the UI:

```sh
cd example && go run .
# open http://127.0.0.1:8080/admin/
```

## Tests

| What | Command |
| ---- | ------- |
| Unit (core + dialects) | `go test ./...` |
| Integration, SQLite only | `cd integration && go test ./...` |
| Integration + PostgreSQL | `scripts/pg-test.sh` |
| Integration + MySQL | `scripts/mysql-test.sh` |
| Or against your own servers | set `SCHEMADMIN_POSTGRES_DSN` / `SCHEMADMIN_MYSQL_DSN`, then `cd integration && go test ./...` |

CI runs `gofmt`, `go vet`, race-enabled unit tests, SQLite/Postgres/MySQL
integration tests, and a build of `example/`. Match that locally when you
can: at minimum `gofmt -l .` must be empty and `go test -race ./...` must
pass.

PostgreSQL integration runs under both pgx and lib/pq. Golden files under
`integration/testdata/` are updated with `UPDATE_GOLDEN=1` when an intentional
SQL or scan change requires it. Do not regenerate them to silence a failure
you do not understand.

## Layout

| Path | Role |
| ---- | ---- |
| `*.go` at the root | Handler, query builder, forms, security |
| `assets/` | Embedded HTML, CSS, JS |
| `postgres/`, `sqlite/`, `mysql/` | Dialects: introspection + SQL quirks |
| `integration/` | Separate module; real databases |
| `example/` | Separate module; demo shop on SQLite |
| `scripts/` | Throwaway Postgres/MySQL for local CI |

The core `go.mod` must stay dependency-free. New imports in the root module
will fail CI.

## Rules that must not regress

These are load-bearing. A PR that weakens them will be rejected.

- **Fail closed.** No nil `Authorizer`. Every route checks it. Indirect
  reads (FK options, labels, dependent counts) check `ActionView` on the
  other table too.
- **Bound every value.** Identifiers come only from introspected schema and
  are quoted by the dialect. Never splice request data into SQL text.
- **NULL ≠ empty string.** Forms and the value codec keep them distinct.
- **Conservative writes.** UPDATE only columns that changed. Blank create
  fields with a default are omitted. Generated columns are never written.
- **Views are read-only.**
- **Secrets stay masked** unless `ColumnConfig.Mask` says otherwise.
- **`Dialect.SelectExpr` only in the select list.** WHERE / ORDER BY / join
  conditions use the bare quoted column so indexes stay usable.
- **Errors to users carry a reference ID**, never SQL or driver text.

`PLAN.md` is the feature map and the written contract for these rules.

## Pull requests

1. One concern per PR. Bug fix, dialect fix, docs, or a single feature.
2. Include tests for behaviour changes. Integration tests when SQL or
   scanning is involved.
3. Keep the prose in README, comments and UI calm and specific. No marketing
   filler.
4. Do not commit secrets, local databases, or editor config.
5. Describe *why* in the PR body. Link an issue when there is one.

Opening an issue before a large PR saves everyone time. Good issue reports
include the dialect, a minimal schema, and what you expected versus what
happened.

## Out of scope (for now)

Do not send PRs for these without an agreed design issue first:

- Bulk edit of arbitrary columns
- Export formats other than CSV
- Schema migration from the UI
- Built-in authentication, OAuth, or user management
- An ORM or code-generation layer on top of the schema

## License

By contributing you agree that your contributions are licensed under the
MIT License in `LICENSE`.
