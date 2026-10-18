package operations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	ErrStopRequestPending = errors.New("stop request is unresolved")
	ErrStopRequestState   = errors.New("stop request is not ready for this action")
	ErrManualException    = errors.New("manual exception required")
)

type CancellationRequest struct {
	ID                    string `json:"id"`
	LineIndex             int    `json:"line_index"`
	Quantity              int64  `json:"quantity"`
	Reason                string `json:"reason"`
	RequestingManager     string `json:"requesting_manager"`
	KitchenConfirmer      string `json:"kitchen_confirmer"`
	NonDeliveryConfirmer  string `json:"non_delivery_confirmer"`
	Status                string `json:"status"`
	Version               int64  `json:"version"`
	ManualExceptionReason string `json:"manual_exception_reason"`
}

type StopRequestEligibility string

const (
	StopEligible            StopRequestEligibility = "ELIGIBLE"
	StopPending             StopRequestEligibility = "PENDING"
	StopManualException     StopRequestEligibility = "MANUAL_EXCEPTION"
	StopNoRemainingQuantity StopRequestEligibility = "NO_REMAINING_QUANTITY"
)

// stopRequestEligibility describes only line eligibility. Order state, holds,
// permissions and optimistic versions are still checked when requesting a stop.
func stopRequestEligibility(line OrderLine, requests []CancellationRequest) StopRequestEligibility {
	if line.Quantity <= 0 {
		return StopNoRemainingQuantity
	}
	for _, request := range requests {
		if request.LineIndex != line.LineIndex {
			continue
		}
		if request.Status == "REJECTED" || request.ManualExceptionReason != "" {
			return StopManualException
		}
		if request.Status == "REQUESTED" || request.Status == "KITCHEN_CONFIRMED" {
			return StopPending
		}
	}
	return StopEligible
}

type StopActionRequest struct {
	OrderActionRequest
	RequestID              string
	Action                 string
	ExpectedRequestVersion int64
	NonDeliveryConfirmed   bool
	CashHandBackConfirmed  bool
}

func validStopReason(reason string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(reason))
	return n > 0 && n <= 500
}

// RequestStop reserves a line for a human decision, without modifying any money
// or effective quantities. Session locking serializes KDS, close and settlements.
func (s *Service) RequestStop(ctx context.Context, r AdjustmentRequest) (OrderState, error) {
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
	if o, found, e := replayAction(ctx, tx, action, "STOP_REQUEST", digest); e != nil {
		return o, e
	} else if found {
		return o, tx.Commit(ctx)
	}
	if a.order.Version != r.ExpectedVersion {
		return OrderState{}, ErrConflict
	}
	if a.order.FulfillmentStatus != Preparing {
		return OrderState{}, ErrStopRequestState
	}
	var card bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_kds_cards WHERE order_id=$1)`, r.OrderID).Scan(&card); err != nil {
		return OrderState{}, err
	}
	if !card || a.order.OperationalHold || a.order.PaymentReviewRequired {
		return OrderState{}, ErrBlocked
	}
	seen := map[int]bool{}
	for _, l := range r.Lines {
		if l.LineIndex < 0 || l.LineIndex >= len(a.order.Lines) || seen[l.LineIndex] || l.CancelledQuantity <= 0 || l.CancelledQuantity > a.order.Lines[l.LineIndex].Quantity {
			return OrderState{}, ErrInvalid
		}
		seen[l.LineIndex] = true
		switch stopRequestEligibility(a.order.Lines[l.LineIndex], a.order.CancellationRequests) {
		case StopManualException:
			return OrderState{}, ErrManualException
		case StopPending:
			return OrderState{}, ErrStopRequestPending
		case StopNoRemainingQuantity:
			return OrderState{}, ErrInvalid
		}
		id, e := randomID()
		if e != nil {
			return OrderState{}, e
		}
		if _, err = tx.Exec(ctx, `INSERT INTO cancellation_requests(request_id,order_id,line_index,quantity,reason,requesting_manager,idempotency_key) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, r.OrderID, l.LineIndex, l.CancelledQuantity, r.Reason, r.Actor, r.IdempotencyKey); err != nil {
			return OrderState{}, err
		}
	}
	if err = stopAudit(ctx, tx, a.order, r.Actor, "STOP_REQUESTED", r.Reason, r.Lines); err != nil {
		return OrderState{}, err
	}
	if err = bumpStopOrder(ctx, tx, r.OrderID, r.ExpectedVersion); err != nil {
		return OrderState{}, err
	}
	o, err := saveAction(ctx, tx, action, "STOP_REQUEST", digest)
	if err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}

func bumpStopOrder(ctx context.Context, tx pgx.Tx, orderID string, version int64) error {
	cmd, err := tx.Exec(ctx, `UPDATE operation_order_state SET version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$2`, orderID, version)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func stopAudit(ctx context.Context, tx pgx.Tx, o OrderState, actor, event, reason string, details any) error {
	before, err := json.Marshal(o)
	if err != nil {
		return err
	}
	after, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,before_fulfillment_status,after_fulfillment_status,state_version,reason,before_state,after_state) VALUES($1,$2,$3,$4,$4,$5,$5,$6,$7,$8::jsonb,$9::jsonb)`, o.ID, actor, event, string(o.PaymentStatus), string(o.FulfillmentStatus), o.Version+1, reason, string(before), string(after))
	return err
}

func (s *Service) ActOnStopRequest(ctx context.Context, r StopActionRequest) (OrderState, error) {
	if !validTableScope(r.Scope) || r.SessionID == "" || r.OrderID == "" || !validActor(r.Actor) || !validKey(r.IdempotencyKey) || r.ExpectedVersion < 1 || r.ExpectedRequestVersion < 1 || r.RequestID == "" {
		return OrderState{}, ErrInvalid
	}
	switch r.Action {
	case "confirm", "reject", "withdraw", "finalize":
	default:
		return OrderState{}, ErrInvalid
	}
	r.Reason = strings.TrimSpace(r.Reason)
	if (r.Reason != "" && !validStopReason(r.Reason)) || ((r.Action == "reject" || (r.Action == "finalize" && !r.NonDeliveryConfirmed)) && !validStopReason(r.Reason)) {
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
	action := "STOP_" + strings.ToUpper(r.Action)
	if o, found, e := replayAction(ctx, tx, r.OrderActionRequest, action, digest); e != nil {
		return o, e
	} else if found {
		return o, tx.Commit(ctx)
	}
	var c CancellationRequest
	err = tx.QueryRow(ctx, `SELECT request_id,line_index,quantity,reason,requesting_manager,kitchen_confirmer,non_delivery_confirmer,status,version,manual_exception_reason FROM cancellation_requests WHERE request_id=$1 AND order_id=$2 FOR UPDATE`, r.RequestID, r.OrderID).Scan(&c.ID, &c.LineIndex, &c.Quantity, &c.Reason, &c.RequestingManager, &c.KitchenConfirmer, &c.NonDeliveryConfirmer, &c.Status, &c.Version, &c.ManualExceptionReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, ErrNotFound
	}
	if err != nil {
		return OrderState{}, err
	}
	if a.order.Version != r.ExpectedVersion || c.Version != r.ExpectedRequestVersion {
		return OrderState{}, ErrConflict
	}
	if a.order.FulfillmentStatus != Preparing {
		return OrderState{}, ErrStopRequestState
	}
	event := "STOP_" + strings.ToUpper(r.Action)
	reason := r.Reason
	if reason == "" {
		reason = c.Reason
	}
	var adjustmentID any
	switch r.Action {
	case "confirm":
		if c.Status != "REQUESTED" {
			return OrderState{}, ErrStopRequestState
		}
		c.Status = "KITCHEN_CONFIRMED"
		c.KitchenConfirmer = r.Actor
	case "reject":
		if c.Status != "REQUESTED" {
			return OrderState{}, ErrStopRequestState
		}
		c.Status = "REJECTED"
		c.KitchenConfirmer = r.Actor
		c.ManualExceptionReason = reason
	case "withdraw":
		if c.Status != "REQUESTED" && c.Status != "KITCHEN_CONFIRMED" {
			return OrderState{}, ErrStopRequestState
		}
		c.Status = "WITHDRAWN"
	case "finalize":
		if c.Status == "REJECTED" || c.ManualExceptionReason != "" {
			return OrderState{}, ErrManualException
		}
		if c.Status != "KITCHEN_CONFIRMED" || c.KitchenConfirmer == "" {
			return OrderState{}, ErrStopRequestState
		}
		if !r.NonDeliveryConfirmed {
			c.ManualExceptionReason = reason
			event = "STOP_MANUAL_EXCEPTION"
		} else {
			if a.order.OperationalHold || a.order.PaymentReviewRequired {
				return OrderState{}, ErrBlocked
			}
			c.Status = "FINALIZED"
			c.NonDeliveryConfirmer = r.Actor
			id, e := randomID()
			if e != nil {
				return OrderState{}, e
			}
			adjustmentID = id
		}
	}
	c.Version++
	cmd, err := tx.Exec(ctx, `UPDATE cancellation_requests SET status=$3,kitchen_confirmer=$4,non_delivery_confirmer=$5,manual_exception_reason=$6,adjustment_id=$7,version=version+1,updated_at=clock_timestamp() WHERE request_id=$1 AND version=$2`, c.ID, r.ExpectedRequestVersion, c.Status, c.KitchenConfirmer, c.NonDeliveryConfirmer, c.ManualExceptionReason, adjustmentID)
	if err != nil {
		return OrderState{}, err
	}
	if cmd.RowsAffected() != 1 {
		return OrderState{}, ErrConflict
	}
	if err = stopAudit(ctx, tx, a.order, r.Actor, event, reason, c); err != nil {
		return OrderState{}, err
	}
	if c.Status == "FINALIZED" {
		adjustment := AdjustmentRequest{Scope: r.Scope, SessionID: r.SessionID, OrderID: r.OrderID, Actor: r.Actor, ExpectedVersion: r.ExpectedVersion, Reason: c.Reason, Lines: []AdjustmentLine{{LineIndex: c.LineIndex, CancelledQuantity: c.Quantity}}, CashHandBackConfirmed: r.CashHandBackConfirmed}
		p, cancelled, e := calculateAdjustmentMode(a, adjustment, true)
		if e != nil {
			return OrderState{}, e
		}
		if a.order.PaymentMethod == "CASH" && p.RefundAmountJPY > 0 && !r.CashHandBackConfirmed {
			return OrderState{}, ErrCashHandBackRequired
		}
		if err = applyAdjustment(ctx, tx, a, adjustment, p, cancelled, adjustmentID.(string)); err != nil {
			return OrderState{}, err
		}
	} else if err = bumpStopOrder(ctx, tx, r.OrderID, r.ExpectedVersion); err != nil {
		return OrderState{}, err
	}
	o, err := saveAction(ctx, tx, r.OrderActionRequest, action, digest)
	if err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}
