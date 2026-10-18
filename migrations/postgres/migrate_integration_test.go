package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
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
	var version int
	if err = target.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 8 {
		t.Fatalf("migration version %d err %v", version, err)
	}
	if err = postgres.Reapply(ctx, target); err != nil {
		t.Fatalf("down/up reapply: %v", err)
	}
	var exists bool
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".operation_order_state").Scan(&exists); err != nil || !exists {
		t.Fatalf("005 operations schema missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".stripe_payment_attempts").Scan(&exists); err != nil || !exists {
		t.Fatalf("006 payments schema missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".company_product_emergency_holds").Scan(&exists); err != nil || !exists {
		t.Fatalf("007 company product holds schema missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='stripe_payment_attempts' AND column_name='company_hold_expire_requested_at')`, schema).Scan(&exists); err != nil || !exists {
		t.Fatalf("008 retained expiry marker missing after reapply: exists=%v err=%v", exists, err)
	}
	if err = postgres.Down(ctx, target); err != nil {
		t.Fatalf("final down: %v", err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".company_product_emergency_holds").Scan(&exists); err != nil || exists {
		t.Fatalf("007 hold table survived migration down: exists=%v err=%v", exists, err)
	}
	if err = target.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, schema+".protect_company_hold_expiry_request()").Scan(&exists); err != nil || exists {
		t.Fatalf("008 function survived migration down: exists=%v err=%v", exists, err)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("final up: %v", err)
	}
}
