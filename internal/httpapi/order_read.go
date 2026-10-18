package httpapi

import (
	"errors"
	"net/http"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
)

func orderReadHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (a OrderingAPI) orderStatus(w http.ResponseWriter, r *http.Request) {
	orderReadHeaders(w)
	// Query credentials are never supported, including alongside a valid header.
	if r.URL.RawQuery != "" {
		writeError(w, 403, "forbidden")
		return
	}
	status, err := a.Orders.ReadOrderStatus(r.Context(), r.PathValue("orderID"), joinToken(r))
	writeOrderRead(w, status, err)
}

func (a OrderingAPI) requestOrderRead(w http.ResponseWriter, r *http.Request) {
	orderReadHeaders(w)
	if r.URL.RawQuery != "" {
		writeError(w, 403, "forbidden")
		return
	}
	if !checkEmptyPaymentRequest(w, r) {
		return
	}
	grant, err := a.Orders.RequestOrderRead(r.Context(), r.PathValue("sessionID"), joinToken(r), r.PathValue("orderID"))
	writeOrderRead(w, grant, err)
}

func writeOrderRead(w http.ResponseWriter, value any, err error) {
	if errors.Is(err, ordering.ErrDenied) {
		writeError(w, 403, "forbidden")
		return
	}
	if err != nil {
		writeError(w, 503, "status_unavailable")
		return
	}
	writeMoneyJSON(w, http.StatusOK, value)
}
