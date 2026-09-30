package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/credential"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func refreshCredentialFixture(t *testing.T) (refreshRuntimeCredentialReaderFactory, refreshrun.JobRecord, credentialmodule.RuntimeResource, credentialmodule.RuntimeCredentialReference, *foregroundRuntimeHookRepository, *runtimeReaderKeyring, *refreshRuntimeTokenEvidence) {
	t.Helper()
	foreground, _, identity, resource, _, reference := newForegroundRuntimeCredentialAuthorityFixture(t)
	pipelineID := projectgraph.ResourceID("pipeline:refresh")
	plan, err := projectpipelineplan.New(projectpipelineplan.Plan{
		ID: "plan:refresh", PipelineID: pipelineID.String(), ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: "semantic:refresh", ServingGenerationID: identity.GenerationID,
		ArtifactDigest: activeResultIdentityDigest('a'), SelectionDigest: activeResultIdentityDigest('b'),
		MaterializationScope: []string{"orders"}, InvocationSource: refreshrun.TriggerManual, RunAsPrincipalID: foreground.principalID,
	})
	require.NoError(t, err)
	runPair, err := permissions.NewExactPair(access.PermissionCatalogProfile, "pipeline.run", identity.ProjectID.String(), "pipeline", pipelineID.String())
	require.NoError(t, err)
	connectionPair, err := permissions.NewExactPair(access.PermissionCatalogProfile, "connection.use", identity.ProjectID.String(), "connection", resource.ResourceID)
	require.NoError(t, err)
	expiresAt := time.Now().UTC().Add(time.Hour)
	job := refreshrun.JobRecord{
		ID: "job:refresh", RunID: "run:refresh", Identity: identity, PrincipalID: foreground.principalID,
		SemanticModelID: "semantic:refresh", PipelineID: pipelineID, PipelinePlan: &plan,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: pipelineID, TargetRevision: 1,
		TriggerType: refreshrun.TriggerManual, Kind: refreshrun.JobKindRefreshPipeline,
		EstimatedMemoryBytes: 1, LeaseOwner: "worker:refresh", LeaseRevision: 1,
		Authority: jobs.AuthorityEnvelope{
			Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode,
			ActorPrincipalID: foreground.principalID, ExecutionPrincipalID: foreground.principalID,
			Target:      jobs.AuthorityTarget{InstanceID: foreground.instanceID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment, ResourceKind: "pipeline", ResourceID: pipelineID.String()},
			Credential:  &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: "token:refresh", Fingerprint: "fingerprint:refresh", ExpiresAt: expiresAt},
			Permissions: []permissions.Pair{runPair, connectionPair},
		},
	}
	require.NoError(t, job.Validate())
	accessPairs := make([]access.PermissionPair, len(job.Authority.Permissions))
	for i, pair := range job.Authority.Permissions {
		accessPairs[i], err = access.FromContractPermissionPair(pair)
		require.NoError(t, err)
	}
	tokens := &refreshRuntimeTokenEvidence{token: access.APIToken{
		ID: job.Authority.Credential.ID, PrincipalID: job.PrincipalID, TokenFingerprint: job.Authority.Credential.Fingerprint,
		ExpiresAt: expiresAt.Format(time.RFC3339Nano), PermissionProfile: access.PermissionCatalogProfile, Permissions: accessPairs,
	}}
	current := func(_ context.Context, principalID string, pair access.PermissionPair, environment string) (bool, error) {
		return principalID == job.PrincipalID && environment == identity.Environment && access.PermissionSetAllows(accessPairs, pair), nil
	}
	encryptionBinding := runtimeReaderEncryptionBinding(reference.Scope, reference.VersionID)
	repository := &foregroundRuntimeHookRepository{runtimeReaderRepository: runtimeReaderRepository{versions: map[string]credential.StoredVersion{
		reference.VersionID: runtimeReaderStoredVersion(encryptionBinding, `{"password":"queued-refresh-secret"}`),
	}}}
	keys := &runtimeReaderKeyring{deploymentID: encryptionBinding.DeploymentID}
	factory := refreshRuntimeCredentialReaderFactory{
		authority: refreshRuntimeCredentialAuthority{
			instanceID: foreground.instanceID, environment: foreground.environment, provider: foreground.provider,
			evidence: foreground.evidence, owners: foreground.owners, bindings: foreground.bindings, subjects: foreground.subjects,
			revalidator: newAuthorityRevalidator(tokens, nil, nil, current, current, foreground.instanceID, foreground.environment),
		},
		readers: runtimeCredentialReaderFactoryFunc(func(authority credentialmodule.RuntimeUseAuthority) (credentialmodule.RuntimeCredentialReader, error) {
			return credential.NewRuntimeResolver(repository, keys, authority)
		}),
	}
	return factory, job, resource, reference, repository, keys, tokens
}

func TestRefreshRuntimeCredentialReaderUsesOnlyJobBaseAndCapturedToken(t *testing.T) {
	factory, job, resource, reference, repository, keys, tokens := refreshCredentialFixture(t)
	reader, err := factory.reader(job)
	require.NoError(t, err)
	// Queue evidence is detached from mutable slices/pointers retained by a caller.
	job.Authority.Permissions[1].Target.ResourceID = "other"
	job.Authority.Credential.ID = "other-token"
	ctx := accessmodule.WithPrincipal(t.Context(), accessmodule.Principal{ID: "ambient-admin", DevBypass: true})
	var retained map[string]string
	require.NoError(t, reader.WithCredential(ctx, job.Identity, resource, func(got credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
		require.Equal(t, reference, got)
		require.Equal(t, "queued-refresh-secret", fields["password"])
		retained = fields
		return nil
	}))
	require.Equal(t, 2, tokens.calls)
	require.Equal(t, reference.VersionID, repository.versionID)
	require.Equal(t, reference.Scope.OwnerID, repository.ownerID)
	require.Equal(t, 1, keys.decryptCalls)
	require.Empty(t, retained)
	require.Equal(t, make([]byte, len(keys.lastPlaintext)), keys.lastPlaintext)
}

func TestRefreshRuntimeCredentialReaderRejectsAuthorityAndIdentityDriftBeforeDecryption(t *testing.T) {
	for _, scenario := range []string{"candidate identity", "missing captured connection", "wrong connection", "revoked token", "token ceiling", "revoked after storage", "owner after storage", "binding after storage", "endpoint after storage"} {
		t.Run(scenario, func(t *testing.T) {
			factory, job, resource, _, repository, keys, tokens := refreshCredentialFixture(t)
			identity := job.Identity
			switch scenario {
			case "candidate identity":
				identity.GenerationID = "new-candidate-generation"
			case "missing captured connection":
				job.Authority.Permissions = job.Authority.Permissions[:1]
			case "wrong connection":
				resource.ResourceID = "other"
			case "revoked token":
				tokens.revoked = true
			case "token ceiling":
				tokens.token.Permissions = tokens.token.Permissions[:1]
			case "revoked after storage":
				repository.afterRead = func() { tokens.revoked = true }
			case "owner after storage":
				owner := &refreshRuntimeOwner{owner: "customer_credential"}
				factory.authority.owners = owner
				repository.afterRead = func() { owner.owner = "another-owner" }
			case "endpoint after storage":
				binding := factory.authority.bindings.(*testCredentialBindingLookup)
				repository.afterRead = func() { binding.binding.Endpoint.Host = "changed.example" }
			case "binding after storage":
				binding := factory.authority.bindings.(*testCredentialBindingLookup)
				repository.afterRead = func() { binding.binding.Revision++ }
			}
			reader, err := factory.reader(job)
			require.NoError(t, err)
			called := false
			err = reader.WithCredential(t.Context(), identity, resource, func(credentialmodule.RuntimeCredentialReference, map[string]string) error { called = true; return nil })
			require.Error(t, err)
			require.False(t, called)
			require.Zero(t, keys.decryptCalls)
			if scenario == "revoked after storage" || scenario == "owner after storage" || scenario == "binding after storage" || scenario == "endpoint after storage" {
				require.Equal(t, 1, repository.calls)
			} else {
				require.Zero(t, repository.calls)
			}
		})
	}
}

func TestRefreshCredentialPreflightRejectsEveryLocalPinWithoutReadingSecrets(t *testing.T) {
	for _, scenario := range []string{"selected local pin", "unselected local pin", "provider only", "development provider only", "missing commitment", "wrong base", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			factory, job, _, _, repository, keys, _ := refreshCredentialFixture(t)
			ctx := t.Context()
			switch scenario {
			case "unselected local pin":
				job.Authority.Permissions = job.Authority.Permissions[:1]
			case "provider only", "development provider only":
				stub := factory.authority.evidence.releases.(sourceSchemaProvenanceStub)
				stub.provenance.Plan.Bindings[0].CredentialVersionID = ""
				stub.provenance.Plan.Bindings[0].ValidatedVersion = "provider-version"
				factory.authority.evidence.releases = stub
				if scenario == "development provider only" {
					job.Authority = jobs.AuthorityEnvelope{}
				}
			case "missing commitment":
				factory.authority.evidence.commitments.(*committedCredentialGenerationStub).err = errors.New("private database diagnostic")
			case "wrong base":
				stub := factory.authority.evidence.releases.(sourceSchemaProvenanceStub)
				stub.provenance.Plan.Identity.GenerationID = uuid.NewString()
				factory.authority.evidence.releases = stub
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := factory.checkBaseCredentials(ctx, job)
			switch scenario {
			case "selected local pin", "unselected local pin":
				require.ErrorIs(t, err, errRefreshLocalCredentialUnsupported)
			case "provider only", "development provider only":
				require.NoError(t, err)
			case "canceled":
				require.ErrorIs(t, err, context.Canceled)
			default:
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private database diagnostic")
			}
			require.Zero(t, repository.calls)
			require.Zero(t, keys.decryptCalls)
		})
	}
}

func TestRefreshRuntimeCredentialReaderDelegatesToWorkloadGrantNotIssuer(t *testing.T) {
	for _, scenario := range []string{"authorized", "revoked after storage", "grant ceiling changed", "issuer only"} {
		t.Run(scenario, func(t *testing.T) {
			factory, job, resource, _, repository, keys, tokens := refreshCredentialFixture(t)
			job.Authority.Mode = jobs.DelegatedWorkloadMode
			job.Authority.ActorPrincipalID = uuid.NewString()
			job.Authority.Target.ResourceUID = uuid.NewString()
			job.Authority.Credential = nil
			job.Authority.ExecutionGrant = &jobs.ExecutionGrantEvidence{
				ID: "grant:refresh", Fingerprint: "fingerprint:grant", ExpiresAt: time.Now().UTC().Add(time.Hour),
				WorkflowID: "workflow:refresh", WorkflowRevision: "revision:refresh", ClosureDigest: job.PipelinePlan.Digest,
				ParameterDigest: job.PipelinePlan.ParameterDigest, BindingDigest: job.PipelinePlan.BindingDigest,
				DestinationDigest: job.PipelinePlan.DestinationDigest, TriggerDigest: job.PipelinePlan.TriggerDigest,
				RunAsPrincipalID: job.PrincipalID, Environment: job.Identity.Environment,
			}
			evidence := job.Authority.ExecutionGrant
			grants := &refreshRuntimeGrantReader{grant: access.ExecutionGrant{
				ID: evidence.ID, Profile: access.DurableGrantProfile, Fingerprint: evidence.Fingerprint, ExpiresAt: evidence.ExpiresAt,
				Target:               access.DurableGrantTarget{InstanceID: factory.authority.instanceID, ProjectID: job.Identity.ProjectID, ResourceID: job.PipelineID, ResourceKind: projectgraph.KindPipeline, ResourceUID: job.Authority.Target.ResourceUID},
				Issuer:               access.GrantIssuerEvidence{PrincipalID: job.Authority.ActorPrincipalID, Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: "token:issuer", Fingerprint: "fingerprint:issuer:credential"}},
				ExecutionPrincipalID: job.PrincipalID, Permissions: tokens.token.Permissions,
				WorkflowID: evidence.WorkflowID, WorkflowRevision: evidence.WorkflowRevision, ClosureDigest: evidence.ClosureDigest,
				BindingDigest: evidence.BindingDigest, DestinationDigest: evidence.DestinationDigest, TriggerDigest: evidence.TriggerDigest,
			}}
			current := func(_ context.Context, principalID string, pair access.PermissionPair, _ string) (bool, error) {
				if scenario == "issuer only" {
					return principalID == job.Authority.ActorPrincipalID, nil
				}
				return principalID == job.PrincipalID && access.PermissionSetAllows(tokens.token.Permissions, pair), nil
			}
			factory.authority.revalidator = newAuthorityRevalidator(nil, nil, grants, current, current, factory.authority.instanceID, factory.authority.environment)
			if scenario == "revoked after storage" {
				repository.afterRead = func() { grants.grant.RevokedAt = time.Now().UTC() }
			}
			if scenario == "grant ceiling changed" {
				grants.grant.Permissions = grants.grant.Permissions[:1]
			}
			reader, err := factory.reader(job)
			require.NoError(t, err)
			// Prove the reader kept its own immutable grant evidence.
			job.Authority.ExecutionGrant.Fingerprint = "changed-after-bind"
			called := false
			err = reader.WithCredential(t.Context(), job.Identity, resource, func(credentialmodule.RuntimeCredentialReference, map[string]string) error { called = true; return nil })
			if scenario == "authorized" {
				require.NoError(t, err)
				require.True(t, called)
				require.Equal(t, 1, keys.decryptCalls)
			} else {
				require.Error(t, err)
				require.False(t, called)
				require.Zero(t, keys.decryptCalls)
			}
			require.Zero(t, tokens.calls, "delegation must not borrow an initiating token")
		})
	}
}

type refreshRuntimeGrantReader struct{ grant access.ExecutionGrant }

func (reader *refreshRuntimeGrantReader) CurrentExecutionGrant(_ context.Context, id, principalID string) (access.ExecutionGrant, error) {
	if id != reader.grant.ID || principalID != reader.grant.ExecutionPrincipalID {
		return access.ExecutionGrant{}, access.ErrForbidden
	}
	return reader.grant, nil
}

type refreshRuntimeTokenEvidence struct {
	token   access.APIToken
	revoked bool
	calls   int
}

func (evidence *refreshRuntimeTokenEvidence) APITokenAuthorityEvidence(_ context.Context, principalID, tokenID string, now time.Time) (access.APIToken, error) {
	evidence.calls++
	if evidence.revoked || evidence.token.PrincipalID != principalID || evidence.token.ID != tokenID {
		return access.APIToken{}, errors.New("revoked refresh token")
	}
	return evidence.token, nil
}

type refreshRuntimeOwner struct{ owner string }

func (owner *refreshRuntimeOwner) CustomerOwner(context.Context) (string, error) {
	return owner.owner, nil
}
