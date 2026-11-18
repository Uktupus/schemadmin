# schemadmin: plan

An ORM-agnostic admin panel: takes a `*sql.DB`, introspects the schema at
startup, and serves a CRUD admin UI as a plain `http.Handler`. The core
module uses only the standard library. PostgreSQL, SQLite and MySQL behind
a `Dialect` interface.

## Milestones

### v1

1. Schema model, `Dialect`, SQLite introspection. Done.
2. Postgres introspection, tested through pgx and lib/pq. Done.
3. Query builder and value codec. Done.
4. Handler core: `New`, config, authorizer, CSRF, headers, errors, index. Done.
5. List view: search, filters, sorting, pagination. Done.
6. Forms: create and edit, validation, foreign-key selects. Done.
7. Delete with dependent rows. Done.
8. Example module, README, security sweep. Done.

### v2

1. MySQL dialect. Done.
2. Views as read-only relations. Done.
3. Bulk delete on the list. Done.
4. CSV export of the current list filter. Done.
5. Editing binary columns as hexadecimal. Done.
6. Select controls for composite foreign keys. Done.
7. Transitive cascade counts on the delete page. Done.
8. Schema reload without a process restart. Done.

## Rules the code must keep

Security

- Fail closed: `New` refuses a nil `Authorizer`; every route checks it.
- Every SQL value is a bound parameter. Identifiers come only from the
  introspected schema and are quoted by the dialect.
- Reads that reach another table (foreign-key options, foreign-key labels,
  dependent counts) check `ActionView` on that table and hide what the user
  may not see.
- CSRF through `http.CrossOriginProtection`; only POST mutates.
- Database values are rendered only through `html/template` escaping.
- Error pages carry a reference ID, never SQL or driver text.
- Columns whose names contain password, passwd, secret, token, api_key,
  private_key, salt or hash are masked by default: never selected, shown as
  set or NULL, write-only in forms. `ColumnConfig.Mask` overrides.

Query builder

- `Dialect.SelectExpr` appears only in the select list. WHERE, ORDER BY and
  join conditions name the bare quoted column, so indexes stay usable.
- A row is addressed by its full primary key with `column = ?` per key
  column. The row-key encoding has no representation for NULL.
- Every query runs under `Config.QueryTimeout` (default 10s). `COUNT(*)`
  runs under the shorter `Config.CountTimeout`; on timeout the list falls
  back to "is there a next page".
- Binary columns are selected as hex text. MySQL has no
  `INSERT … RETURNING`; create uses `LastInsertId` for a single
  auto-increment key.

Forms

- NULL and the empty string stay distinct. Nullable columns have an
  explicit NULL control.
- UPDATE sets only the columns whose submitted value differs from the row
  as loaded. An untouched form issues no UPDATE.
- On create, a blank field with a database default is omitted so the
  default applies. Generated columns are never written.
- Views never accept writes. Binary columns are edited as hex.

List

- The state of a list is its URL: `q`, `sort`, `dir`, `page`, and per
  column `eq.`, `has.`, `from.`, `to.` and `is.` (null, notnull, empty).
- Filters live in a menu behind each column header.
- Export downloads the current filter as CSV. Bulk delete posts selected
  row keys, capped by `Config.BulkLimit`.

Reload

- `Admin.Reload` and, when `Config.AllowReload` is set, `POST /reload`
  re-run introspection under a lock and swap the live metadata.

## Out of scope

Bulk edit of arbitrary columns, export formats other than CSV, editing
of composite foreign keys when the target has more than a hundred rows
(typed encoded key only), schema migration from the UI.
