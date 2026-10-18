package payments

import (
	"errors"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// D9 gap: a guest abandons a Stripe Checkout and the provider confirms it
// expired unpaid. Staff reconciliation records EXPIRED, yet no staff action
// can make the order terminal, so the table can never be closed.
func TestPostgresD9ExpiredUnpaidStripeOrderBlocksClose(t *testing.T) {
	h := newHoldFixture(t)
	order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work() // Create Checkout.
	x := h.checkout(order.Order.ID)
	if _, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutExpired, stripeprovider.PaymentUnpaid); err != nil {
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
	status, err := h.service.Reconcile(h.ctx, scope, order.Order.SessionID, order.Order.ID, "fixture-manager")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateExpired {
		t.Fatalf("reconciled state=%s, want EXPIRED", status.State)
	}
	h.assertState(order.Order.ID, StateExpired, false, false)

	// The existing whole-order cancellation path refuses unpaid Stripe orders.
	state, err := h.Operations.ReadSession(h.ctx, scope, order.Order.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	o := sessionOrderByID(t, state.Orders, order.Order.ID)
	_, err = h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "fixture-manager", IdempotencyKey: "d9-cancel", ExpectedVersion: o.Version, Reason: "guest left without paying", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if !errors.Is(err, operations.ErrPaymentUnresolved) {
		t.Fatalf("whole-order cancel of unpaid Stripe order: %v", err)
	}

	tx, err := h.Pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(h.ctx)
	err = h.service.CheckClose(h.ctx, tx, tablesession.Session{ID: order.Order.SessionID, Scope: tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}})
	if err != nil {
		t.Fatalf("D9 gap: provider-confirmed unpaid Stripe order blocks table close: %v", err)
	}
}
