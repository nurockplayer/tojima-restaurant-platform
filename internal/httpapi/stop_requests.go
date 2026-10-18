package httpapi

import (
	"net/http"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
)

func (a OperationsAPI) requestStop(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.tableAuthorize(w, r, "operations.stop.request", fulfillmentApprovalRoles)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64                       `json:"expected_version"`
		Reason          string                      `json:"reason"`
		Lines           []operations.AdjustmentLine `json:"lines"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	o, err := a.Operations.RequestStop(r.Context(), operations.AdjustmentRequest{Scope: scope, Actor: actor.SubjectID, SessionID: r.PathValue("sessionID"), OrderID: r.PathValue("orderID"), ExpectedVersion: body.ExpectedVersion, IdempotencyKey: r.Header.Get("Idempotency-Key"), Reason: body.Reason, Lines: body.Lines})
	adjustmentRespond(w, o, err)
}

func (a OperationsAPI) stopAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	roles := fulfillmentApprovalRoles
	switch action {
	case "confirm", "reject":
		roles = []authz.Role{authz.Kitchen}
	case "withdraw", "finalize":
	default:
		writeError(w, 404, "not_found")
		return
	}
	scope, actor, ok := a.tableAuthorize(w, r, "operations.stop."+action, roles)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion        int64  `json:"expected_version"`
		ExpectedRequestVersion int64  `json:"expected_request_version"`
		Reason                 string `json:"reason"`
		NonDeliveryConfirmed   bool   `json:"non_delivery_confirmed"`
		CashHandBackConfirmed  bool   `json:"cash_hand_back_confirmed"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	o, err := a.Operations.ActOnStopRequest(r.Context(), operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, Actor: actor.SubjectID, SessionID: r.PathValue("sessionID"), OrderID: r.PathValue("orderID"), ExpectedVersion: body.ExpectedVersion, IdempotencyKey: r.Header.Get("Idempotency-Key"), Reason: body.Reason}, RequestID: r.PathValue("requestID"), Action: action, ExpectedRequestVersion: body.ExpectedRequestVersion, NonDeliveryConfirmed: body.NonDeliveryConfirmed, CashHandBackConfirmed: body.CashHandBackConfirmed})
	adjustmentRespond(w, o, err)
}
