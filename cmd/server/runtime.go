package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/config"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/httpapi"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// The future MOD-08 Keycloak adapter must implement httpapi.IdentityResolver
// and authz.Authorizer (including current memberships). There is deliberately
// no env switch or header that can enable staff access in this composition.
func productionHandler(api, spa http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", httpapi.NewHandler())
	deny := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})
	for _, prefix := range []string{"/v1/staff", "/v1/admin"} {
		mux.Handle(prefix, deny)
		mux.Handle(prefix+"/", deny)
	}
	mux.Handle("/v1", http.NotFoundHandler())
	mux.Handle("/v1/", api)
	mux.Handle("/", spa)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if deniedNamespace(r) {
			deny.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// deniedNamespace checks the decoded and the escaped path, so encoded
// separators (%2F, %2f, %252F) cannot route staff/admin requests elsewhere.
func deniedNamespace(r *http.Request) bool {
	for _, raw := range []string{r.URL.Path, r.URL.EscapedPath()} {
		p := strings.ToLower(raw)
		for i := 0; i < 3 && strings.Contains(p, "%"); i++ { // undo double encoding
			if u, err := url.PathUnescape(p); err == nil {
				p = strings.ToLower(u)
			} else {
				break
			}
		}
		p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
		for _, prefix := range []string{"/v1/staff", "/v1/admin"} {
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				return true
			}
		}
	}
	return false
}

type providerResolver struct {
	ref      string
	resolved payments.ResolvedProvider
}

func (r providerResolver) Resolve(_ context.Context, ref string) (payments.ResolvedProvider, error) {
	if r.ref == "" || ref != r.ref {
		return payments.ResolvedProvider{}, stripeprovider.ErrUnconfigured
	}
	return r.resolved, nil
}

type runtime struct {
	handler  http.Handler
	payments *payments.Service
	close    func()
}

func composeRuntime(ctx context.Context, cfg config.Config) (*runtime, error) {
	if cfg.HealthOnly {
		return &runtime{handler: httpapi.NewHandler(), close: func() {}}, nil
	}
	spa, closeSPA, err := newSPAHandler(cfg.WebRoot)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			closeSPA()
		}
	}()
	// pgx reads libpq PG* only when DATABASE_URL is empty. Never echo parse or
	// connect errors: they can contain the DSN, credentials, or server payloads.
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, errors.New("invalid database configuration; check DATABASE_URL or PG* variables")
	}
	poolConfig.MaxConns = 4
	poolConfig.ConnConfig.ConnectTimeout = 10 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("cannot initialize database pool")
	}
	defer func() {
		if !success {
			pool.Close()
		}
	}()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(startup); err != nil {
		return nil, errors.New("database unavailable; check connectivity and credentials")
	}
	api, worker, err := composeAPI(pool, cfg)
	if err != nil {
		return nil, err
	}

	success = true
	return &runtime{handler: productionHandler(api, spa), payments: worker, close: func() { pool.Close(); closeSPA() }}, nil
}

// composeAPI constructs services without migrations, seed writes or network I/O.
func composeAPI(pool *pgxpool.Pool, cfg config.Config) (http.Handler, *payments.Service, error) {
	ops, err := operations.New(pool)
	if err != nil {
		return nil, nil, errors.New("cannot initialize operations service")
	}
	menus, err := catalog.New(pool)
	if err != nil {
		return nil, nil, errors.New("cannot initialize catalog service")
	}
	var resolver payments.ProviderResolver
	if cfg.Stripe.Enabled {
		provider, err := stripeprovider.New(stripeprovider.Config{
			APIKey: cfg.Stripe.SecretKey, Mode: stripeprovider.ModeConnectedDirectFixture,
			RequestTimeout: 10 * time.Second, MaxAmountJPY: cfg.Stripe.MaxAmountJPY,
			SuccessURL: cfg.Stripe.SuccessURL, CancelURL: cfg.Stripe.CancelURL,
		})
		if err != nil {
			return nil, nil, errors.New("cannot initialize Stripe test provider")
		}
		resolver = providerResolver{ref: cfg.Stripe.CredentialRef, resolved: payments.ResolvedProvider{
			Provider: provider, Account: stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: cfg.Stripe.AccountID},
			ProfileFingerprint: sha256.Sum256([]byte(stripeprovider.APIVersion + "|server-test-direct|" + cfg.Stripe.SuccessURL + "|" + cfg.Stripe.CancelURL + "|" + strconv.FormatInt(cfg.Stripe.MaxAmountJPY, 10))),
		}}
	}
	pay, err := payments.New(pool, ops, resolver, cfg.Payments)
	if err != nil {
		return nil, nil, errors.New("cannot initialize payment service")
	}
	sessions, err := tablesession.New(pool, cfg.Sessions, pay)
	if err != nil {
		return nil, nil, errors.New("cannot initialize table session service")
	}
	orders, err := ordering.NewWithOrderPolicy(pool, sessions, menus, pay)
	if err != nil {
		return nil, nil, errors.New("cannot initialize ordering service")
	}
	var api http.Handler = httpapi.NewGuestHandler(sessions, menus, orders, ops)
	var worker *payments.Service
	if cfg.Stripe.Enabled {
		proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: cfg.Stripe.SigningKeys, TTL: cfg.Stripe.ProofTTL})
		if err != nil {
			return nil, nil, errors.New("cannot initialize checkout proofs")
		}
		api, err = httpapi.NewPaymentsRuntime(sessions, menus, orders, ops, pay, httpapi.DenyIdentity{}, authz.DenyAll{}, httpapi.PaymentsHTTPConfig{
			Proofs: proofs, Origin: cfg.Stripe.Origin, WebhookSecrets: []string{cfg.Stripe.WebhookSecret},
		})
		if err != nil {
			return nil, nil, errors.New("cannot initialize payments HTTP configuration")
		}
		worker = pay
	}
	return api, worker, nil
}

// Stop cancels processing and waits for the worker for at most the shutdown deadline.
var workerStopTimeout = 15 * time.Second

func startPaymentWorker(parent context.Context, process func(context.Context) (bool, error)) func() {
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
				if ctx.Err() != nil {
					return
				}
				if _, err := process(ctx); err != nil && ctx.Err() == nil {
					// Provider/DB errors can carry secrets. Durable retry state stays in DB.
					log.Print("payment worker iteration failed; durable work will retry")
				}
			}
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(workerStopTimeout):
			log.Print("payment worker did not stop within the shutdown deadline; durable work will be reclaimed after its lease")
		}
	}
}
