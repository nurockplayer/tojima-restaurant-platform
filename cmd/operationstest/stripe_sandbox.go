package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

var errStripeSandboxRuntime = errors.New("Stripe Sandbox runtime configuration failed")

// This uses exactly the existing provider/service architecture. Constructors
// perform no Stripe calls; the durable worker owns subsequent provider work.
func configureStripeSandbox(ctx context.Context, f *testguest.Fixture, tenant string, cfg stripeSandboxConfig) (*payments.Service, *checkoutproof.Service, error) {
	accountID, ok := cfg.accounts[tenant]
	if !ok || f == nil || f.TenantID != "stripe-test-tenant-"+sandboxTenantSuffix(tenant) || f.StoreID != "stripe-test-store-"+sandboxTenantSuffix(tenant) {
		return nil, nil, errStripeSandboxRuntime
	}
	provider, err := stripeprovider.New(stripeprovider.Config{
		APIKey: cfg.apiKey, Mode: stripeprovider.ModeConnectedDirectFixture,
		RequestTimeout: 10 * time.Second, MaxAmountJPY: cfg.maxAmountJPY,
		SuccessURL: cfg.successURL, CancelURL: cfg.cancelURL,
	})
	if err != nil {
		return nil, nil, errStripeSandboxRuntime
	}
	account := stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: accountID}
	ref := "stripe-test:" + sandboxTenantSuffix(tenant) + ":v1"
	resolver := fixtureProviderResolver{ref: {
		Provider: sandboxProvider{provider}, Account: account,
		ProfileFingerprint: sha256.Sum256([]byte(stripeprovider.APIVersion + "|stripe-sandbox-direct|" + cfg.successURL + "|" + cfg.cancelURL + "|" + strconv.FormatInt(cfg.maxAmountJPY, 10))),
	}}
	service, proofs, err := configurePaymentGraph(ctx, f, resolver, ref, "stripe-test-cli", "sandbox-bootstrap", account)
	if err != nil {
		return nil, nil, errStripeSandboxRuntime
	}
	return service, proofs, nil
}

func sandboxTenantSuffix(tenant string) string {
	switch tenant {
	case "A":
		return "a"
	case "B":
		return "b"
	case "C":
		return "c"
	default:
		return ""
	}
}

// Apply an additional test-runtime-only hosted Checkout allowlist without
// changing the keyless fake adapter or production provider contract.
type sandboxProvider struct{ *stripeprovider.Provider }

// Stripe Checkout URLs carry a client-side fragment (#fid…); it is allowed.
func sandboxCheckoutURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "checkout.stripe.com" && u.User == nil && u.Opaque == ""
}

func (p sandboxProvider) CreateCheckout(ctx context.Context, req stripeprovider.CreateCheckoutRequest) (stripeprovider.CheckoutResult, error) {
	r, err := p.Provider.CreateCheckout(ctx, req)
	if err == nil && !sandboxCheckoutURL(r.URL) {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrOutcomeUnknown
	}
	return r, err
}

func (p sandboxProvider) RetrieveCheckout(ctx context.Context, account stripeprovider.AccountContext, id string) (stripeprovider.CheckoutResult, error) {
	r, err := p.Provider.RetrieveCheckout(ctx, account, id)
	if err == nil && !sandboxCheckoutURL(r.URL) {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrUnsafeResponse
	}
	return r, err
}

func (p sandboxProvider) ExpireCheckout(ctx context.Context, account stripeprovider.AccountContext, id, key string) (stripeprovider.CheckoutResult, error) {
	r, err := p.Provider.ExpireCheckout(ctx, account, id, key)
	if err == nil && !sandboxCheckoutURL(r.URL) {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrOutcomeUnknown
	}
	return r, err
}

func stripeSandboxHostGuard(next http.Handler, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

var _ payments.Provider = sandboxProvider{}
