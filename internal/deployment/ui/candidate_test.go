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
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), Status: deployment.CandidateFailed,
		FailureReason: "RUNTIME_PREPARATION_FAILED", UpdatedAt: time.Now(),
	}
	var rendered bytes.Buffer
	if err := CandidatePage(candidate).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	body := rendered.String()
	for _, want := range []string{"cand_opaque", "failed", "RUNTIME_PREPARATION_FAILED", "active dashboards were not changed"} {
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
