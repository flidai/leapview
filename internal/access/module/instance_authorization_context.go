package module

import (
	"context"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

type instanceAuthorizationContextKey struct{}

// instanceAuthorization records an exact typed instance decision made by the
// generated API boundary. The type and context key remain package-private so
// downstream handlers cannot manufacture authority; they may only ask the
// module to preserve a decision it already made for this request.
type instanceAuthorization struct {
	OperationID string
	PrincipalID string
	InstanceID  string
	Action      access.Action
}

func withInstanceAuthorization(ctx context.Context, authorization instanceAuthorization) context.Context {
	if ctx == nil || strings.TrimSpace(authorization.OperationID) == "" || strings.TrimSpace(authorization.PrincipalID) == "" || strings.TrimSpace(authorization.InstanceID) == "" {
		return ctx
	}
	definition, ok := access.Permission(authorization.Action)
	if !ok || definition.Scope != access.PermissionScopeInstance {
		return ctx
	}
	authorization.OperationID = strings.TrimSpace(authorization.OperationID)
	authorization.PrincipalID = strings.TrimSpace(authorization.PrincipalID)
	authorization.InstanceID = strings.TrimSpace(authorization.InstanceID)
	return context.WithValue(ctx, instanceAuthorizationContextKey{}, authorization)
}

func instanceAuthorizationFromContext(ctx context.Context) (instanceAuthorization, bool) {
	if ctx == nil {
		return instanceAuthorization{}, false
	}
	authorization, ok := ctx.Value(instanceAuthorizationContextKey{}).(instanceAuthorization)
	if !ok || strings.TrimSpace(authorization.OperationID) == "" || strings.TrimSpace(authorization.PrincipalID) == "" || strings.TrimSpace(authorization.InstanceID) == "" {
		return instanceAuthorization{}, false
	}
	definition, known := access.Permission(authorization.Action)
	if !known || definition.Scope != access.PermissionScopeInstance {
		return instanceAuthorization{}, false
	}
	return authorization, true
}
