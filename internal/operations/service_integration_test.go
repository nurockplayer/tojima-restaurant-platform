package operations_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func TestPostgresCashSnapshotSettlementConcurrencyAndCloseGate(t *testing.T) {
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
	scope := operations.TableScope{TenantID: grant.Session.Scope.TenantID, StoreID: grant.Session.Scope.StoreID, TableID: grant.Session.Scope.TableID}
	first := commitCashOrder(t, ctx, f, grant, 1, "cash-paylater")
	states, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(states.Orders) != 1 || states.Orders[0].PaymentStatus != operations.Unpaid || states.Orders[0].CashMode != operations.PayLater || states.Orders[0].FulfillmentStatus != operations.Waiting {
		t.Fatalf("bad PAY_LATER snapshot: %+v", states.Orders)
	}
	accepted, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: first.ID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: "accept-first"})
	if err != nil || accepted.FulfillmentStatus != operations.Accepted {
		t.Fatalf("PAY_LATER acceptance did not release: state=%+v err=%v", accepted, err)
	}
	var recordedVersion int64
	if err = f.Pool.QueryRow(ctx, `SELECT result_version FROM operation_action_idempotency WHERE session_id=$1 AND order_id=$2 AND action='ACCEPT' AND idempotency_key='accept-first'`, grant.Session.ID, first.ID).Scan(&recordedVersion); err != nil || recordedVersion != accepted.Version {
		t.Fatalf("idempotency result version %d does not match final release state %d: %v", recordedVersion, accepted.Version, err)
	}
	cards, err := f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 1 {
		t.Fatalf("expected one released card: %+v %v", cards, err)
	}
	if _, err = f.Operations.SetCashSettings(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID}, "manager", true, operations.Prepaid, 1); err != nil {
		t.Fatal(err)
	}
	second := commitCashOrder(t, ctx, f, grant, 2, "cash-prepaid")
	states, err = f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var secondState operations.OrderState
	for _, o := range states.Orders {
		if o.ID == second.ID {
			secondState = o
		}
	}
	if secondState.CashMode != operations.Prepaid || secondState.PaymentStatus != operations.Unpaid {
		t.Fatalf("new order did not snapshot PREPAID: %+v", secondState)
	}
	accepted, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: second.ID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: "accept-second"})
	if err != nil || accepted.FulfillmentStatus != operations.Waiting {
		t.Fatalf("unpaid PREPAID order released before settlement: %+v %v", accepted, err)
	}
	cards, err = f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 1 {
		t.Fatalf("prepaid card escaped gate: %+v %v", cards, err)
	}
	setOperationalHold(t, ctx, f, grant.Session.ID, first.ID, true)
	if _, err = f.Operations.PreviewCash(ctx, scope, grant.Session.ID); !errors.Is(err, operations.ErrBlocked) {
		t.Fatalf("held PAY_LATER order was omitted from the cash set: %v", err)
	}
	setOperationalHold(t, ctx, f, grant.Session.ID, first.ID, false)
	preview, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.OrderIDs) != 2 || preview.DueJPY != 660 {
		t.Fatalf("settlement set included wrong orders: %+v", preview)
	}
	results := make(chan struct {
		receipt operations.Receipt
		err     error
	}, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, key := range []string{"settle-a", "settle-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			r, e := f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: preview.Fingerprint, TenderedJPY: 1000, RegisterSessionRef: "shift-1", IdempotencyKey: key})
			results <- struct {
				receipt operations.Receipt
				err     error
			}{r, e}
		}(key)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner operations.Receipt
	successes, reconfirms := 0, 0
	for r := range results {
		if r.err == nil {
			successes++
			winner = r.receipt
		} else if errors.Is(r.err, operations.ErrReconfirm) {
			reconfirms++
		} else {
			t.Fatalf("unexpected concurrent settlement error: %v", r.err)
		}
	}
	if successes != 1 || reconfirms != 1 {
		t.Fatalf("settlement race successes=%d reconfirm=%d", successes, reconfirms)
	}
	if winner.DueJPY != 660 || winner.ChangeJPY != 340 || len(winner.OrderIDs) != 2 {
		t.Fatalf("bad receipt %+v", winner)
	}
	states, err = f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range states.Orders {
		if o.ID == second.ID && (o.PaymentStatus != operations.Paid || o.FulfillmentStatus != operations.Accepted) {
			t.Fatalf("settled PREPAID state %+v", o)
		}
	}
	cards, err = f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 2 {
		t.Fatalf("settled prepaid order not released once: %+v %v", cards, err)
	}
	// Same idempotency key returns its original receipt even after the current
	// unpaid set would otherwise be different.
	var usedKey string
	if winner.ID == "" {
		t.Fatal("missing winning receipt")
	}
	// Determine which contender created the immutable receipt and replay it.
	for _, key := range []string{"settle-a", "settle-b"} {
		r, e := f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: preview.Fingerprint, TenderedJPY: 1000, RegisterSessionRef: "shift-1", IdempotencyKey: key})
		if e == nil && r.ID == winner.ID {
			usedKey = key
			break
		}
	}
	if usedKey == "" {
		t.Fatal("winning settlement key did not replay")
	}
	setOperationalHold(t, ctx, f, grant.Session.ID, first.ID, true)
	cards, err = f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 1 || cards[0].ID != second.ID {
		t.Fatalf("held card remained dispatchable: %+v %v", cards, err)
	}
	if _, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: first.ID, Actor: "kitchen", ExpectedVersion: 5, IdempotencyKey: "held-advance", Target: operations.Preparing}); !errors.Is(err, operations.ErrBlocked) {
		t.Fatalf("held order advanced: %v", err)
	}
	if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "cashier"); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("close passed with unserved orders: %v", err)
	}
}

func TestPostgresStripeGateAndExactCashInvoice(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.Prepaid})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: grant.Session.Scope.TenantID, StoreID: grant.Session.Scope.StoreID, TableID: grant.Session.Scope.TableID}
	store := catalog.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID}
	if _, err = f.Menus.SetStorePriceOverride(ctx, store, "manager", "teishoku", 900, 0); err != nil {
		t.Fatal(err)
	}
	stripe := commitTestOrder(t, ctx, f.Orders, grant, 1, "stripe-900", ordering.Stripe, "teishoku")
	initialState, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var projectedStripe *operations.OrderState
	for i := range initialState.Orders {
		if initialState.Orders[i].ID == stripe.ID {
			projectedStripe = &initialState.Orders[i]
			break
		}
	}
	if projectedStripe == nil || len(projectedStripe.Lines) != 1 || projectedStripe.Lines[0].ProductName != "東島の定食" || projectedStripe.Lines[0].UnitGrossJPY != 900 || len(projectedStripe.Lines[0].Options) != 1 || projectedStripe.Lines[0].Options[0].GroupName != "" || projectedStripe.Lines[0].Options[0].OptionName != "普通" || projectedStripe.Lines[0].Options[0].PriceDeltaMinor != 0 {
		t.Fatalf("order projection must preserve historical option name/price without inventing a group label: %+v", projectedStripe)
	}
	state, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: stripe.ID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: "accept-stripe"})
	if err != nil || state.FulfillmentStatus != operations.Waiting {
		t.Fatalf("pending Stripe was released: %+v %v", state, err)
	}
	cards, err := f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 0 {
		t.Fatalf("Stripe pending KDS leakage: %+v %v", cards, err)
	}
	setStorePrice(t, ctx, f, store, "tea", 600, 0)
	cash600 := commitTestOrder(t, ctx, f.Orders, grant, 2, "cash-600", ordering.Cash, "tea")
	setStorePrice(t, ctx, f, store, "tea", 500, 1)
	cash500 := commitTestOrder(t, ctx, f.Orders, grant, 3, "cash-500", ordering.Cash, "tea")
	for _, o := range []ordering.Order{cash600, cash500} {
		if _, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: o.ID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: "accept-" + o.ID}); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.OrderIDs) != 2 || preview.DueJPY != 1100 {
		t.Fatalf("cash preview must exclude 900 Stripe and total 1100: %+v", preview)
	}
	markStripePaidFixture(t, ctx, f, grant.Session.ID, stripe.ID)
	cards, err = f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 1 || cards[0].ID != stripe.ID {
		t.Fatalf("test paid Stripe did not release alone: %+v %v", cards, err)
	}
	// Two cashiers may race the same business idempotency request. The winning
	// immutable receipt must be returned to both; only one allocation per order.
	results := make(chan operations.Receipt, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, actor := range []string{"cashier-a", "cashier-b"} {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			<-start
			r, e := f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: actor, Fingerprint: preview.Fingerprint, TenderedJPY: 1200, RegisterSessionRef: "register-a", IdempotencyKey: "cash-invoice-1"})
			results <- r
			errs <- e
		}(actor)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	var winner operations.Receipt
	for r := range results {
		if winner.ID == "" {
			winner = r
		} else if winner.ID != r.ID {
			t.Fatalf("retry duplicated receipt %s vs %s", winner.ID, r.ID)
		}
	}
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent cash retry failed: %v", e)
		}
	}
	if winner.DueJPY != 1100 || winner.ChangeJPY != 100 || len(winner.OrderIDs) != 2 || winner.Tax.Total.GrossJPY != 1100 {
		t.Fatalf("incorrect mixed-rate invoice %+v", winner)
	}
	// A later order cannot be silently absorbed by the old preview.
	setStorePrice(t, ctx, f, store, "tea", 400, 2)
	late := commitTestOrder(t, ctx, f.Orders, grant, 4, "cash-late", ordering.Cash, "tea")
	if _, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: late.ID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: "accept-late"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: preview.Fingerprint, TenderedJPY: 1500, RegisterSessionRef: "register-a", IdempotencyKey: "cash-late-settlement"}); !errors.Is(err, operations.ErrReconfirm) {
		t.Fatalf("late order did not require preview reconfirmation: %v", err)
	}
	latePreview, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil || latePreview.DueJPY != 400 || len(latePreview.OrderIDs) != 1 || latePreview.OrderIDs[0] != late.ID {
		t.Fatalf("late set incorrect %+v %v", latePreview, err)
	}
	setStorePrice(t, ctx, f, store, "tea", 300, 3)
	start = make(chan struct{})
	type commitResult struct {
		o   ordering.Order
		err error
	}
	committed := make(chan commitResult, 1)
	settled := make(chan error, 1)
	go func() {
		<-start
		o, e := createTestOrder(ctx, f.Orders, grant, 5, "cash-raced", ordering.Cash, "tea")
		committed <- commitResult{o, e}
	}()
	go func() {
		<-start
		_, e := f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: latePreview.Fingerprint, TenderedJPY: 500, RegisterSessionRef: "register-a", IdempotencyKey: "settle-race"})
		settled <- e
	}()
	close(start)
	racedOrder := <-committed
	if racedOrder.err != nil {
		t.Fatal(racedOrder.err)
	}
	raceErr := <-settled
	if raceErr != nil && !errors.Is(raceErr, operations.ErrReconfirm) {
		t.Fatalf("settlement race error %v", raceErr)
	}
	if _, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: racedOrder.o.ID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: "accept-raced"}); err != nil {
		t.Fatal(err)
	}
	current, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantDue := int64(300)
	if errors.Is(raceErr, operations.ErrReconfirm) {
		wantDue = 700
	}
	if current.DueJPY != wantDue {
		t.Fatalf("session-lock race left wrong unpaid set: due=%d want=%d set=%v", current.DueJPY, wantDue, current.OrderIDs)
	}
	if _, err = f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: grant.Session.ID, Actor: "cashier", Fingerprint: current.Fingerprint, TenderedJPY: 1000, RegisterSessionRef: "register-a", IdempotencyKey: "settle-race-retry"}); err != nil {
		t.Fatal(err)
	}
	states, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range states.Orders {
		if order.PaymentStatus != operations.Paid || order.FulfillmentStatus != operations.Accepted {
			t.Fatalf("unexpected released state before kitchen steps: %+v", order)
		}
	}
	// Dispatch delivery is retryable and never creates another card.
	outbox, err := f.Operations.PendingOutbox(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID}, 20)
	if err != nil || len(outbox) != 5 {
		t.Fatalf("expected five durable release events: %+v %v", outbox, err)
	}
	if err = f.Operations.RecordOutboxAttempt(ctx, outbox[0].ID, false); err != nil {
		t.Fatal(err)
	}
	retry, err := f.Operations.PendingOutbox(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID}, 20)
	if err != nil || len(retry) != 5 || retry[0].Attempts != 1 {
		t.Fatalf("outbox retry not rereadable: %+v %v", retry, err)
	}
	if err = f.Operations.RecordOutboxAttempt(ctx, outbox[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Operations.ReadSession(ctx, operations.TableScope{TenantID: scope.TenantID, StoreID: "foreign-store", TableID: scope.TableID}, grant.Session.ID); !errors.Is(err, operations.ErrNotFound) {
		t.Fatalf("cross-store read succeeded: %v", err)
	}
	// Mark every order served through the required separate transitions, then
	// confirm a REVIEW/STOP seam still blocks close even for paid, served work.
	for _, o := range states.Orders {
		for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
			next, e := f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: o.ID, Actor: "kitchen", ExpectedVersion: o.Version, IdempotencyKey: fmt.Sprintf("%s-%s", o.ID, target), Target: target})
			if e != nil {
				t.Fatalf("advance %s to %s: %v", o.ID, target, e)
			}
			o.Version = next.Version
		}
	}
	setOperationalHold(t, ctx, f, grant.Session.ID, stripe.ID, true)
	if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "cashier"); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("held paid+served order allowed close: %v", err)
	}
	setOperationalHold(t, ctx, f, grant.Session.ID, stripe.ID, false)
	if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "cashier"); err != nil {
		t.Fatalf("settled and served session did not close: %v", err)
	}
}

func TestPostgresLegacyStripeStateFailsClosedAndCashWithoutPolicyIsDenied(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.Prepaid})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := ordering.New(f.Pool, f.Sessions, f.Menus)
	if err != nil {
		t.Fatal(err)
	}
	cart, err := legacy.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: "legacy-cash-add", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := legacy.Preview(ctx, grant.Session.ID, grant.Token, 1, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: cart.Version, IdempotencyKey: "legacy-cash", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash}); !errors.Is(err, ordering.ErrPaymentUnavailable) {
		t.Fatalf("CASH order committed without operations hook: %v", err)
	}
	if _, err = legacy.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: cart.Version, IdempotencyKey: "legacy-stripe", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe}); err != nil {
		t.Fatalf("legacy Stripe order should remain available: %v", err)
	}
	state, err := f.Operations.ReadSession(ctx, operations.TableScope{TenantID: grant.Session.Scope.TenantID, StoreID: grant.Session.Scope.StoreID, TableID: grant.Session.Scope.TableID}, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Orders) != 1 || state.Orders[0].PaymentStatus != operations.Unknown || state.Orders[0].BlockedReason != "OPERATION_STATE_MISSING" {
		t.Fatalf("legacy state was not fail-closed: %+v", state.Orders)
	}
	if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "cashier"); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("legacy Stripe without payment state closed session: %v", err)
	}
}

func setStorePrice(t *testing.T, ctx context.Context, f *testguest.Fixture, scope catalog.StoreScope, key string, amount, version int64) {
	t.Helper()
	if _, err := f.Menus.SetStorePriceOverride(ctx, scope, "manager", key, amount, version); err != nil {
		t.Fatal(err)
	}
}

func commitTestOrder(t *testing.T, ctx context.Context, service *ordering.Service, grant tablesession.Grant, generation int64, key string, method ordering.PaymentMethod, product string) ordering.Order {
	t.Helper()
	o, err := createTestOrder(ctx, service, grant, generation, key, method, product)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func createTestOrder(ctx context.Context, service *ordering.Service, grant tablesession.Grant, generation int64, key string, method ordering.PaymentMethod, product string) (ordering.Order, error) {
	options := []catalog.OptionSelection{}
	if product == "teishoku" {
		options = []catalog.OptionSelection{{GroupKey: "rice", OptionKey: "regular"}}
	}
	cart, err := service.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: generation, ExpectedVersion: 1, IdempotencyKey: key + "-add", Kind: ordering.AddLine, ProductKey: product, Quantity: 1, Options: options})
	if err != nil {
		return ordering.Order{}, err
	}
	preview, err := service.Preview(ctx, grant.Session.ID, grant.Token, generation, cart.Version)
	if err != nil {
		return ordering.Order{}, err
	}
	result, err := service.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: generation, ExpectedVersion: cart.Version, IdempotencyKey: key + "-commit", PreviewFingerprint: preview.Fingerprint, PaymentMethod: method})
	if err != nil {
		return ordering.Order{}, err
	}
	return result.Order, nil
}

// markStripePaidFixture models a trusted payment-provider callback only within
// this integration test. No production HTTP endpoint can set Stripe to PAID.
func markStripePaidFixture(t *testing.T, ctx context.Context, f *testguest.Fixture, sessionID, orderID string) {
	t.Helper()
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT session_id FROM table_sessions WHERE session_id=$1 FOR UPDATE`, sessionID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err = tx.QueryRow(ctx, `UPDATE operation_order_state SET payment_status='PAID',version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND payment_method='STRIPE' AND payment_status='PENDING' RETURNING version`, orderID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,state_version) VALUES($1,'stripe-test-fixture','STRIPE_PAID_TEST_FIXTURE','PENDING','PAID',$2)`, orderID, version); err != nil {
		t.Fatal(err)
	}
	var tenant, store, table string
	if err = tx.QueryRow(ctx, `SELECT tenant_id,store_id,table_id FROM operation_order_state WHERE order_id=$1`, orderID).Scan(&tenant, &store, &table); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_kds_cards(order_id,session_id,tenant_id,store_id,table_id) VALUES($1,$2,$3,$4,$5)`, orderID, sessionID, tenant, store, table); err != nil {
		t.Fatal(err)
	}
	var next int64
	if err = tx.QueryRow(ctx, `UPDATE operation_order_state SET fulfillment_status='ACCEPTED',version=version+1 WHERE order_id=$1 AND fulfillment_status='WAITING' RETURNING version`, orderID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_fulfillment_status,after_fulfillment_status,state_version) VALUES($1,'stripe-test-fixture','KDS_RELEASED','WAITING','ACCEPTED',$2)`, orderID, next); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_outbox(order_id,event_type) VALUES($1,'KDS_RELEASED')`, orderID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func setOperationalHold(t *testing.T, ctx context.Context, f *testguest.Fixture, sessionID, orderID string, held bool) {
	t.Helper()
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT session_id FROM table_sessions WHERE session_id=$1 FOR UPDATE`, sessionID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE operation_order_state SET operational_hold=$2,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1`, orderID, held); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func commitCashOrder(t *testing.T, ctx context.Context, f *testguest.Fixture, grant tablesession.Grant, generation int64, key string) ordering.Order {
	t.Helper()
	version := int64(1)
	cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: generation, ExpectedVersion: version, IdempotencyKey: key + "-add", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: generation, ExpectedVersion: cart.Version, IdempotencyKey: key + "-commit", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash})
	if err != nil {
		t.Fatal(err)
	}
	return result.Order
}
