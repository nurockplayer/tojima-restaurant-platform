package postgres_test

import (
	"os"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func Test017EmptySchemaUpDownUp(t *testing.T) {
	ctx, conn := connectMigrationTestSchema(t)
	if err := postgres.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	assertPresent := func(want bool) {
		t.Helper()
		var present bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('staff_sessions') IS NOT NULL`).Scan(&present); err != nil || present != want {
			t.Fatalf("staff_sessions present=%v want=%v err=%v", present, want, err)
		}
	}
	assertPresent(true)
	down, err := os.ReadFile("017_staff_auth.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("017 down: %v", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=17`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertPresent(false)
	if err = postgres.Up(ctx, conn); err != nil {
		t.Fatalf("017 up again: %v", err)
	}
	assertPresent(true)
}
