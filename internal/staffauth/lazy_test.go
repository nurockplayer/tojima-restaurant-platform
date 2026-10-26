package staffauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	for _, route := range []string{"login", "step-up", "callback", "backchannel-logout"} {
		r := httptest.NewRequest("POST", "/staff/auth/"+route, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"error":"staff_auth_unavailable"`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
	}
	for _, test := range []struct {
		method, route, origin string
		status                int
	}{
		{"GET", "session", "", 401}, {"GET", "logout", "", 405},
		{"POST", "logout", "https://wrong.example.invalid", 403},
		{"POST", "logout", testOrigin, 303},
	} {
		r := httptest.NewRequest(test.method, "/staff/auth/"+test.route, nil)
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("local %s %s: %d", test.method, test.route, w.Code)
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

func TestLazyDiscoveryRecoversAndResolveSurvivesOutage(t *testing.T) {
	ctx, pool, store := staffAuthTestStore(t)
	var available atomic.Bool
	var attempts atomic.Int32
	var issuerURL string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if !available.Load() {
			w.WriteHeader(503)
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
	// Existing sessions resolve even before discovery has ever succeeded.
	token, _, err := testCreateSession(t, ctx, store, "lazy-subject", "lazy-sid", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/staff/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	if actor, err := l.Resolve(ctx, r); err != nil || actor.SubjectID != "lazy-subject" {
		t.Fatalf("resolve before discovery: %v", err)
	}
	available.Store(true)
	deadline = time.Now().Add(5 * time.Second)
	for l.h.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if l.h.Load() == nil {
		t.Fatal("discovery did not recover")
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/staff/auth/login", nil))
	if w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), issuerURL+"/authorize?") {
		t.Fatalf("after discovery: %d %s", w.Code, w.Header().Get("Location"))
	}
	available.Store(false)
	before := attempts.Load()
	if actor, err := l.Resolve(ctx, r); err != nil || actor.SubjectID != "lazy-subject" {
		t.Fatalf("resolve during outage: %v", err)
	}
	if attempts.Load() != before {
		t.Fatal("Resolve contacted the issuer")
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

func TestLazyLocalSessionAndLogoutBeforeDiscovery(t *testing.T) {
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
	if w := call("GET", "session", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "cold-logout-subject") {
		t.Fatalf("session before discovery: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "logout", ""); w.Code != 405 {
		t.Fatalf("logout GET = %d", w.Code)
	}
	for _, origin := range []string{"", "https://wrong.example.invalid"} {
		if w := call("POST", "logout", origin); w.Code != 403 {
			t.Fatalf("logout wrong Origin = %d", w.Code)
		}
	}
	if _, err := store.ResolveSession(ctx, token); err != nil {
		t.Fatalf("rejected logout revoked session: %v", err)
	}
	w := call("POST", "logout", testOrigin)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != DefaultReturn {
		t.Fatalf("local logout: %d %s", w.Code, w.Header().Get("Location"))
	}
	if cookie := staffResponseCookie(t, w, SessionCookie); cookie.MaxAge != -1 || !cookie.Secure || !cookie.HttpOnly {
		t.Fatal("logout did not clear the secure staff cookie")
	}
	if _, err := store.ResolveSession(ctx, token); err == nil {
		t.Fatal("local logout left session live")
	}
	if w := call("GET", "session", ""); w.Code != 401 {
		t.Fatalf("revoked session probe = %d", w.Code)
	}
	if l.h.Load() != nil {
		t.Fatal("local routes unexpectedly initialized discovery")
	}
}
