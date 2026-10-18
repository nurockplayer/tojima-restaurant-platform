package payments

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

func validStoreScope(s operations.StoreScope) bool {
	return strings.TrimSpace(s.TenantID) != "" && len(s.TenantID) <= 200 && strings.TrimSpace(s.StoreID) != "" && len(s.StoreID) <= 200
}

var (
	accountPattern = regexp.MustCompile(`^acct_[A-Za-z0-9]+$`)
	refPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)
)

func accountIDValid(s string) bool { return accountPattern.MatchString(s) }
func validRef(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && refPattern.MatchString(s)
}

func (s *Service) setMerchantConfig(ctx context.Context, scope operations.StoreScope, expected int64, actor string, input MerchantConfigInput) (MerchantConfig, error) {
	if !validStoreScope(scope) || strings.TrimSpace(actor) == "" || len(actor) > 200 || expected < 0 || input.Mode != stripeprovider.ModeConnectedDirectFixture ||
		!accountIDValid(input.StripeAccountID) || !validRef(input.CredentialRef, 200) || !validRef(input.WebhookEndpointRef, 200) || input.APIVersion != stripeprovider.APIVersion {
		return MerchantConfig{}, ErrInvalid
	}
	if input.Enabled {
		if _, err := s.resolve(ctx, input.CredentialRef, input.StripeAccountID, input.Mode); err != nil {
			return MerchantConfig{}, ErrUnavailable
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return MerchantConfig{}, err
	}
	defer tx.Rollback(ctx)
	var storeOK bool
	err = tx.QueryRow(ctx, `SELECT st.enabled AND st.operating AND t.enabled FROM stores st JOIN tenants t USING(tenant_id) WHERE st.tenant_id=$1 AND st.store_id=$2 FOR UPDATE OF st`, scope.TenantID, scope.StoreID).Scan(&storeOK)
	if errors.Is(err, pgx.ErrNoRows) {
		return MerchantConfig{}, ErrNotFound
	}
	if err != nil {
		return MerchantConfig{}, err
	}
	if !storeOK {
		return MerchantConfig{}, ErrDenied
	}
	var before MerchantConfig
	err = tx.QueryRow(ctx, `SELECT enabled,provider_mode,stripe_account_id,credential_ref,webhook_endpoint_ref,api_version,version FROM store_stripe_merchants WHERE tenant_id=$1 AND store_id=$2 FOR UPDATE`, scope.TenantID, scope.StoreID).
		Scan(&before.Enabled, &before.Mode, &before.StripeAccountID, &before.CredentialRef, &before.WebhookEndpointRef, &before.APIVersion, &before.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		before.Version = 0
	} else if err != nil {
		return MerchantConfig{}, err
	}
	if before.Version != expected {
		return MerchantConfig{}, ErrConflict
	}
	next := MerchantConfig{Enabled: input.Enabled, Mode: input.Mode, StripeAccountID: input.StripeAccountID, CredentialRef: input.CredentialRef, WebhookEndpointRef: input.WebhookEndpointRef, APIVersion: input.APIVersion, Version: expected + 1}
	_, err = tx.Exec(ctx, `INSERT INTO store_stripe_merchants(tenant_id,store_id,enabled,provider_mode,stripe_account_id,credential_ref,webhook_endpoint_ref,api_version,version,updated_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(tenant_id,store_id) DO UPDATE SET enabled=EXCLUDED.enabled,provider_mode=EXCLUDED.provider_mode,stripe_account_id=EXCLUDED.stripe_account_id,credential_ref=EXCLUDED.credential_ref,webhook_endpoint_ref=EXCLUDED.webhook_endpoint_ref,api_version=EXCLUDED.api_version,version=EXCLUDED.version,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp()`, scope.TenantID, scope.StoreID, next.Enabled, next.Mode, next.StripeAccountID, next.CredentialRef, next.WebhookEndpointRef, next.APIVersion, next.Version, actor)
	if err != nil {
		return MerchantConfig{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO store_stripe_merchants_audit(tenant_id,store_id,actor_id,before_enabled,after_enabled,before_account_id,after_account_id,before_mode,after_mode,before_credential_ref,after_credential_ref,before_endpoint_ref,after_endpoint_ref,before_version,after_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, scope.TenantID, scope.StoreID, actor, nullableBoolLocal(before.Version > 0, before.Enabled), next.Enabled, nullableString(before.Version > 0, before.StripeAccountID), next.StripeAccountID, nullableString(before.Version > 0, before.Mode), next.Mode, nullableString(before.Version > 0, before.CredentialRef), next.CredentialRef, nullableString(before.Version > 0, before.WebhookEndpointRef), next.WebhookEndpointRef, before.Version, next.Version)
	if err != nil {
		return MerchantConfig{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return MerchantConfig{}, err
	}
	return next, nil
}

func nullableString(ok bool, v string) any {
	if !ok {
		return nil
	}
	return v
}
func nullableBoolLocal(ok, v bool) any {
	if !ok {
		return nil
	}
	return v
}

func (s *Service) readMerchantConfig(ctx context.Context, scope operations.StoreScope) (MerchantConfig, error) {
	if !validStoreScope(scope) {
		return MerchantConfig{}, ErrInvalid
	}
	var out MerchantConfig
	err := s.db.QueryRow(ctx, `SELECT m.enabled,m.provider_mode,m.stripe_account_id,m.credential_ref,m.webhook_endpoint_ref,m.api_version,m.version FROM store_stripe_merchants m
		JOIN stores st USING(tenant_id,store_id) JOIN tenants t USING(tenant_id) WHERE m.tenant_id=$1 AND m.store_id=$2 AND st.enabled AND t.enabled`, scope.TenantID, scope.StoreID).
		Scan(&out.Enabled, &out.Mode, &out.StripeAccountID, &out.CredentialRef, &out.WebhookEndpointRef, &out.APIVersion, &out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stores st JOIN tenants t USING(tenant_id) WHERE st.tenant_id=$1 AND st.store_id=$2 AND st.enabled AND t.enabled)`, scope.TenantID, scope.StoreID).Scan(&exists); e != nil {
			return MerchantConfig{}, e
		}
		if !exists {
			return MerchantConfig{}, ErrNotFound
		}
		return MerchantConfig{Mode: stripeprovider.ModeUnconfigured}, nil
	}
	return out, err
}

func (s *Service) readSafeStatus(ctx context.Context, scope operations.TableScope, sessionID, orderID string) (Status, error) {
	if !validStoreScope(operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID}) || strings.TrimSpace(scope.TableID) == "" || sessionID == "" || orderID == "" {
		return Status{}, ErrInvalid
	}
	var out Status
	var state string
	err := s.db.QueryRow(ctx, `SELECT a.order_id,a.state,coalesce(a.checkout_status,''),coalesce(a.provider_payment_status,''),a.provider_expires_at,a.last_reconciled_at,a.payment_review_required OR st.payment_review_required
		FROM stripe_payment_attempts a JOIN operation_order_state st USING(order_id) JOIN table_sessions ts ON ts.session_id=a.session_id
		JOIN stores so ON so.tenant_id=a.tenant_id AND so.store_id=a.store_id
		JOIN tenants t ON t.tenant_id=a.tenant_id
		JOIN dining_tables dt ON dt.tenant_id=a.tenant_id AND dt.store_id=a.store_id AND dt.table_id=a.table_id
		WHERE a.order_id=$1 AND a.session_id=$2 AND a.tenant_id=$3 AND a.store_id=$4 AND a.table_id=$5
		AND so.enabled AND t.enabled`, orderID, sessionID, scope.TenantID, scope.StoreID, scope.TableID).
		Scan(&out.OrderID, &state, &out.CheckoutStatus, &out.PaymentStatus, &out.ProviderExpiresAt, &out.LastReconciledAt, &out.PaymentReviewRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, err
	}
	out.State = AttemptState(state)
	if out.PaymentReviewRequired {
		out.State = StateReviewRequired
	}
	return out, nil
}

func (s *Service) canResume(ctx context.Context, sessionID, orderID string, proofHash [32]byte) (bool, error) {
	if sessionID == "" || orderID == "" || allZero(proofHash[:]) {
		return false, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT a.session_id=$1 AND a.checkout_proof_hash=$3 AND a.continuation_expires_at>clock_timestamp() AND a.continuation_revoked_at IS NULL
		AND a.state='OPEN' AND a.checkout_session_id IS NOT NULL AND a.checkout_status='open' AND a.provider_payment_status='unpaid'
		AND a.company_hold_expire_requested_at IS NULL AND NOT a.payment_review_required AND ts.status='OPEN' AND ts.expires_at>clock_timestamp() AND st.enabled AND st.operating AND t.enabled AND dt.enabled
		AND NOT coalesce(os.operational_hold,true) AND NOT coalesce(os.payment_review_required,true)
		FROM stripe_payment_attempts a JOIN table_sessions ts ON ts.session_id=a.session_id
		JOIN stores st ON st.tenant_id=a.tenant_id AND st.store_id=a.store_id
		JOIN tenants t ON t.tenant_id=a.tenant_id
		JOIN dining_tables dt ON dt.tenant_id=a.tenant_id AND dt.store_id=a.store_id AND dt.table_id=a.table_id LEFT JOIN operation_order_state os USING(order_id)
		WHERE a.order_id=$2`, sessionID, orderID, proofHash[:]).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

type resumeRow struct {
	tenant, store, table, credential, mode, account, checkout, attempt string
	amount                                                             int64
	expires                                                            time.Time
	proof                                                              []byte
}

func (s *Service) resume(ctx context.Context, sessionID, orderID string, proofHash [32]byte) (PrivateCheckout, error) {
	ok, err := s.canResume(ctx, sessionID, orderID, proofHash)
	if err != nil {
		return PrivateCheckout{}, err
	}
	if !ok {
		return PrivateCheckout{}, ErrProofDenied
	}
	var r resumeRow
	err = s.db.QueryRow(ctx, `SELECT a.tenant_id,a.store_id,a.table_id,a.credential_ref,a.provider_mode,a.stripe_account_id,a.checkout_session_id,a.attempt_id,a.amount_jpy,a.continuation_expires_at,a.checkout_proof_hash
		FROM stripe_payment_attempts a WHERE a.order_id=$1 AND a.session_id=$2`, orderID, sessionID).
		Scan(&r.tenant, &r.store, &r.table, &r.credential, &r.mode, &r.account, &r.checkout, &r.attempt, &r.amount, &r.expires, &r.proof)
	if err != nil {
		return PrivateCheckout{}, ErrProofDenied
	}
	resolved, err := s.resolve(ctx, r.credential, r.account, r.mode)
	if err != nil {
		return PrivateCheckout{}, ErrProofDenied
	}
	result, err := resolved.Provider.RetrieveCheckout(ctx, resolved.Account, r.checkout)
	if err != nil || !validResumeResult(result, r, orderID, s.cfg.Now()) {
		return PrivateCheckout{}, ErrProofDenied
	}
	ok, err = s.canResume(ctx, sessionID, orderID, proofHash)
	if err != nil {
		return PrivateCheckout{}, err
	}
	if !ok {
		return PrivateCheckout{}, ErrProofDenied
	}
	return PrivateCheckout{URL: result.URL, ExpiresAt: result.ExpiresAt}, nil
}

func validResumeResult(x stripeprovider.CheckoutResult, r resumeRow, orderID string, now time.Time) bool {
	return x.AccountID == r.account && x.SessionID == r.checkout && x.Status == stripeprovider.CheckoutOpen && x.PaymentStatus == stripeprovider.PaymentUnpaid && x.AmountJPY == r.amount && x.Currency == stripeprovider.CurrencyJPY && !x.LiveMode && x.ExpiresAt.After(now) && x.URL != "" && x.Metadata["tenant_id"] == r.tenant && x.Metadata["store_id"] == r.store && x.Metadata["order_id"] == orderID && x.Metadata["attempt_id"] == r.attempt
}
