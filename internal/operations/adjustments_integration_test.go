package operations_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func TestPostgresC1CashReductionsReceiptsAndKitchenAcknowledgement(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, paid := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpaid", true: "paid"}[paid], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close(ctx)
			grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
			if err != nil {
				t.Fatal(err)
			}
			scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: grant.Session.Scope.TableID}
			setStorePrice(t, ctx, f, catalog.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}, "tea", 500, 0)
			cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: "two-teas", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 2})
			if err != nil {
				t.Fatal(err)
			}
			quote, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
			if err != nil {
				t.Fatal(err)
			}
			committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "commit", PreviewFingerprint: quote.Fingerprint, PaymentMethod: ordering.Cash})
			if err != nil {
				t.Fatal(err)
			}
			orderID := committed.Order.ID
			state, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: "accept"})
			if err != nil {
				t.Fatal(err)
			}
			var receipt operations.Receipt
			var receiptBefore string
			if paid {
				preview, e := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
				if e != nil {
					t.Fatal(e)
				}
				receipt, err = f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: preview.Fingerprint, TenderedJPY: 1000, RegisterSessionRef: "shift", IdempotencyKey: "settle"})
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(c)::text FROM cash_settlements c WHERE receipt_id=$1`, receipt.ID).Scan(&receiptBefore); err != nil {
					t.Fatal(err)
				}
				detail, e := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
				if e != nil {
					t.Fatal(e)
				}
				state = detail.Orders[0]
			}
			var original string
			if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(o)::text FROM table_orders o WHERE order_id=$1`, orderID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			req := operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "adjust", Reason: "guest requested one less", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}}
			preview, err := f.Operations.PreviewAdjustment(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			wantRefund := int64(0)
			if paid {
				wantRefund = 500
			}
			if preview.EffectiveTotalGrossJPY != 500 || preview.RefundAmountJPY != wantRefund || preview.WholeOrderCancel {
				t.Fatalf("preview=%+v", preview)
			}
			if paid {
				if _, err = f.Operations.AdjustOrder(ctx, req); !errors.Is(err, operations.ErrCashHandBackRequired) {
					t.Fatalf("handback not required: %v", err)
				}
				req.CashHandBackConfirmed = true
			}
			state, err = f.Operations.AdjustOrder(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if state.TotalGrossJPY != 1000 || state.EffectiveTotalGrossJPY != 500 || state.Lines[0].Quantity != 1 || state.Lines[0].OriginalQuantity != 2 || !state.OrderUpdated {
				t.Fatalf("adjusted=%+v", state)
			}
			replay, err := f.Operations.AdjustOrder(ctx, req)
			if err != nil || replay.Version != state.Version {
				t.Fatalf("replay=%+v %v", replay, err)
			}
			var n int
			if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_refunds WHERE order_id=$1`, orderID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if paid {
				if n != 1 || state.Refunds[0].Status != "SUCCEEDED" || state.PaymentStatus != operations.Paid {
					t.Fatalf("cash refund=%+v n=%d", state, n)
				}
				var after string
				if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(c)::text FROM cash_settlements c WHERE receipt_id=$1`, receipt.ID).Scan(&after); err != nil || after != receiptBefore {
					t.Fatal("original receipt changed", err)
				}
				if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM cash_refund_ledger WHERE receipt_id=$1 AND actor_id='manager' AND amount_jpy=500`, receipt.ID).Scan(&n); err != nil || n != 1 {
					t.Fatalf("ledger count=%d %v", n, err)
				}
			} else {
				if n != 0 {
					t.Fatalf("unpaid refund created: %d", n)
				}
				due, e := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
				if e != nil || due.DueJPY != 500 {
					t.Fatalf("adjusted due=%+v %v", due, e)
				}
				tables, e := f.Operations.ListTables(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
				if e != nil {
					t.Fatal(e)
				}
				found := false
				for _, tb := range tables {
					if tb.SessionID == grant.Session.ID {
						found = true
						if tb.UnpaidCashJPY != 500 {
							t.Fatal(tb)
						}
					}
				}
				if !found {
					t.Fatal("table absent")
				}
			}
			if _, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "prepare-before-ack", Target: operations.Preparing}); !errors.Is(err, operations.ErrUpdateAcknowledgementRequired) {
				t.Fatalf("prepared without ack: %v", err)
			}
			cards, err := f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
			if err != nil || len(cards) != 1 || cards[0].Lines[0].Quantity != 1 || !cards[0].OrderUpdated {
				t.Fatalf("updated card=%+v %v", cards, err)
			}
			ack := operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "ack"}
			state, err = f.Operations.AcknowledgeOrderUpdate(ctx, ack)
			if err != nil || state.OrderUpdated {
				t.Fatalf("ack=%+v %v", state, err)
			}
			state, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "prepare", Target: operations.Preparing})
			if err != nil {
				t.Fatal(err)
			}
			req.ExpectedVersion = state.Version
			req.IdempotencyKey = "too-late"
			if _, err = f.Operations.AdjustOrder(ctx, req); !errors.Is(err, operations.ErrPreparationStarted) {
				t.Fatalf("preparing adjustment accepted: %v", err)
			}
			var unchanged string
			if err = f.Pool.QueryRow(ctx, `SELECT row_to_json(o)::text FROM table_orders o WHERE order_id=$1`, orderID).Scan(&unchanged); err != nil || unchanged != original {
				t.Fatal("original order changed", err)
			}
			if _, err = f.Pool.Exec(ctx, `UPDATE order_adjustments SET reason='rewrite' WHERE order_id=$1`, orderID); err == nil {
				t.Fatal("adjustment history mutable")
			}
			if _, err = f.Pool.Exec(ctx, `DELETE FROM order_adjustments WHERE order_id=$1`, orderID); err == nil {
				t.Fatal("adjustment history deletable")
			}
		})
	}
}

func TestPostgresC1CashLastItemClosesWithoutDeletingFacts(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, paid := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpaid", true: "paid"}[paid], func(t *testing.T) {
			ctx := context.Background()
			f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close(ctx)
			grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
			if err != nil {
				t.Fatal(err)
			}
			scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: grant.Session.Scope.TableID}
			order := commitCashOrder(t, ctx, f, grant, 1, "full-cancel")
			if paid {
				preview, e := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
				if e != nil {
					t.Fatal(e)
				}
				if _, err = f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: preview.Fingerprint, TenderedJPY: 1000, RegisterSessionRef: "shift", IdempotencyKey: "settle"}); err != nil {
					t.Fatal(err)
				}
			}
			detail, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
			if err != nil {
				t.Fatal(err)
			}
			o := detail.Orders[0]
			req := operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: o.Version, IdempotencyKey: "cancel", Reason: "item unavailable", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: o.Lines[0].Quantity}}, CashHandBackConfirmed: paid}
			o, err = f.Operations.AdjustOrder(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if o.FulfillmentStatus != operations.Cancelled || o.EffectiveTotalGrossJPY != 0 || o.ReviewDecision != "CANCELLED" {
				t.Fatalf("full cancelled=%+v", o)
			}
			if paid && (o.PaymentStatus != operations.Refunded || len(o.Refunds) != 1 || o.Refunds[0].AmountJPY != o.TotalGrossJPY) {
				t.Fatalf("full cash refund=%+v", o)
			}
			history, err := f.Operations.ReadGuestOperations(ctx, grant.Session.ID, grant.Token)
			if err != nil {
				t.Fatal(err)
			}
			wantPayment := operations.Unpaid
			if paid {
				wantPayment = operations.Unknown
			}
			if len(history.Orders) != 1 || history.Orders[0].DisplayState != "UNKNOWN" || history.Orders[0].PaymentStatus != wantPayment || history.Orders[0].FulfillmentStatus != operations.Cancelled {
				t.Fatalf("cancelled guest history=%+v", history)
			}
			if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "cashier"); err != nil {
				t.Fatalf("resolved cancelled session cannot close: %v", err)
			}
			var n int
			if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM table_orders WHERE order_id=$1`, order.ID).Scan(&n); err != nil || n != 1 {
				t.Fatal("cancelled fact deleted", err)
			}
		})
	}
}

func TestPostgresC1CancelledWaitingCardRetainedUntilKitchenAcknowledgement(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: grant.Session.Scope.TableID}
	order := commitCashOrder(t, ctx, f, grant, 1, "cancel-waiting")
	o, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "staff", ExpectedVersion: 1, IdempotencyKey: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	req := operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: o.Version, IdempotencyKey: "cancel", Reason: "cancel waiting item", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}}
	o, err = f.Operations.AdjustOrder(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	cards, err := f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 1 || cards[0].FulfillmentStatus != operations.Cancelled || cards[0].Lines[0].Quantity != 0 || !cards[0].OrderUpdated {
		t.Fatalf("cancellation notice=%+v %v", cards, err)
	}
	if _, err = f.Operations.AcknowledgeOrderUpdate(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: o.Version, IdempotencyKey: "ack-cancellation"}); err != nil {
		t.Fatal(err)
	}
	cards, err = f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 0 {
		t.Fatalf("acknowledged cancelled card still active=%+v %v", cards, err)
	}
	var n int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_kds_cards WHERE order_id=$1`, order.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("historical card changed", err)
	}
}

func TestPostgresC1CashRefundUsesActualSettlementAfterUnpaidReduction(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.Prepaid})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: grant.Session.Scope.TableID}
	setStorePrice(t, ctx, f, catalog.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}, "tea", 500, 0)
	cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: "two", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	quote, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "commit", PreviewFingerprint: quote.Fingerprint, PaymentMethod: ordering.Cash})
	if err != nil {
		t.Fatal(err)
	}
	oldCashPreview, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: committed.Order.ID, Actor: "manager", ExpectedVersion: 1, IdempotencyKey: "unpaid-reduce", Reason: "reduce before payment", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}}
	o, err := f.Operations.AdjustOrder(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	settle := operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "staff", Fingerprint: oldCashPreview.Fingerprint, TenderedJPY: 1000, RegisterSessionRef: "shift", IdempotencyKey: "settle"}
	if _, err = f.Operations.SettleCash(ctx, settle); !errors.Is(err, operations.ErrReconfirm) {
		t.Fatalf("stale settlement fingerprint accepted: %v", err)
	}
	preview, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	settle.Fingerprint = preview.Fingerprint
	receipt, err := f.Operations.SettleCash(ctx, settle)
	if err != nil || receipt.DueJPY != 500 {
		t.Fatalf("actual paid=%+v %v", receipt, err)
	}
	detail, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	o = detail.Orders[0]
	req.ExpectedVersion = o.Version
	req.IdempotencyKey = "paid-cancel"
	req.CashHandBackConfirmed = true
	o, err = f.Operations.AdjustOrder(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if o.TotalGrossJPY != 1000 || o.EffectiveTotalGrossJPY != 0 || o.PaymentStatus != operations.Refunded || len(o.Refunds) != 1 || o.Refunds[0].AmountJPY != 500 {
		t.Fatalf("refund did not use actual allocation: %+v", o)
	}
	var paid, refunded int64
	if err = f.Pool.QueryRow(ctx, `SELECT a.amount_jpy,(SELECT sum(r.amount_jpy) FROM operation_refunds r WHERE r.order_id=a.order_id) FROM cash_settlement_allocations a WHERE order_id=$1`, o.ID).Scan(&paid, &refunded); err != nil || paid != 500 || refunded != paid {
		t.Fatalf("paid=%d refunded=%d err=%v", paid, refunded, err)
	}
}
