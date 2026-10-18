package payments

import (
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

func TestPostgresP82RefundDelayedFirstDispatch(t *testing.T) {
	for _, unconfigured := range []bool{false, true} {
		t.Run(fmt.Sprintf("unconfigured-%v", unconfigured), func(t *testing.T) {
			h := newHoldFixture(t)
			order := createHeldPaidReview(t, h)
			h.work()
			// Simulate a refund waiting more than the old 20h horizon, without
			// rewriting immutable history or waiting on a wall clock.
			if _, err := h.Pool.Exec(h.ctx, `ALTER TABLE operation_refunds ALTER COLUMN created_at SET DEFAULT (clock_timestamp()-interval '21 hours')`); err != nil {
				t.Fatal(err)
			}
			scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, order.Order.ID), IdempotencyKey: "delayed-cancel", Reason: "cancel unavailable item", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			refundID := o.Refunds[0].ID
			var untouched bool
			if err = h.Pool.QueryRow(h.ctx, `SELECT retry_not_after IS NULL AND created_at < clock_timestamp()-interval '20 hours' FROM operation_refunds WHERE refund_id=$1`, refundID).Scan(&untouched); err != nil || !untouched {
				t.Fatalf("undispatched horizon started: %v %v", untouched, err)
			}
			resolver := h.service.resolver
			if unconfigured {
				h.service.resolver = nil
				for i := 0; i < 2; i++ {
					h.ready()
					h.work()
				}
				var ready bool
				if err = h.Pool.QueryRow(h.ctx, `SELECT r.retry_not_after IS NULL AND r.provider_refund_id IS NULL AND w.status='READY' AND w.available_at>w.updated_at AND w.safe_error_code='REFUND_DISPATCH_PENDING' FROM operation_refunds r JOIN stripe_payment_work w USING(refund_id) WHERE r.refund_id=$1`, refundID).Scan(&ready); err != nil || !ready {
					t.Fatalf("unconfigured work not pending with backoff: %v %v", ready, err)
				}
				detail, e := h.Operations.ReadSession(h.ctx, scope, o.SessionID)
				if e != nil {
					t.Fatal(e)
				}
				if got := sessionOrderByID(t, detail.Orders, o.ID).Refunds[0].SafeErrorCode; got != "REFUND_DISPATCH_PENDING" {
					t.Fatalf("staff code=%s", got)
				}
				if creates, _ := h.provider.RefundOperationCounts(); creates != 0 {
					t.Fatalf("unconfigured creates=%d", creates)
				}
				h.service.resolver = resolver
				h.ready()
			}
			h.provider.SetNextRefundOutcome("pending", false)
			h.work()
			var deadline time.Time
			var anchored bool
			if err = h.Pool.QueryRow(h.ctx, `SELECT retry_not_after,retry_not_after>clock_timestamp()+interval '19 hours' FROM operation_refunds WHERE refund_id=$1`, refundID).Scan(&deadline, &anchored); err != nil || !anchored {
				t.Fatalf("dispatch horizon=%v anchored=%v %v", deadline, anchored, err)
			}
			refund, ok := h.provider.RefundByBusinessID(refundID)
			if !ok {
				t.Fatal("missing provider refund")
			}
			if _, err = h.provider.SetRefundOutcome(refund.ID, "succeeded"); err != nil {
				t.Fatal(err)
			}
			h.ready()
			h.work()
			var unchanged bool
			if err = h.Pool.QueryRow(h.ctx, `SELECT retry_not_after=$2 AND status='SUCCEEDED' FROM operation_refunds WHERE refund_id=$1`, refundID, deadline).Scan(&unchanged); err != nil || !unchanged {
				t.Fatalf("horizon changed=%v %v", !unchanged, err)
			}
			if _, err = h.Pool.Exec(h.ctx, `UPDATE operation_refunds SET retry_not_after=retry_not_after+interval '1 hour' WHERE refund_id=$1`, refundID); err == nil {
				t.Fatal("deadline extension allowed")
			}
			if _, err = h.Pool.Exec(h.ctx, `UPDATE operation_refunds SET retry_not_after=NULL WHERE refund_id=$1`, refundID); err == nil {
				t.Fatal("deadline reset allowed")
			}
			if creates, _ := h.provider.RefundOperationCounts(); creates != 1 {
				t.Fatalf("delayed refund creates=%d", creates)
			}
		})
	}
}

func TestPostgresP82RefundExpiredAmbiguousDispatchStaysPending(t *testing.T) {
	h := newHoldFixture(t)
	order := createHeldPaidReview(t, h)
	h.work()
	scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
	o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, order.Order.ID), IdempotencyKey: "expired-cancel", Reason: "cancel unavailable item", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.service.loadRefundWork(h.ctx, o.Refunds[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct a first dispatch 21h ago with a lost provider response. Set
	// the once-only anchor before that synthetic dispatch; never extend/reset it.
	if _, err = h.Pool.Exec(h.ctx, `UPDATE operation_refunds SET status='PROCESSING',retry_not_after=clock_timestamp()-interval '1 hour' WHERE refund_id=$1`, r.RefundID); err != nil {
		t.Fatal(err)
	}
	resolved, err := h.service.resolve(h.ctx, r.Credential, r.Account, r.Mode)
	if err != nil {
		t.Fatal(err)
	}
	h.provider.SetNextRefundOutcome("pending", true)
	_, err = h.provider.CreateRefund(h.ctx, stripeprovider.CreateRefundRequest{Account: resolved.Account, TenantID: r.TenantID, StoreID: r.StoreID, OrderID: r.OrderID, RefundID: r.RefundID, PaymentIntentID: r.PaymentIntent, IdempotencyKey: r.Key, AmountJPY: r.Amount})
	if !errors.Is(err, stripeprovider.ErrOutcomeUnknown) {
		t.Fatalf("ambiguous dispatch: %v", err)
	}
	for i := 0; i < 2; i++ {
		h.ready()
		h.work()
	}
	if creates, _ := h.provider.RefundOperationCounts(); creates != 1 {
		t.Fatalf("expired horizon repeated create: %d", creates)
	}
	var pending bool
	if err = h.Pool.QueryRow(h.ctx, `SELECT r.status='PROCESSING' AND r.provider_refund_id IS NULL AND w.status='READY' AND w.safe_error_code='HOLD_RECONCILIATION_NEEDED' AND w.available_at>w.updated_at FROM operation_refunds r JOIN stripe_payment_work w USING(refund_id) WHERE r.refund_id=$1`, r.RefundID).Scan(&pending); err != nil || !pending {
		t.Fatalf("ambiguous work stranded: %v %v", pending, err)
	}
	detail, err := h.Operations.ReadSession(h.ctx, scope, o.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionOrderByID(t, detail.Orders, o.ID).Refunds[0].SafeErrorCode; got != "HOLD_RECONCILIATION_NEEDED" {
		t.Fatalf("staff code=%s", got)
	}
	if _, err = h.Operations.RetryRefund(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, o.ID), IdempotencyKey: "unsafe-retry"}, r.RefundID); !errors.Is(err, operations.ErrRefundNotRetryable) {
		t.Fatalf("ambiguous refund got fresh key: %v", err)
	}
	tx, err := h.Pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.service.CheckClose(h.ctx, tx, tablesession.Session{ID: o.SessionID}); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("ambiguous refund allowed close: %v", err)
	}
	tx.Rollback(h.ctx)
	refund, ok := h.provider.RefundByBusinessID(r.RefundID)
	if !ok {
		t.Fatal("missing provider refund")
	}
	refund, err = h.provider.SetRefundOutcome(refund.ID, "succeeded")
	if err != nil {
		t.Fatal(err)
	}
	event := stripeprovider.VerifiedEvent{ID: "evt-expired-recovered", Type: "refund.updated", AccountID: refund.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: refund.ID, Refund: &refund}
	if accepted, e := h.service.EnqueueVerifiedWebhook(h.ctx, event); e != nil || !accepted {
		t.Fatalf("recovery webhook: %v %v", accepted, e)
	}
	h.work()
	detail, err = h.Operations.ReadSession(h.ctx, scope, o.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionOrderByID(t, detail.Orders, o.ID); got.PaymentStatus != operations.Refunded || got.Refunds[0].SafeErrorCode != "" {
		t.Fatalf("reconciled=%+v", got)
	}
}

func TestPostgresP82RefundAmbiguousDispatchRetainsKeyAndHorizon(t *testing.T) {
	h := newHoldFixture(t)
	order := createHeldPaidReview(t, h)
	h.work()
	scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
	o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, order.Order.ID), IdempotencyKey: "ambiguous-cancel", Reason: "cancel unavailable item", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	h.provider.SetNextRefundOutcome("pending", true)
	h.work()
	before, err := h.service.loadRefundWork(h.ctx, o.Refunds[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.ProviderRefundID != "" || before.RetryNotAfter == nil {
		t.Fatalf("ambiguous binding/horizon=%+v", before)
	}
	providerRefund, ok := h.provider.RefundByBusinessID(before.RefundID)
	if !ok {
		t.Fatal("missing provider refund after ambiguous dispatch")
	}
	h.ready()
	h.work()
	after, err := h.service.loadRefundWork(h.ctx, before.RefundID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ProviderRefundID != providerRefund.ID || after.Key != before.Key || after.Generation != before.Generation || after.RetryNotAfter == nil || !after.RetryNotAfter.Equal(*before.RetryNotAfter) {
		t.Fatalf("ambiguous recovery changed attempt: before=%+v after=%+v", before, after)
	}
	if creates, _ := h.provider.RefundOperationCounts(); creates != 2 {
		t.Fatalf("expected two same-key calls, got %d", creates)
	}
}

func TestPostgresP82RefundSucceededThenFailed(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, viaWorker := range []bool{false, true} {
			t.Run(fmt.Sprintf("partial-%v/worker-%v", partial, viaWorker), func(t *testing.T) {
				h := newHoldFixture(t)
				result := commitC1TwoLineOrder(t, h)
				h.provider.SetNextCreateOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
				h.work()
				scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
				lines := []operations.AdjustmentLine{{LineIndex: 1, CancelledQuantity: 1}}
				if !partial {
					lines = append(lines, operations.AdjustmentLine{LineIndex: 0, CancelledQuantity: 1})
				}
				o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: scope, SessionID: result.Order.SessionID, OrderID: result.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, result.Order.ID), IdempotencyKey: "cancel-late-failure", Reason: "cancel unavailable items", Lines: lines})
				if err != nil {
					t.Fatal(err)
				}
				refundID := o.Refunds[0].ID
				read := func() operations.OrderState {
					t.Helper()
					detail, e := h.Operations.ReadSession(h.ctx, scope, o.SessionID)
					if e != nil {
						t.Fatal(e)
					}
					return sessionOrderByID(t, detail.Orders, o.ID)
				}
				checkClose := func() error {
					t.Helper()
					tx, e := h.Pool.Begin(h.ctx)
					if e != nil {
						return e
					}
					defer tx.Rollback(h.ctx)
					return h.service.CheckClose(h.ctx, tx, tablesession.Session{ID: o.SessionID})
				}
				if viaWorker {
					h.provider.SetNextRefundOutcome("pending", false)
				} else {
					h.provider.SetNextRefundOutcome("succeeded", false)
				}
				h.work()
				refund, ok := h.provider.RefundByBusinessID(refundID)
				if !ok {
					t.Fatal("missing provider refund")
				}
				enqueue := func(id string, observation stripeprovider.RefundResult) bool {
					t.Helper()
					accepted, e := h.service.EnqueueVerifiedWebhook(h.ctx, stripeprovider.VerifiedEvent{ID: id, Type: "refund.updated", AccountID: observation.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: observation.ID, Refund: &observation})
					if e != nil {
						t.Fatal(e)
					}
					return accepted
				}
				if viaWorker {
					// Stale failure payload must reconcile to the retrieved success.
					if _, err = h.provider.SetRefundOutcome(refund.ID, "succeeded"); err != nil {
						t.Fatal(err)
					}
					stale := refund
					stale.Status = "failed"
					if !enqueue("evt-stale-failure", stale) {
						t.Fatal("missing webhook work")
					}
					h.work()
				}
				o = read()
				wantPayment := operations.Refunded
				if partial {
					wantPayment = operations.Paid
				}
				if o.Refunds[0].Status != "SUCCEEDED" || o.PaymentStatus != wantPayment {
					t.Fatalf("initial success=%+v", o)
				}
				if partial {
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
				}
				if err = checkClose(); err != nil {
					t.Fatalf("success blocks close: %v", err)
				}
				if viaWorker {
					if _, err = h.provider.SetRefundOutcome(refund.ID, "failed"); err != nil {
						t.Fatal(err)
					}
					h.ready()
				} else {
					if _, err = h.provider.SetRefundOutcome(refund.ID, "failed"); err != nil {
						t.Fatal(err)
					}
					// Even a delayed success event must retrieve the later failure.
					stale := refund
					stale.Status = "succeeded"
					if !enqueue("evt-late-failure", stale) {
						t.Fatal("missing webhook work")
					}
				}
				h.work()
				o = read()
				if !partial {
					wantPayment = operations.RefundPending
				}
				if o.Refunds[0].Status != "FAILED" || o.PaymentStatus != wantPayment {
					t.Fatalf("late failure=%+v", o)
				}
				if err = checkClose(); !errors.Is(err, tablesession.ErrCloseBlocked) {
					t.Fatalf("late failure close gate: %v", err)
				}
				if err = h.Sessions.Close(h.ctx, tablesession.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID, TableID: scope.TableID}, o.SessionID, "manager"); !errors.Is(err, tablesession.ErrCloseBlocked) {
					t.Fatalf("late failure allowed close: %v", err)
				}
				queue, err := h.Operations.ListPaymentReviews(h.ctx, operations.StoreScope{TenantID: h.TenantID, StoreID: h.StoreID})
				if err != nil {
					t.Fatal(err)
				}
				if got := sessionOrderByID(t, queue, o.ID); got.Refunds[0].Status != "FAILED" {
					t.Fatalf("retry hidden in queue: %+v", got)
				}
				var audits int
				if err = h.Pool.QueryRow(h.ctx, `SELECT count(*) FROM operation_order_events WHERE order_id=$1 AND event_type='REFUND_STATUS_CHANGED' AND before_state->>'refund_status'='SUCCEEDED' AND after_state->>'refund_status'='FAILED' AND before_payment_status=$2 AND after_payment_status=$3`, o.ID, map[bool]string{true: "PAID", false: "REFUNDED"}[partial], string(wantPayment)).Scan(&audits); err != nil || audits != 1 {
					t.Fatalf("late failure audit=%d %v", audits, err)
				}
				version := o.Version
				stale := refund
				stale.Status = "succeeded"
				if !enqueue("evt-duplicate-failure", stale) {
					t.Fatal("missing webhook work")
				}
				h.work()
				if enqueue("evt-duplicate-failure", stale) {
					t.Fatal("duplicate webhook accepted")
				}
				if got := read(); got.Version != version || got.Refunds[0].Status != "FAILED" {
					t.Fatalf("stale event changed failure: %+v", got)
				}
				// An event admitted before retry is stale when its work is processed.
				if !enqueue("evt-old-generation-queued", stale) {
					t.Fatal("missing webhook work")
				}
				o, err = h.Operations.RetryRefund(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: o.SessionID, OrderID: o.ID, Actor: "manager", ExpectedVersion: o.Version, IdempotencyKey: "retry-late-failure"}, refundID)
				if err != nil {
					t.Fatalf("manager retry: %v", err)
				}
				var fresh bool
				if err = h.Pool.QueryRow(h.ctx, `SELECT generation=2 AND retry_not_after IS NULL AND provider_refund_id IS NULL FROM operation_refunds WHERE refund_id=$1`, refundID).Scan(&fresh); err != nil || !fresh {
					t.Fatalf("retry generation/horizon: %v %v", fresh, err)
				}
				h.work()
				if got := read(); got.Refunds[0].Status != "REQUESTED" {
					t.Fatalf("old generation changed retry: %+v", got)
				}
				h.provider.SetNextRefundOutcome("succeeded", false)
				h.work()
				if enqueue("evt-old-generation-after-retry", refund) {
					t.Fatal("old provider object accepted")
				}
				if err = checkClose(); err != nil {
					t.Fatalf("retry success blocks close: %v", err)
				}
				creates, retrieves := h.provider.RefundOperationCounts()
				if creates != 2 || retrieves < 2 {
					t.Fatalf("provider operations creates=%d retrieves=%d", creates, retrieves)
				}
			})
		}
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
