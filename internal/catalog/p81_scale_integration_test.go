package catalog_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

// This acceptance slice exercises P81 sequence A through public catalog and
// ordering service APIs. It intentionally does not assert P81 D emergency-hold
// behavior, which is a separate future sequence item.
func TestPostgresP81CompanyCatalogScaleAcceptance(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())

	// The fixture provides one existing linked store. Add 49 linked stores, one
	// detached candidate and a second tenant so the test covers the P81 A scale
	// and scope boundaries using the migrated PostgreSQL schema.
	if _, err = fixture.Pool.Exec(ctx, `
		INSERT INTO tenants(tenant_id,name) VALUES ('other-tenant','Other tenant');
		INSERT INTO stores(tenant_id,store_id,name)
		SELECT 'test-tenant',format('linked-%s',lpad(n::text,2,'0')),format('Linked %s',n)
		FROM generate_series(2,50) AS n;
		INSERT INTO stores(tenant_id,store_id,name) VALUES
			('test-tenant','detached-store','Detached store'),
			('other-tenant','other-store','Other tenant store');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES
			('other-tenant','other-store','other-table','Other table','other-locator')`); err != nil {
		t.Fatal(err)
	}

	companyScope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
	secondLinkedScope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "linked-02"}
	detachedScope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "detached-store"}
	otherScope := catalog.StoreScope{TenantID: "other-tenant", StoreID: "other-store"}

	// Store-specific absolute prices must remain in force when the company
	// pointer advances. Detaching copies the current effective price into the
	// independent store-owned snapshot.
	if _, err = fixture.Menus.SetStorePriceOverride(ctx, companyScope, "p81-acceptance", "teishoku", 1350, 0); err != nil {
		t.Fatalf("set first linked override: %v", err)
	}
	if _, err = fixture.Menus.SetStorePriceOverride(ctx, secondLinkedScope, "p81-acceptance", "teishoku", 1425, 0); err != nil {
		t.Fatalf("set second linked override: %v", err)
	}
	if _, err = fixture.Menus.SetStorePriceOverride(ctx, detachedScope, "p81-acceptance", "teishoku", 1550, 0); err != nil {
		t.Fatalf("set detached candidate override: %v", err)
	}
	if _, err = fixture.Menus.DetachStore(ctx, detachedScope, "p81-acceptance", 1); err != nil {
		t.Fatalf("detach candidate store from current published revision: %v", err)
	}

	otherData := catalog.CatalogData{Categories: []catalog.Category{{Key: "meals", Name: "Other meals", Products: []catalog.Product{{
		Key: "teishoku", Name: "Other tenant meal", BasePriceMinor: 777, Active: true,
	}}}}}
	otherDraft, err := fixture.Menus.SaveCompanyDraft(ctx, "other-tenant", "p81-acceptance", 0, otherData)
	if err != nil {
		t.Fatalf("save other tenant company draft: %v", err)
	}
	if _, err = fixture.Menus.PublishCompanyDraft(ctx, "other-tenant", "p81-acceptance", otherDraft.Version); err != nil {
		t.Fatalf("publish other tenant company catalog: %v", err)
	}

	// Commit one real order from the public ordering service before updating the
	// company catalog. It is the historical snapshot oracle for quote and tax.
	grant, err := fixture.Sessions.DirectJoin(ctx, fixture.Locator, "")
	if err != nil {
		t.Fatalf("join linked table: %v", err)
	}
	cart, err := fixture.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatalf("read initial cart: %v", err)
	}
	cart, err = fixture.Orders.Mutate(ctx, ordering.Mutation{
		SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation,
		ExpectedVersion: cart.Version, IdempotencyKey: "p81-a-add", Kind: ordering.AddLine,
		ProductKey: "teishoku", Quantity: 1,
		Options: []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}},
	})
	if err != nil {
		t.Fatalf("add published product: %v", err)
	}
	preview, err := fixture.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatalf("preview linked order: %v", err)
	}
	committed, err := fixture.Orders.Commit(ctx, ordering.CommitRequest{
		SessionID: grant.Session.ID, GrantToken: grant.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "p81-a-commit", PreviewFingerprint: preview.Fingerprint,
		PaymentMethod: ordering.Cash,
	})
	if err != nil {
		t.Fatalf("commit historical order: %v", err)
	}
	if committed.Order.Quote.Lines[0].BaseGrossJPY != 1350 || committed.Order.Quote.Tax.Total().GrossJPY != 1350 {
		t.Fatalf("order did not snapshot the linked override price and gross tax base: %+v", committed.Order.Quote)
	}
	var originalQuoteSnapshot, originalTaxSnapshot []byte
	if err = fixture.Pool.QueryRow(ctx, `SELECT quote_snapshot,tax_snapshot FROM table_orders WHERE order_id=$1`, committed.Order.ID).
		Scan(&originalQuoteSnapshot, &originalTaxSnapshot); err != nil {
		t.Fatalf("read committed snapshots: %v", err)
	}

	draft, err := fixture.Menus.GetCompanyDraft(ctx, "test-tenant")
	if err != nil {
		t.Fatalf("read company draft: %v", err)
	}
	for ci := range draft.Categories {
		for pi := range draft.Categories[ci].Products {
			if draft.Categories[ci].Products[pi].Key == "teishoku" {
				draft.Categories[ci].Products[pi].Name = "東島の定食 改訂"
				draft.Categories[ci].Products[pi].BasePriceMinor = 1700
			}
		}
	}
	draft, err = fixture.Menus.SaveCompanyDraft(ctx, "test-tenant", "p81-acceptance", draft.Version, draft.CatalogData)
	if err != nil {
		t.Fatalf("save company draft: %v", err)
	}
	if draft.Version != 2 {
		t.Fatalf("company draft did not advance exactly once: version=%d", draft.Version)
	}

	// All fifty still-LINKED stores must keep seeing the old published revision
	// while a valid draft remains unpublished.
	linkedScopes := []catalog.StoreScope{companyScope, secondLinkedScope}
	for n := 3; n <= 50; n++ {
		linkedScopes = append(linkedScopes, catalog.StoreScope{TenantID: "test-tenant", StoreID: fmt.Sprintf("linked-%02d", n)})
	}
	if len(linkedScopes) != 50 {
		t.Fatalf("fixture has %d linked stores, want 50", len(linkedScopes))
	}
	for _, scope := range linkedScopes {
		menu, readErr := fixture.Menus.ReadStoreMenu(ctx, scope)
		if readErr != nil {
			t.Fatalf("read pre-publish menu for %s: %v", scope.StoreID, readErr)
		}
		product := findProduct(t, menu, "teishoku")
		if menu.Mode != "LINKED" || menu.Revision != 1 || product.Name != "東島の定食" {
			t.Fatalf("unpublished draft leaked to %s: mode=%s revision=%d product=%+v", scope.StoreID, menu.Mode, menu.Revision, product)
		}
	}
	guestBeforePublish, err := fixture.Menus.ReadPublishedByTableLocator(ctx, fixture.Locator)
	if err != nil {
		t.Fatalf("read guest-visible company menu before publication: %v", err)
	}
	if guestBeforePublish.Revision != 1 || findProduct(t, guestBeforePublish, "teishoku").Name != "東島の定食" {
		t.Fatalf("guest read exposed an unpublished company draft: %+v", guestBeforePublish)
	}
	if _, err = fixture.Menus.PublishCompanyDraft(ctx, "test-tenant", "p81-acceptance", draft.Version); err != nil {
		t.Fatalf("publish validated company draft: %v", err)
	}
	guestAfterPublish, err := fixture.Menus.ReadPublishedByTableLocator(ctx, fixture.Locator)
	if err != nil {
		t.Fatalf("read guest-visible company menu after publication: %v", err)
	}
	if guestAfterPublish.Revision != 2 || findProduct(t, guestAfterPublish, "teishoku").Name != "東島の定食 改訂" {
		t.Fatalf("guest read did not follow the atomically published pointer: %+v", guestAfterPublish)
	}

	// Publication converges every LINKED store on one pointer while preserving
	// each active absolute override. The DETACHED copy and second tenant remain
	// on their independently owned snapshots.
	var linkedStoreCount, detachedBindingCount int
	if err = fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM stores WHERE tenant_id='test-tenant'`).Scan(&linkedStoreCount); err != nil {
		t.Fatal(err)
	}
	if err = fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM store_menu_bindings WHERE tenant_id='test-tenant' AND mode='DETACHED'`).Scan(&detachedBindingCount); err != nil {
		t.Fatal(err)
	}
	if linkedStoreCount != 51 || detachedBindingCount != 1 {
		t.Fatalf("company scope changed during test: stores=%d detached=%d, want 51 and 1", linkedStoreCount, detachedBindingCount)
	}
	for _, scope := range linkedScopes {
		menu, readErr := fixture.Menus.ReadStoreMenu(ctx, scope)
		if readErr != nil {
			t.Fatalf("read post-publish menu for %s: %v", scope.StoreID, readErr)
		}
		product := findProduct(t, menu, "teishoku")
		if menu.Mode != "LINKED" || menu.Revision != 2 || product.Name != "東島の定食 改訂" {
			t.Fatalf("linked store did not converge at revision 2 (%s): %+v product=%+v", scope.StoreID, menu, product)
		}
		want := int64(1700)
		if scope.StoreID == "test-store" {
			want = 1350
		} else if scope.StoreID == "linked-02" {
			want = 1425
		}
		if product.EffectivePriceMinor != want {
			t.Fatalf("linked store %s effective price=%d, want %d", scope.StoreID, product.EffectivePriceMinor, want)
		}
	}
	detached, err := fixture.Menus.ReadStoreMenu(ctx, detachedScope)
	if err != nil {
		t.Fatalf("read detached store after company publication: %v", err)
	}
	if detached.Mode != "DETACHED" || detached.Revision != 1 {
		t.Fatalf("company publication changed detached source: mode=%s revision=%d", detached.Mode, detached.Revision)
	}
	detachedProduct := findProduct(t, detached, "teishoku")
	if detachedProduct.Name != "東島の定食" || detachedProduct.EffectivePriceMinor != 1550 {
		t.Fatalf("detached content or copied override changed: %+v", detachedProduct)
	}
	otherMenu, err := fixture.Menus.ReadStoreMenu(ctx, otherScope)
	if err != nil {
		t.Fatalf("read other tenant menu: %v", err)
	}
	otherProduct := findProduct(t, otherMenu, "teishoku")
	if otherMenu.Revision != 1 || otherProduct.Name != "Other tenant meal" || otherProduct.EffectivePriceMinor != 777 {
		t.Fatalf("test tenant publication crossed tenant scope: menu=%+v product=%+v", otherMenu, otherProduct)
	}

	orders, err := fixture.Orders.ListOrders(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatalf("read historical order after publication: %v", err)
	}
	if len(orders) != 1 || !reflect.DeepEqual(orders[0], committed.Order) {
		t.Fatalf("company publication changed immutable order snapshot: before=%+v after=%+v", committed.Order, orders)
	}
	var laterQuoteSnapshot, laterTaxSnapshot []byte
	if err = fixture.Pool.QueryRow(ctx, `SELECT quote_snapshot,tax_snapshot FROM table_orders WHERE order_id=$1`, committed.Order.ID).
		Scan(&laterQuoteSnapshot, &laterTaxSnapshot); err != nil {
		t.Fatalf("read historical snapshots after publication: %v", err)
	}
	if !bytes.Equal(originalQuoteSnapshot, laterQuoteSnapshot) || !bytes.Equal(originalTaxSnapshot, laterTaxSnapshot) {
		t.Fatalf("publication rewrote saved quote/tax snapshot: quoteChanged=%v taxChanged=%v", !bytes.Equal(originalQuoteSnapshot, laterQuoteSnapshot), !bytes.Equal(originalTaxSnapshot, laterTaxSnapshot))
	}
	if _, err = fixture.Menus.PublishCompanyDraft(ctx, "test-tenant", "p81-acceptance", draft.Version); !errors.Is(err, catalog.ErrStale) {
		t.Fatalf("stale second publish should not advance pointer: %v", err)
	}
}

func findProduct(t *testing.T, menu catalog.PublishedMenu, productKey string) catalog.Product {
	t.Helper()
	for _, category := range menu.Categories {
		for _, product := range category.Products {
			if product.Key == productKey {
				return product
			}
		}
	}
	t.Fatalf("product %q missing from menu revision %d", productKey, menu.Revision)
	return catalog.Product{}
}
