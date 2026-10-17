package httpapi

import (
	"net/http"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// NewGuestHandler composes the existing guest entry/menu capabilities with the
// shared cart. Staff/admin routes are deliberately absent from this surface.
func NewGuestHandler(sessions *tablesession.Service, menus *catalog.Service, orders *ordering.Service, ops ...*operations.Service) http.Handler {
	var opsService *operations.Service
	if len(ops) == 1 {
		opsService = ops[0]
	}
	return newGuestHandler(sessions, menus, orders, opsService, nil)
}

func newGuestHandler(sessions *tablesession.Service, menus *catalog.Service, orders *ordering.Service, ops *operations.Service, paymentConfig *PaymentsHTTPConfig) http.Handler {
	s := NewSessionHandler(sessions, nil, nil)
	m := NewMenuHandler(menus, nil, nil)
	o := newOrderingHandler(orders, sessions, menus, paymentConfig)
	mux := http.NewServeMux()
	mux.Handle("/v1/guest/tables/{locator}", s)
	mux.Handle("/v1/guest/tables/{locator}/join", s)
	mux.Handle("/v1/guest/tables/{locator}/menu", m)
	mux.Handle("/v1/guest/sessions/{sessionID}", s)
	mux.Handle("/v1/guest/sessions/{sessionID}/join", s)
	mux.Handle("/v1/guest/sessions/{sessionID}/menu", o)
	mux.Handle("/v1/guest/sessions/{sessionID}/cart", o)
	mux.Handle("/v1/guest/sessions/{sessionID}/cart/", o)
	mux.Handle("/v1/guest/sessions/{sessionID}/orders", o)
	if ops != nil {
		mux.Handle("/v1/guest/sessions/{sessionID}/operations", NewOperationsHandler(ops, sessions, nil, nil))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}
