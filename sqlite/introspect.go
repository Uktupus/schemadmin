package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/Uktupus/schemadmin"
)

// Table names never appear in the text of these statements. Each PRAGMA is
// used in its table-valued form and receives the name as a value taken from
// pragma_table_list, so introspection needs no identifier quoting at all.
const (
	tablesQuery = `
SELECT l.name, l.wr, COALESCE(m.sql, ''), l.type
FROM pragma_table_list AS l
LEFT JOIN main.sqlite_master AS m ON m.type IN ('table', 'view') AND m.name = l.name
WHERE l.schema = 'main' AND l.type IN ('table', 'view')
ORDER BY l.name`

	columnsQuery = `
SELECT l.name, x.name, COALESCE(x.type, ''), x."notnull", x.dflt_value, x.pk, x.hidden
FROM pragma_table_list AS l, pragma_table_xinfo(l.name, 'main') AS x
WHERE l.schema = 'main' AND l.type IN ('table', 'view')
ORDER BY l.name, x.cid`

	primaryKeyIndexQuery = `
SELECT DISTINCT l.name
FROM pragma_table_list AS l, pragma_index_list(l.name, 'main') AS i
WHERE l.schema = 'main' AND l.type = 'table' AND i.origin = 'pk'`

	foreignKeysQuery = `
SELECT l.name, f.id, f."table", f."from", f."to", f.on_update, f.on_delete
FROM pragma_table_list AS l, pragma_foreign_key_list(l.name, 'main') AS f
WHERE l.schema = 'main' AND l.type = 'table'
ORDER BY l.name, f.id DESC, f.seq`
)

// Values of the "hidden" column of pragma_table_xinfo.
const (
	hiddenVirtualTableColumn = 1
	hiddenGeneratedVirtual   = 2
	hiddenGeneratedStored    = 3
)

// tableInfo is a table under construction plus the facts needed to finish it.
type tableInfo struct {
	table        *schemadmin.Table
	withoutRowID bool
	ddl          string
	hasPKIndex   bool
	pkPosition   map[string]int // column name -> 1-based position in the key
}

// foreignKeyInfo is one foreign key as pragma_foreign_key_list reports it.
type foreignKeyInfo struct {
	refTable string
	from     []string
	to       []sql.NullString // NULL when the parent's primary key is implied
	onUpdate string
	onDelete string
}

// Introspect reads the ordinary tables and views of the main database.
// Virtual tables, their shadow tables and SQLite's internal tables are
// skipped. Views are marked Table.View and have no primary key or foreign
// keys. A foreign key that SQLite accepted but can never enforce is left
// out and logged at warn level.
func (Dialect) Introspect(ctx context.Context, db *sql.DB, opts schemadmin.IntrospectOptions) (*schemadmin.Schema, error) {
	for _, s := range opts.Schemas {
		if s != "main" {
			return nil, fmt.Errorf(`sqlite: schema %q is not supported; only "main" is introspected`, s)
		}
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// One transaction gives every query the same snapshot of the schema.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite: begin introspection: %w", err)
	}
	defer tx.Rollback()

	if err := checkVersion(ctx, tx); err != nil {
		return nil, err
	}

	tables, byName, err := loadTables(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := loadColumns(ctx, tx, byName); err != nil {
		return nil, err
	}
	if err := loadPrimaryKeyIndexes(ctx, tx, byName); err != nil {
		return nil, err
	}
	foreignKeys, err := loadForeignKeys(ctx, tx, byName)
	if err != nil {
		return nil, err
	}

	schema := &schemadmin.Schema{}
	for _, info := range tables {
		finishPrimaryKey(info)
		applyEnums(info)
		schema.Tables = append(schema.Tables, info.table)
	}
	// Foreign keys are resolved last: an implied reference needs the
	// parent's primary key.
	for _, info := range tables {
		for _, raw := range foreignKeys[info.table.Name] {
			fk, reason := resolveForeignKey(schema, info.table, raw)
			if fk == nil {
				logger.WarnContext(ctx, "schemadmin: foreign key ignored",
					slog.String("dialect", "sqlite"),
					slog.String("table", info.table.Name),
					slog.Any("columns", raw.from),
					slog.String("references", raw.refTable),
					slog.String("reason", reason),
				)
				continue
			}
			info.table.ForeignKeys = append(info.table.ForeignKeys, fk)
		}
	}

	if err := schema.Validate(); err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	return schema, nil
}

func checkVersion(ctx context.Context, tx *sql.Tx) error {
	var version string
	if err := tx.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
		return fmt.Errorf("sqlite: read version: %w", err)
	}
	if !versionAtLeast(version, 3, 37) {
		return fmt.Errorf("sqlite: version %s is not supported; 3.37 or later is required", version)
	}
	return nil
}

// versionAtLeast reports whether a "major.minor.patch" version string is at
// least major.minor.
func versionAtLeast(version string, major, minor int) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	gotMinor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

func loadTables(ctx context.Context, tx *sql.Tx) ([]*tableInfo, map[string]*tableInfo, error) {
	rows, err := tx.QueryContext(ctx, tablesQuery)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite: list tables: %w", err)
	}
	defer rows.Close()

	var tables []*tableInfo
	byName := make(map[string]*tableInfo)
	for rows.Next() {
		var (
			name         string
			withoutRowID bool
			ddl          string
			kind         string
		)
		if err := rows.Scan(&name, &withoutRowID, &ddl, &kind); err != nil {
			return nil, nil, fmt.Errorf("sqlite: list tables: %w", err)
		}
		// The "sqlite_" prefix is reserved for internal tables.
		if strings.HasPrefix(foldASCII(name), "sqlite_") {
			continue
		}
		info := &tableInfo{
			table:        &schemadmin.Table{Name: name, View: kind == "view"},
			withoutRowID: withoutRowID,
			ddl:          ddl,
			pkPosition:   make(map[string]int),
		}
		tables = append(tables, info)
		byName[name] = info
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("sqlite: list tables: %w", err)
	}
	return tables, byName, nil
}

func loadColumns(ctx context.Context, tx *sql.Tx, byName map[string]*tableInfo) error {
	rows, err := tx.QueryContext(ctx, columnsQuery)
	if err != nil {
		return fmt.Errorf("sqlite: list columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tableName string
			name      string
			declType  string
			notNull   bool
			dflt      sql.NullString
			pk        int
			hidden    int
		)
		if err := rows.Scan(&tableName, &name, &declType, &notNull, &dflt, &pk, &hidden); err != nil {
			return fmt.Errorf("sqlite: list columns: %w", err)
		}
		info := byName[tableName]
		if info == nil || hidden == hiddenVirtualTableColumn {
			continue
		}

		generated := hidden == hiddenGeneratedVirtual || hidden == hiddenGeneratedStored
		declType = strings.TrimSpace(declType)
		if generated {
			declType = stripGenerated(declType)
		}
		info.table.Columns = append(info.table.Columns, &schemadmin.Column{
			Name:       name,
			DBType:     declType,
			Kind:       kindOf(declType),
			Nullable:   !notNull,
			HasDefault: dflt.Valid,
			Default:    dflt.String,
			Generated:  generated,
		})
		if pk > 0 {
			info.pkPosition[name] = pk
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: list columns: %w", err)
	}
	return nil
}

func loadPrimaryKeyIndexes(ctx context.Context, tx *sql.Tx, byName map[string]*tableInfo) error {
	rows, err := tx.QueryContext(ctx, primaryKeyIndexQuery)
	if err != nil {
		return fmt.Errorf("sqlite: list indexes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return fmt.Errorf("sqlite: list indexes: %w", err)
		}
		if info := byName[tableName]; info != nil {
			info.hasPKIndex = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: list indexes: %w", err)
	}
	return nil
}

func loadForeignKeys(ctx context.Context, tx *sql.Tx, byName map[string]*tableInfo) (map[string][]*foreignKeyInfo, error) {
	rows, err := tx.QueryContext(ctx, foreignKeysQuery)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list foreign keys: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]*foreignKeyInfo)
	var (
		current      *foreignKeyInfo
		currentTable string
		currentID    int
	)
	for rows.Next() {
		var (
			tableName string
			id        int
			refTable  string
			from      string
			to        sql.NullString
			onUpdate  string
			onDelete  string
		)
		if err := rows.Scan(&tableName, &id, &refTable, &from, &to, &onUpdate, &onDelete); err != nil {
			return nil, fmt.Errorf("sqlite: list foreign keys: %w", err)
		}
		if byName[tableName] == nil {
			continue
		}
		// Rows arrive ordered by table, key id and column sequence, so a
		// change of table or id starts a new key.
		if current == nil || tableName != currentTable || id != currentID {
			current = &foreignKeyInfo{refTable: refTable, onUpdate: onUpdate, onDelete: onDelete}
			currentTable, currentID = tableName, id
			result[tableName] = append(result[tableName], current)
		}
		current.from = append(current.from, from)
		current.to = append(current.to, to)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list foreign keys: %w", err)
	}
	return result, nil
}

// finishPrimaryKey orders the key columns and detects the rowid alias.
func finishPrimaryKey(info *tableInfo) {
	t := info.table
	if t.View {
		t.PrimaryKey = nil
		return
	}
	for name := range info.pkPosition {
		t.PrimaryKey = append(t.PrimaryKey, name)
	}
	sort.Slice(t.PrimaryKey, func(i, j int) bool {
		return info.pkPosition[t.PrimaryKey[i]] < info.pkPosition[t.PrimaryKey[j]]
	})

	// A lone INTEGER PRIMARY KEY column of a rowid table is an alias for
	// the rowid: it cannot hold NULL and SQLite assigns it when omitted.
	// The alias is the one primary key SQLite does not back with an index,
	// which also rules out the "INTEGER PRIMARY KEY DESC" exception.
	if info.withoutRowID || info.hasPKIndex || len(t.PrimaryKey) != 1 {
		return
	}
	c := t.Column(t.PrimaryKey[0])
	if c == nil || !strings.EqualFold(c.DBType, "INTEGER") {
		return
	}
	c.AutoIncrement = true
	c.HasDefault = true
	c.Nullable = false
}

// applyEnums turns text columns restricted by a CHECK ... IN list into enums.
func applyEnums(info *tableInfo) {
	enums := checkEnums(info.ddl)
	if len(enums) == 0 {
		return
	}
	for _, c := range info.table.Columns {
		values, ok := enums[foldASCII(c.Name)]
		if !ok {
			continue
		}
		switch c.Kind {
		case schemadmin.KindString, schemadmin.KindText, schemadmin.KindOther:
			c.Kind = schemadmin.KindEnum
			c.EnumValues = values
		}
	}
}

// resolveForeignKey converts a raw foreign key into the schema model,
// replacing names as written in the constraint with the names the tables
// and columns really have (SQLite matches identifiers case-insensitively).
//
// SQLite accepts foreign keys it can never enforce: ones that name a missing
// parent column, or that imply the primary key of a parent that is missing
// or has a key of a different width. Those cannot be represented, so for
// them resolveForeignKey returns nil and the reason.
func resolveForeignKey(schema *schemadmin.Schema, child *schemadmin.Table, raw *foreignKeyInfo) (*schemadmin.ForeignKey, string) {
	fk := &schemadmin.ForeignKey{
		RefTable: raw.refTable,
		OnDelete: parseAction(raw.onDelete),
		OnUpdate: parseAction(raw.onUpdate),
	}
	if raw.refTable == "" {
		return nil, "the referenced table name is empty"
	}

	for _, name := range raw.from {
		c := findColumn(child, name)
		if c == nil {
			return nil, fmt.Sprintf("column %q does not exist in the table", name)
		}
		fk.Columns = append(fk.Columns, c.Name)
	}

	implied := false
	for _, to := range raw.to {
		if !to.Valid {
			implied = true
		}
	}

	parent := findTable(schema, raw.refTable)
	switch {
	case parent == nil && implied:
		return nil, fmt.Sprintf("referenced table %q does not exist and the constraint names no columns of it", raw.refTable)
	case parent == nil:
		// The parent is not an introspected table. Keep the reference
		// as written.
		for _, to := range raw.to {
			if to.String == "" {
				return nil, "a referenced column name is empty"
			}
			fk.RefColumns = append(fk.RefColumns, to.String)
		}
	case implied:
		if len(parent.PrimaryKey) == 0 {
			return nil, fmt.Sprintf("the constraint implies the primary key of %q, which has none", parent.Name)
		}
		if len(parent.PrimaryKey) != len(fk.Columns) {
			return nil, fmt.Sprintf("the constraint implies the primary key of %q, which has %d columns where the constraint has %d",
				parent.Name, len(parent.PrimaryKey), len(fk.Columns))
		}
		fk.RefTable = parent.Name
		fk.RefColumns = append(fk.RefColumns, parent.PrimaryKey...)
	default:
		fk.RefTable = parent.Name
		for _, to := range raw.to {
			c := findColumn(parent, to.String)
			if c == nil {
				return nil, fmt.Sprintf("referenced column %q does not exist in %q", to.String, parent.Name)
			}
			fk.RefColumns = append(fk.RefColumns, c.Name)
		}
	}

	if !distinct(fk.Columns) {
		return nil, "the constraint lists a column twice"
	}
	if !distinct(fk.RefColumns) {
		return nil, "the constraint lists a referenced column twice"
	}
	return fk, ""
}

func parseAction(s string) schemadmin.FKAction {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "RESTRICT":
		return schemadmin.FKRestrict
	case "CASCADE":
		return schemadmin.FKCascade
	case "SET NULL":
		return schemadmin.FKSetNull
	case "SET DEFAULT":
		return schemadmin.FKSetDefault
	}
	return schemadmin.FKNoAction
}

// findTable looks a table up the way SQLite does: exactly, then ignoring
// ASCII case.
func findTable(schema *schemadmin.Schema, name string) *schemadmin.Table {
	if t := schema.Table("", name); t != nil {
		return t
	}
	folded := foldASCII(name)
	for _, t := range schema.Tables {
		if foldASCII(t.Name) == folded {
			return t
		}
	}
	return nil
}

// findColumn looks a column up exactly, then ignoring ASCII case.
func findColumn(t *schemadmin.Table, name string) *schemadmin.Column {
	if c := t.Column(name); c != nil {
		return c
	}
	folded := foldASCII(name)
	for _, c := range t.Columns {
		if foldASCII(c.Name) == folded {
			return c
		}
	}
	return nil
}

func distinct(names []string) bool {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			return false
		}
		seen[n] = true
	}
	return true
}
