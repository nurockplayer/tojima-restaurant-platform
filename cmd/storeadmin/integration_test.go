package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

// newIntegrationStore migrates a random schema and returns a pool plus the
// production store. It refuses anything but a loopback test database.
func newIntegrationStore(t *testing.T) (*pgxpool.Pool, *postgresStore) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if !loopbackHost(cfg.ConnConfig.Host) {
		t.Fatal("storeadmin integration test requires loopback PostgreSQL")
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if fallback == nil || !loopbackHost(fallback.Host) {
			t.Fatal("storeadmin integration test requires loopback PostgreSQL")
		}
	}
	admin, err := pgx.ConnectConfig(ctx, cfg.ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	var raw [8]byte
	if _, err = rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	schema := "storeadmin_" + hex.EncodeToString(raw[:])
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), time.Minute)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = admin.Close(dropCtx)
	})
	targetConfig := cfg.ConnConfig.Copy()
	targetConfig.RuntimeParams["search_path"] = schema
	target, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	_ = target.Close(ctx)

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := newPostgresStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return pool, store
}

const integrationSpecV1 = `{"tenant":{"id":"tojima","name":"東島"},
 "store":{"id":"main","name":"本店","guest_display_name":"東島","qr_entry_mode":"STAFF_OPEN",
          "cash":{"enabled":true,"mode":"PAY_LATER"}},
 "tables":[{"id":"t01","name":"1番"},{"id":"t02","name":"2番"}]}`

const integrationSpecV2 = `{"tenant":{"id":"tojima","name":"東島"},
 "store":{"id":"main","name":"本店","guest_display_name":"東島","qr_entry_mode":"DIRECT_SCAN"},
 "tables":[{"id":"t01","name":"窓側1番"},{"id":"t03","name":"3番"}]}`

func dbLocators(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT table_id,public_locator FROM dining_tables ORDER BY table_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	locators := map[string]string{}
	for rows.Next() {
		var id, locator string
		if err := rows.Scan(&id, &locator); err != nil {
			t.Fatal(err)
		}
		locators[id] = locator
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return locators
}

func dbCashAuditRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM store_cash_settings_audit`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func guestSessions(t *testing.T, pool *pgxpool.Pool) *tablesession.Service {
	t.Helper()
	pepper := make([]byte, 32)
	if _, err := rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	service, err := tablesession.New(pool, tablesession.Options{
		SessionTTL: time.Hour, GrantTTL: time.Minute, GrantWindow: time.Minute, GrantWindowMax: 100,
		PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: pepper,
	}, tablesession.DenyClose{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestApplyCreatesConvergesAndRotates(t *testing.T) {
	pool, store := newIntegrationStore(t)
	ctx := context.Background()

	code, stdout, stderr := runCLI(t, store, "apply", "-f", writeSpec(t, integrationSpecV1), "-public-origin", "https://order.example.jp")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 5 || lines[0] != "tenant tojima: created" || lines[1] != "store main: created" || lines[4] != "cash: enabled PAY_LATER (changed)" {
		t.Fatalf("stdout=%q", stdout)
	}
	first := dbLocators(t, pool)
	if len(first) != 2 || !locatorPattern.MatchString(first["t01"]) || !locatorPattern.MatchString(first["t02"]) {
		t.Fatalf("locators=%v", first)
	}
	for i, id := range []string{"t01", "t02"} {
		if fields := strings.Fields(lines[2+i]); fields[4] != "https://order.example.jp/?table="+first[id] {
			t.Fatalf("line=%q locator=%s", lines[2+i], first[id])
		}
	}

	var tenantName, storeName, mode string
	var guest *string
	if err := pool.QueryRow(ctx, `SELECT name FROM tenants WHERE tenant_id='tojima'`).Scan(&tenantName); err != nil || tenantName != "東島" {
		t.Fatalf("tenant name=%q err=%v", tenantName, err)
	}
	if err := pool.QueryRow(ctx, `SELECT name,guest_display_name,qr_entry_mode FROM stores WHERE tenant_id='tojima' AND store_id='main'`).
		Scan(&storeName, &guest, &mode); err != nil || storeName != "本店" || guest == nil || *guest != "東島" || mode != "STAFF_OPEN" {
		t.Fatalf("store name=%q guest=%v mode=%q err=%v", storeName, guest, mode, err)
	}
	var enabled bool
	var cashMode string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT enabled,mode,version FROM store_cash_settings WHERE tenant_id='tojima' AND store_id='main'`).
		Scan(&enabled, &cashMode, &version); err != nil || !enabled || cashMode != "PAY_LATER" || version != 1 {
		t.Fatalf("cash enabled=%v mode=%q version=%d err=%v", enabled, cashMode, version, err)
	}
	if rows := dbCashAuditRows(t, pool); rows != 1 {
		t.Fatalf("audit rows=%d", rows)
	}

	// A second identical apply converges: every row unchanged, no audit row.
	code, stdout, stderr = runCLI(t, store, "apply", "-f", writeSpec(t, integrationSpecV1), "-public-origin", "https://order.example.jp")
	if code != 0 || stderr != "" {
		t.Fatalf("second apply code=%d stderr=%q", code, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if !strings.Contains(line, "unchanged") {
			t.Fatalf("not unchanged: %q (stdout=%q)", line, stdout)
		}
	}
	if rows := dbCashAuditRows(t, pool); rows != 1 {
		t.Fatalf("audit rows after re-apply=%d", rows)
	}
	if again := dbLocators(t, pool); again["t01"] != first["t01"] || again["t02"] != first["t02"] {
		t.Fatalf("locators changed: %v -> %v", first, again)
	}

	// Rename t01, add t03, switch the entry mode, leave t02 out of the file.
	code, stdout, stderr = runCLI(t, store, "apply", "-f", writeSpec(t, integrationSpecV2))
	if code != 0 || stderr != "" {
		t.Fatalf("v2 apply code=%d stderr=%q", code, stderr)
	}
	lines = strings.Split(strings.TrimSpace(stdout), "\n")
	if lines[1] != "store main: updated" || !strings.HasPrefix(lines[2], "table t01 窓側1番 ") || !strings.HasSuffix(lines[2], " renamed") {
		t.Fatalf("stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "not in file (kept): t02\n") || !strings.Contains(stdout, "cash: not in file (kept)\n") {
		t.Fatalf("stdout=%q", stdout)
	}
	changed := dbLocators(t, pool)
	if len(changed) != 3 || changed["t01"] != first["t01"] || changed["t02"] != first["t02"] || !locatorPattern.MatchString(changed["t03"]) {
		t.Fatalf("locators=%v", changed)
	}
	if err := pool.QueryRow(ctx, `SELECT qr_entry_mode FROM stores WHERE tenant_id='tojima' AND store_id='main'`).Scan(&mode); err != nil || mode != "DIRECT_SCAN" {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	if rows := dbCashAuditRows(t, pool); rows != 1 {
		t.Fatalf("audit rows after v2=%d", rows)
	}

	// v2 must also converge.
	code, stdout, stderr = runCLI(t, store, "apply", "-f", writeSpec(t, integrationSpecV2))
	if code != 0 || stderr != "" {
		t.Fatalf("second v2 code=%d stderr=%q", code, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.Contains(line, "unchanged") || line == "cash: not in file (kept)" || strings.HasPrefix(line, "not in file (kept): ") {
			continue
		}
		t.Fatalf("not converged: %q (stdout=%q)", line, stdout)
	}

	// rotate-locator changes exactly one table.
	oldT01 := changed["t01"]
	code, stdout, stderr = runCLI(t, store, "rotate-locator", "-tenant", "tojima", "-store", "main", "-table", "t01")
	if code != 0 || stderr != "" {
		t.Fatalf("rotate code=%d stderr=%q", code, stderr)
	}
	rotated := strings.TrimSpace(stdout)
	if !locatorPattern.MatchString(rotated) || rotated == oldT01 {
		t.Fatalf("rotated=%q", rotated)
	}
	after := dbLocators(t, pool)
	if after["t01"] != rotated || after["t02"] != changed["t02"] || after["t03"] != changed["t03"] {
		t.Fatalf("locators=%v", after)
	}

	// A guest entry by the new locator reaches the table; the old one is gone.
	sessions := guestSessions(t, pool)
	table, err := sessions.TableByLocator(ctx, rotated)
	if err != nil || table.Scope.TenantID != "tojima" || table.Scope.StoreID != "main" || table.Scope.TableID != "t01" || table.Mode != tablesession.DirectScan {
		t.Fatalf("table=%+v err=%v", table, err)
	}
	if _, err := sessions.TableByLocator(ctx, oldT01); !errors.Is(err, tablesession.ErrNotFound) {
		t.Fatalf("old locator still resolves: %v", err)
	}

	// The listing command reads the same rows, and a missing table is refused.
	code, stdout, stderr = runCLI(t, store, "tables", "-tenant", "tojima", "-store", "main")
	if code != 0 || stderr != "" || len(strings.Split(strings.TrimSpace(stdout), "\n")) != 3 {
		t.Fatalf("tables code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, stderr = runCLI(t, store, "rotate-locator", "-tenant", "tojima", "-store", "main", "-table", "t09")
	if code != 1 || stderr != "storeadmin: store or table not found\n" {
		t.Fatalf("missing table code=%d stderr=%q", code, stderr)
	}
}

// A plan computed while the store was still absent must not lose to a
// concurrent first apply that created it with another mode and table name.
func TestApplyConvergesAfterConcurrentFirstApply(t *testing.T) {
	pool, store := newIntegrationStore(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES('tojima','東島');
		INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode) VALUES('tojima','main','本店','DIRECT_SCAN');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES('tojima','main','t01','Terrace','other-operator-locator')`); err != nil {
		t.Fatal(err)
	}
	stale := plan{Tenant: named{ID: "tojima", Name: "東島"}, StoreID: "main", StoreName: "本店",
		Mode: tablesession.StaffOpen, ModeChanged: false,
		NewTables: []tableRow{{ID: "t01", Name: "Indoor", Locator: "stale-plan-locator"}}}
	if err := store.Apply(ctx, "tojima", stale); err != nil {
		t.Fatal(err)
	}
	var mode, name, locator string
	if err := pool.QueryRow(ctx, `SELECT s.qr_entry_mode,d.name,d.public_locator FROM stores s JOIN dining_tables d USING(tenant_id,store_id) WHERE d.table_id='t01'`).Scan(&mode, &name, &locator); err != nil {
		t.Fatal(err)
	}
	if mode != string(tablesession.StaffOpen) || name != "Indoor" || locator != "other-operator-locator" {
		t.Fatalf("want STAFF_OPEN/Indoor/kept locator, got %s/%s/%s", mode, name, locator)
	}
}
