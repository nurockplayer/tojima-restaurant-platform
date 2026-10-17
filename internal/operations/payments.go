package operations

import (
	"context"
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
// must already hold the session and payment-attempt locks in that order.
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
	if payment == "PAID" {
		return false, nil
	}
	if payment != "PENDING" && payment != "UNKNOWN" {
		return false, ErrNotReady
	}
	var held, sessionOpen bool
	var review bool
	var version int64
	if err = tx.QueryRow(ctx, `SELECT os.operational_hold,os.payment_review_required,os.version,ts.status='OPEN' AND ts.expires_at>clock_timestamp()
		FROM operation_order_state os JOIN table_sessions ts USING(session_id) WHERE os.order_id=$1`, v.OrderID).Scan(&held, &review, &version, &sessionOpen); err != nil {
		return false, err
	}
	if review {
		return false, nil
	}
	newReview := (held || !sessionOpen) && fulfillment == string(Waiting)
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
