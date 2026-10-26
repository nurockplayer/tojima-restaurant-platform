// Package testoperations contains isolated, in-memory identity adapters for
// the loopback operations acceptance runtime. It is never used in production.
package testoperations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

var ErrInvalidFixture = errors.New("invalid test identity fixture")

type credentialIdentity struct {
	actor authz.Actor
}

// IdentityResolver maps only bearer tokens generated for this isolated test
// process to subjects. It never reads role or subject headers from a request.
type IdentityResolver struct {
	mu     sync.RWMutex
	tokens map[[32]byte]credentialIdentity
}

// MembershipDirectory returns current fixture memberships on every call.
// Remove lets local acceptance tests exercise immediate membership revocation.
type MembershipDirectory struct {
	mu          sync.RWMutex
	memberships map[string][]authz.Membership
}

type Credential struct {
	SubjectID string     `json:"subject_id"`
	Role      authz.Role `json:"role"`
	MFA       bool       `json:"mfa"`
	Token     string     `json:"token"`
}

type CredentialSet struct {
	Cashier      Credential  `json:"cashier"`
	Manager      Credential  `json:"manager"`
	Kitchen      Credential  `json:"kitchen"`
	CatalogAdmin *Credential `json:"catalog_admin,omitempty"`
}

// NewCredentialSet creates opaque, random bearer credentials and current
// memberships for exactly one tenant/store. The credentials are returned to
// the caller for an owner-only local file; they are never logged here.
func NewCredentialSet(tenantID, storeID string) (*IdentityResolver, *MembershipDirectory, CredentialSet, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(storeID) == "" {
		return nil, nil, CredentialSet{}, ErrInvalidFixture
	}
	identity := &IdentityResolver{tokens: make(map[[32]byte]credentialIdentity, 3)}
	directory := &MembershipDirectory{memberships: make(map[string][]authz.Membership, 3)}
	credentials := CredentialSet{}
	roles := []struct {
		role authz.Role
		mfa  bool
		dst  *Credential
	}{
		{authz.Staff, false, &credentials.Cashier},
		{authz.StoreManager, true, &credentials.Manager},
		{authz.Kitchen, false, &credentials.Kitchen},
	}
	for _, item := range roles {
		subject, err := randomHex(16)
		if err != nil {
			return nil, nil, CredentialSet{}, err
		}
		token, err := randomHex(32)
		if err != nil {
			return nil, nil, CredentialSet{}, err
		}
		credential := Credential{SubjectID: "test-" + subject, Role: item.role, MFA: item.mfa, Token: token}
		*item.dst = credential
		identity.tokens[sha256.Sum256([]byte(token))] = credentialIdentity{actor: authz.Actor{SubjectID: credential.SubjectID, MFA: credential.MFA, StepUpFresh: credential.MFA}}
		directory.memberships[credential.SubjectID] = []authz.Membership{{TenantID: tenantID, StoreID: storeID, Role: item.role}}
	}
	return identity, directory, credentials, nil
}

func randomHex(size int) (string, error) {
	if size < 16 || size > 64 {
		return "", ErrInvalidFixture
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (i *IdentityResolver) Resolve(_ context.Context, request *http.Request) (authz.Actor, error) {
	if i == nil || request == nil {
		return authz.Actor{}, authz.ErrDenied
	}
	header := request.Header.Get("Authorization")
	if len(header) < len("Bearer ") || !strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
		return authz.Actor{}, authz.ErrDenied
	}
	token := strings.TrimSpace(header[len("Bearer "):])
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return authz.Actor{}, authz.ErrDenied
	}
	hash := sha256.Sum256([]byte(token))
	i.mu.RLock()
	identity, ok := i.tokens[hash]
	i.mu.RUnlock()
	if !ok || strings.TrimSpace(identity.actor.SubjectID) == "" {
		return authz.Actor{}, authz.ErrDenied
	}
	return identity.actor, nil
}

func (d *MembershipDirectory) Current(_ context.Context, subjectID, tenantID string) ([]authz.Membership, error) {
	if d == nil || strings.TrimSpace(subjectID) == "" || strings.TrimSpace(tenantID) == "" {
		return nil, authz.ErrDenied
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	memberships := d.memberships[subjectID]
	result := make([]authz.Membership, 0, len(memberships))
	for _, membership := range memberships {
		if membership.TenantID == tenantID {
			result = append(result, membership)
		}
	}
	return result, nil
}

// Remove revokes the subject's current fixture membership immediately.
func (d *MembershipDirectory) Remove(subjectID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	delete(d.memberships, subjectID)
	d.mu.Unlock()
}

// Set replaces current memberships, allowing deny/revocation tests without
// changing the subject token or trusting a caller-supplied role.
func (d *MembershipDirectory) Set(subjectID string, memberships []authz.Membership) error {
	if d == nil || strings.TrimSpace(subjectID) == "" {
		return ErrInvalidFixture
	}
	copyOfMemberships := append([]authz.Membership(nil), memberships...)
	for _, membership := range copyOfMemberships {
		if strings.TrimSpace(membership.TenantID) == "" || strings.TrimSpace(string(membership.Role)) == "" {
			return ErrInvalidFixture
		}
	}
	d.mu.Lock()
	d.memberships[subjectID] = copyOfMemberships
	d.mu.Unlock()
	return nil
}
