package staffauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"golang.org/x/oauth2"
)

const (
	SessionCookie          = "__Host-tojima_staff"
	TxCookie               = "__Host-tojima_staff_tx"
	BrowserCookie          = "__Host-tojima_staff_browser"
	CallbackPath           = "/staff/auth/callback"
	DefaultReturn          = "/staff/"
	BackchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"
)

var errInvalidLogoutToken = errors.New("invalid logout token")

type Config struct {
	IssuerURL    string
	DiscoveryURL string
	ClientID     string
	ClientSecret string
	PublicOrigin string
	HTTPClient   *http.Client
}

type Handler struct {
	store                      *Store
	cfg                        Config
	provider                   *oidc.Provider
	idVerifier, logoutVerifier *oidc.IDTokenVerifier
	oauth                      oauth2.Config
	endSessionURL              string
}

func NewHandler(ctx context.Context, store *Store, cfg Config) (*Handler, error) {
	if err := validateConfig(store, cfg); err != nil {
		return nil, err
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	discoveryURL := cfg.IssuerURL
	discoveryCtx := oidc.ClientContext(ctx, cfg.HTTPClient)
	if cfg.DiscoveryURL != "" {
		discoveryURL = cfg.DiscoveryURL
		// Only the discovery address changes; the verifier keeps the public issuer.
		discoveryCtx = oidc.InsecureIssuerURLContext(discoveryCtx, cfg.IssuerURL)
	}
	provider, err := oidc.NewProvider(discoveryCtx, discoveryURL)
	if err != nil {
		return nil, errors.New("staff auth discovery failed")
	}
	var metadata struct {
		Issuer        string `json:"issuer"`
		EndSessionURL string `json:"end_session_endpoint"`
	}
	if err = provider.Claims(&metadata); err != nil || metadata.Issuer != cfg.IssuerURL {
		return nil, errors.New("invalid staff auth discovery")
	}
	return &Handler{
		store: store, cfg: cfg, provider: provider,
		idVerifier:     provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		logoutVerifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID, SkipExpiryCheck: false}),
		oauth:          oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: cfg.PublicOrigin + CallbackPath, Scopes: []string{oidc.ScopeOpenID}},
		endSessionURL:  metadata.EndSessionURL,
	}, nil
}

// validateConfig is shared by eager and lazy construction and performs no I/O.
func validateConfig(store *Store, cfg Config) error {
	if store == nil || strings.TrimSpace(cfg.IssuerURL) == "" || strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return errors.New("invalid staff auth configuration")
	}
	u, err := url.Parse(cfg.PublicOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(cfg.PublicOrigin, "#") ||
		(u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "")) {
		return errors.New("invalid staff auth public origin")
	}
	issuer, err := url.Parse(cfg.IssuerURL)
	if err != nil || issuer.Hostname() == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.ForceQuery || strings.Contains(cfg.IssuerURL, "#") || strings.ContainsFunc(cfg.IssuerURL, unicode.IsSpace) ||
		(issuer.Scheme != "https" && !(issuer.Scheme == "http" && (issuer.Hostname() == "keycloak" || issuer.Hostname() == "127.0.0.1" || issuer.Hostname() == "localhost"))) {
		return errors.New("invalid staff auth issuer")
	}
	if cfg.DiscoveryURL != "" {
		u, err := url.Parse(cfg.DiscoveryURL)
		if err != nil || u.Scheme != "http" || u.Host != "keycloak:8080" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.DiscoveryURL, "#") || u.EscapedPath() != issuer.EscapedPath() {
			return errors.New("invalid staff auth discovery address")
		}
	}
	return nil
}

// Mount keeps even method and Origin rejections out of shared caches.
func (h *Handler) Mount(mux *http.ServeMux) {
	route := func(path, method string, next http.Handler) {
		mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != method && !(method == http.MethodGet && r.Method == http.MethodHead) {
				w.Header().Set("Allow", method)
				authJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
	route("/staff/auth/login", http.MethodGet, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.begin(w, r, "login") }))
	route("/staff/auth/step-up", http.MethodGet, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.begin(w, r, "step_up") }))
	route(CallbackPath, http.MethodGet, http.HandlerFunc(h.callback))
	route("/staff/auth/logout", http.MethodPost, RequireOrigin(h.cfg.PublicOrigin, http.HandlerFunc(h.logout)))
	route("/staff/auth/session", http.MethodGet, http.HandlerFunc(h.session))
	route("/staff/auth/backchannel-logout", http.MethodPost, http.HandlerFunc(h.backchannelLogout))
}

func safeReturn(raw string) string {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") || strings.ContainsFunc(raw, unicode.IsControl) {
		return DefaultReturn
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return DefaultReturn
	}
	return raw
}

func randomAuthValue() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func authCookie(name, value string, maxAge int, expires time.Time) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, MaxAge: maxAge, Expires: expires, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

func authJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func authFail(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, DefaultReturn+"?auth_error=1", http.StatusSeeOther)
}

func (h *Handler) cookieSession(ctx context.Context, r *http.Request) (Session, error) {
	return cookieSession(ctx, h.store, r)
}

func cookieSession(ctx context.Context, store *Store, r *http.Request) (Session, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return Session{}, ErrNoSession
	}
	return store.ResolveSession(ctx, cookie.Value)
}

func (h *Handler) begin(w http.ResponseWriter, r *http.Request, purpose string) {
	in := AuthTxInput{Purpose: purpose, ReturnPath: safeReturn(r.URL.Query().Get("return"))}
	if purpose == "step_up" {
		s, err := h.cookieSession(r.Context(), r)
		if err != nil {
			authJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		in.SessionID, in.SubjectID = s.ID, s.SubjectID
	}
	browser, browserErr := r.Cookie(BrowserCookie)
	newBrowser := browserErr != nil || browser.Value == ""
	browserID := ""
	if newBrowser {
		browserID, browserErr = randomAuthValue()
		if browserErr != nil {
			authFail(w, r)
			return
		}
	} else {
		browserID = browser.Value
	}
	in.BrowserHash = sha256.Sum256([]byte(browserID))
	state, err := randomAuthValue()
	if err != nil {
		authFail(w, r)
		return
	}
	binding, err := randomAuthValue()
	if err != nil {
		authFail(w, r)
		return
	}
	nonce, err := randomAuthValue()
	if err != nil {
		authFail(w, r)
		return
	}
	in.StateHash, in.BindingHash = sha256.Sum256([]byte(state)), sha256.Sum256([]byte(binding))
	in.Nonce, in.PKCEVerifier = nonce, oauth2.GenerateVerifier()
	if err = h.store.CreateAuthTx(r.Context(), in); err != nil {
		authFail(w, r)
		return
	}
	if newBrowser {
		expires := time.Now().Add(30 * 24 * time.Hour)
		http.SetCookie(w, authCookie(BrowserCookie, browserID, 30*24*60*60, expires))
	}
	http.SetCookie(w, authCookie(TxCookie, binding, 600, time.Time{}))
	acr := "basic"
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(in.PKCEVerifier), oauth2.SetAuthURLParam("prompt", "login")}
	if purpose == "step_up" {
		acr = "strong"
		opts = append(opts, oauth2.SetAuthURLParam("max_age", "0"))
	}
	opts = append(opts, oauth2.SetAuthURLParam("acr_values", acr))
	http.Redirect(w, r, h.oauth.AuthCodeURL(state, opts...), http.StatusFound)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, authCookie(TxCookie, "", -1, time.Time{}))
	binding, err := r.Cookie(TxCookie)
	if err != nil || binding.Value == "" {
		authFail(w, r)
		return
	}
	query := r.URL.Query()
	stateHash := sha256.Sum256([]byte(query.Get("state")))
	tx, err := h.store.ConsumeAuthTx(r.Context(), stateHash, sha256.Sum256([]byte(binding.Value)))
	if err != nil || query.Has("error") || query.Get("code") == "" {
		authFail(w, r)
		return
	}
	ctx := oidc.ClientContext(r.Context(), h.cfg.HTTPClient)
	token, err := h.oauth.Exchange(ctx, query.Get("code"), oauth2.VerifierOption(tx.PKCEVerifier))
	if err != nil {
		authFail(w, r)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		authFail(w, r)
		return
	}
	id, err := h.idVerifier.Verify(ctx, raw)
	if err != nil {
		authFail(w, r)
		return
	}
	var claims struct {
		Sub      string `json:"sub"`
		SID      string `json:"sid"`
		ACR      string `json:"acr"`
		Nonce    string `json:"nonce"`
		AuthTime *int64 `json:"auth_time"`
	}
	if err = id.Claims(&claims); err != nil || claims.Sub == "" || claims.SID == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(tx.Nonce)) != 1 {
		authFail(w, r)
		return
	}
	switch tx.Purpose {
	case "login":
		if old, e := h.cookieSession(ctx, r); e == nil {
			if err = h.store.RevokeSession(ctx, old.ID, "relogin"); err != nil && !errors.Is(err, ErrNoSession) {
				authFail(w, r)
				return
			}
		} else if !errors.Is(e, ErrNoSession) {
			authFail(w, r)
			return
		}
		rawSession, s, err := h.store.CreateSession(ctx, claims.Sub, claims.SID, false, tx.CreatedAt, stateHash)
		if err != nil {
			authFail(w, r)
			return
		}
		maxAge := int(time.Until(s.AbsoluteExpires).Seconds())
		if maxAge <= 0 {
			authFail(w, r)
			return
		}
		http.SetCookie(w, authCookie(SessionCookie, rawSession, maxAge, s.AbsoluteExpires))
	case "step_up":
		if claims.Sub != tx.SubjectID || claims.ACR != "strong" || claims.AuthTime == nil || time.Unix(*claims.AuthTime, 0).Before(tx.CreatedAt.Add(-5*time.Second)) {
			authFail(w, r)
			return
		}
		current, resolveErr := h.cookieSession(ctx, r)
		if resolveErr != nil || current.ID != tx.SessionID {
			authFail(w, r)
			return
		}
		rawSession, s, promoteErr := h.store.PromoteStrong(ctx, tx.SessionID, claims.Sub, tx.CreatedAt, stateHash)
		if promoteErr != nil {
			authFail(w, r)
			return
		}
		maxAge := int(time.Until(s.AbsoluteExpires).Seconds())
		if maxAge <= 0 {
			authFail(w, r)
			return
		}
		http.SetCookie(w, authCookie(SessionCookie, rawSession, maxAge, s.AbsoluteExpires))
	default:
		authFail(w, r)
		return
	}
	http.Redirect(w, r, tx.ReturnPath, http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	logoutFailed := func() {
		http.SetCookie(w, authCookie(TxCookie, "", -1, time.Time{}))
		authJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "logout_failed"})
	}
	if browserCookie, err := r.Cookie(BrowserCookie); err == nil && browserCookie.Value != "" {
		if err = h.store.CancelBrowser(r.Context(), sha256.Sum256([]byte(browserCookie.Value))); err != nil {
			logoutFailed()
			return
		}
	}
	if txCookie, err := r.Cookie(TxCookie); err == nil {
		if err = h.store.CancelAuthTx(r.Context(), sha256.Sum256([]byte(txCookie.Value))); err != nil {
			logoutFailed()
			return
		}
	}
	if sessionCookie, err := r.Cookie(SessionCookie); err == nil && sessionCookie.Value != "" {
		if _, err = h.store.RevokeByPresentedToken(r.Context(), sessionCookie.Value, "logout"); err != nil {
			logoutFailed()
			return
		}
	}
	http.SetCookie(w, authCookie(SessionCookie, "", -1, time.Time{}))
	http.SetCookie(w, authCookie(TxCookie, "", -1, time.Time{}))
	target := DefaultReturn
	if h.endSessionURL != "" {
		u, err := url.Parse(h.endSessionURL)
		if err != nil {
			authFail(w, r)
			return
		}
		q := u.Query()
		q.Set("client_id", h.cfg.ClientID)
		q.Set("post_logout_redirect_uri", h.cfg.PublicOrigin+DefaultReturn)
		u.RawQuery = q.Encode()
		target = u.String()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	s, err := h.cookieSession(r.Context(), r)
	if err != nil {
		authJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	authJSON(w, http.StatusOK, struct {
		SubjectID   string `json:"subject_id"`
		MFA         bool   `json:"mfa"`
		StepUpFresh bool   `json:"step_up_fresh"`
		ExpiresAt   string `json:"expires_at"`
	}{s.SubjectID, s.StrongLogin, s.StepUpFresh, s.AbsoluteExpires.Format(time.RFC3339)})
}

func (h *Handler) backchannelLogout(w http.ResponseWriter, r *http.Request) {
	invalid := func() { authJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_logout_token"}) }
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		invalid()
		return
	}
	fresh, err := h.applyBackchannelLogout(r.Context(), r.PostForm.Get("logout_token"), time.Now())
	if err != nil || !fresh {
		invalid()
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) applyBackchannelLogout(ctx context.Context, rawToken string, receivedAt time.Time) (bool, error) {
	verifyCtx := oidc.ClientContext(ctx, h.cfg.HTTPClient)
	id, err := h.logoutVerifier.Verify(verifyCtx, rawToken)
	if err != nil {
		var expired *oidc.TokenExpiredError
		if errors.As(err, &expired) && !expired.Expiry.Before(receivedAt) {
			verifier := h.provider.Verifier(&oidc.Config{ClientID: h.cfg.ClientID, Now: func() time.Time { return receivedAt }})
			id, err = verifier.Verify(verifyCtx, rawToken)
		}
		if err != nil {
			if strings.Contains(err.Error(), "fetching keys") {
				return false, err
			}
			return false, errInvalidLogoutToken
		}
	}
	var claims struct {
		IAT    *int64                     `json:"iat"`
		JTI    string                     `json:"jti"`
		SID    string                     `json:"sid"`
		Sub    string                     `json:"sub"`
		Events map[string]json.RawMessage `json:"events"`
	}
	var raw map[string]json.RawMessage
	if id.Claims(&claims) != nil || id.Claims(&raw) != nil {
		return false, errInvalidLogoutToken
	}
	_, nonce := raw["nonce"]
	event, present := claims.Events[BackchannelLogoutEvent]
	var payload map[string]json.RawMessage
	// The logout event is an empty object, not null or another JSON type.
	validEvent := present && json.Unmarshal(event, &payload) == nil && payload != nil && len(payload) == 0
	// Replay records are retained for 24 hours. Never accept a token that
	// could still be valid after its record has been cleaned up.
	if claims.IAT == nil || claims.JTI == "" || !validEvent || nonce || (claims.SID == "" && claims.Sub == "") || id.Expiry.After(receivedAt.Add(24*time.Hour)) {
		return false, errInvalidLogoutToken
	}
	fresh, err := h.store.ApplyIDPLogout(ctx, claims.JTI, claims.SID, claims.Sub)
	if err != nil {
		return false, err
	}
	return fresh, nil
}

func (h *Handler) Resolve(ctx context.Context, r *http.Request) (authz.Actor, error) {
	return resolve(ctx, h.store, r)
}

func resolve(ctx context.Context, store *Store, r *http.Request) (authz.Actor, error) {
	s, err := cookieSession(ctx, store, r)
	if err != nil {
		return authz.Actor{}, authz.ErrDenied
	}
	return authz.Actor{SubjectID: s.SubjectID, MFA: s.StrongLogin, StepUpFresh: s.StepUpFresh}, nil
}

func RequireOrigin(publicOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && r.Header.Get("Origin") != publicOrigin {
			authJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
