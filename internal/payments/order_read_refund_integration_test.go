package payments

import (
	"fmt"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

func TestPostgresOrderReadRefundResolution(t *testing.T) {
	for _, partial := range []bool{true, false} {
		t.Run(fmt.Sprintf("partial-%v", partial), func(t *testing.T) {
			h := newHoldFixture(t)
			result := commitC1TwoLineOrder(t, h)
			h.provider.SetNextCreateOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
			h.work()
			scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			o, err := h.Operations.AcceptOrder(h.ctx, operations.OrderActionRequest{
				Scope: scope, SessionID: result.Order.SessionID, OrderID: result.Order.ID,
				Actor: "kitchen", ExpectedVersion: reviewOrderVersion(t, h, result.Order.ID), IdempotencyKey: "accept",
			})
			if err != nil {
				t.Fatal(err)
			}
			lines := []operations.AdjustmentLine{{LineIndex: 1, CancelledQuantity: 1}}
			if !partial {
				lines = append(lines, operations.AdjustmentLine{LineIndex: 0, CancelledQuantity: 1})
			}
			o, err = h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{
				Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "manager",
				ExpectedVersion: o.Version, IdempotencyKey: "cancel", Reason: "remove items", Lines: lines,
			})
			if err != nil {
				t.Fatal(err)
			}
			o, err = h.Operations.AcknowledgeOrderUpdate(h.ctx, operations.OrderActionRequest{
				Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "kitchen",
				ExpectedVersion: o.Version, IdempotencyKey: "acknowledge",
			})
			if err != nil {
				t.Fatal(err)
			}
			if partial {
				for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
					o, err = h.Operations.AdvanceFulfillment(h.ctx, operations.OrderActionRequest{
						Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "kitchen",
						ExpectedVersion: o.Version, IdempotencyKey: string(target), Target: target,
					})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			assertStatus := func(t *testing.T, refundStatus string) {
				t.Helper()
				o = assertRefundProjection(t, h, o, refundStatus, "", partial)
				status, err := h.Orders.ReadOrderStatus(h.ctx, result.Order.ID, result.OrderReadGrant.Token)
				if err != nil {
					t.Fatal(err)
				}
				wantFulfillment := operations.Cancelled
				if partial {
					wantFulfillment = operations.Served
				}
				// A successful full refund stays non-terminal under the existing
				// PAID + SERVED rule. Partial success resolves the last gate.
				wantTerminal := partial && refundStatus == "SUCCEEDED"
				if status.IsTerminal != wantTerminal || status.PaymentStatus != string(o.PaymentStatus) || status.FulfillmentStatus != string(wantFulfillment) || status.Version != o.Version {
					t.Fatalf("refund=%s: status=%+v, want terminal=%t payment=%s fulfillment=%s version=%d", refundStatus, status, wantTerminal, o.PaymentStatus, wantFulfillment, o.Version)
				}
			}
			t.Run("REQUESTED", func(t *testing.T) { assertStatus(t, "REQUESTED") })
			h.provider.SetNextRefundOutcome("pending", false)
			h.work()
			t.Run("PROCESSING", func(t *testing.T) { assertStatus(t, "PROCESSING") })
			x, ok := h.provider.RefundByBusinessID(o.Refunds[0].ID)
			if !ok {
				t.Fatal("refund missing")
			}
			for i, tc := range []struct{ provider, local string }{
				{"failed", "FAILED"},
				{"succeeded", "SUCCEEDED"},
				{"pending", "PROCESSING"},
				{"succeeded", "SUCCEEDED"},
			} {
				t.Run(fmt.Sprintf("%d-%s", i, tc.local), func(t *testing.T) {
					if _, err := h.provider.SetRefundOutcome(x.ID, tc.provider); err != nil {
						t.Fatal(err)
					}
					runProjectionWork(t, h, leaseProjectionWork(t, h, x, true, fmt.Sprintf("refund-status-%d", i)))
					assertStatus(t, tc.local)
				})
			}
		})
	}
}
