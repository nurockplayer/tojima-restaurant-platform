package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func TestPostgresAdmissionLocksProtectMenuAndTaxSchedule(t *testing.T) {
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
	schema := "p78_admission_" + hex.EncodeToString(random)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); dropErr != nil {
			t.Errorf("drop owned admission schema: %v", dropErr)
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
		INSERT INTO stores(tenant_id,store_id,name) VALUES('tenant','store','Store')`); err != nil {
		t.Fatal(err)
	}
	service, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := service.SaveCompanyDraft(ctx, "tenant", "owner", 0, sampleMenu("Soup", 1100, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.PublishCompanyDraft(ctx, "tenant", "owner", draft.Version); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	menu, err := service.LockStoreMenuForAdmissionTx(ctx, tx, StoreScope{TenantID: "tenant", StoreID: "store"})
	if err != nil {
		t.Fatal(err)
	}
	if menu.CatalogID < 1 || menu.Revision != 1 || menu.Mode != "LINKED" {
		t.Fatalf("locked menu lost private identity: %+v", menu)
	}

	// A store administrator cannot change enabled/operating state through the
	// admission window; timeout proves the parent row lock conflicts with UPDATE.
	writeCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err = pool.Exec(writeCtx, `UPDATE stores SET operating=false WHERE tenant_id='tenant' AND store_id='store'`)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("store update did not wait behind admission share lock: %v", err)
	}

	// SHARE table lock prevents concurrent insertions as well as edits, so a new
	// overlapping legal rule cannot appear between quote resolution and commit.
	taxWriteCtx, cancelTaxWrite := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err = pool.Exec(taxWriteCtx, `INSERT INTO tax_rate_schedule(tax_rule_id,tax_class,rate_numerator,rate_denominator,effective_from,legal_source,legal_status,rule_version)
		VALUES('concurrent-proposal','JP_DINE_IN_FOOD',12,100,DATE '2030-01-01','test','PROPOSED','test-v1')`)
	cancelTaxWrite()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("tax schedule insert did not wait behind admission table lock: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var operating bool
	if err = pool.QueryRow(ctx, `SELECT operating FROM stores WHERE tenant_id='tenant' AND store_id='store'`).Scan(&operating); err != nil || !operating {
		t.Fatalf("timed-out update changed store state: operating=%v err=%v", operating, err)
	}
}
