package staffauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

const testOrigin = "http://127.0.0.1:43127"
const discardedRefresh = "dummy-refresh-must-never-be-persisted"

type fakeStaffIssuer struct {
	server     *httptest.Server
	key        *rsa.PrivateKey
	mu         sync.Mutex
	codes      map[string]url.Values
	edit       func(map[string]any)
	signingKey *rsa.PrivateKey
}

func newFakeStaffIssuer(t *testing.T) *fakeStaffIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeStaffIssuer{key: key, signingKey: key, codes: make(map[string]url.Values)}
	mux := http.NewServeMux()
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/jwks", "end_session_endpoint": f.server.URL + "/end-session", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()
		f.codes[q.Get("state")] = q
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		f.mu.Lock()
		q, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		f.mu.Unlock()
		hash := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		client, secret, basic := r.BasicAuth()
		if !ok || !basic || client != "staff-client" || secret != "test-secret" || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != testOrigin+CallbackPath || q.Get("code_challenge_method") != "S256" || base64.RawURLEncoding.EncodeToString(hash[:]) != q.Get("code_challenge") {
			http.Error(w, "invalid request", 400)
			return
		}
		claims := f.claims()
		claims["nonce"] = q.Get("nonce")
		if f.edit != nil {
			f.edit(claims)
		}
		raw := signStaffToken(t, f.signingKey, claims)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "discarded-access", "token_type": "Bearer", "id_token": raw, "refresh_token": discardedRefresh})
	})
	return f
}

func (f *fakeStaffIssuer) claims() map[string]any {
	now := time.Now()
	return map[string]any{"iss": f.server.URL, "aud": "staff-client", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "auth_time": now.Unix(), "sub": "staff-a", "sid": "sid-a", "acr": "basic"}
}

func signStaffToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type staffOIDCTest struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *Store
	issuer *fakeStaffIssuer
	h      *Handler
	mux    *http.ServeMux
}

func newStaffOIDCTest(t *testing.T) *staffOIDCTest {
	t.Helper()
	ctx, pool, store := staffAuthTestStore(t)
	issuer := newFakeStaffIssuer(t)
	h, err := NewHandler(ctx, store, Config{IssuerURL: issuer.server.URL, ClientID: "staff-client", ClientSecret: "test-secret", PublicOrigin: testOrigin, HTTPClient: issuer.server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)
	return &staffOIDCTest{ctx, pool, store, issuer, h, mux}
}

func (x *staffOIDCTest) request(t *testing.T, method, target, body, origin string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(c)
		}
	}
	w := httptest.NewRecorder()
	x.mux.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("auth response can be cached")
	}
	return w
}

func staffResponseCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
				t.Fatalf("unsafe cookie attributes for %s", name)
			}
			return c
		}
	}
	t.Fatalf("missing cookie %s", name)
	return nil
}

type staffFlow struct {
	query   url.Values
	binding *http.Cookie
}

func (x *staffOIDCTest) begin(t *testing.T, path, returnPath string, cookie *http.Cookie) staffFlow {
	t.Helper()
	w := x.request(t, "GET", path+"?"+url.Values{"return": {returnPath}}.Encode(), "", "", cookie)
	if w.Code != http.StatusFound {
		t.Fatalf("begin status %d", w.Code)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("prompt") != "login" || q.Get("scope") != "openid" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatal("missing OIDC request protections")
	}
	acr := "basic"
	if path == "/staff/auth/step-up" {
		acr = "strong"
		if q.Get("max_age") != "0" {
			t.Fatal("step-up max_age")
		}
	}
	if q.Get("acr_values") != acr {
		t.Fatal("wrong requested acr")
	}
	// Have the fake authorization endpoint observe the challenge before /token.
	auth := httptest.NewRecorder()
	x.issuer.server.Config.Handler.ServeHTTP(auth, httptest.NewRequest("GET", u.String(), nil))
	if auth.Code != 200 {
		t.Fatal("fake authorization failed")
	}
	binding := staffResponseCookie(t, w, TxCookie)
	if binding.MaxAge != 600 {
		t.Fatal("transaction cookie lifetime")
	}
	return staffFlow{q, binding}
}

func (x *staffOIDCTest) callback(t *testing.T, flow staffFlow, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"state": {flow.query.Get("state")}, "code": {flow.query.Get("state")}}
	return x.request(t, "GET", CallbackPath+"?"+q.Encode(), "", "", cookies...)
}

func assertStaffRejected(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 303 || w.Header().Get("Location") != DefaultReturn+"?auth_error=1" {
		t.Fatalf("expected auth rejection, status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	if c := staffResponseCookie(t, w, TxCookie); c.MaxAge != -1 {
		t.Fatal("transaction cookie not cleared")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			t.Fatal("rejected callback issued a session")
		}
	}
}

func (x *staffOIDCTest) login(t *testing.T) *http.Cookie {
	t.Helper()
	flow := x.begin(t, "/staff/auth/login", "/staff/orders?open=1", nil)
	w := x.callback(t, flow, flow.binding)
	if w.Code != 303 || w.Header().Get("Location") != "/staff/orders?open=1" {
		t.Fatalf("login status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	if staffResponseCookie(t, w, TxCookie).MaxAge != -1 {
		t.Fatal("transaction cookie not cleared")
	}
	c := staffResponseCookie(t, w, SessionCookie)
	if c.MaxAge <= 0 || c.Expires.IsZero() {
		t.Fatal("session cookie lifetime missing")
	}
	return c
}

func (x *staffOIDCTest) actor(t *testing.T, cookie *http.Cookie) authz.Actor {
	t.Helper()
	r := httptest.NewRequest("GET", "/staff/", nil)
	r.AddCookie(cookie)
	actor, err := x.h.Resolve(x.ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func TestOIDCLoginAndLocalSession(t *testing.T) {
	x := newStaffOIDCTest(t)
	x.issuer.edit = func(claims map[string]any) { claims["acr"] = "strong" }
	cookie := x.login(t)
	actor := x.actor(t, cookie)
	if actor.SubjectID != "staff-a" || actor.MFA || actor.StepUpFresh {
		t.Fatalf("basic actor=%+v", actor)
	}
	w := x.request(t, "GET", "/staff/auth/session", "", "", cookie)
	var body struct {
		SubjectID string `json:"subject_id"`
		MFA       bool   `json:"mfa"`
		Fresh     bool   `json:"step_up_fresh"`
		Expires   string `json:"expires_at"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.SubjectID != actor.SubjectID || body.MFA || body.Fresh {
		t.Fatal("session JSON mismatch")
	}
	if _, err := time.Parse(time.RFC3339, body.Expires); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"staff_auth_tx", "staff_sessions", "staff_auth_events"} {
		var found bool
		if err := x.pool.QueryRow(x.ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" t WHERE position($1 in t::text)>0)", discardedRefresh).Scan(&found); err != nil || found {
			t.Fatalf("refresh token persisted in %s: found=%v err=%v", table, found, err)
		}
	}
	// Established sessions and logout need no reachable issuer.
	x.issuer.server.Close()
	if x.actor(t, cookie).SubjectID != "staff-a" {
		t.Fatal("local resolution failed during issuer outage")
	}
	if w = x.request(t, "GET", "/staff/auth/session", "", ""); w.Code != 401 || strings.TrimSpace(w.Body.String()) != `{"error":"unauthenticated"}` {
		t.Fatal("anonymous session accepted")
	}
	for _, c := range []*http.Cookie{nil, {Name: SessionCookie, Value: "invalid"}} {
		r := httptest.NewRequest("GET", "/", nil)
		if c != nil {
			r.AddCookie(c)
		}
		if _, err := x.h.Resolve(x.ctx, r); !errors.Is(err, authz.ErrDenied) {
			t.Fatal("anonymous resolver accepted")
		}
	}
}

func TestOIDCTransactionRejection(t *testing.T) {
	for _, mode := range []string{"replay", "wrong binding", "missing binding", "provider error"} {
		t.Run(mode, func(t *testing.T) {
			x := newStaffOIDCTest(t)
			flow := x.begin(t, "/staff/auth/login", DefaultReturn, nil)
			binding := flow.binding
			switch mode {
			case "replay":
				if w := x.callback(t, flow, binding); w.Code != 303 || w.Header().Get("Location") != DefaultReturn {
					t.Fatal("first callback failed")
				}
			case "wrong binding":
				binding = &http.Cookie{Name: TxCookie, Value: "wrong"}
			case "missing binding":
				binding = nil
			case "provider error":
				w := x.request(t, "GET", CallbackPath+"?"+url.Values{"state": {flow.query.Get("state")}, "error": {"access_denied"}}.Encode(), "", "", binding)
				assertStaffRejected(t, w)
			}
			assertStaffRejected(t, x.callback(t, flow, binding))
			if mode == "wrong binding" {
				assertStaffRejected(t, x.callback(t, flow, flow.binding))
			}
		})
	}
}

func TestOIDCRejectInvalidIDTokens(t *testing.T) {
	cases := map[string]func(map[string]any){
		"nonce":     func(c map[string]any) { c["nonce"] = "wrong" },
		"sid":       func(c map[string]any) { delete(c, "sid") },
		"sub":       func(c map[string]any) { delete(c, "sub") },
		"aud":       func(c map[string]any) { c["aud"] = "other-client" },
		"iss":       func(c map[string]any) { c["iss"] = "https://wrong-issuer.example" },
		"exp":       func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"signature": nil,
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			x := newStaffOIDCTest(t)
			x.issuer.edit = edit
			if name == "signature" {
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
				x.issuer.signingKey = key
			}
			flow := x.begin(t, "/staff/auth/login", DefaultReturn, nil)
			assertStaffRejected(t, x.callback(t, flow, flow.binding))
			var count int
			if err := x.pool.QueryRow(x.ctx, "SELECT count(*) FROM staff_sessions").Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid token created session count=%d err=%v", count, err)
			}
		})
	}
}

func TestOIDCUnsafeReturnPaths(t *testing.T) {
	x := newStaffOIDCTest(t)
	for _, path := range []string{"//evil.example", "https://evil.example", `/\evil`} {
		flow := x.begin(t, "/staff/auth/login", path, nil)
		w := x.callback(t, flow, flow.binding)
		if w.Code != 303 || w.Header().Get("Location") != DefaultReturn {
			t.Fatalf("unsafe return accepted: %q", path)
		}
	}
}

func TestOIDCStepUp(t *testing.T) {
	for _, mode := range []string{"strong", "different subject", "basic", "old auth_time", "missing auth_time", "no session cookie", "different session cookie"} {
		t.Run(mode, func(t *testing.T) {
			x := newStaffOIDCTest(t)
			cookie := x.login(t)
			before, err := x.store.ResolveSession(x.ctx, cookie.Value)
			if err != nil {
				t.Fatal(err)
			}
			x.issuer.edit = func(c map[string]any) {
				if mode != "basic" {
					c["acr"] = "strong"
				}
				if mode == "different subject" {
					c["sub"] = "staff-b"
				}
				if mode == "old auth_time" {
					c["auth_time"] = time.Now().Add(-time.Minute).Unix()
				}
				if mode == "missing auth_time" {
					delete(c, "auth_time")
				}
			}
			flow := x.begin(t, "/staff/auth/step-up", "/staff/refunds", cookie)
			presented := cookie
			if mode == "different session cookie" {
				otherToken, _, createErr := x.store.CreateSession(x.ctx, before.SubjectID, "sid-other", false, dbNow(t, x.ctx, x.pool))
				if createErr != nil {
					t.Fatal(createErr)
				}
				presented = &http.Cookie{Name: SessionCookie, Value: otherToken}
			}
			var w *httptest.ResponseRecorder
			if mode == "no session cookie" {
				w = x.callback(t, flow, flow.binding)
			} else {
				w = x.callback(t, flow, flow.binding, presented)
			}
			if mode == "strong" {
				if w.Code != 303 || w.Header().Get("Location") != "/staff/refunds" {
					t.Fatalf("step-up failed status=%d", w.Code)
				}
				newCookie := staffResponseCookie(t, w, SessionCookie)
				if newCookie.Value == cookie.Value || newCookie.Expires.IsZero() || newCookie.MaxAge <= 0 {
					t.Fatal("step-up did not set a rotated session cookie with expiry")
				}
				actor := x.actor(t, newCookie)
				if !actor.MFA || !actor.StepUpFresh {
					t.Fatalf("rotated session actor=%+v", actor)
				}
				if _, err := x.store.ResolveSession(x.ctx, cookie.Value); !errors.Is(err, ErrNoSession) {
					t.Fatalf("old cookie remained live: %v", err)
				}
				after, err := x.store.ResolveSession(x.ctx, newCookie.Value)
				if err != nil || !after.AbsoluteExpires.Equal(before.AbsoluteExpires) {
					t.Fatal("step-up extended session")
				}
			} else {
				assertStaffRejected(t, w)
				stillLive, resolveErr := x.store.ResolveSession(x.ctx, cookie.Value)
				if resolveErr != nil || stillLive.StrongLogin || stillLive.StepUpFresh {
					t.Fatalf("invalid step-up changed session: session=%+v err=%v", stillLive, resolveErr)
				}
				if mode == "different session cookie" {
					if _, resolveErr = x.store.ResolveSession(x.ctx, presented.Value); resolveErr != nil {
						t.Fatalf("unrelated presented session was changed: %v", resolveErr)
					}
				}
			}
		})
	}
	x := newStaffOIDCTest(t)
	if w := x.request(t, "GET", "/staff/auth/step-up", "", ""); w.Code != 401 || strings.TrimSpace(w.Body.String()) != `{"error":"unauthenticated"}` {
		t.Fatal("anonymous step-up accepted")
	}
}

func TestOIDCReloginAndSubjectEpoch(t *testing.T) {
	for _, mode := range []string{"relogin", "held", "restored"} {
		t.Run(mode, func(t *testing.T) {
			x := newStaffOIDCTest(t)
			old := x.login(t)
			flow := x.begin(t, "/staff/auth/login", DefaultReturn, old)
			if mode != "relogin" {
				if err := x.store.Hold(x.ctx, "staff-a", "test", "operator"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "restored" {
				if err := x.store.Restore(x.ctx, "staff-a", "operator"); err != nil {
					t.Fatal(err)
				}
			}
			w := x.callback(t, flow, flow.binding, old)
			if mode == "relogin" {
				if w.Code != 303 || w.Header().Get("Location") != DefaultReturn {
					t.Fatal("relogin failed")
				}
				newCookie := staffResponseCookie(t, w, SessionCookie)
				if newCookie.Value == old.Value {
					t.Fatal("session cookie not rotated")
				}
				_ = x.actor(t, newCookie)
				var reason string
				if err := x.pool.QueryRow(x.ctx, "SELECT revoked_reason FROM staff_sessions WHERE token_sha256=$1", hashStaffCookie(old)).Scan(&reason); err != nil || reason != "relogin" {
					t.Fatalf("old session reason=%q err=%v", reason, err)
				}
			} else {
				assertStaffRejected(t, w)
				requireKind(t, x.ctx, x.pool, "login_rejected")
			}
			if _, err := x.store.ResolveSession(x.ctx, old.Value); !errors.Is(err, ErrNoSession) {
				t.Fatal("old session remains live")
			}
		})
	}
}

func TestOIDCLoginRejectsLoggedOutIDPSessionGenerically(t *testing.T) {
	x := newStaffOIDCTest(t)
	oldCookie := x.login(t)
	if _, err := x.store.RevokeByIDP(x.ctx, "sid-a", "staff-a"); err != nil {
		t.Fatal(err)
	}
	flow := x.begin(t, "/staff/auth/login", DefaultReturn, oldCookie)
	w := x.callback(t, flow, flow.binding, oldCookie)
	assertStaffRejected(t, w)
	if _, err := x.store.ResolveSession(x.ctx, oldCookie.Value); !errors.Is(err, ErrNoSession) {
		t.Fatalf("old session remained live after rejected login: %v", err)
	}
	var count int
	if err := x.pool.QueryRow(x.ctx, "SELECT count(*) FROM staff_sessions WHERE subject_id='staff-a'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("IDP-logged-out login created a session: count=%d err=%v", count, err)
	}
}

func hashStaffCookie(cookie *http.Cookie) []byte {
	hash := sha256.Sum256([]byte(cookie.Value))
	return hash[:]
}

func TestOIDCLogout(t *testing.T) {
	x := newStaffOIDCTest(t)
	cookie := x.login(t)
	w := x.request(t, "POST", "/staff/auth/logout", "", "", cookie)
	if w.Code != 403 {
		t.Fatal("logout without Origin accepted")
	}
	_ = x.actor(t, cookie)
	x.issuer.server.Close()
	w = x.request(t, "POST", "/staff/auth/logout", "", testOrigin, cookie)
	if w.Code != 303 {
		t.Fatal("logout failed with IdP down")
	}
	if c := staffResponseCookie(t, w, SessionCookie); c.MaxAge != -1 || c.Value != "" {
		t.Fatal("logout cookie not cleared")
	}
	if c := staffResponseCookie(t, w, TxCookie); c.MaxAge != -1 || c.Value != "" {
		t.Fatal("logout transaction cookie not cleared")
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Scheme+"://"+u.Host+u.Path != x.issuer.server.URL+"/end-session" || u.Query().Get("client_id") != "staff-client" || u.Query().Get("post_logout_redirect_uri") != testOrigin+DefaultReturn {
		t.Fatal("wrong IdP logout redirect")
	}
	if _, err = x.store.ResolveSession(x.ctx, cookie.Value); !errors.Is(err, ErrNoSession) {
		t.Fatal("logout session live")
	}
	x.h.endSessionURL = ""
	if w = x.request(t, "POST", "/staff/auth/logout", "", testOrigin); w.Code != 303 || w.Header().Get("Location") != DefaultReturn {
		t.Fatal("local logout failed")
	}
}

func TestOIDCLogoutDatabaseFailurePreservesCookies(t *testing.T) {
	x := newStaffOIDCTest(t)
	cookie := x.login(t)
	x.pool.Close()
	w := x.request(t, "POST", "/staff/auth/logout", "", testOrigin, cookie)
	if w.Code != http.StatusServiceUnavailable || strings.TrimSpace(w.Body.String()) != `{"error":"logout_failed"}` {
		t.Fatalf("logout DB failure response status=%d body=%q", w.Code, w.Body.String())
	}
	if c := staffResponseCookie(t, w, TxCookie); c.MaxAge != -1 || c.Value != "" {
		t.Fatal("logout DB failure did not clear the transaction cookie")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookie {
			t.Fatalf("logout DB failure cleared the session cookie: %+v", c)
		}
	}
}

func TestOIDCLogoutCancellationFailureClearsOnlyTransactionCookie(t *testing.T) {
	x := newStaffOIDCTest(t)
	flow := x.begin(t, "/staff/auth/login", DefaultReturn, nil)
	x.pool.Close()
	w := x.request(t, "POST", "/staff/auth/logout", "", testOrigin, flow.binding)
	if w.Code != http.StatusServiceUnavailable || strings.TrimSpace(w.Body.String()) != `{"error":"logout_failed"}` {
		t.Fatalf("cancel DB failure response status=%d body=%q", w.Code, w.Body.String())
	}
	if c := staffResponseCookie(t, w, TxCookie); c.MaxAge != -1 || c.Value != "" {
		t.Fatal("cancel DB failure did not clear the transaction cookie")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookie {
			t.Fatalf("cancel DB failure set a session cookie: %+v", c)
		}
	}
}

func TestOIDCLogoutCancelsPendingBrowserLogin(t *testing.T) {
	x := newStaffOIDCTest(t)
	flow := x.begin(t, "/staff/auth/login", DefaultReturn, nil)
	w := x.request(t, "POST", "/staff/auth/logout", "", testOrigin, flow.binding)
	// The fake issuer advertises end_session_endpoint, so logout continues there.
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/end-session?client_id=") {
		t.Fatalf("logout status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	if c := staffResponseCookie(t, w, TxCookie); c.MaxAge != -1 || c.Value != "" {
		t.Fatal("logout transaction cookie not cleared")
	}
	if c := staffResponseCookie(t, w, SessionCookie); c.MaxAge != -1 || c.Value != "" {
		t.Fatal("logout session cookie not cleared")
	}
	assertStaffRejected(t, x.callback(t, flow, flow.binding))
	var count int
	if err := x.pool.QueryRow(x.ctx, "SELECT count(*) FROM staff_sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled login created sessions: count=%d err=%v", count, err)
	}
}

func TestOIDCBackchannelLogout(t *testing.T) {
	for _, mode := range []string{"sid", "sub only", "nonce", "events", "signature", "iat", "jti", "no identity", "aud", "iss", "exp", "long expiry", "null event", "string event", "array event", "number event", "nonempty event"} {
		t.Run(mode, func(t *testing.T) {
			x := newStaffOIDCTest(t)
			cookie := x.login(t)
			second, _, err := x.store.CreateSession(x.ctx, "staff-a", "sid-other", false, dbNow(t, x.ctx, x.pool))
			if err != nil {
				t.Fatal(err)
			}
			claims := x.issuer.claims()
			claims["jti"] = "logout-1"
			claims["events"] = map[string]any{BackchannelLogoutEvent: map[string]any{}}
			key := x.issuer.key
			switch mode {
			case "sid":
				delete(claims, "sub")
			case "sub only":
				delete(claims, "sid")
			case "nonce":
				claims["nonce"] = nil
			case "events":
				delete(claims, "events")
			case "signature":
				key, err = rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
			case "iat":
				delete(claims, "iat")
			case "jti":
				delete(claims, "jti")
			case "no identity":
				delete(claims, "sub")
				delete(claims, "sid")
			case "aud":
				claims["aud"] = "wrong-client"
			case "iss":
				claims["iss"] = "https://wrong-issuer.example"
			case "exp":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "long expiry":
				claims["exp"] = time.Now().Add(48 * time.Hour).Unix()
			case "null event":
				claims["events"] = map[string]any{BackchannelLogoutEvent: nil}
			case "string event":
				claims["events"] = map[string]any{BackchannelLogoutEvent: "invalid"}
			case "array event":
				claims["events"] = map[string]any{BackchannelLogoutEvent: []any{}}
			case "number event":
				claims["events"] = map[string]any{BackchannelLogoutEvent: 123}
			case "nonempty event":
				claims["events"] = map[string]any{BackchannelLogoutEvent: map[string]any{"unexpected": true}}
			}
			raw := signStaffToken(t, key, claims)
			post := func() *httptest.ResponseRecorder {
				return x.request(t, "POST", "/staff/auth/backchannel-logout", url.Values{"logout_token": {raw}}.Encode(), "")
			}
			w := post()
			if mode == "sid" || mode == "sub only" {
				if w.Code != 200 {
					t.Fatalf("logout status %d", w.Code)
				}
				if _, err = x.store.ResolveSession(x.ctx, cookie.Value); !errors.Is(err, ErrNoSession) {
					t.Fatal("backchannel session live")
				}
				_, err = x.store.ResolveSession(x.ctx, second)
				if mode == "sid" && err != nil {
					t.Fatal("sid revoked unrelated session")
				}
				if mode == "sub only" && !errors.Is(err, ErrNoSession) {
					t.Fatal("subject logout left session live")
				}
				w = post()
			} else {
				_ = x.actor(t, cookie)
				var count int
				if err := x.pool.QueryRow(x.ctx, "SELECT count(*) FROM staff_logout_jti").Scan(&count); err != nil || count != 0 {
					t.Fatalf("invalid logout registered a JTI: count=%d err=%v", count, err)
				}
			}
			if w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"error":"invalid_logout_token"}` {
				t.Fatalf("invalid logout response status=%d", w.Code)
			}
		})
	}
}

func TestOIDCBackchannelRetryAfterDatabaseFailure(t *testing.T) {
	x := newStaffOIDCTest(t)
	cookie := x.login(t)
	claims := x.issuer.claims()
	claims["jti"] = "retryable-logout"
	claims["events"] = map[string]any{BackchannelLogoutEvent: map[string]any{}}
	raw := signStaffToken(t, x.issuer.key, claims)
	post := func() *httptest.ResponseRecorder {
		return x.request(t, "POST", "/staff/auth/backchannel-logout", url.Values{"logout_token": {raw}}.Encode(), "")
	}
	// The owned schema's trigger fails revocation after the JTI insert.
	if _, err := x.pool.Exec(x.ctx, `CREATE FUNCTION reject_backchannel() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected revocation failure'; END; $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.pool.Exec(x.ctx, `CREATE TRIGGER reject_backchannel BEFORE UPDATE ON staff_sessions
		FOR EACH ROW WHEN (NEW.revoked_reason = 'backchannel_logout') EXECUTE FUNCTION reject_backchannel()`); err != nil {
		t.Fatal(err)
	}
	if w := post(); w.Code != 400 {
		t.Fatalf("failed revocation returned %d", w.Code)
	}
	_ = x.actor(t, cookie)
	var count int
	if err := x.pool.QueryRow(x.ctx, "SELECT count(*) FROM staff_logout_jti WHERE jti='retryable-logout'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed revocation consumed JTI: count=%d err=%v", count, err)
	}
	if _, err := x.pool.Exec(x.ctx, "DROP TRIGGER reject_backchannel ON staff_sessions"); err != nil {
		t.Fatal(err)
	}
	if w := post(); w.Code != 200 {
		t.Fatalf("valid retry returned %d", w.Code)
	}
	if _, err := x.store.ResolveSession(x.ctx, cookie.Value); !errors.Is(err, ErrNoSession) {
		t.Fatal("retry left session live")
	}
	if w := post(); w.Code != 400 {
		t.Fatal("successful logout replay accepted")
	}
	if err := x.pool.QueryRow(x.ctx, "SELECT count(*) FROM staff_auth_events WHERE kind='backchannel_revoke'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("revocation committed more than once: count=%d err=%v", count, err)
	}
}

func TestRequireOrigin(t *testing.T) {
	h := RequireOrigin("https://staff.example", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		method, origin string
		status         int
	}{
		{"GET", "", 204}, {"HEAD", "", 204}, {"OPTIONS", "", 204}, {"POST", "", 403}, {"POST", "https://evil", 403}, {"POST", "https://staff.example/", 403}, {"POST", "https://staff.example", 204},
	} {
		r := httptest.NewRequest(tc.method, "/", nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("method=%s origin=%q status=%d", tc.method, tc.origin, w.Code)
		}
	}
}

func TestSafeReturn(t *testing.T) {
	for _, raw := range []string{"", "//evil", "https://evil", `/\evil`, `/staff\evil`, "/staff/\n", "/staff/\x7f", "/%zz"} {
		if safeReturn(raw) != DefaultReturn {
			t.Fatalf("unsafe path %q", raw)
		}
	}
	if safeReturn("/staff/orders?q=1") != "/staff/orders?q=1" {
		t.Fatal("valid return rejected")
	}
}

func TestOIDCConfigValidation(t *testing.T) {
	for _, origin := range []string{"", "http://example.com", "http://localhost:1234", "http://127.0.0.1", "https://staff.example/", "https://staff.example?x=1", "https://staff.example?", "https://staff.example#fragment", "https://user@staff.example"} {
		_, err := NewHandler(context.Background(), &Store{}, Config{IssuerURL: "https://issuer.example", ClientID: "client", ClientSecret: "secret", PublicOrigin: origin})
		if err == nil || err.Error() != "invalid staff auth public origin" {
			t.Fatalf("origin %q accepted or caused discovery", origin)
		}
	}
	if _, err := NewHandler(context.Background(), nil, Config{}); err == nil {
		t.Fatal("empty configuration accepted")
	}
}
