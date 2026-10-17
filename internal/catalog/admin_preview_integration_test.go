package catalog_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	catalog "github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func TestPostgresB2PreviewCheckedPublishAndStorePriceProjection(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())

	scope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
	snapshot, err := fixture.Menus.ReadCompanyPublishedSnapshot(ctx, scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || menuProduct(snapshot.CatalogData, "teishoku").BasePriceMinor != 1100 {
		t.Fatalf("unexpected company snapshot: revision=%d product=%+v", snapshot.Revision, menuProduct(snapshot.CatalogData, "teishoku"))
	}
	draft, err := fixture.Menus.GetCompanyDraft(ctx, scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}

	override, err := fixture.Menus.SetStorePriceOverride(ctx, scope, "p81-b2-test", "teishoku", 1350, 0)
	if err != nil {
		t.Fatal(err)
	}
	storeProjection, err := fixture.Menus.ReadStoreAdminProjection(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	price := findStoreProductPrice(t, storeProjection.ProductPrices, "teishoku")
	assertStorePrice(t, price, 1100, true, 1350, 1350, override.Version)
	if storeProjection.Menu.Mode != "LINKED" || storeProjection.Menu.Revision != 1 {
		t.Fatalf("store projection lost current source identity: mode=%s revision=%d", storeProjection.Menu.Mode, storeProjection.Menu.Revision)
	}

	changed := cloneCatalogData(t, draft.CatalogData)
	menuProduct(changed, "teishoku").BasePriceMinor = 1200
	saved, err := fixture.Menus.SaveCompanyDraft(ctx, scope.TenantID, "p81-b2-test", draft.Version, changed)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := fixture.Menus.PreviewCompanyPublication(ctx, scope.TenantID, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanPublish || len(preview.Issues) != 0 || preview.DraftVersion != saved.Version || preview.PublishedRevision != 1 || preview.LinkedStoreCount != 1 || len(preview.PreviewFingerprint) != 64 {
		t.Fatalf("unexpected safe preview: %+v", preview)
	}
	if unchanged, readErr := fixture.Menus.ReadCompanyPublishedSnapshot(ctx, scope.TenantID); readErr != nil || unchanged.Revision != 1 || menuProduct(unchanged.CatalogData, "teishoku").BasePriceMinor != 1100 {
		t.Fatalf("preview exposed or published draft data: snapshot=%+v err=%v", unchanged, readErr)
	}
	if unchangedStore, readErr := fixture.Menus.ReadStoreAdminProjection(ctx, scope); readErr != nil {
		t.Fatal(readErr)
	} else {
		assertStorePrice(t, findStoreProductPrice(t, unchangedStore.ProductPrices, "teishoku"), 1100, true, 1350, 1350, override.Version)
	}

	result, err := fixture.Menus.PublishCompanyDraftChecked(ctx, scope.TenantID, "p81-b2-test", saved.Version, preview.PreviewFingerprint)
	if err != nil {
		t.Fatalf("checked publish: %v", err)
	}
	if result.Revision != 2 || result.DraftVersion != saved.Version {
		t.Fatalf("unexpected publish result: %+v", result)
	}
	published, err := fixture.Menus.ReadCompanyPublishedSnapshot(ctx, scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != 2 || menuProduct(published.CatalogData, "teishoku").BasePriceMinor != 1200 {
		t.Fatalf("checked publish did not atomically switch snapshot: revision=%d product=%+v", published.Revision, menuProduct(published.CatalogData, "teishoku"))
	}
	storeProjection, err = fixture.Menus.ReadStoreAdminProjection(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	price = findStoreProductPrice(t, storeProjection.ProductPrices, "teishoku")
	assertStorePrice(t, price, 1200, true, 1350, 1350, override.Version)

	reset, err := fixture.Menus.ResetStorePriceOverride(ctx, scope, "p81-b2-test", "teishoku", override.Version)
	if err != nil {
		t.Fatal(err)
	}
	storeProjection, err = fixture.Menus.ReadStoreAdminProjection(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	price = findStoreProductPrice(t, storeProjection.ProductPrices, "teishoku")
	if price.SourceBasePriceMinor != 1200 || price.OverrideActive || price.OverridePriceMinor != nil || price.EffectivePriceMinor != 1200 || price.OverrideVersion != reset.Version {
		t.Fatalf("reset projection did not resolve to current company price: %+v", price)
	}
}

func TestPostgresB2PreviewFingerprintRejectsChangedAvailabilityAndUsesSameGuard(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())

	scope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
	draft, err := fixture.Menus.GetCompanyDraft(ctx, scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	retireRegular := cloneCatalogData(t, draft.CatalogData)
	for i := range menuGroup(retireRegular, "teishoku", "rice").Options {
		option := &menuGroup(retireRegular, "teishoku", "rice").Options[i]
		if option.Key == "regular" {
			option.Active = false
		}
	}
	saved, err := fixture.Menus.SaveCompanyDraft(ctx, scope.TenantID, "p81-b2-race", draft.Version, retireRegular)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := fixture.Menus.PreviewCompanyPublication(ctx, scope.TenantID, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanPublish {
		t.Fatalf("both confirmed choices should make retirement safe before the race: %+v", preview)
	}

	held, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(context.Background())
	var blockerPID int32
	if err = held.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	var lockedStoreID string
	if err = held.QueryRow(ctx, `SELECT store_id FROM stores WHERE tenant_id=$1 AND store_id=$2 FOR UPDATE`, scope.TenantID, scope.StoreID).Scan(&lockedStoreID); err != nil {
		t.Fatal(err)
	}
	publishResult := make(chan error, 1)
	go func() {
		_, publishErr := fixture.Menus.PublishCompanyDraftChecked(ctx, scope.TenantID, "p81-b2-race", saved.Version, preview.PreviewFingerprint)
		publishResult <- publishErr
	}()
	waitForLinkedStoreLockWait(t, ctx, fixture.Pool, blockerPID)
	var oldVersion int64
	if err = held.QueryRow(ctx, `SELECT version FROM store_option_availability WHERE tenant_id=$1 AND store_id=$2
		AND product_key='teishoku' AND group_key='rice' AND option_key='large' FOR UPDATE`, scope.TenantID, scope.StoreID).Scan(&oldVersion); err != nil {
		t.Fatal(err)
	}
	tag, err := held.Exec(ctx, `UPDATE store_option_availability SET status='PAUSED',reason='B2 fingerprint race',updated_by='p81-b2-race',updated_at=clock_timestamp(),version=version+1
		WHERE tenant_id=$1 AND store_id=$2 AND product_key='teishoku' AND group_key='rice' AND option_key='large' AND status='AVAILABLE'`, scope.TenantID, scope.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("pause update affected %d rows, want one", tag.RowsAffected())
	}
	if _, err = held.Exec(ctx, `INSERT INTO menu_admin_audit(tenant_id,store_id,actor,operation,entity_key,before_value,after_value,before_version,after_version)
		VALUES($1,$2,'p81-b2-race','AVAILABILITY_OPTION','teishoku/rice/large','AVAILABLE','PAUSED',$3,$4)`, scope.TenantID, scope.StoreID, oldVersion, oldVersion+1); err != nil {
		t.Fatal(err)
	}
	if err = held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case publishErr := <-publishResult:
		if !errors.Is(publishErr, catalog.ErrStale) {
			t.Fatalf("checked publish error=%v, want ErrStale for changed availability fingerprint", publishErr)
		}
	case <-ctx.Done():
		t.Fatal("checked publish did not finish after store lock release")
	}
	unchanged, err := fixture.Menus.ReadCompanyPublishedSnapshot(ctx, scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 1 {
		t.Fatalf("stale checked publication advanced current revision to %d", unchanged.Revision)
	}

	refreshed, err := fixture.Menus.PreviewCompanyPublication(ctx, scope.TenantID, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.CanPublish || refreshed.PreviewFingerprint == preview.PreviewFingerprint || len(refreshed.Issues) != 1 || refreshed.Issues[0].Code != catalog.PublicationRequiredChoiceShortage || refreshed.Issues[0].AvailableCount != 0 {
		t.Fatalf("fresh preview did not expose the new blocker: %+v", refreshed)
	}
	_, err = fixture.Menus.PublishCompanyDraftChecked(ctx, scope.TenantID, "p81-b2-race", saved.Version, refreshed.PreviewFingerprint)
	var blocked *catalog.PublicationValidationError
	if !errors.As(err, &blocked) {
		t.Fatalf("checked publish with fresh blocked preview returned %v, want PublicationValidationError", err)
	}
	unchanged, err = fixture.Menus.ReadCompanyPublishedSnapshot(ctx, scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 1 {
		t.Fatalf("blocked checked publication advanced current revision to %d", unchanged.Revision)
	}
	_, err = fixture.Menus.PublishCompanyDraft(ctx, scope.TenantID, "p81-b2-compat", saved.Version)
	if !errors.As(err, &blocked) {
		t.Fatalf("compatibility publish bypassed current B1 validator: %v", err)
	}
}

func findStoreProductPrice(t *testing.T, prices []catalog.StoreProductPrice, productKey string) catalog.StoreProductPrice {
	t.Helper()
	for _, price := range prices {
		if price.ProductKey == productKey {
			return price
		}
	}
	t.Fatalf("store price projection missing product %q", productKey)
	return catalog.StoreProductPrice{}
}

func assertStorePrice(t *testing.T, price catalog.StoreProductPrice, sourceBase int64, overrideActive bool, overrideValue, effective int64, version int64) {
	t.Helper()
	if price.SourceBasePriceMinor != sourceBase || price.OverrideActive != overrideActive || price.OverridePriceMinor == nil || *price.OverridePriceMinor != overrideValue || price.EffectivePriceMinor != effective || price.OverrideVersion != version {
		t.Fatalf("unexpected store price projection: %+v", price)
	}
}
