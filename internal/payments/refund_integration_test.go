package payments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

func TestPostgresP82StripePartialRefundWorkerAndWebhook(t *testing.T) {
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
	provider := testpayments.NewProvider()
	account := stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_fixtureStoreA"}
	const credential = "fixture:payments:v1"
	resolver := testResolver{credential: {Provider: provider, Account: account, ProfileFingerprint: sha256.Sum256([]byte("refund-profile"))}}
	service, err := New(f.Pool, f.Operations, resolver, Config{CreateRetryWindow: time.Hour, ContinuationTTL: 20 * time.Minute, WorkLeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetMerchantConfig(ctx, operations.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}, 0, "fixture-admin", MerchantConfigInput{Enabled: true, Mode: account.Mode, StripeAccountID: account.StripeAccountID, CredentialRef: credential, WebhookEndpointRef: "fixture:webhook", APIVersion: stripeprovider.APIVersion}); err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	f.Sessions, err = tablesession.New(f.Pool, tablesession.Options{SessionTTL: 2 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 100, PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: pepper}, service)
	if err != nil {
		t.Fatal(err)
	}
	f.Orders, err = ordering.NewWithOrderPolicy(f.Pool, f.Sessions, f.Menus, service)
	if err != nil {
		t.Fatal(err)
	}
	// Set the fixture lines to ¥1,500 and ¥500 so this is the P-44 acceptance
	// amount while retaining the regular PostgreSQL catalog/order path.
	draft, err := f.Menus.GetCompanyDraft(ctx, f.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for ci := range draft.CatalogData.Categories {
		for pi := range draft.CatalogData.Categories[ci].Products {
			p := &draft.CatalogData.Categories[ci].Products[pi]
			if p.Key == "teishoku" {
				p.BasePriceMinor = 1500
			}
			if p.Key == "tea" {
				p.Name = "gyoza"
				p.BasePriceMinor = 500
			}
		}
	}
	saved, err := f.Menus.SaveCompanyDraft(ctx, f.TenantID, "refund-fixture", draft.Version, draft.CatalogData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Menus.PublishCompanyDraft(ctx, f.TenantID, "refund-fixture", saved.Version); err != nil {
		t.Fatal(err)
	}
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cart, err := f.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []struct {
		key, id string
		options []catalog.OptionSelection
	}{{"teishoku", "paid-main", []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}}}, {"tea", "paid-gyoza", nil}} {
		cart, err = f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: line.id, Kind: ordering.AddLine, ProductKey: line.key, Quantity: 1, Options: line.options})
		if err != nil {
			t.Fatal(err)
		}
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Quote.Tax.Total().GrossJPY != 2000 {
		t.Fatalf("fixture total=%d, want 2000", preview.Quote.Tax.Total().GrossJPY)
	}
	var proof [32]byte
	if _, err = rand.Read(proof[:]); err != nil {
		t.Fatal(err)
	}
	committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "paid-2000", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe, CheckoutProofHash: proof, CheckoutProofExpiresAt: time.Now().UTC().Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	provider.SetNextCreateOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if worked, e := service.ProcessNext(ctx); e != nil || !worked {
		t.Fatalf("create worker=%v err=%v", worked, e)
	}
	detail, err := f.Operations.ReadSession(ctx, operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := sessionOrderByID(t, detail.Orders, committed.Order.ID)
	if state.PaymentStatus != operations.Paid || state.TotalGrossJPY != 2000 {
		t.Fatalf("paid original state: %+v", state)
	}
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
	state, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: committed.Order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "accept-refund-test"})
	if err != nil {
		t.Fatalf("accept before adjustment: %v", err)
	}
	previewAdj, err := f.Operations.PreviewAdjustment(ctx, operations.AdjustmentRequest{Scope: operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}, SessionID: grant.Session.ID, OrderID: committed.Order.ID, Actor: "manager", ExpectedVersion: state.Version, Reason: "kitchen correction", Lines: []operations.AdjustmentLine{{LineIndex: 1, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if previewAdj.EffectiveTotalGrossJPY != 1500 || previewAdj.RefundAmountJPY != 500 {
		t.Fatalf("refund preview=%+v", previewAdj)
	}
	state, err = f.Operations.AdjustOrder(ctx, operations.AdjustmentRequest{Scope: operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}, SessionID: grant.Session.ID, OrderID: committed.Order.ID, Actor: "manager", IdempotencyKey: "cancel-gyoza", ExpectedVersion: state.Version, Reason: "kitchen correction", Lines: []operations.AdjustmentLine{{LineIndex: 1, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if state.PaymentStatus != operations.Paid || state.TotalGrossJPY != 2000 || state.EffectiveTotalGrossJPY != 1500 {
		t.Fatalf("partial adjustment changed original payment: %+v", state)
	}
	if len(state.Refunds) != 1 || state.Refunds[0].Status != "REQUESTED" || state.Refunds[0].AmountJPY != 500 {
		t.Fatalf("refund record=%+v", state.Refunds)
	}
	if !state.OrderUpdated {
		t.Fatal("existing KDS card did not receive order-updated flag")
	}
	state, err = f.Operations.AcknowledgeOrderUpdate(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: committed.Order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "ack-refund-test"})
	if err != nil {
		t.Fatalf("acknowledge adjusted order: %v", err)
	}
	// The provider commits the refund but the create response is lost. Recovery
	// must retain the same key and a signed webhook can bind the unknown object.
	provider.SetNextRefundOutcome("pending", true)
	if worked, e := service.ProcessNext(ctx); e != nil || !worked {
		t.Fatalf("refund worker=%v err=%v", worked, e)
	}
	var providerRefundID string
	if err = f.Pool.QueryRow(ctx, `SELECT coalesce(provider_refund_id,'') FROM operation_refunds WHERE order_id=$1`, committed.Order.ID).Scan(&providerRefundID); err != nil {
		t.Fatal(err)
	}
	if providerRefundID != "" {
		t.Fatalf("lost response unexpectedly bound provider refund %q", providerRefundID)
	}
	if refundRetryWindowOpen(ctx, f.Pool, time.Now().Add(-time.Hour)) {
		t.Fatal("expired retry window allowed another idempotent create attempt")
	}
	if err = f.Pool.QueryRow(ctx, `SELECT status FROM operation_refunds WHERE order_id=$1`, committed.Order.ID).Scan(&state.Refunds[0].Status); err != nil || state.Refunds[0].Status != "PROCESSING" {
		t.Fatalf("pending status=%s err=%v", state.Refunds[0].Status, err)
	}
	var amount int64
	var attemptState string
	if err = f.Pool.QueryRow(ctx, `SELECT amount_jpy,state FROM stripe_payment_attempts WHERE order_id=$1`, committed.Order.ID).Scan(&amount, &attemptState); err != nil {
		t.Fatal(err)
	}
	if amount != 2000 || attemptState != "PAID" {
		t.Fatalf("original Stripe attempt changed amount=%d state=%s", amount, attemptState)
	}
	checkClose := func() error {
		tx, e := f.Pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		return service.CheckClose(ctx, tx, tablesession.Session{ID: grant.Session.ID, Scope: tablesession.Scope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}})
	}
	if err = checkClose(); err == nil {
		t.Fatal("close succeeded while refund was processing")
	}
	unknownProviderRefund, exists := provider.RefundByBusinessID(state.Refunds[0].ID)
	if !exists {
		t.Fatal("unknown provider response did not persist a provider refund")
	}
	providerRefundID = unknownProviderRefund.ID
	if _, err = provider.SetRefundOutcome(providerRefundID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	refund, err := provider.RetrieveRefund(ctx, account, providerRefundID)
	if err != nil {
		t.Fatal(err)
	}
	oldObservation := refund
	oldObservation.Status = "failed"
	oldEvent := stripeprovider.VerifiedEvent{ID: "evt-refund-old", Type: "refund.failed", AccountID: account.StripeAccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().Add(-time.Minute).UTC(), ObjectID: refund.ID, Refund: &oldObservation}
	if accepted, e := service.EnqueueVerifiedWebhook(ctx, oldEvent); e != nil || !accepted {
		t.Fatalf("old refund webhook accepted=%v err=%v", accepted, e)
	}
	event := stripeprovider.VerifiedEvent{ID: "evt-refund-done", Type: "refund.updated", AccountID: account.StripeAccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: refund.ID, Refund: &refund}
	if accepted, e := service.EnqueueVerifiedWebhook(ctx, event); e != nil || !accepted {
		t.Fatalf("refund webhook accepted=%v err=%v", accepted, e)
	}
	for i := 0; i < 2; i++ {
		if worked, e := service.ProcessNext(ctx); e != nil || !worked {
			t.Fatalf("refund webhook work %d=%v err=%v", i, worked, e)
		}
	}
	creates, _ := provider.RefundOperationCounts()
	if creates != 1 {
		t.Fatalf("unknown create was retried with another provider creation: creates=%d", creates)
	}
	if accepted, e := service.EnqueueVerifiedWebhook(ctx, event); e != nil || accepted {
		t.Fatalf("duplicate refund webhook accepted=%v err=%v", accepted, e)
	}
	if worked, e := service.ProcessNext(ctx); e != nil || worked {
		t.Fatalf("duplicate webhook created work=%v err=%v", worked, e)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp()+interval '1 hour' WHERE refund_id=$1 AND work_type='REFUND'`, state.Refunds[0].ID); err != nil {
		t.Fatal(err)
	}
	detail, err = f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	state = sessionOrderByID(t, detail.Orders, committed.Order.ID)
	state, err = f.Operations.AdjustOrder(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: committed.Order.ID, Actor: "manager", IdempotencyKey: "cancel-last-line", ExpectedVersion: state.Version, Reason: "cancel remaining item", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatalf("cancel last effective item: %v", err)
	}
	if state.FulfillmentStatus != operations.Cancelled || state.PaymentStatus != operations.RefundPending || state.Refunds[len(state.Refunds)-1].AmountJPY != 1500 {
		t.Fatalf("whole order cancellation state=%+v", state)
	}
	provider.SetNextRefundOutcome("failed", false)
	if worked, e := service.ProcessNext(ctx); e != nil || !worked {
		t.Fatalf("failed refund work=%v err=%v", worked, e)
	}
	var failedID, failedStatus string
	if err = f.Pool.QueryRow(ctx, `SELECT provider_refund_id,status FROM operation_refunds WHERE order_id=$1 AND amount_jpy=1500`, committed.Order.ID).Scan(&failedID, &failedStatus); err != nil {
		t.Fatal(err)
	}
	if failedStatus != "FAILED" || failedID == "" {
		t.Fatalf("confirmed failure binding id=%s status=%s", failedID, failedStatus)
	}
	detail, err = f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	state = sessionOrderByID(t, detail.Orders, committed.Order.ID)
	if err = checkClose(); err == nil {
		t.Fatal("close succeeded while full refund failed")
	}
	oldFailed, err := provider.RetrieveRefund(ctx, account, failedID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = f.Operations.RetryRefund(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: committed.Order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "retry-full-refund"}, state.Refunds[len(state.Refunds)-1].ID)
	if err != nil {
		t.Fatalf("manual retry: %v", err)
	}
	oldEvent.Refund = &oldFailed
	oldEvent.ID = "evt-old-generation-after-retry"
	oldEvent.CreatedAt = time.Now().UTC()
	oldEvent.ObjectID = oldFailed.ID
	if accepted, e := service.EnqueueVerifiedWebhook(ctx, oldEvent); e != nil || accepted {
		t.Fatalf("old generation webhook accepted=%v error=%v, want acknowledged no-op", accepted, e)
	}
	provider.SetNextRefundOutcome("succeeded", false)
	if worked, e := service.ProcessNext(ctx); e != nil || !worked {
		t.Fatalf("retry refund work=%v err=%v", worked, e)
	}
	detail, err = f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	state = sessionOrderByID(t, detail.Orders, committed.Order.ID)
	if state.PaymentStatus != operations.Refunded || state.FulfillmentStatus != operations.Cancelled {
		t.Fatalf("whole refund completion=%+v", state)
	}
	if err = checkClose(); err != nil {
		t.Fatalf("close after full refund success: %v", err)
	}
}

func sessionOrderByID(t *testing.T, orders []operations.OrderState, id string) operations.OrderState {
	t.Helper()
	for _, order := range orders {
		if order.ID == id {
			return order
		}
	}
	t.Fatalf("order %q missing from session", id)
	return operations.OrderState{}
}
