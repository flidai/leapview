package localruntimefactory

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/sealedcatalog"
)

func TestSealedAuthorizationEvidenceRequiresExactSealAndOneRootIdentity(t *testing.T) {
	artifact := sealedcatalog.Artifact{SealID: "seal-1"}
	tests := []struct {
		name      string
		lease     catalogartifact.LeaseInput
		wantError bool
	}{
		{name: "candidate", lease: catalogartifact.LeaseInput{SealID: "seal-1", CandidateID: "candidate-1"}},
		{name: "neither", lease: catalogartifact.LeaseInput{SealID: "seal-1"}, wantError: true},
		{name: "both", lease: catalogartifact.LeaseInput{SealID: "seal-1", GenerationID: "generation-1", CandidateID: "candidate-1"}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSealedAuthorizationEvidence(artifact, tt.lease)
			if (err != nil) != tt.wantError {
				t.Fatalf("error=%v, wantError=%v", err, tt.wantError)
			}
		})
	}
}
