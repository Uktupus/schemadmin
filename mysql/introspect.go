package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Uktupus/schemadmin"
)

const minVersion = "8.0.0"

// Schemas are passed as a JSON array of strings. JSON_TABLE expands it for
// use in IN predicates with a single bound parameter per statement.
const (
	currentDatabaseQuery = `SELECT DATABASE()`

	missingSchemasQuery = `
SELECT s.name
FROM JSON_TABLE(?,'$[*]' COLUMNS(name VARCHAR(64) PATH '$' ERROR ON ERROR)) AS s
WHERE s.name NOT IN (SELECT SCHEMA_NAME FROM information_schema.SCHEMATA)`

	tablesQuery = `
SELECT t.TABLE_SCHEMA, t.TABLE_NAME, t.TABLE_TYPE
FROM information_schema.TABLES AS t
JOIN JSON_TABLE(?,'$[*]' COLUMNS(ord FOR ORDINALITY, name VARCHAR(64) PATH '$' ERROR ON ERROR)) AS s
  ON s.name = t.TABLE_SCHEMA
WHERE t.TABLE_TYPE IN ('BASE TABLE', 'VIEW')
ORDER BY s.ord, t.TABLE_NAME`

	columnsQuery = `
SELECT c.TABLE_SCHEMA, c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.DATA_TYPE,
       c.IS_NULLABLE, c.COLUMN_DEFAULT, c.EXTRA, c.GENERATION_EXPRESSION
FROM information_schema.COLUMNS AS c
JOIN JSON_TABLE(?,'$[*]' COLUMNS(ord FOR ORDINALITY, name VARCHAR(64) PATH '$' ERROR ON ERROR)) AS s
  ON s.name = c.TABLE_SCHEMA
WHERE c.TABLE_SCHEMA IN (SELECT j.name FROM JSON_TABLE(?,'$[*]' COLUMNS(name VARCHAR(64) PATH '$' ERROR ON ERROR)) AS j)
ORDER BY s.ord, c.TABLE_NAME, c.ORDINAL_POSITION`

	primaryKeysQuery = `
SELECT k.TABLE_SCHEMA, k.TABLE_NAME, k.COLUMN_NAME, k.SEQ_IN_INDEX
FROM information_schema.STATISTICS AS k
JOIN JSON_TABLE(?,'$[*]' COLUMNS(ord FOR ORDINALITY, name VARCHAR(64) PATH '$' ERROR ON ERROR)) AS s
  ON s.name = k.TABLE_SCHEMA
WHERE k.INDEX_NAME = 'PRIMARY'
ORDER BY s.ord, k.TABLE_NAME, k.SEQ_IN_INDEX`

	foreignKeysQuery = `
SELECT k.TABLE_SCHEMA, k.TABLE_NAME, k.CONSTRAINT_NAME,
       k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME,
       r.DELETE_RULE, r.UPDATE_RULE,
       k.COLUMN_NAME, k.REFERENCED_COLUMN_NAME, k.ORDINAL_POSITION
FROM information_schema.KEY_COLUMN_USAGE AS k
JOIN information_schema.REFERENTIAL_CONSTRAINTS AS r
  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
JOIN JSON_TABLE(?,'$[*]' COLUMNS(ord FOR ORDINALITY, name VARCHAR(64) PATH '$' ERROR ON ERROR)) AS s
  ON s.name = k.TABLE_SCHEMA
WHERE k.REFERENCED_TABLE_NAME IS NOT NULL
ORDER BY s.ord, k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`
)

type qualifiedName struct{ schema, name string }

// Introspect reads the base tables of the schemas named in opts.Schemas, or
// of the connection's default database when none are named. Tables come back
// grouped by schema in the order the schemas were given.
func (Dialect) Introspect(ctx context.Context, db *sql.DB, opts schemadmin.IntrospectOptions) (*schemadmin.Schema, error) {
	schemas, err := resolveSchemas(ctx, db, opts.Schemas)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(schemas)
	if err != nil {
		return nil, fmt.Errorf("mysql: encode schema names: %w", err)
	}
	param := string(encoded)

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("mysql: begin introspection: %w", err)
	}
	defer tx.Rollback()

	if err := checkVersion(ctx, tx); err != nil {
		return nil, err
	}
	if err := checkSchemasExist(ctx, tx, param); err != nil {
		return nil, err
	}

	schema, tables, err := loadTables(ctx, tx, param)
	if err != nil {
		return nil, err
	}
	if err := loadColumns(ctx, tx, param, tables); err != nil {
		return nil, err
	}
	if err := loadPrimaryKeys(ctx, tx, param, tables); err != nil {
		return nil, err
	}
	if err := loadForeignKeys(ctx, tx, param, tables); err != nil {
		return nil, err
	}

	if err := schema.Validate(); err != nil {
		return nil, fmt.Errorf("mysql: %w", err)
	}
	return schema, nil
}

func resolveSchemas(ctx context.Context, db *sql.DB, requested []string) ([]string, error) {
	if len(requested) == 0 {
		var name sql.NullString
		if err := db.QueryRowContext(ctx, currentDatabaseQuery).Scan(&name); err != nil {
			return nil, fmt.Errorf("mysql: read default database: %w", err)
		}
		if !name.Valid || name.String == "" {
			return nil, fmt.Errorf("mysql: no database is selected")
		}
		return []string{name.String}, nil
	}
	seen := make(map[string]bool, len(requested))
	var names []string
	for _, name := range requested {
		if name == "" {
			return nil, fmt.Errorf("mysql: empty schema name")
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

func checkVersion(ctx context.Context, tx *sql.Tx) error {
	var version string
	if err := tx.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return fmt.Errorf("mysql: read version: %w", err)
	}
	if !versionAtLeast(version, minVersion) {
		return fmt.Errorf("mysql: server version %s is not supported; %s or later is required", version, minVersion)
	}
	return nil
}

func versionAtLeast(version, min string) bool {
	return strings.Compare(normalizeVersion(version), normalizeVersion(min)) >= 0
}

// normalizeVersion keeps the leading numeric prefix of a version string.
func normalizeVersion(version string) string {
	var b strings.Builder
	for _, r := range version {
		if (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
		} else {
			break
		}
	}
	return b.String()
}

func checkSchemasExist(ctx context.Context, tx *sql.Tx, param string) error {
	rows, err := tx.QueryContext(ctx, missingSchemasQuery, param)
	if err != nil {
		return fmt.Errorf("mysql: check schemas: %w", err)
	}
	defer rows.Close()

	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("mysql: check schemas: %w", err)
		}
		missing = append(missing, fmt.Sprintf("%q", name))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("mysql: check schemas: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("mysql: schema %s does not exist", strings.Join(missing, ", "))
	}
	return nil
}

func loadTables(ctx context.Context, tx *sql.Tx, param string) (*schemadmin.Schema, map[qualifiedName]*schemadmin.Table, error) {
	rows, err := tx.QueryContext(ctx, tablesQuery, param)
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: list tables: %w", err)
	}
	defer rows.Close()

	schema := &schemadmin.Schema{}
	tables := make(map[qualifiedName]*schemadmin.Table)
	for rows.Next() {
		var key qualifiedName
		var kind string
		if err := rows.Scan(&key.schema, &key.name, &kind); err != nil {
			return nil, nil, fmt.Errorf("mysql: list tables: %w", err)
		}
		table := &schemadmin.Table{Schema: key.schema, Name: key.name, View: kind == "VIEW"}
		schema.Tables = append(schema.Tables, table)
		tables[key] = table
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("mysql: list tables: %w", err)
	}
	return schema, tables, nil
}

func loadColumns(ctx context.Context, tx *sql.Tx, param string, tables map[qualifiedName]*schemadmin.Table) error {
	rows, err := tx.QueryContext(ctx, columnsQuery, param, param)
	if err != nil {
		return fmt.Errorf("mysql: list columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			key        qualifiedName
			name       string
			columnType string
			dataType   string
			nullable   string
			dflt       sql.NullString
			extra      string
			generation sql.NullString
		)
		if err := rows.Scan(&key.schema, &key.name, &name, &columnType, &dataType,
			&nullable, &dflt, &extra, &generation); err != nil {
			return fmt.Errorf("mysql: list columns: %w", err)
		}
		table := tables[key]
		if table == nil {
			continue
		}

		column := &schemadmin.Column{
			Name:     name,
			DBType:   columnType,
			Nullable: strings.EqualFold(nullable, "YES"),
		}
		column.Kind, column.MaxLength = kindOf(dataType, columnType)
		if column.Kind == schemadmin.KindEnum {
			column.EnumValues = parseEnumValues(columnType)
			if len(column.EnumValues) == 0 {
				column.Kind = schemadmin.KindOther
			}
		}

		extraLower := strings.ToLower(extra)
		if strings.Contains(extraLower, "auto_increment") {
			column.AutoIncrement = true
			column.HasDefault = true
		} else if dflt.Valid {
			column.HasDefault = true
			column.Default = dflt.String
		}
		switch {
		case generation.Valid && generation.String != "":
			column.Generated = true
		case strings.Contains(extraLower, "stored generated"),
			strings.Contains(extraLower, "virtual generated"):
			column.Generated = true
		}

		table.Columns = append(table.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("mysql: list columns: %w", err)
	}
	return nil
}

func loadPrimaryKeys(ctx context.Context, tx *sql.Tx, param string, tables map[qualifiedName]*schemadmin.Table) error {
	rows, err := tx.QueryContext(ctx, primaryKeysQuery, param)
	if err != nil {
		return fmt.Errorf("mysql: list primary keys: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key qualifiedName
		var column string
		var seq int
		if err := rows.Scan(&key.schema, &key.name, &column, &seq); err != nil {
			return fmt.Errorf("mysql: list primary keys: %w", err)
		}
		if table := tables[key]; table != nil {
			table.PrimaryKey = append(table.PrimaryKey, column)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("mysql: list primary keys: %w", err)
	}
	return nil
}

func loadForeignKeys(ctx context.Context, tx *sql.Tx, param string, tables map[qualifiedName]*schemadmin.Table) error {
	rows, err := tx.QueryContext(ctx, foreignKeysQuery, param)
	if err != nil {
		return fmt.Errorf("mysql: list foreign keys: %w", err)
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
			refSchema string
			refTable  string
			onDelete  string
			onUpdate  string
			column    string
			refColumn string
			ordinal   int
		)
		if err := rows.Scan(&key.schema, &key.name, &name, &refSchema, &refTable,
			&onDelete, &onUpdate, &column, &refColumn, &ordinal); err != nil {
			return fmt.Errorf("mysql: list foreign keys: %w", err)
		}
		table := tables[key]
		if table == nil {
			continue
		}
		if refSchema == "" {
			refSchema = key.schema
		}
		if current == nil || key != currentTable || name != current.Name {
			current = &schemadmin.ForeignKey{
				Name:      name,
				RefSchema: refSchema,
				RefTable:  refTable,
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
		return fmt.Errorf("mysql: list foreign keys: %w", err)
	}
	return nil
}
