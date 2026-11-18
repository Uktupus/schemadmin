package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/mysql"
	"github.com/Uktupus/schemadmin/postgres"
)

func TestUpdateGoldens(t *testing.T) {
	if os.Getenv("UPDATE_GOLDEN") == "" {
		t.Skip("set UPDATE_GOLDEN=1 to rewrite golden files")
	}
	write := func(name string, s *schemadmin.Schema) {
		path := filepath.Join("testdata", name)
		if err := os.WriteFile(path, []byte(dumpSchema(s)), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wrote", path)
	}
	t.Run("sqlite", func(t *testing.T) {
		db := openSQLite(t)
		mustExec(t, db, readTestdata(t, "sqlite_schema.sql"))
		write("sqlite_schema.golden", introspectSQLite(t, db))
	})
	if dsn := os.Getenv(postgresDSNEnv); dsn != "" {
		t.Run("postgres", func(t *testing.T) {
			db := openPostgres(t, dsn, "pgx")
			mustExec(t, db, readTestdata(t, "postgres_schema.sql"))
			s := mustIntrospect(t, postgres.New(), db, schemadmin.IntrospectOptions{Schemas: []string{"app", "billing"}})
			write("postgres_schema.golden", s)
		})
	}
	if dsn := os.Getenv(mysqlDSNEnv); dsn != "" {
		t.Run("mysql", func(t *testing.T) {
			db := openMysql(t, dsn)
			mustExec(t, db, readTestdata(t, "mysql_schema.sql"))
			s := mustIntrospect(t, mysql.New(), db, schemadmin.IntrospectOptions{Schemas: []string{"app", "billing"}})
			write("mysql_schema.golden", s)
		})
	}
}
