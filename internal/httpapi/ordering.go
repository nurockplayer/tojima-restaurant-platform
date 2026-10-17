package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

type OrderingAPI struct {
	Orders   *ordering.Service
	Sessions *tablesession.Service
	Catalog  *catalog.Service
}

// An existing P-77 product may select all 5,000 catalog options. Each key can
// contain 100 non-BMP runes, encoded as 12 bytes per JSON surrogate pair.
// Bound cart edits separately; preview/commit/control requests retain 16 KiB.
const cartEditMaxBodyBytes = int64(12<<20 + 64<<10)

type cartVersionRequest struct {
	Generation int64 `json:"expected_generation"`
	Version    int64 `json:"expected_version"`
}
type cartLineRequest struct {
	cartVersionRequest
	ProductKey string                    `json:"product_key,omitempty"`
	Quantity   int64                     `json:"quantity"`
	Options    []catalog.OptionSelection `json:"options"`
}
type commitCartRequest struct {
	cartVersionRequest
	Fingerprint   string                 `json:"preview_fingerprint"`
	PaymentMethod ordering.PaymentMethod `json:"payment_method"`
}

// NewOrderingHandler exposes guest capabilities only. A TABLE_JOIN grant is
// never accepted from a request body or substituted for staff/payment identity.
func NewOrderingHandler(orders *ordering.Service, sessions *tablesession.Service, menus *catalog.Service) http.Handler {
	a := OrderingAPI{orders, sessions, menus}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/guest/sessions/{sessionID}/menu", a.menu)
	mux.HandleFunc("GET /v1/guest/sessions/{sessionID}/cart", a.cart)
	mux.HandleFunc("POST /v1/guest/sessions/{sessionID}/cart/lines", a.add)
	mux.HandleFunc("PUT /v1/guest/sessions/{sessionID}/cart/lines/{lineID}", a.set)
	mux.HandleFunc("DELETE /v1/guest/sessions/{sessionID}/cart/lines/{lineID}", a.remove)
	mux.HandleFunc("POST /v1/guest/sessions/{sessionID}/cart/preview", a.preview)
	mux.HandleFunc("POST /v1/guest/sessions/{sessionID}/cart/commit", a.commit)
	mux.HandleFunc("GET /v1/guest/sessions/{sessionID}/orders", a.orders)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func joinToken(r *http.Request) string {
	return tablesession.ValidateBearer(r.Header.Get("Authorization"))
}
func (a OrderingAPI) cart(w http.ResponseWriter, r *http.Request) {
	c, err := a.Orders.ReadCart(r.Context(), r.PathValue("sessionID"), joinToken(r))
	a.respond(w, r, c, err)
}
func (a OrderingAPI) menu(w http.ResponseWriter, r *http.Request) {
	s, err := a.Sessions.Read(r.Context(), r.PathValue("sessionID"), joinToken(r))
	if err != nil {
		respond(w, nil, err)
		return
	}
	m, err := a.Catalog.ReadStoreMenu(r.Context(), catalog.StoreScope{TenantID: s.Scope.TenantID, StoreID: s.Scope.StoreID})
	if err != nil {
		respondMenu(w, nil, err)
		return
	}
	writeMoneyJSON(w, http.StatusOK, safeGuestMenu(m))
}
func (a OrderingAPI) add(w http.ResponseWriter, r *http.Request) { a.edit(w, r, ordering.AddLine) }
func (a OrderingAPI) set(w http.ResponseWriter, r *http.Request) { a.edit(w, r, ordering.SetLine) }
func (a OrderingAPI) edit(w http.ResponseWriter, r *http.Request, kind ordering.MutationKind) {
	var body cartLineRequest
	if !decodeCartLimit(w, r, &body, cartEditMaxBodyBytes) {
		return
	}
	if kind == ordering.SetLine && body.ProductKey != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	c, err := a.Orders.Mutate(r.Context(), ordering.Mutation{SessionID: r.PathValue("sessionID"), GrantToken: joinToken(r), ExpectedGeneration: body.Generation, ExpectedVersion: body.Version, IdempotencyKey: r.Header.Get("Idempotency-Key"), Kind: kind, LineID: r.PathValue("lineID"), ProductKey: body.ProductKey, Quantity: body.Quantity, Options: body.Options})
	a.respond(w, r, c, err)
}
func (a OrderingAPI) remove(w http.ResponseWriter, r *http.Request) {
	g, e1 := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
	v, e2 := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
	if e1 != nil || e2 != nil || len(r.URL.Query()["generation"]) != 1 || len(r.URL.Query()["version"]) != 1 {
		writeError(w, 400, "invalid_request")
		return
	}
	c, err := a.Orders.Mutate(r.Context(), ordering.Mutation{SessionID: r.PathValue("sessionID"), GrantToken: joinToken(r), ExpectedGeneration: g, ExpectedVersion: v, IdempotencyKey: r.Header.Get("Idempotency-Key"), Kind: ordering.RemoveLine, LineID: r.PathValue("lineID")})
	a.respond(w, r, c, err)
}
func (a OrderingAPI) preview(w http.ResponseWriter, r *http.Request) {
	var body cartVersionRequest
	if !decodeCart(w, r, &body) {
		return
	}
	p, err := a.Orders.Preview(r.Context(), r.PathValue("sessionID"), joinToken(r), body.Generation, body.Version)
	a.respond(w, r, map[string]any{"session_id": p.SessionID, "generation": p.Generation, "version": p.Version, "fingerprint": p.Fingerprint, "quote": safeQuote(p.Quote)}, err)
}
func (a OrderingAPI) commit(w http.ResponseWriter, r *http.Request) {
	var body commitCartRequest
	if !decodeCart(w, r, &body) {
		return
	}
	c, err := a.Orders.Commit(r.Context(), ordering.CommitRequest{SessionID: r.PathValue("sessionID"), GrantToken: joinToken(r), ExpectedGeneration: body.Generation, ExpectedVersion: body.Version, IdempotencyKey: r.Header.Get("Idempotency-Key"), PreviewFingerprint: body.Fingerprint, PaymentMethod: body.PaymentMethod})
	a.respond(w, r, map[string]any{"outcome": c.Outcome, "order": safeOrder(c.Order), "cart": c.Cart}, err)
}
func (a OrderingAPI) orders(w http.ResponseWriter, r *http.Request) {
	orders, err := a.Orders.ListOrders(r.Context(), r.PathValue("sessionID"), joinToken(r))
	items := make([]any, 0, len(orders))
	for _, o := range orders {
		items = append(items, safeOrder(o))
	}
	a.respond(w, r, map[string]any{"orders": items}, err)
}
func decodeCart(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeCartLimit(w, r, dst, 16<<10)
}

func decodeCartLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	if err := decodeJSONLimit(w, r, dst, limit); err != nil {
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
func (a OrderingAPI) respond(w http.ResponseWriter, r *http.Request, value any, err error) {
	if err == nil {
		writeMoneyJSON(w, 200, value)
		return
	}
	code := ""
	switch {
	case errors.Is(err, ordering.ErrConflict):
		code = "cart_conflict"
	case errors.Is(err, ordering.ErrRequote):
		code = "requote_required"
	case errors.Is(err, ordering.ErrIdempotencyConflict):
		code = "idempotency_conflict"
	case errors.Is(err, ordering.ErrUnavailable):
		code = "unavailable"
	case errors.Is(err, ordering.ErrEmptyCart), errors.Is(err, ordering.ErrInvalid):
		writeError(w, 400, "invalid_request")
		return
	case errors.Is(err, ordering.ErrNotFound):
		writeError(w, 404, "not_found")
		return
	case errors.Is(err, ordering.ErrDenied):
		writeError(w, 403, "forbidden")
		return
	}
	if code != "" {
		latest, readErr := a.Orders.ReadCart(r.Context(), r.PathValue("sessionID"), joinToken(r))
		if readErr != nil {
			respond(w, nil, readErr)
			return
		}
		writeMoneyJSON(w, 409, map[string]any{"error": code, "cart": latest})
		return
	}
	respond(w, nil, err)
}

func safeOrder(o ordering.Order) any {
	return map[string]any{"order_id": o.ID, "session_id": o.SessionID, "generation": o.Generation, "payment_method": o.PaymentMethod, "created_at": o.CreatedAt, "requires_staff_acceptance": o.RequiresStaffAcceptance, "quote": safeQuote(o.Quote)}
}
func safeQuote(q catalog.MenuQuote) any {
	total := q.Tax.Total()
	buckets := make([]any, 0, len(q.Tax.Groups()))
	for _, g := range q.Tax.Groups() {
		n, d := g.Rate()
		t := g.Total()
		buckets = append(buckets, map[string]any{"rate_numerator": strconv.FormatInt(n, 10), "rate_denominator": strconv.FormatInt(d, 10), "gross_jpy": t.GrossJPY, "net_jpy": t.NetJPY, "tax_jpy": t.TaxJPY})
	}
	return map[string]any{"lines": q.Lines, "tax": map[string]any{"currency": "JPY", "price_mode": "INCLUSIVE", "total_gross_jpy": total.GrossJPY, "total_net_jpy": total.NetJPY, "total_tax_jpy": total.TaxJPY, "buckets": buckets}}
}

// Convert only amount fields after the safe customer projection. UseNumber
// preserves all 64 bits; JavaScript must never receive an unsafe JSON Number.
func writeMoneyJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	var obj any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err = d.Decode(&obj); err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	var convert func(any)
	convert = func(v any) {
		switch item := v.(type) {
		case map[string]any:
			for k, v := range item {
				if n, ok := v.(json.Number); ok && (strings.HasSuffix(k, "_jpy") || strings.HasSuffix(k, "_minor")) {
					item[k] = n.String()
				} else {
					convert(v)
				}
			}
		case []any:
			for _, v := range item {
				convert(v)
			}
		}
	}
	convert(obj)
	writeJSON(w, status, obj)
}
