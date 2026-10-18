package payments

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// A company hold that catches a paid payment leaves the order paid but out of
// KDS until a store manager explicitly approves it. Provider redelivery and a
// later reconciliation must not undo that decision or release a second card.
func TestPostgresP82BCompanyHoldPaidReviewApprovalIsStable(t *testing.T) {
	h := newHoldFixture(t)
	order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work() // Create Checkout.
	h.hold(true, 0)
	x := h.checkout(order.Order.ID)
	x, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	h.event(x, "manager-review-paid", "checkout.session.completed")
	h.work()
	h.assertState(order.Order.ID, StateReviewRequired, true, true)
	reviews, err := h.Operations.ListPaymentReviews(h.ctx, operations.StoreScope{TenantID: h.TenantID, StoreID: h.StoreID})
	if err != nil || len(reviews) != 1 || reviews[0].ID != order.Order.ID {
		t.Fatalf("paid review queue: reviews=%+v err=%v", reviews, err)
	}

	scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
	version := reviewOrderVersion(t, h, order.Order.ID)
	request := operations.OrderActionRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "fixture-manager", ExpectedVersion: version, IdempotencyKey: "approve-company-hold-order", Reason: "Kitchen confirms product is safe after hold cleared"}
	checkClose := func() error {
		tx, beginErr := h.Pool.Begin(h.ctx)
		if beginErr != nil {
			return beginErr
		}
		defer tx.Rollback(h.ctx)
		return h.service.CheckClose(h.ctx, tx, tablesession.Session{ID: order.Order.SessionID, Scope: tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}})
	}
	if err = checkClose(); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("close with unresolved paid review: %v", err)
	}
	if _, err = h.Operations.ApproveFulfillment(h.ctx, request); !errors.Is(err, operations.ErrCompanyHoldActive) {
		t.Fatalf("approval while company hold active: %v", err)
	}
	// Failed approval has no durable decision. After the company hold is OFF,
	// the same current state can be approved.
	h.hold(false, 1)
	approved, err := h.Operations.ApproveFulfillment(h.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if approved.PaymentStatus != operations.Paid || approved.PaymentReviewRequired || approved.ReviewDecision != "APPROVED" {
		t.Fatalf("approval result: %+v", approved)
	}
	replayed, err := h.Operations.ApproveFulfillment(h.ctx, request)
	if err != nil || !equalReviewOrderStates(replayed, approved) {
		t.Fatalf("same-key replay: result=%+v err=%v want=%+v", replayed, err, approved)
	}
	conflicting := request
	conflicting.Reason = "different reason"
	if _, err = h.Operations.ApproveFulfillment(h.ctx, conflicting); !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("conflicting idempotency reuse: %v", err)
	}
	// A distinct verified event for the same paid Checkout exercises webhook
	// redelivery after approval; the provider still reports PAID to reconcile.
	h.event(x, "manager-review-paid-redelivery", "checkout.session.async_payment_succeeded")
	h.work()
	if _, err = h.service.Reconcile(h.ctx, scope, order.Order.SessionID, order.Order.ID, "fixture-manager"); err != nil {
		t.Fatal(err)
	}
	state := approved
	for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
		state, err = h.Operations.AdvanceFulfillment(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "fixture-kitchen", ExpectedVersion: state.Version, IdempotencyKey: "serve-after-review-" + string(target), Target: target})
		if err != nil {
			t.Fatalf("advance fulfillment to %s: %v", target, err)
		}
	}
	if err = checkClose(); err != nil {
		t.Fatalf("close after review resolved and served: %v", err)
	}
	if err = h.Sessions.Close(h.ctx, tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, order.Order.SessionID, "fixture-manager"); err != nil {
		t.Fatalf("close served session: %v", err)
	}
	closedReplay, err := h.Operations.ApproveFulfillment(h.ctx, request)
	if err != nil || !equalReviewOrderStates(closedReplay, approved) {
		t.Fatalf("replay after fulfillment and close: result=%+v err=%v original=%+v", closedReplay, err, approved)
	}
	h.assertState(order.Order.ID, StatePaid, true, false)
	var attemptState string
	var attemptReview bool
	if err = h.Pool.QueryRow(h.ctx, `SELECT state,payment_review_required FROM stripe_payment_attempts WHERE order_id=$1`, order.Order.ID).Scan(&attemptState, &attemptReview); err != nil {
		t.Fatal(err)
	}
	if attemptState != string(StatePaid) || attemptReview {
		t.Fatalf("paid attempt was reverted by redelivery/reconcile: state=%s review=%v", attemptState, attemptReview)
	}
	assertOneManagerApproval(t, h, order.Order.ID)
}

// Late payment can arrive after its table session has closed. The staff
// decision belongs to the paid order row and remains available for review.
func TestPostgresP82BExpiredSessionLatePaymentCanBeApproved(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprintf("closed-%v", closed), func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
			h.work()
			sessionUpdate := `UPDATE table_sessions SET expires_at=clock_timestamp()-interval '1 second',version=version+1 WHERE session_id=$1`
			if closed {
				sessionUpdate = `UPDATE table_sessions SET status='CLOSED',closed_at=clock_timestamp(),version=version+1 WHERE session_id=$1`
			}
			// Backdate only this disposable fixture, then restore its guard
			// before exercising production paths (the existing expiry pattern).
			tx, err := h.Pool.Begin(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(h.ctx)
			if !closed {
				if _, err = tx.Exec(h.ctx, `ALTER TABLE table_sessions DISABLE TRIGGER table_session_snapshot_immutable`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = tx.Exec(h.ctx, sessionUpdate, order.Order.SessionID); err != nil {
				t.Fatal(err)
			}
			if !closed {
				if _, err = tx.Exec(h.ctx, `ALTER TABLE table_sessions ENABLE TRIGGER table_session_snapshot_immutable`); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(h.ctx); err != nil {
				t.Fatal(err)
			}
			x := h.checkout(order.Order.ID)
			x, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
			if err != nil {
				t.Fatal(err)
			}
			h.event(x, "late-payment-after-session-close", "checkout.session.completed")
			h.work()
			h.assertState(order.Order.ID, StateReviewRequired, true, true)

			reviews, err := h.Operations.ListPaymentReviews(h.ctx, operations.StoreScope{TenantID: h.TenantID, StoreID: h.StoreID})
			if err != nil || len(reviews) != 1 || !slices.Contains(reviews[0].ReviewReasonCodes, "SESSION_CLOSED") {
				t.Fatalf("closed/expired reason missing: %+v %v", reviews, err)
			}
			scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			request := operations.OrderActionRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "fixture-manager", ExpectedVersion: reviewOrderVersion(t, h, order.Order.ID), IdempotencyKey: "approve-late-paid-order", Reason: "Late payment verified; kitchen can fulfill"}
			approved, err := h.Operations.ApproveFulfillment(h.ctx, request)
			if err != nil {
				t.Fatalf("approve paid order after session close: %v", err)
			}
			state := approved
			for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
				state, err = h.Operations.AdvanceFulfillment(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "fixture-kitchen", ExpectedVersion: state.Version, IdempotencyKey: "closed-session-" + string(target), Target: target})
				if err != nil {
					t.Fatalf("advance closed session order to %s: %v", target, err)
				}
			}
			h.assertState(order.Order.ID, StatePaid, true, false)
		})
	}
}

// PostgreSQL scans and JSON replays can represent the same instant with different
// locations. Normalize the sole time field on copies; keep all other fields exact.
func equalReviewOrderStates(a, b operations.OrderState) bool {
	a.CreatedAt = a.CreatedAt.UTC()
	b.CreatedAt = b.CreatedAt.UTC()
	return reflect.DeepEqual(a, b)
}

func reviewOrderVersion(t *testing.T, h *holdFixture, orderID string) int64 {
	t.Helper()
	var version int64
	if err := h.Pool.QueryRow(h.ctx, `SELECT version FROM operation_order_state WHERE order_id=$1`, orderID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func assertOneManagerApproval(t *testing.T, h *holdFixture, orderID string) {
	t.Helper()
	var cards, events, outbox int
	if err := h.Pool.QueryRow(h.ctx, `SELECT
		(SELECT count(*) FROM operation_kds_cards WHERE order_id=$1),
		(SELECT count(*) FROM operation_order_events WHERE order_id=$1 AND event_type='MANAGER_REVIEW_APPROVED'),
		(SELECT count(*) FROM operation_outbox WHERE order_id=$1 AND event_type='KDS_RELEASED')`, orderID).Scan(&cards, &events, &outbox); err != nil {
		t.Fatal(err)
	}
	if cards != 1 || events != 1 || outbox != 1 {
		t.Fatalf("approval effects: KDS=%d audit=%d outbox=%d", cards, events, outbox)
	}
}

func TestPostgresP82BConcurrentManagerApprovals(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-key-%v", sameKey), func(t *testing.T) {
			h := newHoldFixture(t)
			order := createHeldPaidReview(t, h)
			h.hold(false, 1)
			request := managerApprovalRequest(h, order.Order.SessionID, order.Order.ID, reviewOrderVersion(t, h, order.Order.ID), "concurrent-approve", "reviewed by manager")
			requests := []operations.OrderActionRequest{request, request}
			if !sameKey {
				requests[1].IdempotencyKey = "concurrent-approve-second-key"
			}
			start := make(chan struct{})
			type result struct {
				state operations.OrderState
				err   error
			}
			results := make([]result, 2)
			var wg sync.WaitGroup
			for i := range requests {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					results[i].state, results[i].err = h.Operations.ApproveFulfillment(h.ctx, requests[i])
				}(i)
			}
			close(start)
			wg.Wait()
			successes := 0
			for _, got := range results {
				if got.err == nil {
					successes++
				} else if !sameKey && !errors.Is(got.err, operations.ErrReviewNotReady) && !errors.Is(got.err, operations.ErrConflict) {
					t.Fatalf("different-key loser: %v", got.err)
				} else if sameKey {
					t.Fatalf("same-key approval failed: %v", got.err)
				}
			}
			if successes != 1 && !sameKey {
				t.Fatalf("different keys had %d successes, want one: %+v", successes, results)
			}
			if sameKey && (results[0].err != nil || results[1].err != nil || !equalReviewOrderStates(results[0].state, results[1].state)) {
				t.Fatalf("same-key callers did not receive original result: %+v", results)
			}
			h.assertState(order.Order.ID, StatePaid, true, false)
			assertOneManagerApproval(t, h, order.Order.ID)
		})
	}
}

func TestPostgresP82BReviewApprovalRejectsStaleUnpaidAndNormalPaid(t *testing.T) {
	h := newHoldFixture(t)
	paidReview := createHeldPaidReview(t, h)
	h.hold(false, 1)
	request := managerApprovalRequest(h, paidReview.Order.SessionID, paidReview.Order.ID, reviewOrderVersion(t, h, paidReview.Order.ID)-1, "stale-review", "stale state")
	if _, err := h.Operations.ApproveFulfillment(h.ctx, request); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("stale approval: %v", err)
	}
	assertNoManagerApproval(t, h, paidReview.Order.ID)

	cash, _ := h.commit(h.Locator, "tea", ordering.Cash)
	if _, err := h.Operations.ApproveFulfillment(h.ctx, managerApprovalRequest(h, cash.Order.SessionID, cash.Order.ID, reviewOrderVersion(t, h, cash.Order.ID), "unpaid-review", "not paid")); !errors.Is(err, operations.ErrReviewNotReady) {
		t.Fatalf("unpaid approval: %v", err)
	}
	paid, _ := h.commit(h.Locator, "tea", ordering.Stripe)
	h.work()
	x := h.checkout(paid.Order.ID)
	x, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	h.event(x, "normal-paid-no-review", "checkout.session.completed")
	h.work()
	if _, err = h.Operations.ApproveFulfillment(h.ctx, managerApprovalRequest(h, paid.Order.SessionID, paid.Order.ID, reviewOrderVersion(t, h, paid.Order.ID), "normal-paid-review", "not under review")); !errors.Is(err, operations.ErrReviewNotReady) {
		t.Fatalf("normal paid approval: %v", err)
	}
}

func TestPostgresP82BPausedStoreProductDoesNotBlockManagerApproval(t *testing.T) {
	h := newHoldFixture(t)
	order := createHeldPaidReview(t, h)
	h.hold(false, 1)
	menu, err := h.Menus.ReadStoreMenu(h.ctx, catalog.StoreScope{TenantID: h.TenantID, StoreID: h.StoreID})
	if err != nil {
		t.Fatal(err)
	}
	var availabilityVersion int64
	for _, category := range menu.Categories {
		for _, product := range category.Products {
			if product.Key == "teishoku" {
				availabilityVersion = product.AvailabilityVersion
			}
		}
	}
	if availabilityVersion == 0 {
		t.Fatal("teishoku missing from store menu")
	}
	if _, err = h.Menus.SetStoreAvailability(h.ctx, catalog.StoreScope{TenantID: h.TenantID, StoreID: h.StoreID}, "fixture-manager", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: "teishoku"}, Status: catalog.Paused, Reason: "store kitchen pause", ExpectedVersion: availabilityVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Operations.ApproveFulfillment(h.ctx, managerApprovalRequest(h, order.Order.SessionID, order.Order.ID, reviewOrderVersion(t, h, order.Order.ID), "approve-paused-store", "manager accepts kitchen decision")); err != nil {
		t.Fatalf("paused store product blocked approval: %v", err)
	}
	h.assertState(order.Order.ID, StatePaid, true, false)
}

// A concurrent HQ hold owns the company catalog UPDATE lock before it waits
// for the session. Approval must wait for that parent lock before it takes the
// session lock, then observe the committed hold and reject safely.
func TestPostgresP82BApprovalAndCompanyHoldLockOrder(t *testing.T) {
	h := newHoldFixture(t)
	order := createHeldPaidReview(t, h)
	h.hold(false, 1)
	// Keep a second, unpaid payment for the same product so the HQ hold
	// transaction must take its session lock after the company catalog lock.
	h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work()
	blocker, err := h.Pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(h.ctx)
	var blockerPID int32
	if err = blocker.QueryRow(h.ctx, `SELECT pg_backend_pid() FROM table_sessions WHERE session_id=$1 FOR UPDATE`, order.Order.SessionID).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	holdResult := make(chan error, 1)
	go func() {
		_, holdErr := h.Menus.SetCompanyProductEmergencyHold(h.ctx, h.TenantID, "teishoku", "fixture-admin", true, "concurrent parent hold", 2, "concurrent-hold-on")
		holdResult <- holdErr
	}()
	holdPID := waitForHoldBlocker(t, h, blockerPID)
	approvalResult := make(chan error, 1)
	request := managerApprovalRequest(h, order.Order.SessionID, order.Order.ID, reviewOrderVersion(t, h, order.Order.ID), "lock-order-approval", "review after hold check")
	go func() {
		_, approvalErr := h.Operations.ApproveFulfillment(h.ctx, request)
		approvalResult <- approvalErr
	}()
	// If approval acquired the session first, the holder would wait on approval
	// and this lookup would show the reverse dependency. Require approval to be
	// blocked by the HQ transaction instead.
	waitForHoldBlocker(t, h, holdPID)
	if err = blocker.Rollback(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-holdResult; err != nil {
		t.Fatalf("HQ hold transaction: %v", err)
	}
	if err = <-approvalResult; !errors.Is(err, operations.ErrCompanyHoldActive) {
		t.Fatalf("approval did not observe serialized hold: %v", err)
	}
	assertNoManagerApproval(t, h, order.Order.ID)
}

func TestPostgresP82BManagerReviewAuditIsImmutableAndComplete(t *testing.T) {
	h := newHoldFixture(t)
	order := createHeldPaidReview(t, h)
	h.hold(false, 1)
	request := managerApprovalRequest(h, order.Order.SessionID, order.Order.ID, reviewOrderVersion(t, h, order.Order.ID), "audit-review", "Manager verified payment and kitchen readiness")
	if _, err := h.Operations.ApproveFulfillment(h.ctx, request); err != nil {
		t.Fatal(err)
	}
	var actor, reason, before, after string
	if err := h.Pool.QueryRow(h.ctx, `SELECT actor_id,reason,before_state::text,after_state::text FROM operation_order_events WHERE order_id=$1 AND event_type='MANAGER_REVIEW_APPROVED'`, order.Order.ID).Scan(&actor, &reason, &before, &after); err != nil {
		t.Fatal(err)
	}
	if actor != request.Actor || reason != request.Reason || !strings.Contains(before, `"payment_review_required": true`) || !strings.Contains(after, `"payment_review_required": false`) {
		t.Fatalf("manager audit missing actor/reason/before/after: actor=%q reason=%q before=%s after=%s", actor, reason, before, after)
	}
	if _, err := h.Pool.Exec(h.ctx, `UPDATE operation_order_events SET reason='rewritten' WHERE order_id=$1 AND event_type='MANAGER_REVIEW_APPROVED'`, order.Order.ID); err == nil {
		t.Fatal("manager audit UPDATE succeeded")
	}
	if _, err := h.Pool.Exec(h.ctx, `DELETE FROM operation_order_events WHERE order_id=$1 AND event_type='MANAGER_REVIEW_APPROVED'`, order.Order.ID); err == nil {
		t.Fatal("manager audit DELETE succeeded")
	}
}

func createHeldPaidReview(t *testing.T, h *holdFixture) ordering.CommitResult {
	t.Helper()
	order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work()
	h.hold(true, 0)
	x := h.checkout(order.Order.ID)
	x, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	h.event(x, "paid-review-"+order.Order.ID, "checkout.session.completed")
	h.work()
	h.assertState(order.Order.ID, StateReviewRequired, true, true)
	return order
}

func managerApprovalRequest(h *holdFixture, sessionID, orderID string, version int64, key, reason string) operations.OrderActionRequest {
	return operations.OrderActionRequest{Scope: operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, SessionID: sessionID, OrderID: orderID, Actor: "fixture-manager", ExpectedVersion: version, IdempotencyKey: key, Reason: reason}
}

func assertNoManagerApproval(t *testing.T, h *holdFixture, orderID string) {
	t.Helper()
	var events, cards, outbox int
	if err := h.Pool.QueryRow(h.ctx, `SELECT (SELECT count(*) FROM operation_order_events WHERE order_id=$1 AND event_type='MANAGER_REVIEW_APPROVED'),(SELECT count(*) FROM operation_kds_cards WHERE order_id=$1),(SELECT count(*) FROM operation_outbox WHERE order_id=$1 AND event_type='KDS_RELEASED')`, orderID).Scan(&events, &cards, &outbox); err != nil {
		t.Fatal(err)
	}
	if events != 0 || cards != 0 || outbox != 0 {
		t.Fatalf("rejected approval wrote effects: audit=%d card=%d outbox=%d", events, cards, outbox)
	}
}
