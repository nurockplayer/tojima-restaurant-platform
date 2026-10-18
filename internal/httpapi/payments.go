package httpapi

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

const (
	checkoutProofCookieName        = "__Host-tojima_checkout"
	fixtureCheckoutProofCookieName = "tojima_checkout_fixture"
)

var errPaymentsHTTPConfig = errors.New("invalid payments HTTP configuration")

const paymentRequestMaxBytes = int64(16 << 10)

type PaymentsAPI struct {
	Payments   *payments.Service
	Sessions   *tablesession.Service
	Operations OperationsAPI
	Config     PaymentsHTTPConfig
}

// PaymentsHTTPConfig binds guest-cookie and webhook trust to one configured
// origin. Loopback HTTP exists only for the explicitly enabled isolated test
// runtime; production deployments must use HTTPS and the __Host cookie.
type PaymentsHTTPConfig struct {
	Proofs            *checkoutproof.Service
	Origin            string
	AllowLoopbackHTTP bool
	WebhookSecrets    []string
}

func (c PaymentsHTTPConfig) validate() error {
	if c.Proofs == nil || len(c.WebhookSecrets) == 0 {
		return errPaymentsHTTPConfig
	}
	for _, secret := range c.WebhookSecrets {
		if strings.TrimSpace(secret) == "" {
			return errPaymentsHTTPConfig
		}
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u == nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return errPaymentsHTTPConfig
	}
	if c.Origin != strings.TrimSuffix(c.Origin, "/") || u.String() != c.Origin {
		return errPaymentsHTTPConfig
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" || !c.AllowLoopbackHTTP || !literalLoopbackHost(u.Hostname()) {
		return errPaymentsHTTPConfig
	}
	return nil
}

func literalLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c PaymentsHTTPConfig) loopbackHTTP() bool {
	u, err := url.Parse(c.Origin)
	return err == nil && u.Scheme == "http" && c.AllowLoopbackHTTP && literalLoopbackHost(u.Hostname())
}

func (c PaymentsHTTPConfig) originAllowed(origin string) bool {
	return origin != "" && origin == c.Origin
}

func (c PaymentsHTTPConfig) requestHostAllowed(r *http.Request) bool {
	u, err := url.Parse(c.Origin)
	return err == nil && r != nil && r.Host != "" && strings.EqualFold(r.Host, u.Host)
}

func (c PaymentsHTTPConfig) cookieName() string {
	if c.loopbackHTTP() {
		return fixtureCheckoutProofCookieName
	}
	return checkoutProofCookieName
}

func (c PaymentsHTTPConfig) proofCookie(token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     c.cookieName(),
		Value:    token,
		Path:     "/",
		Expires:  expiresAt.UTC(),
		HttpOnly: true,
		Secure:   !c.loopbackHTTP(),
		SameSite: http.SameSiteStrictMode,
	}
}

// NewPaymentsRuntime composes the existing guest, staff, and operations APIs
// with the P80 payment boundary. Production health-only composition remains
// separate; nil identity/authorizer adapters retain the existing deny-default.
func NewPaymentsRuntime(sessions *tablesession.Service, menus *catalog.Service, orders *ordering.Service, ops *operations.Service, pay *payments.Service, identity IdentityResolver, authorizer authz.Authorizer, cfg PaymentsHTTPConfig) (http.Handler, error) {
	if sessions == nil || menus == nil || orders == nil || ops == nil || pay == nil || cfg.validate() != nil {
		return nil, errPaymentsHTTPConfig
	}
	if identity == nil {
		identity = DenyIdentity{}
	}
	if authorizer == nil {
		authorizer = authz.DenyAll{}
	}
	a := PaymentsAPI{Payments: pay, Sessions: sessions, Operations: OperationsAPI{Operations: ops, Sessions: sessions, Identity: identity, Authorizer: authorizer}, Config: cfg}

	guestMux := http.NewServeMux()
	guestMux.HandleFunc("POST /v1/guest/sessions/{sessionID}/checkout-context", a.checkoutContext)
	guestMux.HandleFunc("GET /v1/guest/sessions/{sessionID}/orders/{orderID}/checkout", a.guestCheckoutStatus)
	guestMux.HandleFunc("POST /v1/guest/sessions/{sessionID}/orders/{orderID}/checkout/resume", a.resumeCheckout)
	guestMux.Handle("/v1/guest/", newGuestHandler(sessions, menus, orders, ops, &cfg))

	staffMux := http.NewServeMux()
	staffMux.HandleFunc("GET /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions/{sessionID}/orders/{orderID}/payment", a.staffPaymentStatus)
	staffMux.HandleFunc("POST /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions/{sessionID}/orders/{orderID}/payment/reconcile", a.reconcilePayment)
	operationsHandler := NewOperationsHandler(ops, sessions, identity, authorizer)
	const staffBase = "/v1/staff/tenants/{tenantID}/stores/{storeID}"
	const staffSession = staffBase + "/tables/{tableID}/sessions/{sessionID}"
	staffMux.Handle(staffBase+"/operations/", operationsHandler)
	staffMux.Handle(staffSession+"/operations", operationsHandler)
	staffMux.Handle(staffSession+"/cash/", operationsHandler)
	staffMux.Handle(staffSession+"/orders/", operationsHandler)
	staffMux.Handle("/v1/staff/", NewSessionHandler(sessions, identity, authorizer))

	runtime := http.NewServeMux()
	runtime.Handle("/v1/guest/", guestMux)
	runtime.Handle("/v1/staff/", staffMux)
	runtime.HandleFunc("POST /v1/payments/stripe/webhook", a.webhook)
	runtime.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return noStore(runtime), nil
}

func (a PaymentsAPI) checkoutContext(w http.ResponseWriter, r *http.Request) {
	if !a.Config.originAllowed(r.Header.Get("Origin")) || !a.Config.requestHostAllowed(r) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !checkEmptyPaymentRequest(w, r) {
		return
	}
	sessionID := r.PathValue("sessionID")
	session, err := a.Sessions.Read(r.Context(), sessionID, joinToken(r))
	if err != nil {
		writePaymentError(w, err)
		return
	}
	if session.Status != tablesession.Open || session.Expired {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if cookie, err := r.Cookie(a.Config.cookieName()); err == nil {
		if _, verifyErr := a.Config.Proofs.Verify(cookie.Value, time.Now().UTC()); verifyErr == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	token, proof, err := a.Config.Proofs.Issue(time.Now().UTC())
	if err != nil || token == "" || proof.ExpiresAt.IsZero() {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	http.SetCookie(w, a.Config.proofCookie(token, proof.ExpiresAt))
	w.WriteHeader(http.StatusNoContent)
}

func (a PaymentsAPI) guestCheckoutStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	session, err := a.Sessions.Read(r.Context(), sessionID, joinToken(r))
	if err != nil {
		writePaymentError(w, err)
		return
	}
	state, err := a.Payments.ReadSafeStatus(r.Context(), operationTableScope(session.Scope), sessionID, r.PathValue("orderID"))
	if err != nil {
		writePaymentError(w, err)
		return
	}
	resumeAvailable := false
	if cookie, err := r.Cookie(a.Config.cookieName()); err == nil {
		if proof, verifyErr := a.Config.Proofs.Verify(cookie.Value, time.Now().UTC()); verifyErr == nil && len(proof.Hash) == 32 {
			var hash [32]byte
			copy(hash[:], proof.Hash)
			resumeAvailable, err = a.Payments.CanResume(r.Context(), sessionID, r.PathValue("orderID"), hash)
			if err != nil {
				writePaymentError(w, err)
				return
			}
		}
	}
	writePaymentStatus(w, state, resumeAvailable)
}

func (a PaymentsAPI) resumeCheckout(w http.ResponseWriter, r *http.Request) {
	if !a.Config.originAllowed(r.Header.Get("Origin")) || !a.Config.requestHostAllowed(r) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !checkEmptyPaymentRequest(w, r) {
		return
	}
	cookie, err := r.Cookie(a.Config.cookieName())
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	proof, err := a.Config.Proofs.Verify(cookie.Value, time.Now().UTC())
	if err != nil || len(proof.Hash) != 32 {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var hash [32]byte
	copy(hash[:], proof.Hash)
	checkout, err := a.Payments.Resume(r.Context(), r.PathValue("sessionID"), r.PathValue("orderID"), hash)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	if checkout.URL == "" || checkout.ExpiresAt.IsZero() {
		writeError(w, http.StatusServiceUnavailable, "payment_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, checkout)
}

func (a PaymentsAPI) staffPaymentStatus(w http.ResponseWriter, r *http.Request) {
	scope, _, ok := a.Operations.tableAuthorize(w, r, "payments.read", cashRoles)
	if !ok {
		return
	}
	state, err := a.Payments.ReadSafeStatus(r.Context(), scope, r.PathValue("sessionID"), r.PathValue("orderID"))
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writePaymentStatus(w, state, false)
}

func (a PaymentsAPI) reconcilePayment(w http.ResponseWriter, r *http.Request) {
	if !checkEmptyPaymentRequest(w, r) {
		return
	}
	scope, actor, ok := a.Operations.tableAuthorize(w, r, "payments.reconcile", cashRoles)
	if !ok {
		return
	}
	state, err := a.Payments.Reconcile(r.Context(), scope, r.PathValue("sessionID"), r.PathValue("orderID"), actor.SubjectID)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writePaymentStatus(w, state, false)
}

func (a PaymentsAPI) webhook(w http.ResponseWriter, r *http.Request) {
	raw, err := stripeprovider.ReadWebhookBody(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid_webhook")
		return
	}
	event, err := stripeprovider.VerifyWebhook(raw, r.Header.Get("Stripe-Signature"), a.Config.WebhookSecrets, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_webhook")
		return
	}
	_, err = a.Payments.EnqueueVerifiedWebhook(r.Context(), event)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "webhook_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

func operationTableScope(scope tablesession.Scope) operations.TableScope {
	return operations.TableScope{TenantID: scope.TenantID, StoreID: scope.StoreID, TableID: scope.TableID}
}

func writePaymentStatus(w http.ResponseWriter, state payments.Status, resumeAvailable bool) {
	writeJSON(w, http.StatusOK, map[string]any{
		"order_id": state.OrderID, "state": state.State,
		"checkout_status": state.CheckoutStatus, "payment_status": state.PaymentStatus,
		"provider_expires_at": state.ProviderExpiresAt, "last_reconciled_at": state.LastReconciledAt,
		"payment_review_required": state.PaymentReviewRequired, "resume_available": resumeAvailable,
		"safe_error_code": state.SafeErrorCode,
	})
}

func checkEmptyPaymentRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength > paymentRequestMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, paymentRequestMaxBytes+1))
	if err != nil || len(body) > int(paymentRequestMaxBytes) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return false
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed != "" && trimmed != "{}" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func writePaymentError(w http.ResponseWriter, err error) {
	if err == nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	switch {
	case errors.Is(err, payments.ErrDenied), errors.Is(err, tablesession.ErrDenied), errors.Is(err, authz.ErrDenied):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, payments.ErrNotFound), errors.Is(err, tablesession.ErrNotFound), errors.Is(err, ordering.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, payments.ErrConflict), errors.Is(err, tablesession.ErrConflict), errors.Is(err, payments.ErrProofDenied):
		writeError(w, http.StatusConflict, "payment_unavailable")
	case errors.Is(err, tablesession.ErrExpired):
		writeError(w, http.StatusGone, "expired")
	case errors.Is(err, payments.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, payments.ErrOutcomeUnknown), errors.Is(err, stripeprovider.ErrOutcomeUnknown), errors.Is(err, stripeprovider.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "payment_pending")
	default:
		writeError(w, http.StatusServiceUnavailable, "payment_unavailable")
	}
}
