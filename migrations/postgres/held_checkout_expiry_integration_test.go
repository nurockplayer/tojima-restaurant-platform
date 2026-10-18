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
	ctx, target, schema := newV7MigrationFixture(t)
	seedV7HoldFixture(t, ctx, target)
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatalf("migrate v7 to v8: %v", err)
	}
	assertBackfilled(t, ctx, target)

	var firstMarker, firstRevocation time.Time
	if err := target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id='00000000000000000000000000000001'`).Scan(&firstMarker, &firstRevocation); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatalf("idempotent up: %v", err)
	}
	var repeatedMarker, repeatedRevocation time.Time
	if err := target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id='00000000000000000000000000000001'`).Scan(&repeatedMarker, &repeatedRevocation); err != nil {
		t.Fatal(err)
	}
	if !repeatedMarker.Equal(firstMarker) || !repeatedRevocation.Equal(firstRevocation) {
		t.Fatalf("repeated up changed retained timestamps: marker %s -> %s, revocation %s -> %s", firstMarker, repeatedMarker, firstRevocation, repeatedRevocation)
	}
	if _, err := target.Exec(ctx, `DELETE FROM schema_migrations WHERE version=8`); err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("008_held_checkout_expiry.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='stripe_payment_attempts' AND column_name='company_hold_expire_requested_at')`, schema).Scan(&exists); err != nil || exists {
		t.Fatalf("008 down retained its marker column: exists=%v err=%v", exists, err)
	}
	var triggerExists, triggerFunctionExists, helperFunctionExists bool
	if err := target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='company_product_emergency_holds'::regclass AND tgname='company_product_hold_checkout_expiry' AND NOT tgisinternal),
		to_regprocedure('expire_checkouts_on_company_hold()') IS NOT NULL,
		to_regprocedure('enqueue_company_hold_checkouts(text,text,text)') IS NOT NULL`).Scan(&triggerExists, &triggerFunctionExists, &helperFunctionExists); err != nil {
		t.Fatal(err)
	}
	if triggerExists || triggerFunctionExists || helperFunctionExists {
		t.Fatalf("008 down retained hold expiry objects: trigger=%v trigger function=%v helper function=%v", triggerExists, triggerFunctionExists, helperFunctionExists)
	}
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatalf("reapply v8: %v", err)
	}
	assertBackfilled(t, ctx, target)
	var marker, revoked time.Time
	var workCount, eventCount int
	if err := target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id='00000000000000000000000000000001'`).Scan(&marker, &revoked); err != nil {
		t.Fatal(err)
	}
	if marker.IsZero() || !revoked.Equal(firstRevocation) {
		t.Fatalf("reapply did not restore marker or changed retained revocation: marker %s -> %s, revocation %s -> %s", firstMarker, marker, firstRevocation, revoked)
	}
	if err := target.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_work WHERE dedupe_key='company-hold-expire:00000000000000000000000000000001'`).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempt_events WHERE event_key='company-hold-expire:00000000000000000000000000000001'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if workCount != 1 || eventCount != 1 {
		t.Fatalf("reapply duplicated retained work/audit: work=%d events=%d", workCount, eventCount)
	}
}

// newV7MigrationFixture creates an isolated schema at migration 7. The test
// must seed the v7 hold/order state before applying migration 8.
func newV7MigrationFixture(t *testing.T) (context.Context, *pgx.Conn, string) {
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
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var raw [8]byte
	if _, err = rand.Read(raw[:]); err != nil {
		cancel()
		t.Fatal(err)
	}
	schema := "p82_hold_migrate_" + hex.EncodeToString(raw[:])
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		cancel()
		t.Fatal(err)
	}
	targetConfig := cfg.Copy()
	targetConfig.RuntimeParams["search_path"] = schema
	target, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = target.Close(cleanupCtx)
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = admin.Close(cleanupCtx)
		cancel()
	})
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
	return ctx, target, schema
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
		{id: "00000000000000000000000000000014", tenant: "tenant", product: "raw-insert-product", method: "STRIPE", state: "OPEN", checkoutStatus: "open", providerStatus: "unpaid"},
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
		"00000000000000000000000000000014": false, // only matched by the post-migration raw insert
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

// Reproduce work.go's lockPaymentSessionAny ordering against the actual v7
// schema: the metadata SELECT retains AccessShareLock until persistence commits.
func TestHeldCheckoutExpiryMigrationWaitsForPaymentPersistence(t *testing.T) {
	ctx, target, _ := newV7MigrationFixture(t)
	seedV7HoldFixture(t, ctx, target)
	writer := connectFixture(t, target)
	migrator := connectFixture(t, target)
	tx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	const orderID = "00000000000000000000000000000001"
	var sid, tenant, store, table string
	if err = tx.QueryRow(ctx, `SELECT session_id,tenant_id,store_id,table_id FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&sid, &tenant, &store, &table); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- postgres.Up(ctx, migrator) }()
	migrationPID, writerPID := int(migrator.PgConn().PID()), int(writer.PgConn().PID())
	waitForBlockedBy(t, ctx, target, migrationPID, writerPID)
	var waitingForDDL bool
	if err = target.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND relation='stripe_payment_attempts'::regclass AND mode='AccessExclusiveLock' AND NOT granted)`, migrationPID).Scan(&waitingForDDL); err != nil {
		t.Fatal(err)
	}
	if !waitingForDDL {
		t.Fatal("migration did not wait for the payment metadata table lock")
	}
	// With the old migration, this SHARE request closes the deadlock cycle.
	if _, err = tx.Exec(ctx, `SELECT catalog_id FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind='COMPANY' FOR SHARE`, tenant); err != nil {
		t.Fatalf("payment catalog lock while migration waits for DDL: %v", err)
	}
	if _, err = tx.Exec(ctx, `SELECT session_id FROM table_sessions WHERE session_id=$1 AND tenant_id=$2 AND store_id=$3 AND table_id=$4 FOR UPDATE`, sid, tenant, store, table); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state='PAID',provider_payment_status='paid',checkout_status='complete',updated_at=clock_timestamp() WHERE order_id=$1`, orderID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("payment commit: %v", err)
	}
	if err = <-finished; err != nil {
		t.Fatalf("migration after payment persistence: %v", err)
	}
	var state string
	var marked, revoked bool
	var workCount, eventCount int
	if err = target.QueryRow(ctx, `SELECT state,company_hold_expire_requested_at IS NOT NULL,continuation_revoked_at IS NOT NULL,
		(SELECT count(*) FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'),
		(SELECT count(*) FROM stripe_payment_attempt_events WHERE order_id=$1 AND event_key='company-hold-expire:'||$1)
		FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&state, &marked, &revoked, &workCount, &eventCount); err != nil {
		t.Fatal(err)
	}
	if state != "PAID" || marked || revoked || workCount != 0 || eventCount != 0 {
		t.Fatalf("backfill ignored committed payment: state=%s marked=%v revoked=%v work=%d events=%d", state, marked, revoked, workCount, eventCount)
	}
	assertHoldArtifacts(t, ctx, target, "00000000000000000000000000000007", "migration:008_held_checkout_expiry")
}

// Admission uses the opposite table/catalog order from payment persistence.
// Its catalog SHARE must make the migration release DDL locks and retry, so
// the same transaction can insert its attempt and commit before the backfill.
func TestHeldCheckoutExpiryMigrationRetriesForAdmission(t *testing.T) {
	ctx, target, _ := newV7MigrationFixture(t)
	seedV7HoldFixture(t, ctx, target)
	const orderID = "00000000000000000000000000000015"
	var attempt []byte
	if err := target.QueryRow(ctx, `SELECT to_jsonb(a) || jsonb_build_object('order_id',$1::text,'attempt_id','attempt-15','generation',2,'idempotency_key','key-15')
		FROM stripe_payment_attempts a WHERE order_id='00000000000000000000000000000009'`, orderID).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	writer := connectFixture(t, target)
	migrator := connectFixture(t, target)
	tx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT catalog_id FROM menu_catalogs WHERE catalog_id=1 FOR SHARE`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT session_id FROM table_sessions WHERE session_id='session-9' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO table_orders SELECT (jsonb_populate_record(NULL::table_orders,
		to_jsonb(o) || jsonb_build_object('order_id',$1::text,'generation',2))).*
		FROM table_orders o WHERE order_id='00000000000000000000000000000009'`, orderID); err != nil {
		t.Fatal(err)
	}
	migrationPID, writerPID := int(migrator.PgConn().PID()), int(writer.PgConn().PID())
	finished := make(chan error, 1)
	go func() { finished <- postgres.Up(ctx, migrator) }()
	waitForMigrationRetry(t, ctx, target, migrationPID)
	if _, err = tx.Exec(ctx, `INSERT INTO stripe_payment_attempts SELECT * FROM jsonb_populate_record(NULL::stripe_payment_attempts,$1::jsonb)`, attempt); err != nil {
		t.Fatalf("admission insert while migration retries: %v", err)
	}
	// The next DDL attempt must now wait for admission's retained table lock.
	waitForBlockedBy(t, ctx, target, migrationPID, writerPID)
	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("admission commit: %v", err)
	}
	if err = <-finished; err != nil {
		t.Fatalf("migration after admission: %v", err)
	}
	assertHoldArtifacts(t, ctx, target, orderID, "migration:008_held_checkout_expiry")
}

func TestHeldCheckoutExpiryMigrationWaitsForLegacyActivation(t *testing.T) {
	for _, writeBeforeDDL := range []bool{false, true} {
		t.Run(fmt.Sprintf("hold_written_before_DDL=%v", writeBeforeDDL), func(t *testing.T) {
			ctx, target, _ := newV7MigrationFixture(t)
			seedV7HoldFixture(t, ctx, target)
			writer := connectFixture(t, target)
			migrator := connectFixture(t, target)
			tx, err := writer.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `SELECT catalog_id FROM menu_catalogs WHERE catalog_id=1 FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			activate := func() {
				t.Helper()
				if _, err = tx.Exec(ctx, `UPDATE company_product_emergency_holds SET active=true,version=3,reason='legacy activation',activated_by='legacy-writer',activated_at=clock_timestamp(),cleared_by=NULL,cleared_at=NULL WHERE tenant_id='tenant' AND company_catalog_id=1 AND product_key='inactive-product'`); err != nil {
					t.Fatal(err)
				}
			}
			if writeBeforeDDL {
				activate()
			}
			finished := make(chan error, 1)
			go func() { finished <- postgres.Up(ctx, migrator) }()
			migrationPID := int(migrator.PgConn().PID())
			waitForMigrationRetry(t, ctx, target, migrationPID)
			if !writeBeforeDDL {
				activate()
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-finished; err != nil {
				t.Fatalf("migration after legacy activation: %v", err)
			}
			assertHoldArtifacts(t, ctx, target, "00000000000000000000000000000011", "migration:008_held_checkout_expiry")
		})
	}
}

func TestHeldCheckoutExpiryTriggerCatchesLegacyActivationAfterBackfill(t *testing.T) {
	ctx, target, _ := newV7MigrationFixture(t)
	seedV7HoldFixture(t, ctx, target)
	migrationTx, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	migrationSQL, err := os.ReadFile("008_held_checkout_expiry.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrationTx.Exec(ctx, string(migrationSQL)); err != nil {
		t.Fatal(err)
	}
	var migrationPID, writerPID int
	if err = target.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&migrationPID); err != nil {
		t.Fatal(err)
	}
	writer := connectFixture(t, target)
	if err = writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	activationDone := make(chan error, 1)
	go func() {
		writerTx, beginErr := writer.Begin(ctx)
		if beginErr == nil {
			_, beginErr = writerTx.Exec(ctx, `SELECT catalog_id FROM menu_catalogs WHERE catalog_id=1 FOR UPDATE`)
		}
		if beginErr == nil {
			_, beginErr = writerTx.Exec(ctx, `UPDATE company_product_emergency_holds SET active=true,version=3,reason='legacy activation after migration',activated_by='legacy-writer-after',activated_at=clock_timestamp(),cleared_by=NULL,cleared_at=NULL WHERE tenant_id='tenant' AND company_catalog_id=1 AND product_key='inactive-product'`)
		}
		if beginErr != nil {
			if writerTx != nil {
				_ = writerTx.Rollback(ctx)
			}
			activationDone <- beginErr
			return
		}
		activationDone <- writerTx.Commit(ctx)
	}()
	waitForBlockedBy(t, ctx, target, writerPID, migrationPID)
	if err = migrationTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-activationDone; err != nil {
		t.Fatalf("legacy activation after migration: %v", err)
	}
	assertHoldArtifacts(t, ctx, target, "00000000000000000000000000000011", "legacy-writer-after")
}

func TestHeldCheckoutExpiryTriggerCatchesRawSQLInsertAndIsIdempotent(t *testing.T) {
	ctx, target, _ := newV7MigrationFixture(t)
	seedV7HoldFixture(t, ctx, target)
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatal(err)
	}
	orderID := "00000000000000000000000000000014"
	if _, err := target.Exec(ctx, `INSERT INTO company_product_emergency_holds(tenant_id,company_catalog_id,product_key,active,version,reason,activated_by,activated_at)
		VALUES('tenant',1,'raw-insert-product',true,1,'raw SQL insert','old-binary-insert',clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	assertHoldArtifacts(t, ctx, target, orderID, "old-binary-insert")
	var markerBefore, revokedBefore time.Time
	if err := target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&markerBefore, &revokedBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, `UPDATE company_product_emergency_holds SET active=false,version=2,reason='clear raw insert',cleared_by='old-binary-clear',cleared_at=clock_timestamp() WHERE tenant_id='tenant' AND company_catalog_id=1 AND product_key='raw-insert-product'`); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, `UPDATE company_product_emergency_holds SET active=true,version=3,reason='reactivate raw insert',activated_by='old-binary-reactivate',activated_at=clock_timestamp(),cleared_by=NULL,cleared_at=NULL WHERE tenant_id='tenant' AND company_catalog_id=1 AND product_key='raw-insert-product'`); err != nil {
		t.Fatal(err)
	}
	assertHoldArtifacts(t, ctx, target, orderID, "old-binary-insert")
	var markerAfter, revokedAfter time.Time
	if err := target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&markerAfter, &revokedAfter); err != nil {
		t.Fatal(err)
	}
	if !markerAfter.Equal(markerBefore) || !revokedAfter.Equal(revokedBefore) {
		t.Fatalf("off/on cycle replaced retained timestamps: marker %s -> %s; revocation %s -> %s", markerBefore, markerAfter, revokedBefore, revokedAfter)
	}
}

func TestHeldCheckoutExpiryTriggerCatchesRawSQLInactiveToActiveUpdate(t *testing.T) {
	ctx, target, _ := newV7MigrationFixture(t)
	seedV7HoldFixture(t, ctx, target)
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, `UPDATE company_product_emergency_holds SET active=true,version=3,reason='raw SQL transition',activated_by='old-binary-update',activated_at=clock_timestamp(),cleared_by=NULL,cleared_at=NULL WHERE tenant_id='tenant' AND company_catalog_id=1 AND product_key='inactive-product'`); err != nil {
		t.Fatal(err)
	}
	assertHoldArtifacts(t, ctx, target, "00000000000000000000000000000011", "old-binary-update")
}

func TestHeldCheckoutExpiryTriggerRechecksAfterPaidWriter(t *testing.T) {
	ctx, target, _ := newV7MigrationFixture(t)
	seedV7HoldFixture(t, ctx, target)
	if err := postgres.Up(ctx, target); err != nil {
		t.Fatal(err)
	}
	paidWriter := connectFixture(t, target)
	oldWriter := connectFixture(t, target)
	var paidPID, oldPID int
	if err := paidWriter.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&paidPID); err != nil {
		t.Fatal(err)
	}
	if err := oldWriter.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&oldPID); err != nil {
		t.Fatal(err)
	}
	paidTx, err := paidWriter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = paidTx.Exec(ctx, `SELECT catalog_id FROM menu_catalogs WHERE catalog_id=1 FOR SHARE`); err != nil {
		t.Fatal(err)
	}
	if _, err = paidTx.Exec(ctx, `SELECT session_id FROM table_sessions WHERE session_id='session-14' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	activationDone := make(chan error, 1)
	go func() {
		activationTx, activationErr := oldWriter.Begin(ctx)
		if activationErr == nil {
			_, activationErr = activationTx.Exec(ctx, `INSERT INTO company_product_emergency_holds(tenant_id,company_catalog_id,product_key,active,version,reason,activated_by,activated_at)
				VALUES('tenant',1,'raw-insert-product',true,1,'racing raw SQL hold','old-binary-race',clock_timestamp())`)
		}
		if activationErr != nil {
			if activationTx != nil {
				_ = activationTx.Rollback(ctx)
			}
			activationDone <- activationErr
			return
		}
		activationDone <- activationTx.Commit(ctx)
	}()
	waitForBlockedBy(t, ctx, target, oldPID, paidPID)
	if _, err = paidTx.Exec(ctx, `UPDATE stripe_payment_attempts SET state='PAID',provider_payment_status='paid',checkout_status='complete',updated_at=clock_timestamp() WHERE order_id='00000000000000000000000000000014'`); err != nil {
		t.Fatal(err)
	}
	if err = paidTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-activationDone; err != nil {
		t.Fatalf("raw SQL activation after payment: %v", err)
	}
	var marker, revoked *time.Time
	if err = target.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id='00000000000000000000000000000014'`).Scan(&marker, &revoked); err != nil {
		t.Fatal(err)
	}
	var workCount, eventCount int
	if err = target.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_work WHERE order_id='00000000000000000000000000000014' AND work_type='EXPIRE'`).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if err = target.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempt_events WHERE order_id='00000000000000000000000000000014' AND event_key='company-hold-expire:00000000000000000000000000000014'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if marker != nil || revoked != nil || workCount != 0 || eventCount != 0 {
		t.Fatalf("paid attempt was marked for expiry: marker=%v revoked=%v work=%d events=%d", marker, revoked, workCount, eventCount)
	}
}

func connectFixture(t *testing.T, source *pgx.Conn) *pgx.Conn {
	t.Helper()
	conn, err := pgx.ConnectConfig(context.Background(), source.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func waitForBlockedBy(t *testing.T, ctx context.Context, observer *pgx.Conn, blockedPID, blockerPID int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := observer.QueryRow(ctx, `SELECT $2=ANY(pg_blocking_pids($1))`, blockedPID, blockerPID).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("backend %d never became blocked by backend %d", blockedPID, blockerPID)
}

// Observe the retry itself, not an arbitrary delay: PgSleep is reached only
// after NOWAIT failed and the DDL subtransaction released its relation locks.
func waitForMigrationRetry(t *testing.T, ctx context.Context, observer *pgx.Conn, migrationPID int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var retrying bool
		err := observer.QueryRow(ctx, `SELECT coalesce(wait_event='PgSleep',false) AND NOT EXISTS(
			SELECT 1 FROM pg_locks WHERE pid=$1 AND
			((relation='stripe_payment_attempts'::regclass AND mode='AccessExclusiveLock') OR
			 (relation='company_product_emergency_holds'::regclass AND mode='ShareRowExclusiveLock')))
			FROM pg_stat_activity WHERE pid=$1`, migrationPID).Scan(&retrying)
		if err != nil {
			t.Fatal(err)
		}
		if retrying {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("migration backend %d never retried with DDL locks released", migrationPID)
}

func assertHoldArtifacts(t *testing.T, ctx context.Context, conn *pgx.Conn, orderID, actor string) {
	t.Helper()
	var marker, revoked time.Time
	if err := conn.QueryRow(ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&marker, &revoked); err != nil {
		t.Fatal(err)
	}
	var workCount, eventCount int
	key := "company-hold-expire:" + orderID
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE' AND dedupe_key=$2`, orderID, key).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempt_events WHERE order_id=$1 AND event_key=$2 AND actor_id=$3`, orderID, key, actor).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if marker.IsZero() || revoked.IsZero() || workCount != 1 || eventCount != 1 {
		t.Fatalf("order %s expected retained marker, revocation, one EXPIRE work item, and one %q audit; marker=%s revoked=%s work=%d events=%d", orderID, actor, marker, revoked, workCount, eventCount)
	}
}
