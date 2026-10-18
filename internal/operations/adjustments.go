package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
)

var (
	ErrPreparationStarted            = errors.New("order preparation has started")
	ErrPaymentUnresolved             = errors.New("payment must resolve before adjustment")
	ErrCashHandBackRequired          = errors.New("cash hand-back confirmation required")
	ErrUpdateAcknowledgementRequired = errors.New("kitchen update acknowledgement required")
	ErrRefundNotRetryable            = errors.New("refund is not a confirmed failure")
)

type AdjustmentLine struct {
	LineIndex         int   `json:"line_index"`
	CancelledQuantity int64 `json:"cancelled_quantity"`
}
type AdjustmentRequest struct {
	Scope                                     TableScope
	SessionID, OrderID, Actor, IdempotencyKey string
	ExpectedVersion                           int64            `json:"expected_version"`
	Reason                                    string           `json:"reason"`
	Lines                                     []AdjustmentLine `json:"lines"`
	CashHandBackConfirmed                     bool             `json:"cash_hand_back_confirmed"`
}
type AdjustmentPreview struct {
	ExpectedVersion        int64               `json:"expected_version"`
	OriginalTotalGrossJPY  int64               `json:"original_total_gross_jpy"`
	EffectiveTotalGrossJPY int64               `json:"effective_total_gross_jpy"`
	RefundAmountJPY        int64               `json:"refund_amount_jpy"`
	WholeOrderCancel       bool                `json:"whole_order_cancel"`
	Lines                  []OrderLine         `json:"lines"`
	Tax                    *tax.SnapshotRecord `json:"tax,omitempty"`
}
type Refund struct {
	ID        string `json:"id"`
	AmountJPY int64  `json:"amount_jpy"`
	Status    string `json:"status"`
	Method    string `json:"method"`
}

type adjustmentState struct {
	order         OrderState
	tax           tax.SnapshotRecord
	quote         []byte
	cancelled     []int64
	paymentIntent string
}

// Lock order shared with review, payment and HQ: company, session, attempt,
// operation, refund rows. Explicit recovery may target a closed session.
func lockAdjustment(ctx context.Context, tx pgx.Tx, scope TableScope, sessionID, orderID string) (adjustmentState, error) {
	var a adjustmentState
	var catalogID int64
	err := tx.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind='COMPANY' FOR SHARE`, scope.TenantID).Scan(&catalogID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return a, err
	}
	if _, err = lockSessionStatus(ctx, tx, scope, sessionID, "FOR UPDATE"); err != nil {
		return a, err
	}
	err = tx.QueryRow(ctx, `SELECT coalesce(payment_intent_id,'') FROM stripe_payment_attempts WHERE order_id=$1 AND session_id=$2 AND tenant_id=$3 AND store_id=$4 AND table_id=$5 FOR UPDATE`, orderID, sessionID, scope.TenantID, scope.StoreID, scope.TableID).Scan(&a.paymentIntent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return a, err
	}
	var id string
	var rawTax, rawCancelled []byte
	err = tx.QueryRow(ctx, `SELECT st.order_id,o.tax_snapshot,o.quote_snapshot,st.cancelled_quantities FROM operation_order_state st JOIN table_orders o USING(order_id) WHERE st.order_id=$1 AND st.session_id=$2 AND st.tenant_id=$3 AND st.store_id=$4 AND st.table_id=$5 FOR UPDATE OF st`, orderID, sessionID, scope.TenantID, scope.StoreID, scope.TableID).Scan(&id, &rawTax, &a.quote, &rawCancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal(rawTax, &a.tax); err != nil {
		return a, err
	}
	if err = json.Unmarshal(rawCancelled, &a.cancelled); err != nil {
		return a, err
	}
	a.order, err = readOneOrder(ctx, tx, scope, sessionID, orderID)
	return a, err
}
func validAdjustment(r AdjustmentRequest) bool {
	return validTableScope(r.Scope) && r.SessionID != "" && r.OrderID != "" && validActor(r.Actor) && r.ExpectedVersion > 0 && len(r.Lines) > 0 && len(r.Lines) <= 1000 && utf8.RuneCountInString(strings.TrimSpace(r.Reason)) > 0 && utf8.RuneCountInString(strings.TrimSpace(r.Reason)) <= 500
}
func calculateAdjustment(a adjustmentState, r AdjustmentRequest) (AdjustmentPreview, []int64, error) {
	o := a.order
	p := AdjustmentPreview{ExpectedVersion: o.Version, OriginalTotalGrossJPY: o.TotalGrossJPY, Lines: append([]OrderLine(nil), o.Lines...)}
	if o.Version != r.ExpectedVersion {
		return p, nil, ErrConflict
	}
	if o.FulfillmentStatus != Waiting && o.FulfillmentStatus != Accepted {
		return p, nil, ErrPreparationStarted
	}
	if o.PaymentMethod == "STRIPE" && (o.PaymentStatus != Paid || a.paymentIntent == "") {
		return p, nil, ErrPaymentUnresolved
	}
	// Approved review decisions are terminal for C1. A new post-approval action
	// belongs to a subsequent lane; approve and adjust cannot both win.
	if o.ReviewDecision != "UNRESOLVED" {
		return p, nil, ErrReviewNotReady
	}
	if len(o.Lines) != len(a.tax.Lines) {
		return p, nil, ErrInvalid
	}
	seen := map[int]bool{}
	for _, l := range r.Lines {
		if l.LineIndex < 0 || l.LineIndex >= len(p.Lines) || l.CancelledQuantity <= 0 || seen[l.LineIndex] || l.CancelledQuantity > p.Lines[l.LineIndex].Quantity {
			return p, nil, ErrInvalid
		}
		seen[l.LineIndex] = true
		p.Lines[l.LineIndex].Quantity -= l.CancelledQuantity
	}
	quantities := make([]int64, len(p.Lines))
	cancelled := make([]int64, len(p.Lines))
	p.WholeOrderCancel = true
	for i, l := range p.Lines {
		if l.OriginalQuantity != a.tax.Lines[i].Quantity || l.UnitGrossJPY != a.tax.Lines[i].BaseGrossJPY+a.tax.Lines[i].OptionGrossJPY {
			return p, nil, ErrInvalid
		}
		quantities[i] = l.Quantity
		cancelled[i] = l.OriginalQuantity - l.Quantity
		p.Lines[i].LineGrossJPY = l.UnitGrossJPY * l.Quantity
		if l.Quantity > 0 {
			p.WholeOrderCancel = false
		}
	}
	calculated, err := tax.ReduceHistorical(a.tax, quantities)
	if err != nil {
		return p, nil, err
	}
	p.Tax = calculated
	if calculated != nil {
		p.EffectiveTotalGrossJPY = calculated.Total.GrossJPY
	}
	if p.EffectiveTotalGrossJPY > o.EffectiveTotalGrossJPY {
		return p, nil, ErrInvalid
	}
	if o.PaymentStatus == Paid {
		p.RefundAmountJPY = o.EffectiveTotalGrossJPY - p.EffectiveTotalGrossJPY
	}
	return p, cancelled, nil
}
func (s *Service) PreviewAdjustment(ctx context.Context, r AdjustmentRequest) (AdjustmentPreview, error) {
	if !validAdjustment(r) {
		return AdjustmentPreview{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AdjustmentPreview{}, err
	}
	defer tx.Rollback(ctx)
	a, err := lockAdjustment(ctx, tx, r.Scope, r.SessionID, r.OrderID)
	if err != nil {
		return AdjustmentPreview{}, err
	}
	p, _, err := calculateAdjustment(a, r)
	if err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}
func replayAction(ctx context.Context, tx pgx.Tx, r OrderActionRequest, action string, digest []byte) (OrderState, bool, error) {
	var prior, snapshot []byte
	err := tx.QueryRow(ctx, `SELECT request_digest,result_snapshot FROM operation_action_idempotency WHERE session_id=$1 AND order_id=$2 AND action=$3 AND idempotency_key=$4`, r.SessionID, r.OrderID, action, r.IdempotencyKey).Scan(&prior, &snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, false, nil
	}
	if err != nil {
		return OrderState{}, false, err
	}
	if !equalHash(prior, digest) {
		return OrderState{}, true, ErrIdempotencyConflict
	}
	var o OrderState
	err = json.Unmarshal(snapshot, &o)
	return o, true, err
}
func saveAction(ctx context.Context, tx pgx.Tx, r OrderActionRequest, action string, digest []byte) (OrderState, error) {
	o, err := readOneOrder(ctx, tx, r.Scope, r.SessionID, r.OrderID)
	if err != nil {
		return o, err
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return o, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_action_idempotency(session_id,order_id,action,idempotency_key,request_digest,result_version,result_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb)`, r.SessionID, r.OrderID, action, r.IdempotencyKey, digest, o.Version, string(raw))
	return o, err
}
func (s *Service) AdjustOrder(ctx context.Context, r AdjustmentRequest) (OrderState, error) {
	if !validAdjustment(r) || !validKey(r.IdempotencyKey) {
		return OrderState{}, ErrInvalid
	}
	r.Reason = strings.TrimSpace(r.Reason)
	digest, err := jsonDigest(r)
	if err != nil {
		return OrderState{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OrderState{}, err
	}
	defer tx.Rollback(ctx)
	a, err := lockAdjustment(ctx, tx, r.Scope, r.SessionID, r.OrderID)
	if err != nil {
		return OrderState{}, err
	}
	action := OrderActionRequest{Scope: r.Scope, SessionID: r.SessionID, OrderID: r.OrderID, Actor: r.Actor, ExpectedVersion: r.ExpectedVersion, IdempotencyKey: r.IdempotencyKey}
	if o, found, e := replayAction(ctx, tx, action, "ADJUST", digest); e != nil {
		return o, e
	} else if found {
		return o, tx.Commit(ctx)
	}
	p, cancelled, err := calculateAdjustment(a, r)
	if err != nil {
		return OrderState{}, err
	}
	if a.order.PaymentMethod == "CASH" && p.RefundAmountJPY > 0 && !r.CashHandBackConfirmed {
		return OrderState{}, ErrCashHandBackRequired
	}
	adjustmentID, err := randomID()
	if err != nil {
		return OrderState{}, err
	}
	taxJSON, err := json.Marshal(a.tax)
	if err != nil {
		return OrderState{}, err
	}
	linesJSON, _ := json.Marshal(p.Lines)
	cancelledJSON, _ := json.Marshal(cancelled)
	var effectiveTax any
	if p.Tax != nil {
		b, e := json.Marshal(p.Tax)
		if e != nil {
			return OrderState{}, e
		}
		effectiveTax = string(b)
	}
	_, err = tx.Exec(ctx, `INSERT INTO order_adjustments(adjustment_id,order_id,actor_id,reason,state_version,original_snapshot,original_tax_snapshot,lines,cancelled_quantities,previous_total_gross_jpy,effective_total_gross_jpy,effective_tax_snapshot,refund_amount_jpy,whole_order_cancel) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,$10,$11,$12::jsonb,$13,$14)`, adjustmentID, r.OrderID, r.Actor, r.Reason, a.order.Version+1, string(a.quote), string(taxJSON), string(linesJSON), string(cancelledJSON), a.order.EffectiveTotalGrossJPY, p.EffectiveTotalGrossJPY, effectiveTax, p.RefundAmountJPY, p.WholeOrderCancel)
	if err != nil {
		return OrderState{}, err
	}
	payment := a.order.PaymentStatus
	fulfillment := a.order.FulfillmentStatus
	decision := a.order.ReviewDecision
	review := a.order.PaymentReviewRequired
	hold := a.order.OperationalHold
	if p.WholeOrderCancel {
		fulfillment = Cancelled
		decision = "CANCELLED"
		review = false
		hold = false
		if payment == Paid {
			if a.order.PaymentMethod == "STRIPE" {
				payment = RefundPending
			} else {
				payment = Refunded
			}
		}
	}
	// A final zero-price line may be cancelled after all earlier refunds have
	// already succeeded. Do not create a zero-value refund or strand its status.
	if p.WholeOrderCancel && payment == RefundPending && p.RefundAmountJPY == 0 {
		var pending bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_refunds WHERE order_id=$1 AND status<>'SUCCEEDED')`, r.OrderID).Scan(&pending); err != nil {
			return OrderState{}, err
		}
		if !pending {
			payment = Refunded
		}
	}
	before, _ := json.Marshal(a.order)
	after, _ := json.Marshal(map[string]any{"preview": p, "payment_status": payment, "fulfillment_status": fulfillment, "review_decision": decision, "payment_review_required": review, "operational_hold": hold, "version": a.order.Version + 1})
	_, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,before_fulfillment_status,after_fulfillment_status,state_version,reason,before_state,after_state) VALUES($1,$2,'ORDER_ADJUSTED',$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb)`, r.OrderID, r.Actor, string(a.order.PaymentStatus), string(payment), string(a.order.FulfillmentStatus), string(fulfillment), a.order.Version+1, r.Reason, string(before), string(after))
	if err != nil {
		return OrderState{}, err
	}
	if p.WholeOrderCancel && a.order.PaymentReviewRequired {
		if _, err = tx.Exec(ctx, `INSERT INTO stripe_payment_attempt_events(order_id,event_key,actor_id,before_state,after_state) VALUES($1,$2,$3,'REVIEW_REQUIRED','PAID')`, r.OrderID, "manager-review:cancel:"+adjustmentID, r.Actor); err != nil {
			return OrderState{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state='PAID',payment_review_required=false,updated_at=clock_timestamp() WHERE order_id=$1 AND state='REVIEW_REQUIRED'`, r.OrderID); err != nil {
			return OrderState{}, err
		}
	}
	if p.RefundAmountJPY > 0 {
		refundID, e := randomID()
		if e != nil {
			return OrderState{}, e
		}
		state := "REQUESTED"
		var receipt any
		if a.order.PaymentMethod == "CASH" {
			state = "SUCCEEDED"
			var id string
			if err = tx.QueryRow(ctx, `SELECT receipt_id FROM cash_settlement_allocations WHERE order_id=$1`, r.OrderID).Scan(&id); err != nil {
				return OrderState{}, err
			}
			receipt = id
		}
		if _, err = tx.Exec(ctx, `INSERT INTO operation_refunds(refund_id,adjustment_id,order_id,amount_jpy,method,status,actor_id,receipt_id,provider_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, refundID, adjustmentID, r.OrderID, p.RefundAmountJPY, a.order.PaymentMethod, state, r.Actor, receipt, "p82-refund:"+refundID+":1"); err != nil {
			return OrderState{}, err
		}
		if a.order.PaymentMethod == "STRIPE" {
			if err = enqueueRefund(ctx, tx, r.OrderID, refundID, 1); err != nil {
				return OrderState{}, err
			}
		} else {
			invoice, e := cashAdjustedInvoice(ctx, tx, receipt.(string), r.OrderID, p.Tax)
			if e != nil {
				return OrderState{}, e
			}
			if _, err = tx.Exec(ctx, `INSERT INTO cash_refund_ledger(refund_id,receipt_id,order_id,actor_id,amount_jpy,effective_tax_invoice) VALUES($1,$2,$3,$4,$5,$6::jsonb)`, refundID, receipt, r.OrderID, r.Actor, p.RefundAmountJPY, invoice); err != nil {
				return OrderState{}, err
			}
		}
	}
	cmd, err := tx.Exec(ctx, `UPDATE operation_order_state SET effective_total_gross_jpy=$3,effective_tax_snapshot=$4::jsonb,cancelled_quantities=$5::jsonb,payment_status=$6,fulfillment_status=$7,review_decision=$8,payment_review_required=$9,operational_hold=$10,order_updated=order_updated OR EXISTS(SELECT 1 FROM operation_kds_cards WHERE order_id=$1),version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$2 AND review_decision='UNRESOLVED'`, r.OrderID, r.ExpectedVersion, p.EffectiveTotalGrossJPY, effectiveTax, string(cancelledJSON), string(payment), string(fulfillment), decision, review, hold)
	if err != nil {
		return OrderState{}, err
	}
	if cmd.RowsAffected() != 1 {
		return OrderState{}, ErrConflict
	}
	o, err := saveAction(ctx, tx, action, "ADJUST", digest)
	if err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}

func enqueueRefund(ctx context.Context, tx pgx.Tx, orderID, refundID string, generation int64) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO stripe_payment_work(work_id,dedupe_key,work_type,order_id,refund_id,refund_generation) VALUES($1,$2,'REFUND',$3,$4,$5)`, id, fmt.Sprintf("refund:%s:%d", refundID, generation), orderID, refundID, generation)
	return err
}

// Rebuild the CASH transaction boundary, including unchanged receipt orders,
// from historical snapshots. Never sum independently rounded order taxes.
func cashAdjustedInvoice(ctx context.Context, tx pgx.Tx, receiptID, changed string, changedTax *tax.SnapshotRecord) (any, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT tax_invoice FROM cash_settlements WHERE receipt_id=$1`, receiptID).Scan(&raw); err != nil {
		return nil, err
	}
	var original tax.InvoiceRecord
	if err := json.Unmarshal(raw, &original); err != nil {
		return nil, err
	}
	orders := []tax.InvoiceOrder{}
	for _, o := range original.Orders {
		if o.OrderID == changed {
			if changedTax != nil {
				orders = append(orders, tax.InvoiceOrder{OrderID: changed, Snapshot: *changedTax})
			}
			continue
		}
		var adjusted []byte
		var hasAdjustment bool
		if err := tx.QueryRow(ctx, `SELECT effective_total_gross_jpy IS NOT NULL,effective_tax_snapshot FROM operation_order_state WHERE order_id=$1`, o.OrderID).Scan(&hasAdjustment, &adjusted); err != nil {
			return nil, err
		}
		if hasAdjustment {
			if len(adjusted) == 0 {
				continue
			}
			if err := json.Unmarshal(adjusted, &o.Snapshot); err != nil {
				return nil, err
			}
		}
		orders = append(orders, o)
	}
	if len(orders) == 0 {
		return nil, nil
	}
	result, err := tax.AggregateInvoice(original.Scope, original.Date, orders)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(result)
	return string(b), err
}

func (s *Service) RetryRefund(ctx context.Context, r OrderActionRequest, refundID string) (OrderState, error) {
	if !validTableScope(r.Scope) || r.SessionID == "" || r.OrderID == "" || !validActor(r.Actor) || !validKey(r.IdempotencyKey) || r.ExpectedVersion < 1 || refundID == "" {
		return OrderState{}, ErrInvalid
	}
	digest, err := jsonDigest(struct {
		Request OrderActionRequest
		Refund  string
	}{r, refundID})
	if err != nil {
		return OrderState{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OrderState{}, err
	}
	defer tx.Rollback(ctx)
	a, err := lockAdjustment(ctx, tx, r.Scope, r.SessionID, r.OrderID)
	if err != nil {
		return OrderState{}, err
	}
	if o, found, e := replayAction(ctx, tx, r, "REFUND_RETRY", digest); e != nil {
		return o, e
	} else if found {
		return o, tx.Commit(ctx)
	}
	if a.order.Version != r.ExpectedVersion {
		return OrderState{}, ErrConflict
	}
	var status, method, providerID string
	var generation int64
	err = tx.QueryRow(ctx, `SELECT status,method,coalesce(provider_refund_id,''),generation FROM operation_refunds WHERE refund_id=$1 AND order_id=$2 FOR UPDATE`, refundID, r.OrderID).Scan(&status, &method, &providerID, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, ErrNotFound
	}
	if err != nil {
		return OrderState{}, err
	}
	if status != "FAILED" || method != "STRIPE" || providerID == "" {
		return OrderState{}, ErrRefundNotRetryable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_refund_attempt_history(refund_id,generation,provider_refund_id,provider_key,status) SELECT refund_id,generation,provider_refund_id,provider_key,status FROM operation_refunds WHERE refund_id=$1`, refundID); err != nil {
		return OrderState{}, err
	}
	beforeRetry, _ := json.Marshal(map[string]any{"refund_id": refundID, "refund_status": "FAILED", "generation": generation, "provider_refund_id": providerID})
	generation++
	afterRetry, _ := json.Marshal(map[string]any{"refund_id": refundID, "refund_status": "REQUESTED", "generation": generation})
	if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,state_version,before_state,after_state) VALUES($1,$2,'REFUND_RETRIED',$3,$4::jsonb,$5::jsonb)`, r.OrderID, r.Actor, a.order.Version+1, string(beforeRetry), string(afterRetry)); err != nil {
		return OrderState{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE operation_refunds SET status='REQUESTED',generation=$2,provider_key=$3,provider_refund_id=NULL,retry_not_after=clock_timestamp()+interval '20 hours',safe_error_code=NULL,updated_at=clock_timestamp() WHERE refund_id=$1`, refundID, generation, fmt.Sprintf("p82-refund:%s:%d", refundID, generation)); err != nil {
		return OrderState{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE operation_order_state SET version=version+1,updated_at=clock_timestamp() WHERE order_id=$1`, r.OrderID); err != nil {
		return OrderState{}, err
	}
	if err = enqueueRefund(ctx, tx, r.OrderID, refundID, generation); err != nil {
		return OrderState{}, err
	}
	o, err := saveAction(ctx, tx, r, "REFUND_RETRY", digest)
	if err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}
func (s *Service) AcknowledgeOrderUpdate(ctx context.Context, r OrderActionRequest) (OrderState, error) {
	if !validTableScope(r.Scope) || r.SessionID == "" || r.OrderID == "" || !validActor(r.Actor) || !validKey(r.IdempotencyKey) || r.ExpectedVersion < 1 {
		return OrderState{}, ErrInvalid
	}
	digest, err := jsonDigest(r)
	if err != nil {
		return OrderState{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OrderState{}, err
	}
	defer tx.Rollback(ctx)
	a, err := lockAdjustment(ctx, tx, r.Scope, r.SessionID, r.OrderID)
	if err != nil {
		return OrderState{}, err
	}
	if o, found, e := replayAction(ctx, tx, r, "KDS_ACK", digest); e != nil {
		return o, e
	} else if found {
		return o, tx.Commit(ctx)
	}
	if a.order.Version != r.ExpectedVersion {
		return OrderState{}, ErrConflict
	}
	if !a.order.OrderUpdated || (a.order.FulfillmentStatus != Accepted && a.order.FulfillmentStatus != Cancelled) {
		return OrderState{}, ErrNotReady
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,state_version) VALUES($1,$2,'KDS_UPDATE_ACKNOWLEDGED',$3)`, r.OrderID, r.Actor, a.order.Version+1); err != nil {
		return OrderState{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE operation_order_state SET order_updated=false,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1`, r.OrderID); err != nil {
		return OrderState{}, err
	}
	o, err := saveAction(ctx, tx, r, "KDS_ACK", digest)
	if err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}
