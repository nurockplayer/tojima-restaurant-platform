package authz

import (
	"context"
	"errors"
	"testing"
)

func TestSameScopeRequiresExactNonEmptyTenantAndStore(t *testing.T) {
	scope := Scope{TenantID: "tenant-a", StoreID: "store-a"}
	cases := []struct {
		name      string
		requested Scope
		resource  Scope
		want      bool
	}{
		{name: "matching scope", requested: scope, resource: scope, want: true},
		{name: "cross tenant", requested: scope, resource: Scope{TenantID: "tenant-b", StoreID: "store-a"}},
		{name: "cross store", requested: scope, resource: Scope{TenantID: "tenant-a", StoreID: "store-b"}},
		{name: "empty requested scope", resource: scope},
		{name: "empty resource scope", requested: scope},
		{name: "blank tenant", requested: Scope{TenantID: " ", StoreID: "store-a"}, resource: Scope{TenantID: " ", StoreID: "store-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameScope(tc.requested, tc.resource); got != tc.want {
				t.Fatalf("SameScope(%+v, %+v) = %v, want %v", tc.requested, tc.resource, got, tc.want)
			}
		})
	}
}

func TestDenyAllRejectsEveryRequest(t *testing.T) {
	requests := []Request{
		{},
		{Actor: Actor{SubjectID: "staff-a"}, Permission: "order.read", Resource: Scope{TenantID: "tenant-a", StoreID: "store-a"}},
	}
	for _, request := range requests {
		if err := (DenyAll{}).Authorize(context.Background(), request); !errors.Is(err, ErrDenied) {
			t.Fatalf("Authorize(%+v) error = %v, want ErrDenied", request, err)
		}
	}
}
