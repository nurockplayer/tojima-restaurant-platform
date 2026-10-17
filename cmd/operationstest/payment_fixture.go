package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

const fixtureCredentialRef = "fixture:test-store:v1"

// The map is explicit even for this one-store disposable fixture. There is no
// fallback merchant or provider for an unknown tenant/store configuration.
type fixtureProviderResolver map[string]payments.ResolvedProvider

func (r fixtureProviderResolver) Resolve(_ context.Context, ref string) (payments.ResolvedProvider, error) {
	v, ok := r[ref]
	if !ok {
		return payments.ResolvedProvider{}, errors.New("unknown fixture provider reference")
	}
	return v, nil
}

func configurePaymentFixture(ctx context.Context, f *testguest.Fixture) (*payments.Service, *checkoutproof.Service, string, error) {
	provider := testpayments.NewProvider()
	account := stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_fixtureStoreA"}
	resolver := fixtureProviderResolver{fixtureCredentialRef: {
		Provider: provider, Account: account,
		ProfileFingerprint: sha256.Sum256([]byte(stripeprovider.APIVersion + "|keyless-fixture|https://checkout.example.invalid/")),
	}}
	service, err := payments.New(f.Pool, f.Operations, resolver, payments.Config{
		CreateRetryWindow: 20 * time.Hour, ContinuationTTL: 30 * time.Minute, WorkLeaseTTL: 30 * time.Second,
	})
	if err != nil {
		return nil, nil, "", err
	}
	_, err = service.SetMerchantConfig(ctx, operations.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}, 0, "fixture-bootstrap", payments.MerchantConfigInput{
		Enabled: true, Mode: account.Mode, StripeAccountID: account.StripeAccountID,
		CredentialRef: fixtureCredentialRef, WebhookEndpointRef: "fixture-webhook", APIVersion: stripeprovider.APIVersion,
	})
	if err != nil {
		return nil, nil, "", err
	}
	pepper, signingKey, webhookKey := make([]byte, 32), make([]byte, 32), make([]byte, 32)
	for _, dst := range [][]byte{pepper, signingKey, webhookKey} {
		if _, err = rand.Read(dst); err != nil {
			return nil, nil, "", err
		}
	}
	// These TTLs are explicitly local fixture settings, not production policy.
	f.Sessions, err = tablesession.New(f.Pool, tablesession.Options{
		SessionTTL: 4 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 1000,
		PINTTL: 15 * time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: pepper,
	}, service)
	if err != nil {
		return nil, nil, "", err
	}
	f.Orders, err = ordering.NewWithOrderPolicy(f.Pool, f.Sessions, f.Menus, service)
	if err != nil {
		return nil, nil, "", err
	}
	proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: [][]byte{signingKey}, TTL: 30 * time.Minute})
	if err != nil {
		return nil, nil, "", err
	}
	return service, proofs, "whsec_fixture_" + hex.EncodeToString(webhookKey), nil
}

func runPaymentFixtureWorker(parent context.Context, service *payments.Service) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// The service persists safe retry/error state. Never log provider
				// payloads, URLs, grants or fixture signing material here.
				_, _ = service.ProcessNext(ctx)
			}
		}
	}()
	return func() { cancel(); <-done }
}
