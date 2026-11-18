package schemadmin

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// tableMeta is a table as the admin presents it: the introspected table
// with the configuration applied. It is built once, in New.
type tableMeta struct {
	t        *Table
	key      string // name in URLs, in Config.Tables and for the Authorizer
	seg      string // key as a path segment
	label    string
	readOnly bool
	pageSize int

	cols   []*colMeta // every column that is not hidden, in table order
	byName map[string]*colMeta
	pk     []*colMeta // nil when rows cannot be addressed

	listCols   []*colMeta
	searchCols []*colMeta
	display    *colMeta
	sort       *colMeta
	sortDesc   bool

	refs []*reference // foreign keys in other tables that point here
}

// colMeta is a column as the admin presents it.
type colMeta struct {
	c        *Column
	idx      int // position in tableMeta.cols; names the form fields
	label    string
	help     string
	masked   bool
	readOnly bool
	isKey    bool
	filter   bool // offered as a filter in the column's header menu
	widget   Widget
	choices  []string
	fk       *fkMeta // set when this column carries a foreign-key control
	fkFollow bool    // set when another column's composite FK covers this one
}

// fkMeta is a foreign key to another administered table. A single-column
// key names col; a composite key names cols and refCols together.
type fkMeta struct {
	target  *tableMeta
	col     *colMeta   // referenced column of a single-column key
	cols    []*colMeta // local columns of a composite key
	refCols []*colMeta // referenced columns of a composite key
}

// reference is a foreign key seen from the table it points to.
type reference struct {
	from     *tableMeta
	fk       *ForeignKey
	fromCols []*Column  // referencing columns
	toCols   []*colMeta // referenced columns in this table; nil entries are not readable
}

// searchKey is the key column a search also matches exactly, if any: a
// single integer or UUID primary key.
func (tm *tableMeta) searchKey() *colMeta {
	if len(tm.pk) == 1 && (tm.pk[0].c.Kind == KindInt || tm.pk[0].c.Kind == KindUUID) {
		return tm.pk[0]
	}
	return nil
}

func (tm *tableMeta) searchable() bool {
	return len(tm.searchCols) > 0 || tm.searchKey() != nil
}

// writable reports whether forms may set the column at all.
func (c *colMeta) writable() bool {
	return !c.c.Generated && !c.readOnly
}

// comparable reports whether the column can be sorted and filtered on.
func (c *colMeta) comparable() bool {
	if c.masked {
		return false
	}
	switch c.c.Kind {
	case KindJSON, KindBytes, KindOther:
		return false
	}
	return true
}

// readable reports whether the column's values are read at all.
func (c *colMeta) readable() bool { return !c.masked }

// ranged reports whether "from" and "to" bounds make sense for the column.
func (c *colMeta) ranged() bool {
	if c.masked || c.fk != nil || len(c.choices) > 0 {
		return false
	}
	switch c.c.Kind {
	case KindInt, KindFloat, KindDecimal, KindDate, KindTime, KindDateTime, KindDateTimeTZ:
		return true
	}
	return false
}

// matchable reports whether the column can be filtered by "contains".
func (c *colMeta) matchable() bool {
	if !c.readable() || len(c.choices) > 0 || c.fk != nil {
		return false
	}
	switch c.c.Kind {
	case KindString, KindText, KindJSON, KindOther:
		return true
	}
	return false
}

// textual reports whether an empty form field means the empty string. For
// every other column an empty field means "no value".
func (c *colMeta) textual() bool {
	switch c.c.Kind {
	case KindString, KindText, KindOther:
		return len(c.choices) == 0 && c.fk == nil
	}
	return false
}

var maskedWords = []string{"password", "passwd", "secret", "token", "api_key", "private_key", "salt", "hash"}

// sensitiveName reports whether a column name suggests a credential.
func sensitiveName(name string) bool {
	lower := strings.ToLower(name)
	squashed := strings.NewReplacer("_", "", "-", "", " ", "").Replace(lower)
	for _, w := range maskedWords {
		if strings.Contains(lower, w) || strings.Contains(squashed, strings.ReplaceAll(w, "_", "")) {
			return true
		}
	}
	return false
}

// humanize turns an identifier into a label: "created_at" -> "Created at".
func humanize(name string) string {
	s := strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	if s == "" {
		return name
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var displayNames = []string{"name", "title", "label", "display_name", "full_name", "username", "email", "slug", "code"}

// buildMeta applies cfg to schema.
func buildMeta(schema *Schema, cfg *Config, logger *slog.Logger) ([]*tableMeta, map[string]*tableMeta, error) {
	qualify := len(cfg.Schemas) > 1
	keyOf := func(schemaName, table string) string {
		if qualify && schemaName != "" {
			return schemaName + "." + table
		}
		return table
	}

	var tables []*tableMeta
	byKey := map[string]*tableMeta{}
	byTable := map[*Table]*tableMeta{}
	seen := map[string]bool{}
	for _, t := range schema.Tables {
		key := keyOf(t.Schema, t.Name)
		if seen[key] {
			return nil, nil, fmt.Errorf("schemadmin: two tables are both addressed as %q", key)
		}
		seen[key] = true
		tc := cfg.Tables[key]
		if tc.Hidden {
			continue
		}
		if len(t.Columns) == 0 {
			logger.Warn("schemadmin: table has no columns and is left out", slog.String("table", key))
			continue
		}
		tm, err := buildTable(t, key, tc, cfg)
		if err != nil {
			return nil, nil, err
		}
		tables = append(tables, tm)
		byKey[key] = tm
		byTable[t] = tm
	}
	var unknown []string
	for key := range cfg.Tables {
		if !seen[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, nil, fmt.Errorf("schemadmin: Config.Tables names unknown table %q", unknown[0])
	}

	// Foreign keys, once every table exists.
	for _, tm := range tables {
		for _, fk := range tm.t.ForeignKeys {
			target := byTable[schema.Table(fk.RefSchema, fk.RefTable)]
			if target == nil {
				continue
			}
			ref := &reference{from: tm, fk: fk}
			for i, name := range fk.Columns {
				ref.fromCols = append(ref.fromCols, tm.t.Column(name))
				ref.toCols = append(ref.toCols, target.byName[fk.RefColumns[i]])
			}
			target.refs = append(target.refs, ref)
			for _, name := range fk.Columns {
				if c := tm.byName[name]; c != nil {
					c.isKey = true
				}
			}
			switch len(fk.Columns) {
			case 1:
				from, to := tm.byName[fk.Columns[0]], target.byName[fk.RefColumns[0]]
				if from != nil && to != nil && from.fk == nil {
					from.fk = &fkMeta{target: target, col: to}
				}
			default:
				var locals, remotes []*colMeta
				ok := true
				for i, name := range fk.Columns {
					from, to := tm.byName[name], target.byName[fk.RefColumns[i]]
					if from == nil || to == nil || from.fk != nil {
						ok = false
						break
					}
					locals, remotes = append(locals, from), append(remotes, to)
				}
				if !ok {
					continue
				}
				// The first column carries the control; the rest are
				// filled from the same submission.
				locals[0].fk = &fkMeta{target: target, cols: locals, refCols: remotes}
				for _, local := range locals[1:] {
					local.fkFollow = true
				}
			}
		}
	}

	// Masking depends on which columns are keys, so it comes last, and the
	// choices that depend on masking after it.
	for _, tm := range tables {
		tc := cfg.Tables[tm.key]
		for _, c := range tm.cols {
			switch tc.Columns[c.c.Name].Mask {
			case MaskAlways:
				c.masked = true
			case MaskAuto:
				c.masked = !c.isKey && sensitiveName(c.c.Name)
			}
			if c.masked {
				c.fk = nil
				c.choices = nil
			}
		}
		if err := tm.finish(tc); err != nil {
			return nil, nil, err
		}
	}
	for _, tm := range tables {
		for _, c := range tm.cols {
			if c.fk == nil {
				continue
			}
			if c.fk.col != nil && (c.fk.col.masked || c.fk.col.c.Kind == KindBytes) {
				c.fk = nil
				continue
			}
			for _, r := range c.fk.refCols {
				if r.masked || r.c.Kind == KindBytes {
					c.fk = nil
					break
				}
			}
		}
	}
	return tables, byKey, nil
}

func buildTable(t *Table, key string, tc TableConfig, cfg *Config) (*tableMeta, error) {
	tm := &tableMeta{
		t:        t,
		key:      key,
		seg:      escapeKeyPart(key),
		label:    tc.Label,
		readOnly: tc.ReadOnly || t.View,
		pageSize: tc.PageSize,
		byName:   map[string]*colMeta{},
	}
	if tm.label == "" {
		tm.label = humanize(t.Name)
		if t.View {
			tm.label += " (view)"
		}
	}
	if tm.pageSize <= 0 {
		tm.pageSize = cfg.PageSize
	}
	for name := range tc.Columns {
		if t.Column(name) == nil {
			return nil, fmt.Errorf("schemadmin: Config.Tables[%q] names unknown column %q", key, name)
		}
	}
	isPK := map[string]bool{}
	for _, name := range t.PrimaryKey {
		isPK[name] = true
	}
	for _, c := range t.Columns {
		cc := tc.Columns[c.Name]
		if cc.Hidden {
			continue
		}
		cm := &colMeta{
			c:        c,
			idx:      len(tm.cols),
			label:    cc.Label,
			help:     cc.Help,
			readOnly: cc.ReadOnly,
			isKey:    isPK[c.Name],
			widget:   cc.Widget,
			choices:  cc.Choices,
		}
		if cm.label == "" {
			cm.label = humanize(c.Name)
		}
		if len(cm.choices) == 0 && c.Kind == KindEnum {
			cm.choices = c.EnumValues
		}
		tm.cols = append(tm.cols, cm)
		tm.byName[c.Name] = cm
	}
	if len(tm.cols) == 0 {
		return nil, fmt.Errorf("schemadmin: Config.Tables[%q] hides every column", key)
	}
	return tm, nil
}

// finish resolves everything that depends on masking.
func (tm *tableMeta) finish(tc TableConfig) error {
	pick := func(field string, names []string) ([]*colMeta, error) {
		var out []*colMeta
		for _, name := range names {
			c := tm.byName[name]
			if c == nil {
				return nil, fmt.Errorf("schemadmin: Config.Tables[%q].%s names unknown or hidden column %q", tm.key, field, name)
			}
			out = append(out, c)
		}
		return out, nil
	}

	// Rows are addressable when the whole primary key can be read.
	for _, name := range tm.t.PrimaryKey {
		c := tm.byName[name]
		if c == nil || c.masked || c.c.Kind == KindBytes {
			tm.pk = nil
			break
		}
		tm.pk = append(tm.pk, c)
	}

	var err error
	if tm.listCols, err = pick("ListColumns", tc.ListColumns); err != nil {
		return err
	}
	if len(tm.listCols) == 0 {
		tm.listCols = tm.cols
	}

	if tc.SearchColumns != nil {
		if tm.searchCols, err = pick("SearchColumns", tc.SearchColumns); err != nil {
			return err
		}
	} else {
		for _, c := range tm.cols {
			if c.c.Kind == KindString || c.c.Kind == KindText {
				tm.searchCols = append(tm.searchCols, c)
			}
		}
	}
	// Every readable column can be filtered from its header unless the
	// configuration narrows that down.
	if tc.FilterColumns != nil {
		cols, err := pick("FilterColumns", tc.FilterColumns)
		if err != nil {
			return err
		}
		for _, c := range cols {
			if !c.readable() {
				return fmt.Errorf("schemadmin: Config.Tables[%q].FilterColumns: column %q is masked or binary", tm.key, c.c.Name)
			}
			c.filter = true
		}
	} else {
		for _, c := range tm.cols {
			c.filter = c.readable()
		}
	}
	// Asking to search a column that cannot be read is a mistake worth
	// reporting; one picked by the defaults is simply skipped.
	for _, c := range tm.searchCols {
		if tc.SearchColumns != nil && !c.readable() {
			return fmt.Errorf("schemadmin: Config.Tables[%q].SearchColumns: column %q is masked or binary", tm.key, c.c.Name)
		}
	}
	tm.searchCols = keep(tm.searchCols, func(c *colMeta) bool { return c.readable() })

	if tc.DisplayColumn != "" {
		c := tm.byName[tc.DisplayColumn]
		if c == nil || c.masked || c.c.Kind == KindBytes {
			return fmt.Errorf("schemadmin: Config.Tables[%q].DisplayColumn %q is unknown, hidden, masked or binary", tm.key, tc.DisplayColumn)
		}
		tm.display = c
	} else {
		tm.display = guessDisplay(tm)
	}

	if tc.DefaultSort.Column != "" {
		c := tm.byName[tc.DefaultSort.Column]
		if c == nil || !c.comparable() {
			return fmt.Errorf("schemadmin: Config.Tables[%q].DefaultSort: column %q cannot be sorted on", tm.key, tc.DefaultSort.Column)
		}
		tm.sort, tm.sortDesc = c, tc.DefaultSort.Desc
	}
	return nil
}

func guessDisplay(tm *tableMeta) *colMeta {
	usable := func(c *colMeta) bool {
		return !c.masked && (c.c.Kind == KindString || c.c.Kind == KindText)
	}
	for _, name := range displayNames {
		for _, c := range tm.cols {
			if usable(c) && strings.EqualFold(c.c.Name, name) {
				return c
			}
		}
	}
	for _, c := range tm.cols {
		if usable(c) && c.c.Kind == KindString && !c.isKey {
			return c
		}
	}
	return nil
}

func keep(cols []*colMeta, ok func(*colMeta) bool) []*colMeta {
	var out []*colMeta
	for _, c := range cols {
		if ok(c) {
			out = append(out, c)
		}
	}
	return out
}
