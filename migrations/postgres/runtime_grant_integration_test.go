package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

// TestGrantRuntimeDMLOnly proves the runtime role can run DML but not DDL,
// TRUNCATE or ledger writes, using an unprivileged role against a migrated
// schema that the owner role still owns.
func TestGrantRuntimeDMLOnly(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("runtime grant test requires loopback PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var raw [8]byte
	if _, err = rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(raw[:])
	schema := "p_rt_schema_" + suffix
	role := "p_rt_" + suffix
	s := pgx.Identifier{schema}.Sanitize()
	r := pgx.Identifier{role}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+s); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "CREATE ROLE "+r+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		bg := context.Background()
		_, _ = admin.Exec(bg, "DROP SCHEMA IF EXISTS "+s+" CASCADE")
		_, _ = admin.Exec(bg, "DROP OWNED BY "+r)
		_, _ = admin.Exec(bg, "DROP ROLE IF EXISTS "+r)
	}()

	targetConfig := cfg.Copy()
	targetConfig.RuntimeParams["search_path"] = schema
	target, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(context.Background())
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Re-running the grants must stay idempotent: migrate applies them on every Up.
	for pass := 1; pass <= 2; pass++ {
		if err = postgres.GrantRuntime(ctx, target, role); err != nil {
			t.Fatalf("grant runtime pass %d: %v", pass, err)
		}
	}

	// asRole starts a transaction that expires with the statement, so nothing
	// the probe writes survives.
	asRole := func(t *testing.T, sql string) error {
		t.Helper()
		tx, err := target.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, "SET LOCAL ROLE "+r); err != nil {
			t.Fatalf("set local role: %v", err)
		}
		if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+s); err != nil {
			t.Fatalf("set local search_path: %v", err)
		}
		_, err = tx.Exec(ctx, sql)
		return err
	}
	assertAllowed := func(name, sql string) {
		t.Helper()
		if err := asRole(t, sql); err != nil {
			t.Fatalf("%s: want success, got %v", name, err)
		}
	}
	assertDenied := func(name, sql string) {
		t.Helper()
		err := asRole(t, sql)
		var pgErr *pgconn.PgError
		if err == nil {
			t.Fatalf("%s: want insufficient_privilege, statement succeeded", name)
		}
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: want insufficient_privilege, got %v", name, err)
		}
	}

	// Reads and ordinary DML (including an identity sequence) stay available.
	assertAllowed("select ledger", "SELECT count(*) FROM schema_migrations")
	assertAllowed("insert tenant", "INSERT INTO tenants(tenant_id, name) VALUES ('p-rt-tenant', 'Runtime grant test')")
	assertAllowed("sequence use", "SELECT nextval('menu_catalogs_catalog_id_seq')")

	// DDL, TRUNCATE and ledger writes are the owner role's business only.
	assertDenied("ledger insert", "INSERT INTO schema_migrations(version) VALUES (999)")
	assertDenied("create table", "CREATE TABLE rt_ddl_probe(i int)")
	assertDenied("truncate ledger", "TRUNCATE schema_migrations")

	if err = postgres.GrantRuntime(ctx, target, ""); err == nil {
		t.Fatal("empty runtime role name must be rejected")
	}
}
