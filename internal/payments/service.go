package payments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

var (
	ErrInvalid        = errors.New("payment request is invalid")
	ErrDenied         = errors.New("payment access denied")
	ErrNotFound       = errors.New("payment resource not found")
	ErrConflict       = errors.New("payment version conflict")
	ErrUnavailable    = errors.New("payment method is unavailable")
	ErrProofDenied    = errors.New("checkout continuation is unavailable")
	ErrOutcomeUnknown = errors.New("payment provider outcome is unknown")
)

func newService(db *pgxpool.Pool, ops *operations.Service, resolver ProviderResolver, cfg Config) (*Service, error) {
	if db == nil || ops == nil || cfg.CreateRetryWindow <= 0 || cfg.CreateRetryWindow >= 24*time.Hour || cfg.ContinuationTTL <= 0 || cfg.WorkLeaseTTL <= 0 {
		return nil, ErrInvalid
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{db: db, ops: ops, resolver: resolver, cfg: cfg}, nil
}

func (s *Service) CheckClose(ctx context.Context, tx pgx.Tx, session tablesession.Session) error {
	if err := s.ops.CheckClose(ctx, tx, session); err != nil {
		return err
	}
	var unsafe int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM table_orders o JOIN operation_order_state os USING(order_id)
		LEFT JOIN stripe_payment_attempts a USING(order_id)
		WHERE o.session_id=$1 AND os.payment_method='STRIPE'
		AND (a.order_id IS NULL OR a.state<>'PAID' OR a.payment_review_required OR os.payment_review_required)`, session.ID).Scan(&unsafe)
	if err != nil {
		return err
	}
	if unsafe > 0 {
		return tablesession.ErrCloseBlocked
	}
	return nil
}

func (s *Service) initOrderTx(ctx context.Context, tx pgx.Tx, order ordering.CommittedOrder) error {
	if tx == nil || s == nil || order.PaymentMethod != string(ordering.Stripe) {
		if s == nil || tx == nil {
			return ErrInvalid
		}
		return s.ops.InitOrderTx(ctx, tx, order)
	}
	if allZero(order.CheckoutProofHash[:]) {
		return ordering.ErrPaymentUnavailable
	}
	if err := s.ops.InitOrderTx(ctx, tx, order); err != nil {
		return err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if !order.CheckoutProofExpiresAt.After(now) {
		return ordering.ErrPaymentUnavailable
	}
	var enabled, storeEnabled, operating, tenantEnabled bool
	var mode, accountID, credentialRef, endpointRef, apiVersion string
	var merchantVersion int64
	err := tx.QueryRow(ctx, `SELECT m.enabled,m.provider_mode,m.stripe_account_id,m.credential_ref,m.webhook_endpoint_ref,m.api_version,m.version,st.enabled,st.operating,t.enabled
		FROM store_stripe_merchants m JOIN stores st USING(tenant_id,store_id) JOIN tenants t USING(tenant_id)
		WHERE m.tenant_id=$1 AND m.store_id=$2 FOR SHARE OF m`, order.Scope.TenantID, order.Scope.StoreID).
		Scan(&enabled, &mode, &accountID, &credentialRef, &endpointRef, &apiVersion, &merchantVersion, &storeEnabled, &operating, &tenantEnabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!enabled || !storeEnabled || !operating || !tenantEnabled)) {
		return ordering.ErrPaymentUnavailable
	}
	if err != nil {
		return err
	}
	resolved, err := s.resolve(ctx, credentialRef, accountID, mode)
	if err != nil || apiVersion != stripeprovider.APIVersion {
		return ordering.ErrPaymentUnavailable
	}
	attemptID, err := s.randomID()
	if err != nil {
		return err
	}
	requestDigest, err := digestCreate(order, resolved, apiVersion)
	if err != nil {
		return err
	}
	var taxJSON []byte
	taxJSON, err = json.Marshal(order.Tax)
	if err != nil {
		return err
	}
	continuationExpires := now.Add(s.cfg.ContinuationTTL)
	if order.CheckoutProofExpiresAt.Before(continuationExpires) {
		continuationExpires = order.CheckoutProofExpiresAt
	}
	retryNotAfter := now.Add(s.cfg.CreateRetryWindow)
	idempotencyKey := "p80:" + attemptID
	_, err = tx.Exec(ctx, `INSERT INTO stripe_payment_attempts(order_id,attempt_id,session_id,tenant_id,store_id,table_id,generation,amount_jpy,currency,tax_snapshot,provider_mode,stripe_account_id,credential_ref,merchant_version,request_profile_fingerprint,api_version,idempotency_key,request_digest,checkout_proof_hash,checkout_proof_expires_at,continuation_expires_at,state,retry_not_after)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,'jpy',$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,'CREATE_PENDING',$21)`,
		order.ID, attemptID, order.SessionID, order.Scope.TenantID, order.Scope.StoreID, order.Scope.TableID, order.Generation,
		order.TotalGrossJPY, taxJSON, mode, accountID, credentialRef, merchantVersion, resolved.ProfileFingerprint[:], apiVersion,
		idempotencyKey, requestDigest[:], order.CheckoutProofHash[:], order.CheckoutProofExpiresAt, continuationExpires, retryNotAfter)
	if err != nil {
		return err
	}
	if err = s.appendAttemptEvent(ctx, tx, order.ID, "ORDER_COMMITTED", "", string(StateCreatePending), "system"); err != nil {
		return err
	}
	return s.enqueueWork(ctx, tx, "CREATE", order.ID, nil, "create:"+attemptID)
}

func (s *Service) resolve(ctx context.Context, credentialRef, accountID, mode string) (ResolvedProvider, error) {
	if s == nil || s.resolver == nil || mode != stripeprovider.ModeConnectedDirectFixture || strings.TrimSpace(credentialRef) == "" {
		return ResolvedProvider{}, stripeprovider.ErrUnconfigured
	}
	r, err := s.resolver.Resolve(ctx, credentialRef)
	if err != nil || r.Provider == nil || r.Account.Mode != mode || r.Account.StripeAccountID != accountID || allZero(r.ProfileFingerprint[:]) {
		return ResolvedProvider{}, stripeprovider.ErrUnconfigured
	}
	return r, nil
}

func (s *Service) randomID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(s.cfg.Random, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func digestCreate(order ordering.CommittedOrder, provider ResolvedProvider, apiVersion string) ([32]byte, error) {
	value := struct {
		OrderID, TenantID, StoreID, AccountID, APIVersion string
		AmountJPY                                         int64
		Profile                                           [32]byte
	}{order.ID, order.Scope.TenantID, order.Scope.StoreID, provider.Account.StripeAccountID, apiVersion, order.TotalGrossJPY, provider.ProfileFingerprint}
	raw, err := json.Marshal(value)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func allZero(v []byte) bool {
	var n byte
	for _, b := range v {
		n |= b
	}
	return n == 0
}

func (s *Service) appendAttemptEvent(ctx context.Context, tx pgx.Tx, orderID, key, before, after, actor string) error {
	_, err := tx.Exec(ctx, `INSERT INTO stripe_payment_attempt_events(order_id,event_key,actor_id,before_state,after_state) VALUES($1,$2,$3,NULLIF($4,''),$5) ON CONFLICT(order_id,event_key) DO NOTHING`, orderID, key, actor, before, after)
	return err
}

func (s *Service) enqueueWork(ctx context.Context, tx pgx.Tx, kind, orderID string, webhookID *int64, dedupe string) error {
	workID, err := s.randomID()
	if err != nil {
		return err
	}
	var order any
	if orderID != "" {
		order = orderID
	}
	_, err = tx.Exec(ctx, `INSERT INTO stripe_payment_work(work_id,dedupe_key,work_type,order_id,webhook_row_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(dedupe_key) DO NOTHING`, workID, dedupe, kind, order, webhookID)
	return err
}
