package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

func TestCandidatePageRendersBoundedStatusWithoutArtifactOrOwnerDetails(t *testing.T) {
	candidate := deployment.Candidate{
		ID: "cand_opaque", Scope: deployment.CandidateScope{ProjectID: "finance", Environment: "prod"}, TargetID: "lvinst_prod", OwnerID: "principal_secret",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Status: deployment.CandidateFailed, Revision: 1,
		FailureReason: "RUNTIME_PREPARATION_FAILED", UpdatedAt: time.Now(),
	}
	var rendered bytes.Buffer
	if err := CandidatePage(candidate).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	body := rendered.String()
	for _, want := range []string{"cand_opaque", "failed", "RUNTIME_PREPARATION_FAILED", "active dashboards were not changed", `href="/candidates/cand_opaque"`, `href="/candidates/cand_opaque/review"`, "Reviewer handoff", "Revision", "Diagnostic code", "CLI/API-only"} {
		if !strings.Contains(body, want) {
			t.Fatalf("candidate page is missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{candidate.ArtifactDigest, candidate.OwnerID, candidate.Scope.ProjectID.String(), candidate.TargetID} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("candidate page leaked %q:\n%s", forbidden, body)
		}
	}
}

func TestCandidateReviewPageRendersEvidenceWithoutOwnerPreviewAccess(t *testing.T) {
	candidate := deployment.Candidate{
		ID: "cand_review", Scope: deployment.CandidateScope{ProjectID: "finance", Environment: "prod"}, TargetID: "lvinst_prod", OwnerID: "principal_secret",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), Status: deployment.CandidateReady,
		ProvenanceDigest: "sha256:" + strings.Repeat("c", 64), Revision: 4,
		CreatedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 8, 23, 12, 1, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC),
	}
	var rendered bytes.Buffer
	if err := CandidateReviewPage(candidate).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	body := rendered.String()
	for _, want := range []string{"cand_review", "ready", "Revision", "Project", "finance", "Environment", "prod", candidate.ArtifactDigest, candidate.ProvenanceDigest, "approve the exact candidate", "CLI/API-only"} {
		if !strings.Contains(body, want) {
			t.Fatalf("candidate review page is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `href="/candidates/cand_review"`) {
		t.Fatalf("review page exposed owner preview link:\n%s", body)
	}
	for _, forbidden := range []string{candidate.OwnerID, candidate.TargetID} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("candidate review page leaked %q:\n%s", forbidden, body)
		}
	}
}
