package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

var errStripeSandboxConfig = errors.New("invalid Stripe Sandbox configuration; consult the test runtime contract")

var sandboxAccountPattern = regexp.MustCompile(`^acct_[A-Za-z0-9]+$`)

const sandboxPlatformAccount = "acct_1URlHu1Hm4CZe205"

const sandboxReturnOrigin = "https://tojima-restaurant-platform-demo.pages.dev"
const sandboxReturnPath = "/stripe-test-return.html"

// Keep secret-bearing configuration private and redacted under fmt, including
// %+v/%#v. JSON omits all unexported fields. Never put this in a credential file.
type stripeSandboxConfig struct {
	apiKey, webhookSecret string
	accounts              map[string]string
	successURL, cancelURL string
	maxAmountJPY          int64
}

func (stripeSandboxConfig) Format(w fmt.State, _ rune) {
	_, _ = io.WriteString(w, "[redacted Stripe Sandbox configuration]")
}

func loadStripeSandboxConfig(getenv func(string) string) (stripeSandboxConfig, error) {
	c := stripeSandboxConfig{accounts: make(map[string]string, 3)}
	if getenv == nil || getenv("TOJIMA_STRIPE_MODE") != stripeprovider.ModeConnectedDirectFixture || getenv("TOJIMA_STRIPE_API_VERSION") != stripeprovider.APIVersion {
		return stripeSandboxConfig{}, errStripeSandboxConfig
	}
	c.apiKey = getenv("TOJIMA_STRIPE_TEST_SECRET_KEY")
	c.webhookSecret = getenv("TOJIMA_STRIPE_TEST_WEBHOOK_SECRET")
	if !sandboxSecret(c.apiKey, "sk_test_") || !sandboxSecret(c.webhookSecret, "whsec_") {
		return stripeSandboxConfig{}, errStripeSandboxConfig
	}
	seen := make(map[string]bool, 3)
	for _, tenant := range []string{"A", "B", "C"} {
		account := getenv("TOJIMA_STRIPE_TEST_ACCOUNT_" + tenant)
		if !sandboxAccountPattern.MatchString(account) || account == sandboxPlatformAccount || seen[account] {
			return stripeSandboxConfig{}, errStripeSandboxConfig
		}
		seen[account], c.accounts[tenant] = true, account
	}
	c.successURL = sandboxReturnOrigin + sandboxReturnPath + "?result=success"
	c.cancelURL = sandboxReturnOrigin + sandboxReturnPath + "?result=cancel"
	if !validSandboxReturnURL(c.successURL, sandboxReturnOrigin) || !validSandboxReturnURL(c.cancelURL, sandboxReturnOrigin) {
		return stripeSandboxConfig{}, errStripeSandboxConfig
	}
	amount := getenv("TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY")
	var err error
	c.maxAmountJPY, err = strconv.ParseInt(amount, 10, 64)
	if err != nil || c.maxAmountJPY <= 0 || strconv.FormatInt(c.maxAmountJPY, 10) != amount {
		return stripeSandboxConfig{}, errStripeSandboxConfig
	}
	return c, nil
}

func sandboxSecret(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) < len(prefix)+8 {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validSandboxReturnURL(raw, trustedOrigin string) bool {
	u, err := url.Parse(raw)
	trusted, trustedErr := url.Parse(trustedOrigin)
	return err == nil && trustedErr == nil && trustedOrigin != "" &&
		u.Scheme == "https" && u.Host != "" && u.User == nil && u.Opaque == "" &&
		(u.RawQuery == "result=success" || u.RawQuery == "result=cancel") &&
		u.Path == sandboxReturnPath && u.RawPath == "" && u.Fragment == "" &&
		trusted.Scheme == "https" && u.Host == trusted.Host &&
		u.String() == raw && strings.TrimSpace(raw) == raw
}

func validateStripeSandboxFlags(sandbox bool, tenant string, fixture, demo, catalog bool) error {
	if !sandbox {
		if tenant != "" {
			return errors.New("-stripe-tenant requires -stripe-sandbox")
		}
		return nil
	}
	if fixture || demo || catalog || (tenant != "A" && tenant != "B" && tenant != "C") {
		return errors.New("-stripe-sandbox requires exactly one -stripe-tenant A, B or C and cannot combine with fixture/demo/catalog modes")
	}
	return nil
}
