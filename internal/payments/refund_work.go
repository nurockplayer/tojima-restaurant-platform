package payments

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

type refundWorkRow struct {
	ID, OrderID, RefundID, TenantID, StoreID, Credential, Mode, Account, PaymentIntent, Key, ProviderRefundID string
	APIVersion                                                                                                string
	Profile                                                                                                   []byte
	Amount, Generation, ObservationVersion                                                                    int64
	RetryNotAfter                                                                                             *time.Time
	Status                                                                                                    string
}

func (s *Service) loadRefundWork(ctx context.Context, refundID string) (refundWorkRow, error) {
	var r refundWorkRow
	err := s.db.QueryRow(ctx, `SELECT r.refund_id,r.order_id,r.refund_id,a.tenant_id,a.store_id,a.credential_ref,a.provider_mode,a.stripe_account_id,
		coalesce(a.payment_intent_id,''),r.provider_key,coalesce(r.provider_refund_id,''),r.amount_jpy,r.generation,r.retry_not_after,r.status,a.api_version,a.request_profile_fingerprint,r.observation_version
		FROM operation_refunds r JOIN stripe_payment_attempts a USING(order_id) WHERE r.refund_id=$1`, refundID).
		Scan(&r.ID, &r.OrderID, &r.RefundID, &r.TenantID, &r.StoreID, &r.Credential, &r.Mode, &r.Account, &r.PaymentIntent, &r.Key, &r.ProviderRefundID, &r.Amount, &r.Generation, &r.RetryNotAfter, &r.Status, &r.APIVersion, &r.Profile, &r.ObservationVersion)
	return r, err
}

func (s *Service) processRefund(ctx context.Context, w workItem) error {
	if w.RefundID == nil {
		return s.finishWork(ctx, w, false, 0, "MISSING_REFUND")
	}
	if w.RefundGeneration == nil {
		return s.finishWork(ctx, w, false, 0, "MISSING_REFUND_GENERATION")
	}
	r, err := s.loadRefundWork(ctx, *w.RefundID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.finishWork(ctx, w, false, 0, "MISSING_REFUND")
	}
	if err != nil {
		return err
	}
	if r.Generation != *w.RefundGeneration {
		return s.finishWork(ctx, w, false, 0, "STALE_REFUND_GENERATION")
	}
	if r.PaymentIntent == "" || r.Amount <= 0 {
		return s.retryRefund(ctx, w, "REFUND_PAYMENT_BINDING_MISSING")
	}
	resolved, err := s.resolve(ctx, r.Credential, r.Account, r.Mode)
	if err != nil || r.APIVersion != stripeprovider.APIVersion || !bytesEqual(r.Profile, resolved.ProfileFingerprint[:]) {
		return s.retryRefund(ctx, w, "REFUND_DISPATCH_PENDING")
	}

	// Anchor only after configuration validation, immediately before dispatch.
	// The committed deadline survives ambiguous calls and expired worker leases.
	if err = s.markRefundProcessing(ctx, w, &r); err != nil {
		return err
	}
	// Provider calls remain outside transactions. Once bound, retrieve the same
	// provider object; before binding, repeat CreateRefund with the immutable key.
	var result stripeprovider.RefundResult
	if r.ProviderRefundID != "" {
		result, err = resolved.Provider.RetrieveRefund(ctx, resolved.Account, r.ProviderRefundID)
	} else if r.RetryNotAfter != nil && refundRetryWindowOpen(ctx, s.db, *r.RetryNotAfter) {
		result, err = resolved.Provider.CreateRefund(ctx, stripeprovider.CreateRefundRequest{Account: resolved.Account, TenantID: r.TenantID, StoreID: r.StoreID, OrderID: r.OrderID, RefundID: r.RefundID, PaymentIntentID: r.PaymentIntent, IdempotencyKey: r.Key, AmountJPY: r.Amount})
	} else {
		return s.retryRefund(ctx, w, "HOLD_RECONCILIATION_NEEDED")
	}
	if err != nil {
		return s.retryRefund(ctx, w, "REFUND_PROVIDER_OUTCOME_UNKNOWN")
	}
	if !validRefundResult(result, r) {
		return s.retryRefund(ctx, w, "UNSAFE_REFUND_RESPONSE")
	}
	// Create only discovers the object. Every provider status projection, including
	// initial success and unknown statuses, must come from retrieval.
	if r.ProviderRefundID == "" {
		r.ProviderRefundID = result.ID
		// Retain the known binding even if retrieval fails past the create horizon.
		if err = s.persistRefundObservation(ctx, w, r, result, false); err != nil {
			return err
		}
		result, err = resolved.Provider.RetrieveRefund(ctx, resolved.Account, r.ProviderRefundID)
		if err != nil {
			return s.retryRefund(ctx, w, "REFUND_RETRIEVE_UNKNOWN")
		}
		if !validRefundResult(result, r) {
			return s.retryRefund(ctx, w, "UNSAFE_REFUND_RESPONSE")
		}
	}
	return s.persistRefundResult(ctx, w, r, result)
}

func refundRetryWindowOpen(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, deadline time.Time) bool {
	var open bool
	return db.QueryRow(ctx, `SELECT clock_timestamp()<$1`, deadline).Scan(&open) == nil && open
}

func (s *Service) markRefundProcessing(ctx context.Context, w workItem, r *refundWorkRow) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentSessionAny(ctx, tx, r.OrderID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM stripe_payment_attempts WHERE order_id=$1 FOR UPDATE`, r.OrderID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM operation_order_state WHERE order_id=$1 FOR UPDATE`, r.OrderID); err != nil {
		return err
	}
	var status string
	var currentGeneration int64
	if err = tx.QueryRow(ctx, `SELECT status,generation,coalesce(provider_refund_id,''),retry_not_after,observation_version FROM operation_refunds WHERE refund_id=$1 FOR UPDATE`, r.RefundID).Scan(&status, &currentGeneration, &r.ProviderRefundID, &r.RetryNotAfter, &r.ObservationVersion); err != nil {
		return err
	}
	var workGeneration int64
	if err = tx.QueryRow(ctx, `SELECT refund_generation FROM stripe_payment_work WHERE work_id=$1 AND status='LEASED' AND lease_token=$2 AND lease_until>clock_timestamp() FOR UPDATE`, w.ID, w.Lease).Scan(&workGeneration); err != nil {
		return ErrConflict
	}
	if workGeneration != r.Generation || currentGeneration != r.Generation {
		return ErrConflict
	}
	if r.ProviderRefundID == "" && r.RetryNotAfter == nil {
		if err = tx.QueryRow(ctx, `UPDATE operation_refunds SET retry_not_after=clock_timestamp()+interval '20 hours',updated_at=clock_timestamp() WHERE refund_id=$1 RETURNING retry_not_after`, r.RefundID).Scan(&r.RetryNotAfter); err != nil {
			return err
		}
	}
	if status == "REQUESTED" {
		if _, err = tx.Exec(ctx, `UPDATE operation_refunds SET status='PROCESSING',updated_at=clock_timestamp() WHERE refund_id=$1`, r.RefundID); err != nil {
			return err
		}
		if err = bumpRefundOperationVersion(ctx, tx, r.OrderID, r.RefundID, status, "PROCESSING", r.Generation, r.ProviderRefundID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func validRefundResult(x stripeprovider.RefundResult, r refundWorkRow) bool {
	return x.AccountID == r.Account && x.ID != "" && (r.ProviderRefundID == "" || x.ID == r.ProviderRefundID) && x.PaymentIntentID == r.PaymentIntent && x.AmountJPY == r.Amount && x.Currency == stripeprovider.CurrencyJPY && !x.LiveMode &&
		x.Metadata["tenant_id"] == r.TenantID && x.Metadata["store_id"] == r.StoreID && x.Metadata["order_id"] == r.OrderID && x.Metadata["refund_id"] == r.RefundID
}

func (s *Service) retryRefund(ctx context.Context, w workItem, code string) error {
	return s.finishWork(ctx, w, true, holdExpireBackoff(w.Attempts), code)
}

// Both work sources share binding, staleness, projection, audit and retry rules.
func (s *Service) persistRefundResult(ctx context.Context, w workItem, r refundWorkRow, x stripeprovider.RefundResult) error {
	return s.persistRefundObservation(ctx, w, r, x, true)
}

func (s *Service) persistRefundObservation(ctx context.Context, w workItem, r refundWorkRow, x stripeprovider.RefundResult, retrieved bool) error {
	if !validRefundResult(x, r) {
		return ErrDenied
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentSessionAny(ctx, tx, r.OrderID); err != nil {
		return err
	}
	var currentPI string
	if err = tx.QueryRow(ctx, `SELECT coalesce(payment_intent_id,'') FROM stripe_payment_attempts WHERE order_id=$1 FOR UPDATE`, r.OrderID).Scan(&currentPI); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM operation_order_state WHERE order_id=$1 FOR UPDATE`, r.OrderID); err != nil {
		return err
	}
	var currentStatus, providerID, currentCode string
	var currentGeneration, observationVersion int64
	if err = tx.QueryRow(ctx, `SELECT status,coalesce(provider_refund_id,''),generation,observation_version,coalesce(safe_error_code,'') FROM operation_refunds WHERE refund_id=$1 FOR UPDATE`, r.RefundID).Scan(&currentStatus, &providerID, &currentGeneration, &observationVersion, &currentCode); err != nil {
		return err
	}
	var workGeneration int64
	if err = tx.QueryRow(ctx, `SELECT refund_generation FROM stripe_payment_work WHERE work_id=$1 AND status='LEASED' AND lease_token=$2 AND lease_until>clock_timestamp() FOR UPDATE`, w.ID, w.Lease).Scan(&workGeneration); err != nil {
		return ErrConflict
	}
	if currentGeneration != r.Generation || w.RefundGeneration == nil || currentGeneration != *w.RefundGeneration || workGeneration != currentGeneration {
		if err = s.completeLeased(ctx, tx, w); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if currentPI != r.PaymentIntent || (providerID != "" && providerID != x.ID) {
		return ErrDenied
	}
	if !retrieved {
		_, err = tx.Exec(ctx, `UPDATE operation_refunds SET provider_refund_id=coalesce(provider_refund_id,$2),updated_at=clock_timestamp() WHERE refund_id=$1`, r.RefundID, x.ID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// A concurrent retrieval has already committed since this observation began.
	// Discard the stale snapshot, regardless of status, and retrieve again.
	if observationVersion != r.ObservationVersion {
		if err = requeueLeasedRefund(ctx, tx, w, "REFUND_PROCESSING"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	next, code := projectedRefundStatus(x.Status)
	changed := currentStatus != next || providerID == "" || currentCode != code
	if _, err = tx.Exec(ctx, `UPDATE operation_refunds SET status=$2,provider_refund_id=coalesce(provider_refund_id,$3),safe_error_code=nullif($4,''),observation_version=observation_version+1,updated_at=clock_timestamp() WHERE refund_id=$1`, r.RefundID, next, x.ID, code); err != nil {
		return err
	}
	if changed {
		if err = bumpRefundOperationVersion(ctx, tx, r.OrderID, r.RefundID, currentStatus, next, r.Generation, x.ID); err != nil {
			return err
		}
	}
	if next == "PROCESSING" {
		if code == "" {
			code = "REFUND_PROCESSING"
		}
		if err = requeueLeasedRefund(ctx, tx, w, code); err != nil {
			return err
		}
	} else {
		if w.RefundEventID != nil {
			if _, err = tx.Exec(ctx, `UPDATE stripe_refund_webhook_events SET processed_at=clock_timestamp(),safe_error_code=NULL WHERE event_row_id=$1`, *w.RefundEventID); err != nil {
				return err
			}
		}
		if err = s.completeLeased(ctx, tx, w); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Local state projects the retrieved Stripe object, never the prior local state
// or webhook payload. Enum: stripe-go/v87 v87.0.0, API 2026-09-30.endive.
func projectedRefundStatus(providerStatus string) (status, safeCode string) {
	switch providerStatus {
	case "pending", "requires_action":
		return "PROCESSING", ""
	case "succeeded":
		return "SUCCEEDED", ""
	case "failed", "canceled":
		return "FAILED", "PROVIDER_REFUND_FAILED"
	default:
		return "PROCESSING", "HOLD_RECONCILIATION_NEEDED"
	}
}

func requeueLeasedRefund(ctx context.Context, tx pgx.Tx, w workItem, code string) error {
	cmd, err := tx.Exec(ctx, `UPDATE stripe_payment_work SET status='READY',lease_token=NULL,lease_until=NULL,available_at=clock_timestamp()+$4::interval,safe_error_code=$3,updated_at=clock_timestamp() WHERE work_id=$1 AND status='LEASED' AND lease_token=$2 AND lease_until>clock_timestamp()`, w.ID, w.Lease, code, intervalString(holdExpireBackoff(w.Attempts)))
	if err != nil {
		return err
	}
	if cmd.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Service) processRefundWebhook(ctx context.Context, w workItem) error {
	if w.RefundEventID == nil {
		return s.finishWork(ctx, w, false, 0, "MISSING_REFUND_EVENT")
	}
	if w.RefundGeneration == nil {
		return s.finishWork(ctx, w, false, 0, "MISSING_REFUND_GENERATION")
	}
	var refundID, providerRefundID string
	var eventGeneration int64
	err := s.db.QueryRow(ctx, `SELECT refund_id,provider_object_id,generation FROM stripe_refund_webhook_events WHERE event_row_id=$1`, *w.RefundEventID).Scan(&refundID, &providerRefundID, &eventGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.finishWork(ctx, w, false, 0, "MISSING_REFUND_EVENT")
	}
	if err != nil {
		return err
	}
	r, err := s.loadRefundWork(ctx, refundID)
	if err != nil {
		return err
	}
	if eventGeneration != r.Generation || eventGeneration != *w.RefundGeneration {
		return s.finishStaleRefundWebhook(ctx, w)
	}
	if r.ProviderRefundID != "" && r.ProviderRefundID != providerRefundID {
		return ErrDenied
	}
	r.ProviderRefundID = providerRefundID
	resolved, err := s.resolve(ctx, r.Credential, r.Account, r.Mode)
	if err != nil || r.APIVersion != stripeprovider.APIVersion || !bytesEqual(r.Profile, resolved.ProfileFingerprint[:]) {
		return s.retryRefund(ctx, w, "PROVIDER_UNCONFIGURED")
	}
	x, err := resolved.Provider.RetrieveRefund(ctx, resolved.Account, r.ProviderRefundID)
	if err != nil {
		return s.retryRefund(ctx, w, "REFUND_RETRIEVE_UNKNOWN")
	}
	if !validRefundResult(x, r) {
		return s.retryRefund(ctx, w, "UNSAFE_REFUND_RESPONSE")
	}
	return s.persistRefundWebhookResult(ctx, w, r, x)
}

func (s *Service) persistRefundWebhookResult(ctx context.Context, w workItem, r refundWorkRow, x stripeprovider.RefundResult) error {
	return s.persistRefundObservation(ctx, w, r, x, true)
}

func (s *Service) finishStaleRefundWebhook(ctx context.Context, w workItem) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if w.RefundEventID != nil {
		if _, err = tx.Exec(ctx, `UPDATE stripe_refund_webhook_events SET processed_at=clock_timestamp(),safe_error_code='STALE_REFUND_GENERATION' WHERE event_row_id=$1`, *w.RefundEventID); err != nil {
			return err
		}
	}
	if err = s.completeLeased(ctx, tx, w); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func bumpRefundOperationVersion(ctx context.Context, tx pgx.Tx, orderID, refundID, beforeRefundStatus, afterRefundStatus string, generation int64, providerRefundID string) error {
	var payment, fulfillment, decision string
	var review, hold, updated bool
	var version int64
	if err := tx.QueryRow(ctx, `SELECT payment_status,fulfillment_status,review_decision,payment_review_required,operational_hold,order_updated,version FROM operation_order_state WHERE order_id=$1`, orderID).Scan(&payment, &fulfillment, &decision, &review, &hold, &updated, &version); err != nil {
		return err
	}
	newPayment := payment
	if (payment == "REFUND_PENDING" || payment == "REFUNDED") && fulfillment == "CANCELLED" {
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_refunds WHERE order_id=$1 AND status<>'SUCCEEDED')`, orderID).Scan(&pending); err != nil {
			return err
		}
		if pending {
			newPayment = "REFUND_PENDING"
		} else {
			newPayment = "REFUNDED"
		}
	}
	newVersion := version + 1
	if _, err := tx.Exec(ctx, `UPDATE operation_order_state SET payment_status=$4,version=$2,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$3`, orderID, newVersion, version, newPayment); err != nil {
		return err
	}
	state := map[string]any{"payment_status": payment, "fulfillment_status": fulfillment, "review_decision": decision, "payment_review_required": review, "operational_hold": hold, "order_updated": updated, "version": version, "refund_id": refundID, "refund_status": beforeRefundStatus, "refund_generation": generation, "provider_refund_id": providerRefundID}
	after := map[string]any{"payment_status": newPayment, "fulfillment_status": fulfillment, "review_decision": decision, "payment_review_required": review, "operational_hold": hold, "order_updated": updated, "version": newVersion, "refund_id": refundID, "refund_status": afterRefundStatus, "refund_generation": generation, "provider_refund_id": providerRefundID}
	beforeJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,before_fulfillment_status,after_fulfillment_status,state_version,before_state,after_state) VALUES($1,'stripe-provider','REFUND_STATUS_CHANGED',$2,$3,$4,$4,$5,$6::jsonb,$7::jsonb)`, orderID, payment, newPayment, fulfillment, newVersion, beforeJSON, afterJSON)
	return err
}
