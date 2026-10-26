package authz

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type membershipFixture struct {
	mu      sync.RWMutex
	entries []Membership
}

func (m *membershipFixture) Current(context.Context, string, string) ([]Membership, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Membership(nil), m.entries...), nil
}

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

func TestPolicyAuthorizerScopesRolesMFAAndCurrentMembership(t *testing.T) {
	directory := &membershipFixture{entries: []Membership{
		{TenantID: "tenant-a", StoreID: "store-a", Role: Staff},
		{TenantID: "tenant-a", StoreID: "store-a", Role: StoreManager},
		{TenantID: "tenant-a", StoreID: "store-a", Role: Kitchen},
		{TenantID: "tenant-a", StoreID: "store-b", Role: StoreManager},
		{TenantID: "tenant-a", Role: TenantAdmin},
	}}
	authorizer := PolicyAuthorizer{Directory: directory}
	resource := Scope{TenantID: "tenant-a", StoreID: "store-a"}
	staff := Request{Actor: Actor{SubjectID: "actor"}, Permission: "session.open", Resource: resource, AllowedRoles: []Role{Staff, StoreManager}, MFARequiredRoles: []Role{StoreManager}}
	if err := authorizer.Authorize(context.Background(), staff); err != nil {
		t.Fatalf("same-store staff should be allowed: %v", err)
	}
	staffRoleUnion := staff
	staffRoleUnion.AllowedRoles = []Role{StoreManager, Staff}
	if err := authorizer.Authorize(context.Background(), staffRoleUnion); err != nil {
		t.Fatalf("MFA-ineligible manager membership shadowed staff cashier membership: %v", err)
	}
	reversed := &membershipFixture{entries: []Membership{{TenantID: "tenant-a", StoreID: "store-a", Role: StoreManager}, {TenantID: "tenant-a", StoreID: "store-a", Role: Staff}}}
	if err := (PolicyAuthorizer{Directory: reversed}).Authorize(context.Background(), staffRoleUnion); err != nil {
		t.Fatalf("MFA result depended on membership order: %v", err)
	}
	manager := staff
	manager.AllowedRoles = []Role{StoreManager}
	if err := authorizer.Authorize(context.Background(), manager); !errors.Is(err, ErrDenied) {
		t.Fatalf("high-role manager without MFA was accepted: %v", err)
	}
	manager.Actor.MFA = true
	if err := authorizer.Authorize(context.Background(), manager); err != nil {
		t.Fatalf("same-store manager with MFA was denied: %v", err)
	}
	kitchen := staff
	kitchen.AllowedRoles = []Role{Kitchen}
	if err := authorizer.Authorize(context.Background(), kitchen); err != nil {
		t.Fatalf("same-store kitchen read role was denied: %v", err)
	}
	kitchenDirectory := &membershipFixture{entries: []Membership{{TenantID: "tenant-a", StoreID: "store-a", Role: Kitchen}}}
	kitchenAuthorizer := PolicyAuthorizer{Directory: kitchenDirectory}
	kitchen.AllowedRoles = []Role{Staff, StoreManager}
	if err := kitchenAuthorizer.Authorize(context.Background(), kitchen); !errors.Is(err, ErrDenied) {
		t.Fatalf("kitchen role gained close permission: %v", err)
	}
	financeDirectory := &membershipFixture{entries: []Membership{{TenantID: "tenant-a", StoreID: "store-a", Role: FinanceViewer}}}
	financeRead := staff
	financeRead.AllowedRoles = []Role{Staff, Kitchen}
	if err := (PolicyAuthorizer{Directory: financeDirectory}).Authorize(context.Background(), financeRead); !errors.Is(err, ErrDenied) {
		t.Fatalf("FINANCE_VIEWER gained session read: %v", err)
	}
	crossTenant := staff
	crossTenant.Resource.TenantID = "tenant-b"
	crossTenant.AllowedRoles = []Role{Staff, StoreManager}
	crossTenant.Actor.MFA = true
	if err := authorizer.Authorize(context.Background(), crossTenant); !errors.Is(err, ErrDenied) {
		t.Fatalf("membership from another tenant was accepted: %v", err)
	}
	tenantAdmin := Request{Actor: Actor{SubjectID: "actor"}, Permission: "store.qr_mode.write", Resource: resource, AllowedRoles: []Role{TenantAdmin}, MFARequiredRoles: []Role{TenantAdmin}}
	if err := authorizer.Authorize(context.Background(), tenantAdmin); !errors.Is(err, ErrDenied) {
		t.Fatalf("tenant admin without verified MFA was accepted: %v", err)
	}
	tenantAdmin.Actor.MFA = true
	if err := authorizer.Authorize(context.Background(), tenantAdmin); err != nil {
		t.Fatalf("tenant admin with MFA and tenant scope should pass: %v", err)
	}
	financeAdmin := tenantAdmin
	financeAdmin.AllowedRoles = []Role{FinanceViewer}
	financeAdmin.MFARequiredRoles = nil
	if err := authorizer.Authorize(context.Background(), financeAdmin); !errors.Is(err, ErrDenied) {
		t.Fatalf("FINANCE_VIEWER gained tenant configuration access: %v", err)
	}
	crossStoreOnly := &membershipFixture{entries: []Membership{{TenantID: "tenant-a", StoreID: "store-b", Role: StoreManager}}}
	crossStoreRequest := Request{Actor: Actor{SubjectID: "actor", MFA: true}, Permission: "table_session.close", Resource: resource, AllowedRoles: []Role{StoreManager}, MFARequiredRoles: []Role{StoreManager}}
	if err := (PolicyAuthorizer{Directory: crossStoreOnly}).Authorize(context.Background(), crossStoreRequest); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-store-only role was accepted: %v", err)
	}
	crossTenantOnly := &membershipFixture{entries: []Membership{{TenantID: "tenant-b", StoreID: "store-a", Role: StoreManager}}}
	if err := (PolicyAuthorizer{Directory: crossTenantOnly}).Authorize(context.Background(), crossStoreRequest); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-tenant-only role was accepted: %v", err)
	}
	directory.mu.Lock()
	directory.entries = nil
	directory.mu.Unlock()
	if err := authorizer.Authorize(context.Background(), staff); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked membership remained valid: %v", err)
	}
}

func TestPolicyAuthorizerEnforcesCentralMFARolesAndMandatoryStepUp(t *testing.T) {
	ctx := context.Background()
	resource := Scope{TenantID: "tenant-a", StoreID: "store-a"}
	for _, role := range []Role{StoreManager, TenantAdmin, FinanceViewer} {
		directory := &membershipFixture{entries: []Membership{{TenantID: "tenant-a", StoreID: "store-a", Role: role}}}
		// The caller omits MFARequiredRoles; the role still requires MFA.
		request := Request{Actor: Actor{SubjectID: "actor"}, Permission: "operations.read", Resource: resource, AllowedRoles: []Role{role}}
		if err := (PolicyAuthorizer{Directory: directory}).Authorize(ctx, request); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s without MFA: got %v, want ErrDenied", role, err)
		}
		request.Actor.MFA = true
		if err := (PolicyAuthorizer{Directory: directory}).Authorize(ctx, request); err != nil {
			t.Fatalf("%s with MFA: %v", role, err)
		}
	}
	cashier := &membershipFixture{entries: []Membership{{TenantID: "tenant-a", StoreID: "store-a", Role: Staff}}}
	if err := (PolicyAuthorizer{Directory: cashier}).Authorize(ctx, Request{Actor: Actor{SubjectID: "actor"}, Permission: "operations.read", Resource: resource, AllowedRoles: []Role{Staff}}); err != nil {
		t.Fatalf("cashier needs no MFA: %v", err)
	}

	manager := PolicyAuthorizer{Directory: &membershipFixture{entries: []Membership{{TenantID: "tenant-a", StoreID: "store-a", Role: StoreManager}}}}
	for permission := range MandatoryStepUp {
		// The caller omits RequiresStepUp; the permission still requires it.
		stale := Request{Actor: Actor{SubjectID: "actor", MFA: true}, Permission: permission, Resource: resource, AllowedRoles: []Role{StoreManager}}
		if err := manager.Authorize(ctx, stale); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s with stale step-up: got %v, want ErrDenied", permission, err)
		}
		fresh := stale
		fresh.Actor.StepUpFresh = true
		if err := manager.Authorize(ctx, fresh); err != nil {
			t.Fatalf("%s with fresh step-up: %v", permission, err)
		}
		freshWithoutMFA := fresh
		freshWithoutMFA.Actor.MFA = false
		if err := manager.Authorize(ctx, freshWithoutMFA); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s StepUpFresh without MFA: got %v, want ErrDenied", permission, err)
		}
	}
	optIn := Request{Actor: Actor{SubjectID: "actor", MFA: true}, Permission: "operations.read", Resource: resource, AllowedRoles: []Role{StoreManager}, RequiresStepUp: true}
	if err := manager.Authorize(ctx, optIn); !errors.Is(err, ErrDenied) {
		t.Fatalf("caller-requested step-up ignored: %v", err)
	}
}
