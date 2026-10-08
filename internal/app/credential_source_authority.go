package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/jackc/pgx/v5"
)

type sourceCredentialTargetAuthority interface {
	TargetForUpdateTx(context.Context, pgx.Tx, string) (deploymentpostgres.DeliveryTarget, error)
}

// newSourceCredentialAuthority uses the canonical active serving policy, held
// stable by the durable target lock, plus current locked identity-layer subjects
// and authenticated credential attenuation. The lifecycle's separate current
// authority check verifies the exact owner/binding/configuration receipt in this
// same transaction; neither a validation receipt nor platform role grants access.
func newSourceCredentialAuthority(instanceID string, delivery sourceCredentialTargetAuthority, provider runtimehostmodule.Provider) func(context.Context, pgx.Tx, string, credentialmodule.ValidationReceipt) error {
	snapshots := authorizationSnapshotFromProvider(provider)
	return func(ctx context.Context, tx pgx.Tx, actor string, receipt credentialmodule.ValidationReceipt) error {
		binding := receipt.Binding
		if ctx == nil || typednil.IsNil(tx) || typednil.IsNil(delivery) || typednil.IsNil(provider) || snapshots == nil || receipt.Validate() != nil ||
			binding.ScopeKind != "connection" || binding.TargetID != instanceID || binding.DeploymentID != instanceID || actor != receipt.ActorID {
			return access.ErrForbidden
		}
		target, err := delivery.TargetForUpdateTx(ctx, tx, instanceID)
		if err != nil || target.TargetID != instanceID {
			return access.ErrForbidden
		}
		snapshot, err := snapshots(ctx)
		if err != nil || validateSourceCredentialSnapshot(target, snapshot, binding.ProjectID, binding.Environment) != nil {
			return access.ErrForbidden
		}
		ref, err := access.NewResourceRef(projectgraph.ResourceID(binding.ResourceID), projectgraph.KindConnection)
		if err != nil {
			return access.ErrForbidden
		}
		graphResource, exists := snapshot.Project().Resource(ref.ID())
		if !exists || graphResource.Kind != projectgraph.KindConnection {
			return access.ErrForbidden
		}
		pairs := make([]access.PermissionPair, 0, 2)
		for _, action := range []access.Action{access.ActionConnectionManage, access.ActionConnectionUse} {
			pair, err := access.NewExactPermissionPair(action, projectgraph.ResourceID(binding.ProjectID), ref)
			if err != nil {
				return access.ErrForbidden
			}
			pairs = append(pairs, pair)
		}
		issuer, err := accessmodule.CredentialTransactionEvidence(ctx, actor)
		if err != nil {
			return access.ErrForbidden
		}
		if err = accessmodule.RecheckCredentialAuthorityTx(ctx, tx, issuer, pairs); err != nil {
			return access.ErrForbidden
		}
		subjects, err := accessmodule.LockCredentialAuthorizationSubjectsTx(ctx, tx, actor)
		if err != nil {
			return access.ErrForbidden
		}
		granted, err := snapshot.EffectiveTypedPermissions(subjects)
		if err != nil {
			return access.ErrForbidden
		}
		for _, pair := range pairs {
			if !access.PermissionSetAllows(granted, pair) {
				return access.ErrForbidden
			}
		}
		return nil
	}
}

func validateSourceCredentialSnapshot(target deploymentpostgres.DeliveryTarget, snapshot accesssnapshot.AuthorizationSnapshot, project, environment string) error {
	if snapshot.ValidateBound() != nil || target.ActiveGenerationID == "" || target.ActivePublicationID == "" || target.ProjectID != project || target.Environment != environment {
		return access.ErrForbidden
	}
	identity := snapshot.Identity()
	if identity.ProjectID.String() != project || identity.Environment != environment || identity.GenerationID != target.ActiveGenerationID {
		return access.ErrForbidden
	}
	return nil
}
