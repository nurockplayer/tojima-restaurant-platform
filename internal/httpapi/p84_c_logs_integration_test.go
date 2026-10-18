package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

// This test owns process-global log destinations; do not run it in parallel.
// Drain concurrently so a newly introduced verbose logger cannot fill a pipe
// and hang the very regression intended to detect it. Restore even on Fatal.
func p84CCaptureLogs(t *testing.T) (*os.File, func() string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, stdout := os.Stderr, os.Stdout
	oldSlog, oldOutput, oldFlags := slog.Default(), log.Writer(), log.Flags()
	os.Stderr, os.Stdout = writer, writer
	slog.SetDefault(slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(writer)
	done := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		_, _ = io.Copy(&out, reader)
		done <- out.String()
	}()
	var output string
	stop := sync.OnceFunc(func() {
		os.Stderr, os.Stdout = stderr, stdout
		slog.SetDefault(oldSlog)
		log.SetOutput(oldOutput)
		log.SetFlags(oldFlags)
		_ = writer.Close()
		output = <-done
		_ = reader.Close()
	})
	t.Cleanup(stop)
	return writer, func() string { stop(); return output }
}

type p84CTransport func(*http.Request) (*http.Response, error)

func (f p84CTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestP84CRuntimeLogsExcludeSecretsPostgres(t *testing.T) {
	pf := newPaymentHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := pf.fixture
	logs, stopCapture := p84CCaptureLogs(t)
	const apiKey = "sk_test_SECRETMARKER_P84C"
	const webhookSecret = "whsec_SECRETMARKER_P84C"
	const checkoutURL = "https://checkout.stripe.com/c/pay/cs_test_SECRETMARKER_P84C"
	const badStaff = "staff_bearer_SECRETMARKER_P84C"
	markers := []string{apiKey, webhookSecret, "rk_test_SECRETMARKER_P84C", checkoutURL,
		"checkout_proof_SECRETMARKER_P84C", "ORDER_READ_SECRETMARKER_P84C", badStaff, pf.creds.Cashier.Token}
	// Use the real Stripe SDK/adapter, with every HTTP request intercepted.
	// First CREATE returns a secret-bearing Stripe error; retry succeeds with
	// the identical idempotency key. No Stripe network or credentials are used.
	var mu sync.Mutex
	var createKeys []string
	var retrieves int
	var checkout stripeprovider.CheckoutResult
	transport := p84CTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		status := http.StatusOK
		var body any
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			if r.Header.Get("Authorization") != "Bearer "+apiKey {
				return nil, fmt.Errorf("missing synthetic API credential")
			}
			createKeys = append(createKeys, r.Header.Get("Idempotency-Key"))
			if len(createKeys) == 1 {
				status = http.StatusInternalServerError
				body = map[string]any{"error": map[string]string{"type": "api_error", "message": strings.Join(markers, " ")}}
				break
			}
			checkout = stripeprovider.CheckoutResult{
				AccountID: pf.account.StripeAccountID, SessionID: "cs_test_P84C", PaymentIntentID: "pi_P84C",
				Status: stripeprovider.CheckoutOpen, PaymentStatus: stripeprovider.PaymentUnpaid,
				AmountJPY: 330, Currency: "jpy", ExpiresAt: time.Now().UTC().Add(time.Hour), URL: checkoutURL,
				Metadata: map[string]string{},
			}
			for _, key := range []string{"tenant_id", "store_id", "order_id", "attempt_id"} {
				checkout.Metadata[key] = r.Form.Get("metadata[" + key + "]")
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/checkout/sessions/cs_test_P84C":
			retrieves++
		default:
			return nil, fmt.Errorf("unexpected synthetic Stripe request")
		}
		if body == nil {
			body = map[string]any{"id": checkout.SessionID, "object": "checkout.session", "mode": "payment",
				"status": checkout.Status, "payment_status": checkout.PaymentStatus, "payment_intent": checkout.PaymentIntentID,
				"amount_total": checkout.AmountJPY, "currency": checkout.Currency, "livemode": false,
				"expires_at": checkout.ExpiresAt.Unix(), "url": checkout.URL, "metadata": checkout.Metadata}
		}
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
	})
	provider, err := stripeprovider.New(stripeprovider.Config{
		APIKey: apiKey, Mode: pf.account.Mode, RequestTimeout: time.Second, MaxAmountJPY: 10000,
		SuccessURL: "https://example.invalid/success", CancelURL: "https://example.invalid/cancel",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := payments.New(f.Pool, f.Operations, paymentHTTPResolver{paymentHTTPCredentialRef: {
		Provider: provider, Account: pf.account,
		ProfileFingerprint: sha256.Sum256([]byte(stripeprovider.APIVersion + "|http-test-profile")),
	}}, payments.Config{CreateRetryWindow: 20 * time.Hour, ContinuationTTL: 30 * time.Minute, WorkLeaseTTL: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// Ordering uses pf.service's existing identical profile for admission. The
	// replacement service processes its persisted work with the actual adapter.
	proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: [][]byte{[]byte(strings.Repeat("p84c", 8))}, TTL: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewPaymentsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, service, pf.identity,
		authz.PolicyAuthorizer{Directory: pf.directory}, PaymentsHTTPConfig{
			Proofs: proofs, Origin: paymentHTTPOrigin, AllowLoopbackHTTP: true, WebhookSecrets: []string{webhookSecret},
		})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(logs, "http: ", log.LstdFlags)
	server.Start()
	defer server.Close()
	// Positive controls prove that all four destinations are connected, even
	// though the exercised application paths currently emit no runtime logs.
	slog.Debug("p84c-slog-probe")
	log.Print("p84c-log-probe")
	fmt.Fprintln(os.Stderr, "p84c-stderr-probe")
	fmt.Fprintln(os.Stdout, "p84c-stdout-probe")
	server.Config.ErrorLog.Print("p84c-http-probe")
	request := func(method, path, body, token, key string, cookie *http.Cookie, signature string, want int, dst any) *http.Response {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "127.0.0.1:8082"
		r.Header.Set("Origin", paymentHTTPOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("Stripe-Signature", signature)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d", method, path, response.StatusCode, want)
		}
		if dst != nil {
			if err := json.NewDecoder(response.Body).Decode(dst); err != nil {
				t.Fatal(err)
			}
		} else {
			_, _ = io.Copy(io.Discard, response.Body)
		}
		return response
	}
	guest, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	markers = append(markers, guest.Token)
	base := "/v1/guest/sessions/" + guest.Session.ID
	response := request("POST", base+"/checkout-context", "", guest.Token, "", nil, "", 204, nil)
	if len(response.Cookies()) != 1 || response.Cookies()[0].Value == "" {
		t.Fatal("checkout proof was not issued")
	}
	cookie := response.Cookies()[0]
	markers = append(markers, cookie.Value)
	request("POST", base+"/cart/lines", `{"expected_generation":1,"expected_version":1,"product_key":"tea","quantity":1,"options":[]}`, guest.Token, "p84c-add", nil, "", 200, nil)
	var preview struct{ Fingerprint string }
	request("POST", base+"/cart/preview", `{"expected_generation":1,"expected_version":2}`, guest.Token, "", nil, "", 200, &preview)
	var commit struct {
		Order struct {
			ID string `json:"order_id"`
		} `json:"order"`
		Grant *ordering.OrderReadGrant `json:"order_read_grant"`
	}
	request("POST", base+"/cart/commit", fmt.Sprintf(`{"expected_generation":1,"expected_version":2,"preview_fingerprint":%q,"payment_method":"STRIPE"}`, preview.Fingerprint), guest.Token, "p84c-commit", cookie, "", 200, &commit)
	if commit.Grant == nil || commit.Grant.Token == "" || commit.Order.ID == "" {
		t.Fatal("commit did not issue ORDER_READ")
	}
	markers = append(markers, commit.Grant.Token)
	orderBase := base + "/orders/" + commit.Order.ID
	var granted ordering.OrderReadGrant
	request("POST", orderBase+"/read-grant", "", guest.Token, "", nil, "", 200, &granted)
	if granted.Token == "" || granted.Token == commit.Grant.Token {
		t.Fatal("independent ORDER_READ was not issued")
	}
	markers = append(markers, granted.Token)
	statusPath := "/v1/guest/orders/" + commit.Order.ID + "/status"
	for _, token := range []string{commit.Grant.Token, granted.Token} {
		request("GET", statusPath, "", token, "", nil, "", 200, nil)
	}
	request("GET", statusPath, "", "ORDER_READ_SECRETMARKER_P84C", "", nil, "", 403, nil)
	request("POST", orderBase+"/checkout/resume", "", "", "", &http.Cookie{Name: cookie.Name, Value: "checkout_proof_SECRETMARKER_P84C"}, "", 403, nil)
	staffPath := pf.staffPath(f.TableID, guest.Session.ID, "/orders/"+commit.Order.ID+"/payment")
	request("GET", staffPath, "", badStaff, "", nil, "", 403, nil)
	request("GET", staffPath, "", pf.creds.Cashier.Token, "", nil, "", 200, nil)
	process := func() {
		t.Helper()
		if worked, err := service.ProcessNext(ctx); err != nil || !worked {
			t.Fatalf("worker: worked=%v err=%v", worked, err)
		}
	}
	process() // synthetic Stripe 500: UNKNOWN attempt, released retry lease
	var state, work, safeCode string
	var attempts int
	var released, scheduled bool
	err = f.Pool.QueryRow(ctx, `SELECT a.state,w.status,w.safe_error_code,w.attempts,w.lease_token IS NULL AND w.lease_until IS NULL,w.available_at>w.updated_at
 FROM stripe_payment_attempts a JOIN stripe_payment_work w USING(order_id) WHERE a.order_id=$1 AND w.work_type='CREATE'`, commit.Order.ID).
		Scan(&state, &work, &safeCode, &attempts, &released, &scheduled)
	if err != nil || state != "UNKNOWN" || work != "READY" || safeCode != "PROVIDER_OUTCOME_UNKNOWN" || attempts != 1 || !released || !scheduled {
		t.Fatalf("retry not persisted: state=%s work=%s code=%s attempts=%d released=%v scheduled=%v err=%v", state, work, safeCode, attempts, released, scheduled, err)
	}
	// Advance only fixture work eligibility; do not sleep or alter the runner.
	if _, err = f.Pool.Exec(ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE order_id=$1 AND work_type='CREATE'`, commit.Order.ID); err != nil {
		t.Fatal(err)
	}
	process()
	err = f.Pool.QueryRow(ctx, `SELECT a.state,w.status,w.attempts,w.lease_token IS NULL AND w.lease_until IS NULL FROM stripe_payment_attempts a JOIN stripe_payment_work w USING(order_id) WHERE a.order_id=$1 AND w.work_type='CREATE'`, commit.Order.ID).
		Scan(&state, &work, &attempts, &released)
	if err != nil || state != "OPEN" || work != "DONE" || attempts != 2 || !released {
		t.Fatalf("retry did not recover: state=%s work=%s attempts=%d released=%v err=%v", state, work, attempts, released, err)
	}
	var private payments.PrivateCheckout
	request("POST", orderBase+"/checkout/resume", "", "", "", cookie, "", 200, &private)
	if private.URL != checkoutURL {
		t.Fatal("valid CHECKOUT_RESUME did not return the synthetic private URL")
	}
	mu.Lock()
	keysOK := len(createKeys) == 2 && createKeys[0] != "" && createKeys[0] == createKeys[1] && retrieves == 1
	paid := checkout
	mu.Unlock()
	if !keysOK {
		t.Fatal("expected one same-key retry and one resume retrieval")
	}
	// Extra metadata is untrusted provider input. Put the markers in the raw
	// signed body too, so accidental webhook body logging is observable.
	paid.Metadata = make(map[string]string, len(checkout.Metadata)+1)
	for key, value := range checkout.Metadata {
		paid.Metadata[key] = value
	}
	paid.Metadata["private_test_marker"] = strings.Join(markers, " ")
	paid.Status, paid.PaymentStatus = stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid
	body, signature, err := testpayments.SignedCheckoutEvent(paid, "evt_P84C", "checkout.session.completed", webhookSecret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	markers = append(markers, signature)
	request("POST", "/v1/payments/stripe/webhook", string(body)+" ", "", "", nil, signature, 400, nil)
	var inboxCount int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM stripe_webhook_events`).Scan(&inboxCount); err != nil || inboxCount != 0 {
		t.Fatalf("invalid signature was persisted: count=%d err=%v", inboxCount, err)
	}
	request("POST", "/v1/payments/stripe/webhook", string(body), "", "", nil, signature, 200, nil)
	process()
	if err = f.Pool.QueryRow(ctx, `SELECT state FROM stripe_payment_attempts WHERE order_id=$1`, commit.Order.ID).Scan(&state); err != nil || state != "PAID" {
		t.Fatalf("verified webhook not processed: state=%s err=%v", state, err)
	}
	server.Close() // join HTTP goroutines before restoring global destinations
	output := stopCapture()
	for _, probe := range []string{"p84c-slog-probe", "p84c-log-probe", "p84c-stderr-probe", "p84c-stdout-probe", "p84c-http-probe"} {
		if !strings.Contains(output, probe) {
			t.Errorf("log capture missed %s", probe)
		}
	}
	for i, marker := range markers {
		if marker == "" || strings.Contains(output, marker) {
			t.Errorf("runtime logs leaked synthetic marker index %d (or marker was empty)", i)
		}
	}
}
