package httpapi

import (
	"context"
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

func TestPostgresUnpaidStripeVoidHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	orderID := createP82HTTPOrder(t, pf, grant.Session.ID, grant.Token, "tea", 1, ordering.Stripe, "void-http-stripe")
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("create Checkout worked=%v err=%v", worked, err)
	}
	var checkoutID string
	if err = f.Pool.QueryRow(ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&checkoutID); err != nil {
		t.Fatal(err)
	}
	if _, err = pf.provider.SetOutcome(checkoutID, stripeprovider.CheckoutExpired, stripeprovider.PaymentUnpaid); err != nil {
		t.Fatal(err)
	}

	path := pf.staffPath("table-01", grant.Session.ID, "/orders/"+orderID+"/payment/void-unpaid")
	order := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	version := int64JSON(order["version"])
	body := fmt.Sprintf(`{"expected_version":%d,"reason":"guest left without paying"}`, version)
	request := func(body, bearer, key string) *httptest.ResponseRecorder {
		t.Helper()
		return pf.requestWithKey(t, http.MethodPost, path, body, bearer, nil, "", "", key)
	}
	for _, denied := range []struct{ name, token string }{{"cashier", pf.creds.Cashier.Token}, {"kitchen", pf.creds.Kitchen.Token}, {"guest", grant.Token}, {"anonymous", ""}} {
		t.Run("denied to "+denied.name, func(t *testing.T) {
			w := request(body, denied.token, "deny-"+denied.name)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"error":"forbidden"`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	if err = pf.directory.Set(pf.creds.Cashier.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.StoreManager}}); err != nil {
		t.Fatal(err)
	}
	if w := request(body, pf.creds.Cashier.Token, "manager-without-mfa"); w.Code != http.StatusForbidden {
		t.Fatalf("manager role without verified MFA status=%d body=%s", w.Code, w.Body.String())
	}
	if err = pf.directory.Set(pf.creds.Cashier.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.Staff}}); err != nil {
		t.Fatal(err)
	}
	if w := request(body, pf.creds.Manager.Token, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"error":"invalid_request"`) {
		t.Fatalf("missing idempotency key status=%d body=%s", w.Code, w.Body.String())
	}
	staleBody := fmt.Sprintf(`{"expected_version":%d,"reason":"guest left without paying"}`, version+1)
	if w := request(staleBody, pf.creds.Manager.Token, "void-stale"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"error":"stale_version"`) {
		t.Fatalf("stale version status=%d body=%s", w.Code, w.Body.String())
	}

	voided := request(body, pf.creds.Manager.Token, "void-http-key")
	if voided.Code != http.StatusOK {
		t.Fatalf("manager void status=%d body=%s", voided.Code, voided.Body.String())
	}
	for _, private := range []string{"stripe_account_id", "checkout_session_id", "payment_intent_id", "acct_httpFixture", checkoutID, "provider_key", "credential_ref"} {
		if strings.Contains(voided.Body.String(), private) {
			t.Fatalf("void response leaked %q: %s", private, voided.Body.String())
		}
	}
	replay := request(body, pf.creds.Manager.Token, "void-http-key")
	if replay.Code != http.StatusOK || replay.Body.String() != voided.Body.String() {
		t.Fatalf("replay differs: original=%d %s replay=%d %s", voided.Code, voided.Body.String(), replay.Code, replay.Body.String())
	}
	changedReason := strings.Replace(body, "guest left without paying", "different reason", 1)
	if w := request(changedReason, pf.creds.Manager.Token, "void-http-key"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"error":"idempotency_conflict"`) {
		t.Fatalf("changed-reason replay status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(body, pf.creds.Manager.Token, "void-http-new-key"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"error":"void_not_eligible"`) {
		t.Fatalf("second void status=%d body=%s", w.Code, w.Body.String())
	}

	cashGrant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cashOrderID := createP82HTTPOrder(t, pf, cashGrant.Session.ID, cashGrant.Token, "tea", 1, ordering.Cash, "void-http-cash")
	cashPath := pf.staffPath("table-01", cashGrant.Session.ID, "/orders/"+cashOrderID+"/payment/void-unpaid")
	response := pf.requestWithKey(t, http.MethodPost, cashPath, `{"expected_version":1,"reason":"not a Stripe order"}`, pf.creds.Manager.Token, nil, "", "", "void-cash")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"error":"void_not_eligible"`) {
		t.Fatalf("cash refusal status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPostgresUnpaidStripeVoidHTTPRecoversExpiredSessionCreateFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	orderID := createP82HTTPOrder(t, pf, grant.Session.ID, grant.Token, "tea", 1, ordering.Stripe, "void-http-expired-create")
	// Move only this disposable fixture's immutable deadline to simulate expiry.
	if _, err = f.Pool.Exec(ctx, `ALTER TABLE table_sessions DISABLE TRIGGER table_session_snapshot_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()-interval '1 second',version=version+1 WHERE session_id=$1`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `ALTER TABLE table_sessions ENABLE TRIGGER table_session_snapshot_immutable`); err != nil {
		t.Fatal(err)
	}
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("failed CREATE worked=%v err=%v", worked, err)
	}
	var checkoutID, attemptState, workStatus, safeErrorCode string
	var attempts int
	if err = f.Pool.QueryRow(ctx, `SELECT coalesce(checkout_session_id,''),state FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&checkoutID, &attemptState); err != nil {
		t.Fatal(err)
	}
	if err = f.Pool.QueryRow(ctx, `SELECT status,attempts,coalesce(safe_error_code,'') FROM stripe_payment_work WHERE order_id=$1 AND work_type='CREATE'`, orderID).Scan(&workStatus, &attempts, &safeErrorCode); err != nil {
		t.Fatal(err)
	}
	if checkoutID != "" || attemptState != "FAILED" || workStatus != "DONE" || attempts != 1 || safeErrorCode != "SESSION_UNAVAILABLE" {
		t.Fatalf("CREATE did not fail without Checkout as expected: checkout=%q attempt=%q work=%q attempts=%d error=%q", checkoutID, attemptState, workStatus, attempts, safeErrorCode)
	}
	var eventKey string
	if err = f.Pool.QueryRow(ctx, `SELECT event_key FROM stripe_payment_attempt_events WHERE order_id=$1 AND event_key='WORK_STATE:FAILED:SESSION_UNAVAILABLE'`, orderID).Scan(&eventKey); err != nil {
		t.Fatalf("missing SESSION_UNAVAILABLE failure event: %v", err)
	}

	path := pf.staffPath("table-01", grant.Session.ID, "/orders/"+orderID+"/payment/void-unpaid")
	order := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	body := fmt.Sprintf(`{"expected_version":%d,"reason":"checkout creation failed after table session expiry"}`, int64JSON(order["version"]))
	request := func(body, bearer, key string) *httptest.ResponseRecorder {
		t.Helper()
		return pf.requestWithKey(t, http.MethodPost, path, body, bearer, nil, "", "", key)
	}
	if err = pf.directory.Set(pf.creds.Manager.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "other-store", Role: authz.StoreManager}}); err != nil {
		t.Fatal(err)
	}
	if denied := request(body, pf.creds.Manager.Token, "expired-create-cross-store"); denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), `"error":"forbidden"`) {
		t.Fatalf("cross-store manager status=%d body=%s", denied.Code, denied.Body.String())
	}
	if err = pf.directory.Set(pf.creds.Manager.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.StoreManager}}); err != nil {
		t.Fatal(err)
	}
	voided := request(body, pf.creds.Manager.Token, "expired-create-void")
	if voided.Code != http.StatusOK {
		t.Fatalf("manager void after failed CREATE status=%d body=%s", voided.Code, voided.Body.String())
	}
	replay := request(body, pf.creds.Manager.Token, "expired-create-void")
	if replay.Code != http.StatusOK || replay.Body.String() != voided.Body.String() {
		t.Fatalf("void replay differs: original=%d %s replay=%d %s", voided.Code, voided.Body.String(), replay.Code, replay.Body.String())
	}
	closed := pf.request(t, http.MethodPost, pf.staffPath("table-01", grant.Session.ID, "/close"), `{}`, pf.creds.Cashier.Token, nil, "", "")
	if closed.Code != http.StatusOK {
		t.Fatalf("close table after HTTP void status=%d body=%s", closed.Code, closed.Body.String())
	}
}
