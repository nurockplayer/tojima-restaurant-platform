package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
)

// OperationsAPI requires the same verified MOD-08 identity/current-membership
// ports as table entry. Guest capabilities never become staff credentials.
type OperationsAPI struct {
	Operations *operations.Service
	Sessions   *tablesession.Service
	Identity   IdentityResolver
	Authorizer authz.Authorizer
}

var cashRoles = []authz.Role{authz.Staff, authz.StoreManager}
var fulfillmentApprovalRoles = []authz.Role{authz.StoreManager}
var manageRoles = []authz.Role{authz.StoreManager, authz.TenantAdmin}
var operationReadRoles = []authz.Role{authz.Staff, authz.StoreManager, authz.Kitchen, authz.TenantAdmin}

func NewOperationsHandler(service *operations.Service, sessions *tablesession.Service, identity IdentityResolver, authorizer authz.Authorizer) http.Handler {
	if identity == nil {
		identity = DenyIdentity{}
	}
	if authorizer == nil {
		authorizer = authz.DenyAll{}
	}
	a := OperationsAPI{service, sessions, identity, authorizer}
	m := http.NewServeMux()
	const base = "/v1/staff/tenants/{tenantID}/stores/{storeID}"
	const session = base + "/tables/{tableID}/sessions/{sessionID}"
	m.HandleFunc("GET "+base+"/operations/access", a.access)
	m.HandleFunc("GET "+base+"/operations/tables", a.tables)
	m.HandleFunc("GET "+base+"/operations/kds", a.kds)
	m.HandleFunc("GET "+base+"/operations/reviews", a.reviews)
	m.HandleFunc("GET "+base+"/operations/cash-settings", a.settings)
	m.HandleFunc("PUT "+base+"/operations/cash-settings", a.setSettings)
	m.HandleFunc("GET "+session+"/operations", a.detail)
	m.HandleFunc("POST "+session+"/cash/preview", a.cashPreview)
	m.HandleFunc("POST "+session+"/cash/settle", a.settle)
	m.HandleFunc("POST "+session+"/orders/{orderID}/accept", a.accept)
	m.HandleFunc("POST "+session+"/orders/{orderID}/approve-fulfillment", a.approveFulfillment)
	m.HandleFunc("POST "+session+"/orders/{orderID}/fulfillment", a.fulfillment)
	m.HandleFunc("GET /v1/guest/sessions/{sessionID}/operations", a.guest)
	return noStore(m)
}

// NewOperationsRuntime is explicit composition for an accepted adapter or the
// isolated local test command. The production health server never calls it.
func NewOperationsRuntime(sessions *tablesession.Service, menus *catalog.Service, orders *ordering.Service, ops *operations.Service, identity IdentityResolver, authorizer authz.Authorizer) http.Handler {
	m := http.NewServeMux()
	o := NewOperationsHandler(ops, sessions, identity, authorizer)
	const base = "/v1/staff/tenants/{tenantID}/stores/{storeID}"
	const session = base + "/tables/{tableID}/sessions/{sessionID}"
	m.Handle(base+"/operations/", o)
	m.Handle(session+"/operations", o)
	m.Handle(session+"/cash/", o)
	m.Handle(session+"/orders/", o)
	m.Handle("/v1/guest/sessions/{sessionID}/operations", o)
	m.Handle("/v1/staff/", NewSessionHandler(sessions, identity, authorizer))
	m.Handle("/v1/guest/", NewGuestHandler(sessions, menus, orders))
	return noStore(m)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (a OperationsAPI) authorize(w http.ResponseWriter, r *http.Request, permission string, roles []authz.Role) (operations.StoreScope, authz.Actor, bool) {
	s, err := a.Sessions.StoreScope(r.Context(), r.PathValue("tenantID"), r.PathValue("storeID"))
	if err != nil {
		respond(w, nil, err)
		return operations.StoreScope{}, authz.Actor{}, false
	}
	actor, err := a.Identity.Resolve(r.Context(), r)
	if err != nil {
		writeError(w, 403, "forbidden")
		return operations.StoreScope{}, authz.Actor{}, false
	}
	if err = a.Authorizer.Authorize(r.Context(), authz.Request{Actor: actor, Permission: authz.Permission(permission), Resource: authz.Scope{TenantID: s.TenantID, StoreID: s.StoreID}, AllowedRoles: roles, MFARequiredRoles: manageRoles}); err != nil {
		writeError(w, 403, "forbidden")
		return operations.StoreScope{}, authz.Actor{}, false
	}
	return operations.StoreScope{TenantID: s.TenantID, StoreID: s.StoreID}, actor, true
}

func (a OperationsAPI) tableAuthorize(w http.ResponseWriter, r *http.Request, permission string, roles []authz.Role) (operations.TableScope, authz.Actor, bool) {
	s, actor, ok := a.authorize(w, r, permission, roles)
	if !ok {
		return operations.TableScope{}, actor, false
	}
	t, err := a.Sessions.Table(r.Context(), tablesession.Scope{TenantID: s.TenantID, StoreID: s.StoreID, TableID: r.PathValue("tableID")})
	if err != nil {
		respond(w, nil, err)
		return operations.TableScope{}, actor, false
	}
	if !t.Enabled || !t.StoreEnabled || !t.TenantEnabled || !t.Operating {
		writeError(w, 403, "forbidden")
		return operations.TableScope{}, actor, false
	}
	return operations.TableScope{TenantID: t.Scope.TenantID, StoreID: t.Scope.StoreID, TableID: t.Scope.TableID}, actor, true
}

func (a OperationsAPI) access(w http.ResponseWriter, r *http.Request) {
	s, actor, ok := a.authorize(w, r, "operations.read", operationReadRoles)
	if !ok {
		return
	}
	allows := func(roles []authz.Role) bool {
		return a.Authorizer.Authorize(r.Context(), authz.Request{Actor: actor, Permission: "operations.access", Resource: authz.Scope{TenantID: s.TenantID, StoreID: s.StoreID}, AllowedRoles: roles, MFARequiredRoles: manageRoles}) == nil
	}
	writeJSON(w, 200, map[string]bool{"can_cash": allows(cashRoles), "can_serve": allows(cashRoles), "can_kitchen": allows([]authz.Role{authz.Kitchen}), "can_manage": allows(manageRoles), "can_approve_fulfillment": allows(fulfillmentApprovalRoles)})
}

func operationBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := decodeJSON(w, r, dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "request_too_large")
		} else {
			writeError(w, 400, "invalid_request")
		}
		return false
	}
	return true
}

// Canonical decimal JPY prevents exponent/fraction/coercion and JS precision
// loss. Tendered amounts are still verified against server-calculated due.
func parseJPY(v string) (int64, error) {
	if v == "" || len(v) > 19 || (len(v) > 1 && v[0] == '0') {
		return 0, operations.ErrInvalid
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, operations.ErrInvalid
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, operations.ErrInvalid
	}
	return n, nil
}

func (a OperationsAPI) tables(w http.ResponseWriter, r *http.Request) {
	s, _, ok := a.authorize(w, r, "operations.tables.read", operationReadRoles)
	if !ok {
		return
	}
	v, err := a.Operations.ListTables(r.Context(), s)
	if v == nil {
		v = []operations.TableSummary{}
	}
	operationRespond(w, map[string]any{"tables": v}, err)
}
func (a OperationsAPI) kds(w http.ResponseWriter, r *http.Request) {
	s, _, ok := a.authorize(w, r, "operations.kds.read", []authz.Role{authz.Kitchen})
	if !ok {
		return
	}
	v, err := a.Operations.ListKDS(r.Context(), s)
	if v == nil {
		v = []operations.OrderState{}
	}
	operationRespond(w, map[string]any{"orders": v}, err)
}

// The store-wide queue includes historical sessions: a late payment can need
// review after expiry/close, when it no longer appears in the active table list.
func (a OperationsAPI) reviews(w http.ResponseWriter, r *http.Request) {
	scope, _, ok := a.authorize(w, r, "operations.reviews.read", operationReadRoles)
	if !ok {
		return
	}
	orders, err := a.Operations.ListPaymentReviews(r.Context(), scope)
	if orders == nil {
		orders = []operations.OrderState{}
	}
	operationRespond(w, map[string]any{"orders": orders}, err)
}

func (a OperationsAPI) approveFulfillment(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.tableAuthorize(w, r, "operations.order.approve_fulfillment", fulfillmentApprovalRoles)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	result, err := a.Operations.ApproveFulfillment(r.Context(), operations.OrderActionRequest{
		Scope: scope, SessionID: r.PathValue("sessionID"), OrderID: r.PathValue("orderID"),
		Actor: actor.SubjectID, ExpectedVersion: body.ExpectedVersion, Reason: body.Reason,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	// Distinguish stale consent from an uncertain request or a current HQ hold.
	switch {
	case errors.Is(err, operations.ErrConflict):
		writeError(w, 409, "stale_version")
	case errors.Is(err, operations.ErrIdempotencyConflict):
		writeError(w, 409, "idempotency_conflict")
	case errors.Is(err, operations.ErrCompanyHoldActive):
		writeError(w, 409, "company_hold_active")
	case errors.Is(err, operations.ErrReviewNotReady):
		writeError(w, 409, "review_not_ready")
	default:
		operationRespond(w, result, err)
	}
}

func (a OperationsAPI) settings(w http.ResponseWriter, r *http.Request) {
	s, _, ok := a.authorize(w, r, "operations.cash_settings.read", operationReadRoles)
	if !ok {
		return
	}
	v, err := a.Operations.ReadCashSettings(r.Context(), s)
	operationRespond(w, v, err)
}
func (a OperationsAPI) setSettings(w http.ResponseWriter, r *http.Request) {
	s, actor, ok := a.authorize(w, r, "operations.cash_settings.write", manageRoles)
	if !ok {
		return
	}
	var body struct {
		Enabled         bool                `json:"enabled"`
		Mode            operations.CashMode `json:"mode"`
		ExpectedVersion int64               `json:"expected_version"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	v, err := a.Operations.SetCashSettings(r.Context(), s, actor.SubjectID, body.Enabled, body.Mode, body.ExpectedVersion)
	operationRespond(w, v, err)
}
func (a OperationsAPI) detail(w http.ResponseWriter, r *http.Request) {
	s, _, ok := a.tableAuthorize(w, r, "operations.session.read", operationReadRoles)
	if !ok {
		return
	}
	v, err := a.Operations.ReadSession(r.Context(), s, r.PathValue("sessionID"))
	if err != nil {
		operationRespond(w, nil, err)
		return
	}
	receipts := make([]any, 0, len(v.Receipts))
	for _, r := range v.Receipts {
		receipts = append(receipts, receiptProjection(r))
	}
	if v.Orders == nil {
		v.Orders = []operations.OrderState{}
	}
	if v.CloseBlockers == nil {
		v.CloseBlockers = []string{}
	}
	operationRespond(w, map[string]any{"session": map[string]any{"id": v.SessionID, "status": v.Status, "mode": v.Mode, "expires_at": v.ExpiresAt}, "orders": v.Orders, "receipts": receipts, "close_blockers": v.CloseBlockers}, nil)
}
func (a OperationsAPI) cashPreview(w http.ResponseWriter, r *http.Request) {
	s, _, ok := a.tableAuthorize(w, r, "operations.cash.preview", cashRoles)
	if !ok {
		return
	}
	if !operationBody(w, r, &struct{}{}) {
		return
	}
	v, err := a.Operations.PreviewCash(r.Context(), s, r.PathValue("sessionID"))
	operationRespond(w, map[string]any{"session_id": v.SessionID, "fingerprint": v.Fingerprint, "order_ids": v.OrderIDs, "due_jpy": v.DueJPY, "tax": invoiceProjection(v.Tax)}, err)
}
func (a OperationsAPI) settle(w http.ResponseWriter, r *http.Request) {
	s, actor, ok := a.tableAuthorize(w, r, "operations.cash.settle", cashRoles)
	if !ok {
		return
	}
	var body struct {
		Fingerprint        string `json:"fingerprint"`
		TenderedJPY        string `json:"tendered_jpy"`
		RegisterSessionRef string `json:"register_session_ref"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	tendered, err := parseJPY(body.TenderedJPY)
	if err != nil {
		operationRespond(w, nil, err)
		return
	}
	v, err := a.Operations.SettleCash(r.Context(), operations.CashSettlementRequest{Scope: s, SessionID: r.PathValue("sessionID"), Actor: actor.SubjectID, Fingerprint: body.Fingerprint, TenderedJPY: tendered, RegisterSessionRef: body.RegisterSessionRef, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	operationRespond(w, receiptProjection(v), err)
}
func (a OperationsAPI) accept(w http.ResponseWriter, r *http.Request) {
	s, actor, ok := a.tableAuthorize(w, r, "operations.order.accept", cashRoles)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	v, err := a.Operations.AcceptOrder(r.Context(), operations.OrderActionRequest{Scope: s, SessionID: r.PathValue("sessionID"), OrderID: r.PathValue("orderID"), Actor: actor.SubjectID, ExpectedVersion: body.ExpectedVersion, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	operationRespond(w, v, err)
}
func (a OperationsAPI) fulfillment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int64                        `json:"expected_version"`
		Target          operations.FulfillmentStatus `json:"target"`
	}
	if !operationBody(w, r, &body) {
		return
	}
	var roles []authz.Role
	switch body.Target {
	case operations.Preparing, operations.Ready:
		roles = []authz.Role{authz.Kitchen}
	case operations.Served:
		roles = cashRoles
	default:
		writeError(w, 400, "invalid_request")
		return
	}
	s, actor, ok := a.tableAuthorize(w, r, "operations.order."+string(body.Target), roles)
	if !ok {
		return
	}
	v, err := a.Operations.AdvanceFulfillment(r.Context(), operations.OrderActionRequest{Scope: s, SessionID: r.PathValue("sessionID"), OrderID: r.PathValue("orderID"), Actor: actor.SubjectID, ExpectedVersion: body.ExpectedVersion, IdempotencyKey: r.Header.Get("Idempotency-Key"), Target: body.Target})
	operationRespond(w, v, err)
}
func (a OperationsAPI) guest(w http.ResponseWriter, r *http.Request) {
	v, err := a.Operations.ReadGuestOperations(r.Context(), r.PathValue("sessionID"), joinToken(r))
	if v.Orders == nil {
		v.Orders = []operations.GuestOrderState{}
	}
	operationRespond(w, v, err)
}

func invoiceProjection(v tax.InvoiceRecord) any {
	groups := make([]any, 0, len(v.Groups))
	for _, g := range v.Groups {
		groups = append(groups, map[string]any{"rate_numerator": strconv.FormatInt(g.RateNumerator, 10), "rate_denominator": strconv.FormatInt(g.RateDenominator, 10), "gross_jpy": g.Totals.GrossJPY, "net_jpy": g.Totals.NetJPY, "tax_jpy": g.Totals.TaxJPY})
	}
	return map[string]any{"gross_jpy": v.Total.GrossJPY, "net_jpy": v.Total.NetJPY, "tax_jpy": v.Total.TaxJPY, "groups": groups}
}
func receiptProjection(v operations.Receipt) any {
	return map[string]any{"id": v.ID, "session_id": v.SessionID, "order_ids": v.OrderIDs, "due_jpy": v.DueJPY, "tendered_jpy": v.TenderedJPY, "change_jpy": v.ChangeJPY, "cashier_id": v.CashierID, "register_session_ref": v.RegisterSessionRef, "created_at": v.CreatedAt, "tax": invoiceProjection(v.Tax)}
}
func operationRespond(w http.ResponseWriter, v any, err error) {
	if err == nil {
		writeMoneyJSON(w, 200, v)
		return
	}
	switch {
	case errors.Is(err, operations.ErrInvalid), errors.Is(err, tax.ErrInvalidInput), errors.Is(err, tax.ErrOverflow):
		writeError(w, 400, "invalid_request")
	case errors.Is(err, operations.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, operations.ErrDenied):
		writeError(w, 403, "forbidden")
	case errors.Is(err, operations.ErrReconfirm):
		writeError(w, 409, "reconfirm")
	case errors.Is(err, operations.ErrCashUnavailable):
		writeError(w, 409, "cash_unavailable")
	case errors.Is(err, operations.ErrConflict), errors.Is(err, operations.ErrIdempotencyConflict):
		writeError(w, 409, "conflict")
	case errors.Is(err, operations.ErrBlocked), errors.Is(err, operations.ErrNotReady):
		writeError(w, 409, "blocked")
	default:
		respond(w, nil, err)
	}
}
