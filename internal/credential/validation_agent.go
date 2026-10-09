package credential

import (
	"context"
	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
)

func validCredentialRevision(scope string, revision int64) bool {
	return (scope == "connection" && revision > 0) || (scope == "agent" && revision >= 0)
}
func decodeValidatedCredential(scope Scope, plaintext []byte) (map[string]string, bool) {
	if scope.Resource.ScopeKind == "connection" {
		return decodePostgresPassword(plaintext)
	}
	fields := map[string]string{}
	if scope.Resource.ScopeKind != "agent" || strictjson.Decode(plaintext, &fields) != nil || len(fields) != 1 || (fields["api_key"] == "" && !disabledAgentCredential(scope, fields)) || (scope.Provider == "agent-disabled" && !disabledAgentCredential(scope, fields)) {
		clear(fields)
		return nil, false
	}
	return fields, true
}

func disabledAgentCredential(scope Scope, fields map[string]string) bool {
	key, exists := fields["api_key"]
	return scope.Resource.ScopeKind == "agent" && scope.Provider == "agent-disabled" && len(fields) == 1 && exists && key == ""
}
func (service *ValidationService) authorize(ctx context.Context, actor string, resource Resource) error {
	if resource.ScopeKind == "agent" {
		pair, err := access.NewInstancePermissionPair(access.ActionPlatformSettingsUpdate, resource.ResourceID)
		if err != nil || service.authorizer.RequirePermission(ctx, actor, pair) != nil {
			return ErrForbidden
		}
		return nil
	}
	ref, err := access.NewResourceRef(projectgraph.ResourceID(resource.ResourceID), projectgraph.KindConnection)
	if err != nil {
		return ErrInvalid
	}
	for _, action := range []access.Action{access.ActionConnectionManage, access.ActionConnectionUse} {
		pair, err := access.NewExactPermissionPair(action, projectgraph.ResourceID(resource.ProjectID), ref)
		if err != nil || service.authorizer.RequirePermission(ctx, actor, pair) != nil {
			return ErrForbidden
		}
	}
	return nil
}
