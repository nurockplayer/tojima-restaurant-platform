package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
)

func sandboxTestEnvironment() map[string]string {
	return map[string]string{
		"TOJIMA_STRIPE_MODE":                stripeprovider.ModeConnectedDirectFixture,
		"TOJIMA_STRIPE_API_VERSION":         stripeprovider.APIVersion,
		"TOJIMA_STRIPE_TEST_SECRET_KEY":     "sk_test_" + strings.Repeat("x", 24),
		"TOJIMA_STRIPE_TEST_WEBHOOK_SECRET": "whsec_" + strings.Repeat("y", 24),
		"TOJIMA_STRIPE_TEST_ACCOUNT_A":      "acct_syntheticA",
		"TOJIMA_STRIPE_TEST_ACCOUNT_B":      "acct_syntheticB",
		"TOJIMA_STRIPE_TEST_ACCOUNT_C":      "acct_syntheticC",
		"TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY": "10000",
	}
}

func TestStripeSandboxConfigAndRedaction(t *testing.T) {
	env := sandboxTestEnvironment()
	cfg, err := loadStripeSandboxConfig(func(k string) string { return env[k] })
	if err != nil || cfg.accounts["A"] != env["TOJIMA_STRIPE_TEST_ACCOUNT_A"] || cfg.maxAmountJPY != 10000 {
		t.Fatal("valid synthetic config rejected")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		out := fmt.Sprintf(format, cfg)
		if strings.Contains(out, cfg.apiKey) || strings.Contains(out, cfg.webhookSecret) {
			t.Fatal("secret-bearing config formatting was not redacted")
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil || string(raw) != "{}" {
		t.Fatal("config JSON exposed private fields")
	}
	if _, err := loadStripeSandboxConfig(nil); err == nil {
		t.Fatal("nil environment accepted")
	}
}

func TestStripeSandboxConfigRejectsUnsafeInputs(t *testing.T) {
	cases := []struct{ key, value string }{
		{"TOJIMA_STRIPE_MODE", "UNCONFIGURED"},
		{"TOJIMA_STRIPE_MODE", "CONNECTED_DIRECT"},
		{"TOJIMA_STRIPE_API_VERSION", "latest"},
		{"TOJIMA_STRIPE_TEST_SECRET_KEY", "sk_live_" + strings.Repeat("x", 24)},
		{"TOJIMA_STRIPE_TEST_SECRET_KEY", "rk_test_" + strings.Repeat("x", 24)},
		{"TOJIMA_STRIPE_TEST_SECRET_KEY", "sk_test_" + strings.Repeat("x", 24) + "\n"},
		{"TOJIMA_STRIPE_TEST_WEBHOOK_SECRET", ""},
		{"TOJIMA_STRIPE_TEST_ACCOUNT_A", sandboxPlatformAccount},
		{"TOJIMA_STRIPE_TEST_ACCOUNT_B", "acct_syntheticA"},
		{"TOJIMA_STRIPE_TEST_ACCOUNT_C", "acct_bad-id"},
		{"TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "0"},
		{"TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "-1"},
		{"TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "1.5"},
		{"TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "9223372036854775808"},
		{"TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY", "0100"},
	}
	for i, item := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			env := sandboxTestEnvironment()
			env[item.key] = item.value
			if _, err := loadStripeSandboxConfig(func(k string) string { return env[k] }); err == nil {
				t.Fatal("unsafe environment accepted")
			}
		})
	}
}

func TestStripeSandboxURLAndHostBoundaries(t *testing.T) {
	for _, raw := range []string{
		sandboxReturnOrigin + sandboxReturnPath + "?result=success",
		sandboxReturnOrigin + sandboxReturnPath + "?result=cancel",
	} {
		if !validSandboxReturnURL(raw, sandboxReturnOrigin) {
			t.Fatal("pinned return rejected")
		}
	}
	for _, raw := range []string{
		"http://tojima-restaurant-platform-demo.pages.dev" + sandboxReturnPath + "?result=success",
		"https://attacker.invalid" + sandboxReturnPath + "?result=success",
		sandboxReturnOrigin + "/wrong?result=success",
		sandboxReturnOrigin + sandboxReturnPath + "?result=success&token=bad",
		sandboxReturnOrigin + sandboxReturnPath + "?result=success#bad",
		"https://user@tojima-restaurant-platform-demo.pages.dev" + sandboxReturnPath + "?result=success",
	} {
		if validSandboxReturnURL(raw, sandboxReturnOrigin) {
			t.Fatal("unsafe return accepted")
		}
	}
	if !sandboxCheckoutURL("https://checkout.stripe.com/c/pay/cs_test_example") {
		t.Fatal("Stripe hosted URL rejected")
	}
	for _, raw := range []string{"http://checkout.stripe.com/pay", "https://checkout.stripe.com.attacker.invalid/pay", "https://checkout.stripe.com:444/pay", "https://user@checkout.stripe.com/pay"} {
		if sandboxCheckoutURL(raw) {
			t.Fatal("unsafe Checkout URL accepted")
		}
	}
	h := stripeSandboxHostGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), "127.0.0.1:8083")
	for _, host := range []string{"127.0.0.1:8083", "attacker.invalid", "localhost:8083"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8083/healthz", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusForbidden
		if host == "127.0.0.1:8083" {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Fatal("unexpected exact Host gate")
		}
	}
}

func TestStripeSandboxFlags(t *testing.T) {
	if validateStripeSandboxFlags(true, "A", false, false, false) != nil {
		t.Fatal("explicit Sandbox flags rejected")
	}
	for _, tenant := range []string{"", "D", "a"} {
		if validateStripeSandboxFlags(true, tenant, false, false, false) == nil {
			t.Fatal("invalid tenant accepted")
		}
	}
	if validateStripeSandboxFlags(true, "A", true, false, false) == nil || validateStripeSandboxFlags(true, "A", false, true, false) == nil || validateStripeSandboxFlags(true, "A", false, false, true) == nil || validateStripeSandboxFlags(false, "A", false, false, false) == nil {
		t.Fatal("mixed fixture modes accepted")
	}
}

func TestStripeSandboxCredentialDocumentOmitsStripeSecret(t *testing.T) {
	secret := "whsec_" + strings.Repeat("y", 24)
	doc := buildCredentialDocument(&testguest.Fixture{}, testoperations.CredentialSet{}, false, false, false, secret, 8083)
	doc.StripeSandbox = true
	raw, err := json.Marshal(doc)
	if err != nil || strings.Contains(string(raw), secret) || strings.Contains(string(raw), "webhook_fixture_secret") {
		t.Fatal("Sandbox document exposed a Stripe signing secret")
	}
}
