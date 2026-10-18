package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
)

func p84GuestFixture(t *testing.T) (context.Context, *testguest.Fixture) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	f, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close(context.Background()) })
	return ctx, f
}

func p84Request(t *testing.T, h http.Handler, method, path, token, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: HTTP %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}

func p84TeaPreview(t *testing.T, ctx context.Context, f *testguest.Fixture, grant tablesession.Grant, key string) (ordering.Cart, ordering.Preview) {
	t.Helper()
	cart, err := f.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	cart, err = f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: key,
		Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	return cart, preview
}

// All six stores share one schema and deliberately reuse store/table/product
// keys across tenants. Every target exists and is readable with its own grant.
func TestP84ThreeTenantsTwoStoresHTTPIsolation(t *testing.T) {
	ctx, f := p84GuestFixture(t)
	type party struct {
		tenant, store, locator string
		grant                  tablesession.Grant
	}
	parties := make([]party, 0, 6)
	for tenant := 0; tenant < 3; tenant++ {
		tid := fmt.Sprintf("p84-tenant-%d", tenant)
		if _, err := f.Pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES($1,$1)`, tid); err != nil {
			t.Fatal(err)
		}
		draft, err := f.Menus.SaveCompanyDraft(ctx, tid, "fixture", 0, catalog.CatalogData{Categories: []catalog.Category{{Key: "food", Name: tid, Products: []catalog.Product{{Key: "tea", Name: tid + " tea", Active: true, BasePriceMinor: int64(100 + tenant)}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Menus.PublishCompanyDraft(ctx, tid, "fixture", draft.Version); err != nil {
			t.Fatal(err)
		}
		for store := 0; store < 2; store++ {
			sid, locator := fmt.Sprintf("store-%d", store), fmt.Sprintf("p84-%d-%d", tenant, store)
			if _, err = f.Pool.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode) VALUES($1,$2,$2,'DIRECT_SCAN')`, tid, sid); err != nil {
				t.Fatal(err)
			}
			if _, err = f.Pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES($1,$2,'table','Table',$3)`, tid, sid, locator); err != nil {
				t.Fatal(err)
			}
			if _, err = f.Menus.SetStoreAvailability(ctx, catalog.StoreScope{TenantID: tid, StoreID: sid}, "fixture", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: "tea"}, Status: catalog.Available}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.Operations.SetCashSettings(ctx, operations.StoreScope{TenantID: tid, StoreID: sid}, "fixture", true, operations.PayLater, 0); err != nil {
				t.Fatal(err)
			}
			grant, err := f.Sessions.DirectJoin(ctx, locator, "")
			if err != nil {
				t.Fatal(err)
			}
			cart, preview := p84TeaPreview(t, ctx, f, grant, "seed-line")
			if _, err = f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash, IdempotencyKey: "seed-order"}); err != nil {
				t.Fatal(err)
			}
			parties = append(parties, party{tid, sid, locator, grant})
		}
	}
	for _, caller := range parties {
		t.Run(caller.locator, func(t *testing.T) {
			identity, directory, creds, err := testoperations.NewCredentialSet(caller.tenant, caller.store)
			if err != nil {
				t.Fatal(err)
			}
			authorizer := authz.PolicyAuthorizer{Directory: directory}
			h := NewOperationsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, identity, authorizer)
			admin := NewMenuHandler(f.Menus, identity, authorizer)
			for _, target := range parties {
				status := http.StatusForbidden
				if target.locator == caller.locator {
					status = http.StatusOK
				}
				guestBase := "/v1/guest/sessions/" + target.grant.Session.ID
				for _, suffix := range []string{"/menu", "/cart", "/orders", "/operations"} {
					p84Request(t, h, "GET", guestBase+suffix, caller.grant.Token, "", status)
				}
				base := "/v1/staff/tenants/" + target.tenant + "/stores/" + target.store
				p84Request(t, h, "GET", base+"/operations/tables", creds.Cashier.Token, "", status)
				p84Request(t, h, "GET", base+"/tables/table/sessions/"+target.grant.Session.ID+"/operations", creds.Kitchen.Token, "", status)
				p84Request(t, admin, "GET", "/v1/admin/tenants/"+target.tenant+"/stores/"+target.store+"/menu", creds.Manager.Token, "", status)
				// A denied write must neither change settings nor create an audit.
				p84Request(t, h, "PUT", base+"/operations/cash-settings", creds.Manager.Token, `{"enabled":true,"mode":"PAY_LATER","expected_version":1}`, status)
			}
			ownBase := "/v1/staff/tenants/" + caller.tenant + "/stores/" + caller.store
			p84Request(t, h, "PUT", ownBase+"/operations/cash-settings", creds.Kitchen.Token, `{"enabled":false,"mode":"PREPAID","expected_version":2}`, 403)
			directory.Remove(creds.Cashier.SubjectID)
			p84Request(t, h, "GET", ownBase+"/operations/tables", creds.Cashier.Token, "", 403)
		})
	}
	var audits int
	if err := f.Pool.QueryRow(ctx, `SELECT count(*) FROM store_cash_settings_audit WHERE tenant_id LIKE 'p84-tenant-%'`).Scan(&audits); err != nil || audits != 12 {
		t.Fatalf("cross-scope writes changed audit: count=%d want 12 err=%v", audits, err)
	}
}

// Existing P-78 races use a minimal policy; this race uses the full keyless
// payment policy so losing STRIPE admission cannot leave an orphan attempt.
func TestP84ThreeGuestsStripeCashCommitRace(t *testing.T) {
	h := newPaymentHTTPFixture(t)
	ctx := context.Background()
	f := h.fixture
	grants := make([]tablesession.Grant, 3)
	for i := range grants {
		g, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
		if err != nil {
			t.Fatal(err)
		}
		grants[i] = g
		if i > 0 && (g.Session.ID != grants[0].Session.ID || g.Token == grants[i-1].Token) {
			t.Fatal("three phones did not join one seating with distinct grants")
		}
	}
	// Repeated independent generations exercise scheduling without asserting
	// which method wins. The branch-specific durable rows must match every winner.
	for generation := int64(1); generation <= 6; generation++ {
		cart, preview := p84TeaPreview(t, ctx, f, grants[0], fmt.Sprintf("line-%d", generation))
		if cart.Generation != generation {
			t.Fatalf("generation=%d want %d", cart.Generation, generation)
		}
		start := make(chan struct{})
		type outcome struct {
			result ordering.CommitResult
			err    error
		}
		results := make(chan outcome, 2)
		for i, method := range []ordering.PaymentMethod{ordering.Stripe, ordering.Cash} {
			input := ordering.CommitRequest{SessionID: grants[i].Session.ID, GrantToken: grants[i].Token,
				ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint,
				PaymentMethod: method, IdempotencyKey: fmt.Sprintf("commit-%d-%d", generation, i),
				CheckoutProofHash: sha256.Sum256([]byte("p84-fixture-browser")), CheckoutProofExpiresAt: time.Now().Add(20 * time.Minute)}
			go func() {
				<-start
				r, err := f.Orders.Commit(ctx, input)
				results <- outcome{r, err}
			}()
		}
		close(start)
		a, b := <-results, <-results
		if a.err != nil || b.err != nil {
			t.Fatalf("race errors: %v / %v", a.err, b.err)
		}
		if a.result.Order.ID != b.result.Order.ID || a.result.Order.PaymentMethod != b.result.Order.PaymentMethod ||
			!((a.result.Outcome == ordering.Committed && b.result.Outcome == ordering.AlreadyCommitted) || (b.result.Outcome == ordering.Committed && a.result.Outcome == ordering.AlreadyCommitted)) {
			t.Fatalf("race did not converge: %+v / %+v", a.result, b.result)
		}
		var orders, states, attempts, work int
		if err := f.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM table_orders WHERE session_id=$1 AND generation=$2),
			(SELECT count(*) FROM operation_order_state WHERE order_id=$3 AND payment_method=$4),
			(SELECT count(*) FROM stripe_payment_attempts WHERE order_id=$3),
			(SELECT count(*) FROM stripe_payment_work WHERE order_id=$3)`, grants[0].Session.ID, generation, a.result.Order.ID, string(a.result.Order.PaymentMethod)).Scan(&orders, &states, &attempts, &work); err != nil {
			t.Fatal(err)
		}
		wantAttempts := 0
		if a.result.Order.PaymentMethod == ordering.Stripe {
			wantAttempts = 1
		}
		if orders != 1 || states != 1 || attempts != wantAttempts || work != wantAttempts {
			t.Fatalf("winner=%s orders=%d states=%d attempts=%d work=%d", a.result.Order.PaymentMethod, orders, states, attempts, work)
		}
		current, err := f.Orders.ReadCart(ctx, grants[2].Session.ID, grants[2].Token)
		if err != nil || current.Generation != generation+1 || current.Version != 1 || len(current.Lines) != 0 {
			t.Fatalf("third phone rollover: %+v err=%v", current, err)
		}
	}
}

func TestP84SameQRNextPartyPrivacy(t *testing.T) {
	for _, mode := range []tablesession.Mode{tablesession.StaffOpen, tablesession.SessionPIN, tablesession.DirectScan} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, f := p84GuestFixture(t)
			if err := f.Sessions.SetStoreMode(ctx, f.TenantID, f.StoreID, mode); err != nil {
				t.Fatal(err)
			}
			join := func() tablesession.Grant {
				t.Helper()
				if mode == tablesession.DirectScan {
					g, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
					if err != nil {
						t.Fatal(err)
					}
					return g
				}
				o, err := f.Sessions.Open(ctx, tablesession.Scope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}, "fixture-staff")
				if err != nil {
					t.Fatal(err)
				}
				g, err := f.Sessions.Join(ctx, o.Session.ID, o.Session.ID, o.PIN)
				if err != nil {
					t.Fatal(err)
				}
				return g
			}
			old := join()
			cart, preview := p84TeaPreview(t, ctx, f, old, "old-line")
			order, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: old.Session.ID, GrantToken: old.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash, IdempotencyKey: "old-order"})
			if err != nil {
				t.Fatal(err)
			}
			scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
			cash, err := f.Operations.PreviewCash(ctx, scope, old.Session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: old.Session.ID, Actor: "cashier", Fingerprint: cash.Fingerprint, TenderedJPY: cash.DueJPY, RegisterSessionRef: "fixture-shift", IdempotencyKey: "old-cash"}); err != nil {
				t.Fatal(err)
			}
			detail, err := f.Operations.ReadSession(ctx, scope, old.Session.ID)
			if err != nil || len(detail.Orders) != 1 {
				t.Fatalf("old party detail: %+v err=%v", detail, err)
			}
			state := detail.Orders[0]
			if state.RequiresStaffAcceptance {
				state, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: old.Session.ID, OrderID: order.Order.ID, Actor: "cashier", ExpectedVersion: state.Version, IdempotencyKey: "old-accept"})
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
				state, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: old.Session.ID, OrderID: order.Order.ID, Actor: "fixture-staff", ExpectedVersion: state.Version, IdempotencyKey: string(target), Target: target})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Leave an uncommitted line as well as private order history behind.
			p84TeaPreview(t, ctx, f, old, "old-abandoned-line")
			if err = f.Sessions.Close(ctx, old.Session.Scope, old.Session.ID, "cashier"); err != nil {
				t.Fatal(err)
			}
			next := join()
			if next.Session.ID == old.Session.ID {
				t.Fatal("same physical QR reused a closed seating")
			}
			h := NewGuestHandler(f.Sessions, f.Menus, f.Orders, f.Operations)
			for _, suffix := range []string{"/menu", "/cart", "/orders", "/operations"} {
				p84Request(t, h, "GET", "/v1/guest/sessions/"+old.Session.ID+suffix, next.Token, "", 403)
				p84Request(t, h, "GET", "/v1/guest/sessions/"+old.Session.ID+suffix, old.Token, "", 403)
				p84Request(t, h, "GET", "/v1/guest/sessions/"+next.Session.ID+suffix, old.Token, "", 403)
				w := p84Request(t, h, "GET", "/v1/guest/sessions/"+next.Session.ID+suffix, next.Token, "", 200)
				if strings.Contains(w.Body.String(), order.Order.ID) || strings.Contains(w.Body.String(), old.Token) {
					t.Fatal("next party response contains prior private material")
				}
			}
			fresh, err := f.Orders.ReadCart(ctx, next.Session.ID, next.Token)
			if err != nil || len(fresh.Lines) != 0 || fresh.Generation != 1 {
				t.Fatalf("prior cart carried into new seating: %+v err=%v", fresh, err)
			}
			orders, err := f.Orders.ListOrders(ctx, next.Session.ID, next.Token)
			if err != nil || len(orders) != 0 {
				t.Fatalf("prior order carried into new seating: %+v err=%v", orders, err)
			}
		})
	}
}

func TestP84RestartOutboxRetryAndMissedEventResync(t *testing.T) {
	ctx, f := p84GuestFixture(t)
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cart, preview := p84TeaPreview(t, ctx, f, grant, "restart-line")
	order, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash, IdempotencyKey: "restart-order"})
	if err != nil {
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
	if _, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: order.Order.ID, Actor: "cashier", ExpectedVersion: 1, IdempotencyKey: "restart-accept"}); err != nil {
		t.Fatal(err)
	}
	store := operations.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}
	pending, err := f.Operations.PendingOutbox(ctx, store, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("initial outbox: %+v err=%v", pending, err)
	}
	if err = f.Operations.RecordOutboxAttempt(ctx, pending[0].ID, false); err != nil {
		t.Fatal(err)
	}
	// Simulate process recreation using the same schema, with no in-memory
	// delivery state transferred to the new operations service.
	restarted, err := operations.New(f.Pool)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := restarted.PendingOutbox(ctx, store, 10)
	if err != nil || len(retry) != 1 || retry[0].ID != pending[0].ID || retry[0].Attempts != 1 {
		t.Fatalf("durable retry lost across restart: %+v err=%v", retry, err)
	}
	if err = restarted.RecordOutboxAttempt(ctx, retry[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err = restarted.RecordOutboxAttempt(ctx, retry[0].ID, true); !errors.Is(err, operations.ErrNotFound) {
		t.Fatalf("duplicate acknowledgment: %v", err)
	}
	if pending, err = restarted.PendingOutbox(ctx, store, 10); err != nil || len(pending) != 0 {
		t.Fatalf("delivered event still pending: %+v err=%v", pending, err)
	}
	// A kitchen client that missed the notification re-syncs through the
	// ordinary authorized HTTP list after the outbox was acknowledged.
	identity, directory, creds, err := testoperations.NewCredentialSet(f.TenantID, f.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	h := NewOperationsRuntime(f.Sessions, f.Menus, f.Orders, restarted, identity, authz.PolicyAuthorizer{Directory: directory})
	w := p84Request(t, h, "GET", "/v1/staff/tenants/"+f.TenantID+"/stores/"+f.StoreID+"/operations/kds", creds.Kitchen.Token, "", 200)
	if strings.Count(w.Body.String(), order.Order.ID) != 1 {
		t.Fatal("missed-event resync did not return exactly one card")
	}
	var cards, events, attempts int
	if err = f.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_kds_cards), (SELECT count(*) FROM operation_outbox), (SELECT attempts FROM operation_outbox WHERE outbox_id=$1)`, retry[0].ID).Scan(&cards, &events, &attempts); err != nil || cards != 1 || events != 1 || attempts != 2 {
		t.Fatalf("retry duplicated durable state: cards=%d events=%d attempts=%d err=%v", cards, events, attempts, err)
	}
}

func TestP84LateStripeCommitFailureRollsBackAllSideEffects(t *testing.T) {
	h := newPaymentHTTPFixture(t)
	ctx := context.Background()
	f := h.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cart, preview := p84TeaPreview(t, ctx, f, grant, "rollback-line")
	input := ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe, IdempotencyKey: "rollback-order",
		CheckoutProofHash: sha256.Sum256([]byte("rollback-fixture-browser")), CheckoutProofExpiresAt: time.Now().Add(20 * time.Minute)}
	// Test-schema-only failure AFTER order, operations state, attempt, CREATE
	// work and replay binding have been inserted. This is not a migration.
	if _, err = f.Pool.Exec(ctx, `CREATE FUNCTION p84_fail_cart_rollover() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'P84 injected late commit failure'; END; $$;
		CREATE TRIGGER p84_fail_cart_rollover BEFORE UPDATE ON table_carts FOR EACH ROW EXECUTE FUNCTION p84_fail_cart_rollover()`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Orders.Commit(ctx, input); err == nil || !strings.Contains(err.Error(), "P84 injected late commit failure") {
		t.Fatalf("did not reach injected late failure: %v", err)
	}
	unchanged, err := f.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
	if err != nil || !reflect.DeepEqual(unchanged, cart) {
		t.Fatalf("failed transaction changed cart: %+v err=%v", unchanged, err)
	}
	for _, table := range []string{"table_orders", "table_order_idempotency", "operation_order_state", "operation_order_events", "stripe_payment_attempts", "stripe_payment_work", "operation_kds_cards", "operation_outbox"} {
		var count int
		if err = f.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback left %s rows=%d err=%v", table, count, err)
		}
	}
	if _, err = f.Pool.Exec(ctx, `DROP TRIGGER p84_fail_cart_rollover ON table_carts; DROP FUNCTION p84_fail_cart_rollover()`); err != nil {
		t.Fatal(err)
	}
	committed, err := f.Orders.Commit(ctx, input)
	if err != nil || committed.Outcome != ordering.Committed {
		t.Fatalf("same request could not recover after rollback: %+v err=%v", committed, err)
	}
	newWorker := func() *payments.Service {
		t.Helper()
		service, err := payments.New(f.Pool, f.Operations, paymentHTTPResolver{paymentHTTPCredentialRef: {
			Provider: h.provider, Account: h.account, ProfileFingerprint: sha256.Sum256([]byte(stripeprovider.APIVersion + "|http-test-profile")),
		}}, payments.Config{CreateRetryWindow: 20 * time.Hour, ContinuationTTL: 30 * time.Minute, WorkLeaseTTL: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	// Lose the provider response, then model a crashed retry worker whose
	// lease expired. Recreate the service without transferring worker state.
	h.provider.LoseNextCreateResponse()
	service := newWorker()
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("recreated worker lost durable CREATE work: worked=%v err=%v", worked, err)
	}
	updated, err := f.Pool.Exec(ctx, `UPDATE stripe_payment_work SET status='LEASED',lease_token='p84-crashed-worker',lease_until=clock_timestamp()-interval '1 second' WHERE order_id=$1 AND work_type='CREATE' AND status='READY'`, committed.Order.ID)
	if err != nil || updated.RowsAffected() != 1 {
		t.Fatalf("could not model crashed lease: rows=%d err=%v", updated.RowsAffected(), err)
	}
	service = newWorker()
	if worked, err := service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("expired lease not recovered: worked=%v err=%v", worked, err)
	}
	if requests, sessions := h.provider.Counts(); requests != 2 || sessions != 1 {
		t.Fatalf("rollback/restart issued extra Checkout: requests=%d sessions=%d", requests, sessions)
	}
	var status string
	if err = f.Pool.QueryRow(ctx, `SELECT status FROM stripe_payment_work WHERE order_id=$1 AND work_type='CREATE'`, committed.Order.ID).Scan(&status); err != nil || status != "DONE" {
		t.Fatalf("recovered work status=%s err=%v", status, err)
	}
}

func TestP84FailedStripeOrderDoesNotBlockNextCashGeneration(t *testing.T) {
	h := newPaymentHTTPFixture(t)
	ctx := context.Background()
	f := h.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cart, preview := p84TeaPreview(t, ctx, f, grant, "failed-stripe-line")
	stripe, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint,
		PaymentMethod: ordering.Stripe, IdempotencyKey: "failed-stripe-order",
		CheckoutProofHash: sha256.Sum256([]byte("failed-payment-browser")), CheckoutProofExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("CREATE work: worked=%v err=%v", worked, err)
	}
	var intentID string
	if err = f.Pool.QueryRow(ctx, `SELECT payment_intent_id FROM stripe_payment_attempts WHERE order_id=$1`, stripe.Order.ID).Scan(&intentID); err != nil {
		t.Fatal(err)
	}
	intent, err := h.provider.RetrievePaymentIntent(ctx, h.account, intentID)
	if err != nil {
		t.Fatal(err)
	}
	event := stripeprovider.VerifiedEvent{ID: "evt_p84_failed", Type: "payment_intent.payment_failed", AccountID: h.account.StripeAccountID,
		APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: intentID, PaymentIntent: &intent}
	if accepted, err := h.service.EnqueueVerifiedWebhook(ctx, event); err != nil || !accepted {
		t.Fatalf("failure event: accepted=%v err=%v", accepted, err)
	}
	if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("failure work: worked=%v err=%v", worked, err)
	}
	cart, preview = p84TeaPreview(t, ctx, f, grant, "new-cash-line")
	if cart.Generation != 2 {
		t.Fatalf("failure rewound generation: %d", cart.Generation)
	}
	cash, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint,
		PaymentMethod: ordering.Cash, IdempotencyKey: "new-cash-order"})
	if err != nil || cash.Order.ID == stripe.Order.ID || cash.Order.PaymentMethod != ordering.Cash {
		t.Fatalf("new CASH order: %+v err=%v", cash, err)
	}
	orders, err := f.Orders.ListOrders(ctx, grant.Session.ID, grant.Token)
	if err != nil || len(orders) != 2 {
		t.Fatalf("payment failure removed history: %+v err=%v", orders, err)
	}
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
	status, err := h.service.ReadSafeStatus(ctx, scope, grant.Session.ID, stripe.Order.ID)
	if err != nil || status.State != payments.StateFailed {
		t.Fatalf("new order changed old payment failure: %+v err=%v", status, err)
	}
	var methods, attempts int
	if err = f.Pool.QueryRow(ctx, `SELECT (SELECT count(DISTINCT payment_method) FROM table_orders WHERE session_id=$1), (SELECT count(*) FROM stripe_payment_attempts WHERE session_id=$1)`, grant.Session.ID).Scan(&methods, &attempts); err != nil || methods != 2 || attempts != 1 {
		t.Fatalf("failure converted payment or duplicated attempt: methods=%d attempts=%d err=%v", methods, attempts, err)
	}
}
