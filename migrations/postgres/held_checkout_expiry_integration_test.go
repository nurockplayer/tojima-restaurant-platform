package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

// This starts from the actual v7 SQL and data shape: the active hold and
// attempts exist before migration 008 adds its retained marker.
func TestHeldCheckoutExpiryBackfillsV7ActiveHolds(t *testing.T) {
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
	schema := "p82_hold_migrate_" + hex.EncodeToString(raw[:])
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
	for version := 1; version <= 7; version++ {
		migration, readErr := os.ReadFile(fmt.Sprintf("%03d_", version) + migrationName(version) + ".up.sql")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = target.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply v%d: %v", version, err)
		}
	}
	if _, err = target.Exec(ctx, `CREATE TABLE schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now()); INSERT INTO schema_migrations(version) SELECT generate_series(1,7)`); err != nil {
		t.Fatal(err)
	}
	seedV7HoldFixture(t, ctx, target)
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("migrate v7 to v8: %v", err)
	}
	assertBackfilled(t, ctx, target)

	var firstMarker, firstRevocation time.Time
	if err = target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id='00000000000000000000000000000001'`).Scan(&firstMarker, &firstRevocation); err != nil {
		t.Fatal(err)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("idempotent up: %v", err)
	}
	var repeatedMarker, repeatedRevocation time.Time
	if err = target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id='00000000000000000000000000000001'`).Scan(&repeatedMarker, &repeatedRevocation); err != nil {
		t.Fatal(err)
	}
	if !repeatedMarker.Equal(firstMarker) || !repeatedRevocation.Equal(firstRevocation) {
		t.Fatalf("repeated up changed retained timestamps: marker %s -> %s, revocation %s -> %s", firstMarker, repeatedMarker, firstRevocation, repeatedRevocation)
	}
	if _, err = target.Exec(ctx, `DELETE FROM schema_migrations WHERE version=8`); err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("008_held_checkout_expiry.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = target.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='stripe_payment_attempts' AND column_name='company_hold_expire_requested_at')`, schema).Scan(&exists); err != nil || exists {
		t.Fatalf("008 down retained its marker column: exists=%v err=%v", exists, err)
	}
	if err = postgres.Up(ctx, target); err != nil {
		t.Fatalf("reapply v8: %v", err)
	}
	assertBackfilled(t, ctx, target)
	var marker, revoked time.Time
	var workCount, eventCount int
	if err = target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id='00000000000000000000000000000001'`).Scan(&marker, &revoked); err != nil {
		t.Fatal(err)
	}
	if marker.IsZero() || !revoked.Equal(firstRevocation) {
		t.Fatalf("reapply did not restore marker or changed retained revocation: marker %s -> %s, revocation %s -> %s", firstMarker, marker, firstRevocation, revoked)
	}
	if err = target.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_work WHERE dedupe_key='company-hold-expire:00000000000000000000000000000001'`).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if err = target.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempt_events WHERE event_key='company-hold-expire:00000000000000000000000000000001'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if workCount != 1 || eventCount != 1 {
		t.Fatalf("reapply duplicated retained work/audit: work=%d events=%d", workCount, eventCount)
	}
}

func migrationName(version int) string {
	return map[int]string{1: "table_sessions", 2: "menu_catalog", 3: "tax_rates", 4: "shared_cart_orders", 5: "operations", 6: "payments", 7: "company_product_holds"}[version]
}

func seedV7HoldFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES('tenant','Tenant');
		INSERT INTO tenants(tenant_id,name) VALUES('other-tenant','Other tenant');
		INSERT INTO menu_catalogs(tenant_id,owner_kind,draft_version) VALUES('tenant','COMPANY',1),('other-tenant','COMPANY',1);
		INSERT INTO company_product_emergency_holds(tenant_id,company_catalog_id,product_key,active,version,reason,activated_by,activated_at)
		VALUES('tenant',1,'held-product',true,1,'test active hold','test',clock_timestamp()),
		('tenant',1,'second-held-product',true,1,'second test hold','test',clock_timestamp());
		INSERT INTO company_product_emergency_holds(tenant_id,company_catalog_id,product_key,active,version,reason,activated_by,activated_at,cleared_by,cleared_at)
		VALUES('tenant',1,'inactive-product',false,2,'cleared hold','test',clock_timestamp(),'test',clock_timestamp());`); err != nil {
		t.Fatal(err)
	}
	orders := []struct {
		id, tenant, product, secondProduct, method, state, checkoutStatus, providerStatus string
		detached                                                                          bool
	}{
		{id: "00000000000000000000000000000001", tenant: "tenant", product: "held-product", secondProduct: "second-held-product", method: "STRIPE", state: "OPEN", checkoutStatus: "open", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000002", tenant: "tenant", product: "held-product", method: "STRIPE", state: "PAID", checkoutStatus: "complete", providerStatus: "paid"},
		{id: "00000000000000000000000000000003", tenant: "tenant", product: "held-product", method: "STRIPE", state: "EXPIRED", checkoutStatus: "expired", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000004", tenant: "tenant", product: "held-product", method: "STRIPE", state: "OPEN", checkoutStatus: "expired", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000005", tenant: "tenant", product: "other-product", method: "STRIPE", state: "OPEN", checkoutStatus: "open", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000006", tenant: "tenant", product: "held-product", method: "CASH", state: "OPEN", checkoutStatus: "open", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000007", tenant: "tenant", product: "held-product", method: "STRIPE", state: "FAILED", checkoutStatus: "open", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000008", tenant: "tenant", product: "held-product", method: "STRIPE", state: "PROCESSING", checkoutStatus: "complete", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000009", tenant: "tenant", product: "held-product", method: "STRIPE", state: "CREATE_PENDING", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000010", tenant: "tenant", product: "held-product", method: "STRIPE", state: "REVIEW_REQUIRED", checkoutStatus: "complete", providerStatus: "paid"},
		{id: "00000000000000000000000000000011", tenant: "tenant", product: "inactive-product", method: "STRIPE", state: "OPEN", checkoutStatus: "open", providerStatus: "unpaid"},
		{id: "00000000000000000000000000000012", tenant: "tenant", product: "held-product", method: "STRIPE", state: "OPEN", checkoutStatus: "open", providerStatus: "unpaid", detached: true},
		{id: "00000000000000000000000000000013", tenant: "other-tenant", product: "held-product", method: "STRIPE", state: "OPEN", checkoutStatus: "open", providerStatus: "unpaid"},
	}
	for i, order := range orders {
		store := fmt.Sprintf("store-%d", i+1)
		table := fmt.Sprintf("table-%d", i+1)
		session := fmt.Sprintf("session-%d", i+1)
		if _, err := conn.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name) VALUES($1,$2,$2)`, order.tenant, store); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES($1,$2,$3,$3,$3)`, order.tenant, store, table); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO table_sessions(session_id,tenant_id,store_id,table_id,status,qr_mode,expires_at)
			VALUES($1,$2,$3,$4,'OPEN','STAFF_OPEN',clock_timestamp()+interval '1 day')`, session, order.tenant, store, table); err != nil {
			t.Fatal(err)
		}
		catalogID := int64(1)
		catalogMode := "LINKED"
		if order.tenant == "other-tenant" {
			catalogID = 2
		}
		if order.detached {
			catalogMode = "DETACHED"
			if err := conn.QueryRow(ctx, `INSERT INTO menu_catalogs(tenant_id,owner_kind,store_id,draft_version) VALUES($1,'STORE',$2,1) RETURNING catalog_id`, order.tenant, store).Scan(&catalogID); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(ctx, `INSERT INTO store_menu_bindings(tenant_id,store_id,mode,catalog_id,detached_by) VALUES($1,$2,'DETACHED',$3,'test')`, order.tenant, store, catalogID); err != nil {
				t.Fatal(err)
			}
		}
		var checkoutStatus any = order.checkoutStatus
		if order.state == "CREATE_PENDING" || order.state == "FAILED" {
			checkoutStatus = nil
		}
		if _, err := conn.Exec(ctx, `INSERT INTO table_orders(order_id,session_id,tenant_id,store_id,table_id,generation,payment_method,requires_staff_acceptance,catalog_id,catalog_mode,catalog_revision,quote_snapshot,tax_snapshot,total_gross_jpy)
			VALUES($1,$2,$3,$4,$5,1,$6,false,$7,$8,1,jsonb_build_object('quote',jsonb_build_object('lines',jsonb_build_array(jsonb_build_object('product_key',$9::text),jsonb_build_object('product_key',$10::text)))), '{}'::jsonb,100)`, order.id, session, order.tenant, store, table, order.method, catalogID, catalogMode, order.product, order.secondProduct); err != nil {
			t.Fatal(err)
		}
		var checkoutSession any = fmt.Sprintf("cs_%d", i+1)
		if order.state == "CREATE_PENDING" || order.state == "FAILED" {
			checkoutSession = nil
		}
		if _, err := conn.Exec(ctx, `INSERT INTO stripe_payment_attempts(order_id,attempt_id,session_id,tenant_id,store_id,table_id,generation,amount_jpy,currency,tax_snapshot,provider_mode,stripe_account_id,credential_ref,merchant_version,request_profile_fingerprint,api_version,idempotency_key,request_digest,checkout_proof_hash,checkout_proof_expires_at,continuation_expires_at,state,checkout_session_id,checkout_status,provider_payment_status,retry_not_after,payment_review_required)
			VALUES($1,'attempt-'||$2,$3,$4,$5,$6,1,100,'jpy','{}','CONNECTED_DIRECT_TEST_FIXTURE','acct_test','credential',1,decode(repeat('00',32),'hex'),'2025-01-01','key-'||$2,decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex'),statement_timestamp()+interval '1 day',statement_timestamp()+interval '1 day',$7,$8,$9,$10,statement_timestamp()+interval '1 day',$11)`, order.id, fmt.Sprint(i+1), session, order.tenant, store, table, order.state, checkoutSession, checkoutStatus, order.providerStatus, order.state == "REVIEW_REQUIRED"); err != nil {
			t.Fatal(err)
		}
	}
}

func assertBackfilled(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	expected := map[string]bool{
		"00000000000000000000000000000001": true,  // open/unpaid; two held products must enqueue once
		"00000000000000000000000000000002": false, // paid
		"00000000000000000000000000000003": false, // expired state
		"00000000000000000000000000000004": false, // provider says expired
		"00000000000000000000000000000005": false, // product absent from quote
		"00000000000000000000000000000006": false, // cash order
		"00000000000000000000000000000007": true,  // failed attempt without a Checkout binding remains eligible
		"00000000000000000000000000000008": true,  // complete but unpaid remains eligible
		"00000000000000000000000000000009": true,  // CREATE_PENDING has no Checkout binding yet
		"00000000000000000000000000000010": false, // review required
		"00000000000000000000000000000011": false, // inactive hold
		"00000000000000000000000000000012": false, // detached store binding
		"00000000000000000000000000000013": false, // same product, other tenant without a hold
	}
	for orderID, want := range expected {
		key := "company-hold-expire:" + orderID
		var marked, revoked bool
		var workCount, eventCount int
		if err := conn.QueryRow(ctx, `SELECT company_hold_expire_requested_at IS NOT NULL,continuation_revoked_at IS NOT NULL FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&marked, &revoked); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE' AND status='READY' AND dedupe_key=$2`, orderID, key).Scan(&workCount); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempt_events e JOIN stripe_payment_attempts a USING(order_id) WHERE e.order_id=$1 AND e.event_key=$2 AND e.actor_id='migration:008_held_checkout_expiry' AND e.before_state=a.state AND e.after_state=a.state`, orderID, key).Scan(&eventCount); err != nil {
			t.Fatal(err)
		}
		wantCount := 0
		if want {
			wantCount = 1
		}
		if marked != want || revoked != want || workCount != wantCount || eventCount != wantCount {
			t.Errorf("order %s expected eligible=%v; marker=%v revoked=%v work=%d events=%d", orderID, want, marked, revoked, workCount, eventCount)
		}
	}
}
