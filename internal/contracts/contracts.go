// Package contracts records pre-persistence concurrency and idempotency contracts.
// These types do not implement storage, authorization policy, or order processing.
package contracts

import (
	"errors"
	"strings"
)

var ErrVersionConflict = errors.New("version conflict")
var ErrIncompleteKey = errors.New("idempotency key is incomplete")

// RequireVersion expresses the optimistic concurrency precondition for a shared draft.
func RequireVersion(expected, actual uint64) error {
	if expected != actual {
		return ErrVersionConflict
	}
	return nil
}

// OrderKey is unique for a committed order within a tenant/store/session generation.
type OrderKey struct {
	TenantID   string
	StoreID    string
	SessionID  string
	Generation uint64
}

func (k OrderKey) Validate() error {
	if blank(k.TenantID) || blank(k.StoreID) || blank(k.SessionID) || k.Generation == 0 {
		return ErrIncompleteKey
	}
	return nil
}

// PaymentKey identifies one payment attempt for one tenant/store-scoped order.
type PaymentKey struct {
	TenantID  string
	StoreID   string
	OrderID   string
	AttemptID string
}

func (k PaymentKey) Validate() error {
	if blank(k.TenantID) || blank(k.StoreID) || blank(k.OrderID) || blank(k.AttemptID) {
		return ErrIncompleteKey
	}
	return nil
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }
