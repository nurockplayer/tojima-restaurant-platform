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
	down018, err := os.ReadFile("018_staff_auth_logout_fence.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx018, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx018.Exec(ctx, string(down018)); err != nil {
		t.Fatalf("018 down before 017 down: %v", err)
	}
	if _, err = tx018.Exec(ctx, `DELETE FROM schema_migrations WHERE version=18`); err != nil {
		t.Fatal(err)
	}
	if err = tx018.Commit(ctx); err != nil {
		t.Fatal(err)
	}
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

func Test018StaffAuthLogoutFenceUpDownUp(t *testing.T) {
	ctx, conn := connectMigrationTestSchema(t)
	if err := postgres.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	assertFence := func(want bool) {
		t.Helper()
		var present bool
		if err := conn.QueryRow(ctx, `SELECT
			(SELECT count(*) = 4 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN ('staff_auth_tx','staff_sessions') AND column_name IN ('cancelled_at','completed_at','auth_state_sha256','previous_token_sha256'))
			AND EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname='staff_sessions_auth_state_idx')
			AND EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname='staff_sessions_previous_token_idx')`).Scan(&present); err != nil || present != want {
			t.Fatalf("018 schema present=%v want=%v err=%v", present, want, err)
		}
	}
	assertFence(true)
	down, err := os.ReadFile("018_staff_auth_logout_fence.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("018 down: %v", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=18`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertFence(false)
	if err = postgres.Up(ctx, conn); err != nil {
		t.Fatalf("018 up again: %v", err)
	}
	assertFence(true)
}
