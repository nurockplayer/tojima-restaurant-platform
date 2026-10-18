package payments

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

type reconcileRow struct {
	order, attempt, session, tenant, store, table string
	credential, mode, account, checkout, pi       string
	amount                                        int64
	updatedAt                                     time.Time
}

// reconcile retrieves only the provider object already bound to this durable
// attempt. It never creates a Checkout Session or returns a provider URL.
func (s *Service) reconcile(ctx context.Context, scope operations.TableScope, sessionID, orderID, actor string) (Status, error) {
	if !validStoreScope(operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID}) || strings.TrimSpace(scope.TableID) == "" || sessionID == "" || orderID == "" || strings.TrimSpace(actor) == "" {
		return Status{}, ErrInvalid
	}
	var a reconcileRow
	err := s.db.QueryRow(ctx, `SELECT order_id,attempt_id,session_id,tenant_id,store_id,table_id,credential_ref,provider_mode,stripe_account_id,
		coalesce(checkout_session_id,''),coalesce(payment_intent_id,''),amount_jpy,updated_at
		FROM stripe_payment_attempts WHERE order_id=$1 AND session_id=$2 AND tenant_id=$3 AND store_id=$4 AND table_id=$5`,
		orderID, sessionID, scope.TenantID, scope.StoreID, scope.TableID).
		Scan(&a.order, &a.attempt, &a.session, &a.tenant, &a.store, &a.table, &a.credential, &a.mode, &a.account, &a.checkout, &a.pi, &a.amount, &a.updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, err
	}
	if a.checkout == "" && a.pi == "" {
		return s.readSafeStatus(ctx, scope, sessionID, orderID)
	}
	resolved, err := s.resolve(ctx, a.credential, a.account, a.mode)
	if err != nil {
		return Status{}, ErrUnavailable
	}
	var checkout *stripeprovider.CheckoutResult
	var pi *stripeprovider.PaymentIntentResult
	if a.checkout != "" {
		result, retrieveErr := resolved.Provider.RetrieveCheckout(ctx, resolved.Account, a.checkout)
		if retrieveErr != nil {
			return Status{}, ErrUnavailable
		}
		if !validReconcileCheckout(result, a) {
			return Status{}, ErrDenied
		}
		checkout = &result
		if result.PaymentIntentID != "" {
			if a.pi != "" && a.pi != result.PaymentIntentID {
				return Status{}, ErrDenied
			}
			// A Checkout retrieval is authoritative for Checkout state. Retrieve
			// its PaymentIntent too when present so processing/failure is current.
			intent, intentErr := resolved.Provider.RetrievePaymentIntent(ctx, resolved.Account, result.PaymentIntentID)
			if intentErr != nil {
				return Status{}, ErrUnavailable
			}
			if !validReconcileIntent(intent, a, result.PaymentIntentID) {
				return Status{}, ErrDenied
			}
			pi = &intent
		}
	} else {
		result, retrieveErr := resolved.Provider.RetrievePaymentIntent(ctx, resolved.Account, a.pi)
		if retrieveErr != nil {
			return Status{}, ErrUnavailable
		}
		if !validReconcileIntent(result, a, a.pi) {
			return Status{}, ErrDenied
		}
		pi = &result
	}
	return s.persistReconciliation(ctx, scope, a, checkout, pi)
}

func validReconcileCheckout(x stripeprovider.CheckoutResult, a reconcileRow) bool {
	return x.AccountID == a.account && x.SessionID == a.checkout && x.AmountJPY == a.amount && x.Currency == stripeprovider.CurrencyJPY && !x.LiveMode &&
		x.Metadata["tenant_id"] == a.tenant && x.Metadata["store_id"] == a.store && x.Metadata["order_id"] == a.order && x.Metadata["attempt_id"] == a.attempt &&
		(x.Status == stripeprovider.CheckoutOpen || x.Status == stripeprovider.CheckoutComplete || x.Status == stripeprovider.CheckoutExpired) &&
		(x.PaymentStatus == stripeprovider.PaymentUnpaid || x.PaymentStatus == stripeprovider.PaymentPaid)
}

func validReconcileIntent(x stripeprovider.PaymentIntentResult, a reconcileRow, expectedID string) bool {
	return x.AccountID == a.account && x.ID == expectedID && x.AmountJPY == a.amount && x.Currency == stripeprovider.CurrencyJPY && !x.LiveMode &&
		x.Metadata["tenant_id"] == a.tenant && x.Metadata["store_id"] == a.store && x.Metadata["order_id"] == a.order && x.Metadata["attempt_id"] == a.attempt &&
		validPIStatus(x.Status)
}

func (s *Service) persistReconciliation(ctx context.Context, scope operations.TableScope, a reconcileRow, checkout *stripeprovider.CheckoutResult, pi *stripeprovider.PaymentIntentResult) (Status, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Status{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentSessionAny(ctx, tx, a.order); err != nil {
		return Status{}, err
	}
	var current, actualAttempt string
	var currentCheckout, currentPI string
	var currentUpdatedAt time.Time
	var amount int64
	err = tx.QueryRow(ctx, `SELECT state,attempt_id,coalesce(checkout_session_id,''),coalesce(payment_intent_id,''),amount_jpy,updated_at
		FROM stripe_payment_attempts WHERE order_id=$1 AND session_id=$2 AND tenant_id=$3 AND store_id=$4 AND table_id=$5 FOR UPDATE`,
		a.order, a.session, a.tenant, a.store, a.table).Scan(&current, &actualAttempt, &currentCheckout, &currentPI, &amount, &currentUpdatedAt)
	if err != nil {
		return Status{}, err
	}
	if actualAttempt != a.attempt || amount != a.amount || (currentCheckout != "" && a.checkout != "" && currentCheckout != a.checkout) || (currentPI != "" && a.pi != "" && currentPI != a.pi) {
		return Status{}, ErrDenied
	}
	if currentUpdatedAt.After(a.updatedAt) && reconcileState(checkout, pi) != StatePaid {
		// Another webhook/worker committed after this retrieval began. Do not
		// overwrite its newer observation with a potentially stale response.
		// A verified paid observation is monotone, including after an unpaid void.
		_ = tx.Rollback(ctx)
		return s.readSafeStatus(ctx, scope, a.session, a.order)
	}
	next := reconcileState(checkout, pi)
	if current == string(StateExpired) && next != StatePaid {
		var voided bool
		if err = tx.QueryRow(ctx, `SELECT payment_status='VOIDED' AND fulfillment_status='CANCELLED' FROM operation_order_state WHERE order_id=$1 FOR UPDATE`, a.order).Scan(&voided); err != nil {
			return Status{}, err
		}
		// A cancelled intent is still unpaid. Preserve the void's EXPIRED
		// close proof until a verified payment starts the refund path.
		if voided {
			next = StateExpired
		}
	}
	// Paid is monotone. A concurrent webhook may have won while this provider
	// retrieval was in flight; no later non-paid observation can undo it.
	if current == string(StatePaid) || current == string(StateReviewRequired) {
		next = AttemptState(current)
	} else if next == StateUnknown && current != string(StateUnknown) {
		// An ambiguous provider observation must not erase a useful local state.
		next = AttemptState(current)
	}
	if next == StatePaid || next == StateReviewRequired {
		intentID := ""
		if checkout != nil {
			intentID = checkout.PaymentIntentID
		}
		if pi != nil {
			intentID = pi.ID
		}
		if intentID == "" {
			return Status{}, ErrDenied
		}
		_, err = s.ops.ApplyStripePaidTx(ctx, tx, operations.VerifiedStripePayment{OrderID: a.order, SessionID: a.session, TenantID: a.tenant, StoreID: a.store, TableID: a.table, AmountJPY: a.amount, Currency: stripeprovider.CurrencyJPY, PaymentIntent: intentID})
		if err != nil {
			return Status{}, err
		}
		var review bool
		if err = tx.QueryRow(ctx, `SELECT payment_review_required FROM operation_order_state WHERE order_id=$1`, a.order).Scan(&review); err != nil {
			return Status{}, err
		}
		if review {
			next = StateReviewRequired
		} else {
			next = StatePaid
		}
	}
	if current != string(next) {
		if err = s.appendAttemptEvent(ctx, tx, a.order, "RECONCILE:"+current+":"+string(next)+":"+s.cfg.Now().UTC().Format(time.RFC3339Nano), current, string(next), "staff-reconcile"); err != nil {
			return Status{}, err
		}
	}
	var checkoutStatus, paymentStatus, piStatus any
	var providerExpires any
	if checkout != nil {
		checkoutStatus = string(checkout.Status)
		paymentStatus = string(checkout.PaymentStatus)
		providerExpires = checkout.ExpiresAt
	}
	if pi != nil {
		piStatus = pi.Status
		if pi.Status == "succeeded" {
			paymentStatus = string(stripeprovider.PaymentPaid)
		} else if paymentStatus == nil || paymentStatus == string(stripeprovider.PaymentUnpaid) {
			paymentStatus = string(stripeprovider.PaymentUnpaid)
		}
	}
	if next == StatePaid || next == StateReviewRequired {
		paymentStatus = string(stripeprovider.PaymentPaid)
	}
	_, err = tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state=$2,
		checkout_status=coalesce(NULLIF($3,''),checkout_status),
		provider_payment_status=coalesce(NULLIF($4,''),provider_payment_status),
		payment_intent_status=coalesce(NULLIF($5,''),payment_intent_status),
		checkout_session_id=coalesce(checkout_session_id,NULLIF($6,'')),
		payment_intent_id=coalesce(payment_intent_id,NULLIF($7,'')),
		provider_expires_at=coalesce($8,provider_expires_at),payment_review_required=$9,
		last_reconciled_at=clock_timestamp(),updated_at=clock_timestamp() WHERE order_id=$1`,
		a.order, string(next), checkoutStatus, paymentStatus, piStatus,
		checkoutID(checkout), paymentIntentID(checkout, pi), providerExpires, next == StateReviewRequired)
	if err != nil {
		return Status{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Status{}, err
	}
	return s.readSafeStatus(ctx, scope, a.session, a.order)
}

func checkoutID(x *stripeprovider.CheckoutResult) string {
	if x == nil {
		return ""
	}
	return x.SessionID
}

func paymentIntentID(checkout *stripeprovider.CheckoutResult, pi *stripeprovider.PaymentIntentResult) string {
	if pi != nil {
		return pi.ID
	}
	if checkout != nil {
		return checkout.PaymentIntentID
	}
	return ""
}

func reconcileState(checkout *stripeprovider.CheckoutResult, pi *stripeprovider.PaymentIntentResult) AttemptState {
	if (checkout != nil && checkout.PaymentStatus == stripeprovider.PaymentPaid) || (pi != nil && pi.Status == "succeeded") {
		return StatePaid
	}
	if pi != nil {
		switch pi.Status {
		case "canceled":
			return StateFailed
		case "processing", "requires_action", "requires_confirmation", "requires_capture":
			return StateProcessing
		case "requires_payment_method":
			if checkout == nil {
				return StateUnknown
			}
		}
	}
	if checkout != nil {
		switch checkout.Status {
		case stripeprovider.CheckoutExpired:
			return StateExpired
		case stripeprovider.CheckoutComplete:
			return StateProcessing
		case stripeprovider.CheckoutOpen:
			return StateOpen
		}
	}
	return StateUnknown
}
