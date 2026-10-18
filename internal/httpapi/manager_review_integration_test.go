package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

func TestPostgresManagerReviewHTTPAuthorizationAndProjection(t *testing.T) {
	ctx := context.Background()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	directory, creds := pf.directory, pf.creds
	h := pf.handler
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: "review-line", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	quote, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "review-order", PreviewFingerprint: quote.Fingerprint, PaymentMethod: ordering.Stripe, CheckoutProofHash: sha256.Sum256([]byte("http-review-proof")), CheckoutProofExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("create checkout: %v %v", worked, err)
	}
	var checkoutID string
	if err = f.Pool.QueryRow(ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, committed.Order.ID).Scan(&checkoutID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE operation_order_state SET operational_hold=true,version=version+1 WHERE order_id=$1`, committed.Order.ID); err != nil {
		t.Fatal(err)
	}
	paid, err := pf.provider.SetOutcome(checkoutID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pf.service.EnqueueVerifiedWebhook(ctx, stripeprovider.VerifiedEvent{ID: "evt_manager_review_http", Type: "checkout.session.completed", AccountID: paid.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: paid.SessionID, Checkout: &paid}); err != nil {
		t.Fatal(err)
	}
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("paid webhook: %v %v", worked, err)
	}
	base := fmt.Sprintf("/v1/staff/tenants/%s/stores/%s", f.TenantID, f.StoreID)
	path := fmt.Sprintf("%s/tables/%s/sessions/%s/orders/%s/approve-fulfillment", base, grant.Session.Scope.TableID, grant.Session.ID, committed.Order.ID)
	body := `{"expected_version":3,"reason":"kitchen confirmed fulfillment"}`
	request := func(method, path, body, token, key string, want int, code string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", paymentHTTPOrigin)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: HTTP %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		if code != "" && !strings.Contains(w.Body.String(), `"error":"`+code+`"`) {
			t.Fatalf("wanted error %s: %s", code, w.Body.String())
		}
		return w
	}
	setMembership := func(subject, tenant, store string, role authz.Role) {
		t.Helper()
		if err := directory.Set(subject, []authz.Membership{{TenantID: tenant, StoreID: store, Role: role}}); err != nil {
			t.Fatal(err)
		}
	}
	for name, token := range map[string]string{"staff": creds.Cashier.Token, "kitchen": creds.Kitchen.Token, "guest": grant.Token, "anonymous": ""} {
		t.Run(name, func(t *testing.T) { request("POST", path, body, token, "approve", 403, "forbidden") })
	}
	// A valid token with a current manager membership still needs verified MFA.
	setMembership(creds.Cashier.SubjectID, f.TenantID, f.StoreID, authz.StoreManager)
	request("POST", path, body, creds.Cashier.Token, "approve", 403, "forbidden")
	setMembership(creds.Cashier.SubjectID, f.TenantID, f.StoreID, authz.Staff)
	for _, membership := range []authz.Membership{
		{TenantID: f.TenantID, Role: authz.TenantAdmin},
		{TenantID: f.TenantID, StoreID: "other-store", Role: authz.StoreManager},
		{TenantID: "other-tenant", StoreID: f.StoreID, Role: authz.StoreManager},
		{TenantID: f.TenantID, Role: authz.StoreManager},
	} {
		setMembership(creds.Manager.SubjectID, membership.TenantID, membership.StoreID, membership.Role)
		request("POST", path, body, creds.Manager.Token, "approve", 403, "forbidden")
	}
	directory.Remove(creds.Manager.SubjectID)
	request("POST", path, body, creds.Manager.Token, "approve", 403, "forbidden")
	setMembership(creds.Manager.SubjectID, f.TenantID, f.StoreID, authz.StoreManager)
	caps := request("GET", base+"/operations/access", "", creds.Manager.Token, "", 200, "")
	if !strings.Contains(caps.Body.String(), `"can_approve_fulfillment":true`) {
		t.Fatal(caps.Body.String())
	}
	queue := request("GET", base+"/operations/reviews", "", creds.Cashier.Token, "", 200, "")
	var raw struct {
		Orders []map[string]json.RawMessage `json:"orders"`
	}
	if err = json.Unmarshal(queue.Body.Bytes(), &raw); err != nil || len(raw.Orders) != 1 {
		t.Fatalf("queue %s: %v", queue.Body.String(), err)
	}
	if string(raw.Orders[0]["total_gross_jpy"]) != `"660"` || !strings.Contains(string(raw.Orders[0]["lines"]), `"product_name"`) || !strings.Contains(string(raw.Orders[0]["review_reason_codes"]), "OPERATIONAL_HOLD") {
		t.Fatal(queue.Body.String())
	}
	for _, private := range []string{"stripe_account_id", "checkout_session_id", "payment_intent_id", "credential_ref", "actor_id"} {
		if strings.Contains(queue.Body.String(), private) {
			t.Fatalf("queue leaked %s", private)
		}
	}
	request("POST", path, `{"expected_version":3,"reason":" "}`, creds.Manager.Token, "invalid", 400, "invalid_request")
	request("POST", path, body, creds.Manager.Token, "", 400, "invalid_request")
	request("POST", path, `{"expected_version":1,"reason":"old consent"}`, creds.Manager.Token, "stale", 409, "stale_version")
	request("POST", path, `{"expected_version":3,"reason":"ok","actor":"forged"}`, creds.Manager.Token, "forged", 400, "invalid_request")
	approved := request("POST", path, body, creds.Manager.Token, "approve", 200, "")
	if !strings.Contains(approved.Body.String(), `"review_decision":"APPROVED"`) || !strings.Contains(approved.Body.String(), `"payment_review_required":false`) {
		t.Fatal(approved.Body.String())
	}
	replay := request("POST", path, body, creds.Manager.Token, "approve", 200, "")
	if approved.Body.String() != replay.Body.String() {
		t.Fatalf("original response changed on replay\n%s\n%s", approved.Body.String(), replay.Body.String())
	}
	request("POST", path, strings.Replace(body, "kitchen confirmed fulfillment", "changed reason", 1), creds.Manager.Token, "approve", 409, "idempotency_conflict")
	// Replay does not bypass a fresh current-membership check.
	directory.Remove(creds.Manager.SubjectID)
	request("POST", path, body, creds.Manager.Token, "approve", 403, "forbidden")
	queue = request("GET", base+"/operations/reviews", "", creds.Cashier.Token, "", 200, "")
	if !strings.Contains(queue.Body.String(), `"orders":[]`) {
		t.Fatal(queue.Body.String())
	}
	var actor string
	if err = f.Pool.QueryRow(ctx, `SELECT actor_id FROM operation_order_events WHERE order_id=$1 AND event_type='MANAGER_REVIEW_APPROVED'`, committed.Order.ID).Scan(&actor); err != nil || actor != creds.Manager.SubjectID {
		t.Fatalf("audit actor=%q err=%v", actor, err)
	}
}
