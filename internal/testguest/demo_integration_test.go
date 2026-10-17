package testguest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/docs/demo"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

func TestPostgresShironagasukujiraDemoFixtureIsSyntheticAndHistoricalOnly(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL demo fixture tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	f, err := NewWithOptions(ctx, dsn, Options{
		DemoShironagasukujira: true, CashEnabled: true, CashMode: operations.PayLater,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(context.Background())
	if f.TenantID != "demo-synthetic" || f.StoreID != "demo-shironagasukujira" || f.TableID != "table-05" || f.Locator != "demo-shironagasukujira-table-05" {
		t.Fatalf("demo fixture did not use its fixed synthetic scope: tenant=%s store=%s table=%s locator=%s", f.TenantID, f.StoreID, f.TableID, f.Locator)
	}
	if strings.HasPrefix(f.Schema, "demo-") || f.Schema == "" {
		t.Fatalf("expected a fresh isolated schema, got %q", f.Schema)
	}

	scope := catalog.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}
	menu, err := f.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	products := make(map[string]catalog.Product)
	for _, category := range menu.Categories {
		for _, product := range category.Products {
			products[product.Key] = product
		}
	}
	if len(products) != 9 {
		t.Fatalf("expected only nine historical dine-in items, got %d", len(products))
	}
	for _, key := range []string{
		"lunch_shiro", "lunch_karaage", "lunch_gyoza", "lunch_chickennanban", "lunch_beef_curry", "lunch_saba",
		"dinner_iron_gyoza", "dinner_takana_potato", "dinner_tsukemono",
	} {
		product, ok := products[key]
		if !ok || !product.Selectable || !strings.Contains(product.Description, "DEMO / 歴史資料の参考価格・現行店内価格未確認") {
			t.Fatalf("historical product missing orderability/provenance label: key=%s product=%+v found=%v", key, product, ok)
		}
	}
	evidence, err := demo.LoadShironagasukujira()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range evidence.Items {
		if !item.OrderableInDemo {
			continue
		}
		productKey := strings.ToLower(strings.ReplaceAll(item.DemoKey, "-", "_"))
		product := products[productKey]
		source := evidence.Sources[item.SourceID]
		sourceLabel, ok := shironagasukujiraSourceLabel(item.SourceID)
		if !ok || !strings.Contains(product.Description, sourceLabel) || strings.Contains(product.Description, source.Kind) || strings.Contains(product.Description, item.Notes) {
			t.Fatalf("product lost Japanese source provenance or exposed raw source notes: key=%s source=%+v description=%q", productKey, source, product.Description)
		}
	}
	for _, forbidden := range []string{"dinner_hitokuchi_gyoza", "dinner_teba_gyoza", "dinner_motsu", "drink_beer", "deliv_tartar_bento", "deliv_sicilian"} {
		if _, exists := products[forbidden]; exists {
			t.Fatalf("unpriced or delivery-only reference item entered the catalog: %s", forbidden)
		}
	}
	meal := products["lunch_shiro"]
	if meal.BasePriceMinor != 960 || len(meal.Groups) != 1 || meal.Groups[0].MinSelect != 1 || meal.Groups[0].MaxSelect != 1 || !strings.Contains(meal.Description, "ごはん普通980円・小（100g）960円") {
		t.Fatalf("rice-size pricing was not normalized to required choices: %+v", meal)
	}
	prices := map[string]int64{}
	for _, option := range meal.Groups[0].Options {
		prices[option.Key] = option.PriceDeltaMinor
	}
	if prices["regular"] != 20 || prices["small"] != 0 {
		t.Fatalf("unexpected nonnegative rice adjustment: %+v", prices)
	}

	opened, err := f.Sessions.Open(ctx, tablesession.Scope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}, "demo-fixture")
	if err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, 3)
	for i := range tokens {
		grant, joinErr := f.Sessions.Join(ctx, opened.Session.ID, opened.Session.ID, "")
		if joinErr != nil {
			t.Fatal(joinErr)
		}
		tokens[i] = grant.Token
	}
	for i, key := range []string{"dinner_hitokuchi_gyoza", "deliv_tartar_bento"} {
		_, mutateErr := f.Orders.Mutate(ctx, ordering.Mutation{
			SessionID: opened.Session.ID, GrantToken: tokens[i], ExpectedGeneration: 1, ExpectedVersion: 1,
			IdempotencyKey: "demo-excluded-" + key, Kind: ordering.AddLine, ProductKey: key, Quantity: 1,
		})
		if !errors.Is(mutateErr, ordering.ErrUnavailable) {
			t.Fatalf("non-orderable evidence item %s was accepted: %v", key, mutateErr)
		}
	}
	cart, err := f.Orders.Mutate(ctx, ordering.Mutation{
		SessionID: opened.Session.ID, GrantToken: tokens[0], ExpectedGeneration: 1, ExpectedVersion: 1,
		IdempotencyKey: "demo-orderable-meal", Kind: ordering.AddLine, ProductKey: "lunch_shiro", Quantity: 1,
		Options: []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}},
	})
	if err != nil || cart.Version != 2 || len(cart.Lines) != 1 {
		t.Fatalf("could not add the historical orderable meal: cart=%+v err=%v", cart, err)
	}
	preview, err := f.Orders.Preview(ctx, opened.Session.ID, tokens[0], cart.Generation, cart.Version)
	if err != nil || preview.Quote.Tax.Total().GrossJPY != 980 {
		t.Fatalf("demo shared-cart preview has wrong historical total: preview=%+v err=%v", preview, err)
	}

	start := make(chan struct{})
	type commitResult struct {
		order ordering.CommitResult
		err   error
	}
	results := make(chan commitResult, len(tokens))
	for i, token := range tokens {
		go func(i int, token string) {
			<-start
			result, commitErr := f.Orders.Commit(ctx, ordering.CommitRequest{
				SessionID: opened.Session.ID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
				IdempotencyKey: fmt.Sprintf("demo-commit-%d", i), PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash,
			})
			results <- commitResult{order: result, err: commitErr}
		}(i, token)
	}
	close(start)
	var committed ordering.Order
	for range tokens {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent demo commit: %v", result.err)
		}
		if result.order.Order.ID == "" || len(result.order.Order.Quote.Lines) != 1 || result.order.Order.Quote.Lines[0].BaseGrossJPY != 960 || result.order.Order.Quote.Tax.Total().GrossJPY != 980 {
			t.Fatalf("demo order lost its immutable historical quote: %+v", result.order.Order)
		}
		if committed.ID == "" {
			committed = result.order.Order
		} else if result.order.Order.ID != committed.ID {
			t.Fatalf("same generation produced multiple immutable orders: %s and %s", committed.ID, result.order.Order.ID)
		}
	}

	at := time.Now()
	for key, expectedGross := range map[string]int64{"regular": 980, "small": 960} {
		quote, quoteErr := f.Menus.QuoteDineIn(ctx, scope, at, []catalog.MenuLine{{
			LineID: "demo-line", ProductKey: "lunch_shiro", Options: []catalog.OptionSelection{{GroupKey: "rice", OptionKey: key}}, Quantity: 1,
		}})
		if quoteErr != nil {
			t.Fatalf("quote %s historical rice choice: %v", key, quoteErr)
		}
		if len(quote.Lines) != 1 || quote.Lines[0].BaseGrossJPY != 960 || quote.Lines[0].Options[0].PriceGrossJPY != prices[key] || quote.Tax.Total().GrossJPY != expectedGross {
			t.Fatalf("historical rice quote mismatch for %s: line=%+v total=%+v", key, quote.Lines, quote.Tax.Total())
		}
	}

	if _, err = f.Menus.ReadStoreMenu(ctx, catalog.StoreScope{TenantID: "foreign-tenant", StoreID: f.StoreID}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("fixture leaked its store into another tenant: %v", err)
	}
	var stripeMerchants, orders, attempts int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM store_stripe_merchants`).Scan(&stripeMerchants); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM table_orders`).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if stripeMerchants != 0 || orders != 1 || attempts != 0 {
		t.Fatalf("demo fixture initialized real payment/order state: merchants=%d orders=%d attempts=%d", stripeMerchants, orders, attempts)
	}
	var paymentStatus, cashMode string
	if err = f.Pool.QueryRow(ctx, `SELECT payment_status,cash_mode FROM operation_order_state WHERE order_id=$1`, committed.ID).Scan(&paymentStatus, &cashMode); err != nil {
		t.Fatal(err)
	}
	if paymentStatus != "UNPAID" || cashMode != "PAY_LATER" {
		t.Fatalf("demo did not preserve synthetic pay-later semantics: payment=%s mode=%s", paymentStatus, cashMode)
	}
	cashSettings, err := f.Operations.ReadCashSettings(ctx, operations.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID})
	if err != nil || !cashSettings.Enabled || cashSettings.Mode != operations.PayLater {
		t.Fatalf("synthetic demo cash settings not explicit: %+v err=%v", cashSettings, err)
	}
}

func TestShironagasukujiraDemoRequiresExplicitSyntheticCashConfig(t *testing.T) {
	for _, options := range []Options{
		{DemoShironagasukujira: true, CashMode: operations.PayLater},
		{DemoShironagasukujira: true, CashEnabled: true, CashMode: operations.Prepaid},
	} {
		if _, err := NewWithOptions(context.Background(), "", options); err == nil {
			t.Fatalf("unsafe demo fixture options accepted: %+v", options)
		}
	}
}
