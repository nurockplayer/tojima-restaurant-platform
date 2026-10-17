package stripeprovider

import (
	"context"
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
		io.WriteString(w, `{"id":"cs_test_abc123","object":"checkout.session","livemode":false,"mode":"payment","currency":"jpy","amount_total":1500,"status":"open","payment_status":"unpaid","expires_at":1900000000,"url":"https://checkout.stripe.com/c/pay/cs_test_abc123","metadata":{"tenant_id":"tenant-1","store_id":"store-1","order_id":"order-1","attempt_id":"attempt-1"}}`)
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
