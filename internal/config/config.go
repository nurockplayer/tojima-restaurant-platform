// Package config loads and validates runtime configuration.
package config

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// Config is loaded once at startup. It contains secrets; formatting is redacted.
type Config struct {
	HTTPAddress string
	HealthOnly  bool
	DatabaseURL string `json:"-"`
	WebRoot     string
	Sessions    tablesession.Options `json:"-"`
	Payments    payments.Config
	Stripe      StripeConfig `json:"-"`
}

type StripeConfig struct {
	Enabled                                            bool
	SecretKey, WebhookSecret, AccountID, CredentialRef string
	Origin, SuccessURL, CancelURL                      string
	MaxAmountJPY                                       int64
	SigningKeys                                        [][]byte
	ProofTTL                                           time.Duration
}

func (Config) Format(w fmt.State, _ rune) {
	_, _ = io.WriteString(w, "[redacted runtime configuration]")
}
func (StripeConfig) Format(w fmt.State, _ rune) {
	_, _ = io.WriteString(w, "[redacted Stripe configuration]")
}

// LoadAddressFromEnv is also used by the probe: probing needs no DB or secrets.
func LoadAddressFromEnv(lookup func(string) (string, bool)) (string, error) {
	if lookup == nil {
		return "", fmt.Errorf("environment lookup is required")
	}
	address, _ := lookup("APP_ADDR")
	if strings.TrimSpace(address) == "" {
		return "", fmt.Errorf("APP_ADDR is required")
	}
	if err := validateAddress(address); err != nil {
		return "", fmt.Errorf("APP_ADDR must be a valid host:port address")
	}
	return address, nil
}

// LoadFromEnv refuses an accidentally health-only deployment. A DB and explicit
// security policy are required unless APP_HEALTH_ONLY=true is deliberately set.
func LoadFromEnv(lookup func(string) (string, bool)) (Config, error) {
	var c Config
	var err error
	c.HTTPAddress, err = LoadAddressFromEnv(lookup)
	if err != nil {
		return Config{}, err
	}
	get := func(key string) string { v, _ := lookup(key); return v }
	switch get("APP_HEALTH_ONLY") {
	case "", "false":
	case "true":
		c.HealthOnly = true
	default:
		return Config{}, fmt.Errorf("APP_HEALTH_ONLY must be true or false")
	}
	c.DatabaseURL = get("DATABASE_URL")
	if c.HealthOnly {
		for _, key := range []string{"DATABASE_URL", "PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGSERVICE", "PGSSLMODE"} {
			if get(key) != "" {
				return Config{}, fmt.Errorf("APP_HEALTH_ONLY cannot be combined with DB or Stripe configuration")
			}
		}
		if get("STRIPE_MODE") != "" && get("STRIPE_MODE") != "disabled" {
			return Config{}, fmt.Errorf("APP_HEALTH_ONLY cannot be combined with DB or Stripe configuration")
		}
		for _, key := range stripeEnvKeys {
			if get(key) != "" {
				return Config{}, fmt.Errorf("APP_HEALTH_ONLY cannot be combined with %s", key)
			}
		}
		return c, nil
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		for _, key := range []string{"PGHOST", "PGDATABASE", "PGUSER"} {
			if strings.TrimSpace(get(key)) == "" {
				return Config{}, fmt.Errorf("DATABASE_URL or PGHOST, PGDATABASE and PGUSER are required")
			}
		}
		c.DatabaseURL = ""
	}
	c.WebRoot = get("APP_WEB_DIST")
	if c.WebRoot == "" {
		c.WebRoot = "/app/web/dist"
	}
	// No unapproved guest TTL or PIN policy is silently inherited from a harness.
	durations := []struct {
		key string
		dst *time.Duration
	}{
		{"SESSION_TTL", &c.Sessions.SessionTTL}, {"GRANT_TTL", &c.Sessions.GrantTTL},
		{"GRANT_WINDOW", &c.Sessions.GrantWindow}, {"PIN_TTL", &c.Sessions.PINTTL},
		{"PIN_LOCKOUT", &c.Sessions.PINLockout}, {"PAYMENT_CREATE_RETRY_WINDOW", &c.Payments.CreateRetryWindow},
		{"PAYMENT_CONTINUATION_TTL", &c.Payments.ContinuationTTL}, {"PAYMENT_WORK_LEASE_TTL", &c.Payments.WorkLeaseTTL},
	}
	for _, v := range durations {
		*v.dst, err = time.ParseDuration(get(v.key))
		if err != nil || *v.dst < time.Millisecond {
			return Config{}, fmt.Errorf("%s must be a positive duration of at least 1ms", v.key)
		}
	}
	for _, v := range []struct {
		key      string
		got, max time.Duration
	}{
		{"SESSION_TTL", c.Sessions.SessionTTL, 4 * time.Hour},
		{"GRANT_TTL", c.Sessions.GrantTTL, time.Hour},
		{"PIN_TTL", c.Sessions.PINTTL, 15 * time.Minute},
		{"PAYMENT_CONTINUATION_TTL", c.Payments.ContinuationTTL, 30 * time.Minute},
	} {
		if v.got > v.max {
			return Config{}, fmt.Errorf("%s exceeds the approved maximum %s (Owner decision D12)", v.key, v.max)
		}
	}
	if c.Payments.CreateRetryWindow >= 24*time.Hour {
		return Config{}, fmt.Errorf("PAYMENT_CREATE_RETRY_WINDOW must be less than 24h")
	}
	for _, v := range []struct {
		key string
		dst *int
	}{
		{"GRANT_WINDOW_MAX", &c.Sessions.GrantWindowMax}, {"PIN_MAX_ATTEMPTS", &c.Sessions.PINMaxAttempts}, {"PIN_LENGTH", &c.Sessions.PINLength},
	} {
		*v.dst, err = strconv.Atoi(get(v.key))
		if err != nil {
			return Config{}, fmt.Errorf("%s must be an integer", v.key)
		}
	}
	c.Sessions.PINPepper, err = decodeKey(get("PIN_PEPPER"))
	if err != nil {
		return Config{}, fmt.Errorf("PIN_PEPPER must be base64 containing at least 32 bytes")
	}
	if err := c.Sessions.Validate(); err != nil {
		return Config{}, fmt.Errorf("guest policy: %w", err)
	}
	switch get("STRIPE_MODE") {
	case "", "disabled":
		for _, key := range stripeEnvKeys {
			if get(key) != "" {
				return Config{}, fmt.Errorf("%s requires STRIPE_MODE=test", key)
			}
		}
		return c, nil
	case "test":
	case "live":
		return Config{}, fmt.Errorf("STRIPE_MODE=live is unsupported by the current provider; live activation requires a separate approved adapter")
	default:
		return Config{}, fmt.Errorf("STRIPE_MODE must be disabled or test; live is unsupported")
	}
	c.Stripe.Enabled = true
	c.Stripe.SecretKey, c.Stripe.WebhookSecret = get("STRIPE_SECRET_KEY"), get("STRIPE_WEBHOOK_SECRET")
	if !validSecret(c.Stripe.SecretKey, "sk_test_") {
		return Config{}, fmt.Errorf("STRIPE_SECRET_KEY requires a test secret key")
	}
	if !validSecret(c.Stripe.WebhookSecret, "whsec_") {
		return Config{}, fmt.Errorf("STRIPE_WEBHOOK_SECRET is required and must be a signing secret")
	}
	if get("STRIPE_API_VERSION") != stripeprovider.APIVersion {
		return Config{}, fmt.Errorf("STRIPE_API_VERSION must be %s", stripeprovider.APIVersion)
	}
	c.Stripe.AccountID, c.Stripe.CredentialRef = get("STRIPE_ACCOUNT_ID"), get("STRIPE_CREDENTIAL_REF")
	if !regexp.MustCompile(`^acct_[A-Za-z0-9]+$`).MatchString(c.Stripe.AccountID) {
		return Config{}, fmt.Errorf("STRIPE_ACCOUNT_ID is required and must be an account ID")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`).MatchString(c.Stripe.CredentialRef) {
		return Config{}, fmt.Errorf("STRIPE_CREDENTIAL_REF is required and must be a valid reference")
	}
	c.Stripe.Origin, c.Stripe.SuccessURL, c.Stripe.CancelURL = get("APP_PUBLIC_ORIGIN"), get("STRIPE_SUCCESS_URL"), get("STRIPE_CANCEL_URL")
	if !validHTTPSURL(c.Stripe.Origin, true) {
		return Config{}, fmt.Errorf("APP_PUBLIC_ORIGIN must be an HTTPS origin without a trailing slash")
	}
	for _, key := range []string{"STRIPE_SUCCESS_URL", "STRIPE_CANCEL_URL"} {
		if !validHTTPSURL(get(key), false) {
			return Config{}, fmt.Errorf("%s must be an HTTPS URL", key)
		}
	}
	c.Stripe.MaxAmountJPY, err = strconv.ParseInt(get("STRIPE_MAX_AMOUNT_JPY"), 10, 64)
	if err != nil || c.Stripe.MaxAmountJPY <= 0 || c.Stripe.MaxAmountJPY > 10000 {
		return Config{}, fmt.Errorf("STRIPE_MAX_AMOUNT_JPY must be between 1 and 10000 for test mode")
	}
	c.Stripe.ProofTTL, err = time.ParseDuration(get("CHECKOUT_PROOF_TTL"))
	if err != nil || c.Stripe.ProofTTL < time.Millisecond {
		return Config{}, fmt.Errorf("CHECKOUT_PROOF_TTL must be a positive duration of at least 1ms")
	}
	if c.Stripe.ProofTTL > 30*time.Minute {
		return Config{}, fmt.Errorf("CHECKOUT_PROOF_TTL exceeds the approved maximum 30m (Owner decision D12)")
	}
	for _, raw := range strings.Split(get("CHECKOUT_SIGNING_KEYS"), ",") {
		key, err := decodeKey(raw)
		if err != nil {
			return Config{}, fmt.Errorf("CHECKOUT_SIGNING_KEYS requires comma-separated base64 keys of at least 32 bytes, current key first")
		}
		c.Stripe.SigningKeys = append(c.Stripe.SigningKeys, key)
	}
	return c, nil
}

var stripeEnvKeys = []string{"STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "STRIPE_API_VERSION", "STRIPE_ACCOUNT_ID", "STRIPE_CREDENTIAL_REF", "APP_PUBLIC_ORIGIN", "STRIPE_SUCCESS_URL", "STRIPE_CANCEL_URL", "STRIPE_MAX_AMOUNT_JPY", "CHECKOUT_PROOF_TTL", "CHECKOUT_SIGNING_KEYS"}

func decodeKey(raw string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || len(b) < 32 {
		return nil, fmt.Errorf("invalid key")
	}
	return b, nil
}
func validSecret(s, prefix string) bool {
	return strings.HasPrefix(s, prefix) && len(s) >= len(prefix)+8 && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
func validHTTPSURL(raw string, origin bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.String() != raw || strings.ContainsFunc(raw, unicode.IsSpace) {
		return false
	}
	return !origin || (u.Path == "" && u.RawQuery == "" && !u.ForceQuery)
}

func validateAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be a host:port address: %w", err)
	}
	if strings.ContainsAny(host, " \t\r\n/") {
		return fmt.Errorf("host contains invalid whitespace or path characters")
	}
	for _, digit := range portText {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("port must contain decimal digits only")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}
