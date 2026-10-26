package payments

// EXT-4 TTL acceptance tests. These pin the Owner-adopted MVP pilot lifetimes
// (2026-10-26) against the four pilot scenarios: long meals, payment
// continuation, table close and ORDER_READ after close. Expiry is simulated
// with database clock updates, never with sleeps, so no test depends on
// wall-clock waiting.
//
// Scope: the adopted values are injected exactly as a deployment must inject
// them, so these tests prove behaviour under the adopted configuration, not
// that a wiring literal still says 4h. The one production default,
// ordering.DefaultOrderReadTTL, is exercised through ordering.NewWithOrderPolicy
// (built on ordering.New) and asserted directly. Deadlines are read back against
// the database clock, and checkout proofs are checked at exact boundary times
// supplied to Verify.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

// Adopted MVP pilot TTLs. Owner decision 2026-10-26; the runbook EXT-4 section
// records them.
const (
	ext4SessionTTL      = 4 * time.Hour
	ext4GrantTTL        = time.Hour
	ext4PINTTL          = 15 * time.Minute
	ext4PINLockout      = time.Minute
	ext4PINMaxAttempts  = 3
	ext4ProofTTL        = 30 * time.Minute
	ext4ContinuationTTL = 30 * time.Minute
	ext4CreateRetry     = 20 * time.Hour
	ext4WorkLeaseTTL    = 30 * time.Second
	ext4OrderReadTTL    = 24 * time.Hour
	ext4Actor           = "ext4-staff"
)

type ext4Platform struct {
	ctx      context.Context
	fixture  *testguest.Fixture
	provider *testpayments.Provider
	account  stripeprovider.AccountContext
	service  *Service
	proofs   *checkoutproof.Service
	sessions *tablesession.Service
	orders   *ordering.Service
}

func newEXT4Platform(t *testing.T, ctx context.Context) *ext4Platform {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close(context.Background()) })
	// Expiry is simulated by moving persisted deadlines; the immutable-snapshot
	// triggers block that by design, so the disposable fixture schema turns them
	// off (same as the other payments integration tests).
	for _, stmt := range []string{
		`ALTER TABLE table_sessions DISABLE TRIGGER table_session_snapshot_immutable`,
		`ALTER TABLE stripe_payment_attempts DISABLE TRIGGER stripe_payment_attempt_snapshot_immutable`,
	} {
		if _, err = f.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("disable test trigger: %v", err)
		}
	}
	if ordering.DefaultOrderReadTTL != ext4OrderReadTTL {
		t.Fatalf("ordering.DefaultOrderReadTTL is %s, want the adopted %s", ordering.DefaultOrderReadTTL, ext4OrderReadTTL)
	}
	provider := testpayments.NewProvider()
	account := stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_ext4"}
	const credential = "fixture:ext4:v1"
	resolver := testResolver{credential: {Provider: provider, Account: account, ProfileFingerprint: sha256.Sum256([]byte("ext4-profile"))}}
	service, err := New(f.Pool, f.Operations, resolver, Config{CreateRetryWindow: ext4CreateRetry, ContinuationTTL: ext4ContinuationTTL, WorkLeaseTTL: ext4WorkLeaseTTL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetMerchantConfig(ctx, operations.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}, 0, "ext4-bootstrap", MerchantConfigInput{
		Enabled: true, Mode: account.Mode, StripeAccountID: account.StripeAccountID, CredentialRef: credential,
		WebhookEndpointRef: "fixture:ext4-webhook", APIVersion: stripeprovider.APIVersion,
	}); err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	sessions, err := tablesession.New(f.Pool, tablesession.Options{
		SessionTTL: ext4SessionTTL, GrantTTL: ext4GrantTTL, GrantWindow: time.Minute, GrantWindowMax: 1000,
		PINTTL: ext4PINTTL, PINLockout: ext4PINLockout, PINMaxAttempts: ext4PINMaxAttempts, PINLength: 6, PINPepper: pepper,
	}, service)
	if err != nil {
		t.Fatal(err)
	}
	orders, err := ordering.NewWithOrderPolicy(f.Pool, sessions, f.Menus, service)
	if err != nil {
		t.Fatal(err)
	}
	signingKey := make([]byte, 32)
	if _, err = rand.Read(signingKey); err != nil {
		t.Fatal(err)
	}
	proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: [][]byte{signingKey}, TTL: ext4ProofTTL})
	if err != nil {
		t.Fatal(err)
	}
	return &ext4Platform{ctx: ctx, fixture: f, provider: provider, account: account, service: service, proofs: proofs, sessions: sessions, orders: orders}
}

func (p *ext4Platform) tableScope() operations.TableScope {
	return operations.TableScope{TenantID: p.fixture.TenantID, StoreID: p.fixture.StoreID, TableID: p.fixture.TableID}
}

func (p *ext4Platform) sessionScope() tablesession.Scope {
	return tablesession.Scope{TenantID: p.fixture.TenantID, StoreID: p.fixture.StoreID, TableID: p.fixture.TableID}
}

func (p *ext4Platform) join(t *testing.T, expectedSessionID string) tablesession.Grant {
	t.Helper()
	grant, err := p.sessions.DirectJoin(p.ctx, p.fixture.Locator, expectedSessionID)
	if err != nil {
		t.Fatalf("direct join: %v", err)
	}
	return grant
}

func (p *ext4Platform) issueProof(t *testing.T) (string, checkoutproof.Proof) {
	t.Helper()
	return p.issueProofAt(t, time.Now().UTC())
}

// issueProofAt issues a proof and pins the 30m lifetime and both boundary
// decisions against the exact times supplied, never a sleep or the wall clock.
func (p *ext4Platform) issueProofAt(t *testing.T, issuedAt time.Time) (string, checkoutproof.Proof) {
	t.Helper()
	token, proof, err := p.proofs.Issue(issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if want := issuedAt.Add(ext4ProofTTL); !proof.ExpiresAt.Equal(want) {
		t.Fatalf("checkout proof expires at %s, want the adopted issued+30m %s", proof.ExpiresAt, want)
	}
	if _, err = p.proofs.Verify(token, proof.ExpiresAt.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("proof must verify one nanosecond before its expiry: %v", err)
	}
	if _, err = p.proofs.Verify(token, proof.ExpiresAt); !errors.Is(err, checkoutproof.ErrInvalidProof) {
		t.Fatalf("proof at its own expiry: got %v, want ErrInvalidProof", err)
	}
	return token, proof
}

func (p *ext4Platform) addLine(t *testing.T, sessionID, token, key string) ordering.Cart {
	t.Helper()
	cart, err := p.orders.ReadCart(p.ctx, sessionID, token)
	if err != nil {
		t.Fatalf("read cart: %v", err)
	}
	cart, err = p.orders.Mutate(p.ctx, ordering.Mutation{
		SessionID: sessionID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: key, Kind: ordering.AddLine, ProductKey: "teishoku", Quantity: 1,
		Options: []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}},
	})
	if err != nil {
		t.Fatalf("add line: %v", err)
	}
	return cart
}

// commit places one order through the public guest path. Stripe commits carry
// the hash and expiry of a freshly issued 30-minute checkout proof unless the
// caller supplies one, which the continuation tests use to age the proof.
func (p *ext4Platform) commit(t *testing.T, sessionID, token, key string, method ordering.PaymentMethod, supplied ...checkoutproof.Proof) ordering.CommitResult {
	t.Helper()
	cart := p.addLine(t, sessionID, token, key+"-line")
	preview, err := p.orders.Preview(p.ctx, sessionID, token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	req := ordering.CommitRequest{
		SessionID: sessionID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: key, PreviewFingerprint: preview.Fingerprint, PaymentMethod: method,
	}
	if method == ordering.Stripe {
		proof := checkoutproof.Proof{}
		if len(supplied) > 0 {
			proof = supplied[0]
		} else {
			_, proof = p.issueProof(t)
		}
		copy(req.CheckoutProofHash[:], proof.Hash)
		req.CheckoutProofExpiresAt = proof.ExpiresAt
	}
	result, err := p.orders.Commit(p.ctx, req)
	if err != nil {
		t.Fatalf("commit %s: %v", key, err)
	}
	return result
}

// drainWork runs the durable worker until no ready item remains. Payment state
// transitions (CREATE, EXPIRE, webhooks) are asynchronous by design.
func (p *ext4Platform) drainWork(t *testing.T) {
	t.Helper()
	for range 20 {
		worked, err := p.service.ProcessNext(p.ctx)
		if err != nil {
			t.Fatalf("payment worker: %v", err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("payment worker did not drain within 20 steps")
}

func (p *ext4Platform) order(t *testing.T, sessionID, orderID string) operations.OrderState {
	t.Helper()
	detail, err := p.fixture.Operations.ReadSession(p.ctx, p.tableScope(), sessionID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	for _, o := range detail.Orders {
		if o.ID == orderID {
			return o
		}
	}
	t.Fatalf("order %s not found in session %s", orderID, sessionID)
	return operations.OrderState{}
}

func (p *ext4Platform) attemptCheckoutID(t *testing.T, orderID string) string {
	t.Helper()
	var id string
	if err := p.fixture.Pool.QueryRow(p.ctx, `SELECT coalesce(checkout_session_id,'') FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&id); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	return id
}

func (p *ext4Platform) attemptCount(t *testing.T, orderID string) int {
	t.Helper()
	var n int
	if err := p.fixture.Pool.QueryRow(p.ctx, `SELECT count(*) FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&n); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	return n
}

// expireSession simulates the adopted four-hour boundary without sleeping.
func (p *ext4Platform) expireSession(t *testing.T, sessionID string) {
	t.Helper()
	if _, err := p.fixture.Pool.Exec(p.ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()-interval '1 second',version=version+1 WHERE session_id=$1`, sessionID); err != nil {
		t.Fatalf("expire session: %v", err)
	}
}

// dbDeadline reads a persisted deadline relative to the database clock in the
// same statement, so host/database clock skew cannot decide an assertion.
func (p *ext4Platform) dbDeadline(t *testing.T, query string, args ...any) time.Duration {
	t.Helper()
	var seconds float64
	if err := p.fixture.Pool.QueryRow(p.ctx, query, args...).Scan(&seconds); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	return time.Duration(seconds * float64(time.Second))
}

func (p *ext4Platform) sessionExpiresAt(t *testing.T, sessionID string) time.Time {
	t.Helper()
	var expires time.Time
	if err := p.fixture.Pool.QueryRow(p.ctx, `SELECT expires_at FROM table_sessions WHERE session_id=$1`, sessionID).Scan(&expires); err != nil {
		t.Fatalf("read session expiry: %v", err)
	}
	return expires
}

// wantLifetime asserts a lifetime written and read by the same database clock.
// Only statement latency separates the two, so seconds are the honest tolerance:
// ten seconds late is a loaded runner, a different lifetime is a regression.
func wantLifetime(t *testing.T, label string, got, want time.Duration) {
	t.Helper()
	if got < want-10*time.Second || got > want+time.Second {
		t.Fatalf("%s: got %s, want %s", label, got.Round(time.Millisecond), want)
	}
}

// payViaWebhook delivers the provider's paid observation for the attempt's
// current Checkout and lets the durable worker apply it. This models a guest
// completing payment on the already-open Stripe page.
func (p *ext4Platform) payViaWebhook(t *testing.T, orderID, eventID string) {
	t.Helper()
	checkout := p.attemptCheckoutID(t, orderID)
	if checkout == "" {
		t.Fatalf("order %s has no Checkout session to pay", orderID)
	}
	paid, err := p.provider.SetOutcome(checkout, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatalf("provider paid outcome: %v", err)
	}
	accepted, err := p.service.EnqueueVerifiedWebhook(p.ctx, stripeprovider.VerifiedEvent{
		ID: eventID, Type: "checkout.session.completed", AccountID: paid.AccountID, APIVersion: stripeprovider.APIVersion,
		CreatedAt: time.Now().UTC(), ObjectID: paid.SessionID, Checkout: &paid,
	})
	if err != nil || !accepted {
		t.Fatalf("enqueue paid webhook: accepted=%v err=%v", accepted, err)
	}
	p.drainWork(t)
}

func (p *ext4Platform) accept(t *testing.T, sessionID, orderID string, version int64) operations.OrderState {
	t.Helper()
	out, err := p.fixture.Operations.AcceptOrder(p.ctx, operations.OrderActionRequest{
		Scope: p.tableScope(), SessionID: sessionID, OrderID: orderID, Actor: ext4Actor,
		ExpectedVersion: version, IdempotencyKey: "ext4-accept-" + orderID,
	})
	if err != nil {
		t.Fatalf("accept order %s: %v", orderID, err)
	}
	return out
}

func (p *ext4Platform) advance(t *testing.T, sessionID, orderID string, version int64, target operations.FulfillmentStatus) operations.OrderState {
	t.Helper()
	out, err := p.fixture.Operations.AdvanceFulfillment(p.ctx, operations.OrderActionRequest{
		Scope: p.tableScope(), SessionID: sessionID, OrderID: orderID, Actor: ext4Actor,
		ExpectedVersion: version, IdempotencyKey: fmt.Sprintf("ext4-fulfill-%s-%s", orderID, target), Target: target,
	})
	if err != nil {
		t.Fatalf("advance order %s to %s: %v", orderID, target, err)
	}
	return out
}

// serve walks the kitchen path to SERVED. A DIRECT_SCAN session requires staff
// acceptance first; a manager review approval already sets it.
func (p *ext4Platform) serve(t *testing.T, sessionID, orderID string) {
	t.Helper()
	o := p.order(t, sessionID, orderID)
	if o.RequiresStaffAcceptance && !o.StaffAccepted {
		o = p.accept(t, sessionID, orderID, o.Version)
	}
	for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
		if o.FulfillmentStatus == target {
			continue
		}
		o = p.advance(t, sessionID, orderID, o.Version, target)
	}
	if o.FulfillmentStatus != operations.Served {
		t.Fatalf("order %s is %s, want SERVED", orderID, o.FulfillmentStatus)
	}
}

func (p *ext4Platform) settleCash(t *testing.T, sessionID string) {
	t.Helper()
	preview, err := p.fixture.Operations.PreviewCash(p.ctx, p.tableScope(), sessionID)
	if err != nil {
		t.Fatalf("preview cash: %v", err)
	}
	receipt, err := p.fixture.Operations.SettleCash(p.ctx, operations.CashSettlementRequest{
		Scope: p.tableScope(), SessionID: sessionID, Actor: ext4Actor, Fingerprint: preview.Fingerprint,
		TenderedJPY: preview.DueJPY, RegisterSessionRef: "ext4-register", IdempotencyKey: "ext4-settle-" + sessionID,
	})
	if err != nil || receipt.DueJPY != preview.DueJPY {
		t.Fatalf("settle cash: receipt=%+v err=%v", receipt, err)
	}
}

// TestEXT4LongMealOutlivesSession covers scenario 1: a party seated past the
// four-hour session TTL. New guest actions stop at expiry, money already
// offered on an open Checkout still settles (flagged for staff review), and
// staff can review, serve, close and reopen the table.
func TestEXT4LongMealOutlivesSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	grant := p.join(t, "")
	expiry := p.sessionExpiresAt(t, grant.Session.ID)
	_, proof := p.issueProof(t)
	var proofHash [32]byte
	copy(proofHash[:], proof.Hash)
	order := p.commit(t, grant.Session.ID, grant.Token, "ext4-meal", ordering.Stripe, proof)
	p.drainWork(t)
	if status, err := p.service.ReadSafeStatus(ctx, p.tableScope(), grant.Session.ID, order.Order.ID); err != nil || status.State != StateOpen {
		t.Fatalf("checkout should be open before expiry: status=%+v err=%v", status, err)
	}

	// The four-hour boundary is absolute: joining and committing did not extend it.
	if moved := p.sessionExpiresAt(t, grant.Session.ID); !moved.Equal(expiry) {
		t.Fatalf("session expiry moved from %s to %s before expiry", expiry, moved)
	}

	p.expireSession(t, grant.Session.ID)

	// After four hours: no rejoin, no cart, no new order, no continuation.
	if _, err := p.sessions.DirectJoin(ctx, p.fixture.Locator, grant.Session.ID); !errors.Is(err, tablesession.ErrExpired) {
		t.Fatalf("join after session expiry: got %v, want ErrExpired", err)
	}
	if _, err := p.orders.ReadCart(ctx, grant.Session.ID, grant.Token); !errors.Is(err, ordering.ErrDenied) {
		t.Fatalf("cart read after session expiry: got %v, want ErrDenied", err)
	}
	if _, err := p.orders.Commit(ctx, ordering.CommitRequest{
		SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 2, ExpectedVersion: 1,
		IdempotencyKey: "ext4-meal-late", PreviewFingerprint: "unused", PaymentMethod: ordering.Cash,
	}); !errors.Is(err, ordering.ErrDenied) {
		t.Fatalf("order after session expiry: got %v, want ErrDenied", err)
	}
	// The Stripe page cannot be resumed from the app after the boundary; only
	// the already-open page can still complete (asserted below).
	if ok, err := p.service.CanResume(ctx, grant.Session.ID, order.Order.ID, proofHash); err != nil || ok {
		t.Fatalf("resume after session expiry: ok=%v err=%v", ok, err)
	}
	if _, err := p.service.Resume(ctx, grant.Session.ID, order.Order.ID, proofHash); !errors.Is(err, ErrProofDenied) {
		t.Fatalf("resume after session expiry: got %v, want ErrProofDenied", err)
	}

	// The guest is still on the Stripe page and pays there. The verification
	// webhook must record the money exactly once and flag the late match.
	p.payViaWebhook(t, order.Order.ID, "evt-ext4-meal-paid")
	if n := p.attemptCount(t, order.Order.ID); n != 1 {
		t.Fatalf("late payment created another attempt: %d", n)
	}
	paid := p.order(t, grant.Session.ID, order.Order.ID)
	if paid.PaymentStatus != operations.Paid || !paid.PaymentReviewRequired {
		t.Fatalf("late Stripe payment must be PAID with staff review: %+v", paid)
	}

	// Staff recovery: approve the review, serve, close the expired table.
	if _, err := p.fixture.Operations.ApproveFulfillment(ctx, operations.OrderActionRequest{
		Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: order.Order.ID, Actor: ext4Actor,
		ExpectedVersion: paid.Version, IdempotencyKey: "ext4-review-meal", Reason: "late Stripe payment matched to served meal",
	}); err != nil {
		t.Fatalf("approve review: %v", err)
	}
	p.serve(t, grant.Session.ID, order.Order.ID)
	if status, err := p.service.ReadSafeStatus(ctx, p.tableScope(), grant.Session.ID, order.Order.ID); err != nil || status.State != StatePaid {
		t.Fatalf("attempt should be PAID after review: status=%+v err=%v", status, err)
	}
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); err != nil {
		t.Fatalf("close expired session with paid order: %v", err)
	}

	// A new party can start on the same table, and the original ORDER_READ
	// grant still shows the paid order after close (scenario 4).
	opened, err := p.sessions.Open(ctx, p.sessionScope(), ext4Actor)
	if err != nil {
		t.Fatalf("reopen table: %v", err)
	}
	next := p.join(t, opened.Session.ID)
	p.commit(t, next.Session.ID, next.Token, "ext4-meal-next", ordering.Cash)
	if order.OrderReadGrant == nil {
		t.Fatal("commit omitted the ORDER_READ grant")
	}
	status, err := p.orders.ReadOrderStatus(ctx, order.Order.ID, order.OrderReadGrant.Token)
	if err != nil || status.PaymentStatus != "PAID" {
		t.Fatalf("ORDER_READ after close: status=%+v err=%v", status, err)
	}
}

// TestEXT4OrderNearExpiryStillPays covers scenario 1's "order placed near
// expiry": the Checkout created before the boundary stays payable afterwards,
// the paid order is shown as paid and the table still closes.
func TestEXT4OrderNearExpiryStillPays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	grant := p.join(t, "")
	if _, err := p.fixture.Pool.Exec(ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()+interval '2 minutes',version=version+1 WHERE session_id=$1`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	order := p.commit(t, grant.Session.ID, grant.Token, "ext4-near-expiry", ordering.Stripe)
	p.drainWork(t)
	if state := p.attemptCheckoutID(t, order.Order.ID); state == "" {
		t.Fatal("checkout was not created while the session was still live")
	}

	p.expireSession(t, grant.Session.ID)
	// The guest pays after the boundary on the Checkout opened at T-2m.
	p.payViaWebhook(t, order.Order.ID, "evt-ext4-near-expiry-paid")
	paid := p.order(t, grant.Session.ID, order.Order.ID)
	if paid.PaymentStatus != operations.Paid || !paid.PaymentReviewRequired {
		t.Fatalf("payment after session expiry must be PAID + review: %+v", paid)
	}
	if _, err := p.fixture.Operations.ApproveFulfillment(ctx, operations.OrderActionRequest{
		Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: order.Order.ID, Actor: ext4Actor,
		ExpectedVersion: paid.Version, IdempotencyKey: "ext4-review-near-expiry", Reason: "order placed before expiry, paid on open Checkout",
	}); err != nil {
		t.Fatalf("approve review: %v", err)
	}
	p.serve(t, grant.Session.ID, order.Order.ID)
	if status, err := p.orders.ReadOrderStatus(ctx, order.Order.ID, order.OrderReadGrant.Token); err != nil || status.PaymentStatus != "PAID" {
		t.Fatalf("paid order must not be shown unpaid: status=%+v err=%v", status, err)
	}
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); err != nil {
		t.Fatalf("close table after near-expiry payment: %v", err)
	}
}

// TestEXT4GapOrderCommittedBeforeExpiryCreateAfter covers recovery of the EXT-4 gap:
// an order committed moments before the four-hour boundary whose asynchronous
// Checkout creation runs after it becomes a FAILED attempt with no provider
// object. Staff void the uncreated Checkout, then close the expired session.
func TestEXT4GapOrderCommittedBeforeExpiryCreateAfter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	grant := p.join(t, "")
	// A paid sibling must remain paid exactly once through the recovery.
	paidOrder := p.commit(t, grant.Session.ID, grant.Token, "ext4-gap-paid", ordering.Stripe)
	p.drainWork(t)
	p.payViaWebhook(t, paidOrder.Order.ID, "evt-ext4-gap-paid")
	p.payViaWebhook(t, paidOrder.Order.ID, "evt-ext4-gap-paid-duplicate")
	p.serve(t, grant.Session.ID, paidOrder.Order.ID)
	if _, err := p.fixture.Pool.Exec(ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()+interval '2 minutes',version=version+1 WHERE session_id=$1`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	order := p.commit(t, grant.Session.ID, grant.Token, "ext4-gap", ordering.Stripe)
	// The boundary passes before the CREATE worker claims the enqueued work.
	p.expireSession(t, grant.Session.ID)
	p.drainWork(t)
	if checkout := p.attemptCheckoutID(t, order.Order.ID); checkout != "" {
		t.Fatalf("fixture expected no Checkout, got %s", checkout)
	}
	state := p.order(t, grant.Session.ID, order.Order.ID)
	if state.PaymentStatus != operations.Pending {
		t.Fatalf("expected pending order state: %+v", state)
	}
	// Establish the cause before calling it a gap: the CREATE worker must have
	// failed the attempt itself for SESSION_UNAVAILABLE, not for a provider or
	// configuration reason somewhere else.
	var attemptState string
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT state FROM stripe_payment_attempts WHERE order_id=$1`, order.Order.ID).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if attemptState != string(StateFailed) {
		t.Fatalf("attempt state is %s, want %s", attemptState, StateFailed)
	}
	var eventKey string
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT event_key FROM stripe_payment_attempt_events WHERE order_id=$1 AND event_key LIKE 'WORK_STATE:FAILED:%'`, order.Order.ID).Scan(&eventKey); err != nil {
		t.Fatalf("worker recorded no FAILED transition: %v", err)
	}
	if want := "WORK_STATE:FAILED:SESSION_UNAVAILABLE"; eventKey != want {
		t.Fatalf("failure cause is %q, want %q", eventKey, want)
	}
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("unrecovered order must block Close: %v", err)
	}
	r := VoidRequest{
		Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: order.Order.ID, Actor: ext4Actor,
		IdempotencyKey: "ext4-gap-void", Reason: "checkout creation could not start after session expiry", ExpectedVersion: state.Version,
	}
	for _, scope := range []operations.TableScope{
		{TenantID: r.Scope.TenantID, StoreID: "another-store", TableID: r.Scope.TableID},
		{TenantID: "another-tenant", StoreID: r.Scope.StoreID, TableID: r.Scope.TableID},
	} {
		wrong := r
		wrong.Scope = scope
		if _, err := p.service.VoidUnpaidCheckout(ctx, wrong); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-scope void must be denied: %v", err)
		}
	}
	stale := r
	stale.ExpectedVersion++
	if _, err := p.service.VoidUnpaidCheckout(ctx, stale); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("stale version must be denied: %v", err)
	}
	voided, err := p.service.VoidUnpaidCheckout(ctx, r)
	if err != nil || voided.PaymentStatus != operations.Voided || voided.FulfillmentStatus != operations.Cancelled || voided.EffectiveTotalGrossJPY != 0 {
		t.Fatalf("recover uncreated Checkout: %+v err=%v", voided, err)
	}
	assertReplay := func() {
		t.Helper()
		replay, err := p.service.VoidUnpaidCheckout(ctx, r)
		originalJSON, originalErr := json.Marshal(voided)
		replayJSON, replayErr := json.Marshal(replay)
		if err != nil || originalErr != nil || replayErr != nil || string(originalJSON) != string(replayJSON) {
			t.Fatalf("double void must replay without mutation: %+v err=%v marshal=%v/%v", replay, err, originalErr, replayErr)
		}
	}
	assertReplay()
	var auditActor, auditReason string
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT actor_id,reason FROM operation_order_events WHERE order_id=$1 AND event_type='STRIPE_UNPAID_VOIDED'`, r.OrderID).Scan(&auditActor, &auditReason); err != nil || auditActor != r.Actor || auditReason != r.Reason {
		t.Fatalf("void audit: actor=%q reason=%q err=%v", auditActor, auditReason, err)
	}
	var voidEvents, attemptEvents, adjustments, paidEvents, kds, outbox int
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM operation_order_events WHERE order_id=$1 AND event_type='STRIPE_UNPAID_VOIDED'),
		(SELECT count(*) FROM stripe_payment_attempt_events WHERE order_id=$1 AND event_key='STRIPE_VOID:ext4-gap-void'),
		(SELECT count(*) FROM order_adjustments WHERE order_id=$1 AND whole_order_cancel AND refund_amount_jpy=0),
		(SELECT count(*) FROM operation_order_events WHERE order_id=$2 AND event_type='STRIPE_PAID_VERIFIED'),
		(SELECT count(*) FROM operation_kds_cards WHERE order_id=$1),
		(SELECT count(*) FROM operation_outbox WHERE order_id=$1)`, r.OrderID, paidOrder.Order.ID).
		Scan(&voidEvents, &attemptEvents, &adjustments, &paidEvents, &kds, &outbox); err != nil || voidEvents != 1 || attemptEvents != 1 || adjustments != 1 || paidEvents != 1 || kds != 0 || outbox != 0 {
		t.Fatalf("effects: void=%d attempt=%d adjustment=%d paid=%d kds=%d outbox=%d err=%v", voidEvents, attemptEvents, adjustments, paidEvents, kds, outbox, err)
	}
	var localProof bool
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT state='EXPIRED' AND continuation_revoked_at IS NOT NULL AND checkout_session_id IS NULL AND payment_intent_id IS NULL AND checkout_status IS NULL AND provider_payment_status IS NULL AND provider_expires_at IS NULL FROM stripe_payment_attempts WHERE order_id=$1`, r.OrderID).Scan(&localProof); err != nil || !localProof {
		t.Fatalf("local cancellation must not fabricate provider evidence: %v err=%v", localProof, err)
	}
	p.drainWork(t)
	if creates, sessions := p.provider.Counts(); creates != 1 || sessions != 1 {
		t.Fatalf("only the paid sibling may have a provider Checkout: creates=%d sessions=%d", creates, sessions)
	}
	if paid := p.order(t, grant.Session.ID, paidOrder.Order.ID); paid.PaymentStatus != operations.Paid || paid.PaymentReviewRequired {
		t.Fatalf("recovery changed paid sibling: %+v", paid)
	}
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); err != nil {
		t.Fatalf("Close after recovery: %v", err)
	}
	assertReplay()
}

// Only completed, never-dispatched work can prove no remote call happened.
func TestEXT4UncreatedVoidRequiresCompletedUndispatchedCreate(t *testing.T) {
	for _, kind := range []string{"ready", "leased", "dispatched", "unknown-outcome"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			p := newEXT4Platform(t, ctx)
			grant := p.join(t, "")
			order := p.commit(t, grant.Session.ID, grant.Token, "ext4-no-proof", ordering.Stripe)
			p.expireSession(t, grant.Session.ID)
			p.drainWork(t)
			query := map[string]string{
				"ready":           `UPDATE stripe_payment_work SET status='READY' WHERE order_id=$1 AND work_type='CREATE'`,
				"leased":          `UPDATE stripe_payment_work SET status='LEASED',lease_token='in-flight',lease_until=clock_timestamp()+interval '1 minute' WHERE order_id=$1 AND work_type='CREATE'`,
				"dispatched":      `UPDATE stripe_payment_work SET dispatched_at=clock_timestamp() WHERE order_id=$1 AND work_type='CREATE'`,
				"unknown-outcome": `UPDATE stripe_payment_work SET safe_error_code='PROVIDER_OUTCOME_UNKNOWN' WHERE order_id=$1 AND work_type='CREATE'`,
			}[kind]
			if _, err := p.fixture.Pool.Exec(ctx, query, order.Order.ID); err != nil {
				t.Fatal(err)
			}
			before := p.order(t, grant.Session.ID, order.Order.ID)
			if _, err := p.service.VoidUnpaidCheckout(ctx, VoidRequest{
				Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: order.Order.ID, Actor: ext4Actor,
				ExpectedVersion: before.Version, IdempotencyKey: "ext4-no-proof", Reason: "no proof of provider absence",
			}); !errors.Is(err, ErrVoidNotEligible) {
				t.Fatalf("unsafe no-Checkout void must be refused: %v", err)
			}
			if after := p.order(t, grant.Session.ID, order.Order.ID); !reflect.DeepEqual(after, before) {
				t.Fatalf("refused void changed order: before=%+v after=%+v", before, after)
			}
			if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); !errors.Is(err, tablesession.ErrCloseBlocked) {
				t.Fatalf("unresolved work must still block Close: %v", err)
			}
		})
	}
}

func TestEXT4StaleCreateCannotDispatchAfterVoid(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	grant := p.join(t, "")
	order := p.commit(t, grant.Session.ID, grant.Token, "ext4-stale-create", ordering.Stripe)
	expiry := p.sessionExpiresAt(t, grant.Session.ID)
	p.expireSession(t, grant.Session.ID)
	p.drainWork(t)
	state := p.order(t, grant.Session.ID, order.Order.ID)
	if _, err := p.service.VoidUnpaidCheckout(ctx, VoidRequest{
		Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: order.Order.ID, Actor: ext4Actor,
		ExpectedVersion: state.Version, IdempotencyKey: "ext4-stale-void", Reason: "provider was never called",
	}); err != nil {
		t.Fatal(err)
	}
	// Make admission true in this isolated fixture so that the lease fence is
	// tested independently of expiry. No production TTL or session is extended.
	if _, err := p.fixture.Pool.Exec(ctx, `UPDATE table_sessions SET expires_at=$2 WHERE session_id=$1`, grant.Session.ID, expiry); err != nil {
		t.Fatal(err)
	}
	w := workItem{OrderID: order.Order.ID, Lease: "obsolete-lease"}
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT work_id FROM stripe_payment_work WHERE order_id=$1 AND work_type='CREATE'`, order.Order.ID).Scan(&w.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.service.processCreate(ctx, w); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CREATE must fail before provider dispatch: %v", err)
	}
	if creates, sessions := p.provider.Counts(); creates != 0 || sessions != 0 {
		t.Fatalf("voided order acquired a provider Checkout: creates=%d sessions=%d", creates, sessions)
	}
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); err != nil {
		t.Fatalf("stale CREATE must not disturb cancellation: %v", err)
	}
}

func TestEXT4InFlightCreateCannotBeVoidedAfterLeaseReclaimed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	grant := p.join(t, "")
	order := p.commit(t, grant.Session.ID, grant.Token, "ext4-in-flight", ordering.Stripe)
	var checkout stripeprovider.CheckoutResult
	// Pause inside the first provider request, before its response is bound.
	// Reclaim its expired lease after the session expires. That second worker
	// retains UNKNOWN/PROVIDER_OUTCOME_UNKNOWN while the first request is live.
	p.provider.SetNextCreateHook(func(x stripeprovider.CheckoutResult) {
		checkout = x
		p.expireSession(t, grant.Session.ID)
		if _, err := p.fixture.Pool.Exec(ctx, `UPDATE stripe_payment_work SET lease_until=clock_timestamp()-interval '1 second' WHERE order_id=$1 AND work_type='CREATE'`, order.Order.ID); err != nil {
			t.Fatal(err)
		}
		if worked, err := p.service.ProcessNext(ctx); err != nil || !worked {
			t.Fatalf("reclaimed CREATE: worked=%v err=%v", worked, err)
		}
		if id := p.attemptCheckoutID(t, order.Order.ID); id != "" {
			t.Fatalf("first response must still be unbound: %s", id)
		}
		var reclaimedFailure bool
		if err := p.fixture.Pool.QueryRow(ctx, `SELECT a.state='UNKNOWN' AND w.status='DONE' AND w.attempts=2 AND w.dispatched_at IS NOT NULL AND w.safe_error_code='PROVIDER_OUTCOME_UNKNOWN'
			FROM stripe_payment_attempts a JOIN stripe_payment_work w USING(order_id) WHERE a.order_id=$1 AND w.work_type='CREATE'`, order.Order.ID).Scan(&reclaimedFailure); err != nil || !reclaimedFailure {
			t.Fatalf("second lease must fail before first response returns: %v err=%v", reclaimedFailure, err)
		}
		state := p.order(t, grant.Session.ID, order.Order.ID)
		if _, err := p.service.VoidUnpaidCheckout(ctx, VoidRequest{
			Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: order.Order.ID, Actor: ext4Actor,
			ExpectedVersion: state.Version, IdempotencyKey: "ext4-in-flight-void", Reason: "worker failed but first request is live",
		}); !errors.Is(err, ErrVoidNotEligible) {
			t.Fatalf("in-flight provider request must prevent local void: %v", err)
		}
	})
	if worked, err := p.service.ProcessNext(ctx); !worked || !errors.Is(err, ErrConflict) {
		t.Fatalf("stale first response cannot commit: worked=%v err=%v", worked, err)
	}
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("unresolved provider request must block Close: %v", err)
	}
	paid, err := p.provider.SetOutcome(checkout.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := p.service.EnqueueVerifiedWebhook(ctx, stripeprovider.VerifiedEvent{
		ID: "evt-ext4-in-flight-paid", Type: "checkout.session.completed", AccountID: paid.AccountID,
		APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: paid.SessionID, Checkout: &paid,
	}); err != nil || !accepted {
		t.Fatalf("late paid webhook: accepted=%v err=%v", accepted, err)
	}
	p.drainWork(t)
	p.payViaWebhook(t, order.Order.ID, "evt-ext4-in-flight-paid-duplicate")
	state := p.order(t, grant.Session.ID, order.Order.ID)
	if state.PaymentStatus != operations.Paid || !state.PaymentReviewRequired {
		t.Fatalf("late payment must be retained with review: %+v", state)
	}
	var paidEvents int
	if err := p.fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_order_events WHERE order_id=$1 AND event_type='STRIPE_PAID_VERIFIED'`, order.Order.ID).Scan(&paidEvents); err != nil || paidEvents != 1 {
		t.Fatalf("paid must be recorded exactly once: events=%d err=%v", paidEvents, err)
	}
	if _, err := p.fixture.Operations.ApproveFulfillment(ctx, operations.OrderActionRequest{
		Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: order.Order.ID, Actor: ext4Actor,
		ExpectedVersion: state.Version, IdempotencyKey: "ext4-in-flight-review", Reason: "verified payment after expiry",
	}); err != nil {
		t.Fatalf("approve paid order: %v", err)
	}
	p.serve(t, grant.Session.ID, order.Order.ID)
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); err != nil {
		t.Fatalf("close after paid review: %v", err)
	}
	if creates, sessions := p.provider.Counts(); creates != 1 || sessions != 1 {
		t.Fatalf("reclaim must not create a second Checkout: creates=%d sessions=%d", creates, sessions)
	}
}

// TestEXT4PaymentContinuationWindow covers scenario 2: continuation works at
// minute 29, is denied at minute 31, and the payment completed on the still
// open Stripe Checkout is recorded once and shown as paid.
func TestEXT4PaymentContinuationWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	grant := p.join(t, "")
	token, proof := p.issueProof(t)
	if _, err := p.proofs.Verify(token, time.Now().UTC()); err != nil {
		t.Fatalf("fresh proof must verify: %v", err)
	}
	cart := p.addLine(t, grant.Session.ID, grant.Token, "ext4-cont-line")
	preview, err := p.orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	var hash [32]byte
	copy(hash[:], proof.Hash)
	order, err := p.orders.Commit(ctx, ordering.CommitRequest{
		SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "ext4-cont", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe,
		CheckoutProofHash: hash, CheckoutProofExpiresAt: proof.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.drainWork(t)
	// continuation = min(commit + 30m, proof expiry). This proof was issued
	// moments before the commit, so its expiry is the effective deadline.
	var cappedByProof int
	if err = p.fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempts WHERE order_id=$1 AND continuation_expires_at <> checkout_proof_expires_at`, order.Order.ID).Scan(&cappedByProof); err != nil {
		t.Fatal(err)
	}
	if cappedByProof != 0 {
		t.Fatalf("continuation is not capped by the checkout proof expiry")
	}
	wantLifetime(t, "continuation window", p.dbDeadline(t, `SELECT extract(epoch FROM continuation_expires_at - clock_timestamp()) FROM stripe_payment_attempts WHERE order_id=$1`, order.Order.ID), ext4ContinuationTTL)

	// A proof with only five minutes left must shorten the window to five
	// minutes rather than grant a fresh 30.
	agedToken, agedProof := p.issueProofAt(t, time.Now().UTC().Add(-ext4ProofTTL+5*time.Minute))
	if _, err = p.proofs.Verify(agedToken, time.Now().UTC()); err != nil {
		t.Fatalf("a proof with five minutes left must still be usable: %v", err)
	}
	aged := p.commit(t, grant.Session.ID, grant.Token, "ext4-cont-aged", ordering.Stripe, agedProof)
	p.drainWork(t)
	agedWindow := p.dbDeadline(t, `SELECT extract(epoch FROM continuation_expires_at - clock_timestamp()) FROM stripe_payment_attempts WHERE order_id=$1`, aged.Order.ID)
	wantLifetime(t, "continuation window capped by a five-minute proof", agedWindow, 5*time.Minute)
	if err = p.fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM stripe_payment_attempts WHERE order_id=$1 AND continuation_expires_at <> checkout_proof_expires_at`, aged.Order.ID).Scan(&cappedByProof); err != nil {
		t.Fatal(err)
	}
	if cappedByProof != 0 {
		t.Fatalf("aged-proof continuation is not capped by its proof expiry")
	}
	agedState := p.order(t, grant.Session.ID, aged.Order.ID)
	if _, err := p.service.VoidUnpaidCheckout(ctx, VoidRequest{
		Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: aged.Order.ID, Actor: ext4Actor,
		IdempotencyKey: "ext4-cont-aged-void", Reason: "window probe, guest never paid", ExpectedVersion: agedState.Version,
	}); err != nil {
		t.Fatalf("void the aged-window probe order: %v", err)
	}

	// Minute 29: still resumable. The clock position is simulated by moving the
	// persisted continuation deadline (CanResume/Resume read it from the
	// database); the proof's own 30m boundary is pinned in issueProofAt.
	if _, err = p.fixture.Pool.Exec(ctx, `UPDATE stripe_payment_attempts SET continuation_expires_at=clock_timestamp()+interval '1 minute' WHERE order_id=$1`, order.Order.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.service.CanResume(ctx, grant.Session.ID, order.Order.ID, hash); err != nil || !ok {
		t.Fatalf("minute 29 resume: ok=%v err=%v", ok, err)
	}
	private, err := p.service.Resume(ctx, grant.Session.ID, order.Order.ID, hash)
	if err != nil || private.URL == "" {
		t.Fatalf("minute 29 private Checkout: %+v err=%v", private, err)
	}

	// Minute 31: continuation is over, but the Stripe page opened at minute 29
	// still completes and the webhook must record it exactly once.
	if _, err = p.fixture.Pool.Exec(ctx, `UPDATE stripe_payment_attempts SET continuation_expires_at=clock_timestamp()-interval '1 minute' WHERE order_id=$1`, order.Order.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.service.CanResume(ctx, grant.Session.ID, order.Order.ID, hash); err != nil || ok {
		t.Fatalf("minute 31 resume must be denied: ok=%v err=%v", ok, err)
	}
	if _, err := p.service.Resume(ctx, grant.Session.ID, order.Order.ID, hash); !errors.Is(err, ErrProofDenied) {
		t.Fatalf("minute 31 private Checkout: got %v, want ErrProofDenied", err)
	}
	p.payViaWebhook(t, order.Order.ID, "evt-ext4-cont-paid")
	if state := p.order(t, grant.Session.ID, order.Order.ID); state.PaymentStatus != operations.Paid || state.PaymentReviewRequired {
		t.Fatalf("payment on open Checkout must be PAID without review while the session is live: %+v", state)
	}
	if status, err := p.service.ReadSafeStatus(ctx, p.tableScope(), grant.Session.ID, order.Order.ID); err != nil || status.State != StatePaid {
		t.Fatalf("attempt should be PAID: status=%+v err=%v", status, err)
	}
	if n := p.attemptCount(t, order.Order.ID); n != 1 {
		t.Fatalf("guest was charged through %d attempts, want exactly 1", n)
	}
	if _, err := p.orders.ReadOrderStatus(ctx, order.Order.ID, order.OrderReadGrant.Token); err != nil {
		t.Fatalf("ORDER_READ after continuation expiry: %v", err)
	}
	p.serve(t, grant.Session.ID, order.Order.ID)
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); err != nil {
		t.Fatalf("close after continuation-expired payment: %v", err)
	}
}

// TestEXT4CloseWithExpiredSession covers scenario 3: an expired session can
// still be closed once every order is settled and served or voided.
func TestEXT4CloseWithExpiredSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	grant := p.join(t, "")

	unpaidCash := p.commit(t, grant.Session.ID, grant.Token, "ext4-close-cash", ordering.Cash)
	paidStripe := p.commit(t, grant.Session.ID, grant.Token, "ext4-close-paid", ordering.Stripe)
	p.drainWork(t)
	p.payViaWebhook(t, paidStripe.Order.ID, "evt-ext4-close-paid")
	voided := p.commit(t, grant.Session.ID, grant.Token, "ext4-close-void", ordering.Stripe)
	p.drainWork(t)
	voidedState := p.order(t, grant.Session.ID, voided.Order.ID)
	if _, err := p.service.VoidUnpaidCheckout(ctx, VoidRequest{
		Scope: p.tableScope(), SessionID: grant.Session.ID, OrderID: voided.Order.ID, Actor: ext4Actor,
		IdempotencyKey: "ext4-close-void", Reason: "guest left without paying", ExpectedVersion: voidedState.Version,
	}); err != nil {
		t.Fatalf("void unpaid checkout: %v", err)
	}
	p.serve(t, grant.Session.ID, paidStripe.Order.ID)

	p.expireSession(t, grant.Session.ID)
	// Unpaid cash still blocks close: the guest owes money for food received.
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("close with unpaid cash: got %v, want ErrCloseBlocked", err)
	}
	// Staff settle the expired but still open session, then serve and close.
	p.settleCash(t, grant.Session.ID)
	p.serve(t, grant.Session.ID, unpaidCash.Order.ID)
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); err != nil {
		t.Fatalf("close after settling on an expired session: %v", err)
	}
	if states, err := p.fixture.Operations.ReadSession(ctx, p.tableScope(), grant.Session.ID); err != nil || states.Status != tablesession.Closed {
		t.Fatalf("session should be CLOSED: %+v err=%v", states.Status, err)
	}
}

// TestEXT4OrderReadSurvivesSessionAndClose covers scenario 4: the 24h
// ORDER_READ grant outlives session expiry and table close, and its own TTL
// still ends access.
func TestEXT4OrderReadSurvivesSessionAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	grant := p.join(t, "")
	order := p.commit(t, grant.Session.ID, grant.Token, "ext4-read", ordering.Cash)
	if order.OrderReadGrant == nil {
		t.Fatal("commit omitted the ORDER_READ grant")
	}
	// The platform uses ordering.New, so this is the production default; assert
	// both the constant and the deadline the guest actually received.
	if ordering.DefaultOrderReadTTL != ext4OrderReadTTL {
		t.Fatalf("ordering.DefaultOrderReadTTL is %s, want the adopted %s", ordering.DefaultOrderReadTTL, ext4OrderReadTTL)
	}
	hash := sha256.Sum256([]byte(order.OrderReadGrant.Token))
	wantLifetime(t, "ORDER_READ TTL", p.dbDeadline(t, `SELECT extract(epoch FROM expires_at - clock_timestamp()) FROM order_read_grants WHERE token_hash=$1`, hash[:]), ext4OrderReadTTL)

	p.expireSession(t, grant.Session.ID)
	if status, err := p.orders.ReadOrderStatus(ctx, order.Order.ID, order.OrderReadGrant.Token); err != nil || status.PaymentStatus != "UNPAID" {
		t.Fatalf("ORDER_READ after session expiry: status=%+v err=%v", status, err)
	}

	p.settleCash(t, grant.Session.ID)
	p.serve(t, grant.Session.ID, order.Order.ID)
	if err := p.sessions.Close(ctx, p.sessionScope(), grant.Session.ID, ext4Actor); err != nil {
		t.Fatalf("close: %v", err)
	}
	status, err := p.orders.ReadOrderStatus(ctx, order.Order.ID, order.OrderReadGrant.Token)
	if err != nil || status.PaymentStatus != "PAID" || status.FulfillmentStatus != "SERVED" {
		t.Fatalf("ORDER_READ after close: status=%+v err=%v", status, err)
	}
	// Closing revoked TABLE_JOIN: no new capability via the old join token.
	if _, err := p.orders.RequestOrderRead(ctx, grant.Session.ID, grant.Token, order.Order.ID); !errors.Is(err, ordering.ErrDenied) {
		t.Fatalf("ORDER_READ request after close: got %v, want ErrDenied", err)
	}
	// The adopted 24h lifetime still ends the capability.
	if _, err = p.fixture.Pool.Exec(ctx, `UPDATE order_read_grants SET created_at=clock_timestamp()-interval '25 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE token_hash=$1`, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := p.orders.ReadOrderStatus(ctx, order.Order.ID, order.OrderReadGrant.Token); !errors.Is(err, ordering.ErrDenied) {
		t.Fatalf("expired ORDER_READ: got %v, want ErrDenied", err)
	}
}

// TestEXT4PINTTLAndLockout pins the adopted PIN lifetimes: 15 minutes for new
// PIN joins, three attempts, one-minute lockout, and staff ResetPIN recovery.
// Existing grants are untouched by PIN expiry, but ResetPIN revokes them, and a
// grant issued near the session boundary is capped by the session expiry.
func TestEXT4PINTTLAndLockout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newEXT4Platform(t, ctx)
	if err := p.sessions.SetStoreMode(ctx, p.fixture.TenantID, p.fixture.StoreID, tablesession.SessionPIN); err != nil {
		t.Fatal(err)
	}
	opened, err := p.sessions.Open(ctx, p.sessionScope(), ext4Actor)
	if err != nil || opened.PIN == "" {
		t.Fatalf("open PIN session: %+v err=%v", opened, err)
	}
	if ttl := opened.Session.ExpiresAt.Sub(opened.Session.OpenedAt); ttl < ext4SessionTTL || ttl > ext4SessionTTL+time.Millisecond {
		t.Fatalf("session TTL is not the adopted 4h: %s", ttl)
	}
	sessionID := opened.Session.ID
	// Open writes the PIN deadline from the database clock it also stamps the
	// session with, so this pins the 15-minute PIN lifetime at issuance.
	wantLifetime(t, "PIN TTL", p.dbDeadline(t, `SELECT extract(epoch FROM pin_expires_at - clock_timestamp()) FROM table_sessions WHERE session_id=$1`, sessionID), ext4PINTTL)
	wrong := "000000"
	if opened.PIN == wrong {
		wrong = "999999"
	}
	for i := 1; i <= ext4PINMaxAttempts; i++ {
		_, err := p.sessions.Join(ctx, sessionID, sessionID, wrong)
		want := tablesession.ErrInvalidPIN
		if i == ext4PINMaxAttempts {
			want = tablesession.ErrPINLocked
		}
		if !errors.Is(err, want) {
			t.Fatalf("wrong PIN attempt %d: got %v, want %v", i, err, want)
		}
	}
	// The lockout triggered by the third wrong attempt lasts the adopted minute.
	wantLifetime(t, "PIN lockout", p.dbDeadline(t, `SELECT extract(epoch FROM pin_locked_until - clock_timestamp()) FROM table_sessions WHERE session_id=$1`, sessionID), ext4PINLockout)
	if _, err := p.sessions.Join(ctx, sessionID, sessionID, opened.PIN); !errors.Is(err, tablesession.ErrPINLocked) {
		t.Fatalf("locked PIN: got %v, want ErrPINLocked", err)
	}
	if _, err = p.fixture.Pool.Exec(ctx, `UPDATE table_sessions SET pin_locked_until=clock_timestamp()-interval '1 second' WHERE session_id=$1`, sessionID); err != nil {
		t.Fatal(err)
	}
	grant, err := p.sessions.Join(ctx, sessionID, sessionID, opened.PIN)
	if err != nil {
		t.Fatalf("PIN join after lockout: %v", err)
	}
	grantHash := sha256.Sum256([]byte(grant.Token))
	wantLifetime(t, "TABLE_JOIN grant TTL", p.dbDeadline(t, `SELECT extract(epoch FROM expires_at - clock_timestamp()) FROM table_join_grants WHERE token_hash=$1`, grantHash[:]), ext4GrantTTL)
	p.addLine(t, sessionID, grant.Token, "ext4-pin-line")

	// A grant issued close to the session boundary is capped by it rather than
	// outliving the session it belongs to.
	if _, err = p.fixture.Pool.Exec(ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()+interval '20 minutes',version=version+1 WHERE session_id=$1`, sessionID); err != nil {
		t.Fatal(err)
	}
	cappedExpiry := p.sessionExpiresAt(t, sessionID)
	nearGrant, err := p.sessions.Join(ctx, sessionID, sessionID, opened.PIN)
	if err != nil {
		t.Fatalf("PIN join near the session boundary: %v", err)
	}
	nearHash := sha256.Sum256([]byte(nearGrant.Token))
	wantLifetime(t, "grant capped by session expiry", p.dbDeadline(t, `SELECT extract(epoch FROM expires_at - clock_timestamp()) FROM table_join_grants WHERE token_hash=$1`, nearHash[:]), 20*time.Minute)
	var uncapped int
	if err = p.fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM table_join_grants g JOIN table_sessions s USING(session_id) WHERE g.token_hash=$1 AND g.expires_at <> s.expires_at`, nearHash[:]).Scan(&uncapped); err != nil {
		t.Fatal(err)
	}
	if uncapped != 0 {
		t.Fatal("grant near the session boundary is not capped by the session expiry")
	}

	// PIN expiry (15m) stops new PIN joins only; the issued grant keeps working.
	if _, err = p.fixture.Pool.Exec(ctx, `UPDATE table_sessions SET pin_expires_at=clock_timestamp()-interval '1 second' WHERE session_id=$1`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.sessions.Join(ctx, sessionID, sessionID, opened.PIN); !errors.Is(err, tablesession.ErrInvalidPIN) {
		t.Fatalf("PIN after PINTTL: got %v, want ErrInvalidPIN", err)
	}
	if _, err := p.orders.ReadCart(ctx, sessionID, grant.Token); err != nil {
		t.Fatalf("issued grant must survive PIN expiry: %v", err)
	}
	// An expired PIN needs staff recovery. ResetPIN also bumps the grant
	// generation, which revokes every grant issued under the old PIN.
	reset, err := p.sessions.ResetPIN(ctx, p.sessionScope(), sessionID, ext4Actor)
	if err != nil || reset.PIN == "" || reset.PIN == opened.PIN {
		t.Fatalf("staff PIN reset: %+v err=%v", reset, err)
	}
	for _, revoked := range []string{grant.Token, nearGrant.Token} {
		if _, err := p.orders.ReadCart(ctx, sessionID, revoked); !errors.Is(err, ordering.ErrDenied) {
			t.Fatalf("grant issued before ResetPIN: got %v, want ErrDenied", err)
		}
	}
	if _, err := p.sessions.Join(ctx, sessionID, sessionID, reset.PIN); err != nil {
		t.Fatalf("join with reset PIN: %v", err)
	}
	if moved := p.sessionExpiresAt(t, sessionID); !moved.Equal(cappedExpiry) {
		t.Fatalf("session expiry moved from %s to %s", cappedExpiry, moved)
	}
}
