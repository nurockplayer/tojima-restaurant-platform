package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// IdentityResolver must use MOD-08 to verify the subject, verified MFA state,
// live staff-session revocation, absolute session expiry, and platform security
// holds before returning an Actor. A nil/default resolver always denies. The
// handler never trusts body fields or arbitrary identity headers as an actor;
// this is a port for the shared IdP adapter, not a new authentication system.
type IdentityResolver interface {
	Resolve(context.Context, *http.Request) (authz.Actor, error)
}
type DenyIdentity struct{}

func (DenyIdentity) Resolve(context.Context, *http.Request) (authz.Actor, error) {
	return authz.Actor{}, authz.ErrDenied
}

type SessionAPI struct {
	Sessions   *tablesession.Service
	Identity   IdentityResolver
	Authorizer authz.Authorizer
}

type apiError struct {
	Error string `json:"error"`
}
type pinRequest struct {
	PIN               string `json:"pin"`
	ExpectedSessionID string `json:"expected_session_id,omitempty"`
}
type directJoinRequest struct {
	ExpectedSessionID string `json:"expected_session_id,omitempty"`
}
type modeRequest struct {
	Mode tablesession.Mode `json:"mode"`
}

// NewSessionHandler exposes the P-76 API through explicit identity and current-
// membership adapters. Nil adapters deny staff actions; guest grants stay separate.
func NewSessionHandler(service *tablesession.Service, identity IdentityResolver, authorizer authz.Authorizer) http.Handler {
	if identity == nil {
		identity = DenyIdentity{}
	}
	if authorizer == nil {
		authorizer = authz.DenyAll{}
	}
	api := SessionAPI{Sessions: service, Identity: identity, Authorizer: authorizer}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/guest/tables/{locator}", api.publicStatus)
	mux.HandleFunc("POST /v1/guest/tables/{locator}/join", api.directJoin)
	mux.HandleFunc("POST /v1/guest/sessions/{sessionID}/join", api.sessionJoin)
	mux.HandleFunc("GET /v1/guest/sessions/{sessionID}", api.guestRead)
	mux.HandleFunc("POST /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions", api.open)
	mux.HandleFunc("GET /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions/{sessionID}", api.staffRead)
	mux.HandleFunc("POST /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions/{sessionID}/close", api.close)
	mux.HandleFunc("POST /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions/{sessionID}/pin", api.resetPIN)
	mux.HandleFunc("PUT /v1/staff/tenants/{tenantID}/stores/{storeID}/qr-mode", api.setMode)
	return mux
}

func (a SessionAPI) publicStatus(w http.ResponseWriter, r *http.Request) {
	result, err := a.Sessions.PublicStatus(r.Context(), r.PathValue("locator"))
	respond(w, result, err)
}
func (a SessionAPI) directJoin(w http.ResponseWriter, r *http.Request) {
	var input directJoinRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	grant, err := a.Sessions.DirectJoin(r.Context(), r.PathValue("locator"), input.ExpectedSessionID)
	respond(w, grant, err)
}
func (a SessionAPI) sessionJoin(w http.ResponseWriter, r *http.Request) {
	var input pinRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	grant, err := a.Sessions.Join(r.Context(), r.PathValue("sessionID"), input.ExpectedSessionID, input.PIN)
	respond(w, grant, err)
}
func (a SessionAPI) guestRead(w http.ResponseWriter, r *http.Request) {
	session, err := a.Sessions.Read(r.Context(), r.PathValue("sessionID"), tablesession.ValidateBearer(r.Header.Get("Authorization")))
	respond(w, session, err)
}
func (a SessionAPI) open(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.staffScope(w, r, authz.Permission("table_session.open"), []authz.Role{authz.Staff, authz.StoreManager}, []authz.Role{authz.StoreManager})
	if !ok {
		return
	}
	opened, err := a.Sessions.Open(r.Context(), scope, actor.SubjectID)
	respond(w, opened, err)
}
func (a SessionAPI) staffRead(w http.ResponseWriter, r *http.Request) {
	scope, _, ok := a.staffScope(w, r, authz.Permission("table_session.read"), []authz.Role{authz.Staff, authz.StoreManager, authz.Kitchen, authz.TenantAdmin}, []authz.Role{authz.StoreManager, authz.TenantAdmin})
	if !ok {
		return
	}
	session, err := a.Sessions.ReadStaff(r.Context(), scope, r.PathValue("sessionID"))
	respond(w, session, err)
}
func (a SessionAPI) close(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.staffScope(w, r, authz.Permission("table_session.close"), []authz.Role{authz.Staff, authz.StoreManager}, []authz.Role{authz.StoreManager})
	if !ok {
		return
	}
	err := a.Sessions.Close(r.Context(), scope, r.PathValue("sessionID"), actor.SubjectID)
	respond(w, map[string]bool{"closed": err == nil}, err)
}
func (a SessionAPI) resetPIN(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.staffScope(w, r, authz.Permission("table_session.pin.reset"), []authz.Role{authz.Staff, authz.StoreManager}, []authz.Role{authz.StoreManager})
	if !ok {
		return
	}
	result, err := a.Sessions.ResetPIN(r.Context(), scope, r.PathValue("sessionID"), actor.SubjectID)
	respond(w, result, err)
}
func (a SessionAPI) setMode(w http.ResponseWriter, r *http.Request) {
	tenantID, storeID := r.PathValue("tenantID"), r.PathValue("storeID")
	scope, err := a.Sessions.StoreScope(r.Context(), tenantID, storeID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	actor, err := a.Identity.Resolve(r.Context(), r)
	if err != nil {
		respond(w, nil, authz.ErrDenied)
		return
	}
	request := authz.Request{Actor: actor, Permission: authz.Permission("store.qr_mode.write"), Resource: authz.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID}, AllowedRoles: []authz.Role{authz.StoreManager, authz.TenantAdmin}, MFARequiredRoles: []authz.Role{authz.StoreManager, authz.TenantAdmin}}
	if err = a.Authorizer.Authorize(r.Context(), request); err != nil {
		respond(w, nil, authz.ErrDenied)
		return
	}
	var input modeRequest
	if err = decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err = a.Sessions.SetStoreMode(r.Context(), tenantID, storeID, input.Mode); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, map[string]bool{"updated": true}, nil)
}

func (a SessionAPI) staffScope(w http.ResponseWriter, r *http.Request, permission authz.Permission, allowed, mfaRoles []authz.Role) (tablesession.Scope, authz.Actor, bool) {
	scope := tablesession.Scope{TenantID: r.PathValue("tenantID"), StoreID: r.PathValue("storeID"), TableID: r.PathValue("tableID")}
	table, err := a.Sessions.Table(r.Context(), scope)
	if err != nil {
		respond(w, nil, err)
		return tablesession.Scope{}, authz.Actor{}, false
	}
	if table.Scope != scope {
		respond(w, nil, tablesession.ErrNotFound)
		return tablesession.Scope{}, authz.Actor{}, false
	}
	if !table.Enabled || !table.StoreEnabled || !table.TenantEnabled || !table.Operating {
		respond(w, nil, tablesession.ErrDenied)
		return tablesession.Scope{}, authz.Actor{}, false
	}
	actor, err := a.Identity.Resolve(r.Context(), r)
	if err != nil {
		respond(w, nil, authz.ErrDenied)
		return tablesession.Scope{}, authz.Actor{}, false
	}
	request := authz.Request{Actor: actor, Permission: permission, Resource: authz.Scope{TenantID: table.Scope.TenantID, StoreID: table.Scope.StoreID}, AllowedRoles: allowed, MFARequiredRoles: mfaRoles}
	if err = a.Authorizer.Authorize(r.Context(), request); err != nil {
		respond(w, nil, authz.ErrDenied)
		return tablesession.Scope{}, authz.Actor{}, false
	}
	return scope, actor, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("only one JSON value is allowed")
	}
	return nil
}
func respond(w http.ResponseWriter, result any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, tablesession.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, tablesession.ErrDenied), errors.Is(err, authz.ErrDenied):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, tablesession.ErrConflict), errors.Is(err, tablesession.ErrCloseBlocked):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, tablesession.ErrExpired):
		status, code = http.StatusGone, "expired"
	case errors.Is(err, tablesession.ErrInvalidPIN):
		status, code = http.StatusUnauthorized, "invalid_pin"
	case errors.Is(err, tablesession.ErrPINLocked), errors.Is(err, tablesession.ErrRateLimited):
		status, code = http.StatusTooManyRequests, "pin_locked"
	}
	writeError(w, status, code)
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, apiError{Error: code})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
