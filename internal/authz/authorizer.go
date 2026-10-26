// Package authz defines the replaceable authorization boundary shared with MOD-08.
package authz

import (
	"context"
	"errors"
	"strings"
)

var ErrDenied = errors.New("authorization denied")

// Actor identifies the authenticated subject supplied by the MOD-08 adapter.
type Actor struct {
	SubjectID string
	// MFA means the session was established or promoted with strong (LoA 2)
	// authentication; StepUpFresh means that happened within the last 300 s.
	MFA         bool
	StepUpFresh bool
}

// Permission names an operation requested by an actor.
type Permission string

// Scope identifies the tenant and store a resource belongs to.
type Scope struct {
	TenantID string
	StoreID  string
}

// Request is the minimal typed input to the MOD-08 authorization seam.
type Request struct {
	Actor            Actor
	Permission       Permission
	Resource         Scope
	AllowedRoles     []Role
	RequiresMFA      bool
	MFARequiredRoles []Role
	// RequiresStepUp lets a caller add a fresh step-up; it can never remove the
	// step-up that MandatoryStepUp requires for a permission.
	RequiresStepUp bool
}

// Authorizer is the adapter seam for MOD-08 identity and authorization.
type Authorizer interface {
	Authorize(context.Context, Request) error
}

type Role string

const (
	Staff         Role = "STAFF_CASHIER"
	StoreManager  Role = "STORE_MANAGER"
	Kitchen       Role = "KITCHEN"
	TenantAdmin   Role = "TENANT_ADMIN"
	FinanceViewer Role = "FINANCE_VIEWER"
)

// Memberships must read current, active MOD-08 membership data on every call.
// Implementations must not cache authorization decisions across requests.
type Memberships interface {
	Current(context.Context, string, string) ([]Membership, error)
}

type Membership struct {
	TenantID string
	StoreID  string // empty means tenant-wide scope and is valid only for TENANT_ADMIN
	Role     Role
}

// mfaRoles always require Actor.MFA, whatever MFARequiredRoles a caller passes
// (ADR-0008 §5). Callers may add roles, never remove these.
var mfaRoles = map[Role]bool{StoreManager: true, TenantAdmin: true, FinanceViewer: true}

// MandatoryStepUp lists permissions that always need Actor.StepUpFresh
// (ADR-0008 §5): refund-producing actions and staff membership changes.
var MandatoryStepUp = map[Permission]bool{
	"operations.order.adjust": true,
	"operations.refund.retry": true,
	"staff.membership.grant":  true,
	"staff.membership.revoke": true,
}

type PolicyAuthorizer struct{ Directory Memberships }

func (a PolicyAuthorizer) Authorize(ctx context.Context, request Request) error {
	if a.Directory == nil || strings.TrimSpace(request.Actor.SubjectID) == "" || strings.TrimSpace(request.Resource.TenantID) == "" {
		return ErrDenied
	}
	if request.RequiresMFA && !request.Actor.MFA {
		return ErrDenied
	}
	if (request.RequiresStepUp || MandatoryStepUp[request.Permission]) && !(request.Actor.MFA && request.Actor.StepUpFresh) {
		return ErrDenied
	}
	memberships, err := a.Directory.Current(ctx, request.Actor.SubjectID, request.Resource.TenantID)
	if err != nil {
		return err
	}
	for _, member := range memberships {
		if member.TenantID != request.Resource.TenantID {
			continue
		}
		if member.StoreID != request.Resource.StoreID && !(member.StoreID == "" && member.Role == TenantAdmin) {
			continue
		}
		for _, allowed := range request.AllowedRoles {
			if member.Role == allowed {
				mfaEligible := !mfaRoles[member.Role] || request.Actor.MFA
				for _, role := range request.MFARequiredRoles {
					if member.Role == role && !request.Actor.MFA {
						mfaEligible = false
						break
					}
				}
				if mfaEligible {
					return nil
				}
				break
			}
		}
	}
	return ErrDenied
}

// DenyMemberships is the runtime default until MOD-08 supplies a governed directory.
type DenyMemberships struct{}

func (DenyMemberships) Current(context.Context, string, string) ([]Membership, error) {
	return nil, ErrDenied
}

// SameScope allows access only when both complete tenant/store scopes match.
func SameScope(requested, resource Scope) bool {
	return complete(requested) && complete(resource) && requested == resource
}

func complete(scope Scope) bool {
	return strings.TrimSpace(scope.TenantID) != "" && strings.TrimSpace(scope.StoreID) != ""
}

// DenyAll is the safe default until a governed MOD-08 adapter is supplied.
type DenyAll struct{}

func (DenyAll) Authorize(_ context.Context, _ Request) error { return ErrDenied }
