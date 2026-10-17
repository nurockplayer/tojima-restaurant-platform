package testoperations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

func TestCredentialIdentityUsesRandomBearerAndCurrentMembership(t *testing.T) {
	identity, directory, credentials, err := NewCredentialSet("tenant", "store")
	if err != nil {
		t.Fatal(err)
	}
	request := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/staff", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	actor, err := identity.Resolve(context.Background(), request(credentials.Manager.Token))
	if err != nil || actor.SubjectID != credentials.Manager.SubjectID || !actor.MFA {
		t.Fatalf("manager actor=%+v err=%v", actor, err)
	}
	authorizer := authz.PolicyAuthorizer{Directory: directory}
	if err := authorizer.Authorize(context.Background(), authz.Request{Actor: actor, Permission: "test.manager", Resource: authz.Scope{TenantID: "tenant", StoreID: "store"}, AllowedRoles: []authz.Role{authz.StoreManager}, RequiresMFA: true, MFARequiredRoles: []authz.Role{authz.StoreManager}}); err != nil {
		t.Fatalf("manager authorization failed: %v", err)
	}
	if err := authorizer.Authorize(context.Background(), authz.Request{Actor: actor, Resource: authz.Scope{TenantID: "tenant", StoreID: "other"}, AllowedRoles: []authz.Role{authz.StoreManager}}); err == nil {
		t.Fatal("manager credential crossed store scope")
	}
	if _, err := identity.Resolve(context.Background(), request("not-a-fixture-token")); err == nil {
		t.Fatal("unissued bearer token was accepted")
	}

	// Role-looking headers never affect the actor or its live membership.
	roleHeader := request(credentials.Kitchen.Token)
	roleHeader.Header.Set("X-Role", string(authz.StoreManager))
	kitchen, err := identity.Resolve(context.Background(), roleHeader)
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(context.Background(), authz.Request{Actor: kitchen, Resource: authz.Scope{TenantID: "tenant", StoreID: "store"}, AllowedRoles: []authz.Role{authz.StoreManager}}); err == nil {
		t.Fatal("caller-supplied role header elevated kitchen credential")
	}

	directory.Remove(credentials.Manager.SubjectID)
	if err := authorizer.Authorize(context.Background(), authz.Request{Actor: actor, Resource: authz.Scope{TenantID: "tenant", StoreID: "store"}, AllowedRoles: []authz.Role{authz.StoreManager}}); err == nil {
		t.Fatal("revoked membership remained authorized")
	}
}

func TestIdentityRequiresExactlyOneBearerCredential(t *testing.T) {
	identity, _, credentials, err := NewCredentialSet("tenant", "store")
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", credentials.Cashier.Token, "Basic " + credentials.Cashier.Token, "Bearer", "Bearer " + credentials.Cashier.Token + " extra"} {
		r := httptest.NewRequest(http.MethodGet, "/staff", nil)
		r.Header.Set("Authorization", header)
		if _, err := identity.Resolve(context.Background(), r); err == nil {
			t.Fatalf("malformed Authorization %q was accepted", header)
		}
	}
}
