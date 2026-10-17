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
	Actor      Actor
	Permission Permission
	Resource   Scope
}

// Authorizer is the adapter seam for MOD-08 identity and authorization.
type Authorizer interface {
	Authorize(context.Context, Request) error
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
