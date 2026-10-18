package operations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// ApproveFulfillment resolves a paid review hold and releases the existing KDS
// path atomically. Lock order is company catalog SHARE, session, payment attempt, order state,
// then idempotency/audit writes, matching payment workers and HQ hold changes.
func (s *Service) ApproveFulfillment(ctx context.Context, input OrderActionRequest) (OrderState, error) {
	if !validTableScope(input.Scope) || input.SessionID == "" || input.OrderID == "" || !validActor(input.Actor) ||
		!validKey(input.IdempotencyKey) || input.ExpectedVersion < 1 || len(strings.TrimSpace(input.Reason)) == 0 || utf8.RuneCountInString(strings.TrimSpace(input.Reason)) > 500 {
		return OrderState{}, ErrInvalid
	}
	digest, err := jsonDigest(struct {
		Action, Session, Order, Actor, Reason string
		Version                               int64
	}{
		"REVIEW_APPROVE", input.SessionID, input.OrderID, input.Actor, strings.TrimSpace(input.Reason), input.ExpectedVersion,
	})
	if err != nil {
		return OrderState{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OrderState{}, err
	}
	defer tx.Rollback(ctx)
	// Payment and HQ hold writers serialize on this company parent first.
	var catalogID int64
	err = tx.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind='COMPANY' FOR SHARE`, input.Scope.TenantID).Scan(&catalogID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, err
	}
	// Review is an explicit recovery action, so CLOSED and expired sessions are
	// permitted. Tenant/store/table identity and enabled state remain required.
	var sid string
	err = tx.QueryRow(ctx, `SELECT ts.session_id FROM table_sessions ts JOIN stores st USING(tenant_id,store_id) JOIN tenants t USING(tenant_id) JOIN dining_tables dt USING(tenant_id,store_id,table_id)
		WHERE ts.session_id=$1 AND ts.tenant_id=$2 AND ts.store_id=$3 AND ts.table_id=$4 AND st.enabled AND st.operating AND t.enabled AND dt.enabled FOR UPDATE OF ts`,
		input.SessionID, input.Scope.TenantID, input.Scope.StoreID, input.Scope.TableID).Scan(&sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, ErrNotFound
	}
	if err != nil {
		return OrderState{}, err
	}
	// Match payment's attempt-before-operation lock order. The row is updated
	// after the immutable manager audit row is written below.
	var attemptState string
	err = tx.QueryRow(ctx, `SELECT state FROM stripe_payment_attempts WHERE order_id=$1 AND session_id=$2 AND tenant_id=$3 AND store_id=$4 AND table_id=$5 FOR UPDATE`, input.OrderID, input.SessionID, input.Scope.TenantID, input.Scope.StoreID, input.Scope.TableID).Scan(&attemptState)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, ErrReviewNotReady
	}
	if err != nil {
		return OrderState{}, err
	}
	var priorDigest []byte
	var snapshot []byte
	err = tx.QueryRow(ctx, `SELECT request_digest,result_snapshot FROM operation_action_idempotency WHERE session_id=$1 AND order_id=$2 AND action='REVIEW_APPROVE' AND idempotency_key=$3`, input.SessionID, input.OrderID, input.IdempotencyKey).Scan(&priorDigest, &snapshot)
	if err == nil {
		if !equalHash(priorDigest, digest) {
			return OrderState{}, ErrIdempotencyConflict
		}
		var out OrderState
		if err = json.Unmarshal(snapshot, &out); err != nil {
			return OrderState{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return OrderState{}, err
		}
		return out, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, err
	}
	if attemptState != "REVIEW_REQUIRED" {
		return OrderState{}, ErrReviewNotReady
	}
	var payment string
	var fulfillment FulfillmentStatus
	var review, held bool
	var staffRequired, staffAccepted bool
	var decision string
	var version int64
	err = tx.QueryRow(ctx, `SELECT payment_status,fulfillment_status,payment_review_required,operational_hold,requires_staff_acceptance,staff_accepted,review_decision,version FROM operation_order_state
		WHERE order_id=$1 AND session_id=$2 AND tenant_id=$3 AND store_id=$4 AND table_id=$5 FOR UPDATE`, input.OrderID, input.SessionID, input.Scope.TenantID, input.Scope.StoreID, input.Scope.TableID).
		Scan(&payment, &fulfillment, &review, &held, &staffRequired, &staffAccepted, &decision, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, ErrNotFound
	}
	if err != nil {
		return OrderState{}, err
	}
	if payment != "PAID" || !review || fulfillment != Waiting || decision != "UNRESOLVED" {
		return OrderState{}, ErrReviewNotReady
	}
	var hasCard bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_kds_cards WHERE order_id=$1)`, input.OrderID).Scan(&hasCard); err != nil {
		return OrderState{}, err
	}
	if hasCard {
		return OrderState{}, ErrReviewNotReady
	}
	if version != input.ExpectedVersion {
		return OrderState{}, ErrConflict
	}
	var activeHold bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM table_orders o JOIN operation_order_state os USING(order_id) CROSS JOIN LATERAL jsonb_array_elements(o.quote_snapshot->'quote'->'lines') WITH ORDINALITY AS lines(l,idx)
		JOIN company_product_emergency_holds h ON h.tenant_id=o.tenant_id AND h.product_key=l->>'product_key' AND h.active
		WHERE o.order_id=$1 AND (l->>'quantity')::bigint>coalesce((os.cancelled_quantities->>(idx-1)::int)::bigint,0))`, input.OrderID).Scan(&activeHold)
	if err != nil {
		return OrderState{}, err
	}
	if activeHold {
		return OrderState{}, ErrCompanyHoldActive
	}
	before, _ := json.Marshal(map[string]any{"payment_status": payment, "payment_review_required": review, "operational_hold": held, "staff_accepted": staffAccepted, "fulfillment_status": fulfillment, "review_decision": decision, "version": version})
	newVersion := version + 1
	after, _ := json.Marshal(map[string]any{"payment_status": payment, "payment_review_required": false, "operational_hold": false, "staff_accepted": staffRequired, "fulfillment_status": fulfillment, "review_decision": "APPROVED", "version": newVersion})
	if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,before_fulfillment_status,after_fulfillment_status,state_version,reason,before_state,after_state) VALUES($1,$2,'MANAGER_REVIEW_APPROVED','PAID','PAID','WAITING','WAITING',$3,$4,$5::jsonb,$6::jsonb)`, input.OrderID, input.Actor, newVersion, strings.TrimSpace(input.Reason), string(before), string(after)); err != nil {
		return OrderState{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO stripe_payment_attempt_events(order_id,event_key,actor_id,before_state,after_state) VALUES($1,$2,$3,'REVIEW_REQUIRED','PAID')`, input.OrderID, "manager-review:"+input.IdempotencyKey, input.Actor); err != nil {
		return OrderState{}, err
	}
	command, err := tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state='PAID',payment_review_required=false,updated_at=clock_timestamp() WHERE order_id=$1 AND state='REVIEW_REQUIRED' AND payment_review_required`, input.OrderID)
	if err != nil {
		return OrderState{}, err
	}
	if command.RowsAffected() != 1 {
		return OrderState{}, ErrConflict
	}
	command, err = tx.Exec(ctx, `UPDATE operation_order_state SET payment_review_required=false,operational_hold=false,staff_accepted=requires_staff_acceptance,review_decision='APPROVED',version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$2 AND payment_review_required AND fulfillment_status='WAITING' AND review_decision='UNRESOLVED'`, input.OrderID, version)
	if err != nil {
		return OrderState{}, err
	}
	if command.RowsAffected() != 1 {
		return OrderState{}, ErrConflict
	}
	if err = releaseOrder(ctx, tx, input.OrderID, input.Actor); err != nil {
		return OrderState{}, err
	}
	out, err := readOneOrder(ctx, tx, input.Scope, input.SessionID, input.OrderID)
	if err != nil {
		return OrderState{}, err
	}
	result, err := json.Marshal(out)
	if err != nil {
		return OrderState{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_action_idempotency(session_id,order_id,action,idempotency_key,request_digest,result_version,result_snapshot) VALUES($1,$2,'REVIEW_APPROVE',$3,$4,$5,$6::jsonb)`, input.SessionID, input.OrderID, input.IdempotencyKey, digest, out.Version, string(result)); err != nil {
		return OrderState{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OrderState{}, err
	}
	return out, nil
}

// ListPaymentReviews returns only safe order snapshots and reason codes. It
// includes closed/expired sessions for recovery and never exposes provider IDs.
func (s *Service) ListPaymentReviews(ctx context.Context, scope StoreScope) ([]OrderState, error) {
	if !validStoreScope(scope) {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	orders, err := readOrdersMode(ctx, tx, TableScope{TenantID: scope.TenantID, StoreID: scope.StoreID}, "", false, true)
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return orders, nil
	}
	ids := make([]string, len(orders))
	for i := range orders {
		ids[i] = orders[i].ID
	}
	rows, err := tx.Query(ctx, `SELECT o.order_id,ts.status='OPEN' AND ts.expires_at>clock_timestamp(),os.operational_hold,
		(coalesce(a.company_hold_expire_requested_at IS NOT NULL,false) OR EXISTS(SELECT 1 FROM jsonb_array_elements(o.quote_snapshot->'quote'->'lines') l JOIN company_product_emergency_holds h ON h.tenant_id=o.tenant_id AND h.product_key=l->>'product_key' AND h.active))
		FROM table_orders o JOIN operation_order_state os USING(order_id) JOIN table_sessions ts ON ts.session_id=o.session_id
		LEFT JOIN stripe_payment_attempts a USING(order_id) WHERE o.order_id=ANY($1::text[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type reasons struct {
		open, hold, company bool
	}
	byID := map[string]reasons{}
	for rows.Next() {
		var id string
		var r reasons
		if err = rows.Scan(&id, &r.open, &r.hold, &r.company); err != nil {
			return nil, err
		}
		byID[id] = r
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range orders {
		r, ok := byID[orders[i].ID]
		if !ok {
			return nil, ErrNotFound
		}
		codes := []string{}
		if orders[i].PaymentReviewRequired {
			codes = append(codes, "PAYMENT_REVIEW")
		}
		for _, refund := range orders[i].Refunds {
			if refund.Status != "SUCCEEDED" {
				codes = append(codes, "REFUND_UNRESOLVED")
				break
			}
		}
		for _, request := range orders[i].CancellationRequests {
			if request.ManualExceptionReason != "" {
				codes = append(codes, "STOP_MANUAL_EXCEPTION")
				break
			}
			if request.Status == "REQUESTED" || request.Status == "KITCHEN_CONFIRMED" {
				codes = append(codes, "STOP_REQUEST_PENDING")
				break
			}
		}
		if r.hold {
			codes = append(codes, "OPERATIONAL_HOLD")
		}
		if !r.open {
			codes = append(codes, "SESSION_CLOSED")
		}
		if r.company {
			codes = append(codes, "COMPANY_HOLD")
		}
		orders[i].ReviewReasonCodes = codes
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return orders, nil
}
