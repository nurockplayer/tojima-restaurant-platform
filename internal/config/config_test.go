package config

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}
func guestEnv() map[string]string {
	return map[string]string{
		"APP_ADDR": "0.0.0.0:8080", "DATABASE_URL": "postgres://user:private-db-password@db/app",
		"SESSION_TTL": "4h", "GRANT_TTL": "1h", "GRANT_WINDOW": "1m", "GRANT_WINDOW_MAX": "100",
		"PIN_TTL": "15m", "PIN_LOCKOUT": "1m", "PIN_MAX_ATTEMPTS": "3", "PIN_LENGTH": "6",
		"PIN_PEPPER":                  base64.StdEncoding.EncodeToString([]byte(strings.Repeat("p", 32))),
		"PAYMENT_CREATE_RETRY_WINDOW": "20h", "PAYMENT_CONTINUATION_TTL": "30m", "PAYMENT_WORK_LEASE_TTL": "30s",
	}
}
func stripeEnv() map[string]string {
	values := guestEnv()
	for key, value := range map[string]string{
		"STRIPE_MODE": "test", "STRIPE_SECRET_KEY": "sk_test_private-test-material", "STRIPE_WEBHOOK_SECRET": "whsec_private-webhook-material",
		"STRIPE_ACCOUNT_ID": "acct_synthetic", "STRIPE_CREDENTIAL_REF": "merchant:test:v1", "STRIPE_API_VERSION": stripeprovider.APIVersion,
		"APP_PUBLIC_ORIGIN": "https://guest.example.invalid", "STRIPE_SUCCESS_URL": "https://guest.example.invalid/return?result=success",
		"STRIPE_CANCEL_URL": "https://guest.example.invalid/return?result=cancel", "STRIPE_MAX_AMOUNT_JPY": "1000", "CHECKOUT_PROOF_TTL": "30m",
		"CHECKOUT_SIGNING_KEYS": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))),
	} {
		values[key] = value
	}
	return values
}

func TestLoadAddressFromEnv(t *testing.T) {
	for _, address := range []string{"", " ", "localhost", "localhost:70000", "localhost:0", "localhost:+80", "bad host:80"} {
		if _, err := LoadAddressFromEnv(env(map[string]string{"APP_ADDR": address})); err == nil {
			t.Errorf("accepted invalid address %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:8080", "0.0.0.0:8080", "[::]:8080"} {
		if got, err := LoadAddressFromEnv(env(map[string]string{"APP_ADDR": address})); err != nil || got != address {
			t.Errorf("valid address rejected: %v", err)
		}
	}
	if _, err := LoadAddressFromEnv(nil); err == nil {
		t.Fatal("nil lookup accepted")
	}
}
func TestNormalStartupRequiresDBAndPolicy(t *testing.T) {
	if _, err := LoadFromEnv(nil); err == nil {
		t.Fatal("nil lookup accepted")
	}
	if _, err := LoadFromEnv(env(map[string]string{"APP_ADDR": "127.0.0.1:8080"})); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("missing DB error = %v", err)
	}
	for key := range guestEnv() {
		values := guestEnv()
		delete(values, key)
		if _, err := LoadFromEnv(env(values)); err == nil {
			t.Errorf("accepted missing %s", key)
		}
	}
	values := guestEnv()
	c, err := LoadFromEnv(env(values))
	if err != nil || c.HealthOnly || c.Stripe.Enabled || c.WebRoot != "/app/web/dist" {
		t.Fatalf("guest config = %v, %v", c, err)
	}
	values["APP_WEB_DIST"] = "/custom/dist"
	c, err = LoadFromEnv(env(values))
	if err != nil || c.WebRoot != "/custom/dist" {
		t.Fatalf("custom web root: %v", err)
	}
}
func TestDatabaseURLOrLibpq(t *testing.T) {
	values := guestEnv()
	delete(values, "DATABASE_URL")
	values["PGHOST"], values["PGUSER"], values["PGDATABASE"] = "db", "app", "app"
	c, err := LoadFromEnv(env(values))
	if err != nil || c.DatabaseURL != "" {
		t.Fatalf("libpq rejected: %v", err)
	}
	for _, key := range []string{"PGHOST", "PGUSER", "PGDATABASE"} {
		copy := guestEnv()
		delete(copy, "DATABASE_URL")
		for k, v := range values {
			copy[k] = v
		}
		delete(copy, key)
		if _, err := LoadFromEnv(env(copy)); err == nil {
			t.Errorf("accepted incomplete libpq: %s", key)
		}
	}
	values["DATABASE_URL"] = "postgres://explicit/db"
	c, err = LoadFromEnv(env(values))
	if err != nil || c.DatabaseURL != values["DATABASE_URL"] {
		t.Fatalf("URL precedence: %v", err)
	}
}
func TestExplicitHealthOnly(t *testing.T) {
	values := map[string]string{"APP_ADDR": "127.0.0.1:8080", "APP_HEALTH_ONLY": "true"}
	c, err := LoadFromEnv(env(values))
	if err != nil || !c.HealthOnly {
		t.Fatalf("health-only rejected: %v", err)
	}
	for _, key := range []string{"DATABASE_URL", "PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGSERVICE", "PGSSLMODE", "STRIPE_MODE", "STRIPE_SECRET_KEY"} {
		t.Run(key, func(t *testing.T) {
			withConfig := map[string]string{"APP_ADDR": "127.0.0.1:8080", "APP_HEALTH_ONLY": "true", key: "misconfigured"}
			if _, err := LoadFromEnv(env(withConfig)); err == nil {
				t.Errorf("health-only accepted %s", key)
			}
		})
	}
	values["APP_HEALTH_ONLY"] = "yes"
	if _, err := LoadFromEnv(env(values)); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestApprovedTTLCeilings(t *testing.T) {
	for _, test := range []struct {
		key, maximum string
		stripe       bool
	}{
		{"SESSION_TTL", "4h", false},
		{"GRANT_TTL", "1h", false},
		{"PIN_TTL", "15m", false},
		{"PAYMENT_CONTINUATION_TTL", "30m", false},
		{"CHECKOUT_PROOF_TTL", "30m", true},
	} {
		t.Run(test.key, func(t *testing.T) {
			values := guestEnv()
			if test.stripe {
				values = stripeEnv()
			}
			values[test.key] = test.maximum
			if _, err := LoadFromEnv(env(values)); err != nil {
				t.Fatalf("maximum %s rejected: %v", test.maximum, err)
			}
			values[test.key] = test.maximum + "1s"
			if _, err := LoadFromEnv(env(values)); err == nil {
				t.Fatalf("maximum plus one second accepted")
			}
		})
	}
}
func TestInvalidGuestPolicy(t *testing.T) {
	for key, value := range map[string]string{
		"SESSION_TTL": "0s", "GRANT_TTL": "-1m", "GRANT_WINDOW": "garbage", "GRANT_WINDOW_MAX": "10001",
		"PIN_LENGTH": "3", "PIN_MAX_ATTEMPTS": "21", "PIN_PEPPER": "invalid-private-material",
		"PAYMENT_CREATE_RETRY_WINDOW": "24h", "PAYMENT_CONTINUATION_TTL": "0s", "PAYMENT_WORK_LEASE_TTL": "0s",
	} {
		t.Run(key, func(t *testing.T) {
			values := guestEnv()
			values[key] = value
			if _, err := LoadFromEnv(env(values)); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}
func TestStripeRequiresCompleteExplicitTestConfiguration(t *testing.T) {
	c, err := LoadFromEnv(env(stripeEnv()))
	if err != nil || !c.Stripe.Enabled {
		t.Fatalf("valid test config rejected: %v", err)
	}
	for _, key := range stripeEnvKeys {
		values := stripeEnv()
		delete(values, key)
		if _, err := LoadFromEnv(env(values)); err == nil {
			t.Errorf("accepted missing %s", key)
		}
	}
	for key, value := range map[string]string{
		"STRIPE_MODE": "live", "STRIPE_SECRET_KEY": "sk_live_private-material", "APP_PUBLIC_ORIGIN": "http://localhost:8080",
		"STRIPE_SUCCESS_URL": "http://example.invalid", "STRIPE_ACCOUNT_ID": "wrong", "STRIPE_CREDENTIAL_REF": "bad ref",
		"STRIPE_API_VERSION": "old", "STRIPE_MAX_AMOUNT_JPY": "10001", "CHECKOUT_SIGNING_KEYS": "secret", "CHECKOUT_PROOF_TTL": "0s",
	} {
		values := stripeEnv()
		values[key] = value
		if _, err := LoadFromEnv(env(values)); err == nil {
			t.Errorf("accepted invalid %s", key)
		}
	}
	for _, mode := range []string{"", "disabled"} {
		for _, key := range stripeEnvKeys {
			values := guestEnv()
			values["STRIPE_MODE"] = mode
			values[key] = "secret"
			if _, err := LoadFromEnv(env(values)); err == nil {
				t.Errorf("accepted disabled mode with %s", key)
			}
		}
	}
}
func TestSecretsAreRedacted(t *testing.T) {
	values := stripeEnv()
	c, err := LoadFromEnv(env(values))
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{fmt.Sprintf("%v %+v %#v", c, c, c), fmt.Sprintf("%v %+v %#v", c.Stripe, c.Stripe, c.Stripe)} {
		for _, key := range []string{"DATABASE_URL", "STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "PIN_PEPPER", "CHECKOUT_SIGNING_KEYS"} {
			if strings.Contains(output, values[key]) {
				t.Errorf("format leaked %s", key)
			}
		}
	}
}
