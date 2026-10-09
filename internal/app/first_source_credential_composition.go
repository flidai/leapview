package app

import (
	"context"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	appaccesspostgres "github.com/flidai/leapview/internal/app/accesspostgres"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/app/postgresauthority"
	"github.com/flidai/leapview/internal/app/projectsource"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/jackc/pgx/v5/pgxpool"
)

type firstSourceCredentialScopeConfig struct {
	production          bool
	target, environment string
	pool                *pgxpool.Pool
	graph               *postgresauthority.PostgresAuthorityGraph
	activeProject       func(context.Context) (projectgraph.ResourceID, error)
	activeAuthorize     connectionAuthorization
}

func composeFirstSourceCredentialScope(c firstSourceCredentialScopeConfig) (*firstSourceCredentialServiceScope, error) {
	if !c.production {
		return nil, nil
	}
	admissions, err := credentialmodule.NewFirstSourceAdmissionReader(c.pool, c.graph.ConnectionBindingAudit.RecordAuditEvent)
	if err != nil {
		return nil, err
	}
	return &firstSourceCredentialServiceScope{
		authority: firstSourceCredentialAuthority{
			production: true, targetID: c.target, environment: c.environment,
			pool: c.pool, fence: c.graph.DeploymentRepository, admissions: admissions, bindings: c.graph.ConnectionBinding,
			policyTx: appaccesspostgres.LockedCurrentAuthorizationPolicyTx,
		},
		targets: c.graph.DeploymentRepository, admissions: admissions,
		activeProject: c.activeProject, activeAuthorize: c.activeAuthorize,
	}, nil
}

type firstSourceCredentialCompositionConfig struct {
	scope         *firstSourceCredentialServiceScope
	services      *credentialmodule.Services
	pool          *pgxpool.Pool
	targets       *deploymentpostgres.Repository
	sources       *projectsource.NativeCandidateSourceSynchronizer
	analytics     *analyticsmodule.Module
	audit         credentialmodule.AuditRecorder
	rotationAudit connectionRotationAuditRecorder
	normal        candidateConnectionLeaser
	runtime       func() *credentialLifecycle
}

type firstSourceCredentialComposition struct {
	preparation *firstSourceCredentialPreparationService
	plan        *firstSourceCredentialPlan
	connections *firstSourceCredentialBuild
}

func composeFirstSourceCredentials(c firstSourceCredentialCompositionConfig) (firstSourceCredentialComposition, error) {
	var result firstSourceCredentialComposition
	if c.scope == nil || c.services == nil {
		return result, nil
	}
	if c.pool == nil || c.targets == nil || c.sources == nil || c.analytics == nil || c.audit == nil || c.runtime == nil {
		return result, credentialmodule.ErrValidationUnavailable
	}
	journal, err := credentialmodule.NewFirstSourcePreparationMutations(c.pool, c.audit)
	if err != nil {
		return result, err
	}
	result.preparation = &firstSourceCredentialPreparationService{
		authority: c.scope.authority, receipts: c.services.ActivationRepository(), preparations: journal,
		targets: c.targets, probePolicy: c.analytics.CredentialProbePolicyIdentity,
		retainedSource: func(ctx context.Context, projectID, ownerID, digest, attestation string) (firstSourceRetainedSource, error) {
			source, err := c.sources.SnapshotAttestation(ctx, projectmodule.CandidateSourceScope{ProjectID: projectgraph.ResourceID(projectID), OwnerID: ownerID}, digest, attestation)
			return firstSourceRetainedSource{ProjectID: source.ProjectID.String(), SourceDigest: source.ArtifactDigest, SourceAttestationDigest: source.SourceAttestationDigest}, err
		},
	}
	result.plan = &firstSourceCredentialPlan{authority: c.scope.authority, journal: journal, receipts: c.services.ActivationRepository(), probePolicy: c.analytics.CredentialProbePolicyIdentity}
	result.connections = &firstSourceCredentialBuild{plan: result.plan, targets: c.targets,
		normal: appdeploymentpostgres.NativePlanConnectionAuthorities{BindingEvidence: c.normal, Connections: c.normal},
		connections: func(authorize connectionbinding.RuntimeBindingAuthorizer, pins connectionbinding.LocalCredentialPins) (appdeploymentpostgres.NativePlanConnectionAuthorities, error) {
			runtime := c.runtime()
			if runtime == nil || runtime.gate == nil {
				return appdeploymentpostgres.NativePlanConnectionAuthorities{}, credentialmodule.ErrValidationUnavailable
			}
			leaser, err := c.analytics.NewRuntimeBindingLeaser(analyticsmodule.RuntimeBindingLeaserConfig{Authorize: authorize, Now: time.Now, Audit: c.rotationAudit})
			if err != nil {
				return appdeploymentpostgres.NativePlanConnectionAuthorities{}, err
			}
			if err := leaser.ConfigureProviderAdmission(runtime.gate); err != nil {
				return appdeploymentpostgres.NativePlanConnectionAuthorities{}, err
			}
			if err := leaser.ConfigureLocalCredentials(pins, c.analytics); err != nil {
				return appdeploymentpostgres.NativePlanConnectionAuthorities{}, err
			}
			connections := candidateConnectionLeaser{leaser: leaser, module: c.analytics}
			return appdeploymentpostgres.NativePlanConnectionAuthorities{BindingEvidence: connections, Connections: connections}, nil
		},
	}
	return result, nil
}

func (c firstSourceCredentialComposition) selector() appdeploymentpostgres.NativePlanConnectionSelector {
	if c.connections == nil {
		return nil
	}
	return c.connections.Select
}

func (c firstSourceCredentialComposition) nativeBuild(normal sourceCredentialMutations, reader deploymentmodule.NativeDeliveryReader, activation func() credentialmodule.ActivationService) (sourceCredentialMutations, *firstSourceNativeBuild) {
	if c.connections == nil {
		return normal, nil
	}
	build := &firstSourceNativeBuild{sourceCredentialMutations: normal, connections: c.connections,
		load: func(ctx context.Context, id string) (deployment.DeliveryPlan, error) {
			stored, err := reader.LoadPlan(ctx, id)
			if err != nil {
				return deployment.DeliveryPlan{}, err
			}
			return stored.RichPlan()
		}, activation: activation,
	}
	return build, build
}
