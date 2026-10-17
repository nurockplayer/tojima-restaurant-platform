// Package ordering owns one shared, server-authoritative cart and immutable
// orders for each P-76 table session. It does not execute payment or KDS work.
package ordering

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
)

const (
	// These bounded MVP limits cap validation, quote, and snapshot work. They are
	// implementation safety limits, not restaurant policy.
	MaxCartLines    = 100
	MaxLineQuantity = 99
	// Catalog publication admits at most 5,000 options in aggregate; this
	// selection limit allows every published option while bounding cart work.
	MaxOptionsPerLine = 5000
	// Across all lines, keep the persisted cart at the same bounded option cap.
	MaxCartOptions       = 5000
	MaxIdempotencyKeyLen = 128
)

type PaymentMethod string

const (
	Stripe PaymentMethod = "STRIPE"
	Cash   PaymentMethod = "CASH"
)

type MutationKind string

const (
	AddLine    MutationKind = "ADD"
	SetLine    MutationKind = "SET"
	RemoveLine MutationKind = "REMOVE"
)

var (
	ErrNotFound            = errors.New("cart or order not found")
	ErrDenied              = errors.New("cart access denied")
	ErrConflict            = errors.New("cart version conflict")
	ErrRequote             = errors.New("cart must be requoted and confirmed")
	ErrInvalid             = errors.New("cart request is invalid")
	ErrIdempotencyConflict = errors.New("idempotency key was used for a different request")
	ErrUnavailable         = errors.New("one or more cart lines are unavailable")
	ErrEmptyCart           = errors.New("cannot commit an empty cart")
	ErrPaymentUnavailable  = errors.New("payment method is unavailable")
)

// OrderCommitPolicy initializes payment and fulfillment state in the same
// transaction that persists the immutable order.
type OrderCommitPolicy interface {
	InitOrderTx(context.Context, pgx.Tx, CommittedOrder) error
}

// CommittedOrder contains server-derived immutable values only.
type CommittedOrder struct {
	ID                      string
	SessionID               string
	Scope                   tablesession.Scope
	Generation              int64
	PaymentMethod           string
	RequiresStaffAcceptance bool
	TotalGrossJPY           int64
	Tax                     tax.SnapshotRecord
}

// Cart contains only mutable customer selections. It carries no client scope,
// price, or tax fields; every returned value is revalidated from server state.
type Cart struct {
	SessionID  string     `json:"session_id"`
	Generation int64      `json:"generation"`
	Version    int64      `json:"version"`
	Lines      []CartLine `json:"lines"`
}

type CartLine struct {
	LineID           string       `json:"line_id"`
	ProductKey       string       `json:"product_key"`
	ProductName      string       `json:"product_name"`
	ProductAvailable bool         `json:"product_available"`
	UnavailableCode  string       `json:"unavailable_code,omitempty"`
	Quantity         int64        `json:"quantity"`
	Options          []CartOption `json:"options"`
}

type CartOption struct {
	GroupKey        string `json:"group_key"`
	GroupName       string `json:"group_name"`
	OptionKey       string `json:"option_key"`
	OptionName      string `json:"option_name"`
	Available       bool   `json:"available"`
	UnavailableCode string `json:"unavailable_code,omitempty"`
}

// Mutation carries the bearer grant for service authorization. HTTP handlers
// should source it exclusively from Authorization, never a JSON field.
type Mutation struct {
	SessionID          string
	GrantToken         string
	ExpectedGeneration int64
	ExpectedVersion    int64
	IdempotencyKey     string
	Kind               MutationKind
	LineID             string
	ProductKey         string
	Quantity           int64
	Options            []catalog.OptionSelection
}

type Preview struct {
	SessionID   string            `json:"session_id"`
	Generation  int64             `json:"generation"`
	Version     int64             `json:"version"`
	Fingerprint string            `json:"fingerprint"`
	Quote       catalog.MenuQuote `json:"quote"`
	LatestCart  *Cart             `json:"latest_cart,omitempty"`
}

type CommitRequest struct {
	SessionID          string
	GrantToken         string
	ExpectedGeneration int64
	ExpectedVersion    int64
	IdempotencyKey     string
	PreviewFingerprint string
	PaymentMethod      PaymentMethod
}

type Order struct {
	ID                      string            `json:"order_id"`
	SessionID               string            `json:"session_id"`
	Generation              int64             `json:"generation"`
	PaymentMethod           PaymentMethod     `json:"payment_method"`
	RequiresStaffAcceptance bool              `json:"requires_staff_acceptance"`
	Quote                   catalog.MenuQuote `json:"quote"`
	CreatedAt               string            `json:"created_at"`
	// CatalogID is private source provenance for audit/reconciliation. The
	// guest-safe projection must omit it.
	CatalogID int64 `json:"-"`
}

type CommitOutcome string

const (
	Committed        CommitOutcome = "COMMITTED"
	AlreadyCommitted CommitOutcome = "ALREADY_COMMITTED"
)

type CommitResult struct {
	Outcome        CommitOutcome `json:"outcome"`
	Order          Order         `json:"order"`
	Cart           Cart          `json:"cart"`
	CurrentPreview *Preview      `json:"current_preview,omitempty"`
}

type storedSnapshot struct {
	CatalogID int64              `json:"catalog_id"`
	Quote     catalog.MenuQuote  `json:"quote"`
	Tax       tax.SnapshotRecord `json:"tax"`
}
