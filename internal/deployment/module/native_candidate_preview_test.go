package module

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

func TestResolveOwnedCandidateUsesNativeEvidenceAndCanonicalPreview(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	m := nativeReadModule(rows)
	m.canonicalOrigin = "https://prod.leapview.example/"
	richPlan, err := rows.plan.RichPlan()
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := m.ResolveOwnedCandidate(t.Context(), rows.candidate.CandidateID, rows.attempt.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ID != rows.candidate.CandidateID || candidate.OwnerID != rows.attempt.OwnerID || candidate.TargetID != rows.plan.TargetID || candidate.Scope.ProjectID.String() != "finance" || candidate.Scope.Environment != "prod" {
		t.Fatalf("candidate identity = %#v", candidate)
	}
	if candidate.Status != deployment.CandidateReady || candidate.Revision != rows.candidate.CandidateRevision || candidate.ArtifactDigest != richPlan.SourceDigest || candidate.ProvenanceDigest != richPlan.ProvenanceDigest {
		t.Fatalf("candidate evidence = %#v", candidate)
	}
	if candidate.PreviewURL != "https://prod.leapview.example/candidates/"+rows.candidate.CandidateID {
		t.Fatalf("preview URL = %q", candidate.PreviewURL)
	}
	if strings.Contains(candidate.PreviewURL, rows.attempt.OwnerID) {
		t.Fatalf("preview URL contains owner: %q", candidate.PreviewURL)
	}
}

func TestResolveOwnedCandidateConcealsNativeForeignOwner(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	m := nativeReadModule(rows)
	m.canonicalOrigin = "https://prod.leapview.example"

	_, err := m.ResolveOwnedCandidate(t.Context(), rows.candidate.CandidateID, "foreign-owner")
	if !errors.Is(err, deployment.ErrCandidateNotFound) {
		t.Fatalf("error = %v, want candidate not found", err)
	}
}

func TestResolveOwnedCandidateRequiresCommittedQualifiedEvidence(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	rows.attempt.State = nativepostgres.AttemptRunning
	m := nativeReadModule(rows)
	m.canonicalOrigin = "https://prod.leapview.example"

	_, err := m.ResolveOwnedCandidate(t.Context(), rows.candidate.CandidateID, rows.attempt.OwnerID)
	if !errors.Is(err, deployment.ErrCandidateUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestNativeCandidateStatusForPreviewExpiresQualifiedCandidate(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if got := nativeCandidateStatusForPreviewAt("qualified", nativepostgres.AttemptCommitted, now.Add(-time.Second), now); got != deployment.CandidateExpired {
		t.Fatalf("status at deadline = %q, want expired", got)
	}
	if got := nativeCandidateStatusForPreviewAt("qualified", nativepostgres.AttemptCommitted, now.Add(time.Second), now); got != deployment.CandidateReady {
		t.Fatalf("status before deadline = %q, want ready", got)
	}
}

func TestServeCandidatePreviewMapsNativeFailure(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	rows.candidate.Status = "rejected"
	m := nativeReadModule(rows)
	m.canonicalOrigin = "https://prod.leapview.example"

	recorder := httptest.NewRecorder()
	m.ServeCandidatePreview(recorder, httptest.NewRequest(http.MethodGet, "/candidates/"+rows.candidate.CandidateID, nil), rows.candidate.CandidateID, rows.attempt.OwnerID, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), rows.attempt.OwnerID) {
		t.Fatalf("preview leaked owner: %s", recorder.Body.String())
	}
}
