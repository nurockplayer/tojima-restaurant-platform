package operations

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

// VerifiedStripePayment is passed only after the payments service has matched
// the provider object to an immutable local attempt and amount snapshot.
type VerifiedStripePayment struct {
	OrderID       string
	SessionID     string
	TenantID      string
	StoreID       string
	TableID       string
	AmountJPY     int64
	Currency      string
	PaymentIntent string
}

// ApplyStripePaidTx records the monotone Stripe-paid transition. The caller
// must already hold company catalog SHARE, session and payment-attempt locks
// in that order. HQ hold activation takes the same parent with UPDATE.
func (s *Service) ApplyStripePaidTx(ctx context.Context, tx pgx.Tx, v VerifiedStripePayment) (bool, error) {
	if tx == nil || strings.TrimSpace(v.OrderID) == "" || strings.TrimSpace(v.SessionID) == "" ||
		strings.TrimSpace(v.TenantID) == "" || strings.TrimSpace(v.StoreID) == "" || strings.TrimSpace(v.TableID) == "" ||
		v.AmountJPY <= 0 || v.Currency != "jpy" || strings.TrimSpace(v.PaymentIntent) == "" {
		return false, ErrInvalid
	}
	var method, payment, fulfillment string
	var tenant, store, table, session string
	var amount int64
	err := tx.QueryRow(ctx, `SELECT st.payment_method,st.payment_status,st.fulfillment_status,st.tenant_id,st.store_id,st.table_id,st.session_id,o.total_gross_jpy
		FROM operation_order_state st JOIN table_orders o USING(order_id)
		WHERE st.order_id=$1 FOR UPDATE OF st`, v.OrderID).
		Scan(&method, &payment, &fulfillment, &tenant, &store, &table, &session, &amount)
	if err != nil {
		return false, err
	}
	if method != "STRIPE" || tenant != v.TenantID || store != v.StoreID || table != v.TableID || session != v.SessionID || amount != v.AmountJPY {
		return false, ErrDenied
	}
	if payment == "PAID" || payment == "REFUND_PENDING" || payment == "REFUNDED" {
		return false, nil
	}
	if payment == "VOIDED" {
		if fulfillment != string(Cancelled) {
			return false, ErrNotReady
		}
		return s.applyLateStripePaymentTx(ctx, tx, v, amount)
	}
	if payment != "PENDING" && payment != "UNKNOWN" {
		return false, ErrNotReady
	}
	var held, sessionOpen, companyHeld bool
	var review bool
	var version int64
	if err = tx.QueryRow(ctx, `SELECT os.operational_hold,os.payment_review_required,os.version,ts.status='OPEN' AND ts.expires_at>clock_timestamp()
		FROM operation_order_state os JOIN table_sessions ts USING(session_id) WHERE os.order_id=$1`, v.OrderID).Scan(&held, &review, &version, &sessionOpen); err != nil {
		return false, err
	}
	if review {
		return false, nil
	}
	// The retained marker survives hold OFF and later detachment. The active
	// hold query also covers a completed-but-unpaid session at ON time.
	if err = tx.QueryRow(ctx, `SELECT a.company_hold_expire_requested_at IS NOT NULL OR (
		NOT EXISTS(SELECT 1 FROM store_menu_bindings b WHERE b.tenant_id=a.tenant_id AND b.store_id=a.store_id)
		AND EXISTS(SELECT 1 FROM company_product_emergency_holds h JOIN table_orders o ON o.order_id=a.order_id
			WHERE h.tenant_id=a.tenant_id AND h.active
			AND EXISTS(SELECT 1 FROM jsonb_array_elements(o.quote_snapshot->'quote'->'lines') l WHERE l->>'product_key'=h.product_key)))
		FROM stripe_payment_attempts a WHERE a.order_id=$1`, v.OrderID).Scan(&companyHeld); err != nil {
		return false, err
	}
	newReview := (held || companyHeld || !sessionOpen) && fulfillment == string(Waiting)
	newVersion := version + 1
	if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,state_version)
		VALUES($1,'stripe','STRIPE_PAID_VERIFIED',$2,'PAID',$3)`, v.OrderID, payment, newVersion); err != nil {
		return false, err
	}
	if newReview {
		if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,state_version)
			VALUES($1,'stripe','STRIPE_REVIEW_REQUIRED',$2,'PAID',$3)`, v.OrderID, payment, newVersion); err != nil {
			return false, err
		}
	}
	command, err := tx.Exec(ctx, `UPDATE operation_order_state SET payment_status='PAID',payment_review_required=$2,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$3`, v.OrderID, newReview, version)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() != 1 {
		return false, ErrConflict
	}
	if !newReview {
		if err = releaseOrder(ctx, tx, v.OrderID, "stripe"); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Service) applyLateStripePaymentTx(ctx context.Context, tx pgx.Tx, v VerifiedStripePayment, amount int64) (bool, error) {
	var version int64
	var fulfillment string
	var cancelled []byte
	var effective *int64
	if err := tx.QueryRow(ctx, `SELECT st.version,st.fulfillment_status,st.cancelled_quantities,st.effective_total_gross_jpy
		FROM operation_order_state st JOIN table_orders o USING(order_id) WHERE st.order_id=$1 FOR UPDATE OF st`, v.OrderID).Scan(&version, &fulfillment, &cancelled, &effective); err != nil {
		return false, err
	}
	if fulfillment != string(Cancelled) || effective == nil || *effective != 0 {
		return false, ErrNotReady
	}
	var kds bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_kds_cards WHERE order_id=$1)`, v.OrderID).Scan(&kds); err != nil {
		return false, err
	}
	if kds {
		return false, ErrNotReady
	}
	var lines []byte
	if err := tx.QueryRow(ctx, `SELECT lines FROM order_adjustments WHERE order_id=$1 ORDER BY state_version DESC LIMIT 1`, v.OrderID).Scan(&lines); err != nil {
		return false, err
	}
	newVersion := version + 1
	verified, err := json.Marshal(map[string]any{"payment_intent_id": v.PaymentIntent, "amount_jpy": v.AmountJPY, "currency": v.Currency})
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,before_fulfillment_status,after_fulfillment_status,state_version,reason,before_state,after_state)
		VALUES($1,'stripe','STRIPE_PAID_VERIFIED','VOIDED','REFUND_PENDING','CANCELLED','CANCELLED',$2,'late verified Stripe payment','{}'::jsonb,$3::jsonb)`, v.OrderID, newVersion, string(verified)); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,before_fulfillment_status,after_fulfillment_status,state_version,reason,before_state,after_state)
		VALUES($1,'stripe','STRIPE_LATE_PAYMENT_REFUND','VOIDED','REFUND_PENDING','CANCELLED','CANCELLED',$2,'late verified Stripe payment','{}'::jsonb,'{}'::jsonb)`, v.OrderID, newVersion); err != nil {
		return false, err
	}
	adjustmentID, err := randomID()
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_adjustments(adjustment_id,order_id,actor_id,reason,state_version,original_snapshot,original_tax_snapshot,lines,cancelled_quantities,previous_total_gross_jpy,effective_total_gross_jpy,effective_tax_snapshot,refund_amount_jpy,whole_order_cancel)
		SELECT $1,$2,'stripe','late verified Stripe payment', $3,o.quote_snapshot,o.tax_snapshot,$4::jsonb,$5::jsonb,$6,0,NULL,$6,true FROM table_orders o WHERE o.order_id=$2`, adjustmentID, v.OrderID, newVersion, string(lines), string(cancelled), amount); err != nil {
		return false, err
	}
	refundID, err := randomID()
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_refunds(refund_id,adjustment_id,order_id,amount_jpy,method,status,actor_id,provider_key) VALUES($1,$2,$3,$4,'STRIPE','REQUESTED','stripe',$5)`, refundID, adjustmentID, v.OrderID, amount, "p82-refund:"+refundID+":1"); err != nil {
		return false, err
	}
	if err = enqueueRefund(ctx, tx, v.OrderID, refundID, 1); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE operation_order_state SET payment_status='REFUND_PENDING',version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$2`, v.OrderID, version); err != nil {
		return false, err
	}
	return true, nil
}
