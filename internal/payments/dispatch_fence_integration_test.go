package payments

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// claimCreateForDispatchFence creates a specific lease without running the
// worker. This keeps the crash and stale-token interleavings deterministic.
func claimCreateForDispatchFence(t *testing.T, p *ext4Platform, orderID, token string) workItem {
	t.Helper()
	w := workItem{Kind: "CREATE", OrderID: orderID, Lease: token}
	err := p.fixture.Pool.QueryRow(p.ctx, `UPDATE stripe_payment_work
		SET status='LEASED',lease_token=$2,lease_until=clock_timestamp()+interval '1 minute',attempts=attempts+1,updated_at=clock_timestamp()
		WHERE order_id=$1 AND work_type='CREATE' AND status='READY'
		RETURNING work_id,attempts`, orderID, token).Scan(&w.ID, &w.Attempts)
	if err != nil {
		t.Fatalf("claim CREATE work: %v", err)
	}
	return w
}

func reclaimCreateForDispatchFence(t *testing.T, p *ext4Platform, orderID, oldToken, newToken string) workItem {
	t.Helper()
	w := workItem{Kind: "CREATE", OrderID: orderID, Lease: newToken}
	err := p.fixture.Pool.QueryRow(p.ctx, `UPDATE stripe_payment_work
		SET lease_token=$3,lease_until=clock_timestamp()+interval '1 minute',attempts=attempts+1,updated_at=clock_timestamp()
		WHERE order_id=$1 AND work_type='CREATE' AND status='LEASED' AND lease_token=$2 AND lease_until<=clock_timestamp()
		RETURNING work_id,attempts`, orderID, oldToken, newToken).Scan(&w.ID, &w.Attempts)
	if err != nil {
		t.Fatalf("reclaim CREATE work: %v", err)
	}
	return w
}

func dispatchFenceOrder(t *testing.T, p *ext4Platform, key string) (string, ordering.CommitResult) {
	t.Helper()
	grant := p.join(t, "")
	order := p.commit(t, grant.Session.ID, grant.Token, key, ordering.Stripe)
	return grant.Session.ID, order
}

func TestCreateDispatchFenceCrashBeforeDispatchCanBeVoided(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	sessionID, order := dispatchFenceOrder(t, p, "dispatch-fence-crash")
	w := claimCreateForDispatchFence(t, p, order.Order.ID, "crash-before-dispatch")

	// Model the durable failure write succeeding immediately before a process
	// crash, with finishWork never reached.
	if err := p.service.updateAttemptFailure(ctx, w, order.Order.ID, "SESSION_UNAVAILABLE", false); err != nil {
		t.Fatalf("persist SESSION_UNAVAILABLE failure: %v", err)
	}
	if _, err := p.fixture.Pool.Exec(ctx, `UPDATE stripe_payment_work SET lease_until=clock_timestamp()-interval '1 second' WHERE work_id=$1`, w.ID); err != nil {
		t.Fatalf("expire crashed lease: %v", err)
	}
	p.expireSession(t, sessionID)
	if worked, err := p.service.ProcessNext(ctx); !worked || err != nil {
		t.Fatalf("reclaim crashed CREATE: worked=%v err=%v", worked, err)
	}
	var closedFailure bool
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT a.state='FAILED' AND w.status='DONE' AND w.attempts=2
		AND w.safe_error_code='SESSION_UNAVAILABLE' AND w.dispatched_at IS NULL
		AND EXISTS(SELECT 1 FROM stripe_payment_attempt_events e WHERE e.order_id=a.order_id
			AND e.event_key='WORK_STATE:FAILED:SESSION_UNAVAILABLE')
		FROM stripe_payment_attempts a JOIN stripe_payment_work w USING(order_id)
		WHERE a.order_id=$1 AND w.work_type='CREATE'`, order.Order.ID).Scan(&closedFailure); err != nil || !closedFailure {
		t.Fatalf("reclaimed pre-dispatch failure was not durably closed: %v err=%v", closedFailure, err)
	}
	state := p.order(t, sessionID, order.Order.ID)
	if _, err := p.service.VoidUnpaidCheckout(ctx, VoidRequest{
		Scope: p.tableScope(), SessionID: sessionID, OrderID: order.Order.ID, Actor: ext4Actor,
		ExpectedVersion: state.Version, IdempotencyKey: "dispatch-fence-crash-void", Reason: "Checkout dispatch never began",
	}); err != nil {
		t.Fatalf("void undispatched Checkout: %v", err)
	}
	if err := p.sessions.Close(ctx, p.sessionScope(), sessionID, ext4Actor); err != nil {
		t.Fatalf("close session after void: %v", err)
	}
	if creates, sessions := p.provider.Counts(); creates != 0 || sessions != 0 {
		t.Fatalf("provider called before dispatch fence: creates=%d sessions=%d", creates, sessions)
	}
}

func TestCreateDispatchFenceRejectsReclaimedLeaseToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	_, order := dispatchFenceOrder(t, p, "dispatch-fence-stale-lease")
	old := claimCreateForDispatchFence(t, p, order.Order.ID, "first-create-lease")
	if _, err := p.fixture.Pool.Exec(ctx, `UPDATE stripe_payment_work SET lease_until=clock_timestamp()-interval '1 second' WHERE work_id=$1`, old.ID); err != nil {
		t.Fatalf("expire first lease: %v", err)
	}
	newLease := reclaimCreateForDispatchFence(t, p, order.Order.ID, old.Lease, "second-create-lease")
	if newLease.Attempts != 2 {
		t.Fatalf("reclaimed attempts=%d, want 2", newLease.Attempts)
	}
	if err := p.service.processCreate(ctx, old); !errors.Is(err, ErrConflict) {
		t.Fatalf("old lease token must be fenced before provider dispatch: %v", err)
	}
	var undispatched bool
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT dispatched_at IS NULL FROM stripe_payment_work WHERE order_id=$1 AND work_type='CREATE'`, order.Order.ID).Scan(&undispatched); err != nil || !undispatched {
		t.Fatalf("stale lease set dispatched_at: %v err=%v", undispatched, err)
	}
	if creates, sessions := p.provider.Counts(); creates != 0 || sessions != 0 {
		t.Fatalf("stale lease called provider: creates=%d sessions=%d", creates, sessions)
	}
}

func TestCreateDispatchFenceKeepsLostProviderOutcomeUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	sessionID, order := dispatchFenceOrder(t, p, "dispatch-fence-unknown")
	// The fake stores a Checkout before returning the lost response. Expire the
	// session and first lease inside that provider call so the next worker must
	// reclaim the in-flight lease and consult the durable dispatch marker.
	p.provider.SetNextCreateHook(func(_ stripeprovider.CheckoutResult) {
		p.expireSession(t, sessionID)
		if _, err := p.fixture.Pool.Exec(ctx, `UPDATE stripe_payment_work SET lease_until=clock_timestamp()-interval '1 second'
			WHERE order_id=$1 AND work_type='CREATE'`, order.Order.ID); err != nil {
			t.Fatalf("expire in-flight provider lease: %v", err)
		}
	})
	p.provider.LoseNextCreateResponse()
	if worked, err := p.service.ProcessNext(ctx); !worked || !errors.Is(err, ErrConflict) {
		t.Fatalf("in-flight CREATE after lost response and expired lease: worked=%v err=%v", worked, err)
	}
	if worked, err := p.service.ProcessNext(ctx); !worked || err != nil {
		t.Fatalf("reclaim CREATE after lost response: worked=%v err=%v", worked, err)
	}
	var unknownOutcome bool
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT a.state='UNKNOWN' AND w.status='DONE' AND w.attempts=2
		AND w.safe_error_code='PROVIDER_OUTCOME_UNKNOWN' AND w.dispatched_at IS NOT NULL
		AND EXISTS(SELECT 1 FROM stripe_payment_attempt_events e WHERE e.order_id=a.order_id
			AND e.event_key='WORK_STATE:UNKNOWN:PROVIDER_OUTCOME_UNKNOWN')
		FROM stripe_payment_attempts a JOIN stripe_payment_work w USING(order_id)
		WHERE a.order_id=$1 AND w.work_type='CREATE'`, order.Order.ID).Scan(&unknownOutcome); err != nil || !unknownOutcome {
		t.Fatalf("dispatched provider outcome was not preserved as unknown: %v err=%v", unknownOutcome, err)
	}
	state := p.order(t, sessionID, order.Order.ID)
	if _, err := p.service.VoidUnpaidCheckout(ctx, VoidRequest{
		Scope: p.tableScope(), SessionID: sessionID, OrderID: order.Order.ID, Actor: ext4Actor,
		ExpectedVersion: state.Version, IdempotencyKey: "dispatch-fence-unknown-void", Reason: "provider outcome is unknown",
	}); !errors.Is(err, ErrVoidNotEligible) {
		t.Fatalf("unknown provider outcome must not be locally voidable: %v", err)
	}
	if creates, sessions := p.provider.Counts(); creates != 1 || sessions != 1 {
		t.Fatalf("expected one possibly successful provider request: creates=%d sessions=%d", creates, sessions)
	}
	if err := p.sessions.Close(ctx, p.sessionScope(), sessionID, ext4Actor); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("unknown provider outcome must continue to block Close: %v", err)
	}
}
