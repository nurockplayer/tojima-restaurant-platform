package demogateway

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testSecret = "synthetic-gate-value-for-unit-tests-only"
const testPublic = "https://tojima-mobile-order-demo.pages.dev"

func config(upstream string) Config {
	return Config{Listen: "127.0.0.1:8090", Upstream: upstream, PublicOrigin: testPublic, Secret: testSecret}
}
func gateway(t *testing.T, upstream string) http.Handler {
	t.Helper()
	h, err := New(config(upstream))
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func request(h http.Handler, method, target string, body io.Reader, headers http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	r.Header = headers.Clone()
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func gated() http.Header { h := make(http.Header); h.Set(GateHeader, testSecret); return h }

func TestConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"missing secret", func(c *Config) { c.Secret = "" }}, {"short secret", func(c *Config) { c.Secret = strings.Repeat("a", 31) }},
		{"newline secret", func(c *Config) { c.Secret = testSecret + "\n" }},
		{"wildcard", func(c *Config) { c.Listen = "0.0.0.0:8090" }}, {"name", func(c *Config) { c.Listen = "localhost:8090" }},
		{"public ip", func(c *Config) { c.Listen = "192.0.2.1:8090" }}, {"ipv6 wildcard", func(c *Config) { c.Listen = "[::]:8090" }},
		{"zero port", func(c *Config) { c.Listen = "127.0.0.1:0" }}, {"service port", func(c *Config) { c.Listen = "127.0.0.1:http" }},
		{"upstream name", func(c *Config) { c.Upstream = "http://localhost:8082" }}, {"upstream public", func(c *Config) { c.Upstream = "http://192.0.2.1" }},
		{"upstream https", func(c *Config) { c.Upstream = "https://127.0.0.1:8082" }}, {"upstream credentials", func(c *Config) { c.Upstream = "http://user:password@127.0.0.1:8082" }},
		{"upstream path", func(c *Config) { c.Upstream += "/foo" }}, {"upstream query", func(c *Config) { c.Upstream += "?x=1" }},
		{"upstream fragment", func(c *Config) { c.Upstream += "#" }}, {"empty query", func(c *Config) { c.Upstream += "?" }},
		{"public http", func(c *Config) { c.PublicOrigin = "http://example.test" }}, {"missing public", func(c *Config) { c.PublicOrigin = "" }},
		{"public slash", func(c *Config) { c.PublicOrigin += "/" }}, {"public credentials", func(c *Config) { c.PublicOrigin = "https://user:password@example.test" }},
		{"bad port", func(c *Config) { c.Upstream = "http://127.0.0.1:65536" }}, {"ipv6 zone", func(c *Config) { c.Upstream = "http://[::1%25lo0]:8082" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config("http://127.0.0.1:8082")
			tc.change(&c)
			if _, err := New(c); err == nil {
				t.Fatal("accepted invalid configuration")
			} else if strings.Contains(err.Error(), testSecret) {
				t.Fatal("configuration error disclosed secret")
			}
		})
	}
	for _, tc := range []Config{config("http://127.0.0.1:8082"), {Listen: "[::1]:8090", Upstream: "http://[::1]:8082", PublicOrigin: testPublic, Secret: testSecret}} {
		if _, err := New(tc); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGateRequiredAndOriginPolicy(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer up.Close()
	h := gateway(t, up.URL)
	for _, secret := range []string{"", "wrong", strings.Repeat("x", len(testSecret))} {
		headers := make(http.Header)
		if secret != "" {
			headers.Set(GateHeader, secret)
		}
		w := request(h, "GET", "/", nil, headers)
		if w.Code != 404 || w.Body.Len() > 32 {
			t.Fatal("invalid gate did not receive tiny 404")
		}
		if w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Fatal("missing robots header")
		}
	}
	for _, origin := range []string{"https://foreign.test", "null", "", testPublic + "/", testPublic + ".evil.test"} {
		headers := gated()
		headers["Origin"] = []string{origin}
		if w := request(h, "POST", "/v1/guest/tables/t/join", nil, headers); w.Code != 403 {
			t.Fatalf("origin rejection status = %d", w.Code)
		}
	}
	headers := gated()
	headers.Add(GateHeader, testSecret)
	if w := request(h, "GET", "/", nil, headers); w.Code != 404 {
		t.Fatal("duplicate gate accepted")
	}
	headers = gated()
	headers["Origin"] = []string{testPublic, testPublic}
	if w := request(h, "GET", "/", nil, headers); w.Code != 403 {
		t.Fatal("duplicate origin accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("rejected request reached upstream")
	}
	for _, origin := range []string{"", testPublic} {
		headers = gated()
		if origin != "" {
			headers.Set("Origin", origin)
		}
		if w := request(h, "GET", "/", nil, headers); w.Code != 204 {
			t.Fatalf("valid request status = %d", w.Code)
		}
	}
}

// A local end-to-end HTTP smoke: the real gateway fronts an httptest service
// enforcing the same exact Host/Origin checks as httpapi.PaymentsHTTPConfig.
// No PostgreSQL, public tunnel or external service is used.
func TestLocalHostOriginCookieSmoke(t *testing.T) {
	var upstreamOrigin string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != strings.TrimPrefix(upstreamOrigin, "http://") || r.Header.Get("Origin") != upstreamOrigin {
			http.Error(w, "origin guard", 403)
			return
		}
		if r.Referer() != upstreamOrigin+"/staff?view=tables" {
			t.Error("referer not rewritten")
		}
		for _, name := range []string{GateHeader, "Connection", "X-Hop-Secret", "Cf-Connecting-Ip", "Cf-Ray", "X-Forwarded-For", "X-Forwarded-Host", "Forwarded", "Upgrade", "Te", "Proxy-Authorization"} {
			if r.Header.Get(name) != "" {
				t.Errorf("header was not stripped: %s", name)
			}
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-app-token" {
			t.Error("app bearer was lost")
		}
		if r.Header.Get("Cookie") != "app_one=first; tojima_checkout_fixture=proof; app_two=second" {
			t.Error("app cookies were changed or gate cookie leaked")
		}
		if r.URL.RawQuery != "amounts=string" {
			t.Error("query was changed")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "{}" {
			t.Error("body was changed")
		}
		w.Header().Add("Set-Cookie", "app_one=first; Path=/; HttpOnly")
		w.Header().Add("Set-Cookie", "app_two=second; Path=/; HttpOnly")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Robots-Tag", "index")
		w.WriteHeader(204)
	}))
	defer up.Close()
	upstreamOrigin = up.URL
	edge := httptest.NewServer(gateway(t, up.URL))
	defer edge.Close()
	r, _ := http.NewRequest("POST", edge.URL+"/v1/guest/tables/demo-table/join?amounts=string", strings.NewReader("{}"))
	r.Header = gated()
	r.Header.Set("Origin", testPublic)
	r.Header.Set("Referer", testPublic+"/staff?view=tables")
	r.Header.Set("Connection", "X-Hop-Secret, keep-alive")
	for _, name := range []string{"X-Hop-Secret", "Cf-Connecting-Ip", "Cf-Ray", "X-Forwarded-For", "X-Forwarded-Host", "Forwarded", "Upgrade", "Proxy-Authorization"} {
		r.Header.Set(name, "untrusted")
	}
	r.Header.Set("Te", "trailers")
	r.Header.Set("Authorization", "Bearer synthetic-app-token")
	r.Header.Add("Cookie", "app_one=first; "+CookieName+"=edge-secret; tojima_checkout_fixture=proof")
	r.Header.Add("Cookie", CookieName+"=duplicate; app_two=second")
	res, err := edge.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("smoke status = %d", res.StatusCode)
	}
	if len(res.Header.Values("Set-Cookie")) != 2 {
		t.Fatal("Set-Cookie headers collapsed")
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatal("upstream overrode response policy")
	}
}

func TestAllowlist(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer up.Close()
	h := gateway(t, up.URL)
	const s = "/v1/staff/tenants/t/stores/s/tables/table/sessions/session"
	for _, tc := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/staff"}, {"GET", "/kitchen"}, {"GET", "/catalog"}, {"GET", "/order"}, {"HEAD", "/assets/App-a1_b.js"}, {"GET", "/assets/index-a1.css"},
		{"GET", "/v1/guest/tables/demo-shironagasukujira-table-05"}, {"POST", "/v1/guest/tables/t/join"},
		{"GET", "/v1/guest/sessions/s/menu"}, {"GET", "/v1/guest/sessions/s/cart"}, {"POST", "/v1/guest/sessions/s/cart/lines"},
		{"PUT", "/v1/guest/sessions/s/cart/lines/l"}, {"DELETE", "/v1/guest/sessions/s/cart/lines/l"},
		{"POST", "/v1/guest/sessions/s/cart/preview"}, {"POST", "/v1/guest/sessions/s/cart/commit"},
		{"GET", "/v1/guest/sessions/s/orders"}, {"GET", "/v1/guest/sessions/s/operations"},
		{"POST", "/v1/guest/sessions/s/orders/o/read-grant"}, {"GET", "/v1/guest/orders/o/status"},
		{"GET", "/v1/staff/tenants/t/stores/s/operations/access"}, {"GET", "/v1/staff/tenants/t/stores/s/operations/kds"},
		{"GET", s + "/operations"}, {"POST", s + "/orders/o/accept"}, {"POST", s + "/orders/o/fulfillment"},
		{"POST", s + "/cash/preview"}, {"POST", s + "/cash/settle"}, {"POST", s + "/close"},
		{"GET", "/v1/admin/tenants/t/menu/published"}, {"GET", "/v1/admin/tenants/t/menu/draft"},
		{"GET", "/v1/admin/tenants/t/stores/s/menu"}, {"PUT", "/v1/admin/tenants/t/stores/s/menu/availability"},
	} {
		if w := request(h, tc.method, tc.path, nil, gated()); w.Code != 204 {
			t.Errorf("denied walkthrough route %s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	calls.Store(0)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/healthz"}, {"GET", "/secrets.json"}, {"GET", "/unknown"}, {"POST", "/staff"},
		{"GET", "/assets/source.map"}, {"GET", "/assets/../secrets.js"}, {"GET", "/assets/%2e%2e/secrets.js"},
		{"GET", "//staff"}, {"GET", "/v1/guest/tables/a%2fb"}, {"GET", "/v1/guest/tables/.."},
		{"GET", "/v1/fixture"}, {"POST", "/v1/payments/stripe/webhook"}, {"POST", "/v1/guest/sessions/s/checkout-context"},
		{"POST", s + "/orders/o/payment/reconcile"}, {"POST", s + "/orders/o/refunds/r/retry"},
		{"PUT", "/v1/admin/tenants/t/menu/draft"}, {"POST", "/v1/admin/tenants/t/menu/publish"},
		{"GET", "/v1/admin/tenants/t/menu/products/p/emergency-hold"}, {"OPTIONS", "/v1/guest/tables/t"},
		{"GET", "/v1/guest/sessions/s/cart/unlisted"},
	} {
		if w := request(h, tc.method, tc.path, nil, gated()); w.Code != 404 {
			t.Errorf("unlisted route status %s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unlisted route reached upstream")
	}
}

func TestBodyCapBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if len(body) != MaxBodyBytes {
			t.Error("body boundary changed")
		}
		w.WriteHeader(204)
	}))
	defer up.Close()
	h := gateway(t, up.URL)
	for _, length := range []int{MaxBodyBytes, MaxBodyBytes + 1} {
		for _, chunked := range []bool{false, true} {
			r := httptest.NewRequest("POST", "/v1/guest/tables/t/join", strings.NewReader(strings.Repeat("x", length)))
			r.Header = gated()
			if chunked {
				r.ContentLength = -1
				r.TransferEncoding = []string{"chunked"}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 204
			if length > MaxBodyBytes {
				want = 413
			}
			if w.Code != want {
				t.Fatalf("body cap status = %d, want %d", w.Code, want)
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatal("oversized body reached upstream")
	}
}

func TestLocationRewrite(t *testing.T) {
	for _, loc := range []string{"upstream", "relative-network", "/staff", "https://elsewhere.invalid/staff"} {
		t.Run(loc, func(t *testing.T) {
			var origin string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				target := loc
				if loc == "upstream" {
					target = origin + "/staff?x=1#tab"
				}
				if loc == "relative-network" {
					target = strings.TrimPrefix(origin, "http:") + "/staff?x=1#tab"
				}
				w.Header().Set("Location", target)
				w.WriteHeader(302)
			}))
			defer up.Close()
			origin = up.URL
			w := request(gateway(t, up.URL), "GET", "/", nil, gated())
			want := loc
			if loc == "upstream" || loc == "relative-network" {
				want = testPublic + "/staff?x=1#tab"
			}
			if w.Code != 302 || w.Header().Get("Location") != want {
				t.Fatal("redirect not preserved/rewritten correctly")
			}
		})
	}
}

type failedBody struct{}

func (failedBody) Read([]byte) (int, error) { return 0, errors.New(testSecret) }
func (failedBody) Close() error             { return nil }
func TestNoSecretsInErrorsOrLogs(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	up.Close()
	h := gateway(t, up.URL)
	for _, body := range []io.Reader{nil, failedBody{}} {
		w := request(h, "POST", "/v1/guest/tables/t/join?value="+testSecret, body, gated())
		if w.Code != 503 && w.Code != 400 {
			t.Fatalf("unexpected failure status %d", w.Code)
		}
		if strings.Contains(w.Body.String(), testSecret) || strings.Contains(logs.String(), testSecret) {
			t.Fatal("secret leaked in response/log")
		}
	}
	c := config("http://127.0.0.1:8082")
	c.PublicOrigin = testSecret
	if _, err := New(c); err == nil || strings.Contains(err.Error(), testSecret) {
		t.Fatal("unsafe configuration error")
	}
}
