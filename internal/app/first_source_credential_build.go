package app

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Selection starts from the native coordinator's persisted plan, never a
// request-supplied version. Every inspection and pool acquisition rechecks the
// exact plan link and current authority; the selected pair is not a grant.
type firstSourceCredentialBuild struct {
	plan        *firstSourceCredentialPlan
	targets     firstSourcePreparationTargetReader
	normal      appdeploymentpostgres.NativePlanConnectionAuthorities
	connections func(connectionbinding.RuntimeBindingAuthorizer, connectionbinding.LocalCredentialPins) (appdeploymentpostgres.NativePlanConnectionAuthorities, error)
}

func (b *firstSourceCredentialBuild) Select(ctx context.Context, plan deployment.DeliveryPlan) (appdeploymentpostgres.NativePlanConnectionAuthorities, error) {
	stored, err := b.lookup(ctx, plan)
	if errors.Is(err, credentialmodule.ErrValidationNotFound) {
		return b.normal, nil
	}
	if err != nil {
		return appdeploymentpostgres.NativePlanConnectionAuthorities{}, err
	}
	if _, _, err = b.check(ctx, plan, stored.Link); err != nil {
		return appdeploymentpostgres.NativePlanConnectionAuthorities{}, err
	}
	return b.selected(plan, stored.Link)
}

func (b *firstSourceCredentialBuild) lookup(ctx context.Context, plan deployment.DeliveryPlan) (credentialmodule.FirstSourceStoredPlanLink, error) {
	if b == nil || !b.plan.available() || ctx == nil {
		return credentialmodule.FirstSourceStoredPlanLink{}, credentialmodule.ErrValidationUnavailable
	}
	tx, err := b.plan.authority.pool.Begin(ctx)
	if err != nil {
		return credentialmodule.FirstSourceStoredPlanLink{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	return b.plan.journal.StoredPlanLinkTx(ctx, tx, plan.TargetID, plan.ID)
}

func (b *firstSourceCredentialBuild) selected(plan deployment.DeliveryPlan, link credentialmodule.FirstSourcePlanLink) (appdeploymentpostgres.NativePlanConnectionAuthorities, error) {
	if b.connections == nil {
		return appdeploymentpostgres.NativePlanConnectionAuthorities{}, credentialmodule.ErrValidationUnavailable
	}
	selected, err := b.connections(func(ctx context.Context, actor string, binding connectionbinding.TargetBinding) error {
		current, _, err := b.check(ctx, plan, link)
		if err != nil {
			return err
		}
		if actor != plan.ActorID || !firstSourceBuildBindingMatches(current, binding) {
			return credentialmodule.ErrValidationConflict
		}
		return nil
	}, func(ctx context.Context, request connectionbinding.RuntimeBindingRequest, binding connectionbinding.TargetBinding) (string, error) {
		current, version, err := b.check(ctx, plan, link)
		if err != nil {
			return "", err
		}
		if request.Actor != plan.ActorID || request.TargetID.String() != plan.TargetID || request.Identity.ProjectID != plan.ProjectID || request.Identity.Environment != plan.Environment || !firstSourceBuildBindingMatches(current, binding) {
			return "", credentialmodule.ErrValidationConflict
		}
		return version, nil
	})
	if err != nil {
		return appdeploymentpostgres.NativePlanConnectionAuthorities{}, err
	}
	if typednil.IsNil(selected.BindingEvidence) || typednil.IsNil(selected.Connections) {
		return appdeploymentpostgres.NativePlanConnectionAuthorities{}, credentialmodule.ErrValidationUnavailable
	}
	bound := &firstSourceBuildConnections{selected: selected, project: func(ctx context.Context) (projectgraph.ResourceID, error) {
		_, _, err := b.check(ctx, plan, link)
		return plan.ProjectID, err
	}, target: plan.TargetID, environment: plan.Environment}
	return appdeploymentpostgres.NativePlanConnectionAuthorities{BindingEvidence: bound, Connections: bound}, nil
}

func (b *firstSourceCredentialBuild) check(ctx context.Context, plan deployment.DeliveryPlan, link credentialmodule.FirstSourcePlanLink) (binding connectionbinding.TargetBinding, version string, err error) {
	if typednil.IsNil(b.targets) {
		return binding, "", credentialmodule.ErrValidationUnavailable
	}
	err = b.plan.authority.fence.WithUnpublishedTarget(ctx, plan.TargetID, plan.ProjectID.String(), plan.Environment, func(ctx context.Context) error {
		tx, err := b.plan.authority.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.WithoutCancel(ctx))
		stored, err := b.plan.journal.StoredPlanLinkTx(ctx, tx, plan.TargetID, plan.ID)
		if err != nil {
			return err
		}
		if stored.Link != link {
			return credentialmodule.ErrValidationConflict
		}
		i := stored.Preparation.Preparation.Intent
		request := deploymentmodule.NativeDeliveryPlanRequest{ProjectID: plan.ProjectID, TargetID: plan.TargetID, Environment: plan.Environment, PrincipalID: plan.ActorID, SourceOwnerID: plan.SourceOwnerID, Operation: string(plan.Operation), SourceDigest: plan.SourceDigest, SourceAttestationDigest: plan.Provenance.AttestationDigest, IdempotencyKey: i.PlanIdempotencyKey, FirstSourcePreparationID: link.PreparationID}
		if !firstSourcePlanIntentMatches(i, request, plan.BaseTargetRevision) || !firstSourcePlanMatches(plan, request, i.ExpectedTargetRevision) || link.BindingDigest != plan.Execution.BindingDigest || link.RequestDigest != i.PlanRequestDigest {
			return credentialmodule.ErrValidationConflict
		}
		target, err := b.targets.TargetTx(ctx, tx, plan.TargetID)
		if err != nil {
			return err
		}
		binding, err = b.plan.checkIntentTx(ctx, tx, stored.Preparation, request, target)
		if err != nil {
			return err
		}
		fingerprint, err := deployment.BindingFingerprint(firstSourceCredentialEvidence(binding, i))
		if err != nil {
			return err
		}
		if fingerprint != link.BindingDigest {
			return credentialmodule.ErrValidationConflict
		}
		version = i.Receipt.Binding.VersionID
		return tx.Commit(ctx)
	})
	return binding, version, err
}

func firstSourceBuildBindingMatches(current, supplied connectionbinding.TargetBinding) bool {
	return current.ID == supplied.ID && current.Scope == supplied.Scope && current.TargetID == supplied.TargetID && current.ConnectionID == supplied.ConnectionID && current.ConnectorKind == supplied.ConnectorKind && current.Revision == supplied.Revision && current.Enabled == supplied.Enabled && reflect.DeepEqual(current.Configuration(), supplied.Configuration())
}

type firstSourceRuntimeScopeKey struct{}
type firstSourceRuntimeScope struct {
	target, environment string
	project             func(context.Context) (projectgraph.ResourceID, error)
	active              *atomic.Bool
}

// Only the selected native build adapter creates this short-lived context.
// It permits the runtime's exact-version scope read for a currently authorized
// publisher token without changing browser draft/session authorization.
type firstSourceBuildConnections struct {
	selected            appdeploymentpostgres.NativePlanConnectionAuthorities
	target, environment string
	project             func(context.Context) (projectgraph.ResourceID, error)
}

func (c *firstSourceBuildConnections) scoped(ctx context.Context) (context.Context, func()) {
	active := &atomic.Bool{}
	active.Store(true)
	return context.WithValue(ctx, firstSourceRuntimeScopeKey{}, firstSourceRuntimeScope{target: c.target, environment: c.environment, project: c.project, active: active}), func() { active.Store(false) }
}

func (c *firstSourceBuildConnections) Resolve(ctx context.Context, request deployment.CandidateConnectionRequest) ([]deployment.CandidateConnectionEvidence, error) {
	ctx, cancel := c.scoped(ctx)
	defer cancel()
	return c.selected.BindingEvidence.Resolve(ctx, request)
}

func (c *firstSourceBuildConnections) Acquire(ctx context.Context, request deployment.CandidateConnectionRequest) (deployment.CandidateConnectionLeases, error) {
	ctx, cancel := c.scoped(ctx)
	defer cancel()
	return c.selected.Connections.Acquire(ctx, request)
}
