// Package tablesession owns table entry modes, physical seating sessions, and
// narrowly scoped guest join capabilities. It does not own orders or payments.
package tablesession

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
)

type Mode string

const (
	StaffOpen  Mode = "STAFF_OPEN"
	SessionPIN Mode = "SESSION_PIN"
	DirectScan Mode = "DIRECT_SCAN"
)

type Status string

const (
	Open   Status = "OPEN"
	Closed Status = "CLOSED"
)

var (
	ErrNotFound     = errors.New("resource not found")
	ErrDenied       = errors.New("access denied")
	ErrConflict     = errors.New("session conflict")
	ErrInvalidPIN   = errors.New("PIN invalid or unavailable")
	ErrPINLocked    = errors.New("PIN attempts locked")
	ErrCloseBlocked = errors.New("session close is not authorized")
	ErrExpired      = errors.New("session or grant expired")
	ErrRateLimited  = errors.New("table join grant limit reached")
)

// Options are named test/runtime configuration, not owner policy. Every TTL
// and PIN lockout setting is required and validated when a service is created.
type Options struct {
	SessionTTL     time.Duration
	GrantTTL       time.Duration
	GrantWindow    time.Duration
	GrantWindowMax int
	PINTTL         time.Duration
	PINLockout     time.Duration
	PINMaxAttempts int
	PINLength      int
	PINPepper      []byte
	Random         io.Reader
}

func (o Options) Validate() error {
	if o.SessionTTL < time.Millisecond || o.GrantTTL < time.Millisecond || o.GrantWindow < time.Millisecond || o.PINTTL < time.Millisecond || o.PINLockout < time.Millisecond {
		return errors.New("session TTL, grant TTL, grant window, PIN TTL, and PIN lockout must be positive")
	}
	if o.GrantWindowMax < 1 || o.GrantWindowMax > 10000 {
		return errors.New("grant window max must be between 1 and 10000")
	}
	if o.PINMaxAttempts < 1 || o.PINMaxAttempts > 20 {
		return errors.New("PIN max attempts must be between 1 and 20")
	}
	if o.PINLength < 4 || o.PINLength > 8 {
		return errors.New("PIN length must be between 4 and 8")
	}
	if len(o.PINPepper) < 32 {
		return errors.New("PIN pepper must contain at least 32 bytes")
	}
	return nil
}

type Scope struct{ TenantID, StoreID, TableID string }
type Session struct {
	ID                      string     `json:"id"`
	Scope                   Scope      `json:"-"`
	Status                  Status     `json:"status"`
	Mode                    Mode       `json:"mode"`
	RequiresStaffAcceptance bool       `json:"requires_staff_acceptance"`
	OpenedAt                time.Time  `json:"opened_at"`
	ExpiresAt               time.Time  `json:"expires_at"`
	Expired                 bool       `json:"-"`
	ClosedAt                *time.Time `json:"closed_at,omitempty"`
	Version                 int64      `json:"version"`
}
type TableInfo struct {
	GuestDisplayName *string
	Scope            Scope
	Locator          string
	Enabled          bool
	StoreEnabled     bool
	TenantEnabled    bool
	Operating        bool
	Mode             Mode
}
type PublicTable struct {
	StoreDisplayName string `json:"store_display_name,omitempty"`
	Mode             Mode   `json:"mode"`
	CurrentSessionID string `json:"current_session_id,omitempty"`
	Open             bool   `json:"open"`
	Expired          bool   `json:"expired,omitempty"`
}
type Grant struct {
	Token     string    `json:"table_join_token"`
	ExpiresAt time.Time `json:"expires_at"`
	Session   Session   `json:"session"`
}
type OpenResult struct {
	Session Session `json:"session"`
	PIN     string  `json:"pin,omitempty"`
}

// ClosePolicy must re-check all future order/payment gates inside the same
// transaction as close. The production default denies because those domains
// do not exist yet.
type ClosePolicy interface {
	CheckClose(context.Context, pgx.Tx, Session) error
}

type DenyClose struct{}

func (DenyClose) CheckClose(context.Context, pgx.Tx, Session) error { return ErrCloseBlocked }
