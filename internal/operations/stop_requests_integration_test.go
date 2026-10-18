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
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func commitTwoLineCashOrder(t *testing.T, ctx context.Context, f *testguest.Fixture, grant tablesession.Grant, key string) ordering.Order {
	t.Helper()
	cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: key + "-tea", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	cart, err = f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: cart.Version, IdempotencyKey: key + "-meal", Kind: ordering.AddLine, ProductKey: "teishoku", Quantity: 1, Options: []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: key + "-commit", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash})
	if err != nil {
		t.Fatal(err)
	}
	return committed.Order
}

func awaitBlockedBy(t *testing.T, ctx context.Context, f *testguest.Fixture, blockerPID int32, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		if err := f.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, blockerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("operation did not reach the held session row lock")
}

func newRequestedStop(t *testing.T, ctx context.Context, dsn, key string) (*testguest.Fixture, tablesession.Grant, operations.TableScope, operations.OrderState, operations.CancellationRequest) {
	t.Helper()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		f.Close(ctx)
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: grant.Session.Scope.TableID}
	order := commitCashOrder(t, ctx, f, grant, 1, key)
	state, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "staff", ExpectedVersion: 1, IdempotencyKey: key + "-accept"})
	if err != nil {
		f.Close(ctx)
		t.Fatal(err)
	}
	state, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: key + "-prepare", Target: operations.Preparing})
	if err != nil {
		f.Close(ctx)
		t.Fatal(err)
	}
	state, err = f.Operations.RequestStop(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: key + "-request", Reason: "stop requested", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		f.Close(ctx)
		t.Fatal(err)
	}
	return f, grant, scope, state, state.CancellationRequests[0]
}

func TestPostgresC2StopRequestBlocksFulfillmentAndFinalizesCashReduction(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
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
	order := commitTwoLineCashOrder(t, ctx, f, grant, "c2-stop")
	state, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "staff", ExpectedVersion: 1, IdempotencyKey: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "prepare", Target: operations.Preparing})
	if err != nil {
		t.Fatal(err)
	}
	stopRequest := operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "stop-one", Reason: "guest no longer wants the tea", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}}
	requested, err := f.Operations.RequestStop(ctx, stopRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(requested.CancellationRequests) != 1 || requested.CancellationRequests[0].Status != "REQUESTED" || requested.EffectiveTotalGrossJPY != state.EffectiveTotalGrossJPY || requested.Version != state.Version+1 {
		t.Fatalf("request changed fulfillment or amounts: %+v", requested)
	}
	for _, wrong := range []operations.TableScope{{TenantID: scope.TenantID, StoreID: "other-store", TableID: scope.TableID}, {TenantID: "other-tenant", StoreID: scope.StoreID, TableID: scope.TableID}} {
		bad := stopRequest
		bad.Scope = wrong
		bad.IdempotencyKey += "-out-of-scope"
		if _, err = f.Operations.RequestStop(ctx, bad); !errors.Is(err, operations.ErrNotFound) {
			t.Fatalf("out-of-scope stop request accepted for %+v: %v", wrong, err)
		}
	}
	replay, err := f.Operations.RequestStop(ctx, stopRequest)
	if err != nil || replay.Version != requested.Version || len(replay.CancellationRequests) != 1 {
		t.Fatalf("request replay=%+v err=%v", replay, err)
	}
	if _, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: requested.Version, IdempotencyKey: "ready-early", Target: operations.Ready}); !errors.Is(err, operations.ErrStopRequestPending) {
		t.Fatalf("READY accepted with active request: %v", err)
	}
	if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "cashier"); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("close accepted with active request: %v", err)
	}
	cards, err := f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 1 || len(cards[0].CancellationRequests) != 1 {
		t.Fatalf("pending request missing from KDS card: %+v err=%v", cards, err)
	}
	stop := requested.CancellationRequests[0]
	state, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: requested.Version, IdempotencyKey: "confirm-stop"}, RequestID: stop.ID, Action: "confirm", ExpectedRequestVersion: stop.Version})
	if err != nil {
		t.Fatal(err)
	}
	if state.CancellationRequests[0].Status != "KITCHEN_CONFIRMED" || state.EffectiveTotalGrossJPY != requested.EffectiveTotalGrossJPY {
		t.Fatalf("kitchen confirmation changed amounts: %+v", state)
	}
	stop = state.CancellationRequests[0]
	final, err := f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "finalize-stop"}, RequestID: stop.ID, Action: "finalize", ExpectedRequestVersion: stop.Version, NonDeliveryConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if final.EffectiveTotalGrossJPY >= requested.EffectiveTotalGrossJPY || final.Lines[0].Quantity != 1 || final.Lines[1].Quantity != requested.Lines[1].Quantity || final.CancellationRequests[0].Status != "FINALIZED" || final.FulfillmentStatus != operations.Preparing {
		t.Fatalf("finalized state=%+v", final)
	}
	due, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil || due.DueJPY == 0 {
		t.Fatalf("remaining cash due after partial stop=%+v err=%v", due, err)
	}
	var count int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM order_adjustments WHERE order_id=$1`, order.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("adjustment facts=%d err=%v", count, err)
	}
	finalizeReq := operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "finalize-stop"}, RequestID: stop.ID, Action: "finalize", ExpectedRequestVersion: stop.Version, NonDeliveryConfirmed: true}
	final, err = f.Operations.ActOnStopRequest(ctx, finalizeReq)
	if err != nil {
		t.Fatalf("finalize replay: %v", err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM order_adjustments WHERE order_id=$1`, order.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate adjustment count=%d err=%v", count, err)
	}
}

func TestPostgresC2RejectedAndUncertainStopRequestsRequireManualResolution(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, action := range []string{"reject", "uncertain"} {
		t.Run(action, func(t *testing.T) {
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
			order := commitCashOrder(t, ctx, f, grant, 1, "c2-manual-"+action)
			state, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "staff", ExpectedVersion: 1, IdempotencyKey: "accept"})
			if err != nil {
				t.Fatal(err)
			}
			state, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "prepare", Target: operations.Preparing})
			if err != nil {
				t.Fatal(err)
			}
			state, err = f.Operations.RequestStop(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "request", Reason: "customer request", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			stop := state.CancellationRequests[0]
			if action == "reject" {
				state, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "reject", Reason: "already delivered"}, RequestID: stop.ID, Action: "reject", ExpectedRequestVersion: stop.Version})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				state, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "confirm"}, RequestID: stop.ID, Action: "confirm", ExpectedRequestVersion: stop.Version})
				if err != nil {
					t.Fatal(err)
				}
				stop = state.CancellationRequests[0]
				state, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "uncertain", Reason: "delivery cannot be determined"}, RequestID: stop.ID, Action: "finalize", ExpectedRequestVersion: stop.Version, NonDeliveryConfirmed: false})
				if err != nil {
					t.Fatal(err)
				}
				if state.CancellationRequests[0].Status != "KITCHEN_CONFIRMED" || state.CancellationRequests[0].ManualExceptionReason == "" {
					t.Fatalf("uncertain result=%+v", state.CancellationRequests[0])
				}
				stop = state.CancellationRequests[0]
				if _, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "try-finalize"}, RequestID: stop.ID, Action: "finalize", ExpectedRequestVersion: stop.Version, NonDeliveryConfirmed: true}); !errors.Is(err, operations.ErrManualException) {
					t.Fatalf("manual exception finalized: %v", err)
				}
			}
			if _, err = f.Operations.AdjustOrder(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "direct-cancel", Reason: "bypass stop request", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}}); !errors.Is(err, operations.ErrPreparationStarted) {
				t.Fatalf("direct cancel was not refused after preparation: %v", err)
			}
			wantEvent, wantActor, wantReason := "STOP_REJECT", "kitchen", "already delivered"
			if action == "uncertain" {
				wantEvent, wantActor, wantReason = "STOP_MANUAL_EXCEPTION", "manager", "delivery cannot be determined"
			}
			var actor, event, reason string
			var auditVersion int64
			if err = f.Pool.QueryRow(ctx, `SELECT actor_id,event_type,reason,state_version FROM operation_order_events WHERE order_id=$1 AND event_type=$2`, order.ID, wantEvent).Scan(&actor, &event, &reason, &auditVersion); err != nil {
				t.Fatalf("missing %s audit: %v", wantEvent, err)
			}
			if actor != wantActor || event != wantEvent || reason != wantReason || auditVersion != state.Version {
				t.Fatalf("audit actor=%q event=%q reason=%q version=%d want state version=%d", actor, event, reason, auditVersion, state.Version)
			}
			review, err := f.Operations.ListPaymentReviews(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
			if err != nil {
				t.Fatal(err)
			}
			visible := false
			for _, item := range review {
				if item.ID == order.ID && len(item.CancellationRequests) == 1 {
					visible = true
				}
			}
			if !visible {
				t.Fatalf("manual exception missing from review queue: %+v", review)
			}
			if action == "reject" {
				if _, err = f.Operations.RequestStop(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "request-again", Reason: "retry rejected stop", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}}); !errors.Is(err, operations.ErrManualException) {
					t.Fatalf("rejected line was re-requested: %v", err)
				}
			}
			var adjustments, refunds int
			if err = f.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM order_adjustments WHERE order_id=$1),(SELECT count(*) FROM operation_refunds WHERE order_id=$1)`, order.ID).Scan(&adjustments, &refunds); err != nil || adjustments != 0 || refunds != 0 {
				t.Fatalf("manual path made adjustments=%d refunds=%d err=%v", adjustments, refunds, err)
			}
		})
	}
}

func TestPostgresC2ConcurrentManagersCreateOneStopRequest(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
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
	order := commitCashOrder(t, ctx, f, grant, 1, "c2-concurrent")
	state, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "staff", ExpectedVersion: 1, IdempotencyKey: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "prepare", Target: operations.Preparing})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, actor := range []string{"manager-a", "manager-b"} {
		go func(actor string) {
			<-start
			_, e := f.Operations.RequestStop(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: actor, ExpectedVersion: state.Version, IdempotencyKey: "request-" + actor, Reason: "duplicate click", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
			results <- e
		}(actor)
	}
	close(start)
	var succeeded int
	for i := 0; i < 2; i++ {
		if e := <-results; e == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("same-version manager requests succeeded=%d, want exactly one", succeeded)
	}
	final, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Orders) != 1 || len(final.Orders[0].CancellationRequests) != 1 || final.Orders[0].EffectiveTotalGrossJPY != state.EffectiveTotalGrossJPY {
		t.Fatalf("concurrent requests duplicated or changed amounts: %+v", final)
	}
}

func TestPostgresC2PaidCashStopUsesSeparateCashRefund(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
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
	order := commitTwoLineCashOrder(t, ctx, f, grant, "c2-paid-cash")
	state, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "staff", ExpectedVersion: 1, IdempotencyKey: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: preview.Fingerprint, TenderedJPY: 5000, RegisterSessionRef: "shift", IdempotencyKey: "settle"}); err != nil {
		t.Fatal(err)
	}
	detail, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	state = detail.Orders[0]
	state, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "prepare", Target: operations.Preparing})
	if err != nil {
		t.Fatal(err)
	}
	state, err = f.Operations.RequestStop(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "request", Reason: "reduce paid cash order", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	stop := state.CancellationRequests[0]
	state, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "confirm"}, RequestID: stop.ID, Action: "confirm", ExpectedRequestVersion: stop.Version})
	if err != nil {
		t.Fatal(err)
	}
	stop = state.CancellationRequests[0]
	finalize := operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "finalize"}, RequestID: stop.ID, Action: "finalize", ExpectedRequestVersion: stop.Version, NonDeliveryConfirmed: true}
	if _, err = f.Operations.ActOnStopRequest(ctx, finalize); !errors.Is(err, operations.ErrCashHandBackRequired) {
		t.Fatalf("paid cash finalized without hand-back: %v", err)
	}
	finalize.CashHandBackConfirmed = true
	state, err = f.Operations.ActOnStopRequest(ctx, finalize)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Refunds) != 1 || state.Refunds[0].Status != "SUCCEEDED" || state.Refunds[0].AmountJPY != 330 || state.EffectiveTotalGrossJPY >= state.TotalGrossJPY {
		t.Fatalf("paid CASH stop=%+v", state)
	}
	var ledger int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM cash_refund_ledger WHERE order_id=$1 AND amount_jpy=330`, order.ID).Scan(&ledger); err != nil || ledger != 1 {
		t.Fatalf("cash refund ledger=%d err=%v", ledger, err)
	}
}

func TestPostgresC2FinalizeRacesWithReadyAndCloseUnderSessionLock(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, race := range []string{"ready", "close"} {
		t.Run(race, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			f, grant, scope, state, stop := newRequestedStop(t, ctx, dsn, "c2-finalize-vs-"+race)
			defer f.Close(ctx)
			state, err := f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "confirm"}, RequestID: stop.ID, Action: "confirm", ExpectedRequestVersion: stop.Version})
			if err != nil {
				t.Fatal(err)
			}
			stop = state.CancellationRequests[0]
			block, err := f.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer block.Rollback(context.Background())
			var pid int32
			if err = block.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = block.Exec(ctx, `LOCK TABLE table_sessions IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			actionDone := make(chan error, 1)
			go func() {
				var e error
				if race == "ready" {
					_, e = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "ready", Target: operations.Ready})
				} else {
					e = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "cashier")
				}
				actionDone <- e
			}()
			awaitBlockedBy(t, ctx, f, pid, 1)
			finalizeDone := make(chan error, 1)
			go func() {
				_, e := f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "finalize"}, RequestID: stop.ID, Action: "finalize", ExpectedRequestVersion: stop.Version, NonDeliveryConfirmed: true})
				finalizeDone <- e
			}()
			awaitBlockedBy(t, ctx, f, pid, 2)
			if err = block.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			firstErr, finalErr := <-actionDone, <-finalizeDone
			if race == "ready" && firstErr != nil && !errors.Is(firstErr, operations.ErrStopRequestPending) && !errors.Is(firstErr, operations.ErrConflict) {
				t.Fatalf("READY race returned unexpected result: %v", firstErr)
			}
			if race == "ready" && firstErr == nil {
				t.Fatal("READY succeeded after finalizing the only item")
			}
			if finalErr != nil {
				t.Fatalf("finalize lost race after blocker release: %v", finalErr)
			}
			if race == "close" && firstErr != nil && !errors.Is(firstErr, tablesession.ErrCloseBlocked) {
				t.Fatalf("close race returned unexpected result: %v", firstErr)
			}
			var status string
			if err = f.Pool.QueryRow(ctx, `SELECT fulfillment_status FROM operation_order_state WHERE order_id=$1`, state.ID).Scan(&status); err != nil || status != string(operations.Cancelled) {
				t.Fatalf("finalized last item status=%q err=%v", status, err)
			}
			var adjustments, refunds int
			if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM order_adjustments WHERE order_id=$1`, state.ID).Scan(&adjustments); err != nil || adjustments != 1 {
				t.Fatalf("adjustments=%d err=%v", adjustments, err)
			}
			if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_refunds WHERE order_id=$1`, state.ID).Scan(&refunds); err != nil || refunds != 0 {
				t.Fatalf("refunds=%d err=%v", refunds, err)
			}
			var sessionStatus string
			if err = f.Pool.QueryRow(ctx, `SELECT status FROM table_sessions WHERE session_id=$1`, grant.Session.ID).Scan(&sessionStatus); err != nil {
				t.Fatal(err)
			}
			if (firstErr == nil) != (sessionStatus == "CLOSED") {
				t.Fatalf("close result=%v session status=%s", firstErr, sessionStatus)
			}
		})
	}
}

func TestPostgresC2WithdrawRacesWithKitchenConfirmUnderSessionLock(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f, grant, scope, state, stop := newRequestedStop(t, ctx, dsn, "c2-withdraw-vs-confirm")
	defer f.Close(ctx)
	block, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(context.Background())
	var pid int32
	if err = block.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = block.Exec(ctx, `LOCK TABLE table_sessions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	withdrawDone := make(chan error, 1)
	go func() {
		_, e := f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "withdraw"}, RequestID: stop.ID, Action: "withdraw", ExpectedRequestVersion: stop.Version})
		withdrawDone <- e
	}()
	awaitBlockedBy(t, ctx, f, pid, 1)
	confirmDone := make(chan error, 1)
	go func() {
		_, e := f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "kitchen", ExpectedVersion: state.Version, IdempotencyKey: "confirm"}, RequestID: stop.ID, Action: "confirm", ExpectedRequestVersion: stop.Version})
		confirmDone <- e
	}()
	awaitBlockedBy(t, ctx, f, pid, 2)
	if err = block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w, c := <-withdrawDone, <-confirmDone
	if (w == nil) == (c == nil) {
		t.Fatalf("withdraw/confirm should have a single CAS winner: withdraw=%v confirm=%v", w, c)
	}
	loser := w
	if loser == nil {
		loser = c
	}
	if !errors.Is(loser, operations.ErrConflict) {
		t.Fatalf("losing withdraw/confirm action was not rejected by CAS: %v", loser)
	}
	final, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	status := final.Orders[0].CancellationRequests[0].Status
	if status != "WITHDRAWN" && status != "KITCHEN_CONFIRMED" {
		t.Fatalf("race left invalid request status %q", status)
	}
}
