package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func TestPostgresMigrationsUpDownReapply(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("migration test requires loopback PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	var raw [8]byte
	if _, err = rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	schema := "p79_migrate_" + hex.EncodeToString(raw[:])
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	targetConfig := cfg.Copy()
	targetConfig.RuntimeParams["search_path"] = schema
	target, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("idempotent up: %v", err)
	}
	var exists bool
	if err = target.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, schema+`.protect_table_session_pending_stop_requests()`).Scan(&exists); err != nil || !exists {
		t.Fatalf("012 pending-session-close guard function missing after up: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=$1::regclass AND tgname='table_sessions_pending_stop_requests' AND NOT tgisinternal)`, schema+`.table_sessions`).Scan(&exists); err != nil || !exists {
		t.Fatalf("012 pending-session-close guard trigger missing after up: exists=%v err=%v", exists, err)
	}
	var version int
	if err = target.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 17 {
		t.Fatalf("migration version %d err %v", version, err)
	}
	var voidConstraint string
	if err = target.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='operation_order_state'::regclass AND conname='operation_order_state_check'`).Scan(&voidConstraint); err != nil || !strings.Contains(voidConstraint, "VOIDED") {
		t.Fatalf("014 payment status constraint missing VOIDED: %s err=%v", voidConstraint, err)
	}
	down014, err := os.ReadFile("014_stripe_unpaid_void.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx014, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx014.Exec(ctx, string(down014)); err != nil {
		t.Fatalf("014 down: %v", err)
	}
	if _, err = tx014.Exec(ctx, "DELETE FROM schema_migrations WHERE version=14"); err != nil {
		t.Fatal(err)
	}
	if err = tx014.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = target.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='operation_order_state'::regclass AND conname='operation_order_state_check'`).Scan(&voidConstraint); err != nil || strings.Contains(voidConstraint, "VOIDED") {
		t.Fatalf("014 down did not restore payment status constraint: %s err=%v", voidConstraint, err)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("014 reapply: %v", err)
	}
	rollback014 := func() {
		t.Helper()
		tx, e := target.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if _, e = tx.Exec(ctx, string(down014)); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=14"); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	rollback014()
	// 013 must be reversible independently, and Up must upgrade an installed
	// 012 ledger rather than skipping an amended historical migration.
	down013, err := os.ReadFile("013_stop_request_state_lock.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	rollback013 := func() {
		t.Helper()
		tx, err := target.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, string(down013)); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=13"); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var lockedGuard string
	if err = target.QueryRow(ctx, `SELECT pg_get_functiondef('protect_cancellation_request()'::regprocedure)`).Scan(&lockedGuard); err != nil || !strings.Contains(lockedGuard, "FOR SHARE") {
		t.Fatalf("013 insert guard missing lock: %v", err)
	}
	rollback013()
	var originalGuard string
	if err = target.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid='protect_cancellation_request()'::regprocedure`).Scan(&originalGuard); err != nil {
		t.Fatal(err)
	}
	up012, err := os.ReadFile("012_stop_requests.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	wantGuard := strings.Split(strings.Split(string(up012), "CREATE FUNCTION protect_cancellation_request() RETURNS trigger LANGUAGE plpgsql AS $$")[1], "$$;")[0]
	if originalGuard != wantGuard {
		t.Fatal("013 down did not restore exactly the 012 request guard")
	}
	var ledgerBefore string
	if err = target.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(m) ORDER BY version)::text FROM schema_migrations m`).Scan(&ledgerBefore); err != nil {
		t.Fatal(err)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("012 to 013 upgrade: %v", err)
	}
	var upgradedGuard, ledgerAfter string
	if err = target.QueryRow(ctx, `SELECT pg_get_functiondef('protect_cancellation_request()'::regprocedure)`).Scan(&upgradedGuard); err != nil || upgradedGuard != lockedGuard {
		t.Fatalf("012 upgrade differs from fresh 013 guard: %v", err)
	}
	if err = target.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(m) ORDER BY version)::text FROM schema_migrations m WHERE version<=12 OR version>=15`).Scan(&ledgerAfter); err != nil || ledgerAfter != ledgerBefore {
		t.Fatalf("013 upgrade changed the 012 ledger: %v", err)
	}
	if err = target.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 17 {
		t.Fatalf("upgraded migration version %d err %v", version, err)
	}
	rollback014()
	rollback013()
	// Paired C2 rollback restores exactly the C1 transition guard before C1 down.
	down012, err := os.ReadFile("012_stop_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx012, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx012.Exec(ctx, string(down012)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx012.Exec(ctx, "DELETE FROM schema_migrations WHERE version=12"); err != nil {
		t.Fatal(err)
	}
	if err = tx012.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, schema+`.protect_table_session_pending_stop_requests()`).Scan(&exists); err != nil || exists {
		t.Fatalf("012 down retained pending-session-close guard function: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=$1::regclass AND tgname='table_sessions_pending_stop_requests' AND NOT tgisinternal)`, schema+`.table_sessions`).Scan(&exists); err != nil || exists {
		t.Fatalf("012 down retained pending-session-close guard trigger: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=$1::regclass AND tgname='table_session_snapshot_immutable' AND NOT tgisinternal)`, schema+`.table_sessions`).Scan(&exists); err != nil || !exists {
		t.Fatalf("012 down removed original snapshot trigger: exists=%v err=%v", exists, err)
	}
	var guard012 string
	if err = target.QueryRow(ctx, `SELECT pg_get_functiondef('validate_operation_order_transition()'::regprocedure)`).Scan(&guard012); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(guard012, "cancellation_requests") || !strings.Contains(guard012, "order_adjustments") {
		t.Fatal("012 down failed to restore C1 guard")
	}
	// Exercise 011 rollback before 010 and verify it restores lane B guards.
	down011, err := os.ReadFile("011_item_adjustments_refunds.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx011, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx011.Exec(ctx, string(down011)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx011.Exec(ctx, "DELETE FROM schema_migrations WHERE version=11"); err != nil {
		t.Fatal(err)
	}
	if err = tx011.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var guard011 string
	if err = target.QueryRow(ctx, `SELECT pg_get_functiondef('validate_operation_order_transition()'::regprocedure)`).Scan(&guard011); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(guard011, "order_adjustments") || !strings.Contains(guard011, "MANAGER_REVIEW_APPROVED") {
		t.Fatalf("011 down failed to restore B guard: %s", guard011)
	}
	// Exercise 010 alone: full Down would immediately drop the restored
	// functions and could conceal a broken rollback body.
	down010, err := os.ReadFile("010_manager_review.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(down010)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=10"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var guard string
	if err = target.QueryRow(ctx, `SELECT pg_get_functiondef('validate_operation_order_transition()'::regprocedure)`).Scan(&guard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(guard, "review_decision") || strings.Contains(guard, "MANAGER_REVIEW_APPROVED") || !strings.Contains(guard, "IN ('PENDING','UNKNOWN')") {
		t.Fatalf("010 down did not restore operation guard: %s", guard)
	}
	if err = target.QueryRow(ctx, `SELECT pg_get_functiondef('protect_stripe_attempt_snapshot()'::regprocedure)`).Scan(&guard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(guard, "manager-review:") || !strings.Contains(guard, "payment review requires governed resolution") {
		t.Fatalf("010 down did not restore attempt guard: %s", guard)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("010 reapply: %v", err)
	}
	if err = postgres.Reapply(ctx, target); err != nil {
		t.Fatalf("down/up reapply: %v", err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".operation_order_state").Scan(&exists); err != nil || !exists {
		t.Fatalf("005 operations schema missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".stripe_payment_attempts").Scan(&exists); err != nil || !exists {
		t.Fatalf("006 payments schema missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".company_product_emergency_holds").Scan(&exists); err != nil || !exists {
		t.Fatalf("007 company product holds schema missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".order_read_grants").Scan(&exists); err != nil || !exists {
		t.Fatalf("009 read grants missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='stripe_payment_attempts' AND column_name='company_hold_expire_requested_at')`, schema).Scan(&exists); err != nil || !exists {
		t.Fatalf("008 retained expiry marker missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='operation_order_state' AND column_name='review_decision')`, schema).Scan(&exists); err != nil || !exists {
		t.Fatalf("010 manager review state missing after reapply: exists=%v err=%v", exists, err)
	}
	for _, name := range []string{"order_adjustments", "operation_refunds", "cash_refund_ledger", "stripe_refund_webhook_events", "operation_refund_attempt_history"} {
		if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+"."+name).Scan(&exists); err != nil || !exists {
			t.Fatalf("011 table %s missing: %v", name, err)
		}
	}
	if err = postgres.Down(ctx, target); err != nil {
		t.Fatalf("final down: %v", err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".company_product_emergency_holds").Scan(&exists); err != nil || exists {
		t.Fatalf("007 hold table survived migration down: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".order_read_grants").Scan(&exists); err != nil || exists {
		t.Fatalf("009 read grants survived down: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, schema+".protect_company_hold_expiry_request()").Scan(&exists); err != nil || exists {
		t.Fatalf("008 function survived migration down: exists=%v err=%v", exists, err)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("final up: %v", err)
	}
}

func Test015EmptySchemaUpDownUp(t *testing.T) {
	ctx, conn := connectMigrationTestSchema(t)
	if err := postgres.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	assertColumn := func(want bool) {
		t.Helper()
		var present bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='stores' AND column_name='guest_display_name')`).Scan(&present); err != nil || present != want {
			t.Fatalf("015 column present=%v want=%v err=%v", present, want, err)
		}
	}
	assertColumn(true)
	down, err := os.ReadFile("015_store_guest_display_name.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("015 down: %v", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=15`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertColumn(false)
	if err = postgres.Up(ctx, conn); err != nil {
		t.Fatalf("015 up again: %v", err)
	}
	assertColumn(true)
}
