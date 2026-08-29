package postgres

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
)

func TestProjectClaimErrorMappingDoesNotMisclassifyInfrastructureFailure(t *testing.T) {
	infrastructure := errors.New("database unavailable")
	mapped := mapClaimError(infrastructure)
	if !errors.Is(mapped, infrastructure) {
		t.Fatalf("mapped error = %v, want infrastructure cause", mapped)
	}
	if errors.Is(mapped, deployment.ErrProjectClaimInvalid) {
		t.Fatalf("infrastructure failure was misclassified as invalid input: %v", mapped)
	}
}
