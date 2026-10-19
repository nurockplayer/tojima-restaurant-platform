package payments

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

var ErrVoidNotEligible = errors.New("Stripe checkout cannot be voided as unpaid")

type VoidRequest struct {
	Scope                                             operations.TableScope
	SessionID, OrderID, Actor, IdempotencyKey, Reason string
	ExpectedVersion                                   int64
}

type voidAttempt struct {
	reconcileRow
	api      string
	profile  []byte
	eligible bool
	version  int64
}

type voidReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readVoidAttempt(ctx context.Context, q voidReader, r VoidRequest, locked bool) (voidAttempt, error) {
	var a voidAttempt
	query := `SELECT os.order_id,os.session_id,os.tenant_id,os.store_id,os.table_id,coalesce(a.attempt_id,''),
		coalesce(a.credential_ref,''),coalesce(a.provider_mode,''),coalesce(a.stripe_account_id,''),coalesce(a.checkout_session_id,''),coalesce(a.payment_intent_id,''),coalesce(a.amount_jpy,0),coalesce(a.updated_at,os.updated_at),
		coalesce(a.api_version,''),a.request_profile_fingerprint,os.version,
		os.payment_method='STRIPE' AND os.payment_status IN ('PENDING','UNKNOWN') AND os.fulfillment_status='WAITING'
		AND NOT os.payment_review_required AND NOT os.operational_hold AND os.review_decision='UNRESOLVED'
		AND a.order_id IS NOT NULL AND NOT a.payment_review_required
		AND a.state NOT IN ('PAID','REVIEW_REQUIRED','PROCESSING') AND coalesce(a.checkout_session_id,'')<>''
		AND NOT EXISTS(SELECT 1 FROM operation_kds_cards k WHERE k.order_id=os.order_id)
		FROM operation_order_state os LEFT JOIN stripe_payment_attempts a USING(order_id)
		WHERE os.order_id=$1 AND os.session_id=$2 AND os.tenant_id=$3 AND os.store_id=$4 AND os.table_id=$5`
	if locked {
		query += ` FOR UPDATE OF os`
	}
	err := q.QueryRow(ctx, query, r.OrderID, r.SessionID, r.Scope.TenantID, r.Scope.StoreID, r.Scope.TableID).
		Scan(&a.order, &a.session, &a.tenant, &a.store, &a.table, &a.attempt, &a.credential, &a.mode, &a.account, &a.checkout, &a.pi, &a.amount, &a.updatedAt, &a.api, &a.profile, &a.version, &a.eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Service) VoidUnpaidCheckout(ctx context.Context, r VoidRequest) (operations.OrderState, error) {
	r.Reason = strings.TrimSpace(r.Reason)
	if !validStoreScope(operations.StoreScope{TenantID: r.Scope.TenantID, StoreID: r.Scope.StoreID}) || strings.TrimSpace(r.Scope.TableID) == "" || !validRef(r.SessionID, 255) || !validRef(r.OrderID, 255) || strings.TrimSpace(r.Actor) == "" || len(r.Actor) > 200 || strings.TrimSpace(r.IdempotencyKey) == "" || len(r.IdempotencyKey) > 128 || r.ExpectedVersion < 1 || utf8.RuneCountInString(r.Reason) < 1 || utf8.RuneCountInString(r.Reason) > 500 {
		return operations.OrderState{}, ErrInvalid
	}
	req := operations.StripeVoidRequest{Scope: r.Scope, SessionID: r.SessionID, OrderID: r.OrderID, Actor: r.Actor, ExpectedVersion: r.ExpectedVersion, IdempotencyKey: r.IdempotencyKey, Reason: r.Reason}
	if prior, ok, err := s.ops.ReplayStripeVoid(ctx, req); ok || err != nil {
		return prior, err
	}
	a, err := readVoidAttempt(ctx, s.db, r, false)
	if err != nil {
		return operations.OrderState{}, err
	}
	if !a.eligible {
		return operations.OrderState{}, ErrVoidNotEligible
	}
	if a.version != r.ExpectedVersion {
		return operations.OrderState{}, operations.ErrConflict
	}
	resolved, err := s.resolve(ctx, a.credential, a.account, a.mode)
	if err != nil || a.api != stripeprovider.APIVersion || !bytesEqual(a.profile, resolved.ProfileFingerprint[:]) {
		return operations.OrderState{}, ErrUnavailable
	}
	x, err := resolved.Provider.RetrieveCheckout(ctx, resolved.Account, a.checkout)
	if err != nil {
		return operations.OrderState{}, ErrUnavailable
	}
	valid := func(x stripeprovider.CheckoutResult) bool {
		return validReconcileCheckout(x, a.reconcileRow) && !x.ExpiresAt.IsZero() && (a.pi == "" || x.PaymentIntentID == a.pi)
	}
	if !valid(x) {
		return operations.OrderState{}, ErrVoidNotEligible
	}
	persistPaid := func(pi *stripeprovider.PaymentIntentResult) (operations.OrderState, error) {
		if _, err := s.persistReconciliation(ctx, r.Scope, a.reconcileRow, &x, pi); err != nil {
			return operations.OrderState{}, err
		}
		return operations.OrderState{}, ErrVoidNotEligible
	}
	if x.PaymentStatus == stripeprovider.PaymentPaid {
		return persistPaid(nil)
	}
	expireUnconfirmed := false
	if x.Status == stripeprovider.CheckoutOpen && x.PaymentStatus == stripeprovider.PaymentUnpaid {
		x, err = resolved.Provider.ExpireCheckout(ctx, resolved.Account, a.checkout, "p80-expire:"+a.attempt)
		if err != nil {
			expireUnconfirmed = true
			x, err = resolved.Provider.RetrieveCheckout(ctx, resolved.Account, a.checkout)
		}
		if err != nil {
			return operations.OrderState{}, ErrUnavailable
		}
		if !valid(x) {
			return operations.OrderState{}, ErrVoidNotEligible
		}
		if x.PaymentStatus == stripeprovider.PaymentPaid {
			return persistPaid(nil)
		}
	}
	var pi *stripeprovider.PaymentIntentResult
	if x.PaymentIntentID != "" {
		intent, intentErr := resolved.Provider.RetrievePaymentIntent(ctx, resolved.Account, x.PaymentIntentID)
		if intentErr != nil {
			return operations.OrderState{}, ErrUnavailable
		}
		if !validReconcileIntent(intent, a.reconcileRow, x.PaymentIntentID) {
			return operations.OrderState{}, ErrVoidNotEligible
		}
		pi = &intent
		if intent.Status == "succeeded" {
			return persistPaid(pi)
		}
		if intent.Status == "processing" || intent.Status == "requires_capture" {
			return operations.OrderState{}, ErrVoidNotEligible
		}
	}
	if expireUnconfirmed && x.Status == stripeprovider.CheckoutOpen {
		return operations.OrderState{}, ErrUnavailable
	}
	if x.Status != stripeprovider.CheckoutExpired || x.PaymentStatus != stripeprovider.PaymentUnpaid {
		return operations.OrderState{}, ErrVoidNotEligible
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return operations.OrderState{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentSessionAny(ctx, tx, a.order); err != nil {
		return operations.OrderState{}, err
	}
	var checkout, attempt, current string
	if err = tx.QueryRow(ctx, `SELECT coalesce(checkout_session_id,''),attempt_id,state FROM stripe_payment_attempts WHERE order_id=$1 FOR UPDATE`, a.order).Scan(&checkout, &attempt, &current); err != nil {
		return operations.OrderState{}, err
	}
	// Replay wins over current eligibility, including a later paid/refunded state.
	if prior, ok, err := s.ops.ReplayStripeVoidTx(ctx, tx, req); ok || err != nil {
		return prior, err
	}
	locked, err := readVoidAttempt(ctx, tx, r, true)
	if err != nil {
		return operations.OrderState{}, err
	}
	if !locked.eligible || checkout != a.checkout || attempt != a.attempt {
		return operations.OrderState{}, ErrVoidNotEligible
	}
	if locked.version != r.ExpectedVersion {
		return operations.OrderState{}, operations.ErrConflict
	}
	var piStatus any
	if pi != nil {
		piStatus = pi.Status
	}
	_, err = tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state='EXPIRED',checkout_status='expired',provider_payment_status='unpaid',payment_intent_id=coalesce(payment_intent_id,NULLIF($2,'')),payment_intent_status=coalesce($3,payment_intent_status),provider_expires_at=$4,last_reconciled_at=clock_timestamp(),continuation_revoked_at=coalesce(continuation_revoked_at,clock_timestamp()),updated_at=clock_timestamp() WHERE order_id=$1`, a.order, x.PaymentIntentID, piStatus, x.ExpiresAt)
	if err != nil {
		return operations.OrderState{}, err
	}
	if err = s.appendAttemptEvent(ctx, tx, a.order, "STRIPE_VOID:"+r.IdempotencyKey, current, string(StateExpired), r.Actor); err != nil {
		return operations.OrderState{}, err
	}
	result, err := s.ops.VoidStripeUnpaidTx(ctx, tx, req)
	if errors.Is(err, operations.ErrPaymentUnresolved) {
		return operations.OrderState{}, ErrVoidNotEligible
	}
	if err != nil {
		return operations.OrderState{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return operations.OrderState{}, err
	}
	return result, nil
}
