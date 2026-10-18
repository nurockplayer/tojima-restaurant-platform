package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

func TestPostgresP82AdjustmentHTTPAuthorizationAndSafeProjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}

	orderID := createP82HTTPOrder(t, pf, grant.Session.ID, grant.Token, "tea", 2, ordering.Stripe, "p82-paid-stripe")
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("create checkout worked=%v err=%v", worked, err)
	}
	var checkoutID string
	if err = f.Pool.QueryRow(ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&checkoutID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE operation_order_state SET operational_hold=true,version=version+1 WHERE order_id=$1`, orderID); err != nil {
		t.Fatal(err)
	}
	paid, err := pf.provider.SetOutcome(checkoutID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pf.service.EnqueueVerifiedWebhook(ctx, stripeprovider.VerifiedEvent{ID: "evt_p82_adjust_http", Type: "checkout.session.completed", AccountID: paid.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: paid.SessionID, Checkout: &paid}); err != nil {
		t.Fatal(err)
	}
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("paid webhook worked=%v err=%v", worked, err)
	}

	order := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	version := int64JSON(order["version"])
	path := pf.staffPath("table-01", grant.Session.ID, "/orders/"+orderID)
	previewPath := path + "/adjustments/preview"
	adjustPath := path + "/adjustments"
	previewBody := fmt.Sprintf(`{"expected_version":%d,"reason":"customer correction","lines":[{"line_index":0,"cancelled_quantity":1}],"cash_hand_back_confirmed":false}`, version)
	request := func(method, p, body, bearer, key string) *httptest.ResponseRecorder {
		t.Helper()
		return pf.requestWithKey(t, method, p, body, bearer, nil, "", "", key)
	}
	managerPreview := func() *httptest.ResponseRecorder {
		return request(http.MethodPost, previewPath, previewBody, pf.creds.Manager.Token, "")
	}

	// Each role/scope denial goes through the actual identity and membership adapters.
	for _, denied := range []struct{ name, token string }{
		{"cashier", pf.creds.Cashier.Token}, {"kitchen", pf.creds.Kitchen.Token}, {"guest capability", grant.Token}, {"anonymous", ""},
	} {
		t.Run("preview denied to "+denied.name, func(t *testing.T) {
			w := request(http.MethodPost, previewPath, previewBody, denied.token, "")
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"error":"forbidden"`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	setMembership := func(subject string, memberships ...authz.Membership) {
		t.Helper()
		if err := pf.directory.Set(subject, memberships); err != nil {
			t.Fatal(err)
		}
	}
	// A staff identity promoted to manager still has no verified MFA claim.
	setMembership(pf.creds.Cashier.SubjectID, authz.Membership{TenantID: "test-tenant", StoreID: "test-store", Role: authz.StoreManager})
	if w := request(http.MethodPost, previewPath, previewBody, pf.creds.Cashier.Token, ""); w.Code != http.StatusForbidden {
		t.Fatalf("manager role without MFA status=%d body=%s", w.Code, w.Body.String())
	}
	setMembership(pf.creds.Cashier.SubjectID, authz.Membership{TenantID: "test-tenant", StoreID: "test-store", Role: authz.Staff})

	for _, membership := range []authz.Membership{
		{TenantID: "test-tenant", Role: authz.TenantAdmin},
		{TenantID: "test-tenant", StoreID: "other-store", Role: authz.StoreManager},
		{TenantID: "other-tenant", StoreID: "test-store", Role: authz.StoreManager},
	} {
		setMembership(pf.creds.Manager.SubjectID, membership)
		w := managerPreview()
		if w.Code != http.StatusForbidden {
			t.Fatalf("out-of-scope membership %+v status=%d body=%s", membership, w.Code, w.Body.String())
		}
	}
	pf.directory.Remove(pf.creds.Manager.SubjectID)
	if w := managerPreview(); w.Code != http.StatusForbidden {
		t.Fatalf("revoked manager status=%d body=%s", w.Code, w.Body.String())
	}
	setMembership(pf.creds.Manager.SubjectID, authz.Membership{TenantID: "test-tenant", StoreID: "test-store", Role: authz.StoreManager})

	if w := request(http.MethodPost, adjustPath, previewBody, pf.creds.Manager.Token, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"error":"invalid_request"`) {
		t.Fatalf("missing idempotency key status=%d body=%s", w.Code, w.Body.String())
	}
	preview := managerPreview()
	if preview.Code != http.StatusOK {
		t.Fatalf("manager preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	var quote map[string]any
	if err = json.Unmarshal(preview.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	if quote["original_total_gross_jpy"] != "660" || quote["effective_total_gross_jpy"] != "330" || quote["refund_amount_jpy"] != "330" {
		t.Fatalf("unexpected adjustment preview: %s", preview.Body.String())
	}
	assertP82SafeStaffProjection(t, preview.Body.String())

	adjusted := request(http.MethodPost, adjustPath, previewBody, pf.creds.Manager.Token, "p82-adjust-http")
	if adjusted.Code != http.StatusOK {
		t.Fatalf("manager adjustment status=%d body=%s", adjusted.Code, adjusted.Body.String())
	}
	if !strings.Contains(adjusted.Body.String(), `"effective_total_gross_jpy":"330"`) || !strings.Contains(adjusted.Body.String(), `"status":"REQUESTED"`) {
		t.Fatalf("adjustment omitted effective amount or safe refund state: %s", adjusted.Body.String())
	}
	assertP82SafeStaffProjection(t, adjusted.Body.String())
	replay := request(http.MethodPost, adjustPath, previewBody, pf.creds.Manager.Token, "p82-adjust-http")
	if replay.Code != http.StatusOK || replay.Body.String() != adjusted.Body.String() {
		t.Fatalf("adjustment replay differs: status=%d body=%s original=%s", replay.Code, replay.Body.String(), adjusted.Body.String())
	}
	conflictBody := strings.Replace(previewBody, "customer correction", "different correction", 1)
	if w := request(http.MethodPost, adjustPath, conflictBody, pf.creds.Manager.Token, "p82-adjust-http"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"error":"idempotency_conflict"`) {
		t.Fatalf("idempotency conflict status=%d body=%s", w.Code, w.Body.String())
	}

	var refundID string
	if err = f.Pool.QueryRow(ctx, `SELECT refund_id FROM operation_refunds WHERE order_id=$1`, orderID).Scan(&refundID); err != nil {
		t.Fatal(err)
	}
	pf.provider.SetNextRefundOutcome("failed", false)
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("refund failure work=%v err=%v", worked, err)
	}
	state := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	retryPath := path + "/refunds/" + refundID + "/retry"
	retryBody := fmt.Sprintf(`{"expected_version":%d}`, int64JSON(state["version"]))
	for _, denied := range []struct{ name, token string }{{"cashier", pf.creds.Cashier.Token}, {"kitchen", pf.creds.Kitchen.Token}, {"guest", grant.Token}, {"anonymous", ""}} {
		w := request(http.MethodPost, retryPath, retryBody, denied.token, "retry-denied")
		if w.Code != http.StatusForbidden {
			t.Fatalf("retry allowed for %s: %d %s", denied.name, w.Code, w.Body.String())
		}
	}
	setMembership(pf.creds.Manager.SubjectID, authz.Membership{TenantID: "test-tenant", StoreID: "test-store", Role: authz.TenantAdmin})
	if w := request(http.MethodPost, retryPath, retryBody, pf.creds.Manager.Token, "retry-denied"); w.Code != http.StatusForbidden {
		t.Fatalf("tenant admin retried refund: %d %s", w.Code, w.Body.String())
	}
	setMembership(pf.creds.Manager.SubjectID, authz.Membership{TenantID: "test-tenant", StoreID: "test-store", Role: authz.StoreManager})
	retried := request(http.MethodPost, retryPath, retryBody, pf.creds.Manager.Token, "p82-refund-retry")
	if retried.Code != http.StatusOK || !strings.Contains(retried.Body.String(), `"status":"REQUESTED"`) {
		t.Fatalf("manager retry status=%d body=%s", retried.Code, retried.Body.String())
	}
	assertP82SafeStaffProjection(t, retried.Body.String())

	// A still-active Stripe checkout is stablely rejected by the preview route.
	unresolvedID := createP82HTTPOrder(t, pf, grant.Session.ID, grant.Token, "tea", 1, ordering.Stripe, "p82-unresolved")
	unresolved := request(http.MethodPost, pf.staffPath("table-01", grant.Session.ID, "/orders/"+unresolvedID+"/adjustments/preview"), `{"expected_version":1,"reason":"pending payment","lines":[{"line_index":0,"cancelled_quantity":1}]}`, pf.creds.Manager.Token, "")
	if unresolved.Code != http.StatusConflict || !strings.Contains(unresolved.Body.String(), `"error":"payment_unresolved"`) {
		t.Fatalf("unresolved Stripe preview status=%d body=%s", unresolved.Code, unresolved.Body.String())
	}
}

func TestPostgresP82AdjustmentHTTPKitchenAcknowledgementAndPreparationGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	orderID := createP82HTTPOrder(t, pf, grant.Session.ID, grant.Token, "tea", 2, ordering.Cash, "p82-cash-kds")
	path := pf.staffPath("table-01", grant.Session.ID, "/orders/"+orderID)
	order := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	acceptBody := fmt.Sprintf(`{"expected_version":%d}`, int64JSON(order["version"]))
	accepted := pf.requestWithKey(t, http.MethodPost, path+"/accept", acceptBody, pf.creds.Cashier.Token, nil, "", "", "p82-accept-kds")
	if accepted.Code != http.StatusOK {
		t.Fatalf("cash order accept status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	order = readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	adjustBody := fmt.Sprintf(`{"expected_version":%d,"reason":"customer correction","lines":[{"line_index":0,"cancelled_quantity":1}],"cash_hand_back_confirmed":false}`, int64JSON(order["version"]))
	// PAY_LATER cash enters ACCEPTED with one KDS card; C1 still reduces the due without a refund.
	adjusted := pf.requestWithKey(t, http.MethodPost, path+"/adjustments", adjustBody, pf.creds.Manager.Token, nil, "", "", "p82-cash-adjust")
	if adjusted.Code != http.StatusOK {
		t.Fatalf("cash adjustment status=%d body=%s", adjusted.Code, adjusted.Body.String())
	}
	if strings.Contains(adjusted.Body.String(), `"refunds":[{`) || !strings.Contains(adjusted.Body.String(), `"effective_total_gross_jpy":"330"`) || !strings.Contains(adjusted.Body.String(), `"order_updated":true`) {
		t.Fatalf("unpaid cash adjustment did not reduce due/update KDS safely: %s", adjusted.Body.String())
	}
	assertP82SafeStaffProjection(t, adjusted.Body.String())

	ackPath := path + "/acknowledge-update"
	order = readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	ackBody := fmt.Sprintf(`{"expected_version":%d}`, int64JSON(order["version"]))
	for _, denied := range []struct{ name, token string }{{"manager", pf.creds.Manager.Token}, {"cashier", pf.creds.Cashier.Token}, {"guest", grant.Token}, {"anonymous", ""}} {
		w := pf.requestWithKey(t, http.MethodPost, ackPath, ackBody, denied.token, nil, "", "", "ack-denied")
		if w.Code != http.StatusForbidden {
			t.Fatalf("ack allowed for %s: %d %s", denied.name, w.Code, w.Body.String())
		}
	}
	ack := pf.requestWithKey(t, http.MethodPost, ackPath, ackBody, pf.creds.Kitchen.Token, nil, "", "", "ack-kitchen")
	if ack.Code != http.StatusOK || !strings.Contains(ack.Body.String(), `"order_updated":false`) {
		t.Fatalf("kitchen ack status=%d body=%s", ack.Code, ack.Body.String())
	}
	assertP82SafeStaffProjection(t, ack.Body.String())
	var afterAck map[string]any
	if err = json.Unmarshal(ack.Body.Bytes(), &afterAck); err != nil {
		t.Fatal(err)
	}
	prep := fmt.Sprintf(`{"expected_version":%d,"target":"PREPARING"}`, int64JSON(afterAck["version"]))
	started := pf.requestWithKey(t, http.MethodPost, path+"/fulfillment", prep, pf.creds.Kitchen.Token, nil, "", "", "p82-start")
	if started.Code != http.StatusOK {
		t.Fatalf("kitchen start status=%d body=%s", started.Code, started.Body.String())
	}
	preview := pf.request(t, http.MethodPost, path+"/adjustments/preview", fmt.Sprintf(`{"expected_version":%d,"reason":"too late","lines":[{"line_index":0,"cancelled_quantity":1}]}`, int64JSON(afterAck["version"])+1), pf.creds.Manager.Token, nil, "", "")
	if preview.Code != http.StatusConflict || !strings.Contains(preview.Body.String(), `"error":"preparation_started"`) {
		t.Fatalf("preparing adjustment status=%d body=%s", preview.Code, preview.Body.String())
	}
}

func createP82HTTPOrder(t *testing.T, pf *paymentHTTPFixture, sessionID, guestToken, productKey string, quantity int, method ordering.PaymentMethod, key string) string {
	t.Helper()
	guestBase := "/v1/guest/sessions/" + sessionID
	cartResponse := pf.request(t, http.MethodGet, guestBase+"/cart", "", guestToken, nil, "", "")
	var cart map[string]any
	decodePaymentHTTP(t, cartResponse, http.StatusOK, &cart)
	addBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"product_key":%q,"quantity":%d,"options":[]}`, int64JSON(cart["generation"]), int64JSON(cart["version"]), productKey, quantity)
	added := pf.requestWithKey(t, http.MethodPost, guestBase+"/cart/lines", addBody, guestToken, nil, "", "", key+"-line")
	var updated map[string]any
	decodePaymentHTTP(t, added, http.StatusOK, &updated)
	previewBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d}`, int64JSON(updated["generation"]), int64JSON(updated["version"]))
	quoted := pf.request(t, http.MethodPost, guestBase+"/cart/preview", previewBody, guestToken, nil, "", "")
	var quote map[string]any
	decodePaymentHTTP(t, quoted, http.StatusOK, &quote)
	commitBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"preview_fingerprint":%q,"payment_method":%q}`, int64JSON(updated["generation"]), int64JSON(updated["version"]), quote["fingerprint"], string(method))
	var cookie *http.Cookie
	origin := ""
	if method == ordering.Stripe {
		origin = paymentHTTPOrigin
		proof := pf.request(t, http.MethodPost, guestBase+"/checkout-context", "", guestToken, nil, paymentHTTPOrigin, "")
		if proof.Code != http.StatusNoContent || len(proof.Result().Cookies()) != 1 {
			t.Fatalf("establish checkout context status=%d cookies=%d body=%s", proof.Code, len(proof.Result().Cookies()), proof.Body.String())
		}
		cookie = proof.Result().Cookies()[0]
	}
	committed := pf.requestWithKey(t, http.MethodPost, guestBase+"/cart/commit", commitBody, guestToken, cookie, origin, "", key+"-commit")
	var result map[string]any
	decodePaymentHTTP(t, committed, http.StatusOK, &result)
	order, ok := result["order"].(map[string]any)
	if !ok {
		t.Fatalf("commit missing order: %s", committed.Body.String())
	}
	id, ok := order["order_id"].(string)
	if !ok || id == "" {
		t.Fatalf("commit missing order id: %s", committed.Body.String())
	}
	return id
}

func readP82HTTPOrder(t *testing.T, pf *paymentHTTPFixture, sessionID, orderID string) map[string]any {
	t.Helper()
	path := pf.staffPath("table-01", sessionID, "/operations")
	response := pf.request(t, http.MethodGet, path, "", pf.creds.Cashier.Token, nil, "", "")
	var session map[string]any
	decodePaymentHTTP(t, response, http.StatusOK, &session)
	orders, ok := session["orders"].([]any)
	if !ok {
		t.Fatalf("session projection has no orders array: %s", response.Body.String())
	}
	for _, raw := range orders {
		order, ok := raw.(map[string]any)
		if ok && order["id"] == orderID {
			return order
		}
	}
	t.Fatalf("order %s missing from session projection: %s", orderID, response.Body.String())
	return nil
}

func assertP82SafeStaffProjection(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"stripe_account_id", "checkout_session_id", "payment_intent_id", "provider_refund_id", "provider_key", "credential_ref", "webhook_secret", "checkout_proof_hash"} {
		if strings.Contains(body, secret) {
			t.Fatalf("staff projection leaked %q: %s", secret, body)
		}
	}
}
