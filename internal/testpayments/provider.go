// Package testpayments contains in-process payment fixtures. It performs no
// Stripe HTTP calls and must never be composed into the production server.
package testpayments

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

// Provider keeps explicit account-qualified synthetic sessions. Its reserved
// .invalid URLs cannot accept payment; they are evidence of a private handoff
// only, not Stripe Test Mode or real payment links.
type Provider struct {
	mu                   sync.Mutex
	sessions             map[string]stripeprovider.CheckoutResult
	keys                 map[string]string
	digests              map[string][32]byte
	loseNext             bool
	creates              int
	retrieves            int
	expires              int
	nextStatus           stripeprovider.CheckoutStatus
	nextPayment          stripeprovider.PaymentStatus
	hasNextOutcome       bool
	nextCreateHook       func(stripeprovider.CheckoutResult)
	nextExpireHook       func(stripeprovider.CheckoutResult)
	nextExpireStatus     stripeprovider.CheckoutStatus
	nextExpirePayment    stripeprovider.PaymentStatus
	nextExpireUnknown    bool
	hasNextExpireOutcome bool
	refunds              map[string]stripeprovider.RefundResult
	refundKeys           map[string]string
	refundDigests        map[string][32]byte
	nextRefundStatus     string
	nextRefundUnknown    bool
	hasNextRefundOutcome bool
	refundCreates        int
	refundRetrieves      int
}

func NewProvider() *Provider {
	return &Provider{sessions: make(map[string]stripeprovider.CheckoutResult), keys: make(map[string]string), digests: make(map[string][32]byte), refunds: make(map[string]stripeprovider.RefundResult), refundKeys: make(map[string]string), refundDigests: make(map[string][32]byte)}
}

// CreateRefund is an in-process idempotent refund fixture. A configured
// unknown response stores the refund first, so retrying the same request
// returns the existing provider object.
func (p *Provider) CreateRefund(ctx context.Context, req stripeprovider.CreateRefundRequest) (stripeprovider.RefundResult, error) {
	if ctx.Err() != nil {
		return stripeprovider.RefundResult{}, stripeprovider.ErrOutcomeUnknown
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refundCreates++
	if req.Account.Mode != stripeprovider.ModeConnectedDirectFixture || req.Account.StripeAccountID == "" || req.AmountJPY <= 0 || req.IdempotencyKey == "" || req.PaymentIntentID == "" {
		return stripeprovider.RefundResult{}, stripeprovider.ErrInvalid
	}
	body, _ := json.Marshal(req)
	digest := sha256.Sum256(body)
	key := req.Account.StripeAccountID + ":" + req.IdempotencyKey
	if id, ok := p.refundKeys[key]; ok {
		if p.refundDigests[key] != digest {
			return stripeprovider.RefundResult{}, stripeprovider.ErrInvalid
		}
		return cloneRefund(p.refunds[id]), nil
	}
	status := "pending"
	if p.hasNextRefundOutcome {
		status = p.nextRefundStatus
	}
	lateFailure := status == "succeeded_then_failed"
	if lateFailure {
		status = "succeeded"
	}
	if status == "" {
		status = "pending"
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return stripeprovider.RefundResult{}, stripeprovider.ErrOutcomeUnknown
	}
	id := "re_fixture" + hex.EncodeToString(nonce[:])
	r := stripeprovider.RefundResult{AccountID: req.Account.StripeAccountID, ID: id, PaymentIntentID: req.PaymentIntentID, AmountJPY: req.AmountJPY, Currency: stripeprovider.CurrencyJPY, Status: status, Metadata: map[string]string{"tenant_id": req.TenantID, "store_id": req.StoreID, "order_id": req.OrderID, "refund_id": req.RefundID}}
	p.refunds[id], p.refundKeys[key], p.refundDigests[key] = r, id, digest
	if lateFailure {
		failed := r
		failed.Status = "failed"
		p.refunds[id] = failed
	}
	unknown := p.hasNextRefundOutcome && p.nextRefundUnknown
	p.hasNextRefundOutcome, p.nextRefundUnknown, p.nextRefundStatus = false, false, ""
	if unknown {
		return stripeprovider.RefundResult{}, stripeprovider.ErrOutcomeUnknown
	}
	return cloneRefund(r), nil
}

func (p *Provider) RetrieveRefund(ctx context.Context, account stripeprovider.AccountContext, id string) (stripeprovider.RefundResult, error) {
	if ctx.Err() != nil {
		return stripeprovider.RefundResult{}, stripeprovider.ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refundRetrieves++
	r, ok := p.refunds[id]
	if !ok || account.Mode != stripeprovider.ModeConnectedDirectFixture || r.AccountID != account.StripeAccountID {
		return stripeprovider.RefundResult{}, stripeprovider.ErrNotFound
	}
	return cloneRefund(r), nil
}

// SetNextRefundOutcome configures the next created fixture as pending (delayed),
// succeeded, failed, or succeeded_then_failed (success response, later retrieved
// failure). unknown simulates a lost response after provider write.
func (p *Provider) SetNextRefundOutcome(status string, unknown bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextRefundStatus, p.nextRefundUnknown, p.hasNextRefundOutcome = status, unknown, true
}

// SetRefundOutcome models an asynchronous provider transition such as delayed
// processing or a retryable failure.
func (p *Provider) SetRefundOutcome(id, status string) (stripeprovider.RefundResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.refunds[id]
	if !ok {
		return stripeprovider.RefundResult{}, stripeprovider.ErrNotFound
	}
	r.Status = status
	p.refunds[id] = r
	return cloneRefund(r), nil
}

func (p *Provider) RefundOperationCounts() (creates, retrieves int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refundCreates, p.refundRetrieves
}

// RefundByBusinessID exposes the fake's persisted provider object to durable
// worker integration tests after simulating a lost CreateRefund response.
func (p *Provider) RefundByBusinessID(refundID string) (stripeprovider.RefundResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, result := range p.refunds {
		if result.Metadata["refund_id"] == refundID {
			return cloneRefund(result), true
		}
	}
	return stripeprovider.RefundResult{}, false
}

func (p *Provider) CreateCheckout(ctx context.Context, req stripeprovider.CreateCheckoutRequest) (stripeprovider.CheckoutResult, error) {
	if err := ctx.Err(); err != nil {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrOutcomeUnknown
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	if req.Account.Mode != stripeprovider.ModeConnectedDirectFixture || req.Account.StripeAccountID == "" || req.AmountJPY <= 0 || req.IdempotencyKey == "" {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrInvalid
	}
	encoded, _ := json.Marshal(req)
	digest := sha256.Sum256(encoded)
	key := req.Account.StripeAccountID + ":" + req.IdempotencyKey
	if id, ok := p.keys[key]; ok {
		if p.digests[key] != digest {
			return stripeprovider.CheckoutResult{}, stripeprovider.ErrInvalid
		}
		return clone(p.sessions[id]), nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrOutcomeUnknown
	}
	id := "cs_fixture" + hex.EncodeToString(nonce[:])
	r := stripeprovider.CheckoutResult{
		AccountID: req.Account.StripeAccountID, SessionID: id,
		PaymentIntentID: "pi_fixture" + hex.EncodeToString(nonce[:]),
		Status:          stripeprovider.CheckoutOpen, PaymentStatus: stripeprovider.PaymentUnpaid,
		AmountJPY: req.AmountJPY, Currency: stripeprovider.CurrencyJPY,
		ExpiresAt: time.Now().UTC().Add(time.Hour), URL: "https://checkout.example.invalid/" + id,
		Metadata: map[string]string{"tenant_id": req.TenantID, "store_id": req.StoreID, "order_id": req.OrderID, "attempt_id": req.AttemptID},
	}
	if p.hasNextOutcome {
		r.Status, r.PaymentStatus = p.nextStatus, p.nextPayment
		if r.Status != stripeprovider.CheckoutOpen {
			r.URL = ""
		}
		p.hasNextOutcome = false
	}
	p.keys[key], p.digests[key], p.sessions[id] = id, digest, r
	if p.nextCreateHook != nil {
		hook := p.nextCreateHook
		p.nextCreateHook = nil
		hook(clone(r))
	}
	if p.loseNext {
		p.loseNext = false
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrOutcomeUnknown
	}
	return clone(r), nil
}

func (p *Provider) RetrieveCheckout(ctx context.Context, account stripeprovider.AccountContext, id string) (stripeprovider.CheckoutResult, error) {
	p.mu.Lock()
	p.retrieves++
	p.mu.Unlock()
	if ctx.Err() != nil {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.sessions[id]
	if !ok || account.Mode != stripeprovider.ModeConnectedDirectFixture || r.AccountID != account.StripeAccountID {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrNotFound
	}
	return clone(r), nil
}

func (p *Provider) ExpireCheckout(ctx context.Context, account stripeprovider.AccountContext, id, key string) (stripeprovider.CheckoutResult, error) {
	p.mu.Lock()
	p.expires++
	p.mu.Unlock()
	if ctx.Err() != nil || key == "" {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrOutcomeUnknown
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.sessions[id]
	if !ok || account.Mode != stripeprovider.ModeConnectedDirectFixture || r.AccountID != account.StripeAccountID {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrNotFound
	}
	if p.hasNextExpireOutcome {
		r.Status, r.PaymentStatus = p.nextExpireStatus, p.nextExpirePayment
		if r.Status != stripeprovider.CheckoutOpen {
			r.URL = ""
		}
		p.sessions[id] = r
		returnUnknown := p.nextExpireUnknown
		p.hasNextExpireOutcome = false
		if returnUnknown {
			return stripeprovider.CheckoutResult{}, stripeprovider.ErrOutcomeUnknown
		}
		return clone(r), nil
	}
	if r.Status != stripeprovider.CheckoutOpen || r.PaymentStatus != stripeprovider.PaymentUnpaid {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrOutcomeUnknown
	}
	r.Status, r.URL = stripeprovider.CheckoutExpired, ""
	p.sessions[id] = r
	if p.nextExpireHook != nil {
		hook := p.nextExpireHook
		p.nextExpireHook = nil
		hook(clone(r))
	}
	return clone(r), nil
}

func (p *Provider) RetrievePaymentIntent(ctx context.Context, account stripeprovider.AccountContext, id string) (stripeprovider.PaymentIntentResult, error) {
	if ctx.Err() != nil {
		return stripeprovider.PaymentIntentResult{}, stripeprovider.ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.sessions {
		if r.PaymentIntentID == id && r.AccountID == account.StripeAccountID && account.Mode == stripeprovider.ModeConnectedDirectFixture {
			status := "requires_payment_method"
			if r.PaymentStatus == stripeprovider.PaymentPaid {
				status = "succeeded"
			} else if r.Status == stripeprovider.CheckoutComplete {
				status = "processing"
			}
			return stripeprovider.PaymentIntentResult{AccountID: r.AccountID, ID: id, Status: status, AmountJPY: r.AmountJPY, Currency: r.Currency, Metadata: clone(r).Metadata}, nil
		}
	}
	return stripeprovider.PaymentIntentResult{}, stripeprovider.ErrNotFound
}

// LoseNextCreateResponse simulates a successful provider write with a lost
// response. Retrying the same immutable request returns the existing session.
func (p *Provider) LoseNextCreateResponse() { p.mu.Lock(); defer p.mu.Unlock(); p.loseNext = true }

// SetNextCreateOutcome configures one synthetic response before the next
// create. It lets package integration tests model a payment completing while
// the original create response is lost; it is not exposed to runtime callers.
func (p *Provider) SetNextCreateOutcome(status stripeprovider.CheckoutStatus, payment stripeprovider.PaymentStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextStatus, p.nextPayment, p.hasNextOutcome = status, payment, true
}

// SetNextCreateHook runs once after a synthetic Checkout has been stored but
// before CreateCheckout returns. Integration tests use it to place a durable
// store/order hold in the exact create/persist race window.
func (p *Provider) SetNextCreateHook(hook func(stripeprovider.CheckoutResult)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextCreateHook = hook
}

// SetNextExpireHook pauses a successful synthetic expiration before returning
// its observation, allowing a webhook to commit while that response is in flight.
func (p *Provider) SetNextExpireHook(hook func(stripeprovider.CheckoutResult)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextExpireHook = hook
}

// SetNextExpireOutcome models a concurrent payment completion while a worker
// attempts to expire an open Checkout. The optional unknown result requires
// the caller to retrieve the existing object before concluding.
func (p *Provider) SetNextExpireOutcome(status stripeprovider.CheckoutStatus, payment stripeprovider.PaymentStatus, returnUnknown bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextExpireStatus, p.nextExpirePayment, p.nextExpireUnknown, p.hasNextExpireOutcome = status, payment, returnUnknown, true
}

func (p *Provider) Counts() (requests, sessions int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.creates, len(p.sessions)
}

// CheckoutOperationCounts is test-only observability for proving bounded
// retries stop before making another provider request after an attempt cutoff.
func (p *Provider) CheckoutOperationCounts() (retrieves, expires int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.retrieves, p.expires
}

// SetOutcome is a Go-test driver, never an HTTP route or customer action.
func (p *Provider) SetOutcome(id string, status stripeprovider.CheckoutStatus, payment stripeprovider.PaymentStatus) (stripeprovider.CheckoutResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.sessions[id]
	if !ok {
		return stripeprovider.CheckoutResult{}, stripeprovider.ErrNotFound
	}
	r.Status, r.PaymentStatus = status, payment
	if status != stripeprovider.CheckoutOpen {
		r.URL = ""
	}
	p.sessions[id] = r
	return clone(r), nil
}

// SignedCheckoutEvent produces a synthetic, exactly signed raw webhook body
// for HTTP tests of the real verifier. No real endpoint secret is needed.
func SignedCheckoutEvent(r stripeprovider.CheckoutResult, eventID, eventType, fixtureSecret string, at time.Time) ([]byte, string, error) {
	body, err := json.Marshal(map[string]any{
		"id": eventID, "object": "event", "type": eventType, "account": r.AccountID,
		"api_version": stripeprovider.APIVersion, "created": at.Unix(), "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": r.SessionID, "object": "checkout.session", "mode": "payment", "status": r.Status,
			"payment_status": r.PaymentStatus, "amount_total": r.AmountJPY, "currency": r.Currency,
			"payment_intent": r.PaymentIntentID, "expires_at": r.ExpiresAt.Unix(), "livemode": false,
			"metadata": r.Metadata,
		}},
	})
	if err != nil {
		return nil, "", err
	}
	mac := hmac.New(sha256.New, []byte(fixtureSecret))
	fmt.Fprintf(mac, "%d.", at.Unix())
	_, _ = mac.Write(body)
	return body, fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(mac.Sum(nil))), nil
}

func clone(r stripeprovider.CheckoutResult) stripeprovider.CheckoutResult {
	metadata := make(map[string]string, len(r.Metadata))
	for k, v := range r.Metadata {
		metadata[k] = v
	}
	r.Metadata = metadata
	return r
}

func cloneRefund(r stripeprovider.RefundResult) stripeprovider.RefundResult {
	metadata := make(map[string]string, len(r.Metadata))
	for k, v := range r.Metadata {
		metadata[k] = v
	}
	r.Metadata = metadata
	return r
}
