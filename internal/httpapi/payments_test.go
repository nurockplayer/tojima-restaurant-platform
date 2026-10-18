package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
)

func TestPaymentsHTTPConfigRequiresHTTPSOrExplicitLiteralLoopback(t *testing.T) {
	secure := PaymentsHTTPConfig{Origin: "https://orders.example", WebhookSecrets: []string{"whsec_test"}}
	if err := secure.validate(); err == nil {
		t.Fatal("missing proof service accepted")
	}
	proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: [][]byte{[]byte("0123456789abcdef0123456789abcdef")}, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	secure.Proofs = proofs
	if err := secure.validate(); err != nil {
		t.Fatalf("valid HTTPS config rejected: %v", err)
	}
	if secure.cookieName() != checkoutProofCookieName {
		t.Fatalf("HTTPS selected fixture cookie %q", secure.cookieName())
	}
	cookie := secure.proofCookie("opaque", time.Now().Add(time.Minute))
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("unsafe HTTPS cookie attributes: %+v", cookie)
	}

	loopback := PaymentsHTTPConfig{Proofs: proofs, Origin: "http://127.0.0.1:8082", AllowLoopbackHTTP: true, WebhookSecrets: []string{"whsec_fixture"}}
	if err := loopback.validate(); err != nil {
		t.Fatalf("explicit literal loopback config rejected: %v", err)
	}
	if loopback.cookieName() != fixtureCheckoutProofCookieName {
		t.Fatalf("loopback selected production cookie %q", loopback.cookieName())
	}
	cookie = loopback.proofCookie("opaque", time.Now().Add(time.Minute))
	if cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("unsafe fixture cookie attributes: %+v", cookie)
	}

	for _, cfg := range []PaymentsHTTPConfig{
		{Proofs: proofs, Origin: "http://127.0.0.1:8082", WebhookSecrets: []string{"whsec_fixture"}},
		{Proofs: proofs, Origin: "http://localhost:8082", AllowLoopbackHTTP: true, WebhookSecrets: []string{"whsec_fixture"}},
		{Proofs: proofs, Origin: "https://orders.example/path", WebhookSecrets: []string{"whsec_test"}},
		{Proofs: proofs, Origin: "https://orders.example/", WebhookSecrets: []string{"whsec_test"}},
	} {
		if err := cfg.validate(); err == nil {
			t.Fatalf("unsafe/non-origin config accepted: %+v", cfg)
		}
	}
}

func TestPaymentsHTTPOriginMustMatchExactConfiguredOrigin(t *testing.T) {
	cfg := PaymentsHTTPConfig{Origin: "https://orders.example", AllowLoopbackHTTP: true}
	for _, test := range []struct {
		origin string
		allow  bool
	}{
		{origin: "https://orders.example", allow: true},
		{origin: "https://orders.example/"},
		{origin: "https://attacker.example"},
		{origin: "null"},
		{origin: ""},
	} {
		if got := cfg.originAllowed(test.origin); got != test.allow {
			t.Fatalf("originAllowed(%q)=%v, want %v", test.origin, got, test.allow)
		}
	}
}

func TestPaymentsHTTPOriginMustMatchRequestHost(t *testing.T) {
	cfg := PaymentsHTTPConfig{Origin: "http://127.0.0.1:8082", AllowLoopbackHTTP: true}
	matching := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8082/v1/guest/sessions/s1/checkout-context", nil)
	foreign := httptest.NewRequest(http.MethodPost, "http://attacker.example/v1/guest/sessions/s1/checkout-context", nil)
	if !cfg.requestHostAllowed(matching) {
		t.Fatal("configured literal loopback host rejected")
	}
	if cfg.requestHostAllowed(foreign) {
		t.Fatal("foreign request Host accepted with an otherwise valid Origin")
	}
}

func TestPaymentsAPIGuestMutationsRequireOriginAndProof(t *testing.T) {
	proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: [][]byte{[]byte("0123456789abcdef0123456789abcdef")}, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	a := PaymentsAPI{Config: PaymentsHTTPConfig{Proofs: proofs, Origin: "https://orders.example", WebhookSecrets: []string{"whsec_test"}}}

	for _, tc := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		path    string
		origin  string
		cookie  string
	}{
		{name: "resume cross origin", handler: a.resumeCheckout, path: "https://orders.example/v1/guest/sessions/s1/orders/o1/checkout/resume", origin: "https://attacker.example"},
		{name: "resume missing proof", handler: a.resumeCheckout, path: "https://orders.example/v1/guest/sessions/s1/orders/o1/checkout/resume", origin: "https://orders.example"},
		{name: "context cross origin", handler: a.checkoutContext, path: "https://orders.example/v1/guest/sessions/s1/checkout-context", origin: "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			r.Header.Set("Origin", tc.origin)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: checkoutProofCookieName, Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			tc.handler(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status=%d want403: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPaymentsAPIWebhookRejectsUnverifiedBodyBeforeEnqueue(t *testing.T) {
	a := PaymentsAPI{Config: PaymentsHTTPConfig{WebhookSecrets: []string{"whsec_test"}}}
	r := httptest.NewRequest(http.MethodPost, "/v1/payments/stripe/webhook", strings.NewReader(`{"id":"evt_fake"}`))
	r.Header.Set("Stripe-Signature", "t=1,v1=00")
	w := httptest.NewRecorder()
	a.webhook(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want400: %s", w.Code, w.Body.String())
	}
}

func TestPaymentStatusProjectionContainsNoProviderURLOrIdentifiers(t *testing.T) {
	w := httptest.NewRecorder()
	writePaymentStatus(w, payments.Status{OrderID: "order-safe", State: payments.StateOpen, CheckoutStatus: "open", PaymentStatus: "unpaid", SafeErrorCode: "HOLD_RECONCILIATION_NEEDED"}, true)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["order_id"] != "order-safe" || got["resume_available"] != true || got["payment_review_required"] != false || got["safe_error_code"] != "HOLD_RECONCILIATION_NEEDED" {
		t.Fatalf("incorrect safe status: %v", got)
	}
	for _, forbidden := range []string{"url", "checkout_url", "checkout_session_id", "payment_intent_id", "account_id", "proof", "checkout_proof_hash"} {
		if _, ok := got[forbidden]; ok {
			t.Fatalf("private field %q in projection: %v", forbidden, got)
		}
	}
}
