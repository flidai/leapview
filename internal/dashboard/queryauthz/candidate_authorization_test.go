package authz

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/stretchr/testify/require"
)

func TestCandidateQueryCapabilityRejectsOwnershipWorkspaceAndIdentityExpansion(t *testing.T) {
	base := CandidateQueryCapability{
		CandidateID: "cand_1", OwnerPrincipalID: "author_1",
		WorkspaceID:  "sales",
		PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for name, mutate := range map[string]func(*CandidateQueryCapability, *dataquery.Query){
		"owner": func(capability *CandidateQueryCapability, _ *dataquery.Query) {
			capability.OwnerPrincipalID = "author_2"
		},
		"workspace": func(_ *CandidateQueryCapability, query *dataquery.Query) {
			query.WorkspaceID = "operations"
		},
		"candidate": func(_ *CandidateQueryCapability, query *dataquery.Query) {
			query.CandidateID = "cand_2"
		},
		"missing_policy": func(capability *CandidateQueryCapability, _ *dataquery.Query) {
			capability.PolicyDigest = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			capability := base
			request := candidateGovernanceRequest()
			mutate(&capability, &request)
			repository := &candidateAuthorizationRepository{}
			metrics := New(semanticModelMetrics{model: governanceTestModel()}, Options{
				Repo: repository,
				PrincipalFromContext: func(context.Context) (Principal, bool) {
					return Principal{ID: "author_1"}, true
				},
			})
			ctx := WithCandidateQueryCapability(t.Context(), capability)

			if _, _, err := metrics.GovernDataQuery(ctx, request); !IsDenied(err) {
				t.Fatalf("GovernDataQuery() error = %v, want denied candidate expansion", err)
			}
			if len(repository.audits) != 1 || repository.audits[0].Status != "denied" {
				t.Fatalf("candidate denial audits = %#v", repository.audits)
			}
		})
	}
}

func TestCandidateIdentityCannotBeInjectedWithoutServerCapability(t *testing.T) {
	repository := &candidateAuthorizationRepository{}
	metrics := New(semanticModelMetrics{model: governanceTestModel()}, Options{
		Repo: repository,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: "author_1"}, true
		},
	})
	request := candidateGovernanceRequest()
	request.CandidateID = "cand_injected"

	if _, _, err := metrics.GovernDataQuery(t.Context(), request); !IsDenied(err) {
		t.Fatalf("GovernDataQuery() error = %v, want denied injected candidate identity", err)
	}
	if len(repository.audits) != 1 || repository.audits[0].Status != "denied" {
		t.Fatalf("candidate injection audits = %#v", repository.audits)
	}
}

func TestCandidateQueryCapabilityAddsRestrictionsAndEffectivePolicyFingerprint(t *testing.T) {
	repository := &candidateAuthorizationRepository{}
	metrics := New(semanticModelMetrics{model: governanceTestModel()}, Options{
		Repo: repository,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: "author_1"}, true
		},
	})
	capability := CandidateQueryCapability{
		CandidateID: "cand_1", OwnerPrincipalID: "author_1",
		WorkspaceID:  "sales",
		PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Restrictions: []access.DataPolicy{mustCompileTestPolicy(t, access.DataPolicy{
			ID: "candidate_region", WorkspaceID: "sales", PolicyType: "row_filter",
			ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["DK"]}`,
		})},
	}

	governed, _, err := metrics.GovernDataQuery(
		WithCandidateQueryCapability(t.Context(), capability),
		candidateGovernanceRequest(),
	)
	require.NoError(t, err)
	if governed.CandidateID != capability.CandidateID ||
		governed.EffectivePolicyFingerprint == "" {
		t.Fatalf("governed candidate identity = %#v", governed)
	}
	if len(governed.Filters) != 1 ||
		governed.Filters[0].Field != "ratings.region" {
		t.Fatalf("candidate restrictions = %#v", governed.Filters)
	}
	if len(repository.privileges) == 0 || repository.privileges[0] != access.PrivilegePreviewData {
		t.Fatalf("candidate authorization privileges = %#v, want PREVIEW_DATA", repository.privileges)
	}

	changed := capability
	changed.PolicyDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	next, _, err := metrics.GovernDataQuery(
		WithCandidateQueryCapability(t.Context(), changed),
		candidateGovernanceRequest(),
	)
	require.NoError(t, err)
	if next.EffectivePolicyFingerprint == governed.EffectivePolicyFingerprint {
		t.Fatal("candidate policy change reused the effective policy fingerprint")
	}
}

func TestCandidateQueryCapabilityAppliesOnlyRelevantObjectRestrictions(t *testing.T) {
	repository := &candidateAuthorizationRepository{}
	metrics := New(semanticModelMetrics{model: governanceTestModel()}, Options{
		Repo: repository,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: "author_1"}, true
		},
	})
	capability := CandidateQueryCapability{
		CandidateID: "cand_1", OwnerPrincipalID: "author_1", WorkspaceID: "sales",
		PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Restrictions: []access.DataPolicy{mustCompileTestPolicy(t, access.DataPolicy{
			ID: "unrelated", WorkspaceID: "sales",
			ObjectID: "semantic_model:sales:unrelated", PolicyType: "row_filter",
			ExpressionJSON: `{"field":"unrelated.region","operator":"equals","values":["DK"]}`,
		})},
	}
	governed, _, err := metrics.GovernDataQuery(
		WithCandidateQueryCapability(t.Context(), capability),
		candidateGovernanceRequest(),
	)
	require.NoError(t, err)
	if len(governed.Filters) != 0 {
		t.Fatalf("unrelated candidate restriction leaked into query = %#v", governed.Filters)
	}
}

func TestCandidateQueryCapabilityCannotDeleteOrShadowActiveRestrictions(t *testing.T) {
	repository := &candidateAuthorizationRepository{
		policies: []access.DataPolicy{{
			ID: "region", WorkspaceID: "sales", PolicyType: "row_filter",
			ExpressionJSON: `{"field":"ratings.country","operator":"equals","values":["DK"]}`,
		}},
	}
	metrics := New(semanticModelMetrics{model: governanceTestModel()}, Options{
		Repo: repository,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: "author_1"}, true
		},
	})
	capability := CandidateQueryCapability{
		CandidateID: "cand_1", OwnerPrincipalID: "author_1",
		WorkspaceID:  "sales",
		PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Restrictions: []access.DataPolicy{mustCompileTestPolicy(t, access.DataPolicy{
			// Reusing an active ID must append, rather than replace, its current
			// restriction.
			ID: "region", WorkspaceID: "sales", PolicyType: "row_filter",
			ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["Hovedstaden"]}`,
		})},
	}

	governed, _, err := metrics.GovernDataQuery(
		WithCandidateQueryCapability(t.Context(), capability),
		candidateGovernanceRequest(),
	)
	require.NoError(t, err)
	if len(governed.Filters) != 2 ||
		governed.Filters[0].Field != "ratings.country" ||
		governed.Filters[1].Field != "ratings.region" {
		t.Fatalf("active and candidate restrictions = %#v", governed.Filters)
	}
}

func TestCandidateViewAsIntersectsSubjectAndCandidateRestrictions(t *testing.T) {
	repository := &viewAsAuthorizationRepository{
		allow: map[string]bool{
			"author_1:" + string(access.PrivilegeTestDataPolicy): true,
			"viewer_1:" + string(access.PrivilegePreviewData):    true,
		},
		policies: map[string][]access.DataPolicy{
			"viewer_1": {{
				ID: "viewer_country", WorkspaceID: "sales", PolicyType: "row_filter",
				ExpressionJSON: `{"field":"ratings.country","operator":"equals","values":["DK"]}`,
			}},
		},
	}
	metrics := newViewAsMetrics(repository)
	candidate := CandidateQueryCapability{
		CandidateID: "cand_1", OwnerPrincipalID: "author_1",
		WorkspaceID:  "sales",
		PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Restrictions: []access.DataPolicy{mustCompileTestPolicy(t, access.DataPolicy{
			ID: "candidate_region", WorkspaceID: "sales", PolicyType: "row_filter",
			ExpressionJSON: `{"field":"ratings.region","operator":"equals","values":["Hovedstaden"]}`,
		})},
	}
	viewAs := ViewAsCapability{
		ActorPrincipalID: "author_1", SubjectPrincipalID: "viewer_1", WorkspaceID: "sales",
	}
	ctx := WithCandidateQueryCapability(t.Context(), candidate)
	ctx = WithViewAsCapability(ctx, viewAs)

	governed, _, err := metrics.GovernDataQuery(ctx, candidateGovernanceRequest())
	require.NoError(t, err)
	if governed.PrincipalID != "viewer_1" || governed.CandidateID != "cand_1" {
		t.Fatalf("candidate view-as identity = %#v", governed)
	}
	if len(governed.Filters) != 2 ||
		governed.Filters[0].Field != "ratings.country" ||
		governed.Filters[1].Field != "ratings.region" {
		t.Fatalf("candidate view-as restrictions = %#v", governed.Filters)
	}
}

func candidateGovernanceRequest() dataquery.Query {
	return dataquery.Query{
		WorkspaceID: "sales", ModelID: "activity",
		Kind: dataquery.KindModelTableRows, Target: "ratings",
		Fields: []dataquery.Field{{Field: "rating"}},
	}
}

type candidateAuthorizationRepository struct {
	policies   []access.DataPolicy
	audits     []access.AuditEventInput
	privileges []access.Privilege
}

func (repository *candidateAuthorizationRepository) Authorize(
	context.Context,
	string,
	access.Privilege,
	access.ObjectRef,
) (access.AuthorizationDecision, error) {
	return access.AuthorizationDecision{Allowed: true}, nil
}

func (repository *candidateAuthorizationRepository) AuthorizeAny(
	_ context.Context,
	_ string,
	privilege access.Privilege,
	_ []access.ObjectRef,
) (access.AuthorizationDecision, error) {
	repository.privileges = append(repository.privileges, privilege)
	return access.AuthorizationDecision{Allowed: true}, nil
}

func (repository *candidateAuthorizationRepository) ListEffectiveDataPolicies(
	context.Context,
	string,
	access.ObjectRef,
	bool,
) ([]access.DataPolicy, error) {
	return compileTestPolicies(repository.policies)
}

func (repository *candidateAuthorizationRepository) RecordAuditEvent(
	_ context.Context,
	input access.AuditEventInput,
) error {
	repository.audits = append(repository.audits, input)
	return nil
}

var _ access.DataAuthorizationService = (*candidateAuthorizationRepository)(nil)
