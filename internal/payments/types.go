// Package payments coordinates durable Stripe attempts with ordering and
// operations. Provider calls are always made outside database transactions.
package payments

import (
	"context"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

type AttemptState string

const (
	StateCreatePending  AttemptState = "CREATE_PENDING"
	StateOpen           AttemptState = "OPEN"
	StateProcessing     AttemptState = "PROCESSING"
	StateUnknown        AttemptState = "UNKNOWN"
	StatePaid           AttemptState = "PAID"
	StateExpired        AttemptState = "EXPIRED"
	StateFailed         AttemptState = "FAILED"
	StateReviewRequired AttemptState = "REVIEW_REQUIRED"
)

// Provider is deliberately narrow and mirrors the adapter; it is injectable
// so database workflows can be tested without Stripe credentials or network.
type Provider interface {
	CreateCheckout(context.Context, stripeprovider.CreateCheckoutRequest) (stripeprovider.CheckoutResult, error)
	RetrieveCheckout(context.Context, stripeprovider.AccountContext, string) (stripeprovider.CheckoutResult, error)
	RetrievePaymentIntent(context.Context, stripeprovider.AccountContext, string) (stripeprovider.PaymentIntentResult, error)
	ExpireCheckout(context.Context, stripeprovider.AccountContext, string, string) (stripeprovider.CheckoutResult, error)
}

type ResolvedProvider struct {
	Provider Provider
	Account  stripeprovider.AccountContext
	// ProfileFingerprint identifies immutable request behavior (API version and
	// trusted redirect configuration), excluding rotatable secret material.
	ProfileFingerprint [32]byte
}

type ProviderResolver interface {
	// Resolve must only use already-prepared in-memory configuration and must
	// never perform secret-manager/provider/network I/O; order commits call it
	// while their database transaction is open.
	Resolve(context.Context, string) (ResolvedProvider, error)
}

type Config struct {
	CreateRetryWindow time.Duration // strictly positive and less than 24 hours
	ContinuationTTL   time.Duration
	WorkLeaseTTL      time.Duration
	Now               func() time.Time
	Random            io.Reader
}

type Service struct {
	db       *pgxpool.Pool
	ops      *operations.Service
	resolver ProviderResolver
	cfg      Config
}

type MerchantConfig struct {
	Enabled            bool   `json:"enabled"`
	Mode               string `json:"mode"`
	StripeAccountID    string `json:"stripe_account_id"`
	CredentialRef      string `json:"credential_ref"`
	WebhookEndpointRef string `json:"webhook_endpoint_ref"`
	APIVersion         string `json:"api_version"`
	Version            int64  `json:"version"`
}

type MerchantConfigInput struct {
	Enabled            bool
	Mode               string
	StripeAccountID    string
	CredentialRef      string
	WebhookEndpointRef string
	APIVersion         string
}

type Status struct {
	SafeErrorCode         string       `json:"safe_error_code,omitempty"`
	OrderID               string       `json:"order_id"`
	State                 AttemptState `json:"state"`
	CheckoutStatus        string       `json:"checkout_status,omitempty"`
	PaymentStatus         string       `json:"payment_status,omitempty"`
	ProviderExpiresAt     *time.Time   `json:"provider_expires_at,omitempty"`
	LastReconciledAt      *time.Time   `json:"last_reconciled_at,omitempty"`
	PaymentReviewRequired bool         `json:"payment_review_required"`
}

// PrivateCheckout contains the only provider URL projection. It must be
// returned only by an explicit proof-authorized resume operation.
type PrivateCheckout struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// New validates bounded retry/lease configuration. A nil or unconfigured
// resolver is permitted: STRIPE commits still fail closed at merchant lookup.
func New(db *pgxpool.Pool, ops *operations.Service, resolver ProviderResolver, cfg Config) (*Service, error) {
	return newService(db, ops, resolver, cfg)
}

func (s *Service) InitOrderTx(ctx context.Context, tx pgx.Tx, order ordering.CommittedOrder) error {
	return s.initOrderTx(ctx, tx, order)
}

func (s *Service) SetMerchantConfig(ctx context.Context, scope operations.StoreScope, expectedVersion int64, actor string, input MerchantConfigInput) (MerchantConfig, error) {
	return s.setMerchantConfig(ctx, scope, expectedVersion, actor, input)
}

func (s *Service) ReadMerchantConfig(ctx context.Context, scope operations.StoreScope) (MerchantConfig, error) {
	return s.readMerchantConfig(ctx, scope)
}

func (s *Service) ReadSafeStatus(ctx context.Context, scope operations.TableScope, sessionID, orderID string) (Status, error) {
	return s.readSafeStatus(ctx, scope, sessionID, orderID)
}

func (s *Service) CanResume(ctx context.Context, sessionID, orderID string, proofHash [32]byte) (bool, error) {
	return s.canResume(ctx, sessionID, orderID, proofHash)
}

func (s *Service) Resume(ctx context.Context, sessionID, orderID string, proofHash [32]byte) (PrivateCheckout, error) {
	return s.resume(ctx, sessionID, orderID, proofHash)
}

func (s *Service) EnqueueVerifiedWebhook(ctx context.Context, event stripeprovider.VerifiedEvent) (bool, error) {
	return s.enqueueVerifiedWebhook(ctx, event)
}

func (s *Service) ProcessNext(ctx context.Context) (bool, error) { return s.processNext(ctx) }

func (s *Service) Reconcile(ctx context.Context, scope operations.TableScope, sessionID, orderID, actor string) (Status, error) {
	return s.reconcile(ctx, scope, sessionID, orderID, actor)
}

var _ ordering.OrderCommitPolicy = (*Service)(nil)
