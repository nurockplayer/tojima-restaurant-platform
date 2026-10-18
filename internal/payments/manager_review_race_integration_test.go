package payments

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

func TestPostgresP82BApprovalSerializesWithPaidWebhookRedelivery(t *testing.T) {
	for _, approvalFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("approval-first-%v", approvalFirst), func(t *testing.T) {
			h := newHoldFixture(t)
			order := createHeldPaidReview(t, h)
			// Finish the retained expiry job before queuing the redelivery so
			// ProcessNext has exactly one eligible job, regardless of timing.
			h.work()
			h.hold(false, 1)
			request := managerApprovalRequest(h, order.Order.SessionID, order.Order.ID, reviewOrderVersion(t, h, order.Order.ID), "approve-webhook-race", "Verified paid review")
			const eventID = "approval-race-late-paid"
			// event enqueues an older paid observation and redelivers that same
			// event ID, asserting that the duplicate does not enqueue more work.
			h.event(h.checkout(order.Order.ID), eventID, "checkout.session.async_payment_succeeded")
			var approved operations.OrderState
			approve := func() (err error) {
				approved, err = h.Operations.ApproveFulfillment(h.ctx, request)
				return err
			}
			webhook := func() error {
				worked, err := h.service.ProcessNext(h.ctx)
				if err == nil && !worked {
					return errors.New("webhook worker found no work")
				}
				return err
			}
			first, second := approve, webhook
			if !approvalFirst {
				first, second = webhook, approve
			}
			firstErr, secondErr := runManagerReviewLockRace(t, h, order.Order.SessionID, first, second)
			if firstErr != nil || secondErr != nil {
				t.Fatalf("serialized approval/webhook: first=%v second=%v", firstErr, secondErr)
			}
			h.assertState(order.Order.ID, StatePaid, true, false)
			assertOneManagerApproval(t, h, order.Order.ID)
			var processed bool
			var workCount int
			if err := h.Pool.QueryRow(h.ctx, `SELECT e.processed_at IS NOT NULL,
				(SELECT count(*) FROM stripe_payment_work w WHERE w.webhook_row_id=e.webhook_row_id AND w.status='DONE')
				FROM stripe_webhook_events e WHERE e.stripe_event_id=$1`, eventID).Scan(&processed, &workCount); err != nil {
				t.Fatal(err)
			}
			if !processed || workCount != 1 {
				t.Fatalf("redelivery was not processed exactly once: processed=%v work=%d", processed, workCount)
			}
			var decision string
			var version int64
			if err := h.Pool.QueryRow(h.ctx, `SELECT review_decision,version FROM operation_order_state WHERE order_id=$1`, order.Order.ID).Scan(&decision, &version); err != nil {
				t.Fatal(err)
			}
			if approved.PaymentStatus != operations.Paid || decision != "APPROVED" || version != approved.Version {
				t.Fatalf("paid redelivery changed approved state: approval=%+v decision=%s version=%d", approved, decision, version)
			}
		})
	}
}

func TestPostgresP82BApprovalSerializesWithTableClose(t *testing.T) {
	for _, approvalFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("approval-first-%v", approvalFirst), func(t *testing.T) {
			h := newHoldFixture(t)
			order := createHeldPaidReview(t, h)
			h.hold(false, 1)
			// Use the production payment close policy, including its operation
			// checks, rather than the guest fixture's operation-only policy.
			sessions, err := tablesession.New(h.Pool, tablesession.Options{SessionTTL: 2 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 100, PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: make([]byte, 32)}, h.service)
			if err != nil {
				t.Fatal(err)
			}
			scope := tablesession.Scope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			request := managerApprovalRequest(h, order.Order.SessionID, order.Order.ID, reviewOrderVersion(t, h, order.Order.ID), "approve-close-race", "Kitchen ready to fulfill")
			var approved operations.OrderState
			approve := func() (err error) {
				approved, err = h.Operations.ApproveFulfillment(h.ctx, request)
				return err
			}
			closeTable := func() error {
				return sessions.Close(h.ctx, scope, order.Order.SessionID, "fixture-manager")
			}
			first, second := approve, closeTable
			if !approvalFirst {
				first, second = closeTable, approve
			}
			firstErr, secondErr := runManagerReviewLockRace(t, h, order.Order.SessionID, first, second)
			approvalErr, closeErr := firstErr, secondErr
			if !approvalFirst {
				approvalErr, closeErr = secondErr, firstErr
			}
			// Exactly one succeeds in either order: close-first sees an
			// unresolved review; approval-first leaves an unserved paid order.
			if approvalErr != nil || !errors.Is(closeErr, tablesession.ErrCloseBlocked) {
				t.Fatalf("approval/close race: approval=%v close=%v", approvalErr, closeErr)
			}
			var status tablesession.Status
			if err = h.Pool.QueryRow(h.ctx, `SELECT status FROM table_sessions WHERE session_id=$1`, order.Order.SessionID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != tablesession.Open {
				t.Fatalf("close succeeded with an unresolved or unserved order: %s", status)
			}
			h.assertState(order.Order.ID, StatePaid, true, false)
			assertOneManagerApproval(t, h, order.Order.ID)
			state := approved
			for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
				if err = closeTable(); !errors.Is(err, tablesession.ErrCloseBlocked) {
					t.Fatalf("close before SERVED at %s: %v", state.FulfillmentStatus, err)
				}
				state, err = h.Operations.AdvanceFulfillment(h.ctx, operations.OrderActionRequest{Scope: request.Scope, SessionID: request.SessionID, OrderID: request.OrderID, Actor: "fixture-kitchen", ExpectedVersion: state.Version, IdempotencyKey: "close-race-" + string(target), Target: target})
				if err != nil {
					t.Fatalf("advance to %s: %v", target, err)
				}
			}
			if err = closeTable(); err != nil {
				t.Fatalf("close after SERVED: %v", err)
			}
			if err = h.Pool.QueryRow(h.ctx, `SELECT status FROM table_sessions WHERE session_id=$1`, order.Order.SessionID).Scan(&status); err != nil || status != tablesession.Closed {
				t.Fatalf("served session did not close: status=%s err=%v", status, err)
			}
		})
	}
}

// Queue both real service transactions behind an explicit session row lock.
// The first wait proves it reached the barrier; the second wait proves it is
// behind the first in PostgreSQL's lock queue before either can proceed. This
// exercises both serial orders without sleeps or scheduler-dependent starts.
func runManagerReviewLockRace(t *testing.T, h *holdFixture, sessionID string, first, second func() error) (error, error) {
	t.Helper()
	blocker, err := h.Pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	var blockerPID int32
	if err = blocker.QueryRow(h.ctx, `SELECT pg_backend_pid() FROM table_sessions WHERE session_id=$1 FOR UPDATE`, sessionID).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	firstResult, secondResult := make(chan error, 1), make(chan error, 1)
	go func() { firstResult <- first() }()
	firstPID := waitForHoldBlocker(t, h, blockerPID)
	go func() { secondResult <- second() }()
	waitForHoldBlocker(t, h, firstPID)
	if err = blocker.Rollback(h.ctx); err != nil {
		t.Fatal(err)
	}
	return <-firstResult, <-secondResult
}
