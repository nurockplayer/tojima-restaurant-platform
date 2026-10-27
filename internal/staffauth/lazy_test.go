package staffauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

func TestLazyValidatesWithoutDiscovery(t *testing.T) {
	var requests atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer issuer.Close()
	cfg := Config{IssuerURL: issuer.URL, ClientID: "client", ClientSecret: "synthetic", PublicOrigin: testOrigin}
	l, err := NewLazy(&Store{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("constructor performed network I/O")
	}
	mux := http.NewServeMux()
	l.Mount(mux)
	for _, route := range []string{"login", "step-up", "callback", "backchannel-logout", "session", "logout"} {
		r := httptest.NewRequest("POST", "/staff/auth/"+route, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"error":"staff_auth_unavailable"`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
	}
	if _, err := NewLazy(nil, cfg); err == nil {
		t.Fatal("nil store accepted")
	}
	cfg.PublicOrigin = "http://other.example"
	if _, err := NewLazy(&Store{}, cfg); err == nil {
		t.Fatal("invalid origin accepted")
	}
}

func TestLazyQueueOverflowKeepsSessionsUnavailable(t *testing.T) {
	ctx, pool, store := staffAuthTestStore(t)
	token, _, err := testCreateSession(t, ctx, store, "overflow-subject", "overflow-sid", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	var attempts atomic.Int32
	var issuerURL string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if !available.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuerURL, "authorization_endpoint": issuerURL + "/authorize", "token_endpoint": issuerURL + "/token", "jwks_uri": issuerURL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	}))
	defer issuer.Close()
	issuerURL = issuer.URL
	l, err := NewLazy(store, Config{IssuerURL: issuerURL, ClientID: "client", ClientSecret: "synthetic", PublicOrigin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	l.Mount(mux)
	for i := 0; i <= maxPendingLogoutCount; i++ {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/staff/auth/backchannel-logout", strings.NewReader("logout_token=unverified")))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("queued request %d: %d", i, w.Code)
		}
	}
	l.pendingMu.Lock()
	overflow := l.pendingOverflow
	l.pendingMu.Unlock()
	if !overflow {
		t.Fatal("queue overflow was not recorded")
	}

	l.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for attempts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() == 0 {
		t.Fatal("discovery did not start")
	}
	available.Store(true)
	deadline = time.Now().Add(5 * time.Second)
	for attempts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatal("discovery did not recover")
	}
	if l.h.Load() != nil {
		t.Fatal("handler activated after dropping a back-channel notification")
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/staff/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	if _, err := l.Resolve(ctx, r); err != authz.ErrDenied {
		t.Fatalf("overflowed session Resolve = %v, want denied", err)
	}
	if _, err := store.ResolveSession(ctx, token); err != nil {
		t.Fatalf("untrusted queue overflow revoked the persisted session: %v", err)
	}
}

func TestLazyDiscoveryRecoversAndResolveSurvivesOutage(t *testing.T) {
	ctx, pool, store := staffAuthTestStore(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	var attempts atomic.Int32
	var issuerURL string
	muxIssuer := http.NewServeMux()
	issuer := httptest.NewServer(muxIssuer)
	defer issuer.Close()
	issuerURL = issuer.URL
	muxIssuer.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if !available.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuerURL, "authorization_endpoint": issuerURL + "/authorize", "token_endpoint": issuerURL + "/token", "jwks_uri": issuerURL + "/jwks", "end_session_endpoint": issuerURL + "/end-session", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	muxIssuer.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	})
	muxIssuer.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	muxIssuer.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unused", http.StatusBadRequest) })
	muxIssuer.HandleFunc("/end-session", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	l, err := NewLazy(store, Config{IssuerURL: issuerURL, ClientID: "client", ClientSecret: "synthetic", PublicOrigin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	l.Mount(mux)
	discoveryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	l.Start(discoveryCtx)
	deadline := time.Now().Add(5 * time.Second)
	for attempts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() == 0 {
		t.Fatal("discovery did not start")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/staff/auth/login", nil))
	if w.Code != 503 {
		t.Fatalf("before discovery: %d", w.Code)
	}
	token, _, err := testCreateSession(t, ctx, store, "lazy-subject", "lazy-sid", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, err := testCreateSession(t, ctx, store, "lazy-other-subject", "lazy-other-sid", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/staff/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	if _, err := l.Resolve(ctx, r); err != authz.ErrDenied {
		t.Fatalf("resolve before discovery = %v, want denied", err)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/staff/auth/backchannel-logout", strings.NewReader("")))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"error":"staff_auth_unavailable"`) {
		t.Fatalf("back-channel before discovery: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/staff/auth/session", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"error":"staff_auth_unavailable"`) {
		t.Fatalf("session before discovery: %d %s", w.Code, w.Body.String())
	}
	queuedLogout := map[string]any{"iss": issuerURL, "aud": "client", "exp": time.Now().Add(2 * time.Second).Unix(), "iat": time.Now().Unix(), "jti": "lazy-queued-backchannel", "sid": "lazy-sid", "events": map[string]any{BackchannelLogoutEvent: map[string]any{}}}
	queuedToken := signStaffToken(t, key, queuedLogout)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/staff/auth/backchannel-logout", strings.NewReader(url.Values{"logout_token": {queuedToken}}.Encode())))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("queued back-channel before discovery: %d %s", w.Code, w.Body.String())
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_lazy_backchannel() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected lazy revocation failure'; END; $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER reject_lazy_backchannel BEFORE UPDATE ON staff_sessions
		FOR EACH ROW WHEN (NEW.revoked_reason = 'backchannel_logout') EXECUTE FUNCTION reject_lazy_backchannel()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP TRIGGER IF EXISTS reject_lazy_backchannel ON staff_sessions")
		_, _ = pool.Exec(ctx, "DROP FUNCTION IF EXISTS reject_lazy_backchannel()")
	})
	deadline = time.Now().Add(5 * time.Second)
	for attempts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatal("discovery did not retry while issuer remained unavailable")
	}
	available.Store(true)
	deadline = time.Now().Add(5 * time.Second)
	for attempts.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() < 3 {
		t.Fatal("discovery did not recover")
	}
	// The token is now expired, but it was valid when received. A transient DB
	// failure must also keep the handler unpublished until the revoke commits.
	deadline = time.Now().Add(5 * time.Second)
	drained := false
	retryPending := false
	for time.Now().Before(deadline) {
		l.pendingMu.Lock()
		pendingCount := len(l.pending)
		l.pendingMu.Unlock()
		if pendingCount == 0 {
			drained = true
		} else if drained {
			retryPending = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !retryPending {
		t.Fatal("failed queued revocation was not retained for retry")
	}
	if l.h.Load() != nil {
		t.Fatal("handler activated before queued revocation committed")
	}
	if _, err := pool.Exec(ctx, "DROP TRIGGER reject_lazy_backchannel ON staff_sessions"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for l.h.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if l.h.Load() == nil {
		t.Fatal("handler did not activate after queued revocation retry succeeded")
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/staff/auth/login", nil))
	if w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), issuerURL+"/authorize?") {
		t.Fatalf("after discovery: %d %s", w.Code, w.Header().Get("Location"))
	}
	if _, err := l.Resolve(ctx, r); err != authz.ErrDenied {
		t.Fatalf("queued logout session after discovery = %v, want denied", err)
	}
	otherRequest := httptest.NewRequest("GET", "/v1/staff/", nil)
	otherRequest.AddCookie(&http.Cookie{Name: SessionCookie, Value: otherToken})
	if actor, err := l.Resolve(ctx, otherRequest); err != nil || actor.SubjectID != "lazy-other-subject" {
		t.Fatalf("unrevoked session after discovery: actor=%+v err=%v", actor, err)
	}
	available.Store(false)
	before := attempts.Load()
	if actor, err := l.Resolve(ctx, otherRequest); err != nil || actor.SubjectID != "lazy-other-subject" {
		t.Fatalf("resolve during outage after discovery: %v", err)
	}
	if attempts.Load() != before {
		t.Fatal("Resolve contacted the issuer")
	}
	claims := map[string]any{"iss": issuerURL, "aud": "client", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "jti": "lazy-backchannel", "sid": "lazy-other-sid", "events": map[string]any{BackchannelLogoutEvent: map[string]any{}}}
	raw := signStaffToken(t, key, claims)
	w = httptest.NewRecorder()
	backchannelRequest := httptest.NewRequest(http.MethodPost, "/staff/auth/backchannel-logout", strings.NewReader(url.Values{"logout_token": {raw}}.Encode()))
	backchannelRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(w, backchannelRequest)
	if w.Code != http.StatusOK {
		t.Fatalf("back-channel after discovery: %d %s", w.Code, w.Body.String())
	}
	if _, err := l.Resolve(ctx, otherRequest); err != authz.ErrDenied {
		t.Fatalf("session after back-channel logout = %v, want denied", err)
	}
}

// A private discovery address must not become the token issuer or allow a
// mismatched discovery document to replace the configured public issuer.
func TestPrivateDiscoveryKeepsPublicIssuerValidation(t *testing.T) {
	issuer := newFakeStaffIssuer(t)
	const publicIssuer = "https://idp.example.invalid/realms/tojima"
	for _, metadataIssuer := range []string{publicIssuer, "https://wrong.example.invalid/realms/tojima"} {
		t.Run(metadataIssuer, func(t *testing.T) {
			client := &http.Client{Transport: discoveryRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "keycloak:8080" {
					if r.URL.Path != "/realms/tojima/.well-known/openid-configuration" {
						t.Errorf("discovery path = %s", r.URL.Path)
					}
					w := httptest.NewRecorder()
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"issuer": metadataIssuer, "authorization_endpoint": publicIssuer + "/authorize", "token_endpoint": issuer.server.URL + "/token", "jwks_uri": issuer.server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
					return w.Result(), nil
				}
				return issuer.server.Client().Transport.RoundTrip(r)
			})}
			h, err := NewHandler(context.Background(), &Store{}, Config{IssuerURL: publicIssuer, DiscoveryURL: "http://keycloak:8080/realms/tojima", ClientID: "staff-client", ClientSecret: "synthetic", PublicOrigin: testOrigin, HTTPClient: client})
			if metadataIssuer != publicIssuer {
				if err == nil {
					t.Fatal("mismatched metadata issuer accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			claims := issuer.claims()
			claims["iss"] = publicIssuer
			if _, err := h.idVerifier.Verify(context.Background(), signStaffToken(t, issuer.key, claims)); err != nil {
				t.Fatalf("public issuer rejected: %v", err)
			}
			claims["iss"] = "http://keycloak:8080/realms/tojima"
			if _, err := h.idVerifier.Verify(context.Background(), signStaffToken(t, issuer.key, claims)); err == nil {
				t.Fatal("private address accepted as token issuer")
			}
		})
	}
}

type discoveryRoundTripper func(*http.Request) (*http.Response, error)

func (f discoveryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLazyFailsClosedBeforeDiscoveryWithLiveSession(t *testing.T) {
	ctx, pool, store := staffAuthTestStore(t)
	// Never start discovery: this models a restarted server with the IdP down.
	l, err := NewLazy(store, Config{IssuerURL: "http://127.0.0.1:1", ClientID: "client", ClientSecret: "synthetic", PublicOrigin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	l.Mount(mux)
	token, _, err := testCreateSession(t, ctx, store, "cold-logout-subject", "cold-logout-sid", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, route, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/staff/auth/"+route, nil)
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("local auth response may be cached")
		}
		return w
	}
	if w := call("GET", "session", ""); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"error":"staff_auth_unavailable"`) {
		t.Fatalf("session before discovery: %d %s", w.Code, w.Body.String())
	}
	if _, err := store.ResolveSession(ctx, token); err != nil {
		t.Fatalf("test session is not live: %v", err)
	}
	r := httptest.NewRequest("GET", "/v1/staff/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	if _, err := l.Resolve(ctx, r); err != authz.ErrDenied {
		t.Fatalf("Resolve before discovery = %v, want denied", err)
	}
	if w := call("POST", "backchannel-logout", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("back-channel before discovery: %d %s", w.Code, w.Body.String())
	}
}
