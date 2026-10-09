package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type firstSourcePlanAuthorityFixture struct {
	*firstSourcePreparationServiceFixture
	authority *firstSourceCredentialPlan
	request   deploymentmodule.NativeDeliveryPlanRequest
	target    deploymentpostgres.DeliveryTarget
	candidate deployment.CandidateConnectionRequest
}

func newFirstSourcePlanAuthorityFixture(t *testing.T) *firstSourcePlanAuthorityFixture {
	t.Helper()
	f := newFirstSourcePreparationServiceFixture(t)
	_, err := f.service.Prepare(f.ctx, f.actor, f.resource, f.request)
	require.NoError(t, err)
	request := deploymentmodule.NativeDeliveryPlanRequest{ProjectID: projectgraph.ResourceID(f.resource.ProjectID), TargetID: f.resource.TargetID, Environment: f.resource.Environment,
		PrincipalID: f.request.PublisherID, SourceOwnerID: f.request.SourceOwnerID, Operation: "code_change", SourceDigest: f.request.SourceDigest, SourceAttestationDigest: f.request.SourceAttestationDigest,
		IdempotencyKey: f.request.PlanIdempotencyKey, FirstSourcePreparationID: f.request.PreparationID}
	target := deploymentpostgres.DeliveryTarget{TargetID: request.TargetID, ProjectID: request.ProjectID.String(), Environment: request.Environment, TargetRevision: 1}
	candidate := deployment.CandidateConnectionRequest{CandidateID: "inspect-first-source", Actor: request.PrincipalID, TargetID: request.TargetID,
		Identity:     projectgraph.ServingIdentity{ProjectID: request.ProjectID, Environment: request.Environment, GenerationID: "inspect-first-source"},
		Requirements: []deployment.CandidateConnectionRequirement{{ConnectionID: projectgraph.ResourceID(f.resource.ResourceID), ConnectorKind: "postgres"}}}
	return &firstSourcePlanAuthorityFixture{firstSourcePreparationServiceFixture: f, request: request, target: target, candidate: candidate,
		authority: &firstSourceCredentialPlan{authority: f.service.authority, journal: f.service.preparations, receipts: f.services.ActivationRepository(), probePolicy: f.probe.CredentialProbePolicyIdentity}}
}

func (f *firstSourcePlanAuthorityFixture) plan(t *testing.T, evidence []deployment.CandidateConnectionEvidence) deployment.DeliveryPlan {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	digest, err := deployment.BindingFingerprint(evidence)
	require.NoError(t, err)
	var plan deployment.DeliveryPlan
	plan.ID, plan.ProjectID, plan.TargetID, plan.Environment = id.String(), f.request.ProjectID, f.request.TargetID, f.request.Environment
	plan.ActorID, plan.SourceOwnerID = f.request.PrincipalID, f.request.SourceOwnerID
	plan.Operation, plan.SourceDigest = deployment.DeliveryOperationCodeChange, f.request.SourceDigest
	plan.Provenance.AttestationDigest = f.request.SourceAttestationDigest
	plan.Execution.BindingDigest, plan.BaseTargetRevision = digest, f.target.TargetRevision
	return plan
}

// These tests qualify the actual preparation journal/authority boundary. The
// native coordinator's separate PG tests cover the full plan transaction; the
// production first-publication journey remains a separate integration gate.
func TestFirstSourceNativePlanRequiresCurrentValidatingOperator(t *testing.T) {
	f := newFirstSourcePlanAuthorityFixture(t)
	require.Equal(t, f.actor, f.request.PrincipalID)
	_, err := f.authority.Resolve(t.Context(), f.request, f.target, f.candidate)
	require.Error(t, err, "stored preparation cannot replace current request authority")
	evidence, err := f.authority.Resolve(f.ctx, f.request, f.target, f.candidate)
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	require.Equal(t, f.firstSourcePreparationServiceFixture.request.VersionID, evidence[0].CredentialVersionID)
	require.Empty(t, evidence[0].ProviderVersion)
	plan := f.plan(t, evidence)
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	require.NoError(t, f.authority.BindTx(f.ctx, tx, f.request, f.target, plan))
	require.NoError(t, tx.Commit(t.Context()))
	tx, err = f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	require.NoError(t, f.authority.ValidateReplayTx(t.Context(), tx, f.request, plan))
	changed := plan
	changed.Execution.BindingDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	require.Error(t, f.authority.ValidateReplayTx(t.Context(), tx, f.request, changed))
	require.Equal(t, 1, f.probe.calls, "planning performs no source credential probe")
}

func TestFirstSourceNativePlanRejectsMovingPublisherSourceAndTargetIntent(t *testing.T) {
	f := newFirstSourcePlanAuthorityFixture(t)
	for _, scenario := range []string{"publisher", "source-owner", "source", "attestation", "operation-key", "preparation", "target-revision", "foreign-connection", "extra-connection"} {
		t.Run(scenario, func(t *testing.T) {
			request, target, candidate := f.request, f.target, f.candidate
			candidate.Requirements = append([]deployment.CandidateConnectionRequirement(nil), candidate.Requirements...)
			switch scenario {
			case "publisher":
				request.PrincipalID = uuid.NewString()
			case "source-owner":
				request.SourceOwnerID = uuid.NewString()
			case "source":
				request.SourceDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "attestation":
				request.SourceAttestationDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			case "operation-key":
				request.IdempotencyKey = "different-key"
			case "preparation":
				request.FirstSourcePreparationID = uuid.NewString()
			case "target-revision":
				target.TargetRevision++
			case "foreign-connection":
				candidate.Requirements[0].ConnectionID = "connection:other"
			case "extra-connection":
				candidate.Requirements = append(candidate.Requirements, candidate.Requirements[0])
			}
			_, err := f.authority.Resolve(f.ctx, request, target, candidate)
			require.Error(t, err)
		})
	}
}

func TestFirstSourceNativePlanRechecksOperatorAndProbePolicyBeforeLink(t *testing.T) {
	for _, revoke := range []string{"grant", "probe-policy", "session"} {
		t.Run(revoke, func(t *testing.T) {
			f := newFirstSourcePlanAuthorityFixture(t)
			evidence, err := f.authority.Resolve(f.ctx, f.request, f.target, f.candidate)
			require.NoError(t, err)
			plan := f.plan(t, evidence)
			if revoke == "probe-policy" {
				f.probe.policy = "changed-production-policy"
			} else if revoke == "session" {
				require.NoError(t, f.repository.DeleteSession(t.Context(), f.session))
			} else {
				grant, err := f.admission.Intent.Grant()
				require.NoError(t, err)
				grant.Permissions = grant.Permissions[1:]
				_, err = f.repository.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: access.AuthorizationPolicyScope{TargetID: f.target.TargetID, ProjectID: f.target.ProjectID, Environment: f.target.Environment}, Grant: grant, ExpectedRevision: f.admission.PolicyRevision, IdempotencyKey: "native-first-source-revoke"})
				require.NoError(t, err)
			}
			tx, err := f.pool.Begin(t.Context())
			require.NoError(t, err)
			defer tx.Rollback(context.Background())
			require.Error(t, f.authority.BindTx(f.ctx, tx, f.request, f.target, plan))
			_, err = f.service.preparations.StoredPlanLinkTx(t.Context(), tx, f.target.TargetID, plan.ID)
			require.ErrorIs(t, err, credentialmodule.ErrValidationNotFound)
		})
	}
}
