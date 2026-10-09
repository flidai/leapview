package app

import (
	"context"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Candidate bootstrap may inspect/use only an already admitted local profile.
// The leaser still checks the durable profile's complete binding/digest set.
// The outer opaque marker proves pre-activation principal authority; the
// credential must additionally permit use of this exact source connection.
func developmentCandidateBootstrapAuthorized(ctx context.Context, production, profileAdmission bool, actor string, binding analyticsmodule.ConnectionTargetBinding, target, environment string) bool {
	if production || !profileAdmission {
		return false
	}
	marker, marked := accessmodule.BootstrapAuthorizationFromContext(ctx)
	operation, started := apigencommand.OperationID(ctx)
	credential, authenticated := accessmodule.APICredentialFromContext(ctx)
	return marked && started && authenticated && developmentCandidateBootstrapBindingMatches(marker, operation, credential, actor, binding, target, environment)
}

func developmentCandidateBootstrapBindingMatches(marker accessmodule.BootstrapAuthorization, operation string, credential access.APICredential, actor string, binding analyticsmodule.ConnectionTargetBinding, target, environment string) bool {
	want := access.Capability("")
	switch operation {
	case "createDeliveryPlan":
		want = access.CapabilityResourceRead
	case "buildDeliveryPlan":
		want = access.CapabilityResourceUse
	default:
		return false
	}
	if actor == "" || marker.PrincipalID != actor || marker.ProjectID != binding.Scope.ProjectID || marker.Capability != want ||
		target == "" || binding.TargetID.String() != target || environment == "" || binding.Scope.Environment != environment {
		return false
	}
	return bootstrapBindingCredentialAllows(credential, actor, binding, target, access.ActionConnectionUse)
}

func bootstrapBindingCredentialAllows(credential access.APICredential, actor string, binding analyticsmodule.ConnectionTargetBinding, target string, action access.Action) bool {
	if actor == "" || credential.Token.ID == "" || credential.Principal.ID != actor || credential.Token.PrincipalID != actor {
		return false
	}
	resource, err := access.NewResourceRef(binding.ConnectionID, projectgraph.KindConnection)
	if err != nil {
		return false
	}
	pair, err := access.NewExactPermissionPair(action, binding.Scope.ProjectID, resource)
	if err != nil {
		return false
	}
	if credential.Authoring != nil {
		return credential.Authoring.Scope.AuthorizePairs(target, binding.Scope.ProjectID.String(), []access.PermissionPair{pair}) == nil
	}
	return credential.Token.PermissionProfile == access.PermissionCatalogProfile &&
		access.ValidatePermissionPairs(credential.Token.Permissions) == nil && access.PermissionSetAllows(credential.Token.Permissions, pair)
}
