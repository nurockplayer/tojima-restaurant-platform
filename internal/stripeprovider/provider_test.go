package stripeprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

const (
	testKey     = "sk_test_12345678901234567890"
	testAccount = "acct_test123"
)

type redirectTransport struct {
	target string
	base   http.RoundTripper
}

func (t redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	target, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	u.Scheme, u.Host = target.Scheme, target.Host
	clone.URL, clone.Host = &u, target.Host
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func testConfig(client *http.Client) Config {
	return Config{APIKey: testKey, Mode: ModeConnectedDirectFixture, RequestTimeout: 2 * time.Second, MaxAmountJPY: 100000, SuccessURL: "https://example.test/payment/success", CancelURL: "https://example.test/payment/cancel", HTTPClient: client}
}

func TestNewFailsClosedWithoutExplicitTestConfiguration(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("New empty config err=%v", err)
	}
	cases := []Config{
		{APIKey: "sk_live_123456789", Mode: ModeConnectedDirectFixture, RequestTimeout: time.Second, MaxAmountJPY: 1, SuccessURL: "https://a.test", CancelURL: "https://b.test"},
		{APIKey: testKey, Mode: ModeUnconfigured, RequestTimeout: time.Second, MaxAmountJPY: 1, SuccessURL: "https://a.test", CancelURL: "https://b.test"},
		{APIKey: testKey, Mode: ModeConnectedDirectFixture, RequestTimeout: 0, MaxAmountJPY: 1, SuccessURL: "https://a.test", CancelURL: "https://b.test"},
		{APIKey: testKey, Mode: ModeConnectedDirectFixture, RequestTimeout: time.Second, MaxAmountJPY: 1, SuccessURL: "http://a.test", CancelURL: "https://b.test"},
	}
	for i, c := range cases {
		want := ErrInvalid
		if c.Mode == ModeUnconfigured {
			want = ErrUnconfigured
		}
		if _, err := New(c); !errors.Is(err, want) {
			t.Errorf("case %d err=%v", i, err)
		}
	}
}

func TestCreateCheckoutUsesScopedTestModeJPYAndStableIdempotency(t *testing.T) {
	var calls atomic.Int32
	var gotForm url.Values
	server := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Stripe-Account") != testAccount {
			t.Errorf("account header=%q", r.Header.Get("Stripe-Account"))
		}
		if r.Header.Get("Idempotency-Key") != "attempt-key-1" {
			t.Errorf("idempotency header=%q", r.Header.Get("Idempotency-Key"))
		}
		if v := r.Header.Get("Stripe-Version"); v != APIVersion {
			t.Errorf("Stripe-Version=%q", v)
		}
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("authorization header missing test key")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"cs_test_abc123","object":"checkout.session","livemode":false,"mode":"payment","currency":"jpy","amount_total":1500,"status":"open","payment_status":"unpaid","expires_at":1900000000,"url":"https://checkout.stripe.com/c/pay/cs_test_abc123#fidkdWxOYHwnPyd1blpxYHZxWjA0","metadata":{"tenant_id":"tenant-1","store_id":"store-1","order_id":"order-1","attempt_id":"attempt-1"}}`)
	})
	ts := httptest.NewServer(server)
	defer ts.Close()
	cfg := testConfig(&http.Client{Timeout: 2 * time.Second, Transport: redirectTransport{target: ts.URL}})
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.CreateCheckout(context.Background(), CreateCheckoutRequest{Account: AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, TenantID: "tenant-1", StoreID: "store-1", OrderID: "order-1", AttemptID: "attempt-1", IdempotencyKey: "attempt-key-1", AmountJPY: 1500, Description: "Restaurant order"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if got.AmountJPY != 1500 || got.Currency != CurrencyJPY || got.Status != CheckoutOpen || got.PaymentStatus != PaymentUnpaid || got.LiveMode || !strings.HasPrefix(got.URL, "https://checkout.stripe.com/") {
		t.Fatalf("unexpected result %#v", got)
	}
	if gotForm.Get("mode") != "payment" || gotForm.Get("currency") != "jpy" || gotForm.Get("line_items[0][quantity]") != "1" || gotForm.Get("line_items[0][price_data][unit_amount]") != "1500" || gotForm.Get("line_items[0][price_data][currency]") != "jpy" || gotForm.Get("line_items[0][price_data][product_data][name]") != "Restaurant order" {
		t.Fatalf("unexpected Stripe params: %#v", gotForm)
	}
	if gotForm.Has("automatic_tax[enabled]") || gotForm.Has("automatic_tax") {
		t.Fatalf("unexpected automatic tax params: %#v", gotForm)
	}
	if gotForm.Get("success_url") != cfg.SuccessURL || gotForm.Get("cancel_url") != cfg.CancelURL {
		t.Fatalf("redirects not server config")
	}
	for _, key := range []string{"tenant_id", "store_id", "order_id", "attempt_id"} {
		value := gotForm.Get("metadata[" + key + "]")
		if value == "" || gotForm.Get("payment_intent_data[metadata]["+key+"]") != value {
			t.Errorf("scope metadata %q not copied to PaymentIntent: session=%q PI=%q", key, value, gotForm.Get("payment_intent_data[metadata]["+key+"]"))
		}
	}
}

func TestRefundCreateAndRetrieveUseOriginalIntentScopeAndIdempotency(t *testing.T) {
	var gotForm url.Values
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Stripe-Account") != testAccount {
			t.Errorf("account header=%q", r.Header.Get("Stripe-Account"))
		}
		if r.Method == http.MethodPost {
			if r.URL.Path != "/v1/refunds" || r.Header.Get("Idempotency-Key") != "refund-key-1" {
				t.Errorf("create request %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			gotForm = r.Form
		} else if r.URL.Path != "/v1/refunds/re_test123" {
			t.Errorf("retrieve path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"re_test123","object":"refund","livemode":false,"amount":500,"currency":"jpy","status":"pending","payment_intent":"pi_test123","metadata":{"tenant_id":"tenant-1","store_id":"store-1","order_id":"order-1","refund_id":"refund-1"}}`)
	}))
	defer ts.Close()
	p, err := New(testConfig(&http.Client{Timeout: 2 * time.Second, Transport: redirectTransport{target: ts.URL}}))
	if err != nil {
		t.Fatal(err)
	}
	req := CreateRefundRequest{Account: AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, TenantID: "tenant-1", StoreID: "store-1", OrderID: "order-1", RefundID: "refund-1", PaymentIntentID: "pi_test123", IdempotencyKey: "refund-key-1", AmountJPY: 500}
	got, err := p.CreateRefund(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.AmountJPY != 500 || got.PaymentIntentID != "pi_test123" || got.LiveMode {
		t.Fatalf("refund result %#v", got)
	}
	if gotForm.Get("payment_intent") != "pi_test123" || gotForm.Get("amount") != "500" || gotForm.Get("currency") != "jpy" || gotForm.Get("metadata[tenant_id]") != "tenant-1" || gotForm.Get("metadata[store_id]") != "store-1" || gotForm.Get("metadata[order_id]") != "order-1" || gotForm.Get("metadata[refund_id]") != "refund-1" {
		t.Fatalf("unexpected refund form %#v", gotForm)
	}
	retrieved, err := p.RetrieveRefund(context.Background(), req.Account, "re_test123")
	if err != nil || retrieved.ID != got.ID || calls.Load() != 2 {
		t.Fatalf("retrieve=%#v err=%v calls=%d", retrieved, err, calls.Load())
	}
}

func TestCreateRefundRejectsInvalidScopeAmountAndPaymentIntentBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer ts.Close()
	p, err := New(testConfig(&http.Client{Timeout: time.Second, Transport: redirectTransport{target: ts.URL}}))
	if err != nil {
		t.Fatal(err)
	}
	base := CreateRefundRequest{Account: AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, TenantID: "tenant-1", StoreID: "store-1", OrderID: "order-1", RefundID: "refund-1", PaymentIntentID: "pi_test123", IdempotencyKey: "refund-key-1", AmountJPY: 500}
	cases := []struct {
		name   string
		mutate func(*CreateRefundRequest)
	}{
		{name: "empty tenant", mutate: func(r *CreateRefundRequest) { r.TenantID = "" }},
		{name: "empty refund id", mutate: func(r *CreateRefundRequest) { r.RefundID = "" }},
		{name: "bad payment intent", mutate: func(r *CreateRefundRequest) { r.PaymentIntentID = "cs_test123" }},
		{name: "zero amount", mutate: func(r *CreateRefundRequest) { r.AmountJPY = 0 }},
		{name: "above ceiling", mutate: func(r *CreateRefundRequest) { r.AmountJPY = 100001 }},
		{name: "missing idempotency", mutate: func(r *CreateRefundRequest) { r.IdempotencyKey = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			if _, err := p.CreateRefund(context.Background(), req); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached transport %d times", calls.Load())
	}
}

func TestCreateCheckoutRejectsBadScopeAndNeverRetriesAmbiguousResult(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"private provider detail"}}`)
	}))
	defer ts.Close()
	p, err := New(testConfig(&http.Client{Timeout: time.Second, Transport: redirectTransport{target: ts.URL}}))
	if err != nil {
		t.Fatal(err)
	}
	base := CreateCheckoutRequest{Account: AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, TenantID: "tenant", StoreID: "store", OrderID: "order", AttemptID: "attempt", IdempotencyKey: "stable-key", AmountJPY: 10, Description: "Order"}
	bad := base
	bad.Account.StripeAccountID = ""
	if _, err = p.CreateCheckout(context.Background(), bad); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("bad account err=%v", err)
	}
	bad = base
	bad.AmountJPY = 100001
	if _, err = p.CreateCheckout(context.Background(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over-limit err=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached transport")
	}
	if _, err = p.CreateCheckout(context.Background(), base); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ambiguous error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("request was retried: calls=%d", calls.Load())
	}
	if strings.Contains(ErrOutcomeUnknown.Error(), testKey) || strings.Contains(ErrOutcomeUnknown.Error(), "private provider detail") || strings.Contains(ErrOutcomeUnknown.Error(), "https://checkout.stripe.com/") {
		t.Fatal("error leaks secret/provider response")
	}
}

func TestCreateRecoveryAcceptsCompletedPaidWithoutContinuationURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"cs_test_recovered","object":"checkout.session","livemode":false,"mode":"payment","currency":"jpy","amount_total":330,"status":"complete","payment_status":"paid","payment_intent":"pi_recovered","expires_at":1900000000,"url":null,"metadata":{"tenant_id":"t","store_id":"s","order_id":"o","attempt_id":"a"}}`)
	}))
	defer server.Close()
	p, err := New(testConfig(&http.Client{Transport: redirectTransport{target: server.URL}}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.CreateCheckout(context.Background(), CreateCheckoutRequest{Account: AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, TenantID: "t", StoreID: "s", OrderID: "o", AttemptID: "a", IdempotencyKey: "same-attempt", AmountJPY: 330, Description: "Order"})
	if err != nil || got.Status != CheckoutComplete || got.PaymentStatus != PaymentPaid || got.PaymentIntentID != "pi_recovered" || got.URL != "" {
		t.Fatalf("completed recovery discarded: result=%#v err=%v", got, err)
	}
}

func TestCreateCheckoutPropagatesCanceledContextWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer ts.Close()
	p, err := New(testConfig(&http.Client{Timeout: time.Second, Transport: redirectTransport{target: ts.URL}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.CreateCheckout(ctx, CreateCheckoutRequest{Account: AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, TenantID: "t", StoreID: "s", OrderID: "o", AttemptID: "a", IdempotencyKey: "stable", AmountJPY: 1, Description: "Order"})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("canceled create err=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("canceled request reached transport: %d", calls.Load())
	}
}

func TestCreateCheckoutRejectsMismatchedProviderAmount(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"cs_test_abc123","livemode":false,"mode":"payment","currency":"jpy","amount_total":99,"status":"open","payment_status":"unpaid","expires_at":1900000000,"metadata":{"tenant_id":"t","store_id":"s","order_id":"o","attempt_id":"a"}}`)
	}))
	defer ts.Close()
	p, err := New(testConfig(&http.Client{Timeout: time.Second, Transport: redirectTransport{target: ts.URL}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.CreateCheckout(context.Background(), CreateCheckoutRequest{Account: AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, TenantID: "t", StoreID: "s", OrderID: "o", AttemptID: "a", IdempotencyKey: "key", AmountJPY: 100, Description: "Order"})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("mismatched amount err=%v", err)
	}
}

func TestRetrieveExpireAndPaymentIntentUseExplicitAccount(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Stripe-Account") != testAccount {
			t.Errorf("missing/wrong Stripe-Account on %s: %#v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/checkout/sessions/cs_test_abc123", "/v1/checkout/sessions/cs_test_abc123/expire":
			status := "open"
			if strings.HasSuffix(r.URL.Path, "/expire") {
				status = "expired"
			}
			fmt.Fprintf(w, `{"id":"cs_test_abc123","livemode":false,"mode":"payment","currency":"jpy","amount_total":10,"status":%q,"payment_status":"unpaid","expires_at":1900000000}`, status)
		case "/v1/payment_intents/pi_test_abc123":
			io.WriteString(w, `{"id":"pi_test_abc123","livemode":false,"currency":"jpy","amount":10,"status":"succeeded"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()
	p, err := New(testConfig(&http.Client{Timeout: time.Second, Transport: redirectTransport{target: ts.URL}}))
	if err != nil {
		t.Fatal(err)
	}
	a := AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}
	got, err := p.RetrieveCheckout(context.Background(), a, "cs_test_abc123")
	if err != nil || got.Status != CheckoutOpen {
		t.Fatalf("retrieve %#v err=%v", got, err)
	}
	got, err = p.ExpireCheckout(context.Background(), a, "cs_test_abc123", "expire-key")
	if err != nil || got.Status != CheckoutExpired {
		t.Fatalf("expire %#v err=%v", got, err)
	}
	pi, err := p.RetrievePaymentIntent(context.Background(), a, "pi_test_abc123")
	if err != nil || pi.Status != "succeeded" || pi.AmountJPY != 10 {
		t.Fatalf("payment intent %#v err=%v", pi, err)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests=%d", requests.Load())
	}
	if _, err = p.RetrieveCheckout(context.Background(), AccountContext{Mode: "", StripeAccountID: testAccount}, "cs_test_abc123"); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("missing mode err=%v", err)
	}
}

func TestRetrievalRejectsProviderObjectIDMismatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
			io.WriteString(w, `{"id":"cs_test_other","livemode":false,"mode":"payment","currency":"jpy","amount_total":10,"status":"open","payment_status":"unpaid","expires_at":1900000000}`)
			return
		}
		io.WriteString(w, `{"id":"pi_test_other","livemode":false,"currency":"jpy","amount":10,"status":"succeeded"}`)
	}))
	defer ts.Close()
	p, err := New(testConfig(&http.Client{Timeout: time.Second, Transport: redirectTransport{target: ts.URL}}))
	if err != nil {
		t.Fatal(err)
	}
	a := AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}
	if _, err = p.RetrieveCheckout(context.Background(), a, "cs_test_expected"); !errors.Is(err, ErrUnsafeResponse) {
		t.Fatalf("retrieve mismatch err=%v", err)
	}
	if _, err = p.ExpireCheckout(context.Background(), a, "cs_test_expected", "expire-key"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("expire mismatch err=%v", err)
	}
	if _, err = p.RetrievePaymentIntent(context.Background(), a, "pi_test_expected"); !errors.Is(err, ErrUnsafeResponse) {
		t.Fatalf("payment intent mismatch err=%v", err)
	}
}

func TestCheckoutRejectsNonPaymentModes(t *testing.T) {
	for _, mode := range []string{"subscription", "setup"} {
		t.Run(mode, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"id":"cs_test_abc123","livemode":false,"mode":%q,"currency":"jpy","amount_total":10,"status":"open","payment_status":"unpaid","expires_at":1900000000}`, mode)
			}))
			defer ts.Close()
			p, err := New(testConfig(&http.Client{Timeout: time.Second, Transport: redirectTransport{target: ts.URL}}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.RetrieveCheckout(context.Background(), AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, "cs_test_abc123")
			if !errors.Is(err, ErrUnsafeResponse) {
				t.Fatalf("mode %q err=%v", mode, err)
			}
		})
	}
}

func signedEvent(t *testing.T, now time.Time, secret string, live bool, apiVersion string) ([]byte, string) {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"id":"evt_test_123","object":"event","api_version":%q,"created":%d,"livemode":%t,"type":"checkout.session.completed","account":%q,"data":{"object":{"id":"cs_test_abc123","object":"checkout.session","livemode":false,"mode":"payment","currency":"jpy","amount_total":10,"status":"complete","payment_status":"unpaid","expires_at":1900000000,"url":"https://checkout.stripe.com/private"}}}`, apiVersion, now.Unix(), live, testAccount))
	sig := stripe.ComputeSignature(now, payload, secret)
	return payload, fmt.Sprintf("t=%d,v1=%x", now.Unix(), sig)
}

func TestVerifyWebhookExactBytesRotationAndSafeProjection(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	body, header := signedEvent(t, now, "whsec_previous", false, APIVersion)
	event, err := VerifyWebhook(body, header, []string{"whsec_current", "whsec_previous"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "evt_test_123" || event.AccountID != testAccount || event.ObjectID != "cs_test_abc123" || event.Checkout == nil || event.Checkout.PaymentStatus != PaymentUnpaid || event.Checkout.URL != "" {
		t.Fatalf("unsafe/incorrect event projection %#v", event)
	}
	mutated := append([]byte(nil), body...)
	mutated[len(mutated)-2] ^= 1
	if _, err = VerifyWebhook(mutated, header, []string{"whsec_previous"}, now); !errors.Is(err, ErrWebhook) {
		t.Fatalf("mutated body err=%v", err)
	}
	if _, err = VerifyWebhook(body, header, []string{"whsec_wrong"}, now); !errors.Is(err, ErrWebhook) {
		t.Fatalf("wrong secret err=%v", err)
	}
}

func TestVerifyWebhookProjectsRefundLifecycleAndChargeRefunds(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, typ, object string
		wantID            string
		plural            bool
	}{
		{name: "refund-failed", typ: "refund.failed", wantID: "re_test123", object: `{"id":"re_test123","object":"refund","livemode":false,"amount":500,"currency":"jpy","status":"failed","payment_intent":"pi_test123","metadata":{"tenant_id":"t","store_id":"s","order_id":"o","refund_id":"r"}}`},
		{name: "charge-refunded", typ: "charge.refunded", wantID: "ch_test123", plural: true, object: `{"id":"ch_test123","object":"charge","livemode":false,"payment_intent":"pi_test123","refunds":{"object":"list","has_more":true,"data":[{"id":"re_test123","object":"refund","livemode":false,"amount":500,"currency":"jpy","status":"succeeded","metadata":{"tenant_id":"t","store_id":"s","order_id":"o","refund_id":"r"}},{"id":"re_other123","object":"refund","amount":50,"currency":"jpy","status":"succeeded","metadata":{"tenant_id":"other-tenant","store_id":"other-store","order_id":"other-order","refund_id":"other-refund"}}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"id": "evt_test_refund", "object": "event", "type": tc.typ, "account": testAccount, "api_version": APIVersion, "created": now.Unix(), "livemode": false, "data": map[string]any{"object": json.RawMessage(tc.object)}})
			sig := stripe.ComputeSignature(now, body, "whsec_refund")
			got, err := VerifyWebhook(body, fmt.Sprintf("t=%d,v1=%x", now.Unix(), sig), []string{"whsec_refund"}, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.ObjectID != tc.wantID {
				t.Fatalf("object id=%q", got.ObjectID)
			}
			if tc.plural {
				if len(got.Refunds) != 2 || got.Refunds[0].PaymentIntentID != "pi_test123" || got.Refunds[1].PaymentIntentID != "pi_test123" || got.Refunds[1].Metadata["tenant_id"] != "other-tenant" || !got.RefundsHasMore {
					t.Fatalf("refunds %#v", got.Refunds)
				}
			} else if got.Refund == nil || got.Refund.ID != tc.wantID || got.Refund.Status != "failed" {
				t.Fatalf("refund %#v", got.Refund)
			}
		})
	}
}

func TestVerifyWebhookRejectsLiveRefundEnvelopeAndChargePIConflict(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, eventType string
		live            bool
		object          string
	}{
		{name: "live refund event", eventType: "refund.failed", live: true, object: `{"id":"re_test123","object":"refund","amount":500,"currency":"jpy","status":"failed","payment_intent":"pi_test123"}`},
		{name: "charge refund PI conflict", eventType: "charge.refunded", object: `{"id":"ch_test123","object":"charge","livemode":false,"payment_intent":"pi_test123","refunds":{"object":"list","data":[{"id":"re_test123","object":"refund","amount":500,"currency":"jpy","status":"succeeded","payment_intent":"pi_other123"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"id": "evt_test_refund", "object": "event", "type": tc.eventType, "account": testAccount, "api_version": APIVersion, "created": now.Unix(), "livemode": tc.live, "data": map[string]any{"object": json.RawMessage(tc.object)}})
			sig := stripe.ComputeSignature(now, body, "whsec_refund")
			if _, err := VerifyWebhook(body, fmt.Sprintf("t=%d,v1=%x", now.Unix(), sig), []string{"whsec_refund"}, now); !errors.Is(err, ErrWebhook) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestVerifyWebhookHonorsInjectedHistoricalClock(t *testing.T) {
	now := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	body, header := signedEvent(t, now, "whsec_test", false, APIVersion)
	for _, tc := range []struct {
		name    string
		offset  time.Duration
		wantErr bool
	}{
		{name: "at-signing-time"},
		{name: "old-boundary", offset: WebhookTolerance},
		{name: "future-boundary", offset: -WebhookTolerance},
		{name: "expired", offset: WebhookTolerance + time.Second, wantErr: true},
		{name: "too-far-in-future", offset: -WebhookTolerance - time.Second, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, err := VerifyWebhook(body, header, []string{"whsec_test"}, now.Add(tc.offset))
			if tc.wantErr {
				if !errors.Is(err, ErrWebhook) {
					t.Fatalf("outside tolerance err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("within tolerance err=%v", err)
			}
			if event.ID != "evt_test_123" || !event.CreatedAt.Equal(now) {
				t.Fatalf("incorrect historical event %#v", event)
			}
		})
	}
}

func TestVerifyWebhookRejectsOldFutureLiveWrongVersionAndOversize(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		at       time.Time
		live     bool
		version  string
		tooLarge bool
	}{
		{name: "old", at: now.Add(-WebhookTolerance - time.Second), version: APIVersion},
		{name: "future", at: now.Add(WebhookTolerance + time.Second), version: APIVersion},
		{name: "live", at: now, live: true, version: APIVersion},
		{name: "wrong-version", at: now, version: "2025-01-01.acacia"},
		{name: "oversize", at: now, version: APIVersion, tooLarge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, header := signedEvent(t, tc.at, "whsec_test", tc.live, tc.version)
			if tc.tooLarge {
				body = make([]byte, WebhookMaxBytes+1)
				header = "t=1,v1=bad"
			}
			if _, err := VerifyWebhook(body, header, []string{"whsec_test"}, now); !errors.Is(err, ErrWebhook) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	body, header := signedEvent(t, now, "whsec_test", false, APIVersion)
	parts := strings.Split(header, ",")
	badTimestamp := parts[0] + ",t=" + parts[0][2:] + "," + parts[1]
	if _, err := VerifyWebhook(body, badTimestamp, []string{"whsec_test"}, now); !errors.Is(err, ErrWebhook) {
		t.Fatalf("duplicate timestamp err=%v", err)
	}
}

func TestReadWebhookBodyBoundsAllocation(t *testing.T) {
	body, err := ReadWebhookBody(strings.NewReader("small-event"))
	if err != nil || string(body) != "small-event" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if _, err := ReadWebhookBody(strings.NewReader(strings.Repeat("x", WebhookMaxBytes+100))); !errors.Is(err, ErrWebhook) {
		t.Fatalf("oversized reader err=%v", err)
	}
	if _, err := ReadWebhookBody(nil); !errors.Is(err, ErrWebhook) {
		t.Fatalf("nil reader err=%v", err)
	}
}

func TestRefundStatusTransportIncludesPinnedEnumAndUnknown(t *testing.T) {
	// These are all RefundStatus constants in stripe-go/v87 v87.0.0. Preserve
	// unknown/empty values too: only retrieved status projection decides outcome.
	for _, status := range []string{string(stripe.RefundStatusPending), string(stripe.RefundStatusRequiresAction), string(stripe.RefundStatusSucceeded), string(stripe.RefundStatusFailed), string(stripe.RefundStatusCanceled), "future_status", ""} {
		t.Run(status, func(t *testing.T) {
			object := fmt.Sprintf(`{"id":"re_test123","object":"refund","amount":500,"currency":"jpy","status":%q,"payment_intent":"pi_test123","metadata":{"tenant_id":"t","store_id":"s","order_id":"o","refund_id":"r"}}`, status)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/refunds/re_test123" || r.Header.Get("Stripe-Version") != APIVersion {
					t.Errorf("unexpected retrieval %s %s version=%s", r.Method, r.URL.Path, r.Header.Get("Stripe-Version"))
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, object)
			}))
			defer ts.Close()
			p, err := New(testConfig(&http.Client{Timeout: 2 * time.Second, Transport: redirectTransport{target: ts.URL}}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.RetrieveRefund(context.Background(), AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount}, "re_test123")
			if err != nil || result.Status != status {
				t.Fatalf("status=%q result=%+v err=%v", status, result, err)
			}
			now := time.Now()
			body, _ := json.Marshal(map[string]any{"id": "evt_status", "object": "event", "type": "refund.updated", "account": testAccount, "api_version": APIVersion, "created": now.Unix(), "livemode": false, "data": map[string]any{"object": json.RawMessage(object)}})
			signature := stripe.ComputeSignature(now, body, "whsec_status")
			event, err := VerifyWebhook(body, fmt.Sprintf("t=%d,v1=%x", now.Unix(), signature), []string{"whsec_status"}, now)
			if err != nil || event.Refund == nil || event.Refund.Status != status {
				t.Fatalf("webhook=%+v err=%v", event, err)
			}
		})
	}
}

func TestCheckoutURLAllowsStripeFragmentOnlyOnHostedCheckout(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://checkout.stripe.com/c/pay/cs_test_abc123#fidkdWxOYHwnPyd1blpx": true,
		"https://checkout.stripe.com/c/pay/cs_test_abc123":                      true,
		"http://checkout.stripe.com/c/pay/cs_test_abc123":                       false,
		"https://evil.example/c/pay/cs_test_abc123#fid":                         false,
		"https://checkout.stripe.com.evil.example/c/pay/cs_test_abc123":         false,
		"https://user@checkout.stripe.com/c/pay/cs_test_abc123":                 false,
		"https://checkout.stripe.com:8443/c/pay/cs_test_abc123":                 false,
	} {
		if got := trustedCheckoutURL(raw); got != want {
			t.Errorf("trustedCheckoutURL(%q)=%v want %v", raw, got, want)
		}
	}
}
