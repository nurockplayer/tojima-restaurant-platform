package catalog_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func TestPostgresCompanyHoldPreservesExistingCashOrderAndKDSCard(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	cart, err := f.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	cart, err = f.Orders.Mutate(ctx, ordering.Mutation{
		SessionID: grant.Session.ID, GrantToken: grant.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "d1-existing-kds-add-tea", Kind: ordering.AddLine,
		ProductKey: "tea", Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{
		SessionID: grant.Session.ID, GrantToken: grant.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "d1-existing-kds-commit", PreviewFingerprint: preview.Fingerprint,
		PaymentMethod: ordering.Cash,
	})
	if err != nil || committed.Outcome != ordering.Committed {
		t.Fatalf("commit real CASH order: result=%+v err=%v", committed, err)
	}
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
	accepted, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{
		Scope: scope, SessionID: grant.Session.ID, OrderID: committed.Order.ID,
		Actor: "test-cashier", ExpectedVersion: 1, IdempotencyKey: "d1-existing-kds-accept",
	})
	if err != nil || accepted.FulfillmentStatus != operations.Accepted {
		t.Fatalf("accept CASH order for KDS: state=%+v err=%v", accepted, err)
	}
	cardsBefore, err := f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID})
	if err != nil || len(cardsBefore) != 1 || cardsBefore[0].ID != committed.Order.ID {
		t.Fatalf("fixture must have one existing KDS card before hold: cards=%+v err=%v", cardsBefore, err)
	}

	var quoteBefore, taxBefore, orderBefore, operationBefore, cardBefore string
	var settlementsBefore, outboxBefore int
	if err = f.Pool.QueryRow(ctx, `SELECT quote_snapshot::text,tax_snapshot::text FROM table_orders WHERE order_id=$1`, committed.Order.ID).Scan(&quoteBefore, &taxBefore); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'generation',generation,'payment_method',payment_method,
		'requires_staff_acceptance',requires_staff_acceptance,'created_at',created_at)::text
		FROM table_orders WHERE order_id=$1`, committed.Order.ID).Scan(&orderBefore); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'payment_status',payment_status,'cash_mode',cash_mode,'fulfillment_status',fulfillment_status,
		'staff_accepted',staff_accepted,'operational_hold',operational_hold,
		'payment_review_required',payment_review_required,'version',version)::text
		FROM operation_order_state WHERE order_id=$1`, committed.Order.ID).Scan(&operationBefore); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT released_at::text FROM operation_kds_cards WHERE order_id=$1`, committed.Order.ID).Scan(&cardBefore); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM cash_settlements WHERE session_id=$1`, grant.Session.ID).Scan(&settlementsBefore); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_outbox WHERE order_id=$1`, committed.Order.ID).Scan(&outboxBefore); err != nil {
		t.Fatal(err)
	}

	if _, err = f.Menus.SetCompanyProductEmergencyHold(ctx, f.TenantID, "tea", "tenant-admin", true, "safety review", 0, "d1-existing-kds-hold-on"); err != nil {
		t.Fatalf("activate hold after order was released: %v", err)
	}
	if _, err = f.Menus.SetCompanyProductEmergencyHold(ctx, f.TenantID, "tea", "tenant-admin", false, "review cleared", 1, "d1-existing-kds-hold-off"); err != nil {
		t.Fatalf("clear hold after order was released: %v", err)
	}

	var quoteAfter, taxAfter, orderAfter, operationAfter, cardAfter string
	var settlementsAfter, outboxAfter int
	if err = f.Pool.QueryRow(ctx, `SELECT quote_snapshot::text,tax_snapshot::text FROM table_orders WHERE order_id=$1`, committed.Order.ID).Scan(&quoteAfter, &taxAfter); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'generation',generation,'payment_method',payment_method,
		'requires_staff_acceptance',requires_staff_acceptance,'created_at',created_at)::text
		FROM table_orders WHERE order_id=$1`, committed.Order.ID).Scan(&orderAfter); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'payment_status',payment_status,'cash_mode',cash_mode,'fulfillment_status',fulfillment_status,
		'staff_accepted',staff_accepted,'operational_hold',operational_hold,
		'payment_review_required',payment_review_required,'version',version)::text
		FROM operation_order_state WHERE order_id=$1`, committed.Order.ID).Scan(&operationAfter); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT released_at::text FROM operation_kds_cards WHERE order_id=$1`, committed.Order.ID).Scan(&cardAfter); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM cash_settlements WHERE session_id=$1`, grant.Session.ID).Scan(&settlementsAfter); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_outbox WHERE order_id=$1`, committed.Order.ID).Scan(&outboxAfter); err != nil {
		t.Fatal(err)
	}
	cardsAfter, err := f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID})
	if err != nil || len(cardsAfter) != 1 || cardsAfter[0].ID != committed.Order.ID {
		t.Fatalf("company hold changed the existing KDS card: cards=%+v err=%v", cardsAfter, err)
	}
	if quoteAfter != quoteBefore || taxAfter != taxBefore || orderAfter != orderBefore || operationAfter != operationBefore || cardAfter != cardBefore || settlementsAfter != settlementsBefore || outboxAfter != outboxBefore {
		t.Fatalf("company hold changed a committed CASH order: quote=%t tax=%t order=%t operation=%t card=%t settlements=%d→%d outbox=%d→%d",
			quoteAfter == quoteBefore, taxAfter == taxBefore, orderAfter == orderBefore,
			operationAfter == operationBefore, cardAfter == cardBefore, settlementsBefore, settlementsAfter, outboxBefore, outboxAfter)
	}
}

func TestPostgresCompanyHoldReadFailureRejectsCommitWithoutMutation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	cart, err := f.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	cart, err = f.Orders.Mutate(ctx, ordering.Mutation{
		SessionID: grant.Session.ID, GrantToken: grant.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "d1-hold-read-fail-add-tea", Kind: ordering.AddLine,
		ProductKey: "tea", Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil || preview.Fingerprint == "" {
		t.Fatalf("prepare valid preview before hold-table outage: preview=%+v err=%v", preview, err)
	}

	if _, err = f.Pool.Exec(ctx, `ALTER TABLE company_product_emergency_holds RENAME TO unavailable_company_product_emergency_holds`); err != nil {
		t.Fatal(err)
	}
	commit, commitErr := f.Orders.Commit(ctx, ordering.CommitRequest{
		SessionID: grant.Session.ID, GrantToken: grant.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "d1-hold-read-fail-commit", PreviewFingerprint: preview.Fingerprint,
		PaymentMethod: ordering.Cash,
	})
	_, restoreErr := f.Pool.Exec(ctx, `ALTER TABLE unavailable_company_product_emergency_holds RENAME TO company_product_emergency_holds`)
	if restoreErr != nil {
		t.Fatalf("restore isolated hold table: %v", restoreErr)
	}
	if commitErr == nil {
		t.Fatalf("commit succeeded while authoritative company holds were unavailable: result=%+v", commit)
	}
	if errors.Is(commitErr, ordering.ErrUnavailable) {
		t.Logf("commit failed closed with unavailable error: %v", commitErr)
	}

	var orderCount, operationCount, settlementCount, stripeAttemptCount int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM table_orders WHERE session_id=$1`, grant.Session.ID).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_order_state WHERE session_id=$1`, grant.Session.ID).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM cash_settlements WHERE session_id=$1`, grant.Session.ID).Scan(&settlementCount); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempts WHERE session_id=$1`, grant.Session.ID).Scan(&stripeAttemptCount); err != nil {
		t.Fatal(err)
	}
	latestCart, err := f.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if orderCount != 0 || operationCount != 0 || settlementCount != 0 || stripeAttemptCount != 0 || latestCart.Version != cart.Version || len(latestCart.Lines) != 1 || latestCart.Lines[0].ProductKey != "tea" {
		t.Fatalf("failed-closed commit mutated business state: orders=%d operation=%d settlements=%d stripe_attempts=%d cart=%+v",
			orderCount, operationCount, settlementCount, stripeAttemptCount, latestCart)
	}
}
