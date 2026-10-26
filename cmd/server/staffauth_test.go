package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/config"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func staffWiringConfig(issuer string) config.Config {
	return config.Config{
		Sessions: tablesession.Options{SessionTTL: time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 100,
			PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: []byte(strings.Repeat("p", 32))},
		Payments:  payments.Config{CreateRetryWindow: time.Hour, ContinuationTTL: time.Minute, WorkLeaseTTL: time.Minute},
		StaffAuth: config.StaffAuthConfig{Enabled: true, IssuerURL: issuer, ClientID: "client", ClientSecret: strings.Repeat("s", 32), PublicOrigin: "https://staff.example.invalid"},
	}
}

func TestEnabledStaffBoundaryBeforeDatabaseAccess(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://synthetic:synthetic@127.0.0.1:1/synthetic?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		t.Error("anonymous staff request performed database I/O")
		return nil, errors.New("deliberately unavailable")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	c := staffWiringConfig("https://idp.example.invalid")
	api, worker, staff, err := composeAPI(pool, c)
	if err != nil {
		t.Fatal(err)
	}
	if worker != nil || staff == nil {
		t.Fatal("incorrect staff/payment composition")
	}
	h := productionHandler(api, http.NotFoundHandler(), staff, c.StaffAuth.PublicOrigin)
	for _, path := range []string{
		"/v1/staff/tenants/x/stores/y/operations/access", "/v1/admin/tenants/x/menu/preview",
		"/v1/staff%2Ftenants/x/stores/y/operations/access", "/v1/staff%252Ftenants/x",
		"/v1/admin%2f", "/v1/STAFF/x", "/v1/./staff/x", "/v1//staff/x", `/v1/staff\x`,
		"/staff%2Fauth/login", "/staff%252Fauth/login", "/staff//auth/login",
	} {
		if got := request(h, "GET", path); got.Code != 403 {
			t.Errorf("GET %s = %d", path, got.Code)
		}
	}
	for _, origin := range []string{"", "https://wrong.example.invalid"} {
		r := httptest.NewRequest("POST", "/v1/staff/tenants/x/stores/y/operations/access", nil)
		r.Header.Set("Origin", origin)
		r.AddCookie(&http.Cookie{Name: "__Host-tojima_staff", Value: "must-not-query-db"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("wrong Origin = %d", w.Code)
		}
	}
	if got := request(h, "GET", "/staff/auth/login"); got.Code != 503 {
		t.Fatalf("before discovery = %d", got.Code)
	}
}

func TestEnabledStaffLoginRedirect(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	schema := pgx.Identifier{"staff_wiring_" + strconv.FormatInt(time.Now().UnixNano(), 10)}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = postgres.Up(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	var issuerURL string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuerURL, "authorization_endpoint": issuerURL + "/authorize", "token_endpoint": issuerURL + "/token", "jwks_uri": issuerURL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	}))
	defer issuer.Close()
	issuerURL = issuer.URL
	cfg := staffWiringConfig(issuerURL)
	api, _, staff, err := composeAPI(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	staff.Start(ctx)
	h := productionHandler(api, http.NotFoundHandler(), staff, cfg.StaffAuth.PublicOrigin)
	deadline := time.Now().Add(5 * time.Second)
	var w *httptest.ResponseRecorder
	for time.Now().Before(deadline) {
		w = request(h, "GET", "/staff/auth/login?return=/staff/")
		if w.Code != 503 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w == nil || w.Code != 302 {
		t.Fatalf("login did not redirect: %v", w)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != issuerURL+"/authorize" || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("prompt") != "login" || u.Query().Get("acr_values") != "basic" {
		t.Fatalf("login redirect = %s", u)
	}
}
