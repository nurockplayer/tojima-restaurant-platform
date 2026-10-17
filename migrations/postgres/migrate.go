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

const migrationVersion = 1

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
	var applied bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)", migrationVersion).Scan(&applied); err != nil {
		return err
	}
	if !applied {
		migration, readErr := files.ReadFile("001_table_sessions.up.sql")
		if readErr != nil {
			return readErr
		}
		if _, err = tx.Exec(ctx, string(migration)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES ($1)", migrationVersion); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Down removes the P-76 schema in one transaction. It is intended for controlled
// test/schema lifecycle use; callers must select the target schema themselves.
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
	var applied bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)", migrationVersion).Scan(&applied); err != nil {
		return err
	}
	if applied {
		migration, readErr := files.ReadFile("001_table_sessions.down.sql")
		if readErr != nil {
			return readErr
		}
		if _, err = tx.Exec(ctx, string(migration)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", migrationVersion); err != nil {
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
