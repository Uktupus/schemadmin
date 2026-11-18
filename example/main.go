// Command example runs schemadmin against a small seeded SQLite database.
//
//	cd example && go run .
//
// Then open http://localhost:8080/admin/. The database lives in a temporary
// directory and is thrown away on exit; pass -db to keep it in a file.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Uktupus/schemadmin"
	"github.com/Uktupus/schemadmin/sqlite"

	_ "modernc.org/sqlite"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	path := flag.String("db", "", "database file (default: a temporary one, seeded and removed on exit)")
	flag.Parse()

	if err := run(*addr, *path); err != nil {
		fmt.Fprintln(os.Stderr, "example:", err)
		os.Exit(1)
	}
}

func run(addr, path string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if path == "" {
		dir, err := os.MkdirTemp("", "schemadmin-example-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		path = filepath.Join(dir, "shop.db")
	}
	// Foreign keys are off in SQLite unless each connection turns them on.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	defer db.Close()
	if err := seed(ctx, db); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	admin, err := schemadmin.New(ctx, schemadmin.Config{
		DB:       db,
		Dialect:  sqlite.New(),
		BasePath: "/admin",
		Title:    "Shop admin",

		// schemadmin refuses to start without an Authorizer. This one lets
		// in whoever connects from this machine, which is right for a demo
		// bound to 127.0.0.1 and wrong for anything else: a real
		// application checks its own session here.
		AllowReload: true,

		Authorizer: schemadmin.AuthorizerFunc(func(r *http.Request, action schemadmin.Action, table string) bool {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				return false
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				return false
			}
			// The audit log can be read but not changed, by anyone.
			if table == "audit_log" && action != schemadmin.ActionView {
				return false
			}
			return true
		}),

		Tables: map[string]schemadmin.TableConfig{
			"customers": {
				Columns: map[string]schemadmin.ColumnConfig{
					"email": {Help: "Must be unique."},
					"notes": {Widget: schemadmin.WidgetTextarea},
				},
			},
			"orders": {
				DefaultSort:   schemadmin.Sort{Column: "placed_at", Desc: true},
				FilterColumns: []string{"status", "placed_at"},
			},
			"products": {
				ListColumns: []string{"id", "sku", "name", "category_id", "price", "stock", "discontinued"},
			},
			"audit_log": {
				Label:       "Audit log",
				DefaultSort: schemadmin.Sort{Column: "id", Desc: true},
			},
		},
	})
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/admin/", admin)
	mux.Handle("/{$}", http.RedirectHandler("/admin/", http.StatusFound))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("schemadmin example listening", "url", "http://"+addr+"/admin/")
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
