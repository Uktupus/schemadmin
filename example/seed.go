package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const schemaSQL = `
CREATE TABLE customers (
	id            INTEGER PRIMARY KEY,
	name          TEXT NOT NULL,
	email         TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	tier          TEXT NOT NULL DEFAULT 'free' CHECK (tier IN ('free', 'pro', 'enterprise')),
	is_active     BOOLEAN NOT NULL DEFAULT 1,
	notes         TEXT,
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE categories (
	id        INTEGER PRIMARY KEY,
	name      TEXT NOT NULL UNIQUE,
	parent_id INTEGER REFERENCES categories (id) ON DELETE SET NULL
);

CREATE TABLE products (
	id           INTEGER PRIMARY KEY,
	sku          VARCHAR(16) NOT NULL UNIQUE,
	name         TEXT NOT NULL,
	category_id  INTEGER REFERENCES categories (id) ON DELETE SET NULL,
	price        DECIMAL(10, 2) NOT NULL CHECK (price >= 0),
	stock        INTEGER NOT NULL DEFAULT 0,
	attributes   JSON,
	discontinued BOOLEAN NOT NULL DEFAULT 0,
	released_on  DATE
);

CREATE TABLE orders (
	id          INTEGER PRIMARY KEY,
	customer_id INTEGER NOT NULL REFERENCES customers (id) ON DELETE RESTRICT,
	status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'shipped', 'cancelled')),
	placed_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	shipped_at  DATETIME,
	note        TEXT
);
CREATE INDEX orders_customer ON orders (customer_id);

CREATE TABLE order_items (
	order_id   INTEGER NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
	product_id INTEGER NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
	quantity   INTEGER NOT NULL DEFAULT 1 CHECK (quantity > 0),
	unit_price DECIMAL(10, 2) NOT NULL,
	PRIMARY KEY (order_id, product_id)
);
CREATE INDEX order_items_product ON order_items (product_id);

CREATE TABLE api_tokens (
	id           INTEGER PRIMARY KEY,
	customer_id  INTEGER NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
	label        TEXT NOT NULL,
	token        TEXT NOT NULL,
	last_used_at DATETIME
);

CREATE TABLE settings (
	key         TEXT PRIMARY KEY NOT NULL,
	value       TEXT NOT NULL,
	description TEXT
) WITHOUT ROWID;

CREATE TABLE audit_log (
	id      INTEGER PRIMARY KEY,
	at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	actor   TEXT NOT NULL,
	action  TEXT NOT NULL,
	details JSON
);
`

var (
	firstNames = []string{"Ada", "Bruno", "Chiara", "Dmitri", "Elif", "Farid", "Greta", "Hugo", "Ines", "Jonas", "Keiko", "Lars"}
	lastNames  = []string{"Okafor", "Lindqvist", "Moreau", "Tanaka", "Haddad", "Novak", "Silva", "Brandt", "Costa", "Ivanova"}
	categories = []struct {
		name   string
		parent int // index into this slice, 1-based; 0 for none
	}{
		{"Kitchen", 0}, {"Cookware", 1}, {"Knives", 1}, {"Office", 0}, {"Desks", 4}, {"Lighting", 4}, {"Outdoor", 0},
	}
	adjectives = []string{"Cast-iron", "Oak", "Brushed steel", "Walnut", "Ceramic", "Linen", "Copper", "Matte black"}
	nouns      = []string{"skillet", "desk lamp", "chef's knife", "standing desk", "kettle", "shelf", "lantern", "stool", "tray"}
	statuses   = []string{"paid", "shipped", "shipped", "pending", "shipped", "cancelled", "paid"}
)

// seed creates the schema and fills it. It does nothing when the database
// already has tables, so a file passed with -db survives restarts.
func seed(ctx context.Context, db *sql.DB) error {
	var existing int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return err
	}
	exec := func(query string, args ...any) {
		if err == nil {
			_, err = tx.ExecContext(ctx, query, args...)
		}
	}
	start := time.Date(2025, 1, 6, 9, 0, 0, 0, time.UTC)
	stamp := func(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

	const customers = 64
	for i := range customers {
		first, last := firstNames[i%len(firstNames)], lastNames[(i*7)%len(lastNames)]
		tier := []string{"free", "free", "pro", "free", "enterprise"}[i%5]
		var notes any
		switch i % 9 {
		case 0:
			notes = "Prefers invoices by post.\nCall before delivery."
		case 4:
			notes = ""
		}
		exec(`INSERT INTO customers (name, email, password_hash, tier, is_active, notes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			first+" "+last,
			fmt.Sprintf("%s.%s%d@example.com", strings.ToLower(first), strings.ToLower(last), i+1),
			fmt.Sprintf("$2a$12$%022d", i*7919+13),
			tier, i%11 != 0, notes, stamp(start.Add(time.Duration(i)*31*time.Hour)))
	}

	for _, c := range categories {
		var parent any
		if c.parent > 0 {
			parent = c.parent
		}
		exec(`INSERT INTO categories (name, parent_id) VALUES (?, ?)`, c.name, parent)
	}

	const products = 36
	prices := make([]string, products)
	for i := range products {
		prices[i] = fmt.Sprintf("%d.%02d", 9+(i*37)%240, []int{0, 50, 90, 95, 99}[i%5])
		var attrs, released any
		if i%3 != 0 {
			attrs = fmt.Sprintf(`{"weight_kg": %.1f, "colour": %q}`, 0.4+float64(i%13)*0.7, []string{"black", "natural", "white"}[i%3])
		}
		if i%5 != 0 {
			released = start.AddDate(-2, i, i*3).Format("2006-01-02")
		}
		exec(`INSERT INTO products (sku, name, category_id, price, stock, attributes, discontinued, released_on) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("SKU-%04d", 1000+i*13),
			adjectives[i%len(adjectives)]+" "+nouns[(i*5)%len(nouns)],
			2+i%6, prices[i], (i*17)%90, attrs, i%10 == 9, released)
	}

	const orders = 230
	for i := range orders {
		placed := start.Add(time.Duration(i) * 23 * time.Hour).Add(time.Duration(i%50) * time.Minute)
		status := statuses[i%len(statuses)]
		var shipped, note any
		if status == "shipped" {
			shipped = stamp(placed.Add(time.Duration(20+i%60) * time.Hour))
		}
		if i%14 == 3 {
			note = "Gift wrap, no receipt in the parcel."
		}
		exec(`INSERT INTO orders (customer_id, status, placed_at, shipped_at, note) VALUES (?, ?, ?, ?, ?)`,
			1+(i*5)%customers, status, stamp(placed), shipped, note)
		for j := range 1 + i%3 {
			p := (i*3 + j*11) % products
			exec(`INSERT INTO order_items (order_id, product_id, quantity, unit_price) VALUES (?, ?, ?, ?)`,
				i+1, p+1, 1+(i+j)%4, prices[p])
		}
	}

	for i := range 18 {
		var used any
		if i%4 != 0 {
			used = stamp(start.Add(time.Duration(i) * 190 * time.Hour))
		}
		exec(`INSERT INTO api_tokens (customer_id, label, token, last_used_at) VALUES (?, ?, ?, ?)`,
			1+(i*3)%customers, []string{"CI", "Laptop", "Reporting"}[i%3], fmt.Sprintf("tok_%032x", i*104729+7), used)
	}

	for _, s := range [][3]string{
		{"currency", "EUR", "ISO 4217 code used for all prices."},
		{"free_shipping_from", "75.00", "Order total from which shipping is free."},
		{"support_email", "help@example.com", ""},
		{"maintenance_banner", "", "Shown on every page when not empty."},
	} {
		var desc any
		if s[2] != "" {
			desc = s[2]
		}
		exec(`INSERT INTO settings (key, value, description) VALUES (?, ?, ?)`, s[0], s[1], desc)
	}

	for i := range 40 {
		exec(`INSERT INTO audit_log (at, actor, action, details) VALUES (?, ?, ?, ?)`,
			stamp(start.Add(time.Duration(i)*53*time.Hour)),
			[]string{"ines", "jonas", "system"}[i%3],
			[]string{"order.refund", "product.update", "customer.login", "settings.change"}[i%4],
			fmt.Sprintf(`{"request_id": "req_%06d"}`, 4200+i*17))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
