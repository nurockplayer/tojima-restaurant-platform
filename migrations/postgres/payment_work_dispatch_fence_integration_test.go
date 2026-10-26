package postgres_test

import (
	"os"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func Test016EmptySchemaUpDownUp(t *testing.T) {
	ctx, conn := connectMigrationTestSchema(t)
	if err := postgres.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	assertColumn := func(want bool) {
		t.Helper()
		var present bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='stripe_payment_work' AND column_name='dispatched_at' AND data_type='timestamp with time zone' AND is_nullable='YES')`).Scan(&present); err != nil || present != want {
			t.Fatalf("016 nullable timestamptz column present=%v want=%v err=%v", present, want, err)
		}
	}
	assertColumn(true)
	down, err := os.ReadFile("016_payment_work_dispatch_fence.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("016 down: %v", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=16`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertColumn(false)
	if err = postgres.Up(ctx, conn); err != nil {
		t.Fatalf("016 up again: %v", err)
	}
	assertColumn(true)
}

func Test016ConservativeDispatchBackfill(t *testing.T) {
	ctx, conn := connectMigrationTestSchema(t)
	// Isolate the migration's populated-table contract from unrelated payment
	// foreign keys. Every column read or written by 016 has its real DB type.
	if _, err := conn.Exec(ctx, `CREATE TABLE stripe_payment_work (
		work_id text PRIMARY KEY,work_type text NOT NULL,attempts integer NOT NULL,
		updated_at timestamptz NOT NULL);
		INSERT INTO stripe_payment_work VALUES
		('fresh-create','CREATE',0,'2026-10-26T00:00:00Z'),
		('claimed-create','CREATE',1,'2026-10-26T01:00:00Z'),
		('reclaimed-create','CREATE',2,'2026-10-26T02:00:00Z'),
		('other-work','EXPIRE',2,'2026-10-26T03:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("016_payment_work_dispatch_fence.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, string(up)); err != nil {
		t.Fatalf("016 populated up: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var correct bool
	if err = conn.QueryRow(ctx, `SELECT count(*)=4 AND bool_and(CASE
		WHEN work_type='CREATE' AND attempts>0 THEN dispatched_at IS NOT NULL AND dispatched_at=updated_at
		ELSE dispatched_at IS NULL END) FROM stripe_payment_work`).Scan(&correct); err != nil || !correct {
		t.Fatalf("016 must fence exactly the previously claimed CREATE rows: correct=%v err=%v", correct, err)
	}
}
