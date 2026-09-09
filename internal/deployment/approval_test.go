package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestApprovalBindsDecisionToExactDeploymentPlan(t *testing.T) {
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	repository := newApprovalMemoryRepository()
	service := mustApprovalService(t, repository, &now)
	requester := ApprovalActor{
		PrincipalID: "publisher", CredentialClass: CredentialClassWorkload,
		CredentialID: "session_publish", CredentialExpiresAt: now.Add(time.Hour),
	}
	approval, err := service.Request(t.Context(), ApprovalRequest{
		ProjectID: "finance", DeploymentID: "deployment_1",
		Environment: "prod", RequestDigest: "sha256:plan",
		ReleaseID: "release_1", RequestedBy: requester,
	})
	require.NoError(t, err)
	now = now.Add(time.Minute)
	approver := ApprovalActor{
		PrincipalID: "reviewer", CredentialClass: CredentialClassHuman,
		CredentialID: "session_review", CredentialExpiresAt: now.Add(time.Hour),
	}
	approved, err := service.Approve(t.Context(), ApprovalTransition{
		ProjectID: "finance", DeploymentID: "deployment_1",
		ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Actor: approver,
	})
	require.NoError(t, err)
	if approved.Status != ApprovalApproved || approved.ApprovedBy != approver.PrincipalID {
		t.Fatalf("approved = %#v", approved)
	}

	for name, check := range map[string]ApprovalActivation{
		"project": {
			ProjectID: "other", DeploymentID: "deployment_1", Environment: "prod",
			RequestDigest: "sha256:plan", ReleaseID: "release_1",
		},
		"deployment": {
			ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod",
			RequestDigest: "sha256:plan", ReleaseID: "release_1",
		},
		"environment": {
			ProjectID: "finance", DeploymentID: "deployment_1", Environment: "staging",
			RequestDigest: "sha256:plan", ReleaseID: "release_1",
		},
		"plan": {
			ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod",
			RequestDigest: "sha256:other", ReleaseID: "release_1",
		},
		"release": {
			ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod",
			RequestDigest: "sha256:plan", ReleaseID: "release_2",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "deployment" {
				check.DeploymentID = "deployment_2"
				if _, err := service.AuthorizeActivation(t.Context(), check); !errors.Is(err, ErrApprovalRequired) {
					t.Fatalf("AuthorizeActivation() error = %v, want %v", err, ErrApprovalRequired)
				}
				return
			}
			if _, err := service.AuthorizeActivation(t.Context(), check); !errors.Is(err, ErrApprovalScope) {
				t.Fatalf("AuthorizeActivation() error = %v, want %v", err, ErrApprovalScope)
			}
		})
	}
	authorized, err := service.AuthorizeActivation(t.Context(), ApprovalActivation{
		ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod",
		RequestDigest: "sha256:plan", ReleaseID: "release_1",
	})
	if err != nil || authorized.ID != approved.ID {
		t.Fatalf("AuthorizeActivation() = %#v, %v", authorized, err)
	}
}

func TestValidateApprovalActivationRechecksExactScopeAndFreshness(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	service := mustApprovalService(t, newApprovalMemoryRepository(), &now)
	approval := approveApproval(t, service, &now)
	request := approvalActivation()
	if err := ValidateApprovalActivation(approval, request, now); err != nil {
		t.Fatalf("matching approval rejected: %v", err)
	}
	changed := request
	changed.RequestDigest = "sha256:changed-plan"
	if err := ValidateApprovalActivation(approval, changed, now); !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("changed request digest error = %v, want ErrApprovalScope", err)
	}
	revoked := approval
	revoked.Status, revoked.RevokedBy, revoked.RevokedAt = ApprovalRevoked, "security-reviewer", now
	if err := ValidateApprovalActivation(revoked, request, now); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("revoked approval error = %v, want ErrApprovalRequired", err)
	}
	if err := ValidateApprovalActivation(approval, request, approval.ExpiresAt); !errors.Is(err, ErrApprovalExpired) {
		t.Fatalf("stale approval error = %v, want ErrApprovalExpired", err)
	}
}

func TestApprovalBindsCanonicalPlanAndEvidenceDigests(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	repository := newApprovalMemoryRepository()
	service := mustApprovalService(t, repository, &now)
	request := approvalRequest(now, ApprovalActor{
		PrincipalID: "publisher", CredentialClass: CredentialClassWorkload,
		CredentialID: "publisher", CredentialExpiresAt: now.Add(time.Hour),
	})
	request.PlanDigest = " " + approvalDigest('a') + " "
	request.EvidenceDigest = " " + approvalDigest('b') + " "
	approval, err := service.Request(t.Context(), request)
	require.NoError(t, err)
	if approval.PlanDigest != approvalDigest('a') || approval.EvidenceDigest != approvalDigest('b') {
		t.Fatalf("approval digests = %q/%q, want trimmed canonical identities", approval.PlanDigest, approval.EvidenceDigest)
	}

	activation := ApprovalActivation{
		ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod",
		RequestDigest: request.RequestDigest, PlanDigest: approval.PlanDigest,
		EvidenceDigest: approval.EvidenceDigest, ReleaseID: request.ReleaseID,
	}
	if err := ValidateApprovalActivation(approval, activation, now); !errors.Is(err, ErrApprovalRequired) {
		// The request is intentionally pending; this assertion also makes sure
		// the exact evidence pair did not produce a scope error.
		t.Fatalf("pending exact activation error = %v, want ErrApprovalRequired", err)
	}

	changedPlan := activation
	changedPlan.PlanDigest = approvalDigest('c')
	if err := ValidateApprovalActivation(approval, changedPlan, now); !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("changed plan digest error = %v, want ErrApprovalScope", err)
	}
	changedEvidence := activation
	changedEvidence.EvidenceDigest = approvalDigest('d')
	if err := ValidateApprovalActivation(approval, changedEvidence, now); !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("changed evidence digest error = %v, want ErrApprovalScope", err)
	}

	request.EvidenceDigest = approvalDigest('d')
	if _, err := service.Request(t.Context(), request); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("changed evidence request error = %v, want ErrApprovalConflict", err)
	}
}

func TestApprovalRequiresCompleteCanonicalPlanEvidencePair(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	service := mustApprovalService(t, newApprovalMemoryRepository(), &now)
	base := approvalRequest(now, ApprovalActor{
		PrincipalID: "publisher", CredentialClass: CredentialClassWorkload,
		CredentialID: "publisher", CredentialExpiresAt: now.Add(time.Hour),
	})
	for name, mutate := range map[string]func(*ApprovalRequest){
		"plan without evidence": func(request *ApprovalRequest) { request.PlanDigest = approvalDigest('a') },
		"evidence without plan": func(request *ApprovalRequest) { request.EvidenceDigest = approvalDigest('b') },
		"noncanonical plan": func(request *ApprovalRequest) {
			request.PlanDigest, request.EvidenceDigest = "sha256:plan", approvalDigest('b')
		},
		"noncanonical evidence": func(request *ApprovalRequest) {
			request.PlanDigest, request.EvidenceDigest = approvalDigest('a'), "sha256:evidence"
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := service.Request(t.Context(), request); !errors.Is(err, ErrApprovalInvalid) {
				t.Fatalf("Request() error = %v, want ErrApprovalInvalid", err)
			}
		})
	}

	// Empty together remains accepted for legacy approvals.
	if _, err := service.Request(t.Context(), base); err != nil {
		t.Fatalf("legacy request with no plan/evidence digests: %v", err)
	}
}

func TestApprovalThreatModelFailsClosed(t *testing.T) {
	start := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		run  func(*testing.T, *ApprovalService, *approvalMemoryRepository, *time.Time)
	}{
		{
			name: "request credential expired",
			run: func(t *testing.T, service *ApprovalService, _ *approvalMemoryRepository, now *time.Time) {
				_, err := service.Request(t.Context(), approvalRequest(*now, ApprovalActor{
					PrincipalID: "publisher", CredentialClass: CredentialClassWorkload,
					CredentialID: "workload", CredentialExpiresAt: now.Add(-time.Second),
				}))
				if !errors.Is(err, ErrApprovalCredentialExpired) {
					t.Fatalf("Request() error = %v", err)
				}
			},
		},
		{
			name: "self approval",
			run: func(t *testing.T, service *ApprovalService, _ *approvalMemoryRepository, now *time.Time) {
				approval := requestApproval(t, service, *now)
				_, err := service.Approve(t.Context(), ApprovalTransition{
					ProjectID: "finance", DeploymentID: "deployment_1",
					ApprovalID: approval.ID, ExpectedRevision: approval.Revision,
					Actor: ApprovalActor{
						PrincipalID: "publisher", CredentialClass: CredentialClassHuman,
						CredentialID: "other", CredentialExpiresAt: now.Add(time.Hour),
					},
				})
				if !errors.Is(err, ErrApprovalSeparationOfDuty) {
					t.Fatalf("Approve() error = %v", err)
				}
			},
		},
		{
			name: "approval credential bounds decision lifetime",
			run: func(t *testing.T, service *ApprovalService, _ *approvalMemoryRepository, now *time.Time) {
				approval := requestApproval(t, service, *now)
				expires := now.Add(2 * time.Minute)
				approved, err := service.Approve(t.Context(), ApprovalTransition{
					ProjectID: "finance", DeploymentID: "deployment_1",
					ApprovalID: approval.ID, ExpectedRevision: approval.Revision,
					Actor: ApprovalActor{
						PrincipalID: "reviewer", CredentialClass: CredentialClassWorkload,
						CredentialID: "reviewer_workload", CredentialExpiresAt: expires,
					},
				})
				require.NoError(t, err)
				if !approved.ExpiresAt.Equal(expires) {
					t.Fatalf("approval expiry = %s, want %s", approved.ExpiresAt, expires)
				}
				if !approved.ApprovalCredentialExpiresAt.Equal(expires) {
					t.Fatalf(
						"approval credential expiry = %s, want %s",
						approved.ApprovalCredentialExpiresAt,
						expires,
					)
				}
				*now = expires
				_, err = service.AuthorizeActivation(t.Context(), approvalActivation())
				if !errors.Is(err, ErrApprovalExpired) {
					t.Fatalf("AuthorizeActivation() error = %v", err)
				}
			},
		},
		{
			name: "revocation",
			run: func(t *testing.T, service *ApprovalService, _ *approvalMemoryRepository, now *time.Time) {
				approval := approveApproval(t, service, now)
				revoked, err := service.Revoke(t.Context(), ApprovalTransition{
					ProjectID: "finance", DeploymentID: "deployment_1",
					ApprovalID: approval.ID, ExpectedRevision: approval.Revision,
					Actor: ApprovalActor{
						PrincipalID: "reviewer", CredentialClass: CredentialClassHuman,
						CredentialID: "reviewer", CredentialExpiresAt: now.Add(time.Hour),
					},
				})
				require.NoError(t, err)
				if revoked.Status != ApprovalRevoked {
					t.Fatalf("revoked status = %q", revoked.Status)
				}
				if _, err := service.AuthorizeActivation(t.Context(), approvalActivation()); !errors.Is(err, ErrApprovalRequired) {
					t.Fatalf("AuthorizeActivation() error = %v", err)
				}
			},
		},
		{
			name: "optimistic replay",
			run: func(t *testing.T, service *ApprovalService, _ *approvalMemoryRepository, now *time.Time) {
				approval := requestApproval(t, service, *now)
				transition := ApprovalTransition{
					ProjectID: "finance", DeploymentID: "deployment_1",
					ApprovalID: approval.ID, ExpectedRevision: approval.Revision,
					Actor: ApprovalActor{
						PrincipalID: "reviewer", CredentialClass: CredentialClassHuman,
						CredentialID: "reviewer", CredentialExpiresAt: now.Add(time.Hour),
					},
				}
				if _, err := service.Approve(t.Context(), transition); err != nil {
					t.Fatal(err)
				}
				if _, err := service.Revoke(t.Context(), transition); !errors.Is(err, ErrApprovalConflict) {
					t.Fatalf("stale Revoke() error = %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := start
			repository := newApprovalMemoryRepository()
			service := mustApprovalService(t, repository, &now)
			test.run(t, service, repository, &now)
		})
	}
}

func TestDenyRecordsBoundDecisionAndCannotActivate(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	service := mustApprovalService(t, newApprovalMemoryRepository(), &now)
	approval := requestApproval(t, service, now)

	denied, err := service.Deny(t.Context(), ApprovalTransition{
		ProjectID: "finance", DeploymentID: "deployment_1",
		ApprovalID: approval.ID, ExpectedRevision: approval.Revision,
		Actor: ApprovalActor{
			PrincipalID: "reviewer", CredentialClass: CredentialClassHuman,
			CredentialID:        "session_reviewer",
			CredentialExpiresAt: now.Add(15 * time.Minute),
		},
	})
	require.NoError(t, err)
	if denied.Status != ApprovalDenied || denied.ApprovedBy != "reviewer" {
		t.Fatalf("denied approval = %#v", denied)
	}
	if _, err := service.AuthorizeActivation(t.Context(), approvalActivation()); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("activation error = %v, want approval required", err)
	}
}

func TestCurrentPersistsExpiryForStatusReporting(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	repository := newApprovalMemoryRepository()
	service := mustApprovalService(t, repository, &now)
	approval := requestApproval(t, service, now)
	now = approval.ExpiresAt

	current, err := service.Current(t.Context(), approval.DeploymentID)
	require.NoError(t, err)
	if current.Status != ApprovalExpired || repository.approval.Status != ApprovalExpired {
		t.Fatalf("current approval = %#v, persisted = %#v", current, repository.approval)
	}
}

func TestExpiredApprovalIsClosedAndCanBeRequestedAgain(t *testing.T) {
	now := time.Date(2026, 7, 30, 11, 0, 0, 0, time.UTC)
	repository := newApprovalMemoryRepository()
	service := mustApprovalService(t, repository, &now)
	first := requestApproval(t, service, now)

	now = first.ExpiresAt
	replacement, err := service.Request(t.Context(), approvalRequest(now, ApprovalActor{
		PrincipalID: "publisher", CredentialClass: CredentialClassHuman,
		CredentialID: "publisher_next", CredentialExpiresAt: now.Add(time.Hour),
	}))
	require.NoError(t, err)
	if replacement.ID == first.ID || replacement.Status != ApprovalPending {
		t.Fatalf("replacement = %#v, want a new pending approval", replacement)
	}
	if got := repository.history[0].Status; got != ApprovalExpired {
		t.Fatalf("prior approval status = %q, want %q", got, ApprovalExpired)
	}
}

func approvalRequest(now time.Time, actor ApprovalActor) ApprovalRequest {
	return ApprovalRequest{
		ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod",
		RequestDigest: "sha256:plan", ReleaseID: "release_1", RequestedBy: actor,
	}
}

func approvalDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func approvalActivation() ApprovalActivation {
	return ApprovalActivation{
		ProjectID: "finance", DeploymentID: "deployment_1", Environment: "prod",
		RequestDigest: "sha256:plan", ReleaseID: "release_1",
	}
}

func requestApproval(t *testing.T, service *ApprovalService, now time.Time) Approval {
	t.Helper()
	approval, err := service.Request(t.Context(), approvalRequest(now, ApprovalActor{
		PrincipalID: "publisher", CredentialClass: CredentialClassHuman,
		CredentialID: "publisher", CredentialExpiresAt: now.Add(time.Hour),
	}))
	require.NoError(t, err)
	return approval
}

func approveApproval(t *testing.T, service *ApprovalService, now *time.Time) Approval {
	t.Helper()
	approval := requestApproval(t, service, *now)
	approved, err := service.Approve(t.Context(), ApprovalTransition{
		ProjectID: "finance", DeploymentID: "deployment_1",
		ApprovalID: approval.ID, ExpectedRevision: approval.Revision,
		Actor: ApprovalActor{
			PrincipalID: "reviewer", CredentialClass: CredentialClassHuman,
			CredentialID: "reviewer", CredentialExpiresAt: now.Add(time.Hour),
		},
	})
	require.NoError(t, err)
	return approved
}

func mustApprovalService(t *testing.T, repository ApprovalRepository, now *time.Time) *ApprovalService {
	t.Helper()
	var sequence int
	service, err := NewApprovalService(repository, ApprovalServiceConfig{
		Lifetime: 30 * time.Minute,
		Now:      func() time.Time { return *now },
		NewID: func() (string, error) {
			sequence++
			return fmt.Sprintf("approval_%d", sequence), nil
		},
	})
	require.NoError(t, err)
	return service
}

type approvalMemoryRepository struct {
	approval Approval
	history  []Approval
}

func newApprovalMemoryRepository() *approvalMemoryRepository {
	return &approvalMemoryRepository{}
}

func (repository *approvalMemoryRepository) CreateApproval(_ context.Context, approval Approval) (Approval, error) {
	if repository.approval.ID != "" &&
		repository.approval.Status != ApprovalDenied &&
		repository.approval.Status != ApprovalRevoked &&
		repository.approval.Status != ApprovalExpired {
		return Approval{}, ErrApprovalConflict
	}
	if repository.approval.ID != "" {
		repository.history = append(repository.history, repository.approval)
	}
	repository.approval = approval
	return approval, nil
}

func (repository *approvalMemoryRepository) ApprovalByDeployment(_ context.Context, deploymentID string) (Approval, error) {
	if repository.approval.DeploymentID != deploymentID {
		return Approval{}, ErrApprovalNotFound
	}
	return repository.approval, nil
}

func (repository *approvalMemoryRepository) SaveApproval(_ context.Context, approval Approval, expectedRevision int64) (Approval, error) {
	if repository.approval.ID != approval.ID || repository.approval.Revision != expectedRevision {
		return Approval{}, ErrApprovalConflict
	}
	repository.approval = approval
	return approval, nil
}
