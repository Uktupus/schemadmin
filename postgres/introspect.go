package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Uktupus/schemadmin"
)

// minVersionNum is server_version_num for PostgreSQL 12, the first release
// with pg_attribute.attgenerated.
const minVersionNum = 120000

// Every statement takes one parameter: the schema names as a JSON array of
// strings. That keeps the list a single bound text value, which every
// driver can send, and its position in the array orders the result.
//
// The catalogs are read directly rather than through information_schema,
// which hides the order of composite keys and the details of identity and
// generated columns.
const (
	missingSchemasQuery = `
SELECT s.name
FROM json_array_elements_text($1::text::json) AS s(name)
WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace AS n WHERE n.nspname = s.name)`

	// Ordinary and partitioned tables. The partitions of a partitioned
	// table are left out: their rows are the parent's rows.
	tablesQuery = `
SELECT n.nspname, c.relname, c.relkind::text
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
JOIN json_array_elements_text($1::text::json) WITH ORDINALITY AS s(name, ord) ON s.name = n.nspname
WHERE c.relkind IN ('r', 'p', 'v') AND NOT c.relispartition
ORDER BY s.ord, c.relname`

	// domain_base resolves each domain to the type it is ultimately built
	// on, following domains over domains. Along the way it collects
	// whether any domain in the chain is NOT NULL, and the type modifier,
	// which belongs to the innermost domain.
	columnsQuery = `
WITH RECURSIVE domain_chain AS (
    SELECT t.oid AS domain_oid, t.typbasetype AS base_oid, t.typtypmod AS typmod, t.typnotnull AS not_null
    FROM pg_catalog.pg_type AS t
    WHERE t.typtype = 'd'
  UNION ALL
    SELECT d.domain_oid, b.typbasetype, b.typtypmod, d.not_null OR b.typnotnull
    FROM domain_chain AS d
    JOIN pg_catalog.pg_type AS b ON b.oid = d.base_oid
    WHERE b.typtype = 'd'
), domain_base AS (
    SELECT d.domain_oid, d.base_oid, d.typmod, d.not_null
    FROM domain_chain AS d
    JOIN pg_catalog.pg_type AS b ON b.oid = d.base_oid
    WHERE b.typtype <> 'd'
)
SELECT n.nspname, c.relname, a.attname,
       pg_catalog.format_type(a.atttypid, a.atttypmod),
       a.attnotnull OR COALESCE(db.not_null, false),
       a.attidentity::text,
       a.attgenerated::text,
       CASE WHEN a.attgenerated = ''
            THEN COALESCE(pg_catalog.pg_get_expr(ad.adbin, ad.adrelid), t.typdefault)
       END,
       bn.nspname, bt.typname, bt.typtype::text,
       CASE WHEN db.domain_oid IS NULL THEN a.atttypmod ELSE db.typmod END
FROM pg_catalog.pg_attribute AS a
JOIN pg_catalog.pg_class AS c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
JOIN json_array_elements_text($1::text::json) WITH ORDINALITY AS s(name, ord) ON s.name = n.nspname
JOIN pg_catalog.pg_type AS t ON t.oid = a.atttypid
LEFT JOIN domain_base AS db ON db.domain_oid = a.atttypid
JOIN pg_catalog.pg_type AS bt ON bt.oid = COALESCE(db.base_oid, a.atttypid)
JOIN pg_catalog.pg_namespace AS bn ON bn.oid = bt.typnamespace
LEFT JOIN pg_catalog.pg_attrdef AS ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped
  AND c.relkind IN ('r', 'p', 'v') AND NOT c.relispartition
ORDER BY s.ord, c.relname, a.attnum`

	enumsQuery = `
SELECT n.nspname, t.typname, e.enumlabel
FROM pg_catalog.pg_enum AS e
JOIN pg_catalog.pg_type AS t ON t.oid = e.enumtypid
JOIN pg_catalog.pg_namespace AS n ON n.oid = t.typnamespace
ORDER BY n.nspname, t.typname, e.enumsortorder`

	primaryKeysQuery = `
SELECT n.nspname, c.relname, a.attname
FROM pg_catalog.pg_constraint AS con
JOIN pg_catalog.pg_class AS c ON c.oid = con.conrelid
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
JOIN json_array_elements_text($1::text::json) WITH ORDINALITY AS s(name, ord) ON s.name = n.nspname
CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS k(attnum, pos)
JOIN pg_catalog.pg_attribute AS a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
WHERE con.contype = 'p' AND NOT c.relispartition
ORDER BY s.ord, c.relname, k.pos`

	// conparentid = 0 keeps the constraint as declared and skips the
	// copies PostgreSQL makes of it for each partition involved.
	foreignKeysQuery = `
SELECT n.nspname, c.relname, con.conname,
       fn.nspname, fc.relname,
       con.confdeltype::text, con.confupdtype::text,
       a.attname, fa.attname
FROM pg_catalog.pg_constraint AS con
JOIN pg_catalog.pg_class AS c ON c.oid = con.conrelid
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
JOIN json_array_elements_text($1::text::json) WITH ORDINALITY AS s(name, ord) ON s.name = n.nspname
JOIN pg_catalog.pg_class AS fc ON fc.oid = con.confrelid
JOIN pg_catalog.pg_namespace AS fn ON fn.oid = fc.relnamespace
CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(attnum, ref_attnum, pos)
JOIN pg_catalog.pg_attribute AS a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
JOIN pg_catalog.pg_attribute AS fa ON fa.attrelid = con.confrelid AND fa.attnum = k.ref_attnum
WHERE con.contype = 'f' AND con.conparentid = 0 AND NOT c.relispartition
ORDER BY s.ord, c.relname, con.oid, k.pos`
)

// qualifiedName identifies a table or type within the database.
type qualifiedName struct{ schema, name string }

// Introspect reads the ordinary and partitioned tables of the schemas named
// in opts.Schemas, or of "public" when none are named. Tables come back
// grouped by schema in the order the schemas were given. Views, foreign
// tables and the partitions of partitioned tables are skipped.
func (Dialect) Introspect(ctx context.Context, db *sql.DB, opts schemadmin.IntrospectOptions) (*schemadmin.Schema, error) {
	schemas, err := schemaNames(opts.Schemas)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(schemas)
	if err != nil {
		return nil, fmt.Errorf("postgres: encode schema names: %w", err)
	}
	param := string(encoded)

	// A read-only repeatable-read transaction gives every query the same
	// snapshot of the catalogs.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("postgres: begin introspection: %w", err)
	}
	defer tx.Rollback()

	if err := checkVersion(ctx, tx); err != nil {
		return nil, err
	}
	if err := checkSchemasExist(ctx, tx, param); err != nil {
		return nil, err
	}

	enums, err := loadEnums(ctx, tx)
	if err != nil {
		return nil, err
	}
	schema, tables, err := loadTables(ctx, tx, param)
	if err != nil {
		return nil, err
	}
	if err := loadColumns(ctx, tx, param, tables, enums); err != nil {
		return nil, err
	}
	if err := loadPrimaryKeys(ctx, tx, param, tables); err != nil {
		return nil, err
	}
	if err := loadForeignKeys(ctx, tx, param, tables); err != nil {
		return nil, err
	}

	if err := schema.Validate(); err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return schema, nil
}

// schemaNames returns the schemas to read: the given ones without
// repetitions, or "public".
func schemaNames(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return []string{"public"}, nil
	}
	seen := make(map[string]bool, len(requested))
	var names []string
	for _, name := range requested {
		if name == "" {
			return nil, fmt.Errorf("postgres: empty schema name")
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

func checkVersion(ctx context.Context, tx *sql.Tx) error {
	var num int
	if err := tx.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		return fmt.Errorf("postgres: read version: %w", err)
	}
	if num < minVersionNum {
		return fmt.Errorf("postgres: server version %d.%d is not supported; 12 or later is required", num/10000, num/100%100)
	}
	return nil
}

// checkSchemasExist fails when a requested schema is missing, so that a
// misspelt name is an error rather than an empty admin.
func checkSchemasExist(ctx context.Context, tx *sql.Tx, param string) error {
	rows, err := tx.QueryContext(ctx, missingSchemasQuery, param)
	if err != nil {
		return fmt.Errorf("postgres: check schemas: %w", err)
	}
	defer rows.Close()

	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("postgres: check schemas: %w", err)
		}
		missing = append(missing, fmt.Sprintf("%q", name))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: check schemas: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("postgres: schema %s does not exist", strings.Join(missing, ", "))
	}
	return nil
}

// loadEnums returns the labels of every enum type, in their sort order.
func loadEnums(ctx context.Context, tx *sql.Tx) (map[qualifiedName][]string, error) {
	rows, err := tx.QueryContext(ctx, enumsQuery)
	if err != nil {
		return nil, fmt.Errorf("postgres: list enums: %w", err)
	}
	defer rows.Close()

	enums := make(map[qualifiedName][]string)
	for rows.Next() {
		var typ qualifiedName
		var label string
		if err := rows.Scan(&typ.schema, &typ.name, &label); err != nil {
			return nil, fmt.Errorf("postgres: list enums: %w", err)
		}
		enums[typ] = append(enums[typ], label)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list enums: %w", err)
	}
	return enums, nil
}

func loadTables(ctx context.Context, tx *sql.Tx, param string) (*schemadmin.Schema, map[qualifiedName]*schemadmin.Table, error) {
	rows, err := tx.QueryContext(ctx, tablesQuery, param)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: list tables: %w", err)
	}
	defer rows.Close()

	schema := &schemadmin.Schema{}
	tables := make(map[qualifiedName]*schemadmin.Table)
	for rows.Next() {
		var key qualifiedName
		var kind string
		if err := rows.Scan(&key.schema, &key.name, &kind); err != nil {
			return nil, nil, fmt.Errorf("postgres: list tables: %w", err)
		}
		table := &schemadmin.Table{Schema: key.schema, Name: key.name, View: kind == "v"}
		schema.Tables = append(schema.Tables, table)
		tables[key] = table
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("postgres: list tables: %w", err)
	}
	return schema, tables, nil
}

func loadColumns(ctx context.Context, tx *sql.Tx, param string, tables map[qualifiedName]*schemadmin.Table, enums map[qualifiedName][]string) error {
	rows, err := tx.QueryContext(ctx, columnsQuery, param)
	if err != nil {
		return fmt.Errorf("postgres: list columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			key       qualifiedName
			name      string
			dbType    string
			notNull   bool
			identity  string // "" | "a" (always) | "d" (by default)
			generated string // "" | "s" (stored) | "v" (virtual)
			dflt      sql.NullString
			baseType  qualifiedName
			typType   string
			typmod    int
		)
		if err := rows.Scan(&key.schema, &key.name, &name, &dbType, &notNull, &identity, &generated,
			&dflt, &baseType.schema, &baseType.name, &typType, &typmod); err != nil {
			return fmt.Errorf("postgres: list columns: %w", err)
		}
		table := tables[key]
		if table == nil {
			continue
		}

		column := &schemadmin.Column{Name: name, DBType: dbType, Nullable: !notNull}
		column.Kind, column.MaxLength = kindOf(baseType.schema, baseType.name, typType, typmod)
		if column.Kind == schemadmin.KindEnum {
			column.EnumValues = enums[baseType]
			// An enum type without labels admits no value at all.
			if len(column.EnumValues) == 0 {
				column.Kind = schemadmin.KindOther
			}
		}

		switch {
		case generated != "", identity == "a":
			// Computed, or GENERATED ALWAYS AS IDENTITY: PostgreSQL
			// rejects an ordinary write to either.
			column.Generated = true
		case identity == "d":
			column.AutoIncrement = true
			column.HasDefault = true
		case dflt.Valid:
			column.HasDefault = true
			column.Default = dflt.String
			// serial and bigserial are a nextval() default.
			column.AutoIncrement = strings.HasPrefix(dflt.String, "nextval(")
		}
		table.Columns = append(table.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: list columns: %w", err)
	}
	return nil
}

func loadPrimaryKeys(ctx context.Context, tx *sql.Tx, param string, tables map[qualifiedName]*schemadmin.Table) error {
	rows, err := tx.QueryContext(ctx, primaryKeysQuery, param)
	if err != nil {
		return fmt.Errorf("postgres: list primary keys: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key qualifiedName
		var column string
		if err := rows.Scan(&key.schema, &key.name, &column); err != nil {
			return fmt.Errorf("postgres: list primary keys: %w", err)
		}
		if table := tables[key]; table != nil {
			table.PrimaryKey = append(table.PrimaryKey, column)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: list primary keys: %w", err)
	}
	return nil
}

func loadForeignKeys(ctx context.Context, tx *sql.Tx, param string, tables map[qualifiedName]*schemadmin.Table) error {
	rows, err := tx.QueryContext(ctx, foreignKeysQuery, param)
	if err != nil {
		return fmt.Errorf("postgres: list foreign keys: %w", err)
	}
	defer rows.Close()

	var (
		current      *schemadmin.ForeignKey
		currentTable qualifiedName
	)
	for rows.Next() {
		var (
			key       qualifiedName
			name      string
			ref       qualifiedName
			onDelete  string
			onUpdate  string
			column    string
			refColumn string
		)
		if err := rows.Scan(&key.schema, &key.name, &name, &ref.schema, &ref.name,
			&onDelete, &onUpdate, &column, &refColumn); err != nil {
			return fmt.Errorf("postgres: list foreign keys: %w", err)
		}
		table := tables[key]
		if table == nil {
			continue
		}
		// Rows arrive grouped by constraint, and constraint names are
		// unique within a table.
		if current == nil || key != currentTable || name != current.Name {
			current = &schemadmin.ForeignKey{
				Name:      name,
				RefSchema: ref.schema,
				RefTable:  ref.name,
				OnDelete:  parseAction(onDelete),
				OnUpdate:  parseAction(onUpdate),
			}
			currentTable = key
			table.ForeignKeys = append(table.ForeignKeys, current)
		}
		current.Columns = append(current.Columns, column)
		current.RefColumns = append(current.RefColumns, refColumn)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: list foreign keys: %w", err)
	}
	return nil
}
