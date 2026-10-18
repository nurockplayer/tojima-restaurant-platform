package payments

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

func TestPostgresP82RefundExpiredCreateWindowDoesNotCreate(t *testing.T) {
	h := newHoldFixture(t)
	order := createHeldPaidReview(t, h)
	h.work()
	// Set the fixture's insertion default before the manager creates the refund.
	// The persisted deadline remains immutable: no trigger is disabled or record
	// rewritten, and no sleeps or live provider calls are needed.
	if _, err := h.Pool.Exec(h.ctx, `ALTER TABLE operation_refunds ALTER COLUMN retry_not_after SET DEFAULT (clock_timestamp()-interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
	o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, order.Order.ID), IdempotencyKey: "expired-cancel", Reason: "cancel unavailable item", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	h.work()
	creates, _ := h.provider.RefundOperationCounts()
	if creates != 0 {
		t.Fatalf("expired refund created provider object: %d", creates)
	}
	var status, code string
	if err = h.Pool.QueryRow(h.ctx, `SELECT r.status,w.safe_error_code FROM operation_refunds r JOIN stripe_payment_work w USING(refund_id) WHERE r.refund_id=$1`, o.Refunds[0].ID).Scan(&status, &code); err != nil || status != "PROCESSING" || code != "REFUND_RETRY_WINDOW_ELAPSED" {
		t.Fatalf("unresolved=%s code=%s err=%v", status, code, err)
	}
	if _, err = h.Operations.RetryRefund(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, o.ID), IdempotencyKey: "unsafe-retry"}, o.Refunds[0].ID); !errors.Is(err, operations.ErrRefundNotRetryable) {
		t.Fatalf("ambiguous refund got a fresh key: %v", err)
	}
	tx, err := h.Pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = h.service.CheckClose(h.ctx, tx, tablesession.Session{ID: o.SessionID}); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("ambiguous expired refund allowed close: %v", err)
	}
}

func TestPostgresP82RefundIngressSerializesWithManualRetry(t *testing.T) {
	for _, retryFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry-first-%v", retryFirst), func(t *testing.T) {
			h := newHoldFixture(t)
			order := createHeldPaidReview(t, h)
			h.work()
			scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, order.Order.ID), IdempotencyKey: "cancel", Reason: "cancel unavailable item", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			refundID := o.Refunds[0].ID
			h.provider.SetNextRefundOutcome("failed", false)
			h.work()
			old, found := h.provider.RefundByBusinessID(refundID)
			if !found {
				t.Fatal("missing failed refund")
			}
			version := reviewOrderVersion(t, h, o.ID)
			event := stripeprovider.VerifiedEvent{ID: "evt_failed_old", Type: "refund.failed", AccountID: old.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: old.ID, Refund: &old}
			retry := func() error {
				_, e := h.Operations.RetryRefund(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "manager", ExpectedVersion: version, IdempotencyKey: "retry"}, refundID)
				return e
			}
			ingress := func() error { _, e := h.service.EnqueueVerifiedWebhook(h.ctx, event); return e }
			first, second := ingress, retry
			if retryFirst {
				first, second = retry, ingress
			}
			a, b := runManagerReviewLockRace(t, h, o.SessionID, first, second)
			if a != nil || b != nil {
				t.Fatalf("serialized ingress/retry: %v %v", a, b)
			}
			var generation int64
			var status string
			if err = h.Pool.QueryRow(h.ctx, `SELECT generation,status FROM operation_refunds WHERE refund_id=$1`, refundID).Scan(&generation, &status); err != nil || generation != 2 || status != "REQUESTED" {
				t.Fatalf("stale event attached to retry gen=%d status=%s %v", generation, status, err)
			}
			if !retryFirst {
				h.work()
				if err = h.Pool.QueryRow(h.ctx, `SELECT status FROM operation_refunds WHERE refund_id=$1`, refundID).Scan(&status); err != nil || status != "REQUESTED" {
					t.Fatalf("stale work changed new generation: %s %v", status, err)
				}
			}
			h.provider.SetNextRefundOutcome("succeeded", false)
			h.work()
			var providerID string
			if err = h.Pool.QueryRow(h.ctx, `SELECT provider_refund_id FROM operation_refunds WHERE refund_id=$1`, refundID).Scan(&providerID); err != nil {
				t.Fatal(err)
			}
			current, err := h.provider.RetrieveRefund(h.ctx, stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: old.AccountID}, providerID)
			if err != nil {
				t.Fatal(err)
			}
			// A charge list may contain a previous failed generation and an external
			// refund. Neither may prevent reconciliation of this current local refund.
			foreign := current
			foreign.ID = "re_external"
			foreign.Metadata = map[string]string{}
			charge := stripeprovider.VerifiedEvent{ID: "evt_charge_refunded", Type: "charge.refunded", AccountID: old.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: "ch_fixture", Refunds: []stripeprovider.RefundResult{old, foreign, current}, RefundsHasMore: true}
			if ok, e := h.service.EnqueueVerifiedWebhook(h.ctx, charge); e != nil || !ok {
				t.Fatalf("mixed charge refund list=%v %v", ok, e)
			}
			h.work()
			bad := current
			bad.Metadata = map[string]string{"refund_id": refundID, "tenant_id": "foreign", "store_id": h.StoreID, "order_id": o.ID}
			event.ID = "evt_cross_tenant"
			event.ObjectID = bad.ID
			event.Refund = &bad
			if _, err = h.service.EnqueueVerifiedWebhook(h.ctx, event); !errors.Is(err, ErrDenied) {
				t.Fatalf("cross-tenant refund accepted: %v", err)
			}
			creates, _ := h.provider.RefundOperationCounts()
			if creates != 2 {
				t.Fatalf("duplicate refund creations=%d", creates)
			}
			detail, err := h.Operations.ReadSession(h.ctx, scope, o.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if got := sessionOrderByID(t, detail.Orders, o.ID); got.PaymentStatus != operations.Refunded {
				t.Fatalf("successful refund projection=%+v", got)
			}
		})
	}
}

func TestPostgresP82PartialReviewAdjustmentKeepsReviewUntilApproval(t *testing.T) {
	h := newHoldFixture(t)
	result := commitC1TwoLineOrder(t, h)
	h.work()
	h.hold(true, 0)
	x := h.checkout(result.Order.ID)
	x, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	h.event(x, "paid-partial-review", "checkout.session.completed")
	h.work()
	h.work()
	scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
	adjusted, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: result.Order.SessionID, OrderID: result.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, result.Order.ID), IdempotencyKey: "partial-review", Reason: "remove held item only", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if !adjusted.PaymentReviewRequired || adjusted.ReviewDecision != "UNRESOLVED" || adjusted.FulfillmentStatus != operations.Waiting || len(adjusted.Refunds) != 1 {
		t.Fatalf("partial review was released: %+v", adjusted)
	}
	cards, err := h.Operations.ListKDS(h.ctx, operations.StoreScope{TenantID: h.TenantID, StoreID: h.StoreID})
	if err != nil || len(cards) != 0 {
		t.Fatalf("partial review KDS=%+v %v", cards, err)
	}
	// The hold remains ON for the removed item. Remaining non-held lines still
	// require an explicit manager decision, then release exactly one card.
	approved, err := h.Operations.ApproveFulfillment(h.ctx, managerApprovalRequest(h, result.Order.SessionID, result.Order.ID, adjusted.Version, "approve-remaining", "confirm remaining tea"))
	if err != nil {
		t.Fatal(err)
	}
	if approved.PaymentReviewRequired || approved.FulfillmentStatus != operations.Accepted || approved.Lines[0].Quantity != 0 || approved.Lines[1].Quantity != 1 {
		t.Fatalf("remaining approval=%+v", approved)
	}
}

func TestPostgresP82ServedPartialRefundAloneBlocksCloseUntilSuccess(t *testing.T) {
	h := newHoldFixture(t)
	result := commitC1TwoLineOrder(t, h)
	h.provider.SetNextCreateOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	h.work()
	scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
	o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: result.Order.SessionID, OrderID: result.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, result.Order.ID), IdempotencyKey: "partial", Reason: "remove tea", Lines: []operations.AdjustmentLine{{LineIndex: 1, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	h.provider.SetNextRefundOutcome("pending", false)
	h.work()
	detail, err := h.Operations.ReadSession(h.ctx, scope, o.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	o = sessionOrderByID(t, detail.Orders, o.ID)
	o, err = h.Operations.AcceptOrder(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "staff", ExpectedVersion: o.Version, IdempotencyKey: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
		o, err = h.Operations.AdvanceFulfillment(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "kitchen", ExpectedVersion: o.Version, IdempotencyKey: string(target), Target: target})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = h.Sessions.Close(h.ctx, tablesession.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID, TableID: scope.TableID}, o.SessionID, "manager"); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("served partial processing refund allowed close: %v", err)
	}
	refund, ok := h.provider.RefundByBusinessID(o.Refunds[0].ID)
	if !ok {
		t.Fatal("refund missing")
	}
	if _, err = h.provider.SetRefundOutcome(refund.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	// Worker polling, without any webhook, must retrieve the same refund.
	if _, err = h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE refund_id=$1 AND work_type='REFUND'`, o.Refunds[0].ID); err != nil {
		t.Fatal(err)
	}
	h.work()
	if err = h.Sessions.Close(h.ctx, tablesession.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID, TableID: scope.TableID}, o.SessionID, "manager"); err != nil {
		t.Fatalf("confirmed partial refund still blocks close: %v", err)
	}
	detail, err = h.Operations.ReadSession(h.ctx, scope, o.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	o = sessionOrderByID(t, detail.Orders, o.ID)
	if o.PaymentStatus != operations.Paid || o.Refunds[0].Status != "SUCCEEDED" {
		t.Fatalf("partial success state=%+v", o)
	}
}

func commitC1TwoLineOrder(t *testing.T, h *holdFixture) ordering.CommitResult {
	t.Helper()
	grant, err := h.Sessions.DirectJoin(h.ctx, h.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cart, err := h.Orders.ReadCart(h.ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, product := range []string{"teishoku", "tea"} {
		var options []catalog.OptionSelection
		if product == "teishoku" {
			options = []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}}
		}
		cart, err = h.Orders.Mutate(h.ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "add-" + product, Kind: ordering.AddLine, ProductKey: product, Quantity: 1, Options: options})
		if err != nil {
			t.Fatal(err)
		}
	}
	preview, err := h.Orders.Preview(h.ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.Orders.Commit(h.ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "commit-two-lines", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe, CheckoutProofHash: sha256.Sum256([]byte("c1-test-only-proof")), CheckoutProofExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
