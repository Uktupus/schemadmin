package schemadmin

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"
)

// Config configures an Admin. DB, Dialect and Authorizer are required.
type Config struct {
	// DB is the database to administer.
	DB *sql.DB
	// Dialect matches the database behind DB, e.g. sqlite.New().
	Dialect Dialect
	// Authorizer decides what each request may do. There is no default:
	// New fails without one.
	Authorizer Authorizer

	// BasePath is where the admin is mounted, e.g. "/admin". Mount the
	// Admin with mux.Handle("/admin/", admin).
	BasePath string
	// Title is shown in the page header. Default "Admin".
	Title string
	// Logger receives warnings, errors and one record per write.
	// Default slog.Default().
	Logger *slog.Logger

	// Schemas lists the database schemas to administer. Empty means the
	// dialect's default. With more than one, tables are addressed as
	// "schema.table" in URLs and in Tables.
	Schemas []string
	// Tables holds per-table overrides, keyed by table name (or
	// "schema.table"). Unknown tables and columns are an error.
	Tables map[string]TableConfig

	// PageSize is the number of rows per list page. Default 50.
	PageSize int
	// QueryTimeout bounds every query. Default 10s.
	QueryTimeout time.Duration
	// CountTimeout bounds the row count of a list page. When it expires
	// the list is shown without a total. Default 2s.
	CountTimeout time.Duration
	// TrustedOrigins are origins ("https://admin.example.com") allowed to
	// send state-changing requests in addition to the admin's own origin.
	TrustedOrigins []string

	// AllowReload exposes a "Reload schema" control that re-runs
	// introspection without restarting the process.
	AllowReload bool
	// BulkLimit is the maximum number of rows a bulk delete may take.
	// Default 100, maximum 500.
	BulkLimit int
	// ExportMax is the maximum number of rows a CSV export returns.
	// Default 10000, maximum 100000.
	ExportMax int
}

// TableConfig overrides how one table is presented.
type TableConfig struct {
	// Label replaces the table name in the UI.
	Label string
	// Hidden removes the table from the admin entirely.
	Hidden bool
	// ReadOnly disables create, update and delete.
	ReadOnly bool
	// ListColumns selects and orders the columns of the list view.
	ListColumns []string
	// SearchColumns selects the columns the search box looks in.
	SearchColumns []string
	// FilterColumns selects the columns whose header offers a filter.
	// Nil means every column that can be read.
	FilterColumns []string
	// DefaultSort orders the list when the user has not chosen a column.
	DefaultSort Sort
	// PageSize overrides Config.PageSize.
	PageSize int
	// DisplayColumn names the column that labels a row where other tables
	// refer to it.
	DisplayColumn string
	// Columns holds per-column overrides.
	Columns map[string]ColumnConfig
}

// Sort names a column and a direction.
type Sort struct {
	Column string
	Desc   bool
}

// ColumnConfig overrides how one column is presented.
type ColumnConfig struct {
	// Label replaces the column name in the UI.
	Label string
	// Help is shown under the form field.
	Help string
	// Hidden removes the column: it is never selected and never shown.
	Hidden bool
	// ReadOnly shows the column but keeps it out of forms.
	ReadOnly bool
	// Widget chooses the form control for text columns.
	Widget Widget
	// Choices restricts the column to these values, as if it were an enum.
	Choices []string
	// Mask controls whether the column's values are kept out of the UI.
	Mask Mask
}

// Widget is a form control for a text column.
type Widget int

const (
	// WidgetAuto picks by column type.
	WidgetAuto Widget = iota
	WidgetInput
	WidgetTextarea
)

// Mask controls masking. A masked column is never selected: lists and forms
// show only whether it is set, and a form writes it only when a new value
// is entered.
type Mask int

const (
	// MaskAuto masks columns that are not keys and whose name contains
	// password, passwd, secret, token, api_key, private_key, salt or hash.
	MaskAuto Mask = iota
	MaskAlways
	MaskNever
)

// Action is something a request wants to do.
type Action int

const (
	ActionView Action = iota
	ActionCreate
	ActionUpdate
	ActionDelete
)

func (a Action) String() string {
	switch a {
	case ActionView:
		return "view"
	case ActionCreate:
		return "create"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
	}
	return "unknown"
}

// Authorizer decides whether a request may perform an action. The admin
// calls it for every request and for every table a page reads from, and
// treats anything but true as a refusal.
//
// Authentication is the host application's job: put it in front of the
// admin, and have the Authorizer read the result from the request.
type Authorizer interface {
	// Authorize reports whether r may perform action on table. table is
	// "" when the request targets the admin itself (the index, assets).
	Authorize(r *http.Request, action Action, table string) bool
}

// AuthorizerFunc adapts a function to the Authorizer interface.
type AuthorizerFunc func(r *http.Request, action Action, table string) bool

// Authorize calls f.
func (f AuthorizerFunc) Authorize(r *http.Request, action Action, table string) bool {
	return f(r, action, table)
}

// ForeignKeyChecker is implemented by dialects for databases in which
// foreign-key enforcement can be switched off. The delete page uses it to
// say truthfully what will happen to dependent rows.
type ForeignKeyChecker interface {
	ForeignKeysEnforced(ctx context.Context, db *sql.DB) (bool, error)
}
