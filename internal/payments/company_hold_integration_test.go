package payments

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

type holdFixture struct {
	*testguest.Fixture
	service  *Service
	provider *testpayments.Provider
	ctx      context.Context
	t        *testing.T
}

func newHoldFixture(t *testing.T) *holdFixture {
	t.Helper()
	return newHoldFixtureWithRetryWindow(t, time.Hour)
}

func newHoldFixtureWithRetryWindow(t *testing.T, retryWindow time.Duration) *holdFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close(context.Background()) })
	provider := testpayments.NewProvider()
	account := stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_holdFixture"}
	service, err := New(f.Pool, f.Operations, testResolver{"fixture:hold": {Provider: provider, Account: account, ProfileFingerprint: sha256.Sum256([]byte("hold-test"))}}, Config{CreateRetryWindow: retryWindow, ContinuationTTL: 20 * time.Minute, WorkLeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.Orders, err = ordering.NewWithOrderPolicy(f.Pool, f.Sessions, f.Menus, service)
	if err != nil {
		t.Fatal(err)
	}
	h := &holdFixture{Fixture: f, service: service, provider: provider, ctx: ctx, t: t}
	h.merchant(f.TenantID, f.StoreID)
	return h
}

func (h *holdFixture) merchant(tenant, store string) {
	h.t.Helper()
	_, err := h.service.SetMerchantConfig(h.ctx, operations.StoreScope{TenantID: tenant, StoreID: store}, 0, "fixture-admin", MerchantConfigInput{Enabled: true, Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_holdFixture", CredentialRef: "fixture:hold", WebhookEndpointRef: "fixture:webhook", APIVersion: stripeprovider.APIVersion})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *holdFixture) commit(locator, product string, method ordering.PaymentMethod) (ordering.CommitResult, [32]byte) {
	h.t.Helper()
	grant, err := h.Sessions.DirectJoin(h.ctx, locator, "")
	if err != nil {
		h.t.Fatal(err)
	}
	cart, err := h.Orders.ReadCart(h.ctx, grant.Session.ID, grant.Token)
	if err != nil {
		h.t.Fatal(err)
	}
	key := fmt.Sprintf("hold-order-%s-%d", grant.Session.ID, cart.Generation)
	var options []catalog.OptionSelection
	if product == "teishoku" {
		options = []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}}
	}
	cart, err = h.Orders.Mutate(h.ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: key + "-line", Kind: ordering.AddLine, ProductKey: product, Quantity: 1, Options: options})
	if err != nil {
		h.t.Fatal(err)
	}
	preview, err := h.Orders.Preview(h.ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		h.t.Fatal(err)
	}
	proof := sha256.Sum256([]byte(key))
	result, err := h.Orders.Commit(h.ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: key, PreviewFingerprint: preview.Fingerprint, PaymentMethod: method, CheckoutProofHash: proof, CheckoutProofExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil {
		h.t.Fatal(err)
	}
	if method == ordering.Stripe {
		scope := operations.TableScope{TenantID: grant.Session.Scope.TenantID, StoreID: grant.Session.Scope.StoreID, TableID: grant.Session.Scope.TableID}
		if _, err = h.Operations.AcceptOrder(h.ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: result.Order.ID, Actor: "fixture-manager", ExpectedVersion: 1, IdempotencyKey: key + "-accept"}); err != nil {
			h.t.Fatal(err)
		}
	}
	return result, proof
}

func (h *holdFixture) hold(active bool, version int64) {
	h.t.Helper()
	if _, err := h.Menus.SetCompanyProductEmergencyHold(h.ctx, h.TenantID, "teishoku", "fixture-admin", active, "safety review", version, fmt.Sprintf("hold-%d", version)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *holdFixture) work() {
	h.t.Helper()
	if worked, err := h.service.ProcessNext(h.ctx); err != nil || !worked {
		h.t.Fatalf("worker: worked=%v err=%v", worked, err)
	}
}

func (h *holdFixture) ready() {
	h.t.Helper()
	if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE status='READY'`); err != nil {
		h.t.Fatal(err)
	}
}

func (h *holdFixture) checkout(order string) stripeprovider.CheckoutResult {
	h.t.Helper()
	var id string
	if err := h.Pool.QueryRow(h.ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, order).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	x, err := h.provider.RetrieveCheckout(h.ctx, stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_holdFixture"}, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return x
}

func (h *holdFixture) event(x stripeprovider.CheckoutResult, id, kind string) {
	h.t.Helper()
	e := stripeprovider.VerifiedEvent{ID: id, Type: kind, AccountID: x.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().Add(-time.Hour), ObjectID: x.SessionID, Checkout: &x}
	added, err := h.service.EnqueueVerifiedWebhook(h.ctx, e)
	if err != nil || !added {
		h.t.Fatalf("enqueue: added=%v err=%v", added, err)
	}
	added, err = h.service.EnqueueVerifiedWebhook(h.ctx, e)
	if err != nil || added {
		h.t.Fatalf("duplicate: added=%v err=%v", added, err)
	}
}

func (h *holdFixture) assertState(order string, want AttemptState, paid, review bool) {
	h.t.Helper()
	var state, payment string
	var attemptReview, operationReview bool
	var cards, releaseEvents, outbox int
	err := h.Pool.QueryRow(h.ctx, `SELECT a.state,a.payment_review_required,o.payment_status,o.payment_review_required,
        (SELECT count(*) FROM operation_kds_cards WHERE order_id=a.order_id),
        (SELECT count(*) FROM operation_order_events WHERE order_id=a.order_id AND event_type='KDS_RELEASED'),
        (SELECT count(*) FROM operation_outbox WHERE order_id=a.order_id)
        FROM stripe_payment_attempts a JOIN operation_order_state o USING(order_id) WHERE a.order_id=$1`, order).Scan(&state, &attemptReview, &payment, &operationReview, &cards, &releaseEvents, &outbox)
	if err != nil {
		h.t.Fatal(err)
	}
	if state != string(want) || (payment == "PAID") != paid || attemptReview != review || operationReview != review {
		h.t.Fatalf("state=%s payment=%s review=%v/%v want=%s paid=%v review=%v", state, payment, attemptReview, operationReview, want, paid, review)
	}
	if paid && !review && (cards != 1 || releaseEvents != 1 || outbox != 1) {
		h.t.Fatalf("normal paid release must occur once: %d/%d/%d", cards, releaseEvents, outbox)
	}
	if (!paid || review) && (cards != 0 || releaseEvents != 0 || outbox != 0) {
		h.t.Fatalf("unexpected KDS: cards=%d events=%d outbox=%d", cards, releaseEvents, outbox)
	}
}

func (h *holdFixture) addStore(tenant, store string, detached bool) string {
	h.t.Helper()
	if tenant != h.TenantID {
		if _, err := h.Pool.Exec(h.ctx, `INSERT INTO tenants(tenant_id,name) VALUES($1,$1)`, tenant); err != nil {
			h.t.Fatal(err)
		}
		draft, err := h.Menus.GetCompanyDraft(h.ctx, h.TenantID)
		if err != nil {
			h.t.Fatal(err)
		}
		saved, err := h.Menus.SaveCompanyDraft(h.ctx, tenant, "fixture-admin", 0, draft.CatalogData)
		if err != nil {
			h.t.Fatal(err)
		}
		if _, err = h.Menus.PublishCompanyDraft(h.ctx, tenant, "fixture-admin", saved.Version); err != nil {
			h.t.Fatal(err)
		}
	}
	locator := tenant + "-" + store
	if _, err := h.Pool.Exec(h.ctx, `INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode) VALUES($1,$2,$2,'DIRECT_SCAN')`, tenant, store); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.Pool.Exec(h.ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES($1,$2,'table-01','table',$3)`, tenant, store, locator); err != nil {
		h.t.Fatal(err)
	}
	scope := catalog.StoreScope{TenantID: tenant, StoreID: store}
	menu, err := h.Menus.ReadStoreMenu(h.ctx, scope)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range menu.Categories {
		for _, p := range c.Products {
			if _, err = h.Menus.SetStoreAvailability(h.ctx, scope, "fixture-admin", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: p.Key}, Status: catalog.Available, ExpectedVersion: p.AvailabilityVersion}); err != nil {
				h.t.Fatal(err)
			}
			for _, g := range p.Groups {
				for _, o := range g.Options {
					if _, err = h.Menus.SetStoreAvailability(h.ctx, scope, "fixture-admin", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.OptionAvailability, ProductKey: p.Key, GroupKey: g.Key, OptionKey: o.Key}, Status: catalog.Available, ExpectedVersion: o.AvailabilityVersion}); err != nil {
						h.t.Fatal(err)
					}
				}
			}
		}
	}
	if detached {
		if _, err = h.Menus.DetachStore(h.ctx, scope, "fixture-admin", menu.Revision); err != nil {
			h.t.Fatal(err)
		}
	}
	h.merchant(tenant, store)
	return locator
}

func TestPostgresP82HoldTargetsAndOffRetainsWork(t *testing.T) {
	h := newHoldFixture(t)
	linked := h.addStore(h.TenantID, "linked", false)
	detached := h.addStore(h.TenantID, "detached", true)
	other := h.addStore("other-tenant", "other-store", false)
	held, proof := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work()
	second, _ := h.commit(linked, "teishoku", ordering.Stripe)
	h.work()
	for _, tc := range []struct {
		locator, product string
		method           ordering.PaymentMethod
	}{
		{detached, "teishoku", ordering.Stripe}, {other, "teishoku", ordering.Stripe}, {h.Locator, "tea", ordering.Stripe}, {h.Locator, "teishoku", ordering.Cash},
	} {
		h.commit(tc.locator, tc.product, tc.method)
		if tc.method == ordering.Stripe {
			h.work()
		}
	}
	// A locally confirmed paid attempt is outside the unpaid target set.
	paid, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work()
	x := h.checkout(paid.Order.ID)
	x, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	h.event(x, "paid-before-hold", "checkout.session.completed")
	h.work()
	var before string
	if err = h.Pool.QueryRow(h.ctx, `SELECT quote_snapshot::text FROM table_orders WHERE order_id=$1`, held.Order.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	h.hold(true, 0)
	var targeted []string
	rows, err := h.Pool.Query(h.ctx, `SELECT order_id FROM stripe_payment_work WHERE work_type='EXPIRE' ORDER BY order_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		targeted = append(targeted, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(targeted) != 2 || !(targeted[0] == held.Order.ID || targeted[1] == held.Order.ID) || !(targeted[0] == second.Order.ID || targeted[1] == second.Order.ID) {
		t.Fatalf("wrong targets: %v", targeted)
	}
	if ok, err := h.service.CanResume(h.ctx, held.Order.SessionID, held.Order.ID, proof); err != nil || ok {
		t.Fatalf("held continuation: %v %v", ok, err)
	}
	h.hold(false, 1)
	h.work()
	h.work()
	h.assertState(held.Order.ID, StateExpired, false, false)
	h.assertState(second.Order.ID, StateExpired, false, false)
	h.assertState(paid.Order.ID, StatePaid, true, false)
	var after string
	if err = h.Pool.QueryRow(h.ctx, `SELECT quote_snapshot::text FROM table_orders WHERE order_id=$1`, held.Order.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("hold mutated immutable order snapshot")
	}
	if _, err = h.service.Resume(h.ctx, held.Order.SessionID, held.Order.ID, proof); err != ErrProofDenied {
		t.Fatalf("OFF restored continuation: %v", err)
	}
	if _, err = h.Pool.Exec(h.ctx, `UPDATE stripe_payment_attempts SET company_hold_expire_requested_at=NULL WHERE order_id=$1`, held.Order.ID); err == nil {
		t.Fatal("retained marker was cleared")
	}
}

func TestPostgresP82ExpireUnknownRetrievesTrueOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  stripeprovider.CheckoutStatus
		payment stripeprovider.PaymentStatus
		want    AttemptState
	}{
		{"open", stripeprovider.CheckoutOpen, stripeprovider.PaymentUnpaid, StateOpen},
		{"expired", stripeprovider.CheckoutExpired, stripeprovider.PaymentUnpaid, StateExpired},
		{"paid", stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid, StateReviewRequired},
		{"complete-unpaid", stripeprovider.CheckoutComplete, stripeprovider.PaymentUnpaid, StateProcessing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
			h.work()
			h.hold(true, 0)
			h.provider.SetNextExpireOutcome(tc.status, tc.payment, true)
			r0, e0 := h.provider.CheckoutOperationCounts()
			h.work()
			r1, e1 := h.provider.CheckoutOperationCounts()
			if r1-r0 != 2 || e1-e0 != 1 {
				t.Fatalf("unknown must retrieve before and after expire: retrieve=%d expire=%d", r1-r0, e1-e0)
			}
			if tc.payment == stripeprovider.PaymentPaid {
				h.work()
			}
			h.assertState(order.Order.ID, tc.want, tc.payment == stripeprovider.PaymentPaid, tc.payment == stripeprovider.PaymentPaid)
			if tc.status == stripeprovider.CheckoutOpen {
				h.hold(false, 1)
				h.ready()
				h.work()
				h.assertState(order.Order.ID, StateExpired, false, false)
			}
			if tc.status == stripeprovider.CheckoutComplete && tc.payment == stripeprovider.PaymentUnpaid {
				// Completion alone is not paid. Keep retrieving until async payment resolves.
				x := h.checkout(order.Order.ID)
				if _, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid); err != nil {
					t.Fatal(err)
				}
				h.hold(false, 1)
				h.ready()
				h.work()
				h.work()
				h.assertState(order.Order.ID, StateReviewRequired, true, true)
			}
			if creates, sessions := h.provider.Counts(); creates != 1 || sessions != 1 {
				t.Fatalf("created another Checkout: %d/%d", creates, sessions)
			}
		})
	}
}

func TestPostgresP82PaidAndExpiredWebhookOrders(t *testing.T) {
	for _, paidFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("paid-first-%v", paidFirst), func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
			h.work()
			h.hold(true, 0)
			x := h.checkout(order.Order.ID)
			paid := x
			paid.Status = stripeprovider.CheckoutComplete
			paid.PaymentStatus = stripeprovider.PaymentPaid
			expired := x
			expired.Status = stripeprovider.CheckoutExpired
			// Model a paid webhook delayed behind an expired observation, as well
			// as a stale expired delivery arriving after verified payment.
			if paidFirst {
				if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp()+interval '1 hour' WHERE work_type='EXPIRE'`); err != nil {
					t.Fatal(err)
				}
				h.event(paid, "paid-first", "checkout.session.completed")
				h.work()
				h.assertState(order.Order.ID, StateReviewRequired, true, true)
				h.ready()
				h.work() // Provider's stale unpaid observation must not erase PAID.
				h.event(expired, "expired-later", "checkout.session.expired")
				h.work()
			} else {
				h.work()
				h.assertState(order.Order.ID, StateExpired, false, false)
				h.event(expired, "expired-first", "checkout.session.expired")
				h.work()
				h.event(paid, "paid-later", "checkout.session.completed")
				h.work()
			}
			h.event(paid, "paid-duplicate-object", "checkout.session.async_payment_succeeded")
			h.work()
			h.assertState(order.Order.ID, StateReviewRequired, true, true)
			var paidEvents, reviewEvents int
			if err := h.Pool.QueryRow(h.ctx, `SELECT count(*) FILTER(WHERE event_type='STRIPE_PAID_VERIFIED'),count(*) FILTER(WHERE event_type='STRIPE_REVIEW_REQUIRED') FROM operation_order_events WHERE order_id=$1`, order.Order.ID).Scan(&paidEvents, &reviewEvents); err != nil {
				t.Fatal(err)
			}
			if paidEvents != 1 || reviewEvents != 1 {
				t.Fatalf("duplicate effects: paid=%d review=%d", paidEvents, reviewEvents)
			}
		})
	}
}

func TestPostgresP82PendingAndInflightCreation(t *testing.T) {
	for _, mode := range []string{"pending", "inflight", "lost-response", "paid-create"} {
		t.Run(mode, func(t *testing.T) {
			h := newHoldFixture(t)
			order, proof := h.commit(h.Locator, "teishoku", ordering.Stripe)
			if mode == "pending" {
				h.hold(true, 0)
				h.hold(false, 1)
				// Let EXPIRE run before CREATE to exercise the missing-ID branch.
				if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp()+interval '1 hour' WHERE work_type='CREATE'`); err != nil {
					t.Fatal(err)
				}
				h.work()
				var status string
				if err := h.Pool.QueryRow(h.ctx, `SELECT status FROM stripe_payment_work WHERE work_type='EXPIRE'`).Scan(&status); err != nil || status != "READY" {
					t.Fatalf("missing ID work lost: %s %v", status, err)
				}
				if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE work_type='CREATE'`); err != nil {
					t.Fatal(err)
				}
				h.work()
			} else {
				if mode == "paid-create" {
					h.provider.SetNextCreateOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
				}
				if mode == "lost-response" {
					h.provider.LoseNextCreateResponse()
				}
				entered, release := make(chan struct{}), make(chan struct{})
				releaseResponse := sync.OnceFunc(func() { close(release) })
				defer releaseResponse()
				h.provider.SetNextCreateHook(func(stripeprovider.CheckoutResult) { close(entered); <-release })
				result := make(chan error, 1)
				go func() { _, err := h.service.ProcessNext(h.ctx); result <- err }()
				select {
				case <-entered:
				case <-h.ctx.Done():
					t.Fatal(h.ctx.Err())
				}
				h.hold(true, 0)
				h.work() // EXPIRE cannot drop a not-yet-bound in-flight session.
				h.hold(false, 1)
				releaseResponse()
				if err := <-result; err != nil {
					t.Fatal(err)
				}
				if mode == "lost-response" {
					if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE work_type='CREATE'`); err != nil {
						t.Fatal(err)
					}
					h.work()
				}
			}
			if ok, err := h.service.CanResume(h.ctx, order.Order.SessionID, order.Order.ID, proof); err != nil || ok {
				t.Fatalf("held create exposed URL: %v %v", ok, err)
			}
			h.ready()
			h.work()
			if mode == "paid-create" {
				h.assertState(order.Order.ID, StateReviewRequired, true, true)
			} else {
				h.assertState(order.Order.ID, StateExpired, false, false)
			}
			wantCreates := 1
			if mode == "lost-response" {
				wantCreates = 2
			}
			if creates, sessions := h.provider.Counts(); creates != wantCreates || sessions != 1 {
				t.Fatalf("same-key recovery: creates=%d sessions=%d", creates, sessions)
			}
		})
	}
}

// Hold ON deliberately waits on a session lock while retaining company UPDATE.
// Each paid path must then wait on that company parent, never acquire a later
// lock first. This makes the lock-order test deterministic rather than relying
// on a lucky concurrent scheduler run.
func TestPostgresP82HoldAndPaidPathsLockOrder(t *testing.T) {
	for _, path := range []string{"create", "webhook", "reconcile"} {
		t.Run(path, func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
			scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			if path == "create" {
				h.provider.SetNextCreateOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
			} else {
				h.work()
				x := h.checkout(order.Order.ID)
				x, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
				if err != nil {
					t.Fatal(err)
				}
				if path == "webhook" {
					h.event(x, "lock-order-paid", "checkout.session.completed")
				}
			}
			blocker, err := h.Pool.Begin(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			var blockerPID int32
			if err = blocker.QueryRow(h.ctx, `SELECT pg_backend_pid() FROM table_sessions WHERE session_id=$1 FOR UPDATE`, order.Order.SessionID).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			holdResult := make(chan error, 1)
			go func() {
				_, err := h.Menus.SetCompanyProductEmergencyHold(h.ctx, h.TenantID, "teishoku", "fixture-admin", true, "lock-order safety", 0, "lock-order-on")
				holdResult <- err
			}()
			holdPID := waitForHoldBlocker(t, h, blockerPID)
			paidResult := make(chan error, 1)
			go func() {
				var err error
				if path == "reconcile" {
					_, err = h.service.Reconcile(h.ctx, scope, order.Order.SessionID, order.Order.ID, "fixture-manager")
				} else {
					_, err = h.service.ProcessNext(h.ctx)
				}
				paidResult <- err
			}()
			waitForHoldBlocker(t, h, holdPID)
			if err = blocker.Rollback(h.ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-holdResult; err != nil {
				t.Fatalf("hold transaction: %v", err)
			}
			if err = <-paidResult; err != nil {
				t.Fatalf("paid %s transaction: %v", path, err)
			}
			// Reconciliation intentionally discards a response if a concurrent
			// writer advanced updated_at; retrieve again after that safe conflict.
			if path == "reconcile" {
				if _, err = h.service.Reconcile(h.ctx, scope, order.Order.SessionID, order.Order.ID, "fixture-manager"); err != nil {
					t.Fatal(err)
				}
			}
			h.assertState(order.Order.ID, StateReviewRequired, true, true)
		})
	}
}

func waitForHoldBlocker(t *testing.T, h *holdFixture, blockerPID int32) int32 {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var pid int32
		err := h.Pool.QueryRow(h.ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`, blockerPID).Scan(&pid)
		if err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-deadline.C:
			t.Fatal("expected transaction did not wait on the preceding lock")
		case <-tick.C:
		}
	}
}

func TestPostgresP82ConcurrentPaidWebhookAndExpire(t *testing.T) {
	h := newHoldFixture(t)
	order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work()
	h.hold(true, 0)
	x := h.checkout(order.Order.ID)
	x.Status, x.PaymentStatus = stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid
	entered, release := make(chan struct{}), make(chan struct{})
	releaseResponse := sync.OnceFunc(func() { close(release) })
	defer releaseResponse()
	h.provider.SetNextExpireHook(func(stripeprovider.CheckoutResult) { close(entered); <-release })
	result := make(chan error, 1)
	go func() { _, err := h.service.ProcessNext(h.ctx); result <- err }()
	select {
	case <-entered:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	// The EXPIRE worker holds no database locks during this remote call.
	// Commit PAID before it returns its now-stale EXPIRED/unpaid observation.
	h.event(x, "concurrent-paid", "checkout.session.completed")
	h.work()
	h.assertState(order.Order.ID, StateReviewRequired, true, true)
	releaseResponse()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	h.assertState(order.Order.ID, StateReviewRequired, true, true)
}

func (h *holdFixture) assertRetained(order string) {
	h.t.Helper()
	var retained bool
	var pending int
	if err := h.Pool.QueryRow(h.ctx, `SELECT company_hold_expire_requested_at IS NOT NULL AND continuation_revoked_at IS NOT NULL,
		(SELECT count(*) FROM stripe_payment_work WHERE order_id=a.order_id AND work_type='EXPIRE' AND status<>'DONE')
		FROM stripe_payment_attempts a WHERE order_id=$1`, order).Scan(&retained, &pending); err != nil {
		h.t.Fatal(err)
	}
	if !retained || pending != 1 {
		h.t.Fatalf("hold obligation lost: retained=%v pending=%d", retained, pending)
	}
}

func TestPostgresP82HoldAfterCompletedUnpaidOrDecline(t *testing.T) {
	for _, scenario := range []string{"complete-unpaid-off-paid", "decline-expire", "decline-off-paid"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHoldFixture(t)
			order, proof := h.commit(h.Locator, "teishoku", ordering.Stripe)
			h.work()
			x := h.checkout(order.Order.ID)
			wantState, wantCheckout := StateFailed, "open"
			if scenario == "complete-unpaid-off-paid" {
				var err error
				x, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentUnpaid)
				if err != nil {
					t.Fatal(err)
				}
				h.event(x, "complete-before-on", "checkout.session.completed")
				wantState, wantCheckout = StateProcessing, "complete"
			} else {
				intent, err := h.provider.RetrievePaymentIntent(h.ctx, stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: x.AccountID}, x.PaymentIntentID)
				if err != nil {
					t.Fatal(err)
				}
				accepted, err := h.service.EnqueueVerifiedWebhook(h.ctx, stripeprovider.VerifiedEvent{ID: "decline-before-on", Type: "payment_intent.payment_failed", AccountID: x.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: intent.ID, PaymentIntent: &intent})
				if err != nil || !accepted {
					t.Fatalf("decline webhook: %v %v", accepted, err)
				}
			}
			h.work() // Persist completion/decline BEFORE hold ON.
			scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			status, err := h.service.ReadSafeStatus(h.ctx, scope, order.Order.SessionID, order.Order.ID)
			if err != nil || status.State != wantState || status.CheckoutStatus != wantCheckout || status.PaymentStatus != "unpaid" {
				t.Fatalf("pre-hold state: %+v %v", status, err)
			}
			h.hold(true, 0)
			h.assertRetained(order.Order.ID)
			if scenario == "decline-expire" {
				h.work()
				h.assertState(order.Order.ID, StateExpired, false, false)
				if got := h.checkout(order.Order.ID); got.Status != stripeprovider.CheckoutExpired {
					t.Fatalf("declined Checkout still payable: %+v", got)
				}
				return
			}
			if wantState == StateProcessing {
				_, before := h.provider.CheckoutOperationCounts()
				h.work()
				_, after := h.provider.CheckoutOperationCounts()
				if after != before {
					t.Fatal("attempted to expire a completed Checkout")
				}
				h.assertState(order.Order.ID, StateProcessing, false, false)
			}
			h.hold(false, 1)
			h.assertRetained(order.Order.ID)
			if ok, err := h.service.CanResume(h.ctx, order.Order.SessionID, order.Order.ID, proof); err != nil || ok {
				t.Fatalf("OFF restored continuation: %v %v", ok, err)
			}
			x, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
			if err != nil {
				t.Fatal(err)
			}
			h.event(x, "paid-after-off", "checkout.session.async_payment_succeeded")
			h.work()
			h.assertState(order.Order.ID, StateReviewRequired, true, true)
			h.ready()
			h.work()
			h.assertState(order.Order.ID, StateReviewRequired, true, true)
		})
	}
}

// Fail retrieval independently of CREATE and webhooks, without external calls.
type holdRetrieveProvider struct {
	Provider
	unavailable bool
}

func (p *holdRetrieveProvider) RetrieveCheckout(ctx context.Context, account stripeprovider.AccountContext, id string) (stripeprovider.CheckoutResult, error) {
	if p.unavailable {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrUnavailable
	}
	return p.Provider.RetrieveCheckout(ctx, account, id)
}

func TestPostgresP82RetainedBackoffBeyondCreateDeadlineAndRecovery(t *testing.T) {
	for _, mode := range []string{"retrieve-failure", "waiting-for-checkout", "unconfigured", "complete-unpaid"} {
		t.Run(mode, func(t *testing.T) {
			h := newHoldFixtureWithRetryWindow(t, 2*time.Second)
			order, proof := h.commit(h.Locator, "teishoku", ordering.Stripe)
			var x stripeprovider.CheckoutResult
			if mode == "waiting-for-checkout" {
				h.provider.SetNextCreateHook(func(result stripeprovider.CheckoutResult) { x = result })
				h.provider.LoseNextCreateResponse()
			}
			h.work()
			if mode != "waiting-for-checkout" {
				x = h.checkout(order.Order.ID)
			}
			resolved, err := h.service.resolver.Resolve(h.ctx, "fixture:hold")
			if err != nil {
				t.Fatal(err)
			}
			failing := &holdRetrieveProvider{Provider: resolved.Provider, unavailable: mode == "retrieve-failure"}
			resolved.Provider = failing
			h.service.resolver = testResolver{"fixture:hold": resolved}
			h.hold(true, 0)
			h.hold(false, 1)
			initialCode := "CHECKOUT_RETRIEVE_UNKNOWN"
			switch mode {
			case "waiting-for-checkout":
				initialCode = "WAITING_FOR_CHECKOUT"
				if _, err = h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp()+interval '1 hour' WHERE work_type='CREATE'`); err != nil {
					t.Fatal(err)
				}
			case "unconfigured":
				initialCode = "PROVIDER_UNCONFIGURED"
				h.service.resolver = testResolver{}
			case "complete-unpaid":
				initialCode = "PAYMENT_PROCESSING"
				if _, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentUnpaid); err != nil {
					t.Fatal(err)
				}
			}
			// Assert persisted scheduling, not a wall-clock sleep for each retry.
			assertRetry := func(attempt int, delay time.Duration, code string) {
				t.Helper()
				var actualAttempts int
				var actualDelay float64
				var status, actualCode string
				if err := h.Pool.QueryRow(h.ctx, `SELECT attempts,extract(epoch FROM available_at-updated_at)::float8,status,safe_error_code
					FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'`, order.Order.ID).Scan(&actualAttempts, &actualDelay, &status, &actualCode); err != nil {
					t.Fatal(err)
				}
				if actualAttempts != attempt || actualDelay < delay.Seconds()-0.1 || actualDelay > delay.Seconds()+0.1 || status != "READY" || actualCode != code {
					t.Fatalf("retry: attempts=%d delay=%v status=%s code=%s; want %d %v %s", actualAttempts, actualDelay, status, actualCode, attempt, delay, code)
				}
				h.assertRetained(order.Order.ID)
				if worked, err := h.service.ProcessNext(h.ctx); err != nil || worked {
					t.Fatalf("retry ran before available_at: %v %v", worked, err)
				}
			}
			h.work()
			assertRetry(1, 5*time.Second, initialCode)
			var deadline time.Time
			if err = h.Pool.QueryRow(h.ctx, `SELECT retry_not_after FROM stripe_payment_attempts WHERE order_id=$1`, order.Order.ID).Scan(&deadline); err != nil {
				t.Fatal(err)
			}
			if delay := time.Until(deadline) + 50*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			if mode == "waiting-for-checkout" {
				// CREATE stops at its real immutable deadline; EXPIRE must survive.
				h.ready()
				h.work()
				var status, code string
				if err = h.Pool.QueryRow(h.ctx, `SELECT status,safe_error_code FROM stripe_payment_work WHERE order_id=$1 AND work_type='CREATE'`, order.Order.ID).Scan(&status, &code); err != nil || status != "DONE" || code != "RETRY_WINDOW_ELAPSED" {
					t.Fatalf("CREATE cutoff: %s %s %v", status, code, err)
				}
			}
			for i, seconds := range []int{10, 20, 40, 80, 160, 300, 300} {
				h.ready()
				h.work()
				assertRetry(i+2, time.Duration(seconds)*time.Second, "HOLD_RECONCILIATION_NEEDED")
			}
			scope := operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}
			status, err := h.service.ReadSafeStatus(h.ctx, scope, order.Order.SessionID, order.Order.ID)
			if err != nil || status.SafeErrorCode != "HOLD_RECONCILIATION_NEEDED" {
				t.Fatalf("staff cannot see durable reconciliation need: %+v %v", status, err)
			}
			if ok, err := h.service.CanResume(h.ctx, order.Order.SessionID, order.Order.ID, proof); err != nil || ok {
				t.Fatalf("backoff restored continuation: %v %v", ok, err)
			}
			failing.unavailable = false
			h.service.resolver = testResolver{"fixture:hold": resolved}
			want, paid := StateExpired, false
			if mode == "waiting-for-checkout" || mode == "complete-unpaid" {
				// A delayed authentic webhook can supply the missing binding after
				// CREATE's deadline; the retained worker then retrieves payment.
				if mode == "waiting-for-checkout" {
					x, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentUnpaid)
					if err != nil {
						t.Fatal(err)
					}
					h.event(x, "late-binding", "checkout.session.completed")
					h.work()
				}
				if _, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid); err != nil {
					t.Fatal(err)
				}
				want, paid = StateReviewRequired, true
			}
			h.ready()
			h.work()
			if paid {
				h.work()
			}
			h.assertState(order.Order.ID, want, paid, paid)
			status, err = h.service.ReadSafeStatus(h.ctx, scope, order.Order.SessionID, order.Order.ID)
			if err != nil || status.SafeErrorCode != "" {
				t.Fatalf("recovery left active error disposition: %+v %v", status, err)
			}
			var done, retained bool
			if err = h.Pool.QueryRow(h.ctx, `SELECT w.status='DONE',a.company_hold_expire_requested_at IS NOT NULL AND a.continuation_revoked_at IS NOT NULL
				FROM stripe_payment_attempts a JOIN stripe_payment_work w USING(order_id) WHERE a.order_id=$1 AND w.work_type='EXPIRE'`, order.Order.ID).Scan(&done, &retained); err != nil || !done || !retained {
				t.Fatalf("recovery lost retained decision: done=%v retained=%v err=%v", done, retained, err)
			}
			if creates, sessions := h.provider.Counts(); creates != 1 || sessions != 1 {
				t.Fatalf("recovery created another Checkout: %d/%d", creates, sessions)
			}
		})
	}
}

func TestPostgresP82RetainedExpiryDoesNotStarveCreateOrWebhook(t *testing.T) {
	h := newHoldFixture(t)
	held, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work()
	h.hold(true, 0) // Older EXPIRE is already due when unrelated work arrives.
	other, _ := h.commit(h.Locator, "tea", ordering.Stripe)
	h.work()
	h.assertState(other.Order.ID, StateOpen, false, false)
	x := h.checkout(other.Order.ID)
	x, err := h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	h.event(x, "unrelated-payment", "checkout.session.completed")
	h.work()
	h.assertState(other.Order.ID, StatePaid, true, false)
	var attempts int
	if err = h.Pool.QueryRow(h.ctx, `SELECT attempts FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'`, held.Order.ID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("EXPIRE displaced CREATE/webhook: attempts=%d err=%v", attempts, err)
	}
	h.work()
	h.assertState(held.Order.ID, StateExpired, false, false)
}

func TestPostgresP82UnboundFailedBeforeHoldOffAndLateCreatePaid(t *testing.T) {
	h := newHoldFixture(t)
	order, proof := h.commit(h.Locator, "teishoku", ordering.Stripe)
	var x stripeprovider.CheckoutResult
	h.provider.SetNextCreateHook(func(result stripeprovider.CheckoutResult) { x = result })
	h.provider.LoseNextCreateResponse()
	h.work() // Provider created the Checkout, but CREATE has not bound it locally.
	if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp()+interval '1 hour' WHERE work_type='CREATE'`); err != nil {
		t.Fatal(err)
	}
	intent, err := h.provider.RetrievePaymentIntent(h.ctx, stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: x.AccountID}, x.PaymentIntentID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := h.service.EnqueueVerifiedWebhook(h.ctx, stripeprovider.VerifiedEvent{ID: "unbound-decline", Type: "payment_intent.payment_failed", AccountID: x.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: intent.ID, PaymentIntent: &intent})
	if err != nil || !accepted {
		t.Fatalf("decline webhook: %v %v", accepted, err)
	}
	h.work()
	h.assertState(order.Order.ID, StateFailed, false, false)
	var unbound bool
	if err = h.Pool.QueryRow(h.ctx, `SELECT checkout_session_id IS NULL AND payment_intent_id=$2 FROM stripe_payment_attempts WHERE order_id=$1`, order.Order.ID, intent.ID).Scan(&unbound); err != nil || !unbound {
		t.Fatalf("expected only PaymentIntent binding: unbound=%v err=%v", unbound, err)
	}
	h.hold(true, 0)
	h.assertRetained(order.Order.ID)
	h.hold(false, 1)
	h.assertRetained(order.Order.ID)
	h.ready()
	h.work() // Same-key CREATE recovery persists the late Checkout after OFF.
	if got := h.checkout(order.Order.ID); got.SessionID != x.SessionID {
		t.Fatalf("late CREATE did not recover original Checkout: %+v", got)
	}
	if ok, err := h.service.CanResume(h.ctx, order.Order.SessionID, order.Order.ID, proof); err != nil || ok {
		t.Fatalf("OFF restored continuation: %v %v", ok, err)
	}
	x, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	h.event(x, "unbound-decline-paid-after-off", "checkout.session.async_payment_succeeded")
	h.work()
	h.assertState(order.Order.ID, StateReviewRequired, true, true) // Includes no KDS/outbox release.
	if creates, sessions := h.provider.Counts(); creates != 2 || sessions != 1 {
		t.Fatalf("same-key recovery: creates=%d sessions=%d", creates, sessions)
	}
}

func TestPostgresP82UnboundFailedLateWebhookBeyondCreateDeadline(t *testing.T) {
	for _, outcome := range []string{"complete-unpaid-then-paid", "still-open"} {
		t.Run(outcome, func(t *testing.T) {
			h := newHoldFixtureWithRetryWindow(t, 2*time.Second)
			order, proof := h.commit(h.Locator, "teishoku", ordering.Stripe)
			var x stripeprovider.CheckoutResult
			h.provider.SetNextCreateHook(func(result stripeprovider.CheckoutResult) { x = result })
			h.provider.LoseNextCreateResponse()
			h.work()
			if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp()+interval '1 hour' WHERE work_type='CREATE'`); err != nil {
				t.Fatal(err)
			}
			intent, err := h.provider.RetrievePaymentIntent(h.ctx, stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: x.AccountID}, x.PaymentIntentID)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := h.service.EnqueueVerifiedWebhook(h.ctx, stripeprovider.VerifiedEvent{ID: "decline-before-binding", Type: "payment_intent.payment_failed", AccountID: x.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: intent.ID, PaymentIntent: &intent})
			if err != nil || !accepted {
				t.Fatalf("decline webhook: %v %v", accepted, err)
			}
			h.work()
			h.assertState(order.Order.ID, StateFailed, false, false)
			var unbound bool
			var deadline time.Time
			if err = h.Pool.QueryRow(h.ctx, `SELECT checkout_session_id IS NULL AND payment_intent_id=$2,retry_not_after FROM stripe_payment_attempts WHERE order_id=$1`, order.Order.ID, intent.ID).Scan(&unbound, &deadline); err != nil || !unbound {
				t.Fatalf("failed before binding: unbound=%v err=%v", unbound, err)
			}
			h.hold(true, 0)
			h.hold(false, 1)
			h.assertRetained(order.Order.ID)
			if delay := time.Until(deadline) + 50*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			h.ready()
			h.work() // CREATE exhausts its immutable deadline without another call.
			var status, code string
			if err = h.Pool.QueryRow(h.ctx, `SELECT status,safe_error_code FROM stripe_payment_work WHERE order_id=$1 AND work_type='CREATE'`, order.Order.ID).Scan(&status, &code); err != nil || status != "DONE" || code != "RETRY_WINDOW_ELAPSED" {
				t.Fatalf("CREATE cutoff: %s %s %v", status, code, err)
			}
			h.work() // Retained EXPIRE still waits for the missing Checkout ID.
			if err = h.Pool.QueryRow(h.ctx, `SELECT status,safe_error_code FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'`, order.Order.ID).Scan(&status, &code); err != nil || status != "READY" || code != "HOLD_RECONCILIATION_NEEDED" {
				t.Fatalf("retained expiry: %s %s %v", status, code, err)
			}
			if outcome == "complete-unpaid-then-paid" {
				x, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentUnpaid)
				if err != nil {
					t.Fatal(err)
				}
			}
			h.event(x, "late-unpaid-binding", "checkout.session.completed")
			h.work()
			h.assertState(order.Order.ID, StateFailed, false, false) // Binding must not resurrect FAILED.
			var bound, processed bool
			if err = h.Pool.QueryRow(h.ctx, `SELECT coalesce(checkout_session_id=$2 AND payment_intent_id=$3 AND checkout_status=$4,false),
				(SELECT processed_at IS NOT NULL FROM stripe_webhook_events WHERE stripe_event_id='late-unpaid-binding')
				FROM stripe_payment_attempts WHERE order_id=$1`, order.Order.ID, x.SessionID, intent.ID, string(x.Status)).Scan(&bound, &processed); err != nil || !bound || !processed {
				t.Fatalf("late webhook binding: bound=%v processed=%v err=%v", bound, processed, err)
			}
			r0, e0 := h.provider.CheckoutOperationCounts()
			h.ready()
			h.work()
			r1, e1 := h.provider.CheckoutOperationCounts()
			if outcome == "complete-unpaid-then-paid" {
				if r1-r0 != 1 || e1-e0 != 0 {
					t.Fatalf("complete/unpaid must retrieve without expire: retrieve=%d expire=%d", r1-r0, e1-e0)
				}
				h.assertRetained(order.Order.ID)
				if _, err = h.provider.SetOutcome(x.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid); err != nil {
					t.Fatal(err)
				}
				h.ready()
				h.work() // Retrieve the eventual payment; no new external webhook.
				h.work() // Apply the verified paid reconciliation through existing rules.
				h.assertState(order.Order.ID, StateReviewRequired, true, true)
			} else {
				if r1-r0 != 1 || e1-e0 != 1 {
					t.Fatalf("open Checkout must retrieve then expire: retrieve=%d expire=%d", r1-r0, e1-e0)
				}
				h.assertState(order.Order.ID, StateExpired, false, false)
			}
			var done, retained bool
			if err = h.Pool.QueryRow(h.ctx, `SELECT w.status='DONE',a.company_hold_expire_requested_at IS NOT NULL AND a.continuation_revoked_at IS NOT NULL
				FROM stripe_payment_attempts a JOIN stripe_payment_work w USING(order_id) WHERE a.order_id=$1 AND w.work_type='EXPIRE'`, order.Order.ID).Scan(&done, &retained); err != nil || !done || !retained {
				t.Fatalf("recovery: done=%v retained=%v err=%v", done, retained, err)
			}
			if ok, err := h.service.CanResume(h.ctx, order.Order.SessionID, order.Order.ID, proof); err != nil || ok {
				t.Fatalf("late binding restored continuation: %v %v", ok, err)
			}
			if creates, sessions := h.provider.Counts(); creates != 1 || sessions != 1 {
				t.Fatalf("recovery created another Checkout: %d/%d", creates, sessions)
			}
		})
	}
}

func TestPostgresP82ExpiryAgesThroughContinuouslyReplenishedQueue(t *testing.T) {
	h := newHoldFixture(t)
	held, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
	h.work()
	other, _ := h.commit(h.Locator, "tea", ordering.Stripe)
	h.work()
	x := h.checkout(other.Order.ID)
	h.hold(true, 0)
	// Advance eligibility age without sleeping. Before every lease, replenish
	// BOTH competing queues; EXPIRE must progress while both still have work.
	for i, age := range []time.Duration{0, 10 * time.Second, 20 * time.Second, 31 * time.Second} {
		h.commit(h.Locator, "tea", ordering.Stripe)
		h.event(x, fmt.Sprintf("replenished-%d", i), "checkout.session.completed")
		if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp()-$2::interval WHERE order_id=$1 AND work_type='EXPIRE'`, held.Order.ID, age.String()); err != nil {
			t.Fatal(err)
		}
		var creates, webhooks int
		if err := h.Pool.QueryRow(h.ctx, `SELECT count(*) FILTER(WHERE work_type='CREATE'),count(*) FILTER(WHERE work_type='WEBHOOK') FROM stripe_payment_work WHERE status='READY' AND available_at<=clock_timestamp()`).Scan(&creates, &webhooks); err != nil || creates == 0 || webhooks == 0 {
			t.Fatalf("missing competing backlog: create=%d webhook=%d err=%v", creates, webhooks, err)
		}
		h.work()
		var attempts int
		if err := h.Pool.QueryRow(h.ctx, `SELECT attempts FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'`, held.Order.ID).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if age < 30*time.Second && attempts != 0 {
			t.Fatalf("fresh expiry displaced CREATE/webhook: attempts=%d age=%s", attempts, age)
		}
		if age > 30*time.Second && attempts != 1 {
			t.Fatalf("aged expiry starved behind replenished queue: attempts=%d age=%s", attempts, age)
		}
	}
	h.assertState(held.Order.ID, StateExpired, false, false)
}
