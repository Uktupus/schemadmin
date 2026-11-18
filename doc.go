// Package schemadmin serves an admin panel for a SQL database.
//
// New introspects the database behind a *sql.DB and returns an
// http.Handler with a list, create, edit and delete interface for every
// table and view. Nothing is declared in Go: the schema is read from the
// database.
//
//	admin, err := schemadmin.New(ctx, schemadmin.Config{
//		DB:         db,
//		Dialect:    postgres.New(), // or sqlite.New(), mysql.New()
//		BasePath:   "/admin",
//		Authorizer: authorizer,
//	})
//	if err != nil {
//		return err
//	}
//	mux.Handle("/admin/", admin)
//
// # Authorization
//
// There is no default Authorizer: New fails without one, and the admin
// refuses whatever the Authorizer does not allow. It is consulted for every
// request and for every table a page reads from, including the tables
// behind foreign keys. Authentication is left to the host application.
//
// # Dialects
//
// A Dialect introspects a database into a Schema and supplies the few
// pieces of SQL that differ between databases, such as identifier quoting
// and parameter placeholders. Implementations live in subpackages and
// depend only on database/sql, never on a specific driver:
//
//	github.com/Uktupus/schemadmin/postgres
//	github.com/Uktupus/schemadmin/sqlite
//	github.com/Uktupus/schemadmin/mysql
//
// # Guarantees
//
// Every value is a bound parameter; identifiers come only from the
// introspected schema. Cross-site writes are refused. Database content is
// escaped. Error pages carry a reference, never SQL. An update writes only
// the columns that were changed, and NULL is never confused with the empty
// string. Views are read-only. Binary columns are edited as hexadecimal.
package schemadmin
