package sealedcontrol

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

// approvalRepository is intentionally tiny: the production verifier must use
// the service's durable repository, while this test only needs to exercise the
// exact scope checks at the sealed-control boundary.
type approvalRepository struct {
	approval deployment.Approval
}

func (r *approvalRepository) CreateApproval(_ context.Context, approval deployment.Approval) (deployment.Approval, error) {
	if r.approval.ID != "" {
		return deployment.Approval{}, deployment.ErrApprovalConflict
	}
	r.approval = approval
	return approval, nil
}

func (r *approvalRepository) ApprovalByDeployment(_ context.Context, deploymentID string) (deployment.Approval, error) {
	if r.approval.ID == "" || r.approval.DeploymentID != deploymentID {
		return deployment.Approval{}, deployment.ErrApprovalNotFound
	}
	return r.approval, nil
}

func (r *approvalRepository) SaveApproval(_ context.Context, approval deployment.Approval, expectedRevision int64) (deployment.Approval, error) {
	if r.approval.ID == "" || r.approval.Revision != expectedRevision || r.approval.ID != approval.ID {
		return deployment.Approval{}, deployment.ErrApprovalConflict
	}
	r.approval = approval
	return approval, nil
}

func newDurableApprovalVerifierFixture(t *testing.T) (*deployment.ApprovalService, *time.Time, SealBinding, deployment.PublicationIntent) {
	t.Helper()
	now := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	repository := &approvalRepository{}
	var sequence int
	service, err := deployment.NewApprovalService(repository, deployment.ApprovalServiceConfig{
		Lifetime: 30 * time.Minute,
		Now:      func() time.Time { return now },
		NewID: func() (string, error) {
			sequence++
			return fmt.Sprintf("approval-%d", sequence), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	seal := coordinatorSeal()
	generation := coordinatorGeneration(seal)
	publication := coordinatorPublication()
	requested, err := service.Request(t.Context(), deployment.ApprovalRequest{
		ProjectID: publication.ProjectID.String(), DeploymentID: publication.ID,
		Environment: publication.Environment, RequestDigest: publication.RequestDigest,
		ReleaseID: "release-1",
		RequestedBy: deployment.ApprovalActor{
			PrincipalID: "publisher", CredentialClass: deployment.CredentialClassWorkload,
			CredentialID: "publisher-credential", CredentialExpiresAt: now.Add(time.Hour),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(t.Context(), deployment.ApprovalTransition{
		ProjectID: publication.ProjectID.String(), DeploymentID: publication.ID,
		ApprovalID: requested.ID, ExpectedRevision: requested.Revision,
		Actor: deployment.ApprovalActor{
			PrincipalID: "reviewer", CredentialClass: deployment.CredentialClassHuman,
			CredentialID: "reviewer-credential", CredentialExpiresAt: now.Add(time.Hour),
		},
	}); err != nil {
		t.Fatal(err)
	}
	return service, &now, SealBinding{
		Seal: seal, DeploymentID: publication.ID, CandidateID: generation.CandidateID, GenerationID: generation.ID, PlanDigest: generation.PlanDigest,
		ServingArtifactID: generation.ServingArtifactID, ApprovalReleaseID: "release-1",
	}, publication
}

func TestDurableApprovalVerifierBindsCandidatePlanAndRelease(t *testing.T) {
	service, _, binding, publication := newDurableApprovalVerifierFixture(t)
	verify := DurableApprovalVerifier(service)
	if err := verify(t.Context(), binding, publication); err != nil {
		t.Fatalf("exact approval rejected: %v", err)
	}

	tests := map[string]struct {
		binding     SealBinding
		publication deployment.PublicationIntent
		want        error
	}{
		"replacement candidate": {
			binding: func() SealBinding {
				copy := binding
				copy.CandidateID = "candidate-2"
				return copy
			}(),
			publication: publication,
			want:        deployment.ErrApprovalScope,
		},
		"replanned publication": {
			binding: binding,
			publication: func() deployment.PublicationIntent {
				copy := publication
				copy.PlanDigest = coordinatorDigest('f')
				return copy
			}(),
			want: deployment.ErrApprovalScope,
		},
		"replacement serving artifact": {
			binding: func() SealBinding {
				copy := binding
				copy.ServingArtifactID = "artifact-2"
				return copy
			}(),
			publication: publication,
			want:        deployment.ErrApprovalScope,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := verify(t.Context(), test.binding, test.publication); !errors.Is(err, test.want) {
				t.Fatalf("approval error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDurableApprovalVerifierRejectsExpiredAndRevokedApprovals(t *testing.T) {
	service, now, binding, publication := newDurableApprovalVerifierFixture(t)
	verify := DurableApprovalVerifier(service)
	*now = now.Add(30 * time.Minute)
	if err := verify(t.Context(), binding, publication); !errors.Is(err, deployment.ErrApprovalExpired) {
		t.Fatalf("expired approval error = %v, want %v", err, deployment.ErrApprovalExpired)
	}

	service, now, binding, publication = newDurableApprovalVerifierFixture(t)
	verify = DurableApprovalVerifier(service)
	approval, err := service.Current(t.Context(), binding.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Revoke(t.Context(), deployment.ApprovalTransition{
		ProjectID: publication.ProjectID.String(), DeploymentID: binding.DeploymentID,
		ApprovalID: approval.ID, ExpectedRevision: approval.Revision,
		Actor: deployment.ApprovalActor{
			PrincipalID: "revoker", CredentialClass: deployment.CredentialClassHuman,
			CredentialID: "revoker-credential", CredentialExpiresAt: now.Add(time.Hour),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := verify(t.Context(), binding, publication); !errors.Is(err, deployment.ErrApprovalRequired) {
		t.Fatalf("revoked approval error = %v, want %v", err, deployment.ErrApprovalRequired)
	}
}

func TestDurableApprovalVerifierRequiresReleaseAndCandidateIdentity(t *testing.T) {
	service, _, binding, publication := newDurableApprovalVerifierFixture(t)
	verify := DurableApprovalVerifier(service)
	missingCandidate := binding
	missingCandidate.CandidateID = ""
	if err := verify(t.Context(), missingCandidate, publication); !errors.Is(err, deployment.ErrApprovalScope) {
		t.Fatalf("missing candidate error = %v, want %v", err, deployment.ErrApprovalScope)
	}
	missingRelease := binding
	missingRelease.ServingArtifactID = ""
	if err := verify(t.Context(), missingRelease, publication); !errors.Is(err, deployment.ErrApprovalScope) {
		t.Fatalf("missing release error = %v, want %v", err, deployment.ErrApprovalScope)
	}
	if err := DurableApprovalVerifier(nil)(t.Context(), binding, publication); !errors.Is(err, deployment.ErrApprovalRequired) {
		t.Fatalf("nil service error = %v, want %v", err, deployment.ErrApprovalRequired)
	}
}
