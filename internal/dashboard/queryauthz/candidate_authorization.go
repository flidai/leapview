package authz

import (
	"context"
	"fmt"
	"strings"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// CandidateQueryCapability is installed by the candidate runtime adapter after
// it resolves an authenticated author's owned candidate. Candidate policy data
// is deliberately restrictions-only: it may narrow the author's current
// access, but it cannot introduce grants or replace active data policies.
type CandidateQueryCapability struct {
	CandidateID      string
	OwnerPrincipalID string
	ProjectID        projectgraph.ResourceID
	PolicyDigest     string
	Restrictions     []accesssnapshot.DataPolicy
}

type candidateQueryCapabilityKey struct{}

func WithCandidateQueryCapability(ctx context.Context, capability CandidateQueryCapability) context.Context {
	return context.WithValue(ctx, candidateQueryCapabilityKey{}, capability)
}

func candidateQueryCapabilityFromContext(ctx context.Context) (CandidateQueryCapability, bool) {
	capability, ok := ctx.Value(candidateQueryCapabilityKey{}).(CandidateQueryCapability)
	return capability, ok
}

func validateCandidateQueryCapability(
	capability CandidateQueryCapability,
	actor Principal,
	request dataquery.Query,
) (dataquery.Query, error) {
	candidateID := strings.TrimSpace(capability.CandidateID)
	ownerID := strings.TrimSpace(capability.OwnerPrincipalID)
	projectID := capability.ProjectID
	policyDigest := strings.TrimSpace(capability.PolicyDigest)
	if candidateID == "" || ownerID == "" ||
		candidateID != capability.CandidateID ||
		ownerID != capability.OwnerPrincipalID ||
		projectID.String() != strings.TrimSpace(projectID.String()) ||
		projectID == "" ||
		policyDigest != capability.PolicyDigest {
		return request, fmt.Errorf("candidate query capability is incomplete")
	}
	if err := projectID.Validate(); err != nil {
		return request, fmt.Errorf("candidate query project identity is invalid: %w", err)
	}
	if err := digest.ValidateSHA256Identity(policyDigest); err != nil {
		return request, fmt.Errorf("candidate query policy digest is invalid: %w", err)
	}
	if strings.TrimSpace(actor.ID) == "" || actor.ID != ownerID {
		return request, fmt.Errorf("candidate %q is not owned by the authenticated principal", candidateID)
	}
	if request.CandidateID != "" && request.CandidateID != candidateID {
		return request, fmt.Errorf("candidate query identity %q does not match capability %q", request.CandidateID, candidateID)
	}
	if request.ProjectID != projectID {
		return request, fmt.Errorf("candidate query project %q does not match capability project %q", request.ProjectID, projectID)
	}
	for _, policy := range capability.Restrictions {
		if strings.TrimSpace(policy.ID) == "" {
			return request, fmt.Errorf("candidate query restriction is incomplete")
		}
		if err := policy.Resource.Validate(); err != nil {
			return request, fmt.Errorf("candidate query restriction %q resource is invalid: %w", policy.ID, err)
		}
		if !policy.Compiled.Matches(policy.PolicyType, policy.ExpressionJSON) {
			return request, fmt.Errorf("candidate query restriction %q is not compiled", policy.ID)
		}
		switch policy.PolicyType {
		case "row_filter", "column_mask":
		default:
			return request, fmt.Errorf("candidate query restriction %q has unsupported type %q", policy.ID, policy.PolicyType)
		}
	}
	request.CandidateID = candidateID
	request.PrincipalID = ownerID
	return request, nil
}
