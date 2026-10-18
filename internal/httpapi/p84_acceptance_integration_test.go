package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
		for j := 0; j < i; j++ {
			if g.Session.ID != grants[j].Session.ID || g.Token == grants[j].Token {
				t.Fatal("three phones did not join one seating with distinct grants")
			}
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

// D7 accepts that a photo of the fixed QR can join an open STAFF_OPEN seating
// and submit PAY_LATER cash orders that reach KDS without staff acceptance.
func TestP84StaffOpenFixedQRPayLaterAutoRelease(t *testing.T) {
	ctx, f := p84GuestFixture(t)
	if err := f.Sessions.SetStoreMode(ctx, f.TenantID, f.StoreID, tablesession.StaffOpen); err != nil {
		t.Fatal(err)
	}
	settings, err := f.Operations.ReadCashSettings(ctx, operations.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID})
	if err != nil || !settings.Enabled || settings.Mode != operations.PayLater {
		t.Fatalf("expected cash-enabled PAY_LATER fixture: %+v err=%v", settings, err)
	}
	h := NewSessionHandler(f.Sessions, nil, nil)
	qrPath := "/v1/guest/tables/" + f.Locator
	status := requestJSON(t, h, http.MethodGet, qrPath, "", "")
	var table tablesession.PublicTable
	if err = json.Unmarshal(status.Body.Bytes(), &table); err != nil {
		t.Fatal(err)
	}
	if status.Code != http.StatusOK || table.Mode != tablesession.StaffOpen || table.Open || table.CurrentSessionID != "" {
		t.Fatalf("table should await staff open: HTTP %d %+v", status.Code, table)
	}
	if denied := requestJSON(t, h, http.MethodPost, qrPath+"/join", `{}`, ""); denied.Code != http.StatusForbidden {
		t.Fatalf("guest joined before staff open: HTTP %d %s", denied.Code, denied.Body.String())
	}
	var grantCount int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM table_join_grants`).Scan(&grantCount); err != nil || grantCount != 0 {
		t.Fatalf("grant issued before staff open: count=%d err=%v", grantCount, err)
	}
	opened, err := f.Sessions.Open(ctx, tablesession.Scope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}, "fixture-staff")
	if err != nil {
		t.Fatal(err)
	}
	grants := make([]tablesession.Grant, 2)
	for i := range grants {
		// Each guest starts with only the fixed QR, no cookie, token or PIN.
		// Guest 2 represents an independent client holding a photo of that QR.
		status = requestJSON(t, h, http.MethodGet, qrPath, "", "")
		var current tablesession.PublicTable
		if err = json.Unmarshal(status.Body.Bytes(), &current); err != nil {
			t.Fatal(err)
		}
		if status.Code != http.StatusOK || !current.Open || current.Mode != tablesession.StaffOpen || current.CurrentSessionID != opened.Session.ID {
			t.Fatalf("guest %d could not discover the same seating: HTTP %d %+v", i+1, status.Code, current)
		}
		joined := requestJSON(t, h, http.MethodPost, "/v1/guest/sessions/"+current.CurrentSessionID+"/join", `{}`, "")
		if joined.Code != http.StatusOK {
			t.Fatalf("guest %d join: HTTP %d %s", i+1, joined.Code, joined.Body.String())
		}
		if err = json.Unmarshal(joined.Body.Bytes(), &grants[i]); err != nil {
			t.Fatal(err)
		}
		if grants[i].Token == "" || grants[i].Session.ID != opened.Session.ID || grants[i].Session.RequiresStaffAcceptance {
			t.Fatalf("guest %d did not receive a grant for the staff-opened seating", i+1)
		}
	}
	if grants[0].Token == grants[1].Token {
		t.Fatal("independent guests received the same TABLE_JOIN grant")
	}
	guest := grants[1]
	cart, preview := p84TeaPreview(t, ctx, f, guest, "photo-guest-line")
	committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: guest.Session.ID, GrantToken: guest.Token,
		ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint,
		PaymentMethod: ordering.Cash, IdempotencyKey: "photo-guest-order"})
	if err != nil {
		t.Fatal(err)
	}
	if committed.Outcome != ordering.Committed || committed.Order.SessionID != opened.Session.ID || committed.Order.PaymentMethod != ordering.Cash || committed.Order.RequiresStaffAcceptance {
		t.Fatalf("unexpected STAFF_OPEN cash commit: %+v", committed)
	}
	// Read durable state immediately after commit: no AcceptOrder or other action.
	var orderRequiresAcceptance, stateRequiresAcceptance, staffAccepted bool
	var paymentMethod, cashMode, fulfillment string
	var cards int
	if err = f.Pool.QueryRow(ctx, `SELECT o.requires_staff_acceptance,s.requires_staff_acceptance,s.staff_accepted,
		s.payment_method,s.cash_mode,s.fulfillment_status,
		(SELECT count(*) FROM operation_kds_cards k WHERE k.order_id=o.order_id)
		FROM table_orders o JOIN operation_order_state s USING(order_id) WHERE o.order_id=$1`, committed.Order.ID).
		Scan(&orderRequiresAcceptance, &stateRequiresAcceptance, &staffAccepted, &paymentMethod, &cashMode, &fulfillment, &cards); err != nil {
		t.Fatal(err)
	}
	if orderRequiresAcceptance || stateRequiresAcceptance || staffAccepted || paymentMethod != string(ordering.Cash) || cashMode != string(operations.PayLater) || fulfillment != string(operations.Accepted) || cards != 1 {
		t.Fatalf("expected automatic PAY_LATER release: orderRequiresAcceptance=%t stateRequiresAcceptance=%t staffAccepted=%t payment=%s cashMode=%s fulfillment=%s cards=%d",
			orderRequiresAcceptance, stateRequiresAcceptance, staffAccepted, paymentMethod, cashMode, fulfillment, cards)
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

// Establish a real OPEN attempt through the ordinary browser-proof/commit
// boundary. Negative resume assertions must run after CREATE, not while pending.
func p84OpenStripeCheckout(t *testing.T, h *paymentHTTPFixture) (tablesession.Grant, string, *http.Cookie, stripeprovider.CheckoutResult) {
	t.Helper()
	ctx := context.Background()
	f := h.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/guest/sessions/" + grant.Session.ID
	cart, preview := p84TeaPreview(t, ctx, f, grant, "p84-open-line")
	proof := h.request(t, http.MethodPost, base+"/checkout-context", "", grant.Token, nil, paymentHTTPOrigin, "")
	decodePaymentHTTP(t, proof, http.StatusNoContent, nil)
	cookies := proof.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("browser proof cookies=%d want 1", len(cookies))
	}
	body := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"preview_fingerprint":%q,"payment_method":"STRIPE"}`, cart.Generation, cart.Version, preview.Fingerprint)
	committed := h.requestWithKey(t, http.MethodPost, base+"/cart/commit", body, grant.Token, cookies[0], paymentHTTPOrigin, "", "p84-open-order")
	var dto struct {
		Order struct {
			ID string `json:"order_id"`
		} `json:"order"`
	}
	decodePaymentHTTP(t, committed, http.StatusOK, &dto)
	if dto.Order.ID == "" {
		t.Fatal("missing committed order")
	}
	if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("CREATE: worked=%v err=%v", worked, err)
	}
	var id, state string
	if err = f.Pool.QueryRow(ctx, `SELECT checkout_session_id,state FROM stripe_payment_attempts WHERE order_id=$1`, dto.Order.ID).Scan(&id, &state); err != nil || state != string(payments.StateOpen) {
		t.Fatalf("attempt not OPEN: state=%s err=%v", state, err)
	}
	checkout, err := h.provider.RetrieveCheckout(ctx, h.account, id)
	if err != nil || checkout.Status != stripeprovider.CheckoutOpen || checkout.PaymentStatus != stripeprovider.PaymentUnpaid || checkout.URL == "" {
		t.Fatalf("provider Checkout not open/unpaid/resumable: %+v err=%v", checkout, err)
	}
	return grant, dto.Order.ID, cookies[0], checkout
}

func TestP84OpenCheckoutResumeRefusesOtherBrowser(t *testing.T) {
	h := newPaymentHTTPFixture(t)
	first, orderID, cookie, checkout := p84OpenStripeCheckout(t, h)
	second, err := h.fixture.Sessions.DirectJoin(context.Background(), h.fixture.Locator, first.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/guest/sessions/" + first.Session.ID
	proof := h.request(t, http.MethodPost, base+"/checkout-context", "", second.Token, nil, paymentHTTPOrigin, "")
	decodePaymentHTTP(t, proof, http.StatusNoContent, nil)
	cookies := proof.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == cookie.Value {
		t.Fatal("second browser did not receive independent proof")
	}
	path := base + "/orders/" + orderID + "/checkout/resume"
	snapshot := func() map[string]string {
		state := p84PaymentSnapshot(t, h, orderID)
		var work string
		if err := h.fixture.Pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(to_jsonb(w) ORDER BY to_jsonb(w)::text),'[]'::jsonb)::text FROM stripe_payment_work w WHERE order_id=$1`, orderID).Scan(&work); err != nil {
			t.Fatal(err)
		}
		state["stripe_payment_work"] = work
		return state
	}
	before := snapshot()
	var otherBrowserRefusals []*httptest.ResponseRecorder
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		token  string
	}{{"shared guest own cookie", cookies[0], second.Token}, {"shared guest cookie only", cookies[0], ""}, {"original guest missing cookie", nil, first.Token}} {
		t.Run(tc.name, func(t *testing.T) {
			denied := h.request(t, http.MethodPost, path, "", tc.token, tc.cookie, paymentHTTPOrigin, "")
			if denied.Header().Get("Location") != "" || strings.Contains(denied.Body.String(), "url") || strings.Contains(denied.Body.String(), "https://") {
				t.Fatalf("resume denial exposed URL: headers=%v body=%s", denied.Header(), denied.Body.String())
			}
			if tc.cookie != nil {
				decodePaymentHTTP(t, denied, http.StatusConflict, nil)
				if denied.Body.String() != "{\"error\":\"payment_unavailable\"}\n" {
					t.Fatalf("non-uniform resume refusal: %s", denied.Body.String())
				}
				otherBrowserRefusals = append(otherBrowserRefusals, denied)
			} else {
				decodePaymentHTTP(t, denied, http.StatusForbidden, nil)
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("resume refusal changed durable state: before=%v after=%v", before, after)
			}
			if after, err := h.provider.RetrieveCheckout(context.Background(), h.account, checkout.SessionID); err != nil || !reflect.DeepEqual(checkout, after) {
				t.Fatalf("resume refusal changed provider Checkout: before=%+v after=%+v err=%v", checkout, after, err)
			}
		})
	}
	var resumed struct {
		URL string `json:"url"`
	}
	allowed := h.request(t, http.MethodPost, path, "", "", cookie, paymentHTTPOrigin, "")
	decodePaymentHTTP(t, allowed, http.StatusOK, &resumed)
	if resumed.URL != checkout.URL {
		t.Fatalf("original browser did not resume the same OPEN Checkout: %+v", resumed)
	}
	// A genuine expired Checkout with its original proof must be indistinguishable
	// from an OPEN Checkout requested by another browser: no ownership oracle.
	if _, err := h.provider.SetOutcome(checkout.SessionID, stripeprovider.CheckoutExpired, stripeprovider.PaymentUnpaid); err != nil {
		t.Fatal(err)
	}
	unavailable := h.request(t, http.MethodPost, path, "", "", cookie, paymentHTTPOrigin, "")
	decodePaymentHTTP(t, unavailable, http.StatusConflict, nil)
	if unavailable.Header().Get("Location") != "" || unavailable.Body.String() != "{\"error\":\"payment_unavailable\"}\n" {
		t.Fatalf("expired Checkout refusal exposed URL or changed body: headers=%v body=%s", unavailable.Header(), unavailable.Body.String())
	}
	for _, denied := range otherBrowserRefusals {
		if denied.Code != unavailable.Code || denied.Body.String() != unavailable.Body.String() {
			t.Fatalf("resume refusal reveals ownership: other-browser=%d %q unavailable=%d %q", denied.Code, denied.Body.String(), unavailable.Code, unavailable.Body.String())
		}
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("unavailable resume changed durable state: before=%v after=%v", before, after)
	}
}

func p84ProcessVerifiedEvent(t *testing.T, h *paymentHTTPFixture, event stripeprovider.VerifiedEvent) {
	t.Helper()
	ctx := context.Background()
	if accepted, err := h.service.EnqueueVerifiedWebhook(ctx, event); err != nil || !accepted {
		t.Fatalf("enqueue %s: accepted=%v err=%v", event.Type, accepted, err)
	}
	if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("process %s: worked=%v err=%v", event.Type, worked, err)
	}
	var done bool
	if err := h.fixture.Pool.QueryRow(ctx, `SELECT e.processed_at IS NOT NULL AND e.safe_error_code IS NULL AND w.status='DONE' AND w.safe_error_code IS NULL AND w.lease_token IS NULL AND w.lease_until IS NULL
		FROM stripe_webhook_events e JOIN stripe_payment_work w USING(webhook_row_id) WHERE e.stripe_event_id=$1`, event.ID).Scan(&done); err != nil || !done {
		t.Fatalf("%s did not finish inbox/work cleanly: done=%v err=%v", event.Type, done, err)
	}
}

// Compare complete durable payment/order rows and history, including timestamps
// and versions. Inbox/work legitimately grow, and are checked separately.
func p84PaymentSnapshot(t *testing.T, h *paymentHTTPFixture, orderID string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	for _, table := range []string{"table_orders", "stripe_payment_attempts", "stripe_payment_attempt_events", "operation_order_state", "operation_order_events", "operation_kds_cards", "operation_outbox"} {
		var rows string
		if err := h.fixture.Pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM `+table+` r WHERE order_id=$1`, orderID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		snapshot[table] = rows
	}
	return snapshot
}

func TestP84PaidCheckoutIgnoresStaleFailureAndExpiry(t *testing.T) {
	h := newPaymentHTTPFixture(t)
	grant, orderID, _, open := p84OpenStripeCheckout(t, h)
	ctx := context.Background()
	// Capture genuine pre-payment projections for delayed negative events.
	intent, err := h.provider.RetrievePaymentIntent(ctx, h.account, open.PaymentIntentID)
	if err != nil {
		t.Fatal(err)
	}
	expired := open
	expired.Status, expired.URL = stripeprovider.CheckoutExpired, ""
	failed := open
	failed.Status, failed.URL = stripeprovider.CheckoutComplete, ""
	paid, err := h.provider.SetOutcome(open.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	p84ProcessVerifiedEvent(t, h, stripeprovider.VerifiedEvent{ID: "evt_p84_paid", Type: "checkout.session.completed", AccountID: h.account.StripeAccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: at, ObjectID: paid.SessionID, Checkout: &paid})
	scope := operations.TableScope{TenantID: h.fixture.TenantID, StoreID: h.fixture.StoreID, TableID: h.fixture.TableID}
	status, err := h.service.ReadSafeStatus(ctx, scope, grant.Session.ID, orderID)
	if err != nil || status.State != payments.StatePaid || status.PaymentStatus != "paid" {
		t.Fatalf("baseline not PAID: %+v err=%v", status, err)
	}
	detail, err := h.fixture.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil || len(detail.Orders) != 1 || detail.Orders[0].PaymentStatus != operations.Paid {
		t.Fatalf("baseline operation not PAID: %+v err=%v", detail, err)
	}
	before := p84PaymentSnapshot(t, h, orderID)
	for _, event := range []stripeprovider.VerifiedEvent{
		{ID: "evt_p84_stale_pi_failed", Type: "payment_intent.payment_failed", ObjectID: intent.ID, PaymentIntent: &intent},
		{ID: "evt_p84_stale_async_failed", Type: "checkout.session.async_payment_failed", ObjectID: failed.SessionID, Checkout: &failed},
		{ID: "evt_p84_stale_expired", Type: "checkout.session.expired", ObjectID: expired.SessionID, Checkout: &expired},
	} {
		t.Run(event.Type, func(t *testing.T) {
			event.AccountID, event.APIVersion, event.CreatedAt = h.account.StripeAccountID, stripeprovider.APIVersion, at.Add(-time.Minute)
			p84ProcessVerifiedEvent(t, h, event)
			if after := p84PaymentSnapshot(t, h, orderID); !reflect.DeepEqual(before, after) {
				t.Fatalf("stale %s changed paid state/history: before=%v after=%v", event.Type, before, after)
			}
			if accepted, err := h.service.EnqueueVerifiedWebhook(ctx, event); err != nil || accepted {
				t.Fatalf("duplicate stale event: accepted=%v err=%v", accepted, err)
			}
			if worked, err := h.service.ProcessNext(ctx); err != nil || worked {
				t.Fatalf("duplicate left extra work: worked=%v err=%v", worked, err)
			}
		})
	}
}

func TestP84AcceptedPaidReviewOrderExcludedFromKDS(t *testing.T) {
	h := newPaymentHTTPFixture(t)
	grant, orderID, _, open := p84OpenStripeCheckout(t, h)
	ctx := context.Background()
	f := h.fixture
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
	if _, err := f.Pool.Exec(ctx, `UPDATE operation_order_state SET operational_hold=true,version=version+1 WHERE order_id=$1`, orderID); err != nil {
		t.Fatal(err)
	}
	paid, err := h.provider.SetOutcome(open.SessionID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	event := stripeprovider.VerifiedEvent{ID: "evt_p84_review_paid", Type: "checkout.session.completed", AccountID: h.account.StripeAccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: paid.SessionID, Checkout: &paid}
	p84ProcessVerifiedEvent(t, h, event)
	// Fixture-only clear of the operational hold isolates the REVIEW gate.
	// This is not manager review approval or evidence of HQ propagation.
	if _, err = f.Pool.Exec(ctx, `UPDATE operation_order_state SET operational_hold=false,version=version+1 WHERE order_id=$1`, orderID); err != nil {
		t.Fatal(err)
	}
	// A distinct paid observation must preserve the review requirement.
	event.ID = "evt_p84_review_paid_again"
	p84ProcessVerifiedEvent(t, h, event)
	status, err := h.service.ReadSafeStatus(ctx, scope, grant.Session.ID, orderID)
	if err != nil || status.State != payments.StateReviewRequired || status.PaymentStatus != "paid" || !status.PaymentReviewRequired {
		t.Fatalf("paid review state missing: %+v err=%v", status, err)
	}
	detail, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil || len(detail.Orders) != 1 {
		t.Fatalf("read review order: %+v err=%v", detail, err)
	}
	// Acceptance now invokes the real release path with hold=false and review=true.
	state, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "p84-cashier", ExpectedVersion: detail.Orders[0].Version, IdempotencyKey: "p84-review-reception"})
	if err != nil {
		t.Fatal(err)
	}
	if !state.RequiresStaffAcceptance || !state.StaffAccepted || state.OperationalHold || !state.PaymentReviewRequired || state.PaymentStatus != operations.Paid || state.FulfillmentStatus != operations.Waiting {
		t.Fatalf("review is not the sole remaining release gate: %+v", state)
	}
	kds := h.request(t, http.MethodGet, "/v1/staff/tenants/test-tenant/stores/test-store/operations/kds", "", h.creds.Kitchen.Token, nil, "", "")
	var dto struct {
		Orders []operations.OrderState `json:"orders"`
	}
	decodePaymentHTTP(t, kds, http.StatusOK, &dto)
	if len(dto.Orders) != 0 {
		t.Fatalf("accepted paid review order leaked into KDS: %s", kds.Body.String())
	}
	var cards, releases, outbox int
	if err = f.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_kds_cards WHERE order_id=$1), (SELECT count(*) FROM operation_order_events WHERE order_id=$1 AND event_type='KDS_RELEASED'), (SELECT count(*) FROM operation_outbox WHERE order_id=$1)`, orderID).Scan(&cards, &releases, &outbox); err != nil || cards != 0 || releases != 0 || outbox != 0 {
		t.Fatalf("review order was released: cards=%d releases=%d outbox=%d err=%v", cards, releases, outbox, err)
	}
}

func TestP84SharedOptionEditAndHeldLineRemoval(t *testing.T) {
	ctx, f := p84GuestFixture(t)
	grants := make([]tablesession.Grant, 3)
	for i := range grants {
		var err error
		grants[i], err = f.Sessions.DirectJoin(ctx, f.Locator, "")
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < i; j++ {
			if grants[i].Session.ID != grants[j].Session.ID || grants[i].Token == grants[j].Token {
				t.Fatal("guests must have distinct grants for one OPEN session")
			}
		}
	}
	sid := grants[0].Session.ID
	cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: sid, GrantToken: grants[0].Token, ExpectedGeneration: 1, ExpectedVersion: 1,
		IdempotencyKey: "p84-yuzu", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1, Options: []catalog.OptionSelection{{GroupKey: "aroma", OptionKey: "yuzu"}}})
	if err != nil || len(cart.Lines) != 1 || len(cart.Lines[0].Options) != 1 || cart.Lines[0].Options[0].OptionKey != "yuzu" {
		t.Fatalf("initial option not persisted: %+v err=%v", cart, err)
	}
	edit := ordering.Mutation{SessionID: sid, GrantToken: grants[1].Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "p84-ginger", Kind: ordering.SetLine, LineID: cart.Lines[0].LineID, Quantity: 2, Options: []catalog.OptionSelection{{GroupKey: "aroma", OptionKey: "ginger"}}}
	updated, err := f.Orders.Mutate(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.Orders.ReadCart(ctx, sid, grants[2].Token)
	if err != nil || current.Version != cart.Version+1 || len(current.Lines) != 1 || current.Lines[0].Quantity != 2 || len(current.Lines[0].Options) != 1 || current.Lines[0].Options[0].OptionKey != "ginger" || !reflect.DeepEqual(current, updated) {
		t.Fatalf("third guest lost shared option/quantity edit: %+v err=%v", current, err)
	}
	if replay, err := f.Orders.Mutate(ctx, edit); err != nil || !reflect.DeepEqual(replay, current) {
		t.Fatalf("option replay changed cart: %+v err=%v", replay, err)
	}
	stale := edit
	stale.GrantToken, stale.IdempotencyKey, stale.Quantity = grants[0].Token, "p84-stale-edit", 3
	if _, err = f.Orders.Mutate(ctx, stale); !errors.Is(err, ordering.ErrConflict) {
		t.Fatalf("stale shared edit accepted: %v", err)
	}
	if _, err = f.Menus.SetCompanyProductEmergencyHold(ctx, f.TenantID, "tea", "tenant-admin", true, "p84 safety review", 0, "p84-option-hold"); err != nil {
		t.Fatal(err)
	}
	held, err := f.Orders.ReadCart(ctx, sid, grants[2].Token)
	if err != nil || len(held.Lines) != 1 || held.Lines[0].ProductAvailable || held.Lines[0].UnavailableCode != "COMPANY_EMERGENCY_HOLD" {
		t.Fatalf("company hold did not retain blocked line: %+v err=%v", held, err)
	}
	removed, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: sid, GrantToken: grants[2].Token, ExpectedGeneration: held.Generation, ExpectedVersion: held.Version,
		IdempotencyKey: "p84-remove-held", Kind: ordering.RemoveLine, LineID: held.Lines[0].LineID})
	if err != nil || len(removed.Lines) != 0 || removed.Version != held.Version+1 {
		t.Fatalf("active company hold prevented line removal: %+v err=%v", removed, err)
	}
	if fresh, err := f.Orders.ReadCart(ctx, sid, grants[0].Token); err != nil || !reflect.DeepEqual(fresh, removed) {
		t.Fatalf("first guest did not observe held-line removal: %+v err=%v", fresh, err)
	}
}
