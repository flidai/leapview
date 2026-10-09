package app

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/credentialagent"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type credentialLifecycleConfig struct {
	Services                           *credentialmodule.Services
	Pool                               *pgxpool.Pool
	Graph                              *postgresauthority.PostgresAuthorityGraph
	Analytics                          *analyticsmodule.Module
	CandidateBindings                  *connectionbinding.RuntimeBindingLeaser
	CandidateAdmission                 deploymentmodule.CandidatePreparationAdmitter
	Mutations                          sourceCredentialMutations
	Reader                             deploymentmodule.NativeDeliveryReader
	RuntimeHost                        *runtimehostmodule.Module
	Evidence                           sourceCredentialEvidence
	TargetID, Environment, KeyringPath string
	BeforeActivationCommit             deploymentpostgres.ActivationPreCommitHook
	AuthorizeConnection                func(context.Context, string, string, string, access.Action) (bool, error)
}

func composeCredentialLifecycle(ctx context.Context, c credentialLifecycleConfig) (*credentialLifecycle, error) {
	if c.Services == nil {
		return nil, nil
	}
	store, ok := c.Graph.AgentPersistence.Repository.(credentialagent.AgentConfigurationStore)
	if !ok {
		return nil, errors.New("agent credential configuration authority unavailable")
	}
	lock := func(ctx context.Context, tx pgx.Tx) error {
		_, err := c.Graph.DeploymentRepository.TargetForUpdateTx(ctx, tx, c.TargetID)
		return err
	}
	lifecycle, err := newCredentialLifecycle(ctx, c.Services, credentialagent.AgentCredentialConfig{
		Pool: c.Pool, RecordAudit: c.Graph.ConnectionBindingAudit.RecordAuditEvent, Store: store, CustomerOwner: c.Graph.Bootstrap,
		CustomerOwnerTx: func(ctx context.Context, tx pgx.Tx) (string, error) {
			return c.Graph.Bootstrap.WithTx(tx).CustomerOwner(ctx)
		},
		InstanceID: c.TargetID, KeyringPath: c.KeyringPath, LockFence: lock,
	}, c.Analytics)
	if err != nil {
		return nil, err
	}
	lineage, err := appdeploymentpostgres.NewActivationLineageVerifier(c.Graph.Lineage)
	if err != nil {
		return nil, err
	}
	current := sourceCredentialCurrentAuthority(c)
	authorize := newSourceCredentialAuthority(c.TargetID, c.Graph.DeploymentRepository, c.RuntimeHost.Provider())
	restore := func(ctx context.Context) error {
		if err := c.Analytics.RetireCredentialPools(ctx); err != nil {
			return err
		}
		target, err := c.Reader.OperatorSnapshot(ctx, c.TargetID)
		if errors.Is(err, deploymentpostgres.ErrNotFound) {
			// Customer keys may be installed before the first project claim.
			// Only the same fully unclaimed state accepted by startup has no
			// source runtime to restore; a claimed but missing target is corrupt.
			claimed, claimErr := postgresAuthoringProjectIDResolver(c.Graph.DeploymentRepository, c.Graph.ServingState, c.TargetID, servingstate.Environment(c.Environment))(ctx)
			if claimErr != nil {
				return claimErr
			}
			if claimed == "" {
				return nil
			}
		}
		if err != nil {
			return err
		}
		if target.ActiveGenerationID == "" {
			return nil
		}
		return c.RuntimeHost.ReconcileSealed(ctx, servingstate.ID(target.ActiveGenerationID))
	}
	source, err := newSourceCredentialActivation(sourceCredentialConfig{
		Pool: c.Pool, Credentials: c.Services.ActivationRepository(), TargetID: c.TargetID, Environment: c.Environment,
		CandidateAdmission: c.CandidateAdmission, Mutations: c.Mutations, Reader: c.Reader, Delivery: c.Graph.DeploymentRepository, Evidence: c.Evidence,
		BeforeActivationCommit: c.BeforeActivationCommit,
		PublicationRepository: func(operation string, auth credentialmodule.ActivationCommitAuthorizer) (*deploymentpostgres.Repository, error) {
			return appdeploymentpostgres.NewCredentialPublicationRepository(c.Pool, appdeploymentpostgres.Authorities{Access: c.Graph.AccessAudit, Events: c.Graph.Events, Lineage: lineage}, c.Services.ActivationRepository(), operation, auth)
		},
		Authorize: sourceCredentialResourceAuthority(c),
		AuthorizeTx: func(ctx context.Context, tx pgx.Tx, actor string, receipt credentialmodule.ValidationReceipt) error {
			if err := current(ctx, tx, receipt); err != nil {
				return err
			}
			return authorize(ctx, tx, actor, receipt)
		}, CurrentAuthorityTx: current,
		Install: func(ctx context.Context, record credentialmodule.ActivationRecord) error {
			if err := c.Analytics.RetireCredentialPools(ctx); err != nil {
				return err
			}
			return c.RuntimeHost.ReconcileSealed(ctx, servingstate.ID(record.Status.GenerationID))
		}, Restore: restore,
	})
	if err != nil {
		return nil, err
	}
	if err = c.CandidateBindings.ConfigureProviderAdmission(lifecycle.gate); err != nil {
		return nil, err
	}
	if err = c.CandidateBindings.ConfigureLocalCredentials(source.LocalCredentialPin, c.Analytics); err != nil {
		return nil, err
	}
	if err = lifecycle.configure(source, source, c.TargetID); err != nil {
		return nil, err
	}
	return lifecycle, nil
}

func sourceCredentialCurrentAuthority(c credentialLifecycleConfig) func(context.Context, pgx.Tx, credentialmodule.ValidationReceipt) error {
	return func(ctx context.Context, tx pgx.Tx, receipt credentialmodule.ValidationReceipt) error {
		b := receipt.Binding
		if receipt.Validate() != nil || b.ScopeKind != "connection" || b.DeploymentID != c.TargetID || b.TargetID != c.TargetID || b.Environment != c.Environment || b.Purpose != "connection-authentication" || b.Provider != "postgres" {
			return credentialmodule.ErrValidationConflict
		}
		if _, err := c.Graph.DeploymentRepository.TargetForUpdateTx(ctx, tx, c.TargetID); err != nil {
			return err
		}
		owner, err := c.Graph.Bootstrap.WithTx(tx).CustomerOwner(ctx)
		if err != nil || owner != b.OwnerID {
			return credentialmodule.ErrValidationConflict
		}
		binding, err := c.Graph.ConnectionBinding.BindingForShareTx(ctx, tx, connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(b.ProjectID), Environment: b.Environment}, connectionbinding.TargetID(b.TargetID), projectgraph.ResourceID(b.ResourceID))
		if err != nil {
			return err
		}
		configurationDigest, err := credentialValidationConfigurationDigest(binding, c.Analytics.CredentialProbePolicyIdentity())
		if err != nil {
			return err
		}
		if !binding.Enabled || binding.ConnectorKind != b.Provider || binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle || binding.ID.String() != receipt.BindingID || binding.Revision != receipt.BindingRevision || binding.Evidence().EndpointConfigHash != b.Destination || configurationDigest != receipt.ConfigurationDigest {
			return credentialmodule.ErrValidationConflict
		}
		return nil
	}
}
func sourceCredentialResourceAuthority(c credentialLifecycleConfig) func(context.Context, string, credentialmodule.ValidationResource) error {
	return func(ctx context.Context, actor string, resource credentialmodule.ValidationResource) error {
		if resource.Validate() != nil || resource.ScopeKind != "connection" || resource.TargetID != c.TargetID || resource.Environment != c.Environment || c.AuthorizeConnection == nil {
			return access.ErrForbidden
		}
		ref, err := access.NewResourceRef(projectgraph.ResourceID(resource.ResourceID), projectgraph.KindConnection)
		if err != nil {
			return access.ErrForbidden
		}
		pairs := []access.PermissionPair{}
		for _, action := range []access.Action{access.ActionConnectionManage, access.ActionConnectionUse} {
			allowed, err := c.AuthorizeConnection(ctx, actor, resource.ProjectID, resource.ResourceID, action)
			if err != nil || !allowed {
				return access.ErrForbidden
			}
			pair, err := access.NewExactPermissionPair(action, projectgraph.ResourceID(resource.ProjectID), ref)
			if err != nil {
				return access.ErrForbidden
			}
			pairs = append(pairs, pair)
		}
		issuer, err := accessmodule.CredentialTransactionEvidence(ctx, actor)
		if err != nil {
			return access.ErrForbidden
		}
		tx, err := c.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.Background())
		return accessmodule.RecheckCredentialAuthorityTx(ctx, tx, issuer, pairs)
	}
}
