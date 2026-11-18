# schemadmin: plan

An ORM-agnostic admin panel: takes a `*sql.DB`, introspects the schema at
startup, and serves a CRUD admin UI as a plain `http.Handler`. The core
module uses only the standard library. Postgres and SQLite first, behind a
`Dialect` interface.

## Milestones

1. Schema model, `Dialect`, SQLite introspection. Done.
2. Postgres introspection, tested through pgx and lib/pq. Done.
3. Query builder and value codec. Done.
4. Handler core: `New`, config, authorizer, CSRF, headers, errors, index. Done.
5. List view: search, filters, sorting, pagination. Done.
6. Forms: create and edit, validation, foreign-key selects. Done.
7. Delete with dependent rows. Done.
8. Example module, README, security sweep. Done.

Next: MySQL dialect.

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

Query builder (milestone 3)

- `Dialect.SelectExpr` appears only in the select list. WHERE, ORDER BY and
  join conditions name the bare quoted column, so indexes stay usable. A
  test asserts this on the generated SQL.
- A row is addressed by its full primary key with `column = ?` per key
  column. The row-key encoding has no representation for NULL.
- Every query runs under `Config.QueryTimeout` (default 10s). `COUNT(*)`
  runs under the shorter `Config.CountTimeout`; on timeout the list falls
  back to "is there a next page".

Rows with a NULL primary-key value (milestones 3 and 6)

- Decision: such rows are read-only. They are listed, without an edit or
  delete link, and have no URL.
- Reason: SQLite allows several rows with a NULL key in an ordinary table,
  so `key IS NULL` does not identify one row; an edit or delete addressed
  that way could hit more than one.
- Consequences: the row key cannot encode NULL; UPDATE and DELETE run in a
  transaction and roll back unless exactly one row is affected; forms
  require every primary-key column, so the admin cannot create such rows.
- Tables without a primary key are handled the same way: list and create.

Forms (milestone 6)

- NULL and the empty string stay distinct. Nullable columns have an
  explicit NULL control.
- UPDATE sets only the columns whose submitted value differs from the row
  as loaded. An untouched form issues no UPDATE.
- On create, a blank field with a database default is omitted so the
  default applies. Generated columns are never written.
- Round-trip test: for every column kind, including NULL, load a row,
  submit the form unchanged, and assert the stored row is identical.

List (milestone 5)

- The state of a list is its URL: `q`, `sort`, `dir`, `page`, and per
  column `eq.`, `has.`, `from.`, `to.` and `is.` (null, notnull, empty).
  Parameters that name no column are ignored; values not valid for their
  column are ignored and reported.
- Filters live in a menu behind each column header, built on the Popover
  API. The menu is a plain GET form and plain links.

Pages and dialogs

- Rows, the create form and the delete confirmation are ordinary pages.
  The script opens them in a `<dialog>` over the list by fetching the same
  URL with `X-Schemadmin-Modal: 1`, for which the server sends the page
  without its frame. Nothing depends on the script.

Addressing

- With more than one schema in `Config.Schemas`, tables are `schema.table`
  in URLs, `Config.Tables` keys and foreign-key links.

## Out of scope for v1

Views, bulk actions, export, editing binary columns, select controls for
composite foreign keys, transitive cascade counts, schema reload without a
restart.
