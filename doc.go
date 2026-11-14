// Package schemadmin models a relational database schema and defines the
// Dialect contract used to read that schema from a live database.
//
// A Dialect introspects a *sql.DB into a Schema: tables, columns, primary
// keys (including composite ones), foreign keys and enumerated values. The
// same Dialect supplies the few pieces of SQL that differ between databases,
// such as identifier quoting and parameter placeholders.
//
// Dialect implementations live in subpackages and depend only on
// database/sql, never on a specific driver:
//
//	github.com/Uktupus/schemadmin/sqlite
package schemadmin
