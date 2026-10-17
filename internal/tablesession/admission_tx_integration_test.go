package tablesession

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func TestPostgresAdmissionGrantLocksAndRechecksRevocation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "p78_session_admission_" + hex.EncodeToString(random)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); dropErr != nil {
			t.Errorf("drop owned session admission schema: %v", dropErr)
		}
	}()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = postgres.Up(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES('tenant','Tenant');
		INSERT INTO stores(tenant_id,store_id,name) VALUES('tenant','store','Store');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES('tenant','store','table','Table','locator');
		INSERT INTO table_sessions(session_id,tenant_id,store_id,table_id,status,qr_mode,expires_at)
		VALUES('session','tenant','store','table','OPEN','STAFF_OPEN',clock_timestamp()+interval '5 minutes')`); err != nil {
		t.Fatal(err)
	}
	const rawToken = "integration-only-token-value"
	hash := sha256.Sum256([]byte(rawToken))
	if _, err = pool.Exec(ctx, `INSERT INTO table_join_grants(token_hash,session_id,generation,expires_at)
		VALUES($1,'session',1,clock_timestamp()+interval '4 minutes')`, hash[:]); err != nil {
		t.Fatal(err)
	}
	service, err := New(pool, testOptions(), allowClose{})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{TenantID: "tenant", StoreID: "store", TableID: "table"}
	prechecked, err := service.PrecheckGrant(ctx, "session", rawToken)
	if err != nil || prechecked.Scope != scope {
		t.Fatalf("precheck did not derive canonical scope: session=%+v err=%v", prechecked, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT enabled FROM tenants WHERE tenant_id='tenant' FOR SHARE`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT enabled FROM stores WHERE tenant_id='tenant' AND store_id='store' FOR SHARE`); err != nil {
		t.Fatal(err)
	}
	locked, err := LockAndValidateGrantTx(ctx, tx, scope, "session", rawToken)
	if err != nil || locked.Scope != scope {
		t.Fatalf("authoritative locked grant validation failed: session=%+v err=%v", locked, err)
	}
	trustedAt, err := transactionClock(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RecheckGrantTx(ctx, tx, scope, "session", rawToken, trustedAt); err != nil {
		t.Fatalf("post-cart trusted-time recheck rejected active grant: %v", err)
	}
	if _, err = RecheckGrantTx(ctx, tx, scope, "session", rawToken, trustedAt.Add(time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("future trusted time should reject expired capabilities: %v", err)
	}
	if _, err = RecheckGrantTx(ctx, tx, Scope{TenantID: "tenant", StoreID: "store", TableID: "other"}, "session", rawToken, trustedAt); !errors.Is(err, ErrDenied) {
		t.Fatalf("scope mismatch was accepted: %v", err)
	}

	// A concurrent revocation cannot delete the shared-locked grant in the
	// admission window. Once the transaction ends, deletion succeeds and the
	// same previously prechecked raw token no longer authorizes a new admission.
	deleteCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = pool.Exec(deleteCtx, `DELETE FROM table_join_grants WHERE token_hash=$1`, hash[:])
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("grant revocation did not wait behind admission lock: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM table_join_grants WHERE token_hash=$1`, hash[:]); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT enabled FROM tenants WHERE tenant_id='tenant' FOR SHARE`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT enabled FROM stores WHERE tenant_id='tenant' AND store_id='store' FOR SHARE`); err != nil {
		t.Fatal(err)
	}
	if _, err = LockAndValidateGrantTx(ctx, tx, scope, "session", rawToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked prechecked token still admitted: %v", err)
	}
}
