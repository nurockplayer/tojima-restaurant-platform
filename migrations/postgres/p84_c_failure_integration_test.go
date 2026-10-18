package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestP84CMigrationFailureAtomicRecovery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("migration failure test requires loopback PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "p84c_failure_" + hex.EncodeToString(nonce[:])
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	targetConfig := cfg.Copy()
	targetConfig.RuntimeParams["search_path"] = schema
	conn, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	// Model an installed 001–006 database using the real embedded SQL, without
	// changing the runner's migration list or adding a production failure hook.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `CREATE TABLE schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version > 6 {
			break
		}
		sql, err := files.ReadFile(migration.up)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("baseline %03d: %v", migration.version, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, migration.version); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES('p84c-existing','existing tenant before failure')`); err != nil {
		t.Fatal(err)
	}
	// 008 first adds a column and index, then collides with this function.
	// 007 must also disappear: Up commits the entire pending set atomically.
	if _, err = conn.Exec(ctx, `CREATE FUNCTION protect_company_hold_expiry_request() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
 'ledger',(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM schema_migrations m),
 'existing_data',(SELECT jsonb_agg(to_jsonb(t) ORDER BY tenant_id) FROM tenants t),
 'relations',(SELECT jsonb_agg(jsonb_build_array(c.oid,c.relname,c.relkind) ORDER BY c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()),
 'columns',(SELECT jsonb_agg(to_jsonb(c) ORDER BY table_name,ordinal_position) FROM information_schema.columns c WHERE table_schema=current_schema()),
 'functions',(SELECT jsonb_agg(pg_get_functiondef(p.oid) ORDER BY p.proname) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema())
 )::text`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	err = Up(ctx, conn)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42723" {
		t.Fatalf("expected duplicate-function failure inside 008, got %v", err)
	}
	if after := snapshot(); after != before {
		t.Fatal("failed Up changed existing ledger timestamps, relations, columns or functions")
	}
	var clean bool
	err = conn.QueryRow(ctx, `SELECT
 to_regclass('company_product_emergency_holds') IS NULL AND
 to_regclass('stripe_payment_attempt_hold_candidates_idx') IS NULL AND
 NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='stripe_payment_attempts' AND column_name='company_hold_expire_requested_at') AND
 NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='menu_admin_audit' AND column_name='request_key') AND
 (SELECT array_agg(version ORDER BY version) FROM schema_migrations)=ARRAY[1,2,3,4,5,6]`).Scan(&clean)
	if err != nil || !clean {
		t.Fatalf("007/partial 008 changes or ledger survived rollback: clean=%v err=%v", clean, err)
	}
	if _, err = conn.Exec(ctx, `DROP FUNCTION protect_company_hold_expiry_request()`); err != nil {
		t.Fatal(err)
	}
	assertLatest := func() {
		t.Helper()
		var got []int32
		if err := conn.QueryRow(ctx, `SELECT array_agg(version ORDER BY version) FROM schema_migrations`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		want := make([]int32, len(migrations))
		for i, migration := range migrations {
			want[i] = int32(migration.version)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("versions=%v want=%v", got, want)
		}
		var complete bool
		err := conn.QueryRow(ctx, `SELECT to_regclass('company_product_emergency_holds') IS NOT NULL AND to_regclass('stripe_payment_attempt_hold_candidates_idx') IS NOT NULL AND to_regclass('cancellation_requests') IS NOT NULL AND to_regprocedure('protect_company_hold_expiry_request()') IS NOT NULL`).Scan(&complete)
		if err != nil || !complete {
			t.Fatalf("recovered schema incomplete: complete=%v err=%v", complete, err)
		}
	}
	if err = Up(ctx, conn); err != nil {
		t.Fatalf("rerun after removing conflict: %v", err)
	}
	assertLatest()
	var preserved bool
	if err = conn.QueryRow(ctx, `SELECT count(*)=1 FROM tenants WHERE tenant_id='p84c-existing' AND name='existing tenant before failure'`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("existing data lost during recovery: preserved=%v err=%v", preserved, err)
	}
	if err = Up(ctx, conn); err != nil {
		t.Fatalf("idempotent Up: %v", err)
	}
	assertLatest()
	if err = Down(ctx, conn); err != nil {
		t.Fatalf("Down after recovery: %v", err)
	}
	if err = conn.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM schema_migrations) AND to_regclass('table_sessions') IS NULL AND to_regclass('cancellation_requests') IS NULL AND to_regprocedure('protect_company_hold_expiry_request()') IS NULL`).Scan(&clean); err != nil || !clean {
		t.Fatalf("Down incomplete: clean=%v err=%v", clean, err)
	}
	if err = Up(ctx, conn); err != nil {
		t.Fatalf("Up after recovery/Down: %v", err)
	}
	assertLatest()
}
