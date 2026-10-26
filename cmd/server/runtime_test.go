package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/config"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

func builtSPA(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"index.html": "<html>guest shell</html>", "assets/main-abcdefgh.js": "console.log('app')", "robots.txt": "Disallow:", "assets/private.txt": "must never list", ".env": "must never serve"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}
func request(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Authorization", "Bearer test-only-staff-token")
	req.Header.Set("X-Staff-Role", "TENANT_ADMIN")
	handler.ServeHTTP(recorder, req)
	return recorder
}
func TestProductionNamespacesDenyBeforeDispatch(t *testing.T) {
	forbiddenNext := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("staff request reached API/SPA") })
	handler := productionHandler(forbiddenNext, forbiddenNext)
	for _, path := range []string{
		"/v1/staff", "/v1/admin", "/v1/staff/tenants/t/stores/s/tables/a/sessions",
		"/v1/staff/tenants/t/stores/s/operations/kds", "/v1/staff/tenants/t/stores/s/operations/reviews",
		"/v1/staff/tenants/t/stores/s/tables/a/sessions/b/orders/o/payment/reconcile",
		"/v1/admin/tenants/t/menu/preview", "/v1/admin/unknown-future-route",
	} {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"} {
			got := request(handler, method, path)
			if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "forbidden") || got.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s %s: %d %s", method, path, got.Code, got.Body.String())
			}
		}
	}
}
func TestProductionDispatchKeepsAPIOutOfSPAFallback(t *testing.T) {
	spa, closeSPA, err := newSPAHandler(builtSPA(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSPA()
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/guest/tables/table-1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})
	handler := productionHandler(api, spa)
	for path, status := range map[string]int{"/healthz": 200, "/v1/guest/tables/table-1": 204, "/v1/unknown": 404, "/v1": 404, "/checkout/return": 200} {
		got := request(handler, "GET", path)
		if got.Code != status {
			t.Errorf("GET %s = %d, want %d", path, got.Code, status)
		}
		if strings.HasPrefix(path, "/v1") && strings.Contains(got.Body.String(), "guest shell") {
			t.Errorf("API request returned SPA: %s", path)
		}
	}
}
func TestSPAFallbackFilesAndCaching(t *testing.T) {
	spa, closeSPA, err := newSPAHandler(builtSPA(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSPA()
	handler := productionHandler(http.NotFoundHandler(), spa)
	for _, test := range []struct {
		path        string
		status      int
		body, cache string
	}{
		{"/", 200, "guest shell", "no-cache"}, {"/guest/orders/123", 200, "guest shell", "no-cache"},
		{"/index.html", 200, "guest shell", "no-cache"}, {"/assets/main-abcdefgh.js", 200, "console.log", "public, max-age=31536000, immutable"},
		{"/robots.txt", 200, "Disallow:", "no-cache"}, {"/assets/", 404, "404", "no-cache"},
		{"/assets/missing.js", 404, "404", "no-cache"}, {"/assets/missing", 404, "404", "no-cache"},
		{"/missing.css", 404, "404", "no-cache"}, {"/.env", 404, "404", "no-cache"},
	} {
		got := request(handler, "GET", test.path)
		if got.Code != test.status || !strings.Contains(got.Body.String(), test.body) || got.Header().Get("Cache-Control") != test.cache {
			t.Errorf("%s: %d, %q, cache=%q", test.path, got.Code, got.Body.String(), got.Header().Get("Cache-Control"))
		}
		if got.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("missing nosniff: %s", test.path)
		}
	}
	if got := request(handler, "HEAD", "/guest/orders/123"); got.Code != 200 || got.Body.Len() != 0 {
		t.Fatalf("HEAD = %d %s", got.Code, got.Body.String())
	}
	if got := request(handler, "POST", "/guest/orders/123"); got.Code != 405 {
		t.Fatalf("POST = %d", got.Code)
	}
	req := httptest.NewRequest("GET", "/unknown", nil)
	req.Header.Set("Accept", "application/json")
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, req)
	if got.Code != 404 {
		t.Fatalf("non-HTML fallback = %d", got.Code)
	}
}
func TestSPANoSymlinkEscapeOrMissingShell(t *testing.T) {
	directory := builtSPA(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	spa, closeSPA, err := newSPAHandler(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSPA()
	if got := request(spa, "GET", "/escape.txt"); got.Code != 404 || strings.Contains(got.Body.String(), "outside-secret") {
		t.Fatalf("symlink escape: %d", got.Code)
	}
	if _, _, err := newSPAHandler(t.TempDir()); err == nil {
		t.Fatal("missing index accepted")
	}
	if _, _, err := newSPAHandler(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root accepted")
	}
}
func TestExplicitHealthRuntimeNeedsNoDBOrSPA(t *testing.T) {
	runtime, err := composeRuntime(context.Background(), config.Config{HealthOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.close()
	if runtime.payments != nil {
		t.Fatal("health mode started payment worker")
	}
	for path, status := range map[string]int{"/healthz": 200, "/": 404, "/v1/guest/tables/a": 404} {
		if got := request(runtime.handler, "GET", path); got.Code != status {
			t.Errorf("%s = %d", path, got.Code)
		}
	}
}
func TestStartupRefusesMissingConfiguration(t *testing.T) {
	t.Setenv("APP_ADDR", "127.0.0.1:8080")
	t.Setenv("APP_HEALTH_ONLY", "false")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PGHOST", "")
	if err := run(false); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("startup error = %v", err)
	}
}
func TestDatabaseParseErrorIsSanitized(t *testing.T) {
	_, err := composeRuntime(context.Background(), config.Config{WebRoot: builtSPA(t), DatabaseURL: "postgres://user:private-secret@%invalid"})
	if err == nil || strings.Contains(err.Error(), "private-secret") || !strings.Contains(err.Error(), "database configuration") {
		t.Fatalf("database error = %v", err)
	}
}
func TestPaymentWorkerStopsInFlightBeforeReturn(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	stop := startPaymentWorker(context.Background(), func(ctx context.Context) (bool, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return false, errors.New("private provider error")
	})
	defer stop()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never started")
	}
	stop()
	select {
	case <-finished:
	default:
		t.Fatal("stop returned before worker completed")
	}
}

func TestComposedAPIPaymentMountsAndSecurity(t *testing.T) {
	poolConfig, err := pgxpool.ParseConfig("postgres://synthetic:synthetic@127.0.0.1:1/synthetic?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		t.Error("unexpected database I/O for rejected request")
		return nil, errors.New("database deliberately unavailable")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cfg := config.Config{
		Sessions: tablesession.Options{SessionTTL: time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 100,
			PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: []byte(strings.Repeat("p", 32))},
		Payments: payments.Config{CreateRetryWindow: time.Hour, ContinuationTTL: time.Minute, WorkLeaseTTL: time.Minute},
		Stripe: config.StripeConfig{SecretKey: "sk_test_synthetic-material", WebhookSecret: "whsec_synthetic-material", AccountID: "acct_synthetic",
			CredentialRef: "synthetic:v1", Origin: "https://guest.example.invalid", SuccessURL: "https://guest.example.invalid/success",
			CancelURL: "https://guest.example.invalid/cancel", MaxAmountJPY: 1000, ProofTTL: time.Minute,
			SigningKeys: [][]byte{[]byte(strings.Repeat("s", 32))}},
	}
	for _, enabled := range []bool{false, true} {
		cfg.Stripe.Enabled = enabled
		api, worker, err := composeAPI(pool, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if (worker != nil) != enabled {
			t.Errorf("worker configured with Stripe enabled=%v", enabled)
		}
		handler := productionHandler(api, http.NotFoundHandler())
		// Query credentials are denied by the actual ORDER_READ handler without DB I/O.
		if got := request(handler, "GET", "/v1/guest/orders/order/status?token=forbidden"); got.Code != 403 {
			t.Errorf("ORDER_READ route = %d", got.Code)
		}
		if got := request(handler, "POST", "/v1/staff/tenants/t/stores/s/tables/a/sessions"); got.Code != 403 {
			t.Errorf("staff route = %d", got.Code)
		}
		contextStatus, webhookStatus := 404, 404
		if enabled {
			contextStatus, webhookStatus = 403, 400
		}
		if got := request(handler, "POST", "/v1/guest/sessions/session/checkout-context"); got.Code != contextStatus {
			t.Errorf("checkout context enabled=%v: %d", enabled, got.Code)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/payments/stripe/webhook", strings.NewReader(`{}`))
		handler.ServeHTTP(rec, req)
		if rec.Code != webhookStatus {
			t.Errorf("unsigned webhook enabled=%v: %d", enabled, rec.Code)
		}
	}
}
