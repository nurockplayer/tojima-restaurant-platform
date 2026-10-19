package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

func voidRequest(h *holdFixture, order ordering.CommitResult) VoidRequest {
	return VoidRequest{Scope: operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "fixture-manager", ExpectedVersion: reviewOrderVersion(h.t, h, order.Order.ID), IdempotencyKey: "void-unpaid", Reason: "guest left without paying"}
}

func voidOrder(t *testing.T, h *holdFixture, r VoidRequest) operations.OrderState {
	t.Helper()
	state, err := h.Operations.ReadSession(h.ctx, r.Scope, r.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return sessionOrderByID(t, state.Orders, r.OrderID)
}

func checkVoidClose(t *testing.T, h *holdFixture, r VoidRequest, want error) {
	t.Helper()
	tx, err := h.Pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(h.ctx)
	session := tablesession.Session{ID: r.SessionID, Scope: tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}}
	if err = h.Operations.CheckClose(h.ctx, tx, session); !errors.Is(err, want) {
		t.Fatalf("operations close=%v want %v", err, want)
	}
	if err = h.service.CheckClose(h.ctx, tx, session); !errors.Is(err, want) {
		t.Fatalf("payment close=%v want %v", err, want)
	}
}

func paymentSessions(t *testing.T, h *holdFixture) *tablesession.Service {
	t.Helper()
	s, err := tablesession.New(h.Pool, tablesession.Options{SessionTTL: 2 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 100, PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: make([]byte, 32)}, h.service)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPostgresD9ExpiredUnpaidStripeOrderCanClose(t *testing.T) {
	h := newHoldFixture(t)
	order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work()
	x := h.checkout(order.Order.ID)
	if _, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutExpired, stripeprovider.PaymentUnpaid); err != nil {
		t.Fatal(err)
	}
	r := voidRequest(h, order)
	status, err := h.service.Reconcile(h.ctx, r.Scope, r.SessionID, r.OrderID, r.Actor)
	if err != nil || status.State != StateExpired {
		t.Fatalf("reconcile=%+v %v", status, err)
	}
	checkVoidClose(t, h, r, tablesession.ErrCloseBlocked)
	got, err := h.service.VoidUnpaidCheckout(h.ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaymentStatus != "VOIDED" || got.FulfillmentStatus != operations.Cancelled || got.EffectiveTotalGrossJPY != 0 {
		t.Fatalf("void=%+v", got)
	}
	h.assertState(r.OrderID, StateExpired, false, false)
	var actor, reason string
	var adjustments int
	if err = h.Pool.QueryRow(h.ctx, `SELECT actor_id,reason FROM operation_order_events WHERE order_id=$1 AND event_type='STRIPE_UNPAID_VOIDED'`, r.OrderID).Scan(&actor, &reason); err != nil || actor != r.Actor || reason != r.Reason {
		t.Fatalf("audit=%s %s %v", actor, reason, err)
	}
	if err = h.Pool.QueryRow(h.ctx, `SELECT count(*) FROM order_adjustments WHERE order_id=$1 AND whole_order_cancel AND effective_total_gross_jpy=0 AND refund_amount_jpy=0`, r.OrderID).Scan(&adjustments); err != nil || adjustments != 1 {
		t.Fatalf("adjustments=%d %v", adjustments, err)
	}
	checkVoidClose(t, h, r, nil)
	h.Sessions = paymentSessions(t, h)
	if err = h.Sessions.Close(h.ctx, tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, r.SessionID, r.Actor); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresD9OpenVoidAndIdempotency(t *testing.T) {
	h := newHoldFixture(t)
	order, proof := h.commit(h.Locator, "tea", ordering.Stripe)
	h.work()
	r := voidRequest(h, order)
	if can, err := h.service.CanResume(h.ctx, r.SessionID, r.OrderID, proof); err != nil || !can {
		t.Fatalf("resume before=%v %v", can, err)
	}
	stale := r
	stale.ExpectedVersion++
	if _, err := h.service.VoidUnpaidCheckout(h.ctx, stale); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("stale=%v", err)
	}
	got, err := h.service.VoidUnpaidCheckout(h.ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	retrieves, expires := h.provider.CheckoutOperationCounts()
	if expires != 1 {
		t.Fatalf("expires=%d", expires)
	}
	replay, err := h.service.VoidUnpaidCheckout(h.ctx, r)
	originalJSON, _ := json.Marshal(got)
	replayJSON, _ := json.Marshal(replay)
	if err != nil || string(originalJSON) != string(replayJSON) {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	if re, ex := h.provider.CheckoutOperationCounts(); re != retrieves || ex != expires {
		t.Fatalf("replay provider calls=%d/%d before=%d/%d", re, ex, retrieves, expires)
	}
	changed := r
	changed.Reason = "different reason"
	if _, err = h.service.VoidUnpaidCheckout(h.ctx, changed); !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("changed reason=%v", err)
	}
	changed = r
	changed.IdempotencyKey = "new-key"
	if _, err = h.service.VoidUnpaidCheckout(h.ctx, changed); !errors.Is(err, ErrVoidNotEligible) {
		t.Fatalf("new key=%v", err)
	}
	changed = r
	changed.Scope.TableID = "other-table"
	if _, err = h.service.VoidUnpaidCheckout(h.ctx, changed); err == nil {
		t.Fatal("cross-table replay exposed snapshot")
	}
	if can, err := h.service.CanResume(h.ctx, r.SessionID, r.OrderID, proof); err != nil || can {
		t.Fatalf("resume after=%v %v", can, err)
	}
	if _, err = h.service.Resume(h.ctx, r.SessionID, r.OrderID, proof); !errors.Is(err, ErrProofDenied) {
		t.Fatalf("resume after void=%v", err)
	}
}

type failingVoidProvider struct{ Provider }

func (p failingVoidProvider) RetrieveCheckout(context.Context, stripeprovider.AccountContext, string) (stripeprovider.CheckoutResult, error) {
	return stripeprovider.CheckoutResult{}, stripeprovider.ErrUnavailable
}

func TestPostgresD9VoidRefusals(t *testing.T) {
	for _, kind := range []string{"complete-unpaid", "retrieve-error", "no-checkout", "cash", "paid", "provider-paid", "held", "processing"} {
		t.Run(kind, func(t *testing.T) {
			h := newHoldFixture(t)
			method := ordering.Stripe
			if kind == "cash" {
				method = ordering.Cash
			}
			order, _ := h.commit(h.Locator, "tea", method)
			if kind != "no-checkout" && kind != "cash" {
				h.work()
			}
			r := voidRequest(h, order)
			if kind == "complete-unpaid" || kind == "paid" || kind == "provider-paid" {
				payment := stripeprovider.PaymentUnpaid
				if kind != "complete-unpaid" {
					payment = stripeprovider.PaymentPaid
				}
				x := h.checkout(r.OrderID)
				if _, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, payment); err != nil {
					t.Fatal(err)
				}
				if kind == "paid" {
					if _, err := h.service.Reconcile(h.ctx, r.Scope, r.SessionID, r.OrderID, r.Actor); err != nil {
						t.Fatal(err)
					}
					r.ExpectedVersion = reviewOrderVersion(t, h, r.OrderID)
				}
			}
			if kind == "retrieve-error" {
				resolver := h.service.resolver.(testResolver)
				resolved := resolver["fixture:hold"]
				resolved.Provider = failingVoidProvider{resolved.Provider}
				resolver["fixture:hold"] = resolved
			}
			if kind == "held" {
				if _, err := h.Pool.Exec(h.ctx, `UPDATE operation_order_state SET operational_hold=true,version=version+1 WHERE order_id=$1`, r.OrderID); err != nil {
					t.Fatal(err)
				}
				r.ExpectedVersion++
			}
			if kind == "processing" {
				if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_attempts SET state='PROCESSING',updated_at=clock_timestamp() WHERE order_id=$1`, r.OrderID); err != nil {
					t.Fatal(err)
				}
			}
			before := voidOrder(t, h, r)
			_, err := h.service.VoidUnpaidCheckout(h.ctx, r)
			want := ErrVoidNotEligible
			if kind == "retrieve-error" {
				want = ErrUnavailable
			}
			if !errors.Is(err, want) {
				t.Fatalf("refusal=%v want %v", err, want)
			}
			after := voidOrder(t, h, r)
			if kind == "provider-paid" {
				if after.PaymentStatus != operations.Paid {
					t.Fatalf("lost paid: %+v", after)
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed order: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestPostgresD9LatePaidRefundsAfterVoid(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprintf("closed-%v", closed), func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
			h.work()
			r := voidRequest(h, order)
			if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); err != nil {
				t.Fatal(err)
			}
			scope := tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			sessions := paymentSessions(t, h)
			if closed {
				if err := sessions.Close(h.ctx, scope, r.SessionID, r.Actor); err != nil {
					t.Fatal(err)
				}
			}
			x := h.checkout(r.OrderID)
			paid, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
			if err != nil {
				t.Fatal(err)
			}
			h.event(paid, "late-paid", "checkout.session.completed")
			h.work()
			got := voidOrder(t, h, r)
			if got.PaymentStatus != operations.RefundPending || got.FulfillmentStatus != operations.Cancelled || got.PaymentReviewRequired || len(got.Refunds) != 1 || got.Refunds[0].AmountJPY != got.TotalGrossJPY {
				t.Fatalf("late paid=%+v", got)
			}
			h.assertState(r.OrderID, StatePaid, false, false)
			checkVoidClose(t, h, r, tablesession.ErrCloseBlocked)
			reviews, err := h.Operations.ListPaymentReviews(h.ctx, operations.StoreScope{TenantID: h.TenantID, StoreID: h.StoreID})
			if err != nil || len(reviews) != 1 || reviews[0].ID != r.OrderID {
				t.Fatalf("reviews=%+v %v", reviews, err)
			}
			h.event(paid, "late-paid-duplicate", "checkout.session.async_payment_succeeded")
			// Process only the duplicate webhook before dispatching the refund.
			if _, err = h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at='infinity' WHERE order_id=$1 AND work_type='REFUND'`, r.OrderID); err != nil {
				t.Fatal(err)
			}
			h.work()
			if got = voidOrder(t, h, r); len(got.Refunds) != 1 {
				t.Fatalf("duplicate refunds=%+v", got.Refunds)
			}
			var paidEvents, lateEvents int
			if err = h.Pool.QueryRow(h.ctx, `SELECT count(*) FILTER(WHERE event_type='STRIPE_PAID_VERIFIED'),count(*) FILTER(WHERE event_type='STRIPE_LATE_PAYMENT_REFUND') FROM operation_order_events WHERE order_id=$1`, r.OrderID).Scan(&paidEvents, &lateEvents); err != nil || paidEvents != 1 || lateEvents != 1 {
				t.Fatalf("paid audit=%d late=%d %v", paidEvents, lateEvents, err)
			}
			h.provider.SetNextRefundOutcome("succeeded", false)
			h.ready()
			h.work()
			got = voidOrder(t, h, r)
			if got.PaymentStatus != operations.Refunded || got.FulfillmentStatus != operations.Cancelled {
				t.Fatalf("refund=%+v", got)
			}
			checkVoidClose(t, h, r, nil)
			if !closed {
				if err = sessions.Close(h.ctx, scope, r.SessionID, r.Actor); err != nil {
					t.Fatal(err)
				}
			}
			replay, err := h.service.VoidUnpaidCheckout(h.ctx, r)
			if err != nil || replay.PaymentStatus != "VOIDED" {
				t.Fatalf("replay after refund=%+v %v", replay, err)
			}
		})
	}
}

func TestPostgresD9VoidSerializesWithPaidWebhook(t *testing.T) {
	for _, voidFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("void-first-%v", voidFirst), func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
			h.work()
			r := voidRequest(h, order)
			x := h.checkout(r.OrderID)
			if _, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutExpired, stripeprovider.PaymentUnpaid); err != nil {
				t.Fatal(err)
			}
			paid := x
			paid.Status = stripeprovider.CheckoutComplete
			paid.PaymentStatus = stripeprovider.PaymentPaid
			paid.URL = ""
			h.event(paid, "void-race-paid", "checkout.session.completed")
			void := func() error { _, err := h.service.VoidUnpaidCheckout(h.ctx, r); return err }
			webhook := func() error {
				worked, err := h.service.ProcessNext(h.ctx)
				if err == nil && !worked {
					return errors.New("no work")
				}
				return err
			}
			first, second := void, webhook
			if !voidFirst {
				first, second = webhook, void
			}
			firstErr, secondErr := runManagerReviewLockRace(t, h, r.SessionID, first, second)
			if firstErr != nil {
				t.Fatalf("first=%v", firstErr)
			}
			if voidFirst && secondErr != nil || !voidFirst && !errors.Is(secondErr, ErrVoidNotEligible) {
				t.Fatalf("second=%v", secondErr)
			}
			got := voidOrder(t, h, r)
			if voidFirst {
				if got.PaymentStatus != operations.RefundPending || len(got.Refunds) != 1 || got.FulfillmentStatus != operations.Cancelled {
					t.Fatalf("void winner lost payment: %+v", got)
				}
			} else if got.PaymentStatus != operations.Paid {
				t.Fatalf("webhook winner lost payment: %+v", got)
			}
			var state string
			if err := h.Pool.QueryRow(h.ctx, `SELECT state FROM stripe_payment_attempts WHERE order_id=$1`, r.OrderID).Scan(&state); err != nil || state != "PAID" {
				t.Fatalf("attempt=%s %v", state, err)
			}
		})
	}
}

// Checkout can expire before Stripe creates its PaymentIntent. The later
// verified payment must bind that intent and remain dispatchable for refund.
type noIntentVoidProvider struct{ Provider }

func (p noIntentVoidProvider) CreateCheckout(ctx context.Context, r stripeprovider.CreateCheckoutRequest) (stripeprovider.CheckoutResult, error) {
	x, err := p.Provider.CreateCheckout(ctx, r)
	if x.PaymentStatus != stripeprovider.PaymentPaid {
		x.PaymentIntentID = ""
	}
	return x, err
}
func (p noIntentVoidProvider) RetrieveCheckout(ctx context.Context, a stripeprovider.AccountContext, id string) (stripeprovider.CheckoutResult, error) {
	x, err := p.Provider.RetrieveCheckout(ctx, a, id)
	if x.PaymentStatus != stripeprovider.PaymentPaid {
		x.PaymentIntentID = ""
	}
	return x, err
}
func (p noIntentVoidProvider) ExpireCheckout(ctx context.Context, a stripeprovider.AccountContext, id, key string) (stripeprovider.CheckoutResult, error) {
	x, err := p.Provider.ExpireCheckout(ctx, a, id, key)
	if x.PaymentStatus != stripeprovider.PaymentPaid {
		x.PaymentIntentID = ""
	}
	return x, err
}

func TestPostgresD9LatePaidBindsIntentForRefund(t *testing.T) {
	h := newHoldFixture(t)
	resolver := h.service.resolver.(testResolver)
	resolved := resolver["fixture:hold"]
	resolved.Provider = noIntentVoidProvider{resolved.Provider}
	resolver["fixture:hold"] = resolved
	order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
	h.work()
	r := voidRequest(h, order)
	if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); err != nil {
		t.Fatal(err)
	}
	var unbound bool
	if err := h.Pool.QueryRow(h.ctx, `SELECT payment_intent_id IS NULL FROM stripe_payment_attempts WHERE order_id=$1`, r.OrderID).Scan(&unbound); err != nil || !unbound {
		t.Fatalf("intent unbound=%v %v", unbound, err)
	}
	x := h.checkout(r.OrderID)
	paid, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	h.event(paid, "late-first-intent", "checkout.session.completed")
	h.work()
	got := voidOrder(t, h, r)
	if got.PaymentStatus != operations.RefundPending || len(got.Refunds) != 1 {
		t.Fatalf("late payment=%+v", got)
	}
	h.provider.SetNextRefundOutcome("succeeded", false)
	h.work()
	if got = voidOrder(t, h, r); got.PaymentStatus != operations.Refunded {
		t.Fatalf("refund could not dispatch=%+v", got)
	}
}

func TestPostgresD9ExpireRecoveryAndPaidObservation(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(fmt.Sprintf("paid-%v", paid), func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
			h.work()
			r := voidRequest(h, order)
			status, payment := stripeprovider.CheckoutExpired, stripeprovider.PaymentUnpaid
			if paid {
				status, payment = stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid
			}
			h.provider.SetNextExpireOutcome(status, payment, true)
			got, err := h.service.VoidUnpaidCheckout(h.ctx, r)
			if paid {
				if !errors.Is(err, ErrVoidNotEligible) || voidOrder(t, h, r).PaymentStatus != operations.Paid {
					t.Fatalf("paid lost after ambiguous expire=%+v %v", got, err)
				}
			} else if err != nil || got.PaymentStatus != "VOIDED" {
				t.Fatalf("expiry recovery=%+v %v", got, err)
			}
			_, expires := h.provider.CheckoutOperationCounts()
			if expires != 1 {
				t.Fatalf("expire count=%d", expires)
			}
		})
	}
}

func TestPostgresD9GuestVoidProjection(t *testing.T) {
	h := newHoldFixture(t)
	order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
	h.work()
	r := voidRequest(h, order)
	if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); err != nil {
		t.Fatal(err)
	}
	status, err := h.Orders.ReadOrderStatus(h.ctx, r.OrderID, order.OrderReadGrant.Token)
	if err != nil || status.PaymentStatus != "VOIDED" || status.DisplayState != "CANCELLED" || status.EffectiveTotalGrossJPY != 0 || status.IsTerminal {
		t.Fatalf("order-read=%+v %v", status, err)
	}
	grant, err := h.Sessions.DirectJoin(h.ctx, h.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := h.Operations.ReadGuestOperations(h.ctx, r.SessionID, grant.Token)
	if err != nil || len(guest.Orders) != 1 || guest.Orders[0].PaymentStatus != operations.Voided || guest.Orders[0].DisplayState != "CANCELLED" {
		t.Fatalf("guest operations=%+v %v", guest, err)
	}
}

func TestPostgresD9PaidReconciliationSurvivesConcurrentVoid(t *testing.T) {
	h := newHoldFixture(t)
	order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
	h.work()
	r := voidRequest(h, order)
	// Capture the reconciliation snapshot before the void commits, then deliver
	// its verified paid observation afterward. Its old timestamp cannot hide paid.
	var a reconcileRow
	if err := h.Pool.QueryRow(h.ctx, `SELECT order_id,attempt_id,session_id,tenant_id,store_id,table_id,credential_ref,provider_mode,stripe_account_id,checkout_session_id,coalesce(payment_intent_id,''),amount_jpy,updated_at FROM stripe_payment_attempts WHERE order_id=$1`, r.OrderID).Scan(&a.order, &a.attempt, &a.session, &a.tenant, &a.store, &a.table, &a.credential, &a.mode, &a.account, &a.checkout, &a.pi, &a.amount, &a.updatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); err != nil {
		t.Fatal(err)
	}
	paid, err := h.provider.SetOutcome(a.checkout, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	status, err := h.service.persistReconciliation(h.ctx, r.Scope, a, &paid, nil)
	if err != nil || status.State != StatePaid {
		t.Fatalf("paid reconciliation=%+v %v", status, err)
	}
	got := voidOrder(t, h, r)
	if got.PaymentStatus != operations.RefundPending || len(got.Refunds) != 1 {
		t.Fatalf("stale paid observation lost: %+v", got)
	}
	h.provider.SetNextRefundOutcome("succeeded", false)
	h.work()
	if got = voidOrder(t, h, r); got.PaymentStatus != operations.Refunded {
		t.Fatalf("stale paid refund=%+v", got)
	}
}

type unpaidIntentVoidProvider struct {
	Provider
	status string
}

func (p unpaidIntentVoidProvider) RetrievePaymentIntent(ctx context.Context, a stripeprovider.AccountContext, id string) (stripeprovider.PaymentIntentResult, error) {
	x, err := p.Provider.RetrievePaymentIntent(ctx, a, id)
	if err == nil && x.Status != "succeeded" {
		x.Status = p.status
	}
	return x, err
}

func TestPostgresD9VoidStaysExpiredAfterReconcile(t *testing.T) {
	for _, intentStatus := range []string{"canceled", "requires_action", "requires_confirmation"} {
		t.Run(intentStatus, func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
			h.work()
			r := voidRequest(h, order)
			resolver := h.service.resolver.(testResolver)
			resolved := resolver["fixture:hold"]
			resolved.Provider = unpaidIntentVoidProvider{Provider: resolved.Provider, status: intentStatus}
			resolver["fixture:hold"] = resolved
			if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); err != nil {
				t.Fatal(err)
			}
			status, err := h.service.Reconcile(h.ctx, r.Scope, r.SessionID, r.OrderID, r.Actor)
			if err != nil || status.State != StateExpired {
				t.Fatalf("void reconcile=%+v %v", status, err)
			}
			checkVoidClose(t, h, r, nil)
			x := h.checkout(r.OrderID)
			if _, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid); err != nil {
				t.Fatal(err)
			}
			status, err = h.service.Reconcile(h.ctx, r.Scope, r.SessionID, r.OrderID, r.Actor)
			if err != nil || status.State != StatePaid {
				t.Fatalf("late paid reconciliation=%+v %v", status, err)
			}
			got := voidOrder(t, h, r)
			if got.PaymentStatus != operations.RefundPending || len(got.Refunds) != 1 {
				t.Fatalf("late paid after reconcile=%+v", got)
			}
			h.provider.SetNextRefundOutcome("succeeded", false)
			h.work()
			if got = voidOrder(t, h, r); got.PaymentStatus != operations.Refunded {
				t.Fatalf("refund after reconcile=%+v", got)
			}
		})
	}
}

type failingExpireVoidProvider struct{ Provider }

func (p failingExpireVoidProvider) ExpireCheckout(context.Context, stripeprovider.AccountContext, string, string) (stripeprovider.CheckoutResult, error) {
	return stripeprovider.CheckoutResult{}, stripeprovider.ErrUnavailable
}

func TestPostgresD9ExpireErrorWithOpenCheckoutIsUnavailable(t *testing.T) {
	h := newHoldFixture(t)
	order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
	h.work()
	r := voidRequest(h, order)
	resolver := h.service.resolver.(testResolver)
	resolved := resolver["fixture:hold"]
	resolved.Provider = failingExpireVoidProvider{resolved.Provider}
	resolver["fixture:hold"] = resolved
	before := voidOrder(t, h, r)
	if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unconfirmed expiry=%v", err)
	}
	if after := voidOrder(t, h, r); !reflect.DeepEqual(before, after) {
		t.Fatalf("unconfirmed expiry changed order=%+v", after)
	}
	status, err := h.service.ReadSafeStatus(h.ctx, r.Scope, r.SessionID, r.OrderID)
	if err != nil || status.State != StateOpen {
		t.Fatalf("unconfirmed expiry changed attempt=%+v %v", status, err)
	}
}

func TestPostgresD9CompanyHoldUnpaidVoidCanClose(t *testing.T) {
	for _, expireFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained-expire-first-%v", expireFirst), func(t *testing.T) {
			h := newHoldFixture(t)
			order, proof := h.commit(h.Locator, "teishoku", ordering.Stripe)
			h.work()
			h.hold(true, 0)
			var holdAt, revokedAt time.Time
			if err := h.Pool.QueryRow(h.ctx, `SELECT company_hold_expire_requested_at,continuation_revoked_at FROM stripe_payment_attempts WHERE order_id=$1`, order.Order.ID).Scan(&holdAt, &revokedAt); err != nil {
				t.Fatal(err)
			}
			r := voidRequest(h, order)
			if expireFirst {
				h.readyOnly(r.OrderID, "EXPIRE")
				h.work()
				h.assertState(r.OrderID, StateExpired, false, false)
			}
			if got := voidOrder(t, h, r); got.PaymentStatus != operations.Pending {
				t.Fatalf("hold must leave payment pending: %+v", got)
			}
			checkVoidClose(t, h, r, tablesession.ErrCloseBlocked)
			retrieves, expires := h.provider.CheckoutOperationCounts()
			got, err := h.service.VoidUnpaidCheckout(h.ctx, r)
			if err != nil || got.PaymentStatus != operations.Voided || got.FulfillmentStatus != operations.Cancelled || got.EffectiveTotalGrossJPY != 0 {
				t.Fatalf("held void=%+v %v", got, err)
			}
			wantExpires := expires
			if !expireFirst {
				wantExpires++
			}
			if re, ex := h.provider.CheckoutOperationCounts(); re != retrieves+1 || ex != wantExpires {
				t.Fatalf("void provider calls=%d/%d want=%d/%d", re, ex, retrieves+1, wantExpires)
			}
			checkVoidClose(t, h, r, nil)
			if err = paymentSessions(t, h).Close(h.ctx, tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, r.SessionID, r.Actor); err != nil {
				t.Fatal(err)
			}
			if !expireFirst {
				var ready bool
				if err = h.Pool.QueryRow(h.ctx, `SELECT status='READY' FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'`, r.OrderID).Scan(&ready); err != nil || !ready {
					t.Fatalf("void must retain pending expiry: ready=%v %v", ready, err)
				}
				before := voidOrder(t, h, r)
				retrieves, expires = h.provider.CheckoutOperationCounts()
				h.readyOnly(r.OrderID, "EXPIRE")
				h.work()
				if re, ex := h.provider.CheckoutOperationCounts(); re != retrieves+1 || ex != expires {
					t.Fatalf("retained expiry must retrieve without expiring again: %d/%d before=%d/%d", re, ex, retrieves, expires)
				}
				if after := voidOrder(t, h, r); !reflect.DeepEqual(before, after) {
					t.Fatalf("retained expiry changed void: before=%+v after=%+v", before, after)
				}
			}
			var safe bool
			if err = h.Pool.QueryRow(h.ctx, `SELECT a.company_hold_expire_requested_at=$2 AND a.continuation_revoked_at=$3
				AND a.checkout_status='expired' AND a.provider_payment_status='unpaid'
				AND (SELECT count(*)=1 AND bool_and(status='DONE') FROM stripe_payment_work WHERE order_id=a.order_id AND work_type='EXPIRE')
				AND EXISTS(SELECT 1 FROM company_product_emergency_holds WHERE tenant_id=a.tenant_id AND product_key='teishoku' AND active)
				FROM stripe_payment_attempts a WHERE a.order_id=$1`, r.OrderID, holdAt, revokedAt).Scan(&safe); err != nil || !safe {
				t.Fatalf("hold markers or expiry outcome lost: safe=%v %v", safe, err)
			}
			h.assertState(r.OrderID, StateExpired, false, false)
			checkVoidClose(t, h, r, nil)
			if can, err := h.service.CanResume(h.ctx, r.SessionID, r.OrderID, proof); err != nil || can {
				t.Fatalf("held void can resume=%v %v", can, err)
			}
			if _, err = h.service.Resume(h.ctx, r.SessionID, r.OrderID, proof); !errors.Is(err, ErrProofDenied) {
				t.Fatalf("held void resume=%v", err)
			}
			if worked, err := h.service.ProcessNext(h.ctx); err != nil || worked {
				t.Fatalf("unexpected work after expiry: worked=%v %v", worked, err)
			}
			if creates, sessions := h.provider.Counts(); creates != 1 || sessions != 1 {
				t.Fatalf("held void created another Checkout: %d/%d", creates, sessions)
			}
		})
	}
}

func TestPostgresD9CompanyHoldVoidLatePaidRefunds(t *testing.T) {
	for _, source := range []string{"verified-webhook", "retained-expire"} {
		t.Run(source, func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
			h.work()
			h.hold(true, 0)
			r := voidRequest(h, order)
			if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); err != nil {
				t.Fatal(err)
			}
			x := h.checkout(r.OrderID)
			paid, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
			if err != nil {
				t.Fatal(err)
			}
			if source == "verified-webhook" {
				h.readyOnly(r.OrderID, "WEBHOOK") // Park retained expiry until payment is applied.
				h.event(paid, "held-void-late-paid", "checkout.session.completed")
			} else {
				h.readyOnly(r.OrderID, "EXPIRE")
				h.work() // A fresh paid retrieval queues verified reconciliation.
				var queued bool
				if err = h.Pool.QueryRow(h.ctx, `SELECT status='DONE' AND safe_error_code='PAID_RECONCILIATION_QUEUED' FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'`, r.OrderID).Scan(&queued); err != nil || !queued {
					t.Fatalf("expiry lost paid observation: queued=%v %v", queued, err)
				}
			}
			h.work()
			got := voidOrder(t, h, r)
			if got.PaymentStatus != operations.RefundPending || got.FulfillmentStatus != operations.Cancelled || got.PaymentReviewRequired || got.OperationalHold || len(got.Refunds) != 1 || got.Refunds[0].AmountJPY != got.TotalGrossJPY {
				t.Fatalf("held void late payment=%+v", got)
			}
			h.assertState(r.OrderID, StatePaid, false, false)
			checkVoidClose(t, h, r, tablesession.ErrCloseBlocked)
			h.readyOnly(r.OrderID, "EXPIRE")
			if source == "verified-webhook" {
				h.work() // Already-paid retained expiry completes without changing the order.
				if after := voidOrder(t, h, r); !reflect.DeepEqual(got, after) {
					t.Fatalf("paid expiry changed refund: before=%+v after=%+v", got, after)
				}
			}
			h.provider.SetNextRefundOutcome("succeeded", false)
			h.readyOnly(r.OrderID, "REFUND")
			h.work()
			got = voidOrder(t, h, r)
			if got.PaymentStatus != operations.Refunded || got.FulfillmentStatus != operations.Cancelled || len(got.Refunds) != 1 {
				t.Fatalf("held void automatic refund=%+v", got)
			}
			var safe bool
			if err = h.Pool.QueryRow(h.ctx, `SELECT a.company_hold_expire_requested_at IS NOT NULL AND a.continuation_revoked_at IS NOT NULL
				AND (SELECT count(*)=1 AND bool_and(status='DONE') FROM stripe_payment_work WHERE order_id=a.order_id AND work_type='EXPIRE')
				AND (SELECT count(*)=1 FROM operation_order_events WHERE order_id=a.order_id AND event_type='STRIPE_LATE_PAYMENT_REFUND')
				FROM stripe_payment_attempts a WHERE a.order_id=$1`, r.OrderID).Scan(&safe); err != nil || !safe {
				t.Fatalf("held refund safety=%v %v", safe, err)
			}
			h.assertState(r.OrderID, StatePaid, false, false)
			checkVoidClose(t, h, r, nil)
			if creates, sessions := h.provider.Counts(); creates != 1 || sessions != 1 {
				t.Fatalf("held refund created another Checkout: %d/%d", creates, sessions)
			}
			if creates, _ := h.provider.RefundOperationCounts(); creates != 1 {
				t.Fatalf("refund provider creates=%d", creates)
			}
		})
	}
}

func TestPostgresD9CompanyHoldVoidRefusesAndPersistsPaid(t *testing.T) {
	for _, paidOnExpire := range []bool{false, true} {
		t.Run(fmt.Sprintf("paid-on-expire-%v", paidOnExpire), func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
			h.work()
			h.hold(true, 0)
			r := voidRequest(h, order)
			if paidOnExpire {
				h.provider.SetNextExpireOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid, false)
			} else {
				x := h.checkout(r.OrderID)
				if _, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); !errors.Is(err, ErrVoidNotEligible) {
				t.Fatalf("held paid void=%v", err)
			}
			h.assertState(r.OrderID, StateReviewRequired, true, true)
			if got := voidOrder(t, h, r); got.PaymentStatus != operations.Paid || got.FulfillmentStatus != operations.Waiting || len(got.Refunds) != 0 {
				t.Fatalf("held paid observation lost: %+v", got)
			}
			checkVoidClose(t, h, r, tablesession.ErrCloseBlocked)
		})
	}
}

func TestPostgresD9VoidedRetainedExpiryUnpaidObservationThenPaid(t *testing.T) {
	for _, status := range []stripeprovider.CheckoutStatus{stripeprovider.CheckoutComplete, stripeprovider.CheckoutOpen, stripeprovider.CheckoutExpired} {
		t.Run(string(status), func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
			h.work()
			h.hold(true, 0)
			r := voidRequest(h, order)
			if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); err != nil {
				t.Fatal(err)
			}
			before := voidOrder(t, h, r)
			x := h.checkout(r.OrderID)
			// Model a delayed unpaid observation arriving after the void has
			// committed. Even complete/unpaid cannot revoke its close proof.
			if _, err := h.provider.SetOutcome(x.SessionID, status, stripeprovider.PaymentUnpaid); err != nil {
				t.Fatal(err)
			}
			if status == stripeprovider.CheckoutOpen {
				h.provider.SetNextExpireOutcome(status, stripeprovider.PaymentUnpaid, false)
			}
			h.readyOnly(r.OrderID, "EXPIRE")
			h.work()
			h.assertState(r.OrderID, StateExpired, false, false)
			var completed bool
			if err := h.Pool.QueryRow(h.ctx, `SELECT count(*)=1 AND bool_and(status='DONE' AND lease_token IS NULL AND lease_until IS NULL)
				FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'`, r.OrderID).Scan(&completed); err != nil || !completed {
				t.Fatalf("voided unpaid expiry must complete: done=%v err=%v", completed, err)
			}
			if after := voidOrder(t, h, r); !reflect.DeepEqual(before, after) {
				t.Fatalf("unpaid observation changed void: before=%+v after=%+v", before, after)
			}
			checkVoidClose(t, h, r, nil)
			if worked, err := h.service.ProcessNext(h.ctx); err != nil || worked {
				t.Fatalf("voided expiry kept polling: worked=%v err=%v", worked, err)
			}

			paid, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
			if err != nil {
				t.Fatal(err)
			}
			h.event(paid, "paid-after-voided-expiry", "checkout.session.completed")
			h.work()
			h.assertState(r.OrderID, StatePaid, false, false)
			got := voidOrder(t, h, r)
			if got.PaymentStatus != operations.RefundPending || got.FulfillmentStatus != operations.Cancelled || len(got.Refunds) != 1 || got.Refunds[0].AmountJPY != got.TotalGrossJPY {
				t.Fatalf("late paid refund=%+v", got)
			}
			checkVoidClose(t, h, r, tablesession.ErrCloseBlocked)
			h.event(paid, "duplicate-paid-after-voided-expiry", "checkout.session.async_payment_succeeded")
			h.readyOnly(r.OrderID, "WEBHOOK")
			h.work()
			var refunds, refundWork, paidEvents, lateEvents int
			if err = h.Pool.QueryRow(h.ctx, `SELECT
				(SELECT count(*) FROM operation_refunds WHERE order_id=$1),
				(SELECT count(*) FROM stripe_payment_work WHERE order_id=$1 AND work_type='REFUND'),
				count(*) FILTER(WHERE event_type='STRIPE_PAID_VERIFIED'),
				count(*) FILTER(WHERE event_type='STRIPE_LATE_PAYMENT_REFUND')
				FROM operation_order_events WHERE order_id=$1`, r.OrderID).Scan(&refunds, &refundWork, &paidEvents, &lateEvents); err != nil || refunds != 1 || refundWork != 1 || paidEvents != 1 || lateEvents != 1 {
				t.Fatalf("refund reservation=%d work=%d paid=%d late=%d err=%v", refunds, refundWork, paidEvents, lateEvents, err)
			}
			h.provider.SetNextRefundOutcome("succeeded", false)
			h.readyOnly(r.OrderID, "REFUND")
			h.work()
			got = voidOrder(t, h, r)
			if got.PaymentStatus != operations.Refunded || got.FulfillmentStatus != operations.Cancelled || len(got.Refunds) != 1 || got.Refunds[0].Status != "SUCCEEDED" {
				t.Fatalf("completed refund=%+v", got)
			}
			if creates, _ := h.provider.RefundOperationCounts(); creates != 1 {
				t.Fatalf("refund provider creates=%d", creates)
			}
			checkVoidClose(t, h, r, nil)
			if err = paymentSessions(t, h).Close(h.ctx, tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, r.SessionID, r.Actor); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresD9VoidPreservedByNonPaidWriters(t *testing.T) {
	for _, writer := range []string{"checkout-open", "checkout-complete", "webhook-complete", "webhook-failed", "attempt-failure", "attempt-unknown", "expired-retry-window"} {
		t.Run(writer, func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "tea", ordering.Stripe)
			h.work()
			r := voidRequest(h, order)
			if _, err := h.service.VoidUnpaidCheckout(h.ctx, r); err != nil {
				t.Fatal(err)
			}
			before := voidOrder(t, h, r)
			x := h.checkout(r.OrderID)
			if writer == "webhook-complete" || writer == "webhook-failed" {
				x.Status = stripeprovider.CheckoutComplete
				kind := "checkout.session.completed"
				if writer == "webhook-failed" {
					kind = "checkout.session.async_payment_failed"
				}
				h.event(x, "unpaid-after-void", kind)
				h.work()
			} else {
				// Re-lease the original CREATE to model its delayed result or
				// failure arriving after the void, under a valid durable lease.
				w := workItem{OrderID: r.OrderID, Lease: "delayed-create-after-void"}
				if err := h.Pool.QueryRow(h.ctx, `UPDATE stripe_payment_work SET status='LEASED',lease_token=$2,lease_until=clock_timestamp()+interval '1 minute'
					WHERE order_id=$1 AND work_type='CREATE' RETURNING work_id`, r.OrderID, w.Lease).Scan(&w.ID); err != nil {
					t.Fatal(err)
				}
				var err error
				switch writer {
				case "checkout-open", "checkout-complete":
					status := stripeprovider.CheckoutOpen
					if writer == "checkout-complete" {
						status = stripeprovider.CheckoutComplete
					}
					if _, err = h.provider.SetOutcome(x.SessionID, status, stripeprovider.PaymentUnpaid); err == nil {
						err = h.service.processCreate(h.ctx, w)
					}
				case "attempt-failure":
					err = h.service.updateAttemptFailure(h.ctx, w, r.OrderID, "DELAYED_FAILURE", false)
				case "attempt-unknown":
					err = h.service.setAttemptUnknown(h.ctx, w, r.OrderID, "DELAYED_UNKNOWN")
				case "expired-retry-window":
					h.elapseCreateDeadline(r.OrderID)
					var a createRow
					a.order = r.OrderID
					if err = h.Pool.QueryRow(h.ctx, `SELECT retry_not_after FROM stripe_payment_attempts WHERE order_id=$1`, r.OrderID).Scan(&a.retry); err == nil {
						var stopped bool
						stopped, err = h.service.stopExpiredRetryWindow(h.ctx, w, a)
						if err == nil && !stopped {
							t.Fatal("elapsed retry window did not stop work")
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			h.assertState(r.OrderID, StateExpired, false, false)
			if after := voidOrder(t, h, r); !reflect.DeepEqual(before, after) {
				t.Fatalf("non-paid writer changed void: before=%+v after=%+v", before, after)
			}
			checkVoidClose(t, h, r, nil)
		})
	}
}
