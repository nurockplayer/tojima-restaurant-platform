package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func TestPostgresCompanyProductEmergencyHoldCASReplayPublishAndDetach(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())

	if _, err = fixture.Pool.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name) VALUES ('test-tenant','detached','Detached')`); err != nil {
		t.Fatal(err)
	}
	detachedScope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "detached"}
	if _, err = fixture.Menus.DetachStore(ctx, detachedScope, "hold-test", 1); err != nil {
		t.Fatalf("prepare independent detached snapshot: %v", err)
	}
	for i := 0; i < 49; i++ {
		storeID := fmt.Sprintf("linked-%02d", i+1)
		if _, err = fixture.Pool.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name) VALUES ('test-tenant',$1,$2)`, storeID, storeID); err != nil {
			t.Fatalf("prepare 50 linked stores: %v", err)
		}
	}
	if _, err = fixture.Pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('foreign-tenant','Foreign tenant');
		INSERT INTO stores(tenant_id,store_id,name) VALUES ('foreign-tenant','store','Foreign store')`); err != nil {
		t.Fatal(err)
	}
	foreignDraft, err := fixture.Menus.SaveCompanyDraft(ctx, "foreign-tenant", "foreign-owner", 0, catalog.CatalogData{
		Categories: []catalog.Category{{Key: "food", Name: "Food", Products: []catalog.Product{{Key: "teishoku", Name: "Separate", BasePriceMinor: 900, Active: true}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Menus.PublishCompanyDraft(ctx, "foreign-tenant", "foreign-owner", foreignDraft.Version); err != nil {
		t.Fatal(err)
	}

	companyScope := catalog.StoreScope{TenantID: "test-tenant"}
	mainStoreScope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
	initialOverride, err := fixture.Menus.SetStorePriceOverride(ctx, mainStoreScope, "hold-test", "teishoku", 1250, 0)
	if err != nil {
		t.Fatalf("prepare store override: %v", err)
	}
	beforeLinkedMenu, err := fixture.Menus.ReadStoreMenu(ctx, mainStoreScope)
	if err != nil {
		t.Fatal(err)
	}
	beforeProduct := findHoldTestProduct(beforeLinkedMenu.CatalogData, "teishoku")
	initial, err := fixture.Menus.GetCompanyDraft(ctx, companyScope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fixture.Menus.GetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku"); err != nil || got.Active || got.Version != 0 || got.LinkedStoreCount != 50 {
		t.Fatalf("new product hold should begin clear at version zero: got=%+v err=%v", got, err)
	}

	on, err := fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", true, "food safety review", 0, "hold-on-1")
	if err != nil || !on.Active || on.Version != 1 || on.LinkedStoreCount != 50 || on.ActivatedBy != "tenant-admin" || on.ActivatedAt == nil {
		t.Fatalf("activate company hold: %+v err=%v", on, err)
	}
	linked, err := fixture.Menus.ReadStoreMenu(ctx, mainStoreScope)
	if err != nil {
		t.Fatal(err)
	}
	if product := findHoldTestProduct(linked.CatalogData, "teishoku"); !product.CompanyEmergencyHoldActive || product.CompanyEmergencyHoldVersion != 1 || product.Selectable {
		t.Fatalf("linked menu did not apply company hold: %+v", product)
	}
	afterProduct := findHoldTestProduct(linked.CatalogData, "teishoku")
	if afterProduct.EffectivePriceMinor != beforeProduct.EffectivePriceMinor || afterProduct.PriceOverrideVersion != beforeProduct.PriceOverrideVersion || afterProduct.PriceOverrideActive != beforeProduct.PriceOverrideActive || !reflect.DeepEqual(afterProduct.Groups, beforeProduct.Groups) || afterProduct.Availability != beforeProduct.Availability || afterProduct.AvailabilityVersion != beforeProduct.AvailabilityVersion {
		t.Fatalf("hold changed store override, option state, or availability: before=%+v after=%+v", beforeProduct, afterProduct)
	}
	for i := 1; i <= 49; i++ {
		storeID := fmt.Sprintf("linked-%02d", i)
		menu, readErr := fixture.Menus.ReadStoreMenu(ctx, catalog.StoreScope{TenantID: "test-tenant", StoreID: storeID})
		if readErr != nil {
			t.Fatalf("read linked store %s: %v", storeID, readErr)
		}
		product := findHoldTestProduct(menu.CatalogData, "teishoku")
		if !product.CompanyEmergencyHoldActive || product.CompanyEmergencyHoldVersion != 1 || product.Selectable {
			t.Fatalf("hold did not reach linked store %s: %+v", storeID, product)
		}
	}
	detached, err := fixture.Menus.ReadStoreMenu(ctx, detachedScope)
	if err != nil {
		t.Fatal(err)
	}
	if product := findHoldTestProduct(detached.CatalogData, "teishoku"); product.CompanyEmergencyHoldActive || product.CompanyEmergencyHoldVersion != 0 {
		t.Fatalf("company hold leaked into independent detached catalog: %+v", product)
	}
	foreign, err := fixture.Menus.ReadStoreMenu(ctx, catalog.StoreScope{TenantID: "foreign-tenant", StoreID: "store"})
	if err != nil {
		t.Fatal(err)
	}
	if product := findHoldTestProduct(foreign.CatalogData, "teishoku"); product.CompanyEmergencyHoldActive || product.CompanyEmergencyHoldVersion != 0 {
		t.Fatalf("company hold leaked across tenant catalog scope: %+v", product)
	}
	if _, err = fixture.Menus.DetachStore(ctx, mainStoreScope, "hold-test", 1); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("active held product should block detachment: %v", err)
	}
	if heldProduct := findHoldTestProduct(linked.CatalogData, "teishoku"); heldProduct.EffectivePriceMinor != 1250 || heldProduct.PriceOverrideVersion != initialOverride.Version || heldProduct.Availability != catalog.Available {
		t.Fatalf("hold changed store override or availability: %+v", heldProduct)
	}

	// A normal publication changes the source revision but neither clears nor
	// re-keys the stable company hold.
	changed := initial.CatalogData
	changed.Categories = append([]catalog.Category(nil), changed.Categories...)
	for i := range changed.Categories {
		changed.Categories[i].Products = append([]catalog.Product(nil), changed.Categories[i].Products...)
		for j := range changed.Categories[i].Products {
			if changed.Categories[i].Products[j].Key == "teishoku" {
				changed.Categories[i].Products[j].BasePriceMinor++
			}
		}
	}
	saved, err := fixture.Menus.SaveCompanyDraft(ctx, companyScope.TenantID, "tenant-admin", initial.Version, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Menus.PublishCompanyDraft(ctx, companyScope.TenantID, "tenant-admin", saved.Version); err != nil {
		t.Fatalf("publish while held: %v", err)
	}
	got, err := fixture.Menus.GetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku")
	if err != nil || !got.Active || got.Version != 1 || got.PublishedRevision != 2 {
		t.Fatalf("publication did not preserve hold: %+v err=%v", got, err)
	}

	// Exact replay returns the original immutable result, even after later state
	// transitions. Reusing the same key for a different request is rejected.
	replay, err := fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", true, "food safety review", 0, "hold-on-1")
	if err != nil || replay.Version != 1 || replay.PublishedRevision != 1 {
		t.Fatalf("exact hold replay did not return its original result: %+v err=%v", replay, err)
	}
	if _, err = fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", false, "clear review", 1, "hold-on-1"); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("idempotency key reuse with a different request accepted: %v", err)
	}

	off, err := fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", false, "review cleared", 1, "hold-off-1")
	if err != nil || off.Active || off.Version != 2 || off.ClearedBy != "tenant-admin" || off.ClearedAt == nil {
		t.Fatalf("clear company hold: %+v err=%v", off, err)
	}
	if _, err = fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", true, "second review", 2, "hold-on-2"); err != nil {
		t.Fatalf("activate next hold version: %v", err)
	}
	if _, err = fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", false, "second review cleared", 3, "hold-off-2"); err != nil {
		t.Fatalf("clear next hold version: %v", err)
	}
	offReplay, err := fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", false, "review cleared", 1, "hold-off-1")
	if err != nil || offReplay.Active || offReplay.Version != 2 {
		t.Fatalf("old OFF replay did not return its immutable result: %+v err=%v", offReplay, err)
	}
	if _, err = fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", true, "stale transition", 2, "stale-hold-on"); !errors.Is(err, catalog.ErrStale) {
		t.Fatalf("stale hold generation unexpectedly changed state: %v", err)
	}
	currentHold, err := fixture.Menus.GetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku")
	if err != nil || currentHold.Active || currentHold.Version != 4 {
		t.Fatalf("old replay or stale CAS changed current hold generation: %+v err=%v", currentHold, err)
	}
	if _, err = fixture.Menus.DetachStore(ctx, mainStoreScope, "hold-test", 2); err != nil {
		t.Fatalf("clear hold should permit explicit detachment: %v", err)
	}
}

func findHoldTestProduct(data catalog.CatalogData, key string) catalog.Product {
	for _, category := range data.Categories {
		for _, product := range category.Products {
			if product.Key == key {
				return product
			}
		}
	}
	return catalog.Product{}
}

func TestPostgresCompanyProductEmergencyHoldSurvivesProductRetirement(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())

	if _, err = fixture.Menus.SetCompanyProductEmergencyHold(ctx, "test-tenant", "teishoku", "tenant-admin", true, "safety review", 0, "retire-hold-on"); err != nil {
		t.Fatal(err)
	}
	draft, err := fixture.Menus.GetCompanyDraft(ctx, "test-tenant")
	if err != nil {
		t.Fatal(err)
	}
	for categoryIndex := range draft.CatalogData.Categories {
		products := draft.CatalogData.Categories[categoryIndex].Products[:0]
		for _, product := range draft.CatalogData.Categories[categoryIndex].Products {
			if product.Key != "teishoku" {
				products = append(products, product)
			}
		}
		draft.CatalogData.Categories[categoryIndex].Products = products
	}
	retired, err := fixture.Menus.SaveCompanyDraft(ctx, "test-tenant", "tenant-admin", draft.Version, draft.CatalogData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Menus.PublishCompanyDraft(ctx, "test-tenant", "tenant-admin", retired.Version); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.Menus.GetCompanyProductEmergencyHold(ctx, "test-tenant", "teishoku")
	if err != nil || !state.Active || state.Version != 1 || state.PublishedRevision != 2 {
		t.Fatalf("product retirement erased its active hold history: state=%+v err=%v", state, err)
	}
	cleared, err := fixture.Menus.SetCompanyProductEmergencyHold(ctx, "test-tenant", "teishoku", "tenant-admin", false, "retired item reviewed", 1, "retire-hold-off")
	if err != nil || cleared.Active || cleared.Version != 2 || cleared.PublishedRevision != 2 {
		t.Fatalf("retired product hold could not be explicitly cleared: state=%+v err=%v", cleared, err)
	}
}

func TestPostgresCompanyProductEmergencyHoldReadFailsClosedWhenHoldTableUnavailable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())
	if _, err = fixture.Pool.Exec(ctx, `ALTER TABLE company_product_emergency_holds RENAME TO unavailable_company_product_emergency_holds`); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Menus.ReadStoreMenu(ctx, catalog.StoreScope{TenantID: fixture.TenantID, StoreID: fixture.StoreID}); err == nil {
		t.Fatal("linked menu read succeeded after the authoritative hold table became unavailable")
	}
	if _, err = fixture.Pool.Exec(ctx, `ALTER TABLE unavailable_company_product_emergency_holds RENAME TO company_product_emergency_holds`); err != nil {
		t.Fatalf("restore isolated hold table: %v", err)
	}
}

func TestPostgresCompanyHoldSerializesWithDetachInBothOrders(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())
	companyScope := catalog.StoreScope{TenantID: fixture.TenantID}
	firstStore := catalog.StoreScope{TenantID: fixture.TenantID, StoreID: fixture.StoreID}
	if _, err = fixture.Pool.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name) VALUES ($1,'linked-race','Linked race store')`, fixture.TenantID); err != nil {
		t.Fatal(err)
	}
	var catalogID int64
	if err = fixture.Pool.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind='COMPANY'`, fixture.TenantID).Scan(&catalogID); err != nil {
		t.Fatal(err)
	}

	// A real detachment acquires the company SHARE lock, then waits on the
	// store row. Queue the real hold ON behind that SHARE lock; after the store
	// lock is released, detach must finish before the hold can commit.
	detachFirstLock, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var storeID string
	if err = detachFirstLock.QueryRow(ctx, `SELECT store_id FROM stores WHERE tenant_id=$1 AND store_id=$2 FOR UPDATE`, firstStore.TenantID, firstStore.StoreID).Scan(&storeID); err != nil {
		_ = detachFirstLock.Rollback(ctx)
		t.Fatal(err)
	}
	detachDone := make(chan error, 1)
	go func() {
		_, detachErr := fixture.Menus.DetachStore(ctx, firstStore, "store-manager", 1)
		detachDone <- detachErr
	}()
	if err = waitForCatalogLockWait(ctx, fixture.Pool, "%stores%FOR UPDATE%"); err != nil {
		_ = detachFirstLock.Rollback(ctx)
		t.Fatalf("detach did not reach the held store lock: %v", err)
	}
	holdDone := make(chan error, 1)
	go func() {
		_, holdErr := fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", true, "detach race", 0, "detach-race-first-on")
		holdDone <- holdErr
	}()
	if err = waitForCatalogLockWait(ctx, fixture.Pool, "%menu_catalogs%FOR UPDATE%"); err != nil {
		_ = detachFirstLock.Rollback(ctx)
		t.Fatalf("hold updater did not queue behind prior detach lock: %v", err)
	}
	if err = detachFirstLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-detachDone; err != nil {
		t.Fatalf("detach that linearized before hold failed: %v", err)
	}
	if err = <-holdDone; err != nil {
		t.Fatalf("hold after detach failed: %v", err)
	}
	detachedMenu, err := fixture.Menus.ReadStoreMenu(ctx, firstStore)
	if err != nil {
		t.Fatal(err)
	}
	if product := findHoldTestProduct(detachedMenu.CatalogData, "teishoku"); product.CompanyEmergencyHoldActive || product.CompanyEmergencyHoldVersion != 0 {
		t.Fatalf("hold queued after detach leaked into independent snapshot: %+v", product)
	}

	// In the opposite order, queue DetachStore behind the real ON transition.
	// Once the parent lock is released, the store operation must reread the
	// active hold and reject without changing the new linked store.
	if _, err = fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", false, "clear detach race", 1, "detach-race-off"); err != nil {
		t.Fatal(err)
	}
	holdFirstLock, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = holdFirstLock.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs WHERE catalog_id=$1 FOR UPDATE`, catalogID).Scan(&catalogID); err != nil {
		_ = holdFirstLock.Rollback(ctx)
		t.Fatal(err)
	}
	holdDone = make(chan error, 1)
	go func() {
		_, holdErr := fixture.Menus.SetCompanyProductEmergencyHold(ctx, companyScope.TenantID, "teishoku", "tenant-admin", true, "hold before detach", 2, "detach-race-second-on")
		holdDone <- holdErr
	}()
	if err = waitForCatalogLockWait(ctx, fixture.Pool, "%menu_catalogs%FOR UPDATE%"); err != nil {
		_ = holdFirstLock.Rollback(ctx)
		t.Fatalf("hold request did not queue: %v", err)
	}
	detachDone = make(chan error, 1)
	go func() {
		_, detachErr := fixture.Menus.DetachStore(ctx, catalog.StoreScope{TenantID: fixture.TenantID, StoreID: "linked-race"}, "store-manager", 1)
		detachDone <- detachErr
	}()
	if err = waitForCatalogLockWait(ctx, fixture.Pool, "%menu_catalogs%FOR SHARE%"); err != nil {
		_ = holdFirstLock.Rollback(ctx)
		t.Fatalf("detach request did not queue behind hold: %v", err)
	}
	if err = holdFirstLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-holdDone; err != nil {
		t.Fatalf("hold before detach failed: %v", err)
	}
	if err = <-detachDone; !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("detach queued behind active hold was not rejected: %v", err)
	}
	linkedMenu, err := fixture.Menus.ReadStoreMenu(ctx, catalog.StoreScope{TenantID: fixture.TenantID, StoreID: "linked-race"})
	if err != nil {
		t.Fatal(err)
	}
	if product := findHoldTestProduct(linkedMenu.CatalogData, "teishoku"); !product.CompanyEmergencyHoldActive || product.CompanyEmergencyHoldVersion != 3 {
		t.Fatalf("queued detach changed or bypassed the active hold: %+v", product)
	}
}

func waitForCatalogLockWait(ctx context.Context, pool *pgxpool.Pool, pattern string) error {
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1
			AND EXISTS(SELECT 1 FROM pg_locks held WHERE held.pid=pg_stat_activity.pid
				AND held.relation=to_regclass('menu_catalogs')::oid)
			AND EXISTS(SELECT 1 FROM pg_locks waiting WHERE waiting.pid=pg_stat_activity.pid AND NOT waiting.granted))`, pattern).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for blocked SQL matching %q", pattern)
		case <-ticker.C:
		}
	}
}
