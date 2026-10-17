package payments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

type testResolver map[string]ResolvedProvider

func (r testResolver) Resolve(_ context.Context, ref string) (ResolvedProvider, error) {
	v, ok := r[ref]
	if !ok {
		return ResolvedProvider{}, ErrUnavailable
	}
	return v, nil
}

func TestPostgresStripeCommitResumeWebhookAndReviewGate(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(context.Background())
	provider := testpayments.NewProvider()
	account := stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_fixtureStoreA"}
	const credential = "fixture:payments:v1"
	resolver := testResolver{credential: {Provider: provider, Account: account, ProfileFingerprint: sha256.Sum256([]byte("test-profile"))}}
	service, err := New(f.Pool, f.Operations, resolver, Config{CreateRetryWindow: time.Hour, ContinuationTTL: 20 * time.Minute, WorkLeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetMerchantConfig(ctx, operations.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}, 0, "fixture-admin", MerchantConfigInput{Enabled: true, Mode: account.Mode, StripeAccountID: account.StripeAccountID, CredentialRef: credential, WebhookEndpointRef: "fixture:webhook", APIVersion: stripeprovider.APIVersion}); err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	f.Sessions, err = tablesession.New(f.Pool, tablesession.Options{SessionTTL: 2 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 100, PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: pepper}, service)
	if err != nil {
		t.Fatal(err)
	}
	f.Orders, err = ordering.NewWithOrderPolicy(f.Pool, f.Sessions, f.Menus, service)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: "stripe-line", Kind: ordering.AddLine, ProductKey: "teishoku", Quantity: 1, Options: []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	var proof [32]byte
	if _, err = rand.Read(proof[:]); err != nil {
		t.Fatal(err)
	}
	proofExpiry := time.Now().UTC().Add(20 * time.Minute)
	committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "stripe-order", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe, CheckoutProofHash: proof, CheckoutProofExpiresAt: proofExpiry})
	if err != nil {
		t.Fatal(err)
	}
	if committed.Order.ID == "" {
		t.Fatal("commit omitted order id")
	}
	provider.LoseNextCreateResponse()
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("create worker worked=%v err=%v", worked, err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE order_id=$1 AND work_type='CREATE' AND status='READY'`, committed.Order.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("create recovery worker worked=%v err=%v", worked, err)
	}
	if requests, sessions := provider.Counts(); requests != 2 || sessions != 1 {
		t.Fatalf("lost create response must retry same idempotency key: requests=%d sessions=%d", requests, sessions)
	}
	status, err := service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, committed.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateOpen || status.CheckoutStatus != "open" || status.PaymentStatus != "unpaid" {
		t.Fatalf("unexpected create status: %+v", status)
	}
	if ok, err := service.CanResume(ctx, grant.Session.ID, committed.Order.ID, proof); err != nil || !ok {
		t.Fatalf("original proof should resume: ok=%v err=%v", ok, err)
	}
	var other [32]byte
	other[0] = 1
	if ok, err := service.CanResume(ctx, grant.Session.ID, committed.Order.ID, other); err != nil || ok {
		t.Fatalf("unbound proof was accepted: ok=%v err=%v", ok, err)
	}

	// A lost create response followed by a retry may return an already-paid
	// Checkout. The payment state and review gates must be applied atomically.
	paidRetry, _, err := commitStripeFixtureOrder(ctx, f.Orders, grant.Session.ID, grant.Token, "stripe-line-paid-retry", "stripe-order-paid-retry")
	if err != nil {
		t.Fatal(err)
	}
	provider.SetNextCreateOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	provider.LoseNextCreateResponse()
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("paid create initial response-loss worker worked=%v err=%v", worked, err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE order_id=$1 AND work_type='CREATE' AND status='READY'`, paidRetry.Order.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("paid create retry worker worked=%v err=%v", worked, err)
	}
	status, err = service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, paidRetry.Order.ID)
	if err != nil || status.State != StatePaid || status.PaymentStatus != "paid" {
		t.Fatalf("paid response on idempotent retry not applied: status=%+v err=%v", status, err)
	}
	if requests, sessions := provider.Counts(); requests != 4 || sessions != 2 {
		t.Fatalf("paid retry must not create another Checkout: requests=%d sessions=%d", requests, sessions)
	}

	// Retry-window expiry must never issue a fresh create with a new outcome.
	cutoffService, err := New(f.Pool, f.Operations, resolver, Config{CreateRetryWindow: time.Nanosecond, ContinuationTTL: 20 * time.Minute, WorkLeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cutoffOrders, err := ordering.NewWithOrderPolicy(f.Pool, f.Sessions, f.Menus, cutoffService)
	if err != nil {
		t.Fatal(err)
	}
	cutoffOrder, _, err := commitStripeFixtureOrder(ctx, cutoffOrders, grant.Session.ID, grant.Token, "stripe-line-cutoff", "stripe-order-cutoff")
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := cutoffService.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("retry-cutoff worker worked=%v err=%v", worked, err)
	}
	if requests, sessions := provider.Counts(); requests != 4 || sessions != 2 {
		t.Fatalf("retry cutoff made a new provider create: requests=%d sessions=%d", requests, sessions)
	}
	cutoffStatus, err := service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, cutoffOrder.Order.ID)
	if err != nil || cutoffStatus.State != StateUnknown {
		t.Fatalf("retry cutoff should leave attempt unknown: status=%+v err=%v", cutoffStatus, err)
	}

	concurrentOrder, _, err := commitStripeFixtureOrder(ctx, f.Orders, grant.Session.ID, grant.Token, "stripe-line-concurrent", "stripe-order-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	startWorkers := make(chan struct{})
	type workerResult struct {
		worked bool
		err    error
	}
	workerResults := make(chan workerResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-startWorkers
			worked, workerErr := service.ProcessNext(ctx)
			workerResults <- workerResult{worked: worked, err: workerErr}
		}()
	}
	close(startWorkers)
	workedCount := 0
	for i := 0; i < 2; i++ {
		result := <-workerResults
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.worked {
			workedCount++
		}
	}
	if workedCount != 1 {
		t.Fatalf("concurrent workers should claim the create work once, worked=%d", workedCount)
	}
	if requests, sessions := provider.Counts(); requests != 5 || sessions != 3 {
		t.Fatalf("concurrent worker duplicate create: requests=%d sessions=%d", requests, sessions)
	}
	concurrentStatus, err := service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, concurrentOrder.Order.ID)
	if err != nil || concurrentStatus.State != StateOpen {
		t.Fatalf("concurrent create result should be open: status=%+v err=%v", concurrentStatus, err)
	}

	// Place a hold after Stripe has created an open Checkout but before the
	// worker persists its response. This queues the best-effort expiry seam.
	var holdHookErr error
	provider.SetNextCreateHook(func(result stripeprovider.CheckoutResult) {
		_, holdHookErr = f.Pool.Exec(ctx, `UPDATE operation_order_state SET operational_hold=true,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1`, result.Metadata["order_id"])
	})
	heldRaceOrder, _, err := commitStripeFixtureOrder(ctx, f.Orders, grant.Session.ID, grant.Token, "stripe-line-hold-race", "stripe-order-hold-race")
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("held create worker worked=%v err=%v", worked, err)
	}
	if holdHookErr != nil {
		t.Fatalf("hold race fixture: %v", holdHookErr)
	}
	provider.SetNextExpireOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid, true)
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("expiry worker should retrieve completed paid Checkout: worked=%v err=%v", worked, err)
	}
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("expiry paid reconciliation worker worked=%v err=%v", worked, err)
	}
	heldRaceStatus, err := service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, heldRaceOrder.Order.ID)
	if err != nil || heldRaceStatus.State != StateReviewRequired || !heldRaceStatus.PaymentReviewRequired {
		t.Fatalf("paid Checkout racing expiry under hold must require review: status=%+v err=%v", heldRaceStatus, err)
	}

	checkoutID, err := testCheckoutID(ctx, f.Pool, committed.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	piID, err := testPaymentIntentID(ctx, f.Pool, committed.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	var providerIntent stripeprovider.PaymentIntentResult
	providerIntent, err = provider.RetrievePaymentIntent(ctx, account, piID)
	if err != nil {
		t.Fatal(err)
	}
	failure := stripeprovider.VerifiedEvent{ID: "evt_fixture_failed_1", Type: "payment_intent.payment_failed", AccountID: account.StripeAccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: piID, PaymentIntent: &providerIntent}
	if accepted, err := service.EnqueueVerifiedWebhook(ctx, failure); err != nil || !accepted {
		t.Fatalf("enqueue failed payment accepted=%v err=%v", accepted, err)
	}
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("failed-payment worker worked=%v err=%v", worked, err)
	}
	status, err = service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, committed.Order.ID)
	if err != nil || status.State != StateFailed {
		t.Fatalf("payment failure must be distinct: status=%+v err=%v", status, err)
	}
	expired, err := provider.RetrieveCheckout(ctx, account, checkoutID)
	if err != nil {
		t.Fatal(err)
	}
	// A delayed expiry event may arrive after a payment failure even though a
	// fresh retrieval shows this same Checkout is still open for retry.
	expired.Status = stripeprovider.CheckoutExpired
	expiryEvent := stripeprovider.VerifiedEvent{ID: "evt_fixture_expired_1", Type: "checkout.session.expired", AccountID: account.StripeAccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: checkoutID, Checkout: &expired}
	if accepted, err := service.EnqueueVerifiedWebhook(ctx, expiryEvent); err != nil || !accepted {
		t.Fatalf("enqueue expiry accepted=%v err=%v", accepted, err)
	}
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("expiry worker worked=%v err=%v", worked, err)
	}
	status, err = service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, committed.Order.ID)
	if err != nil || status.State != StateFailed {
		t.Fatalf("out-of-order expiry downgraded failure: status=%+v err=%v", status, err)
	}
	status, err = service.Reconcile(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, committed.Order.ID, "fixture-cashier")
	if err != nil || status.State != StateOpen || status.CheckoutStatus != "open" {
		t.Fatalf("fresh retrieval should restore same open Checkout after decline: status=%+v err=%v", status, err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE operation_order_state SET operational_hold=true,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1`, committed.Order.ID); err != nil {
		t.Fatal(err)
	}
	result, err := provider.SetOutcome(checkoutID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	event := stripeprovider.VerifiedEvent{ID: "evt_fixture_paid_1", Type: "checkout.session.completed", AccountID: account.StripeAccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: result.SessionID, Checkout: &result}
	wrongAccount := result
	wrongAccount.AccountID = "acct_fixtureOther"
	wrongEvent := event
	wrongEvent.ID = "evt_fixture_wrong_account"
	wrongEvent.AccountID = wrongAccount.AccountID
	wrongEvent.Checkout = &wrongAccount
	if accepted, err := service.EnqueueVerifiedWebhook(ctx, wrongEvent); err == nil || accepted {
		t.Fatalf("wrong-account payment event accepted=%v err=%v", accepted, err)
	}
	if accepted, err := service.EnqueueVerifiedWebhook(ctx, event); err != nil || !accepted {
		t.Fatalf("enqueue paid event accepted=%v err=%v", accepted, err)
	}
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("webhook worker worked=%v err=%v", worked, err)
	}
	status, err = service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, committed.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateReviewRequired || status.PaymentStatus != "paid" || !status.PaymentReviewRequired {
		t.Fatalf("late paid payment under hold must require review: %+v", status)
	}
	if accepted, err := service.EnqueueVerifiedWebhook(ctx, event); err != nil || accepted {
		t.Fatalf("duplicate webhook should dedupe accepted=%v err=%v", accepted, err)
	}
	guest, err := f.Operations.ReadGuestOperations(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	var guestOrder *operations.GuestOrderState
	for i := range guest.Orders {
		if guest.Orders[i].ID == committed.Order.ID {
			guestOrder = &guest.Orders[i]
			break
		}
	}
	if guestOrder == nil || guestOrder.PaymentStatus != operations.Paid {
		t.Fatalf("paid status not applied to operation state: %+v", guest)
	}
	if guestOrder.FulfillmentStatus != operations.Waiting {
		t.Fatalf("direct scan should await staff acceptance: %+v", guestOrder)
	}
	// The accepted session snapshot is immutable in runtime. This isolated test
	// fixture backdates it only to exercise scoped staff reads after expiry.
	if _, err = f.Pool.Exec(ctx, `ALTER TABLE table_sessions DISABLE TRIGGER table_session_snapshot_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()-interval '1 minute' WHERE session_id=$1`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `ALTER TABLE table_sessions ENABLE TRIGGER table_session_snapshot_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE stores SET operating=false WHERE tenant_id='test-tenant' AND store_id='test-store'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE dining_tables SET enabled=false WHERE tenant_id='test-tenant' AND store_id='test-store' AND table_id='table-01'`); err != nil {
		t.Fatal(err)
	}
	status, err = service.ReadSafeStatus(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, committed.Order.ID)
	if err != nil || status.State != StateReviewRequired {
		t.Fatalf("staff status read must support expired sessions: status=%+v err=%v", status, err)
	}
	status, err = service.Reconcile(ctx, operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}, grant.Session.ID, committed.Order.ID, "fixture-cashier")
	if err != nil || status.State != StateReviewRequired {
		t.Fatalf("staff reconcile should preserve reviewed paid state on expired session: status=%+v err=%v", status, err)
	}
	for _, mutation := range []string{
		`UPDATE stripe_payment_attempts SET attempt_id='tampered-attempt' WHERE order_id=$1`,
		`UPDATE stripe_payment_attempts SET checkout_session_id='cs_tampered' WHERE order_id=$1`,
		`UPDATE stripe_payment_attempts SET payment_intent_id=NULL WHERE order_id=$1`,
		`UPDATE stripe_payment_attempts SET state='UNKNOWN',provider_payment_status='unpaid',payment_review_required=false WHERE order_id=$1`,
		`UPDATE stripe_payment_attempts SET provider_payment_status=NULL WHERE order_id=$1`,
	} {
		if _, err = f.Pool.Exec(ctx, mutation, committed.Order.ID); err == nil {
			t.Fatalf("database allowed immutable/terminal payment mutation: %s", mutation)
		}
	}
}

func testCheckoutID(ctx context.Context, pool *pgxpool.Pool, orderID string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&id)
	return id, err
}

func testPaymentIntentID(ctx context.Context, pool *pgxpool.Pool, orderID string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT payment_intent_id FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&id)
	return id, err
}

func commitStripeFixtureOrder(ctx context.Context, orders *ordering.Service, sessionID, token, lineKey, orderKey string) (ordering.CommitResult, [32]byte, error) {
	cart, err := orders.ReadCart(ctx, sessionID, token)
	if err != nil {
		return ordering.CommitResult{}, [32]byte{}, err
	}
	cart, err = orders.Mutate(ctx, ordering.Mutation{SessionID: sessionID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: lineKey, Kind: ordering.AddLine, ProductKey: "teishoku", Quantity: 1, Options: []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}}})
	if err != nil {
		return ordering.CommitResult{}, [32]byte{}, err
	}
	preview, err := orders.Preview(ctx, sessionID, token, cart.Generation, cart.Version)
	if err != nil {
		return ordering.CommitResult{}, [32]byte{}, err
	}
	var proof [32]byte
	if _, err = rand.Read(proof[:]); err != nil {
		return ordering.CommitResult{}, [32]byte{}, err
	}
	result, err := orders.Commit(ctx, ordering.CommitRequest{SessionID: sessionID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: orderKey, PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe, CheckoutProofHash: proof, CheckoutProofExpiresAt: time.Now().UTC().Add(20 * time.Minute)})
	return result, proof, err
}
