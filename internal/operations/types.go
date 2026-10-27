// Package operations owns cash settlement, order fulfillment, KDS release,
// staff table summaries, and close gating for accepted table orders.
package operations

import (
	"errors"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
)

var (
	ErrNotFound            = errors.New("operation resource not found")
	ErrDenied              = errors.New("operation access denied")
	ErrInvalid             = errors.New("operation request is invalid")
	ErrConflict            = errors.New("operation version conflict")
	ErrReconfirm           = errors.New("cash order set changed; preview again")
	ErrIdempotencyConflict = errors.New("operation idempotency key conflict")
	ErrBlocked             = errors.New("operation is blocked")
	ErrCashUnavailable     = errors.New("cash acceptance is disabled")
	ErrNotReady            = errors.New("order is not ready for this operation")
	ErrCompanyHoldActive   = errors.New("company product hold is active")
	ErrReviewNotReady      = errors.New("order is not ready for review approval")
)

type StoreScope struct {
	TenantID string
	StoreID  string
}

type TableScope struct {
	TenantID string
	StoreID  string
	TableID  string
}

type CashMode string

const (
	Prepaid  CashMode = "PREPAID"
	PayLater CashMode = "PAY_LATER"
)

type CashSettings struct {
	Enabled bool     `json:"enabled"`
	Mode    CashMode `json:"mode"`
	Version int64    `json:"version"`
}

type TableSummary struct {
	TableID       string              `json:"table_id"`
	Name          string              `json:"name"`
	SessionID     string              `json:"session_id,omitempty"`
	Status        tablesession.Status `json:"status"`
	Mode          tablesession.Mode   `json:"mode,omitempty"`
	ExpiresAt     *time.Time          `json:"expires_at,omitempty"`
	UnpaidCashJPY int64               `json:"unpaid_cash_jpy"`
	OrderCount    int                 `json:"order_count"`
}

type PaymentStatus string

const (
	Unpaid        PaymentStatus = "UNPAID"
	Pending       PaymentStatus = "PENDING"
	Paid          PaymentStatus = "PAID"
	Unknown       PaymentStatus = "UNKNOWN"
	RefundPending PaymentStatus = "REFUND_PENDING"
	Refunded      PaymentStatus = "REFUNDED"
	Voided        PaymentStatus = "VOIDED"
)

type FulfillmentStatus string

const (
	Waiting   FulfillmentStatus = "WAITING"
	Accepted  FulfillmentStatus = "ACCEPTED"
	Preparing FulfillmentStatus = "PREPARING"
	Ready     FulfillmentStatus = "READY"
	Served    FulfillmentStatus = "SERVED"
	Cancelled FulfillmentStatus = "CANCELLED"
)

type OrderState struct {
	CancellationRequests    []CancellationRequest `json:"cancellation_requests"`
	ID                      string                `json:"id"`
	SessionID               string                `json:"session_id"`
	TableID                 string                `json:"table_id"`
	TableName               string                `json:"table_name"`
	Generation              int64                 `json:"generation"`
	PaymentMethod           string                `json:"payment_method"`
	PaymentStatus           PaymentStatus         `json:"payment_status"`
	FulfillmentStatus       FulfillmentStatus     `json:"fulfillment_status"`
	RequiresStaffAcceptance bool                  `json:"requires_staff_acceptance"`
	StaffAccepted           bool                  `json:"staff_accepted"`
	CashMode                CashMode              `json:"cash_mode,omitempty"`
	OperationalHold         bool                  `json:"operational_hold"`
	PaymentReviewRequired   bool                  `json:"payment_review_required"`
	ReviewDecision          string                `json:"review_decision"`
	ReviewReasonCodes       []string              `json:"review_reason_codes"`
	Version                 int64                 `json:"version"`
	EffectiveTotalGrossJPY  int64                 `json:"effective_total_gross_jpy"`
	OrderUpdated            bool                  `json:"order_updated"`
	Refunds                 []Refund              `json:"refunds"`
	TotalGrossJPY           int64                 `json:"total_gross_jpy"`
	CreatedAt               time.Time             `json:"created_at"`
	Lines                   []OrderLine           `json:"lines"`
	BlockedReason           string                `json:"blocked_reason,omitempty"`
}

type OrderLine struct {
	StopRequestEligibility StopRequestEligibility `json:"stop_request_eligibility"`
	LineIndex              int                    `json:"line_index"`
	OriginalQuantity       int64                  `json:"original_quantity"`
	ProductName            string                 `json:"product_name"`
	Quantity               int64                  `json:"quantity"`
	UnitGrossJPY           int64                  `json:"unit_gross_jpy"`
	LineGrossJPY           int64                  `json:"line_gross_jpy"`
	Options                []OrderOption          `json:"options"`
}

type OrderOption struct {
	GroupName       string `json:"group_name"`
	OptionName      string `json:"option_name"`
	PriceDeltaMinor int64  `json:"price_delta_minor"`
}

type Receipt struct {
	ID                 string            `json:"id"`
	SessionID          string            `json:"session_id"`
	OrderIDs           []string          `json:"order_ids"`
	DueJPY             int64             `json:"due_jpy"`
	TenderedJPY        int64             `json:"tendered_jpy"`
	ChangeJPY          int64             `json:"change_jpy"`
	CashierID          string            `json:"cashier_id"`
	RegisterSessionRef string            `json:"register_session_ref"`
	CreatedAt          time.Time         `json:"created_at"`
	Tax                tax.InvoiceRecord `json:"tax"`
}

type CashPreview struct {
	SessionID   string            `json:"session_id"`
	Fingerprint string            `json:"fingerprint"`
	OrderIDs    []string          `json:"order_ids"`
	DueJPY      int64             `json:"due_jpy"`
	Tax         tax.InvoiceRecord `json:"tax"`
}

type CashSettlementRequest struct {
	Scope              TableScope
	SessionID          string
	Actor              string
	Fingerprint        string
	TenderedJPY        int64
	RegisterSessionRef string
	IdempotencyKey     string
}

type OrderActionRequest struct {
	Scope           TableScope
	SessionID       string
	OrderID         string
	Actor           string
	ExpectedVersion int64
	IdempotencyKey  string
	Target          FulfillmentStatus
	Reason          string
}

type SessionDetail struct {
	SessionID     string              `json:"session_id"`
	Status        tablesession.Status `json:"status"`
	Mode          tablesession.Mode   `json:"mode"`
	ExpiresAt     time.Time           `json:"expires_at"`
	Orders        []OrderState        `json:"orders"`
	Receipts      []Receipt           `json:"receipts"`
	CloseBlockers []string            `json:"close_blockers"`
}

type GuestOrderState struct {
	ordering.GuestOrderDetails
	ID                      string            `json:"id"`
	PaymentStatus           PaymentStatus     `json:"payment_status"`
	FulfillmentStatus       FulfillmentStatus `json:"fulfillment_status"`
	DisplayState            string            `json:"display_state"`
	RequiresStaffAcceptance bool              `json:"requires_staff_acceptance"`
	StaffAccepted           bool              `json:"staff_accepted"`
	CashMode                CashMode          `json:"cash_mode,omitempty"`
}

type GuestOperations struct {
	Orders      []GuestOrderState `json:"orders"`
	CashEnabled bool              `json:"cash_enabled"`
	CardEnabled bool              `json:"card_enabled"`
}

type OutboxEvent struct {
	ID        int64     `json:"id"`
	OrderID   string    `json:"order_id"`
	EventType string    `json:"event_type"`
	CreatedAt time.Time `json:"created_at"`
	Attempts  int       `json:"attempts"`
}

// Order action actors are supplied by the verified MOD-08 HTTP adapter. The
// service still verifies the order and current store/session state in PostgreSQL.
