package postgres_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func connectMigrationTestSchema(t *testing.T) (context.Context, *pgx.Conn) {
	t.Helper()
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
	t.Cleanup(cancel)
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var raw [8]byte
	if _, err = rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	schema := "p90_migrate_" + hex.EncodeToString(raw[:])
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	})
	targetConfig := cfg.Copy()
	targetConfig.RuntimeParams["search_path"] = schema
	target, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close(context.Background()) })
	return ctx, target
}

func Test015StoreGuestDisplayNameConstraintAndPopulatedUp(t *testing.T) {
	ctx, target := connectMigrationTestSchema(t)
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Model rows that predate 015: remove only its schema and ledger entry,
	// populate the pre-existing table, then apply the retained registry again.
	if _, err := target.Exec(ctx, `ALTER TABLE stores DROP COLUMN guest_display_name; DELETE FROM schema_migrations WHERE version=15`); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('tenant-a','internal-a'),('tenant-b','internal-b')`); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name,enabled,operating,qr_entry_mode) VALUES
		('tenant-a','s1','internal-a-s1',false,true,'SESSION_PIN'),
		('tenant-b','s1','internal-b-s1',true,false,'DIRECT_SCAN')`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatalf("015 apply over populated stores: %v", err)
	}
	var nullCount int
	if err := target.QueryRow(ctx, `SELECT count(*) FROM stores WHERE guest_display_name IS NULL`).Scan(&nullCount); err != nil || nullCount != 2 {
		t.Fatalf("existing stores should remain NULL: count=%d err=%v", nullCount, err)
	}
	if _, err := target.Exec(ctx, `UPDATE stores SET guest_display_name=$3 WHERE tenant_id=$1 AND store_id=$2`, "tenant-a", "s1", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, `UPDATE stores SET guest_display_name=$3 WHERE tenant_id=$1 AND store_id=$2`, "tenant-a", "s1", strings.Repeat("y", 80)); err != nil {
		t.Fatalf("80 characters must be accepted: %v", err)
	}
	for _, invalid := range []string{"", "  x", "x ", strings.Repeat("z", 81), "line\x01break"} {
		_, err := target.Exec(ctx, `UPDATE stores SET guest_display_name=$3 WHERE tenant_id=$1 AND store_id=$2`, "tenant-a", "s1", invalid)
		if err == nil {
			t.Errorf("constraint accepted invalid value %q", invalid)
			continue
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("invalid value %q failed with %T %v, want check violation", invalid, err, err)
		}
	}
	if _, err := target.Exec(ctx, `UPDATE stores SET guest_display_name=NULL WHERE tenant_id='tenant-a' AND store_id='s1'`); err != nil {
		t.Fatalf("NULL must be accepted: %v", err)
	}
}

func Test015LevelThreeRollbackExportsAndRestoresPopulatedData(t *testing.T) {
	ctx, target := connectMigrationTestSchema(t)
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := target.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('tenant-a','internal-a'),('tenant-b','internal-b')`); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name,enabled,operating,qr_entry_mode,guest_display_name) VALUES
		('tenant-a','s1','internal-a-s1',false,true,'SESSION_PIN','A店'),
		('tenant-b','s1','internal-b-s1',true,false,'DIRECT_SCAN','B店'),
		('tenant-a','s2','internal-a-s2',true,true,'STAFF_OPEN',NULL)`); err != nil {
		t.Fatal(err)
	}
	var storesBefore, ledgerBefore string
	if err := target.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(s)-'guest_display_name' ORDER BY tenant_id,store_id)::text FROM stores s`).Scan(&storesBefore); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(m) ORDER BY version)::text FROM schema_migrations m WHERE version<15`).Scan(&ledgerBefore); err != nil {
		t.Fatal(err)
	}

	// Match the documented level-3 rollback: the exported values and schema
	// change occur in one bounded transaction while the migration lock is held.
	tx, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='3s'; SET LOCAL statement_timeout='30s'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(771904283)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE stores IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	var exported bytes.Buffer
	if _, err = tx.Conn().PgConn().CopyTo(ctx, &exported, `COPY (SELECT tenant_id,store_id,guest_display_name FROM stores WHERE guest_display_name IS NOT NULL ORDER BY tenant_id,store_id) TO STDOUT`); err != nil {
		t.Fatalf("export inside rollback transaction: %v", err)
	}
	if _, err = tx.Exec(ctx, `ALTER TABLE stores DROP COLUMN guest_display_name`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=15`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var exists bool
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='stores' AND column_name='guest_display_name')`).Scan(&exists); err != nil || exists {
		t.Fatalf("level-3 rollback retained column: exists=%v err=%v", exists, err)
	}
	var storesAfter, ledgerAfter string
	if err = target.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(s) ORDER BY tenant_id,store_id)::text FROM stores s`).Scan(&storesAfter); err != nil || storesAfter != storesBefore {
		t.Fatalf("rollback changed stores outside the removed column: got=%s want=%s err=%v", storesAfter, storesBefore, err)
	}
	if err = target.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(m) ORDER BY version)::text FROM schema_migrations m`).Scan(&ledgerAfter); err != nil {
		t.Fatal(err)
	}
	if ledgerAfter != ledgerBefore {
		t.Fatalf("level-3 rollback changed ledger entries other than 15: got=%s want=%s", ledgerAfter, ledgerBefore)
	}

	// postgres.Up uses the same retained explicit registry as the level-2 app
	// rollback build. Restore exactly the export captured before dropping 015.
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("reapply retained migration registry: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(exported.String()), "\n") {
		row := strings.Split(line, "\t")
		if len(row) != 3 {
			t.Fatalf("unexpected exported record: %q", line)
		}
		if _, err = target.Exec(ctx, `UPDATE stores SET guest_display_name=$3 WHERE tenant_id=$1 AND store_id=$2`, row[0], row[1], row[2]); err != nil {
			t.Fatalf("restore exported store %q/%q: %v", row[0], row[1], err)
		}
	}
	rows, err := target.Query(ctx, `SELECT tenant_id,store_id,guest_display_name FROM stores WHERE guest_display_name IS NOT NULL ORDER BY tenant_id,store_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var restored [][3]string
	for rows.Next() {
		var row [3]string
		if err = rows.Scan(&row[0], &row[1], &row[2]); err != nil {
			t.Fatal(err)
		}
		restored = append(restored, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantRestored := [][3]string{{"tenant-a", "s1", "A店"}, {"tenant-b", "s1", "B店"}}
	if !reflect.DeepEqual(restored, wantRestored) {
		t.Fatalf("restored public names differ from export: %#v", restored)
	}
	if err = target.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(m) ORDER BY version)::text FROM schema_migrations m WHERE version<15`).Scan(&ledgerAfter); err != nil || ledgerAfter != ledgerBefore {
		t.Fatalf("Up after level-3 rollback changed existing ledger entries: got=%s want=%s err=%v", ledgerAfter, ledgerBefore, err)
	}
	var version15Count int
	if err = target.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version=15`).Scan(&version15Count); err != nil || version15Count != 1 {
		t.Fatalf("Up after level-3 rollback did not restore exactly migration 15: count=%d err=%v", version15Count, err)
	}
}
