// Package stripeprovider contains the narrow Stripe API adapter used by the
// durable payment-attempt service. It deliberately owns no retry,
// one-attempt, tenant mapping, or fulfillment policy.
package stripeprovider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

const (
	APIVersion                 = "2026-09-30.endive"
	CurrencyJPY                = "jpy"
	WebhookMaxBytes            = 1 << 20
	WebhookTolerance           = 5 * time.Minute
	ModeUnconfigured           = "UNCONFIGURED"
	ModeConnectedDirectFixture = "CONNECTED_DIRECT_TEST_FIXTURE"
)

var (
	ErrUnconfigured   = errors.New("stripe provider is not configured")
	ErrInvalid        = errors.New("invalid stripe provider request")
	ErrUnavailable    = errors.New("stripe provider unavailable")
	ErrNotFound       = errors.New("stripe object not found")
	ErrOutcomeUnknown = errors.New("stripe checkout outcome is unknown; reconcile the existing attempt")
	ErrWebhook        = errors.New("stripe webhook verification failed")
	ErrUnsafeResponse = errors.New("stripe response failed validation")
)

var (
	accountIDPattern   = regexp.MustCompile(`^acct_[A-Za-z0-9]+$`)
	objectIDPattern    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,31}_[A-Za-z0-9]+$`)
	idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,255}$`)
)

// Config requires an explicit test-mode key and an explicit transport mode.
// SuccessURL and CancelURL are server-owned, trusted destinations. HTTPClient
// is injectable for contract tests; production callers should use a bounded
// transport with TLS verification enabled.
type Config struct {
	APIKey         string
	Mode           string
	RequestTimeout time.Duration
	MaxAmountJPY   int64
	SuccessURL     string
	CancelURL      string
	HTTPClient     *http.Client
}

type Provider struct {
	client       *stripe.Client
	mode         string
	maxAmountJPY int64
	successURL   string
	cancelURL    string
}

type AccountContext struct {
	Mode            string
	StripeAccountID string
}

type CreateCheckoutRequest struct {
	Account        AccountContext
	TenantID       string
	StoreID        string
	OrderID        string
	AttemptID      string
	IdempotencyKey string
	AmountJPY      int64
	Description    string
}

type CheckoutStatus string
type PaymentStatus string

const (
	CheckoutOpen             CheckoutStatus = "open"
	CheckoutComplete         CheckoutStatus = "complete"
	CheckoutExpired          CheckoutStatus = "expired"
	PaymentUnpaid            PaymentStatus  = "unpaid"
	PaymentPaid              PaymentStatus  = "paid"
	PaymentNoPaymentRequired PaymentStatus  = "no_payment_required"
)

// CheckoutResult is a private provider result. Callers must never put URL into
// shared order/list/table DTOs or logs.
type CheckoutResult struct {
	AccountID       string
	SessionID       string
	PaymentIntentID string
	Status          CheckoutStatus
	PaymentStatus   PaymentStatus
	AmountJPY       int64
	Currency        string
	LiveMode        bool
	ExpiresAt       time.Time
	URL             string
	Metadata        map[string]string
}

type PaymentIntentResult struct {
	AccountID string
	ID        string
	Status    string
	AmountJPY int64
	Currency  string
	LiveMode  bool
	Metadata  map[string]string
}

// CreateRefundRequest carries server-authorized refund facts to Stripe. The
// adapter only transports the request; refund eligibility and totals belong to
// the operations service.
type CreateRefundRequest struct {
	Account         AccountContext
	TenantID        string
	StoreID         string
	OrderID         string
	RefundID        string
	PaymentIntentID string
	IdempotencyKey  string
	AmountJPY       int64
}

type RefundResult struct {
	AccountID       string
	ID              string
	PaymentIntentID string
	AmountJPY       int64
	Currency        string
	Status          string
	LiveMode        bool
	Metadata        map[string]string
}

type VerifiedEvent struct {
	ID             string
	Type           string
	AccountID      string
	APIVersion     string
	CreatedAt      time.Time
	LiveMode       bool
	ObjectID       string
	Checkout       *CheckoutResult
	PaymentIntent  *PaymentIntentResult
	Refund         *RefundResult
	Refunds        []RefundResult
	RefundsHasMore bool
}

func New(cfg Config) (*Provider, error) {
	if cfg.Mode == "" || cfg.Mode == ModeUnconfigured {
		return nil, ErrUnconfigured
	}
	if cfg.Mode != ModeConnectedDirectFixture || !strings.HasPrefix(cfg.APIKey, "sk_test_") || len(cfg.APIKey) < len("sk_test_")+8 || cfg.RequestTimeout <= 0 || cfg.MaxAmountJPY <= 0 || !trustedHTTPSURL(cfg.SuccessURL) || !trustedHTTPSURL(cfg.CancelURL) {
		return nil, ErrInvalid
	}
	var httpClient *http.Client
	if cfg.HTTPClient == nil {
		httpClient = &http.Client{}
	} else {
		clientCopy := *cfg.HTTPClient
		httpClient = &clientCopy
	}
	// The adapter owns a hard upper bound even when a custom transport is
	// injected. Redirects are not followed with a Stripe bearer credential.
	httpClient.Timeout = cfg.RequestTimeout
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient:        httpClient,
		MaxNetworkRetries: stripe.Int64(0),
		EnableTelemetry:   stripe.Bool(false),
		LeveledLogger:     &stripe.LeveledLogger{Level: stripe.LevelNull},
	})
	return &Provider{
		client:       stripe.NewClient(cfg.APIKey, stripe.WithBackends(backends)),
		mode:         cfg.Mode,
		maxAmountJPY: cfg.MaxAmountJPY,
		successURL:   cfg.SuccessURL,
		cancelURL:    cfg.CancelURL,
	}, nil
}

func trustedHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == ""
}

func (p *Provider) validateAccount(a AccountContext) error {
	if p == nil || p.client == nil || p.mode != ModeConnectedDirectFixture || a.Mode != ModeConnectedDirectFixture || !accountIDPattern.MatchString(a.StripeAccountID) {
		return ErrUnconfigured
	}
	return nil
}

func (p *Provider) CreateCheckout(ctx context.Context, req CreateCheckoutRequest) (CheckoutResult, error) {
	if err := p.validateAccount(req.Account); err != nil {
		return CheckoutResult{}, err
	}
	if !validRef(req.TenantID) || !validRef(req.StoreID) || !validRef(req.OrderID) || !validRef(req.AttemptID) || !idempotencyPattern.MatchString(req.IdempotencyKey) || req.AmountJPY <= 0 || req.AmountJPY > p.maxAmountJPY || len(req.Description) > 200 || strings.TrimSpace(req.Description) == "" {
		return CheckoutResult{}, ErrInvalid
	}
	amount := req.AmountJPY
	currency := CurrencyJPY
	mode := "payment"
	qty := int64(1)
	desc := req.Description
	metadata := map[string]string{
		"tenant_id":  req.TenantID,
		"store_id":   req.StoreID,
		"order_id":   req.OrderID,
		"attempt_id": req.AttemptID,
	}
	params := &stripe.CheckoutSessionCreateParams{
		Params:            stripe.Params{Context: ctx, IdempotencyKey: stripe.String(req.IdempotencyKey), StripeAccount: stripe.String(req.Account.StripeAccountID)},
		Mode:              &mode,
		Currency:          &currency,
		SuccessURL:        &p.successURL,
		CancelURL:         &p.cancelURL,
		ClientReferenceID: stripe.String(req.OrderID),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{
			Quantity: &qty,
			PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
				Currency:    &currency,
				UnitAmount:  &amount,
				ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{Name: &desc},
			},
		}},
		Metadata: metadata,
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			Metadata: cloneMap(metadata),
		},
	}
	s, err := p.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		// Do not retry here. Even a canceled request can have reached Stripe;
		// the durable attempt service must reconcile this exact attempt.
		return CheckoutResult{}, ErrOutcomeUnknown
	}
	r, err := checkoutResult(s, req.Account.StripeAccountID)
	if err != nil {
		return CheckoutResult{}, ErrOutcomeUnknown
	}
	// An idempotent recovery may describe a Checkout that has already completed
	// or expired. Only an open Checkout needs a continuation URL; the durable
	// service must still receive completed/paid evidence without one.
	if (r.Status == CheckoutOpen && r.URL == "") || r.AmountJPY != req.AmountJPY || r.Metadata["tenant_id"] != req.TenantID || r.Metadata["store_id"] != req.StoreID || r.Metadata["order_id"] != req.OrderID || r.Metadata["attempt_id"] != req.AttemptID {
		return CheckoutResult{}, ErrOutcomeUnknown
	}
	return r, nil
}

func (p *Provider) RetrieveCheckout(ctx context.Context, a AccountContext, sessionID string) (CheckoutResult, error) {
	if err := p.validateAccount(a); err != nil {
		return CheckoutResult{}, err
	}
	if !validStripeObjectID(sessionID, "cs") {
		return CheckoutResult{}, ErrInvalid
	}
	s, err := p.client.V1CheckoutSessions.Retrieve(ctx, sessionID, &stripe.CheckoutSessionRetrieveParams{Params: stripe.Params{Context: ctx, StripeAccount: stripe.String(a.StripeAccountID)}})
	if err != nil {
		return CheckoutResult{}, classifyStripeError(err)
	}
	result, err := checkoutResult(s, a.StripeAccountID)
	if err != nil {
		return CheckoutResult{}, err
	}
	if result.SessionID != sessionID {
		return CheckoutResult{}, ErrUnsafeResponse
	}
	return result, nil
}

func (p *Provider) ExpireCheckout(ctx context.Context, a AccountContext, sessionID string, idempotencyKey string) (CheckoutResult, error) {
	if err := p.validateAccount(a); err != nil {
		return CheckoutResult{}, err
	}
	if !validStripeObjectID(sessionID, "cs") || !idempotencyPattern.MatchString(idempotencyKey) {
		return CheckoutResult{}, ErrInvalid
	}
	s, err := p.client.V1CheckoutSessions.Expire(ctx, sessionID, &stripe.CheckoutSessionExpireParams{Params: stripe.Params{Context: ctx, StripeAccount: stripe.String(a.StripeAccountID), IdempotencyKey: stripe.String(idempotencyKey)}})
	if err != nil {
		var se *stripe.Error
		if errors.As(err, &se) && se.HTTPStatusCode == http.StatusNotFound {
			return CheckoutResult{}, ErrNotFound
		}
		return CheckoutResult{}, ErrOutcomeUnknown
	}
	result, err := checkoutResult(s, a.StripeAccountID)
	if err != nil {
		return CheckoutResult{}, ErrOutcomeUnknown
	}
	if result.SessionID != sessionID {
		return CheckoutResult{}, ErrOutcomeUnknown
	}
	return result, nil
}

func (p *Provider) RetrievePaymentIntent(ctx context.Context, a AccountContext, paymentIntentID string) (PaymentIntentResult, error) {
	if err := p.validateAccount(a); err != nil {
		return PaymentIntentResult{}, err
	}
	if !validStripeObjectID(paymentIntentID, "pi") {
		return PaymentIntentResult{}, ErrInvalid
	}
	pi, err := p.client.V1PaymentIntents.Retrieve(ctx, paymentIntentID, &stripe.PaymentIntentRetrieveParams{Params: stripe.Params{Context: ctx, StripeAccount: stripe.String(a.StripeAccountID)}})
	if err != nil {
		return PaymentIntentResult{}, classifyStripeError(err)
	}
	result, err := paymentIntentResult(pi, a.StripeAccountID)
	if err != nil {
		return PaymentIntentResult{}, err
	}
	if result.ID != paymentIntentID {
		return PaymentIntentResult{}, ErrUnsafeResponse
	}
	return result, nil
}

// CreateRefund requests an exact integer-JPY refund against the original
// PaymentIntent. Stripe idempotency is mandatory because a transport failure
// can leave the result unknown to the caller.
func (p *Provider) CreateRefund(ctx context.Context, req CreateRefundRequest) (RefundResult, error) {
	if err := p.validateAccount(req.Account); err != nil {
		return RefundResult{}, err
	}
	if !validRef(req.TenantID) || !validRef(req.StoreID) || !validRef(req.OrderID) || !validRef(req.RefundID) || !validStripeObjectID(req.PaymentIntentID, "pi") || !idempotencyPattern.MatchString(req.IdempotencyKey) || req.AmountJPY <= 0 || req.AmountJPY > p.maxAmountJPY {
		return RefundResult{}, ErrInvalid
	}
	currency := CurrencyJPY
	params := &stripe.RefundCreateParams{
		Params:        stripe.Params{Context: ctx, IdempotencyKey: stripe.String(req.IdempotencyKey), StripeAccount: stripe.String(req.Account.StripeAccountID)},
		PaymentIntent: stripe.String(req.PaymentIntentID), Amount: stripe.Int64(req.AmountJPY), Currency: &currency,
		Metadata: map[string]string{"tenant_id": req.TenantID, "store_id": req.StoreID, "order_id": req.OrderID, "refund_id": req.RefundID},
	}
	r, err := p.client.V1Refunds.Create(ctx, params)
	if err != nil {
		return RefundResult{}, ErrOutcomeUnknown
	}
	result, err := refundResult(r, req.Account.StripeAccountID)
	if err != nil || result.PaymentIntentID != req.PaymentIntentID || result.AmountJPY != req.AmountJPY || !refundMetadataMatches(result.Metadata, req.TenantID, req.StoreID, req.OrderID, req.RefundID) {
		return RefundResult{}, ErrOutcomeUnknown
	}
	return result, nil
}

func (p *Provider) RetrieveRefund(ctx context.Context, a AccountContext, refundID string) (RefundResult, error) {
	if err := p.validateAccount(a); err != nil {
		return RefundResult{}, err
	}
	if !validStripeObjectID(refundID, "re") {
		return RefundResult{}, ErrInvalid
	}
	r, err := p.client.V1Refunds.Retrieve(ctx, refundID, &stripe.RefundRetrieveParams{Params: stripe.Params{Context: ctx, StripeAccount: stripe.String(a.StripeAccountID)}})
	if err != nil {
		return RefundResult{}, classifyStripeError(err)
	}
	result, err := refundResult(r, a.StripeAccountID)
	if err != nil {
		return RefundResult{}, err
	}
	if result.ID != refundID || result.PaymentIntentID == "" {
		return RefundResult{}, ErrUnsafeResponse
	}
	return result, nil
}

func validRef(s string) bool {
	return len(s) > 0 && len(s) <= 200 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func validStripeObjectID(s, prefix string) bool {
	return strings.HasPrefix(s, prefix+"_") && objectIDPattern.MatchString(s)
}

func checkoutResult(s *stripe.CheckoutSession, requestedAccount string) (CheckoutResult, error) {
	if s == nil || !validStripeObjectID(s.ID, "cs") || s.Mode != stripe.CheckoutSessionModePayment || s.ExpiresAt <= 0 || s.Livemode || s.Currency != stripe.Currency(CurrencyJPY) || s.AmountTotal <= 0 || s.AutomaticTax != nil && s.AutomaticTax.Enabled || s.Status != stripe.CheckoutSessionStatusOpen && s.Status != stripe.CheckoutSessionStatusComplete && s.Status != stripe.CheckoutSessionStatusExpired || s.PaymentStatus != stripe.CheckoutSessionPaymentStatusUnpaid && s.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid && s.PaymentStatus != stripe.CheckoutSessionPaymentStatusNoPaymentRequired {
		return CheckoutResult{}, ErrUnsafeResponse
	}
	if s.URL != "" && !trustedHTTPSURL(s.URL) {
		return CheckoutResult{}, ErrUnsafeResponse
	}
	piID := ""
	if s.PaymentIntent != nil {
		piID = s.PaymentIntent.ID
		if !validStripeObjectID(piID, "pi") {
			return CheckoutResult{}, ErrUnsafeResponse
		}
	}
	account := requestedAccount
	return CheckoutResult{AccountID: account, SessionID: s.ID, PaymentIntentID: piID, Status: CheckoutStatus(s.Status), PaymentStatus: PaymentStatus(s.PaymentStatus), AmountJPY: s.AmountTotal, Currency: string(s.Currency), LiveMode: s.Livemode, ExpiresAt: time.Unix(s.ExpiresAt, 0).UTC(), URL: s.URL, Metadata: cloneMap(s.Metadata)}, nil
}

func paymentIntentResult(pi *stripe.PaymentIntent, requestedAccount string) (PaymentIntentResult, error) {
	if pi == nil || !validStripeObjectID(pi.ID, "pi") || pi.Status == "" || pi.Livemode || pi.Currency != stripe.Currency(CurrencyJPY) || pi.Amount <= 0 {
		return PaymentIntentResult{}, ErrUnsafeResponse
	}
	account := requestedAccount
	return PaymentIntentResult{AccountID: account, ID: pi.ID, Status: string(pi.Status), AmountJPY: pi.Amount, Currency: string(pi.Currency), LiveMode: pi.Livemode, Metadata: cloneMap(pi.Metadata)}, nil
}

// Unknown statuses are transported intact for fail-closed reconciliation.
func refundResult(r *stripe.Refund, requestedAccount string) (RefundResult, error) {
	if r == nil || !validStripeObjectID(r.ID, "re") || r.Currency != stripe.Currency(CurrencyJPY) || r.Amount <= 0 {
		return RefundResult{}, ErrUnsafeResponse
	}
	piID := ""
	if r.PaymentIntent != nil {
		piID = r.PaymentIntent.ID
	}
	if piID != "" && !validStripeObjectID(piID, "pi") {
		return RefundResult{}, ErrUnsafeResponse
	}
	return RefundResult{AccountID: requestedAccount, ID: r.ID, PaymentIntentID: piID, AmountJPY: r.Amount, Currency: string(r.Currency), Status: string(r.Status), Metadata: cloneMap(r.Metadata)}, nil
}

func refundMetadataMatches(m map[string]string, tenant, store, order, refund string) bool {
	return m["tenant_id"] == tenant && m["store_id"] == store && m["order_id"] == order && m["refund_id"] == refund
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func classifyStripeError(err error) error {
	var se *stripe.Error
	if errors.As(err, &se) && se.HTTPStatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return ErrUnavailable
}
