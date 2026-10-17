package testoperations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

func TestCatalogFixtureAdminIsExplicitTenantScopedAndRevocable(t *testing.T) {
	_, _, ordinary, err := NewCredentialSet("tenant", "store")
	if err != nil || ordinary.CatalogAdmin != nil {
		t.Fatalf("ordinary operations fixture must not mint a catalog administrator: %v", err)
	}
	identity, directory, credentials, err := NewCatalogCredentialSet("tenant", "store")
	if err != nil {
		t.Fatal(err)
	}
	admin := credentials.CatalogAdmin
	if admin == nil || !admin.MFA || admin.Role != authz.TenantAdmin {
		t.Fatal("explicit catalog fixture did not create its test administrator")
	}
	r := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	r.Header.Set("Authorization", "Bearer "+admin.Token)
	actor, err := identity.Resolve(context.Background(), r)
	if err != nil || actor.SubjectID != admin.SubjectID || !actor.MFA {
		t.Fatalf("fixture administrator cannot resolve: %v", err)
	}
	authorizer := authz.PolicyAuthorizer{Directory: directory}
	req := authz.Request{Actor: actor, Resource: authz.Scope{TenantID: "tenant"}, AllowedRoles: []authz.Role{authz.TenantAdmin}, RequiresMFA: true}
	if err := authorizer.Authorize(context.Background(), req); err != nil {
		t.Fatalf("tenant-scoped administrator denied: %v", err)
	}
	req.Resource.TenantID = "foreign"
	if err := authorizer.Authorize(context.Background(), req); err == nil {
		t.Fatal("fixture administrator crossed tenant boundary")
	}
	req.Resource.TenantID = "tenant"
	directory.Remove(admin.SubjectID)
	if err := authorizer.Authorize(context.Background(), req); err == nil {
		t.Fatal("revoked fixture administrator remained authorized")
	}
	_, _, another, err := NewCatalogCredentialSet("tenant", "store")
	if err != nil || another.CatalogAdmin.Token == admin.Token {
		t.Fatal("independent fixture must create different credentials")
	}
}
