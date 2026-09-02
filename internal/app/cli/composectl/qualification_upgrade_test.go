package composectl

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQualificationDeliveryPersistenceEvidenceBindsCandidateGenerationAndSeal(t *testing.T) {
	const (
		candidateID  = "candidate-1"
		generationID = "generation-1"
		sealID       = "seal-1"
		planID       = "plan-1"
		planDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/candidates/"):
			_, _ = io.WriteString(w, `{"id":"`+candidateID+`","status":"ready","sealId":"`+sealID+`","servingStateId":"`+generationID+`","planId":"`+planID+`","planDigest":"`+planDigest+`","targetId":"target-1","physicalPoolId":"pool-1","catalogDigest":"catalog-1","compatibilityDigest":"compat-1","servingArtifactId":"artifact-1","servingArtifactDigest":"artifact-digest"}`)
		case strings.Contains(r.URL.Path, "/generations/"):
			_, _ = io.WriteString(w, `{"id":"`+generationID+`","status":"active","servingStateId":"`+generationID+`","planId":"`+planID+`","planDigest":"`+planDigest+`","targetId":"target-1","physicalPoolId":"pool-1","catalogDigest":"catalog-1","compatibilityDigest":"compat-1","servingArtifactId":"artifact-1","servingArtifactDigest":"artifact-digest"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	evidence, err := (&Controller{}).qualificationDeliveryPersistenceEvidence(
		t.Context(), server.URL, "project-1", candidateID, generationID, "token",
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.CandidateID != candidateID || evidence.GenerationID != generationID || evidence.SnapshotSealID != sealID || evidence.PlanID != planID {
		t.Fatalf("delivery persistence evidence = %#v", evidence)
	}
}
