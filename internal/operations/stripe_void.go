package operations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
)

type StripeVoidRequest struct {
	Scope           TableScope
	SessionID       string
	OrderID         string
	Actor           string
	ExpectedVersion int64
	IdempotencyKey  string
	Reason          string
}

func validStripeVoid(r StripeVoidRequest) bool {
	r.Reason = strings.TrimSpace(r.Reason)
	return validTableScope(r.Scope) && r.SessionID != "" && r.OrderID != "" && validActor(r.Actor) &&
		r.ExpectedVersion > 0 && validKey(r.IdempotencyKey) && utf8.RuneCountInString(r.Reason) > 0 && utf8.RuneCountInString(r.Reason) <= 500
}

func stripeVoidDigest(r StripeVoidRequest) ([]byte, error) {
	r.Reason = strings.TrimSpace(r.Reason)
	return jsonDigest(r)
}

func replayStripeVoid(ctx context.Context, tx pgx.Tx, r StripeVoidRequest, digest []byte) (OrderState, bool, error) {
	var prior, snapshot []byte
	err := tx.QueryRow(ctx, `SELECT i.request_digest,i.result_snapshot FROM operation_action_idempotency i
		JOIN table_orders o ON o.order_id=i.order_id
	WHERE i.session_id=$1 AND i.order_id=$2 AND i.action='STRIPE_VOID' AND i.idempotency_key=$3
	 AND o.session_id=$1 AND o.tenant_id=$4 AND o.store_id=$5 AND o.table_id=$6`, r.SessionID, r.OrderID, r.IdempotencyKey, r.Scope.TenantID, r.Scope.StoreID, r.Scope.TableID).Scan(&prior, &snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, false, nil
	}
	if err != nil {
		return OrderState{}, false, err
	}
	if !equalHash(prior, digest) {
		return OrderState{}, true, ErrIdempotencyConflict
	}
	var out OrderState
	err = json.Unmarshal(snapshot, &out)
	return out, true, err
}

// ReplayStripeVoid returns an accepted snapshot before payment-provider work.
func (s *Service) ReplayStripeVoid(ctx context.Context, r StripeVoidRequest) (OrderState, bool, error) {
	if !validStripeVoid(r) {
		return OrderState{}, false, ErrInvalid
	}
	digest, err := stripeVoidDigest(r)
	if err != nil {
		return OrderState{}, false, err
	}
	var prior, snapshot []byte
	err = s.db.QueryRow(ctx, `SELECT i.request_digest,i.result_snapshot FROM operation_action_idempotency i
		JOIN table_orders o ON o.order_id=i.order_id
	WHERE i.session_id=$1 AND i.order_id=$2 AND i.action='STRIPE_VOID' AND i.idempotency_key=$3
	 AND o.session_id=$1 AND o.tenant_id=$4 AND o.store_id=$5 AND o.table_id=$6`, r.SessionID, r.OrderID, r.IdempotencyKey, r.Scope.TenantID, r.Scope.StoreID, r.Scope.TableID).Scan(&prior, &snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, false, nil
	}
	if err != nil {
		return OrderState{}, false, err
	}
	if !equalHash(prior, digest) {
		return OrderState{}, true, ErrIdempotencyConflict
	}
	var out OrderState
	err = json.Unmarshal(snapshot, &out)
	return out, true, err
}

// ReplayStripeVoidTx repeats the replay check after the caller has acquired its locks.
func (s *Service) ReplayStripeVoidTx(ctx context.Context, tx pgx.Tx, r StripeVoidRequest) (OrderState, bool, error) {
	if !validStripeVoid(r) {
		return OrderState{}, false, ErrInvalid
	}
	digest, err := stripeVoidDigest(r)
	if err != nil {
		return OrderState{}, false, err
	}
	return replayStripeVoid(ctx, tx, r, digest)
}

// VoidStripeUnpaidTx writes the immutable cancellation and idempotency snapshot
// under locks already held by the payments caller. Provider proof stays outside
// this package and this transaction.
func (s *Service) VoidStripeUnpaidTx(ctx context.Context, tx pgx.Tx, r StripeVoidRequest) (OrderState, error) {
	if tx == nil || !validStripeVoid(r) {
		return OrderState{}, ErrInvalid
	}
	r.Reason = strings.TrimSpace(r.Reason)
	digest, err := stripeVoidDigest(r)
	if err != nil {
		return OrderState{}, err
	}
	if out, found, e := replayStripeVoid(ctx, tx, r, digest); e != nil {
		return out, e
	} else if found {
		return out, nil
	}
	a, err := lockAdjustment(ctx, tx, r.Scope, r.SessionID, r.OrderID)
	if err != nil {
		return OrderState{}, err
	}
	if a.order.Version != r.ExpectedVersion {
		return OrderState{}, ErrConflict
	}
	if a.order.PaymentMethod != "STRIPE" || (a.order.PaymentStatus != Pending && a.order.PaymentStatus != Unknown) || a.order.FulfillmentStatus != Waiting || a.order.PaymentReviewRequired || a.order.OperationalHold || a.order.ReviewDecision != "UNRESOLVED" {
		return OrderState{}, ErrPaymentUnresolved
	}
	var card bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_kds_cards WHERE order_id=$1)`, r.OrderID).Scan(&card); err != nil {
		return OrderState{}, err
	}
	if card {
		return OrderState{}, ErrNotReady
	}
	// Unpaid Stripe orders cannot reach KDS; with no card, P-82 C2 kitchen stop confirmation does not apply.
	originalTax := a.tax
	quantities := make([]int64, len(a.order.Lines))
	cancelled := make([]int64, len(a.order.Lines))
	lines := append([]OrderLine(nil), a.order.Lines...)
	for i := range lines {
		quantities[i] = 0
		cancelled[i] = lines[i].OriginalQuantity
		lines[i].Quantity = 0
		lines[i].LineGrossJPY = 0
	}
	effectiveTax, err := tax.ReduceHistorical(originalTax, quantities)
	if err != nil {
		return OrderState{}, err
	}
	effectiveTaxJSON, err := json.Marshal(effectiveTax)
	if err != nil {
		return OrderState{}, err
	}
	taxJSON, err := json.Marshal(originalTax)
	if err != nil {
		return OrderState{}, err
	}
	linesJSON, err := json.Marshal(lines)
	if err != nil {
		return OrderState{}, err
	}
	cancelledJSON, err := json.Marshal(cancelled)
	if err != nil {
		return OrderState{}, err
	}
	adjustmentID, err := randomID()
	if err != nil {
		return OrderState{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_adjustments(adjustment_id,order_id,actor_id,reason,state_version,original_snapshot,original_tax_snapshot,lines,cancelled_quantities,previous_total_gross_jpy,effective_total_gross_jpy,effective_tax_snapshot,refund_amount_jpy,whole_order_cancel) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,$10,0,$11::jsonb,0,true)`, adjustmentID, r.OrderID, r.Actor, r.Reason, a.order.Version+1, string(a.quote), string(taxJSON), string(linesJSON), string(cancelledJSON), a.order.EffectiveTotalGrossJPY, string(effectiveTaxJSON)); err != nil {
		return OrderState{}, err
	}
	before, err := json.Marshal(a.order)
	if err != nil {
		return OrderState{}, err
	}
	after, err := json.Marshal(map[string]any{"payment_status": Voided, "fulfillment_status": Cancelled, "review_decision": a.order.ReviewDecision, "version": a.order.Version + 1, "effective_total_gross_jpy": 0, "cancelled_quantities": cancelled})
	if err != nil {
		return OrderState{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,before_fulfillment_status,after_fulfillment_status,state_version,reason,before_state,after_state) VALUES($1,$2,'STRIPE_UNPAID_VOIDED',$3,'VOIDED',$4,'CANCELLED',$5,$6,$7::jsonb,$8::jsonb)`, r.OrderID, r.Actor, string(a.order.PaymentStatus), string(a.order.FulfillmentStatus), a.order.Version+1, r.Reason, string(before), string(after)); err != nil {
		return OrderState{}, err
	}
	cmd, err := tx.Exec(ctx, `UPDATE operation_order_state SET payment_status='VOIDED',fulfillment_status='CANCELLED',effective_total_gross_jpy=0,effective_tax_snapshot=$3::jsonb,cancelled_quantities=$4::jsonb,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$2`, r.OrderID, r.ExpectedVersion, string(effectiveTaxJSON), string(cancelledJSON))
	if err != nil {
		return OrderState{}, err
	}
	if cmd.RowsAffected() != 1 {
		return OrderState{}, ErrConflict
	}
	out, err := readOneOrder(ctx, tx, r.Scope, r.SessionID, r.OrderID)
	if err != nil {
		return out, err
	}
	result, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_action_idempotency(session_id,order_id,action,idempotency_key,request_digest,result_version,result_snapshot) VALUES($1,$2,'STRIPE_VOID',$3,$4,$5,$6::jsonb)`, r.SessionID, r.OrderID, r.IdempotencyKey, digest, out.Version, string(result))
	return out, err
}
