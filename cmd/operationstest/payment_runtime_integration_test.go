package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/httpapi"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
	stripe "github.com/stripe/stripe-go/v87"
)

// Exercise the actual command constructors without calling any provider. CREATE
// stays queued: synthetic signed ingress tests the real HTTP clock and durable
// inbox, not external delivery or a fabricated paid shortcut in the runtime.
func TestPaymentRuntimeCompositionPostgres(t *testing.T) {
	if stripe.APIVersion != stripeprovider.APIVersion {
		t.Fatal("pinned SDK and webhook API version differ")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, sandbox := range []bool{false, true} {
		t.Run(fmt.Sprintf("sandbox=%t", sandbox), func(t *testing.T) {
			ctx := context.Background()
			opts := testguest.Options{CashEnabled: true, CashMode: operations.PayLater}
			if sandbox {
				opts.StripeSandboxTenant = "A"
			}
			f, err := testguest.NewWithOptions(ctx, dsn, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close(ctx)
			var pay *payments.Service
			var proofs *checkoutproof.Service
			var secret string
			if sandbox {
				env := sandboxTestEnvironment()
				cfg, e := loadStripeSandboxConfig(func(k string) string { return env[k] })
				if e != nil {
					t.Fatal(e)
				}
				pay, proofs, err = configureStripeSandbox(ctx, f, "A", cfg)
				secret = cfg.webhookSecret
			} else {
				pay, proofs, secret, err = configurePaymentFixture(ctx, f)
			}
			if err != nil {
				t.Fatal(err)
			}
			identity, directory, creds, err := testoperations.NewCredentialSet(f.TenantID, f.StoreID)
			if err != nil {
				t.Fatal(err)
			}
			h, err := httpapi.NewPaymentsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, pay, identity, authz.PolicyAuthorizer{Directory: directory}, httpapi.PaymentsHTTPConfig{
				Proofs: proofs, Origin: "http://127.0.0.1:8083", AllowLoopbackHTTP: true, WebhookSecrets: []string{secret},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := func(method, path, token string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "http://127.0.0.1:8083"+path, strings.NewReader("{}"))
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
			if err != nil {
				t.Fatal(err)
			}
			cart, err := f.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
			if err != nil {
				t.Fatal(err)
			}
			cart, err = f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "sandbox-line", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
			if err != nil {
				t.Fatal(err)
			}
			_, proof, err := proofs.Issue(time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "sandbox-order", PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe, CheckoutProofHash: [32]byte(proof.Hash), CheckoutProofExpiresAt: proof.ExpiresAt})
			if err != nil {
				t.Fatal(err)
			}
			if committed.OrderReadGrant == nil {
				t.Fatal("ORDER_READ not issued")
			}
			read := request(http.MethodGet, "/v1/guest/orders/"+committed.Order.ID+"/status", committed.OrderReadGrant.Token)
			if read.Code != http.StatusOK {
				t.Fatalf("ORDER_READ route: %d", read.Code)
			}
			if err := f.Sessions.Close(ctx, tablesession.Scope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}, grant.Session.ID, "fixture-manager"); !errors.Is(err, tablesession.ErrCloseBlocked) {
				t.Fatalf("unpaid close gate: %v", err)
			}
			base := "/v1/staff/tenants/" + f.TenantID + "/stores/" + f.StoreID
			for _, route := range []string{"tables", "reviews", "kds"} {
				token := creds.Manager.Token
				if route == "kds" {
					token = creds.Kitchen.Token
				}
				if w := request(http.MethodGet, base+"/operations/"+route, token); w.Code != http.StatusOK {
					t.Fatalf("%s route: %d", route, w.Code)
				}
			}
			orderPath := base + "/tables/" + f.TableID + "/sessions/" + grant.Session.ID + "/orders/" + committed.Order.ID
			for _, route := range []string{"approve-fulfillment", "adjustments/preview", "adjustments", "refunds/missing/retry", "stop-requests", "stop-requests/missing/confirm", "payment/reconcile"} {
				if w := request(http.MethodPost, orderPath+"/"+route, ""); w.Code != http.StatusForbidden {
					t.Fatalf("protected %s route: %d", route, w.Code)
				}
			}
			var attempt, account string
			var amount int64
			if err := f.Pool.QueryRow(ctx, `SELECT attempt_id,stripe_account_id,amount_jpy FROM stripe_payment_attempts WHERE order_id=$1`, committed.Order.ID).Scan(&attempt, &account, &amount); err != nil {
				t.Fatal(err)
			}
			// A real-time signed completed/unpaid event must pass #32's verifier;
			// stale/future signatures fail before creating any durable inbox work.
			now := time.Now().UTC()
			body, err := json.Marshal(map[string]any{
				"id": "evt_composition", "object": "event", "api_version": stripeprovider.APIVersion, "created": now.Unix(), "livemode": false,
				"type": "checkout.session.completed", "account": account, "data": map[string]any{"object": map[string]any{
					"id": "cs_test_composition", "object": "checkout.session", "livemode": false, "mode": "payment", "currency": "jpy", "amount_total": amount,
					"status": "complete", "payment_status": "unpaid", "expires_at": now.Add(time.Hour).Unix(),
					"metadata": map[string]string{"tenant_id": f.TenantID, "store_id": f.StoreID, "order_id": committed.Order.ID, "attempt_id": attempt},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, offset := range []time.Duration{-10 * time.Minute, 10 * time.Minute, 0, 0} {
				ts := now.Add(offset).Unix()
				mac := hmac.New(sha256.New, []byte(secret))
				fmt.Fprintf(mac, "%d.%s", ts, body)
				r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8083/v1/payments/stripe/webhook", strings.NewReader(string(body)))
				r.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%x", ts, mac.Sum(nil)))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want := http.StatusBadRequest
				if offset == 0 {
					want = http.StatusOK
				}
				if w.Code != want {
					t.Fatalf("webhook offset %s: status %d, want %d", offset, w.Code, want)
				}
			}
			var creates, webhooks int
			if err := f.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE w.work_type='CREATE'),count(*) FILTER (WHERE w.work_type='WEBHOOK') FROM stripe_payment_work w LEFT JOIN stripe_webhook_events e USING(webhook_row_id) WHERE w.order_id=$1 OR e.order_id=$1`, committed.Order.ID).Scan(&creates, &webhooks); err != nil || creates != 1 || webhooks != 1 {
				t.Fatalf("durable work create=%d webhook=%d err=%v", creates, webhooks, err)
			}
		})
	}
}
