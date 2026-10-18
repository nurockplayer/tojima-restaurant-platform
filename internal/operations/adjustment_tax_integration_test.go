package operations_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func TestPostgresCashAdjustmentRebuildsReceiptTaxFromHistoricalMixedRateSnapshots(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(context.Background())
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: grant.Session.Scope.TableID}
	store := catalog.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}
	setStorePrice(t, ctx, f, store, "tea", 6, 0)
	setStorePrice(t, ctx, f, store, "teishoku", 6, 0)

	// Order A has two 10% lines at ¥6 each. Then advance the fixture's legal
	// schedule for this date so the second order snapshots an 8% DINE_IN rule.
	cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: "mixed-a-tea", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	cart, err = f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: cart.Version, IdempotencyKey: "mixed-a-meal", Kind: ordering.AddLine, ProductKey: "teishoku", Quantity: 1, Options: []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}}})
	if err != nil {
		t.Fatal(err)
	}
	previewA, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	commitA, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "mixed-a-commit", PreviewFingerprint: previewA.Fingerprint, PaymentMethod: ordering.Cash})
	if err != nil {
		t.Fatal(err)
	}
	installEightPercentRuleForDate(t, ctx, f, commitA.Order.Quote.Tax.Date())
	commitB := commitCashOrder(t, ctx, f, grant, 2, "mixed-b")
	if commitA.Order.Quote.Tax.Total().GrossJPY != 12 || commitA.Order.Quote.Tax.Total().TaxJPY != 1 || commitB.Quote.Tax.Total().GrossJPY != 6 {
		t.Fatalf("unexpected fixture snapshots A=%+v B=%+v", commitA.Order.Quote.Tax.Total(), commitB.Quote.Tax.Total())
	}
	orderBRate, orderBDenominator := commitB.Quote.Tax.Lines()[0].Rule().Rate()
	if orderBRate != 8 || orderBDenominator != 100 {
		t.Fatalf("second order did not use synthetic historical 8%% rule: %+v", commitB.Quote.Tax.Lines()[0].Rule())
	}

	for i, orderID := range []string{commitA.Order.ID, commitB.ID} {
		if _, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: []string{"accept-a", "accept-b"}[i]}); err != nil {
			t.Fatal(err)
		}
	}
	var originalA, originalB string
	for _, row := range []struct {
		id     string
		target *string
	}{{commitA.Order.ID, &originalA}, {commitB.ID, &originalB}} {
		if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(o)::text FROM table_orders o WHERE order_id=$1`, row.id).Scan(row.target); err != nil {
			t.Fatal(err)
		}
	}
	cashPreview, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cashPreview.DueJPY != 18 || len(cashPreview.OrderIDs) != 2 || cashPreview.Tax.Total.TaxJPY != 1 || len(cashPreview.Tax.Groups) != 2 {
		t.Fatalf("original mixed-rate receipt preview=%+v", cashPreview)
	}
	var tenRateGross int64
	for _, group := range cashPreview.Tax.Groups {
		if group.RateNumerator*10 == group.RateDenominator {
			if group.Totals.GrossJPY != 12 || group.Totals.TaxJPY != 1 {
				t.Fatalf("10%% group did not floor once across its two lines: %+v", group)
			}
			tenRateGross = group.Totals.GrossJPY
		}
		if group.RateNumerator*12 == group.RateDenominator && (group.Totals.GrossJPY != 6 || group.Totals.TaxJPY != 0) {
			t.Fatalf("8%% group=%+v", group)
		}
	}
	if tenRateGross != 12 {
		t.Fatalf("missing normalized 10%% group: %+v", cashPreview.Tax.Groups)
	}
	receipt, err := f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: cashPreview.Fingerprint, TenderedJPY: 20, RegisterSessionRef: "shift-mixed-tax", IdempotencyKey: "settle-mixed-tax"})
	if err != nil {
		t.Fatal(err)
	}
	var receiptBefore string
	if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(c)::text FROM cash_settlements c WHERE receipt_id=$1`, receipt.ID).Scan(&receiptBefore); err != nil {
		t.Fatal(err)
	}
	var paidA string
	if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(st)::text FROM operation_order_state st WHERE order_id=$1`, commitA.Order.ID).Scan(&paidA); err != nil {
		t.Fatal(err)
	}

	// Change both the active catalog prices and legal schedule after payment.
	// The adjustment must use the embedded order tax rules and unit prices.
	setStorePrice(t, ctx, f, store, "tea", 900, 1)
	setStorePrice(t, ctx, f, store, "teishoku", 1200, 1)
	if _, err = f.Pool.Exec(ctx, `UPDATE tax_rate_schedule SET rate_numerator=20 WHERE tax_rule_id='jp-standard-10-2019-10-01';
		UPDATE tax_rate_schedule SET rate_numerator=16 WHERE tax_rule_id='p82-test-dine-in-8-on-date'`); err != nil {
		t.Fatal(err)
	}

	detail, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	stateA := findAdjustmentTestOrder(t, detail.Orders, commitA.Order.ID)
	request := operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: commitA.Order.ID, Actor: "manager", ExpectedVersion: stateA.Version, IdempotencyKey: "remove-one-10pct-line", Reason: "guest requested one less", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}, CashHandBackConfirmed: true}
	if _, err = f.Operations.PreviewAdjustment(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: commitA.Order.ID, Actor: "manager", ExpectedVersion: stateA.Version, Reason: "over-cancel check", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 2}}}); !errors.Is(err, operations.ErrInvalid) {
		t.Fatalf("over-cancel preview accepted: %v", err)
	}
	adjusted, err := f.Operations.AdjustOrder(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if adjusted.TotalGrossJPY != 12 || adjusted.EffectiveTotalGrossJPY != 6 || adjusted.PaymentStatus != operations.Paid || len(adjusted.Refunds) != 1 || adjusted.Refunds[0].AmountJPY != 6 || adjusted.Refunds[0].Status != "SUCCEEDED" {
		t.Fatalf("partial CASH adjustment=%+v", adjusted)
	}
	request.ExpectedVersion = adjusted.Version
	request.IdempotencyKey = "over-cancel-after-adjustment"
	request.Lines = []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 2}}
	if _, err = f.Operations.PreviewAdjustment(ctx, request); !errors.Is(err, operations.ErrInvalid) {
		t.Fatalf("over-cancel after adjustment accepted: %v", err)
	}

	var receiptAfter, orderAAfter, orderBAfter, paidAAfter string
	if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(c)::text FROM cash_settlements c WHERE receipt_id=$1`, receipt.ID).Scan(&receiptAfter); err != nil || receiptAfter != receiptBefore {
		t.Fatalf("original receipt changed: before=%s after=%s err=%v", receiptBefore, receiptAfter, err)
	}
	for _, row := range []struct {
		id, before string
		target     *string
	}{{commitA.Order.ID, originalA, &orderAAfter}, {commitB.ID, originalB, &orderBAfter}} {
		if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(o)::text FROM table_orders o WHERE order_id=$1`, row.id).Scan(row.target); err != nil || *row.target != row.before {
			t.Fatalf("historical order %s changed: before=%s after=%s err=%v", row.id, row.before, *row.target, err)
		}
	}
	if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(st)::text FROM operation_order_state st WHERE order_id=$1`, commitA.Order.ID).Scan(&paidAAfter); err != nil {
		t.Fatal(err)
	}
	var beforeState, afterState map[string]any
	if err = json.Unmarshal([]byte(paidA), &beforeState); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(paidAAfter), &afterState); err != nil {
		t.Fatal(err)
	}
	if afterState["payment_status"] != "PAID" || afterState["effective_total_gross_jpy"].(float64) != 6 || beforeState["payment_status"] != "PAID" {
		t.Fatalf("payment fact/effective amount mismatch before=%v after=%v", beforeState, afterState)
	}

	var effectiveInvoiceJSON string
	if err = f.Pool.QueryRow(ctx, `SELECT effective_tax_invoice::text FROM cash_refund_ledger WHERE receipt_id=$1 AND order_id=$2`, receipt.ID, commitA.Order.ID).Scan(&effectiveInvoiceJSON); err != nil {
		t.Fatal(err)
	}
	var effectiveInvoice tax.InvoiceRecord
	if err = json.Unmarshal([]byte(effectiveInvoiceJSON), &effectiveInvoice); err != nil {
		t.Fatalf("decode rebuilt historical invoice: %v", err)
	}
	if err = effectiveInvoice.Validate(); err != nil {
		t.Fatalf("rebuilt invoice invalid: %v", err)
	}
	if effectiveInvoice.Total.GrossJPY != 12 || effectiveInvoice.Total.TaxJPY != 0 || len(effectiveInvoice.Orders) != 2 || len(effectiveInvoice.Groups) != 2 {
		t.Fatalf("receipt-boundary recomputation=%+v", effectiveInvoice)
	}
	for _, group := range effectiveInvoice.Groups {
		if group.Totals.GrossJPY != 6 || group.Totals.TaxJPY != 0 {
			t.Fatalf("updated receipt did not floor once per retained normalized rate: %+v", group)
		}
	}
	var hasOriginalTen, hasOriginalEight bool
	for _, order := range effectiveInvoice.Orders {
		for _, line := range order.Snapshot.Lines {
			if line.Rule.RateNumerator == 10 && line.Rule.RateDenominator == 100 && line.GrossJPY == 6 {
				hasOriginalTen = true
			}
			if line.Rule.RateNumerator == 8 && line.Rule.RateDenominator == 100 && line.GrossJPY == 6 {
				hasOriginalEight = true
			}
		}
	}
	if !hasOriginalTen || !hasOriginalEight {
		t.Fatalf("effective invoice used changed rates or lost order provenance: %+v", effectiveInvoice.Orders)
	}
	var invalidLedgerCount int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM cash_refund_ledger l JOIN cash_settlement_allocations a USING(receipt_id,order_id)
		WHERE l.receipt_id=$1 AND (l.amount_jpy<=0 OR l.amount_jpy>a.amount_jpy)`, receipt.ID).Scan(&invalidLedgerCount); err != nil || invalidLedgerCount != 0 {
		t.Fatalf("negative or over-refund ledger rows=%d err=%v", invalidLedgerCount, err)
	}
	var refundedJPY, paidJPY int64
	if err = f.Pool.QueryRow(ctx, `SELECT coalesce(sum(l.amount_jpy),0), max(a.amount_jpy)
		FROM cash_refund_ledger l JOIN cash_settlement_allocations a USING(receipt_id,order_id)
		WHERE l.receipt_id=$1 AND l.order_id=$2`, receipt.ID, commitA.Order.ID).Scan(&refundedJPY, &paidJPY); err != nil || refundedJPY != 6 || refundedJPY > paidJPY {
		t.Fatalf("refund sum %d exceeds or differs from original allocation %d: %v", refundedJPY, paidJPY, err)
	}
}

func installEightPercentRuleForDate(t *testing.T, ctx context.Context, f *testguest.Fixture, date time.Time) {
	t.Helper()
	if _, err := f.Pool.Exec(ctx, `UPDATE tax_rate_schedule SET effective_to=$1 WHERE tax_rule_id='jp-standard-10-2019-10-01'`, date); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Pool.Exec(ctx, `INSERT INTO tax_rate_schedule(tax_rule_id,tax_class,rate_numerator,rate_denominator,effective_from,legal_source,legal_status,rule_version)
		VALUES('p82-test-dine-in-8-on-date',$2,8,100,$1,'synthetic PostgreSQL adjustment-tax integration fixture','ENACTED','p82-integration-v1')`, date, tax.ClassDineIn); err != nil {
		t.Fatal(err)
	}
}

func findAdjustmentTestOrder(t *testing.T, orders []operations.OrderState, id string) operations.OrderState {
	t.Helper()
	for _, order := range orders {
		if order.ID == id {
			return order
		}
	}
	t.Fatalf("order %q missing from session", id)
	return operations.OrderState{}
}
