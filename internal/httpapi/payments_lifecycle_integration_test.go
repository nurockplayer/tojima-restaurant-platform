package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

const (
	paymentHTTPOrigin        = "http://127.0.0.1:8082"
	paymentHTTPSecret        = "whsec_http_fixture_only"
	paymentHTTPCredentialRef = "fixture:http-integration:v1"
)

type paymentHTTPResolver map[string]payments.ResolvedProvider

func (r paymentHTTPResolver) Resolve(_ context.Context, key string) (payments.ResolvedProvider, error) {
	value, ok := r[key]
	if !ok {
		return payments.ResolvedProvider{}, fmt.Errorf("unknown test provider reference")
	}
	return value, nil
}

type paymentHTTPFixture struct {
	fixture   *testguest.Fixture
	service   *payments.Service
	provider  *testpayments.Provider
	identity  *testoperations.IdentityResolver
	directory *testoperations.MembershipDirectory
	creds     testoperations.CredentialSet
	handler   http.Handler
	account   stripeprovider.AccountContext
}

func newPaymentHTTPFixture(t *testing.T) *paymentHTTPFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close(context.Background()) })

	provider := testpayments.NewProvider()
	account := stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_httpFixture"}
	resolver := paymentHTTPResolver{paymentHTTPCredentialRef: {
		Provider: provider, Account: account,
		ProfileFingerprint: sha256.Sum256([]byte(stripeprovider.APIVersion + "|http-test-profile")),
	}}
	service, err := payments.New(f.Pool, f.Operations, resolver, payments.Config{
		CreateRetryWindow: 20 * time.Hour, ContinuationTTL: 30 * time.Minute, WorkLeaseTTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SetMerchantConfig(ctx, operations.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}, 0, "http-fixture", payments.MerchantConfigInput{
		Enabled: true, Mode: account.Mode, StripeAccountID: account.StripeAccountID,
		CredentialRef: paymentHTTPCredentialRef, WebhookEndpointRef: "fixture-endpoint", APIVersion: stripeprovider.APIVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	f.Sessions, err = tablesession.New(f.Pool, tablesession.Options{
		SessionTTL: 4 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 1000,
		PINTTL: 15 * time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: pepper,
	}, service)
	if err != nil {
		t.Fatal(err)
	}
	f.Orders, err = ordering.NewWithOrderPolicy(f.Pool, f.Sessions, f.Menus, service)
	if err != nil {
		t.Fatal(err)
	}
	signingKey := make([]byte, 32)
	if _, err = rand.Read(signingKey); err != nil {
		t.Fatal(err)
	}
	proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: [][]byte{signingKey}, TTL: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	identity, directory, creds, err := testoperations.NewCredentialSet("test-tenant", "test-store")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewPaymentsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, service, identity, authz.PolicyAuthorizer{Directory: directory}, PaymentsHTTPConfig{
		Proofs: proofs, Origin: paymentHTTPOrigin, AllowLoopbackHTTP: true, WebhookSecrets: []string{paymentHTTPSecret},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &paymentHTTPFixture{fixture: f, service: service, provider: provider, identity: identity, directory: directory, creds: creds, handler: handler, account: account}
}

func (f *paymentHTTPFixture) request(t *testing.T, method, path, body, bearer string, cookie *http.Cookie, origin, host string) *httptest.ResponseRecorder {
	return f.requestWithKey(t, method, path, body, bearer, cookie, origin, host, "")
}

func (f *paymentHTTPFixture) requestWithKey(t *testing.T, method, path, body, bearer string, cookie *http.Cookie, origin, host, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	if host == "" {
		host = "127.0.0.1:8082"
	}
	url := paymentHTTPOrigin + path
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	r.Host = host
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if idempotencyKey != "" {
		r.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f *paymentHTTPFixture) staffPath(table, session, suffix string) string {
	return "/v1/staff/tenants/test-tenant/stores/test-store/tables/" + table + "/sessions/" + session + suffix
}

func TestPaymentsHTTPProofWebhookAndStaffLifecyclePostgres(t *testing.T) {
	h := newPaymentHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	f := h.fixture
	first, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Sessions.DirectJoin(ctx, f.Locator, first.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	guestBase := "/v1/guest/sessions/" + first.Session.ID

	getCart := h.request(t, http.MethodGet, guestBase+"/cart", "", first.Token, nil, "", "")
	var cart map[string]any
	decodePaymentHTTP(t, getCart, http.StatusOK, &cart)
	addBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"product_key":"tea","quantity":1,"options":[]}`, int64JSON(cart["generation"]), int64JSON(cart["version"]))
	added := h.requestWithKey(t, http.MethodPost, guestBase+"/cart/lines", addBody, first.Token, nil, "", "", "add-tea")
	var addedCart map[string]any
	decodePaymentHTTP(t, added, http.StatusOK, &addedCart)
	previewBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d}`, int64JSON(addedCart["generation"]), int64JSON(addedCart["version"]))
	preview := h.request(t, http.MethodPost, guestBase+"/cart/preview", previewBody, first.Token, nil, "", "")
	var previewResult map[string]any
	decodePaymentHTTP(t, preview, http.StatusOK, &previewResult)
	quote := previewResult["quote"].(map[string]any)
	tax := quote["tax"].(map[string]any)
	if tax["total_gross_jpy"] != "330" {
		t.Fatalf("unexpected fixture quote: %#v", tax)
	}
	commitBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"preview_fingerprint":%q,"payment_method":"STRIPE"}`,
		int64JSON(addedCart["generation"]), int64JSON(addedCart["version"]), previewResult["fingerprint"])
	withoutProof := h.requestWithKey(t, http.MethodPost, guestBase+"/cart/commit", commitBody, first.Token, nil, paymentHTTPOrigin, "", "commit-stripe")
	if withoutProof.Code != http.StatusForbidden {
		t.Fatalf("Stripe commit without established browser proof status=%d body=%s", withoutProof.Code, withoutProof.Body.String())
	}
	if got := h.request(t, http.MethodPost, guestBase+"/checkout-context", "", first.Token, nil, "https://attacker.invalid", ""); got.Code != http.StatusForbidden {
		t.Fatalf("foreign checkout-context Origin status=%d body=%s", got.Code, got.Body.String())
	}
	if got := h.request(t, http.MethodPost, guestBase+"/checkout-context", "", first.Token, nil, paymentHTTPOrigin, "attacker.invalid"); got.Code != http.StatusForbidden {
		t.Fatalf("foreign Host with configured Origin status=%d body=%s", got.Code, got.Body.String())
	}
	contextResponse := h.request(t, http.MethodPost, guestBase+"/checkout-context", "", first.Token, nil, paymentHTTPOrigin, "")
	if contextResponse.Code != http.StatusNoContent {
		t.Fatalf("checkout-context status=%d body=%s", contextResponse.Code, contextResponse.Body.String())
	}
	cookies := contextResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("checkout-context returned %d cookies", len(cookies))
	}
	firstCookie := cookies[0]
	if firstCookie.Name != fixtureCheckoutProofCookieName || firstCookie.Secure || !firstCookie.HttpOnly || firstCookie.SameSite != http.SameSiteStrictMode || firstCookie.Path != "/" || firstCookie.Domain != "" || !firstCookie.Expires.After(time.Now()) {
		t.Fatalf("unsafe or unexpected fixture proof cookie: %+v", firstCookie)
	}
	if contextResponse.Body.Len() != 0 {
		t.Fatalf("checkout-context returned proof material in body: %s", contextResponse.Body.String())
	}
	// Repeating context setup preserves the current browser proof rather than
	// silently replacing the capability that may bind the next winning attempt.
	contextAgain := h.request(t, http.MethodPost, guestBase+"/checkout-context", "", first.Token, firstCookie, paymentHTTPOrigin, "")
	if contextAgain.Code != http.StatusNoContent || len(contextAgain.Result().Cookies()) != 0 {
		t.Fatalf("valid proof was unexpectedly rotated: status=%d cookies=%v", contextAgain.Code, contextAgain.Result().Cookies())
	}

	committed := h.requestWithKey(t, http.MethodPost, guestBase+"/cart/commit", commitBody, first.Token, firstCookie, paymentHTTPOrigin, "", "commit-stripe")
	var commitResult map[string]any
	decodePaymentHTTP(t, committed, http.StatusOK, &commitResult)
	order := commitResult["order"].(map[string]any)
	orderID := order["order_id"].(string)
	for _, response := range []string{committed.Body.String(), preview.Body.String()} {
		assertPaymentResponsePrivate(t, response, "https://checkout.example.invalid", "checkout_session_id", "payment_intent_id", "acct_httpFixture", "checkout_proof_hash")
	}

	otherContext := h.request(t, http.MethodPost, guestBase+"/checkout-context", "", second.Token, nil, paymentHTTPOrigin, "")
	if otherContext.Code != http.StatusNoContent || len(otherContext.Result().Cookies()) != 1 {
		t.Fatalf("second guest could not establish its own context: %d %s", otherContext.Code, otherContext.Body.String())
	}
	secondCookie := otherContext.Result().Cookies()[0]
	otherStatus := h.request(t, http.MethodGet, guestBase+"/orders/"+orderID+"/checkout", "", second.Token, secondCookie, "", "")
	var otherStatusDTO map[string]any
	decodePaymentHTTP(t, otherStatus, http.StatusOK, &otherStatusDTO)
	if otherStatusDTO["resume_available"] != false {
		t.Fatalf("second guest inherited first guest resume capability: %v", otherStatusDTO)
	}
	otherResume := h.request(t, http.MethodPost, guestBase+"/orders/"+orderID+"/checkout/resume", "", "", secondCookie, paymentHTTPOrigin, "")
	if otherResume.Code != http.StatusConflict && otherResume.Code != http.StatusForbidden {
		t.Fatalf("second guest resumed first checkout: %d %s", otherResume.Code, otherResume.Body.String())
	}

	if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("checkout creation worker worked=%v err=%v", worked, err)
	}
	var checkoutID string
	if err = f.Pool.QueryRow(ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&checkoutID); err != nil || checkoutID == "" {
		t.Fatalf("checkout session missing after worker: id=%q err=%v", checkoutID, err)
	}
	status := h.request(t, http.MethodGet, guestBase+"/orders/"+orderID+"/checkout", "", first.Token, firstCookie, "", "")
	var statusDTO map[string]any
	decodePaymentHTTP(t, status, http.StatusOK, &statusDTO)
	if statusDTO["resume_available"] != true || statusDTO["payment_review_required"] != false {
		t.Fatalf("original guest status did not expose safe resume flag: %v", statusDTO)
	}
	assertPaymentResponsePrivate(t, status.Body.String(), "https://checkout.example.invalid", "checkout_session_id", "payment_intent_id", "acct_httpFixture", "checkout_proof_hash")
	resume := h.request(t, http.MethodPost, guestBase+"/orders/"+orderID+"/checkout/resume", "", "", firstCookie, paymentHTTPOrigin, "")
	var privateCheckout map[string]any
	decodePaymentHTTP(t, resume, http.StatusOK, &privateCheckout)
	if !strings.HasPrefix(privateCheckout["url"].(string), "https://checkout.example.invalid/") {
		t.Fatalf("resume missing private checkout URL: %v", privateCheckout)
	}
	assertPaymentResponsePrivate(t, resume.Body.String(), "acct_httpFixture", "checkout_session_id", "payment_intent_id", "checkout_proof_hash")

	tablePath := h.staffPath("table-01", first.Session.ID, "/orders/"+orderID+"/payment")
	for _, credential := range []struct {
		name  string
		token string
		want  int
	}{{"cashier", h.creds.Cashier.Token, http.StatusOK}, {"MFA manager", h.creds.Manager.Token, http.StatusOK}, {"kitchen", h.creds.Kitchen.Token, http.StatusForbidden}} {
		t.Run("staff status "+credential.name, func(t *testing.T) {
			response := h.request(t, http.MethodGet, tablePath, "", credential.token, nil, "", "")
			if response.Code != credential.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, credential.want, response.Body.String())
			}
			if credential.want == http.StatusOK {
				assertPaymentResponsePrivate(t, response.Body.String(), "https://checkout.example.invalid", "checkout_session_id", "payment_intent_id", "acct_httpFixture", "checkout_proof_hash")
			}
		})
	}
	if err = h.directory.Set(h.creds.Kitchen.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.TenantAdmin}}); err != nil {
		t.Fatal(err)
	}
	adminOnly := h.request(t, http.MethodGet, tablePath, "", h.creds.Kitchen.Token, nil, "", "")
	if adminOnly.Code != http.StatusForbidden {
		t.Fatalf("tenant-admin-only membership could read payment: %d %s", adminOnly.Code, adminOnly.Body.String())
	}
	if err = h.directory.Set(h.creds.Kitchen.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.Kitchen}}); err != nil {
		t.Fatal(err)
	}

	paidCheckout, err := h.provider.SetOutcome(checkoutID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	paidBody, paidSignature, err := testpayments.SignedCheckoutEvent(paidCheckout, "evt_http_paid_1", "checkout.session.completed", paymentHTTPSecret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), paidBody...)
	tampered[len(tampered)-2] ^= 1
	tamperResponse := h.webhook(t, tampered, paidSignature)
	if tamperResponse.Code != http.StatusBadRequest {
		t.Fatalf("tampered webhook status=%d body=%s", tamperResponse.Code, tamperResponse.Body.String())
	}
	queued := h.webhook(t, paidBody, paidSignature)
	if queued.Code != http.StatusOK {
		t.Fatalf("signed webhook was not acknowledged: %d %s", queued.Code, queued.Body.String())
	}
	var queuedCount int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM stripe_webhook_events WHERE stripe_event_id='evt_http_paid_1' AND processed_at IS NULL`).Scan(&queuedCount); err != nil || queuedCount != 1 {
		t.Fatalf("signed event not durably queued before ack: rows=%d err=%v", queuedCount, err)
	}
	if duplicate := h.webhook(t, paidBody, paidSignature); duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate verified webhook not idempotently acknowledged: %d %s", duplicate.Code, duplicate.Body.String())
	}
	var duplicateCount int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM stripe_webhook_events WHERE stripe_event_id='evt_http_paid_1'`).Scan(&duplicateCount); err != nil || duplicateCount != 1 {
		t.Fatalf("duplicate webhook created extra durable events: rows=%d err=%v", duplicateCount, err)
	}
	if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("webhook worker worked=%v err=%v", worked, err)
	}
	var attemptState string
	if err = f.Pool.QueryRow(ctx, `SELECT state FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&attemptState); err != nil || attemptState != string(payments.StatePaid) {
		t.Fatalf("verified event did not produce paid attempt: state=%q err=%v", attemptState, err)
	}
	var operationState operations.SessionDetail
	tableScope := operations.TableScope{TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01"}
	operationState, err = f.Operations.ReadSession(ctx, tableScope, first.Session.ID)
	if err != nil || len(operationState.Orders) != 1 || operationState.Orders[0].PaymentStatus != operations.Paid {
		t.Fatalf("paid payment not reflected in operations projection: %+v err=%v", operationState, err)
	}
	kdsPath := "/v1/staff/tenants/test-tenant/stores/test-store/operations/kds"
	kdsBeforeAccept := h.request(t, http.MethodGet, kdsPath, "", h.creds.Kitchen.Token, nil, "", "")
	var kdsEmpty struct {
		Orders []json.RawMessage `json:"orders"`
	}
	decodePaymentHTTP(t, kdsBeforeAccept, http.StatusOK, &kdsEmpty)
	if len(kdsEmpty.Orders) != 0 {
		t.Fatalf("paid but unaccepted order leaked into KDS: %s", kdsBeforeAccept.Body.String())
	}
	accept := h.requestWithKey(t, http.MethodPost, h.staffPath("table-01", first.Session.ID, "/orders/"+orderID+"/accept"), fmt.Sprintf(`{"expected_version":%d}`, operationState.Orders[0].Version), h.creds.Cashier.Token, nil, "", "", "accept-paid-stripe")
	if accept.Code != http.StatusOK {
		t.Fatalf("cashier acceptance failed: %d %s", accept.Code, accept.Body.String())
	}
	kdsAfterAccept := h.request(t, http.MethodGet, kdsPath, "", h.creds.Kitchen.Token, nil, "", "")
	var kdsCards struct {
		Orders []json.RawMessage `json:"orders"`
	}
	decodePaymentHTTP(t, kdsAfterAccept, http.StatusOK, &kdsCards)
	if len(kdsCards.Orders) != 1 {
		t.Fatalf("accepted paid order was not released to kitchen: %s", kdsAfterAccept.Body.String())
	}
	if deniedKDS := h.request(t, http.MethodGet, kdsPath, "", h.creds.Cashier.Token, nil, "", ""); deniedKDS.Code != http.StatusForbidden {
		t.Fatalf("cashier read kitchen-only KDS: %d %s", deniedKDS.Code, deniedKDS.Body.String())
	}

	// Complete the normal operational close gate before testing proof setup
	// against a closed session. All transitions use the authoritative version.
	operationState, err = f.Operations.ReadSession(ctx, tableScope, first.Session.ID)
	if err != nil || len(operationState.Orders) != 1 {
		t.Fatalf("read accepted order state: %+v err=%v", operationState, err)
	}
	version := operationState.Orders[0].Version
	for _, step := range []struct {
		target operations.FulfillmentStatus
		token  string
		key    string
	}{{operations.Preparing, h.creds.Kitchen.Token, "fulfill-preparing"}, {operations.Ready, h.creds.Kitchen.Token, "fulfill-ready"}, {operations.Served, h.creds.Cashier.Token, "fulfill-served"}} {
		body := fmt.Sprintf(`{"expected_version":%d,"target":%q}`, version, step.target)
		response := h.requestWithKey(t, http.MethodPost, h.staffPath("table-01", first.Session.ID, "/orders/"+orderID+"/fulfillment"), body, step.token, nil, "", "", step.key)
		if response.Code != http.StatusOK {
			t.Fatalf("fulfillment %s status=%d body=%s", step.target, response.Code, response.Body.String())
		}
		version++
	}
	closed := h.request(t, http.MethodPost, h.staffPath("table-01", first.Session.ID, "/close"), "{}", h.creds.Cashier.Token, nil, "", "")
	if closed.Code != http.StatusOK {
		t.Fatalf("served paid session close failed: %d %s", closed.Code, closed.Body.String())
	}
	closedContext := h.request(t, http.MethodPost, "/v1/guest/sessions/"+first.Session.ID+"/checkout-context", "", first.Token, firstCookie, paymentHTTPOrigin, "")
	if closedContext.Code != http.StatusForbidden {
		t.Fatalf("closed-session proof setup not denied: %d %s", closedContext.Code, closedContext.Body.String())
	}
	if current := h.request(t, http.MethodGet, tablePath, "", h.creds.Cashier.Token, nil, "", ""); current.Code != http.StatusOK {
		t.Fatalf("cashier payment status unexpectedly denied before membership revocation: %d", current.Code)
	}
	h.directory.Remove(h.creds.Cashier.SubjectID)
	if revoked := h.request(t, http.MethodGet, tablePath, "", h.creds.Cashier.Token, nil, "", ""); revoked.Code != http.StatusForbidden {
		t.Fatalf("revoked current membership remained usable: %d %s", revoked.Code, revoked.Body.String())
	}

	// An expired grant/session is also rejected at context issuance. Reopen the
	// same table through the existing staff route and expire only this fixture row.
	opened := h.request(t, http.MethodPost, "/v1/staff/tenants/test-tenant/stores/test-store/tables/table-01/sessions", "{}", h.creds.Manager.Token, nil, "", "")
	var openDTO map[string]any
	decodePaymentHTTP(t, opened, http.StatusOK, &openDTO)
	newSessionID := openDTO["session"].(map[string]any)["id"].(string)
	expiredGrant, err := f.Sessions.DirectJoin(ctx, f.Locator, newSessionID)
	if err != nil {
		t.Fatal(err)
	}
	newCartResponse := h.request(t, http.MethodGet, "/v1/guest/sessions/"+newSessionID+"/cart", "", expiredGrant.Token, nil, "", "")
	var newCart map[string]any
	decodePaymentHTTP(t, newCartResponse, http.StatusOK, &newCart)
	newAddBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"product_key":"tea","quantity":1,"options":[]}`, int64JSON(newCart["generation"]), int64JSON(newCart["version"]))
	newAdd := h.requestWithKey(t, http.MethodPost, "/v1/guest/sessions/"+newSessionID+"/cart/lines", newAddBody, expiredGrant.Token, nil, "", "", "add-expiring-tea")
	var newCartAdded map[string]any
	decodePaymentHTTP(t, newAdd, http.StatusOK, &newCartAdded)
	newPreviewBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d}`, int64JSON(newCartAdded["generation"]), int64JSON(newCartAdded["version"]))
	newPreviewResponse := h.request(t, http.MethodPost, "/v1/guest/sessions/"+newSessionID+"/cart/preview", newPreviewBody, expiredGrant.Token, nil, "", "")
	var newPreview map[string]any
	decodePaymentHTTP(t, newPreviewResponse, http.StatusOK, &newPreview)
	newContext := h.request(t, http.MethodPost, "/v1/guest/sessions/"+newSessionID+"/checkout-context", "", expiredGrant.Token, nil, paymentHTTPOrigin, "")
	decodePaymentHTTP(t, newContext, http.StatusNoContent, nil)
	newProofCookie := newContext.Result().Cookies()[0]
	newCommitBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"preview_fingerprint":%q,"payment_method":"STRIPE"}`,
		int64JSON(newCartAdded["generation"]), int64JSON(newCartAdded["version"]), newPreview["fingerprint"])
	newCommit := h.requestWithKey(t, http.MethodPost, "/v1/guest/sessions/"+newSessionID+"/cart/commit", newCommitBody, expiredGrant.Token, newProofCookie, paymentHTTPOrigin, "", "commit-expiring-stripe")
	var newCommitDTO map[string]any
	decodePaymentHTTP(t, newCommit, http.StatusOK, &newCommitDTO)
	newOrderID := newCommitDTO["order"].(map[string]any)["order_id"].(string)
	if _, err = f.Pool.Exec(ctx, `ALTER TABLE table_sessions DISABLE TRIGGER table_session_snapshot_immutable`); err != nil {
		t.Fatal(err)
	}
	_, expireErr := f.Pool.Exec(ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE session_id=$1`, newSessionID)
	_, enableErr := f.Pool.Exec(ctx, `ALTER TABLE table_sessions ENABLE TRIGGER table_session_snapshot_immutable`)
	if expireErr != nil {
		t.Fatal(expireErr)
	}
	if enableErr != nil {
		t.Fatal(enableErr)
	}
	expiredContext := h.request(t, http.MethodPost, "/v1/guest/sessions/"+newSessionID+"/checkout-context", "", expiredGrant.Token, nil, paymentHTTPOrigin, "")
	if expiredContext.Code != http.StatusForbidden {
		t.Fatalf("expired-session proof setup not denied: %d %s", expiredContext.Code, expiredContext.Body.String())
	}
	expiredStaffStatus := h.request(t, http.MethodGet, h.staffPath("table-01", newSessionID, "/orders/"+newOrderID+"/payment"), "", h.creds.Manager.Token, nil, "", "")
	var expiredStatusDTO map[string]any
	decodePaymentHTTP(t, expiredStaffStatus, http.StatusOK, &expiredStatusDTO)
	if expiredStatusDTO["state"] != string(payments.StateCreatePending) {
		t.Fatalf("staff S02 could not read the expired session's pending attempt: %v", expiredStatusDTO)
	}
}

func (f *paymentHTTPFixture) webhook(t *testing.T, body []byte, signature string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, paymentHTTPOrigin+"/v1/payments/stripe/webhook", strings.NewReader(string(body)))
	r.Header.Set("Stripe-Signature", signature)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func decodePaymentHTTP(t *testing.T, response *httptest.ResponseRecorder, status int, dst any) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("HTTP %d want %d: %s", response.Code, status, response.Body.String())
	}
	if dst != nil {
		if err := json.Unmarshal(response.Body.Bytes(), dst); err != nil {
			t.Fatalf("decode HTTP body %q: %v", response.Body.String(), err)
		}
	}
}

func assertPaymentResponsePrivate(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && strings.Contains(body, value) {
			t.Fatalf("safe HTTP projection leaked %q: %s", value, body)
		}
	}
}

func int64JSON(value any) int64 {
	if n, ok := value.(float64); ok {
		return int64(n)
	}
	return 0
}
