package testoperations

import (
	"crypto/sha256"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

// NewCatalogCredentialSet adds a tenant-scoped, MFA-verified test administrator
// only for the explicitly enabled, isolated catalog acceptance runtime.
// It does not create real identity-provider users or memberships.
func NewCatalogCredentialSet(tenantID, storeID string) (*IdentityResolver, *MembershipDirectory, CredentialSet, error) {
	identity, directory, credentials, err := NewCredentialSet(tenantID, storeID)
	if err != nil {
		return nil, nil, CredentialSet{}, err
	}
	subject, err := randomHex(16)
	if err != nil {
		return nil, nil, CredentialSet{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return nil, nil, CredentialSet{}, err
	}
	admin := &Credential{SubjectID: "test-" + subject, Role: authz.TenantAdmin, MFA: true, Token: token}
	identity.tokens[sha256.Sum256([]byte(token))] = credentialIdentity{actor: authz.Actor{SubjectID: admin.SubjectID, MFA: true, StepUpFresh: true}}
	directory.memberships[admin.SubjectID] = []authz.Membership{{TenantID: tenantID, Role: authz.TenantAdmin}}
	credentials.CatalogAdmin = admin
	return identity, directory, credentials, nil
}
