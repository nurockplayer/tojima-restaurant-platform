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
	"github.com/nurockplayer/tojima-restaurant-platform/internal/staffauth"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// Staff access is enabled only by STAFF_AUTH_MODE=oidc and still requires a
// verified OIDC session and current membership. No header or env grants an identity.
func productionHandler(api, spa http.Handler, staff *staffauth.Lazy, publicOrigin string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", httpapi.NewHandler())
	deny := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})
	for _, prefix := range []string{"/v1/staff", "/v1/admin"} {
		next := http.Handler(deny)
		if staff != nil {
			next = staffauth.RequireOrigin(publicOrigin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Reject anonymous requests before resource lookups in the API.
				if _, err := staff.Resolve(r.Context(), r); err != nil {
					deny.ServeHTTP(w, r)
					return
				}
				api.ServeHTTP(w, r)
			}))
		}
		mux.Handle(prefix, next)
		mux.Handle(prefix+"/", next)
	}
	if staff != nil {
		auth := http.NewServeMux()
		staff.Mount(auth)
		mux.Handle("/staff/auth/", auth)
	}
	mux.Handle("/v1", http.NotFoundHandler())
	mux.Handle("/v1/", api)
	mux.Handle("/", spa)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Reject ambiguous staff paths instead of letting nested ServeMux instances
		// interpret escapes, case or separators differently. Canonical paths route
		// unchanged; disabled mode retains the existing namespace denial.
		if (staff == nil && deniedNamespace(r)) || (staff != nil && ambiguousStaffPath(r)) {
			deny.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// deniedNamespace checks the decoded and the escaped path, so encoded
// separators (%2F, %2f, %252F) cannot route staff/admin requests elsewhere.
func deniedNamespace(r *http.Request) bool { return protectedNamespace(r, false) }

func protectedNamespace(r *http.Request, includeAuth bool) bool {
	prefixes := []string{"/v1/staff", "/v1/admin"}
	if includeAuth {
		prefixes = append(prefixes, "/staff/auth")
	}
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
		for _, prefix := range prefixes {
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				return true
			}
		}
	}
	return false
}

func ambiguousStaffPath(r *http.Request) bool {
	if !protectedNamespace(r, true) {
		return false
	}
	p := r.URL.Path
	canonicalNamespace := false
	for _, prefix := range []string{"/v1/staff", "/v1/admin", "/staff/auth"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			canonicalNamespace = true
		}
	}
	return !canonicalNamespace || r.URL.EscapedPath() != p || strings.Contains(p, "%") || strings.Contains(p, "\\") || path.Clean(p) != p
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
	api, worker, staff, err := composeAPI(pool, cfg)
	if err != nil {
		return nil, err
	}

	discoveryCtx, stopDiscovery := context.WithCancel(ctx)
	if staff != nil {
		staff.Start(discoveryCtx)
	}
	success = true
	return &runtime{handler: productionHandler(api, spa, staff, cfg.StaffAuth.PublicOrigin), payments: worker, close: func() { stopDiscovery(); pool.Close(); closeSPA() }}, nil
}

// composeAPI constructs services without migrations, seed writes or network I/O.
func composeAPI(pool *pgxpool.Pool, cfg config.Config) (http.Handler, *payments.Service, *staffauth.Lazy, error) {
	ops, err := operations.New(pool)
	if err != nil {
		return nil, nil, nil, errors.New("cannot initialize operations service")
	}
	menus, err := catalog.New(pool)
	if err != nil {
		return nil, nil, nil, errors.New("cannot initialize catalog service")
	}
	var resolver payments.ProviderResolver
	if cfg.Stripe.Enabled {
		provider, err := stripeprovider.New(stripeprovider.Config{
			APIKey: cfg.Stripe.SecretKey, Mode: stripeprovider.ModeConnectedDirectFixture,
			RequestTimeout: 10 * time.Second, MaxAmountJPY: cfg.Stripe.MaxAmountJPY,
			SuccessURL: cfg.Stripe.SuccessURL, CancelURL: cfg.Stripe.CancelURL,
		})
		if err != nil {
			return nil, nil, nil, errors.New("cannot initialize Stripe test provider")
		}
		resolver = providerResolver{ref: cfg.Stripe.CredentialRef, resolved: payments.ResolvedProvider{
			Provider: provider, Account: stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: cfg.Stripe.AccountID},
			ProfileFingerprint: sha256.Sum256([]byte(stripeprovider.APIVersion + "|server-test-direct|" + cfg.Stripe.SuccessURL + "|" + cfg.Stripe.CancelURL + "|" + strconv.FormatInt(cfg.Stripe.MaxAmountJPY, 10))),
		}}
	}
	pay, err := payments.New(pool, ops, resolver, cfg.Payments)
	if err != nil {
		return nil, nil, nil, errors.New("cannot initialize payment service")
	}
	sessions, err := tablesession.New(pool, cfg.Sessions, pay)
	if err != nil {
		return nil, nil, nil, errors.New("cannot initialize table session service")
	}
	orders, err := ordering.NewWithOrderPolicy(pool, sessions, menus, pay)
	if err != nil {
		return nil, nil, nil, errors.New("cannot initialize ordering service")
	}
	var identity httpapi.IdentityResolver = httpapi.DenyIdentity{}
	var authorizer authz.Authorizer = authz.DenyAll{}
	var staff *staffauth.Lazy
	if cfg.StaffAuth.Enabled {
		store, err := staffauth.NewStore(pool)
		if err != nil {
			return nil, nil, nil, errors.New("cannot initialize staff auth store")
		}
		staff, err = staffauth.NewLazy(store, staffauth.Config{
			IssuerURL: cfg.StaffAuth.IssuerURL, DiscoveryURL: cfg.StaffAuth.DiscoveryURL, ClientID: cfg.StaffAuth.ClientID,
			ClientSecret: cfg.StaffAuth.ClientSecret, PublicOrigin: cfg.StaffAuth.PublicOrigin,
		})
		if err != nil {
			return nil, nil, nil, errors.New("cannot initialize staff auth configuration")
		}
		identity, authorizer = staff, authz.PolicyAuthorizer{Directory: store}
	}
	var api http.Handler = httpapi.NewGuestHandler(sessions, menus, orders, ops)
	if staff != nil {
		api = httpapi.NewOperationsRuntime(sessions, menus, orders, ops, identity, authorizer)
	}
	var worker *payments.Service
	if cfg.Stripe.Enabled {
		ops.EnableCardPayments()
		proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: cfg.Stripe.SigningKeys, TTL: cfg.Stripe.ProofTTL})
		if err != nil {
			return nil, nil, nil, errors.New("cannot initialize checkout proofs")
		}
		api, err = httpapi.NewPaymentsRuntime(sessions, menus, orders, ops, pay, identity, authorizer, httpapi.PaymentsHTTPConfig{
			Proofs: proofs, Origin: cfg.Stripe.Origin, WebhookSecrets: []string{cfg.Stripe.WebhookSecret},
		})
		if err != nil {
			return nil, nil, nil, errors.New("cannot initialize payments HTTP configuration")
		}
		worker = pay
	}
	if staff != nil {
		mux := http.NewServeMux()
		mux.Handle("/v1/admin/", httpapi.NewMenuHandler(menus, identity, authorizer))
		mux.Handle("/", api)
		api = mux
	}
	return api, worker, staff, nil
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
