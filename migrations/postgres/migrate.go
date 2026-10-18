// Package postgres applies the small, embedded PostgreSQL migration set.
package postgres

import (
	"context"
	"embed"
	"errors"

	"github.com/jackc/pgx/v5"
)

//go:embed *.sql
var files embed.FS

type migration struct {
	version int
	up      string
	down    string
}

var migrations = []migration{
	{version: 1, up: "001_table_sessions.up.sql", down: "001_table_sessions.down.sql"},
	{version: 2, up: "002_menu_catalog.up.sql", down: "002_menu_catalog.down.sql"},
	{version: 3, up: "003_tax_rates.up.sql", down: "003_tax_rates.down.sql"},
	{version: 4, up: "004_shared_cart_orders.up.sql", down: "004_shared_cart_orders.down.sql"},
	{version: 5, up: "005_operations.up.sql", down: "005_operations.down.sql"},
	{version: 6, up: "006_payments.up.sql", down: "006_payments.down.sql"},
	{version: 7, up: "007_company_product_holds.up.sql", down: "007_company_product_holds.down.sql"},
	{version: 8, up: "008_held_checkout_expiry.up.sql", down: "008_held_checkout_expiry.down.sql"},
}

// Up applies all migrations atomically and serializes concurrent processes.
func Up(ctx context.Context, conn *pgx.Conn) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(771904283)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	for _, item := range migrations {
		var applied bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)", item.version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		migrationSQL, readErr := files.ReadFile(item.up)
		if readErr != nil {
			return readErr
		}
		if _, err = tx.Exec(ctx, string(migrationSQL)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES ($1)", item.version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Down removes all installed schemas in one transaction. It is intended for
// controlled test/schema lifecycle use; callers select the target schema.
func Down(ctx context.Context, conn *pgx.Conn) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(771904283)"); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return tx.Commit(ctx)
	}
	for i := len(migrations) - 1; i >= 0; i-- {
		item := migrations[i]
		var applied bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)", item.version).Scan(&applied); err != nil {
			return err
		}
		if !applied {
			continue
		}
		migrationSQL, readErr := files.ReadFile(item.down)
		if readErr != nil {
			return readErr
		}
		if _, err = tx.Exec(ctx, string(migrationSQL)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", item.version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Reapply verifies down/up behavior for an otherwise empty target schema.
func Reapply(ctx context.Context, conn *pgx.Conn) error {
	if err := Down(ctx, conn); err != nil {
		return err
	}
	if err := Up(ctx, conn); err != nil {
		return err
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('table_sessions') IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("migration reapply did not create table_sessions")
	}
	return nil
}
