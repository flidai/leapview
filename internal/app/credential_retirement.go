package app

import (
	"context"
	"strconv"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

type credentialVersionRetirement struct{ config credentialLifecycleConfig }

func (r *credentialVersionRetirement) InspectVersion(ctx context.Context, actor string, resource credentialmodule.ValidationResource, version string) (credentialmodule.VersionStatus, error) {
	return r.run(ctx, actor, resource, version, false)
}
func (r *credentialVersionRetirement) RetireVersion(ctx context.Context, actor string, resource credentialmodule.ValidationResource, version string) (credentialmodule.VersionStatus, error) {
	return r.run(ctx, actor, resource, version, true)
}
func (r *credentialVersionRetirement) run(ctx context.Context, actor string, resource credentialmodule.ValidationResource, version string, retire bool) (credentialmodule.VersionStatus, error) {
	var result credentialmodule.VersionStatus
	if r == nil || ctx == nil || resource.Validate() != nil {
		return result, credentialmodule.ErrInvalidValidation
	}
	c := r.config
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(context.Background())
	if err = r.authorizeTx(ctx, tx, actor, resource); err != nil {
		return result, err
	}
	owner, err := c.Graph.Bootstrap.WithTx(tx).CustomerOwner(ctx)
	if err != nil {
		return result, err
	}
	authorize := func(ctx context.Context, tx pgx.Tx, metadata credentialmodule.VersionMetadata) ([]credentialmodule.VersionDependency, error) {
		if err := r.authorizeTx(ctx, tx, actor, resource); err != nil {
			return nil, err
		}
		currentOwner, err := c.Graph.Bootstrap.WithTx(tx).CustomerOwner(ctx)
		b := metadata.Binding
		if err != nil || b.DeploymentID != c.TargetID || b.OwnerID != currentOwner || b.VersionID != version {
			return nil, credentialmodule.ErrValidationConflict
		}
		refs, err := c.Graph.Release.CredentialVersionReferencesTx(ctx, tx, version)
		if err != nil {
			return nil, err
		}
		dependencies := make([]credentialmodule.VersionDependency, 0, len(refs))
		for _, ref := range refs {
			dependencies = append(dependencies, credentialmodule.VersionDependency{Kind: ref.Kind, ID: ref.ID})
		}
		store, ok := c.Graph.AgentPersistence.Repository.(interface {
			CredentialVersionReferencesTx(context.Context, pgx.Tx, string) ([]int64, error)
		})
		if !ok {
			return nil, credentialmodule.ErrValidationUnavailable
		}
		configurations, err := store.CredentialVersionReferencesTx(ctx, tx, version)
		if err != nil {
			return nil, err
		}
		for _, revision := range configurations {
			dependencies = append(dependencies, credentialmodule.VersionDependency{Kind: "agent_configuration", ID: strconv.FormatInt(revision, 10)})
		}
		return dependencies, nil
	}
	if retire {
		result, err = c.Services.ActivationRepository().RetireVersionTx(ctx, tx, c.TargetID, owner, actor, resource, version, authorize)
	} else {
		result, err = c.Services.ActivationRepository().InspectVersionTx(ctx, tx, c.TargetID, owner, resource, version, authorize)
	}
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (r *credentialVersionRetirement) authorizeTx(ctx context.Context, tx pgx.Tx, actor string, resource credentialmodule.ValidationResource) error {
	c := r.config
	target, err := c.Graph.DeploymentRepository.TargetForUpdateTx(ctx, tx, c.TargetID)
	if err != nil {
		return err
	}
	if resource.ScopeKind == "agent" {
		if resource.ResourceID != c.TargetID {
			return credentialmodule.ErrValidationForbidden
		}
		pair, err := access.NewInstancePermissionPair(access.ActionPlatformSettingsUpdate, c.TargetID)
		if err != nil {
			return err
		}
		return newAgentCredentialAuthority(c.TargetID)(ctx, tx, actor, pair)
	}
	if resource.TargetID != c.TargetID || resource.Environment != c.Environment {
		return credentialmodule.ErrValidationForbidden
	}
	snapshot, err := authorizationSnapshotFromProvider(c.RuntimeHost.Provider())(ctx)
	if err != nil || validateSourceCredentialSnapshot(target, snapshot, resource.ProjectID, resource.Environment) != nil {
		return credentialmodule.ErrValidationForbidden
	}
	ref, err := access.NewResourceRef(projectgraph.ResourceID(resource.ResourceID), projectgraph.KindConnection)
	if err != nil {
		return credentialmodule.ErrValidationForbidden
	}
	graphResource, exists := snapshot.Project().Resource(ref.ID())
	if !exists || graphResource.Kind != projectgraph.KindConnection {
		return credentialmodule.ErrValidationForbidden
	}
	pairs := make([]access.PermissionPair, 0, 2)
	for _, action := range []access.Action{access.ActionConnectionManage, access.ActionConnectionUse} {
		pair, err := access.NewExactPermissionPair(action, projectgraph.ResourceID(resource.ProjectID), ref)
		if err != nil {
			return err
		}
		pairs = append(pairs, pair)
	}
	issuer, err := accessmodule.CredentialTransactionEvidence(ctx, actor)
	if err != nil {
		return credentialmodule.ErrValidationForbidden
	}
	if err = accessmodule.RecheckCredentialAuthorityTx(ctx, tx, issuer, pairs); err != nil {
		return credentialmodule.ErrValidationForbidden
	}
	subjects, err := accessmodule.LockCredentialAuthorizationSubjectsTx(ctx, tx, actor)
	if err != nil {
		return credentialmodule.ErrValidationForbidden
	}
	granted, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return credentialmodule.ErrValidationForbidden
	}
	for _, pair := range pairs {
		if !access.PermissionSetAllows(granted, pair) {
			return credentialmodule.ErrValidationForbidden
		}
	}
	return nil
}
