package contracts

import (
	"errors"
	"testing"
)

func TestDraftVersionConflictContract(t *testing.T) {
	if err := RequireVersion(8, 8); err != nil {
		t.Fatalf("matching version: %v", err)
	}
	if err := RequireVersion(8, 9); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version error = %v, want ErrVersionConflict", err)
	}
}

func TestOneOrderPerScopedSessionGenerationContract(t *testing.T) {
	first := OrderKey{TenantID: "tenant-a", StoreID: "store-a", SessionID: "session-a", Generation: 3}
	if err := first.Validate(); err != nil {
		t.Fatalf("valid order key: %v", err)
	}
	committed := map[OrderKey]struct{}{first: {}}
	if _, exists := committed[first]; !exists {
		t.Fatal("the same session generation must resolve to the same order key")
	}
	if _, exists := committed[(OrderKey{TenantID: "tenant-a", StoreID: "store-a", SessionID: "session-a", Generation: 4})]; exists {
		t.Fatal("a new generation must have a distinct order key")
	}
	for _, invalid := range []OrderKey{
		{StoreID: "store-a", SessionID: "session-a", Generation: 3},
		{TenantID: "tenant-a", SessionID: "session-a", Generation: 3},
		{TenantID: "tenant-a", StoreID: "store-a", SessionID: "session-a"},
		{TenantID: "tenant-a", StoreID: "store-a", Generation: 3},
		{TenantID: " ", StoreID: "store-a", SessionID: "session-a", Generation: 3},
	} {
		if err := invalid.Validate(); !errors.Is(err, ErrIncompleteKey) {
			t.Errorf("Validate(%+v) = %v, want ErrIncompleteKey", invalid, err)
		}
	}
}

func TestPaymentAttemptIdempotencyContract(t *testing.T) {
	first := PaymentKey{TenantID: "tenant-a", StoreID: "store-a", OrderID: "order-a", AttemptID: "attempt-a"}
	if err := first.Validate(); err != nil {
		t.Fatalf("valid payment key: %v", err)
	}
	attempts := map[PaymentKey]struct{}{first: {}}
	if _, exists := attempts[first]; !exists {
		t.Fatal("retrying an attempt must resolve to the same idempotency key")
	}
	for _, distinct := range []PaymentKey{
		{TenantID: "tenant-a", StoreID: "store-a", OrderID: "order-a", AttemptID: "attempt-b"},
		{TenantID: "tenant-a", StoreID: "store-b", OrderID: "order-a", AttemptID: "attempt-a"},
		{TenantID: "tenant-b", StoreID: "store-a", OrderID: "order-a", AttemptID: "attempt-a"},
		{TenantID: "tenant-a", StoreID: "store-a", OrderID: "order-b", AttemptID: "attempt-a"},
	} {
		if _, exists := attempts[distinct]; exists {
			t.Errorf("distinct scope/order/attempt key must differ: %+v", distinct)
		}
	}
	for _, incomplete := range []PaymentKey{
		{StoreID: "store-a", OrderID: "order-a", AttemptID: "attempt-a"},
		{TenantID: "tenant-a", OrderID: "order-a", AttemptID: "attempt-a"},
		{TenantID: "tenant-a", StoreID: "store-a", AttemptID: "attempt-a"},
		{TenantID: "tenant-a", StoreID: "store-a", OrderID: "order-a"},
		{TenantID: " ", StoreID: "store-a", OrderID: "order-a", AttemptID: "attempt-a"},
	} {
		if err := incomplete.Validate(); !errors.Is(err, ErrIncompleteKey) {
			t.Errorf("Validate(%+v) = %v, want ErrIncompleteKey", incomplete, err)
		}
	}
}
