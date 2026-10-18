package httpapi

import (
	"errors"
	"net/http"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
)

func (a OperationsAPI) adjustment(w http.ResponseWriter, r *http.Request, preview bool) {
	scope, actor, ok := a.tableAuthorize(w, r, "operations.order.adjust", fulfillmentApprovalRoles)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion       int64                       `json:"expected_version"`
		Reason                string                      `json:"reason"`
		Lines                 []operations.AdjustmentLine `json:"lines"`
		CashHandBackConfirmed bool                        `json:"cash_hand_back_confirmed"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	input := operations.AdjustmentRequest{Scope: scope, Actor: actor.SubjectID, SessionID: r.PathValue("sessionID"), OrderID: r.PathValue("orderID"), IdempotencyKey: r.Header.Get("Idempotency-Key"), ExpectedVersion: body.ExpectedVersion, Reason: body.Reason, Lines: body.Lines, CashHandBackConfirmed: body.CashHandBackConfirmed}
	if preview {
		v, err := a.Operations.PreviewAdjustment(r.Context(), input)
		adjustmentRespond(w, v, err)
		return
	}
	v, err := a.Operations.AdjustOrder(r.Context(), input)
	adjustmentRespond(w, v, err)
}
func (a OperationsAPI) previewAdjustment(w http.ResponseWriter, r *http.Request) {
	a.adjustment(w, r, true)
}
func (a OperationsAPI) adjustOrder(w http.ResponseWriter, r *http.Request) { a.adjustment(w, r, false) }
func (a OperationsAPI) retryRefund(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.tableAuthorize(w, r, "operations.refund.retry", fulfillmentApprovalRoles)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	v, err := a.Operations.RetryRefund(r.Context(), operations.OrderActionRequest{Scope: scope, Actor: actor.SubjectID, SessionID: r.PathValue("sessionID"), OrderID: r.PathValue("orderID"), IdempotencyKey: r.Header.Get("Idempotency-Key"), ExpectedVersion: body.ExpectedVersion}, r.PathValue("refundID"))
	adjustmentRespond(w, v, err)
}
func (a OperationsAPI) acknowledgeUpdate(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.tableAuthorize(w, r, "operations.kds.acknowledge_update", []authz.Role{authz.Kitchen})
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	v, err := a.Operations.AcknowledgeOrderUpdate(r.Context(), operations.OrderActionRequest{Scope: scope, Actor: actor.SubjectID, SessionID: r.PathValue("sessionID"), OrderID: r.PathValue("orderID"), IdempotencyKey: r.Header.Get("Idempotency-Key"), ExpectedVersion: body.ExpectedVersion})
	adjustmentRespond(w, v, err)
}
func adjustmentRespond(w http.ResponseWriter, v any, err error) {
	switch {
	case errors.Is(err, operations.ErrStopRequestState):
		writeError(w, 409, "stop_request_state")
	case errors.Is(err, operations.ErrManualException):
		writeError(w, 409, "manual_exception_required")
	case errors.Is(err, operations.ErrConflict):
		writeError(w, 409, "stale_version")
	case errors.Is(err, operations.ErrIdempotencyConflict):
		writeError(w, 409, "idempotency_conflict")
	case errors.Is(err, operations.ErrPreparationStarted):
		writeError(w, 409, "preparation_started")
	case errors.Is(err, operations.ErrPaymentUnresolved):
		writeError(w, 409, "payment_unresolved")
	case errors.Is(err, operations.ErrCashHandBackRequired):
		writeError(w, 409, "cash_hand_back_required")
	case errors.Is(err, operations.ErrRefundNotRetryable):
		writeError(w, 409, "refund_not_retryable")
	case errors.Is(err, operations.ErrReviewNotReady):
		writeError(w, 409, "review_not_ready")
	default:
		operationRespond(w, v, err)
	}
}
