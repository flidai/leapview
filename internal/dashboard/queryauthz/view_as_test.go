package authz

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/stretchr/testify/require"
)

func TestViewAsRequiresSeparateActorPrivilegeAndAuditsDenial(t *testing.T) {
	repository := &viewAsAuthorizationRepository{
		allow: map[string]bool{
			"viewer_1:" + string(access.PrivilegePreviewData): true,
		},
	}
	metrics := newViewAsMetrics(repository)

	_, _, err := metrics.GovernDataQuery(
		WithViewAsCapability(t.Context(), ViewAsCapability{
			ActorPrincipalID: "author_1", SubjectPrincipalID: "viewer_1", WorkspaceID: "sales",
		}),
		candidateGovernanceRequest(),
	)
	if !IsDenied(err) {
		t.Fatalf("GovernDataQuery() error = %v, want denied view-as", err)
	}
	if len(repository.audits) != 1 {
		t.Fatalf("view-as audits = %#v, want one denial", repository.audits)
	}
	event := repository.audits[0]
	if event.Action != "data_policy.view_as" || event.PrincipalID != "author_1" ||
		event.TargetID != "viewer_1" || event.Status != "denied" {
		t.Fatalf("view-as denial audit = %#v", event)
	}
}

func TestViewAsDoesNotBypassSubjectDataAuthorization(t *testing.T) {
	repository := &viewAsAuthorizationRepository{
		allow: map[string]bool{
			"author_1:" + string(access.PrivilegeTestDataPolicy): true,
		},
	}
	metrics := newViewAsMetrics(repository)

	_, _, err := metrics.GovernDataQuery(
		WithViewAsCapability(t.Context(), ViewAsCapability{
			ActorPrincipalID: "author_1", SubjectPrincipalID: "viewer_1", WorkspaceID: "sales",
		}),
		candidateGovernanceRequest(),
	)
	if !IsDenied(err) {
		t.Fatalf("GovernDataQuery() error = %v, want subject authorization denial", err)
	}
	if len(repository.audits) != 2 {
		t.Fatalf("audits = %#v, want view-as authorization and query denial", repository.audits)
	}
	if repository.audits[0].Action != "data_policy.view_as" ||
		repository.audits[0].Status != "authorized" ||
		repository.audits[1].PrincipalID != "viewer_1" ||
		repository.audits[1].Status != "denied" {
		t.Fatalf("view-as subject denial audits = %#v", repository.audits)
	}
}

func TestViewAsRunsAsSubjectWithSubjectPoliciesAndDistinctFingerprint(t *testing.T) {
	repository := &viewAsAuthorizationRepository{
		allow: map[string]bool{
			"author_1:" + string(access.PrivilegeTestDataPolicy): true,
			"author_1:" + string(access.PrivilegePreviewData):    true,
			"viewer_1:" + string(access.PrivilegePreviewData):    true,
		},
		policies: map[string][]access.DataPolicy{
			"viewer_1": {{
				ID: "viewer_region", WorkspaceID: "sales", PolicyType: "row_filter",
				ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["DK"]}`,
			}},
		},
	}
	metrics := newViewAsMetrics(repository)
	capability := ViewAsCapability{
		ActorPrincipalID: "author_1", SubjectPrincipalID: "viewer_1", WorkspaceID: "sales",
	}

	governed, _, err := metrics.GovernDataQuery(
		WithViewAsCapability(t.Context(), capability),
		candidateGovernanceRequest(),
	)
	require.NoError(t, err)
	if governed.PrincipalID != "viewer_1" || governed.EffectivePolicyFingerprint == "" {
		t.Fatalf("governed view-as identity = %#v", governed)
	}
	if len(governed.Filters) != 1 || governed.Filters[0].Field != "ratings.region" {
		t.Fatalf("view-as policies = %#v", governed.Filters)
	}

	direct, _, err := metrics.GovernDataQuery(t.Context(), candidateGovernanceRequest())
	require.NoError(t, err)
	if direct.EffectivePolicyFingerprint == governed.EffectivePolicyFingerprint {
		t.Fatal("view-as query reused the direct author's policy fingerprint")
	}
}

func newViewAsMetrics(repository *viewAsAuthorizationRepository) Metrics {
	return New(semanticModelMetrics{model: governanceTestModel()}, Options{
		Repo: repository,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: "author_1"}, true
		},
	})
}

type viewAsAuthorizationRepository struct {
	allow    map[string]bool
	policies map[string][]access.DataPolicy
	audits   []access.AuditEventInput
}

func (repository *viewAsAuthorizationRepository) Authorize(
	_ context.Context,
	principalID string,
	privilege access.Privilege,
	_ access.ObjectRef,
) (access.AuthorizationDecision, error) {
	return access.AuthorizationDecision{Allowed: repository.allow[principalID+":"+string(privilege)]}, nil
}

func (repository *viewAsAuthorizationRepository) AuthorizeAny(
	ctx context.Context,
	principalID string,
	privilege access.Privilege,
	objects []access.ObjectRef,
) (access.AuthorizationDecision, error) {
	return repository.Authorize(ctx, principalID, privilege, objects[0])
}

func (repository *viewAsAuthorizationRepository) ListEffectiveDataPolicies(
	_ context.Context,
	principalID string,
	_ access.ObjectRef,
	_ bool,
) ([]access.DataPolicy, error) {
	return compileTestPolicies(repository.policies[principalID])
}

func (repository *viewAsAuthorizationRepository) RecordAuditEvent(
	_ context.Context,
	input access.AuditEventInput,
) error {
	repository.audits = append(repository.audits, input)
	return nil
}

var _ access.DataAuthorizationService = (*viewAsAuthorizationRepository)(nil)
