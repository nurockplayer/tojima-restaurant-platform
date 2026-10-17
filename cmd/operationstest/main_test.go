package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
)

func TestCredentialOutputIsExclusiveOwnerOnlyAndRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	doc := credentialDocument{Credentials: testoperations.CredentialSet{Cashier: testoperations.Credential{Role: authz.Staff, Token: "secret"}}}
	owned, err := writeCredentials(path, doc)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credentials permissions=%#o, want 0600", got)
	}
	if _, err := writeCredentials(path, doc); err == nil {
		t.Fatal("existing credential path was overwritten")
	}
	owned.removeIfOwned()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owned credentials file remains after cleanup: %v", err)
	}
}

func TestCredentialsOutsideServedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "credentials.json")
	if err := validateCredentialsOutsideWebRoot(inside, root); err == nil {
		t.Fatal("credentials under the served directory were accepted")
	}
	outside := filepath.Join(t.TempDir(), "credentials.json")
	if err := validateCredentialsOutsideWebRoot(outside, root); err != nil {
		t.Fatalf("outside credential path rejected: %v", err)
	}
}

func TestSPAFallbackServesStaffRouteAndRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("spa-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "secret.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	handler := spaHandler(root)
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/staff", want: "spa-index"},
		{path: "/secret.txt", want: "spa-index"},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || response.Body.String() != test.want {
				t.Fatalf("response=%d %q, want %q", response.Code, response.Body.String(), test.want)
			}
		})
	}
}

func TestDemoFixtureModeRejectsPaymentsBeforeOtherSetup(t *testing.T) {
	err := validateFixtureMode(true, true)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("demo plus payments fixture error = %v", err)
	}
	if err := validateFixtureMode(true, false); err != nil {
		t.Fatalf("demo without payments fixture rejected: %v", err)
	}
	if err := validateFixtureMode(false, true); err != nil {
		t.Fatalf("ordinary payments fixture rejected: %v", err)
	}
	if !catalogFixtureEnabled(false, true) || !catalogFixtureEnabled(true, false) || catalogFixtureEnabled(false, false) {
		t.Fatal("demo did not enable catalog fixture routes independently of the explicit catalog flag")
	}
}

func TestDemoCredentialDocumentUsesFixtureScopeAndEntryURLs(t *testing.T) {
	fixture := &testguest.Fixture{
		TenantID: "demo-synthetic", StoreID: "demo-shironagasukujira", TableID: "table-05",
		Locator: "demo-shironagasukujira-table-05", Schema: "p85_demo_test",
	}
	credentials := testoperations.CredentialSet{Cashier: testoperations.Credential{Role: authz.Staff, Token: "private-token"}}
	doc := buildCredentialDocument(fixture, credentials, true, true, false, "")
	if doc.TenantID != fixture.TenantID || doc.StoreID != fixture.StoreID || doc.TableID != fixture.TableID || doc.TableLocator != fixture.Locator {
		t.Fatalf("credential document hardcoded fixture scope: %+v", doc)
	}
	if doc.GuestURL != doc.BaseURL+"/?table=demo-shironagasukujira-table-05" {
		t.Fatalf("guest URL does not enter the seeded table: %q", doc.GuestURL)
	}
	if doc.CatalogURL != doc.BaseURL+"/catalog" || doc.KitchenURL != doc.BaseURL+"/kitchen" {
		t.Fatalf("demo entry URLs are wrong: %+v", doc)
	}
	if doc.PaymentFixture || doc.WebhookFixtureSecret != "" {
		t.Fatalf("demo credential document advertises a payment fixture: %+v", doc)
	}
}

func TestDemoSPAGatesRootForAllHTMLEntriesAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	index := `<!doctype html><html lang="ja"><head></head><body><div id="root"></div><script src="/app.js"></script></body></html>`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.js"), []byte("window.demo = false"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra.html"), []byte(`<html><body><div id="root"></div></body></html>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateDemoWebRoot(root); err != nil {
		t.Fatalf("valid SPA root could not be gated: %v", err)
	}
	handler := demoSPAHandler(root)
	for _, path := range []string{"/", "/staff", "/kitchen", "/index.html", "/extra.html"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d %s", path, response.Code, response.Body.String())
		}
		if got := strings.Count(response.Body.String(), `data-demo="shironagasukujira"`); got != 1 {
			t.Errorf("GET %s injected marker %d times: %s", path, got, response.Body.String())
		}
		if path != "/extra.html" && !strings.Contains(response.Body.String(), `<div id="root" data-demo="shironagasukujira">`) {
			t.Errorf("GET %s did not gate #root: %s", path, response.Body.String())
		}
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if asset.Code != http.StatusOK || strings.Contains(asset.Body.String(), "data-demo") {
		t.Fatalf("non-HTML asset was modified: %d %s", asset.Code, asset.Body.String())
	}

	badRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(badRoot, "index.html"), []byte(`<html><body><main>no app root</main></body></html>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateDemoWebRoot(badRoot); err == nil {
		t.Fatal("SPA without #root passed demo gate validation")
	}
	if err := os.WriteFile(filepath.Join(badRoot, "index.html"), []byte(`<div id="root" data-demo="other"></div>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := injectDemoMarker([]byte(`<div id="root" data-demo="other"></div>`)); err == nil {
		t.Fatal("conflicting existing demo marker was overwritten")
	}
	if _, err := injectDemoMarker([]byte(`<div id="root"></div><div id="root"></div>`)); err == nil {
		t.Fatal("duplicate SPA roots were accepted")
	}
	if err := os.WriteFile(filepath.Join(badRoot, "index.html"), []byte(`<div id="root"></div>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badRoot, "bad.html"), []byte(`<html><body>ungated page</body></html>`), 0o600); err != nil {
		t.Fatal(err)
	}
	response := demoSPAHandler(badRoot)
	ungated := httptest.NewRecorder()
	response.ServeHTTP(ungated, httptest.NewRequest(http.MethodGet, "/bad.html", nil))
	if ungated.Code != http.StatusInternalServerError || strings.Contains(ungated.Body.String(), "ungated page") {
		t.Fatalf("un-gateable HTML was served: %d %s", ungated.Code, ungated.Body.String())
	}
}

func TestDemoStripeCommitGuardBlocksStripeAndPreservesCashBody(t *testing.T) {
	var calls int
	var receivedBody string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	})
	guard := rejectDemoStripeCommit(next)
	path := "/v1/guest/sessions/session-1/cart/commit"

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "single Stripe", body: `{"payment_method":"STRIPE"}`, wantStatus: http.StatusServiceUnavailable},
		{name: "duplicate cash then Stripe", body: `{"payment_method":"CASH","payment_method":"STRIPE"}`, wantStatus: http.StatusBadRequest},
		{name: "duplicate Stripe then cash", body: `{"payment_method":"STRIPE","payment_method":"CASH"}`, wantStatus: http.StatusBadRequest},
		{name: "case alias", body: `{"PAYMENT_METHOD":"CASH"}`, wantStatus: http.StatusBadRequest},
		{name: "escaped key", body: `{"\u0070ayment_method":"CASH"}`, wantStatus: http.StatusBadRequest},
		{name: "malformed", body: `{"payment_method":"CASH"`, wantStatus: http.StatusBadRequest},
		{name: "trailing JSON", body: `{"payment_method":"CASH"} {}`, wantStatus: http.StatusBadRequest},
		{name: "oversize", body: `{"payment_method":"CASH","padding":"` + strings.Repeat("x", 16<<10) + `"}`, wantStatus: http.StatusBadRequest},
		{name: "invalid type", body: `{"payment_method":0}`, wantStatus: http.StatusBadRequest},
		{name: "missing method", body: `{"expected_version":1}`, wantStatus: http.StatusBadRequest},
		{name: "lowercase method", body: `{"payment_method":"cash"}`, wantStatus: http.StatusBadRequest},
		{name: "noncanonical escaped value", body: `{"payment_method":"CA\u0053H"}`, wantStatus: http.StatusBadRequest},
		{name: "nested method only", body: `{"nested":{"payment_method":"CASH"}}`, wantStatus: http.StatusBadRequest},
		{name: "nonobject", body: `[]`, wantStatus: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			guard.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(tc.body)))
			if response.Code != tc.wantStatus || calls != 0 {
				t.Fatalf("invalid commit reached runtime or got wrong status: status=%d want=%d calls=%d body=%s", response.Code, tc.wantStatus, calls, response.Body.String())
			}
			if strings.Contains(response.Body.String(), strings.Repeat("x", 20)) {
				t.Fatal("guard error reflected sensitive request content")
			}
		})
	}

	for i, cashBody := range []string{
		`{"payment_method":"CASH","expected_generation":2}`,
		"{\n  \"expected_generation\":2, \"payment_method\" : \"CASH\", \"expected_version\":3\n}",
		`{"expected_generation":2,"expected_version":3,"preview_fingerprint":"fixture","payment_method":"CASH"}`,
	} {
		cash := httptest.NewRecorder()
		guard.ServeHTTP(cash, httptest.NewRequest(http.MethodPost, path, strings.NewReader(cashBody)))
		if cash.Code != http.StatusNoContent || calls != i+1 || receivedBody != cashBody {
			t.Fatalf("canonical CASH body was changed or denied: status=%d calls=%d body=%q want=%q", cash.Code, calls, receivedBody, cashBody)
		}
	}

	passBody := `{"payment_method":"CASH"}`
	for _, tc := range []struct{ method, path string }{
		{method: http.MethodGet, path: path},
		{method: http.MethodPost, path: "/v1/guest/sessions/session-1/cart/lines"},
		{method: http.MethodPost, path: "/v1/other/sessions/session-1/cart/commit"},
	} {
		passThrough := httptest.NewRecorder()
		guard.ServeHTTP(passThrough, httptest.NewRequest(tc.method, tc.path, strings.NewReader(passBody)))
		if passThrough.Code != http.StatusNoContent {
			t.Fatalf("ordinary request %s %s was changed: status=%d", tc.method, tc.path, passThrough.Code)
		}
	}
	if calls != 6 {
		t.Fatalf("pass-through call count=%d, want three valid CASH plus three ordinary requests", calls)
	}
}
