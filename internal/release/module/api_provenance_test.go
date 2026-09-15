package module

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/release"
	releaseapi "github.com/flidai/leapview/internal/release/api"
)

func TestReleaseProvenanceAPIConversionRetainsAuthorizationEvidence(t *testing.T) {
	const authorizationDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	provenance := release.Provenance{
		Plan: release.GenerationPlanProvenance{
			PolicyRevision:      17,
			AuthorizationDigest: authorizationDigest,
		},
	}

	apiValue := releaseProvenanceToAPI(&provenance)
	if apiValue == nil || apiValue.Plan.PolicyRevision == nil || *apiValue.Plan.PolicyRevision != provenance.Plan.PolicyRevision {
		t.Fatalf("policy revision was dropped by API conversion: %#v", apiValue)
	}
	if apiValue.Plan.AuthorizationDigest == nil || *apiValue.Plan.AuthorizationDigest != provenance.Plan.AuthorizationDigest {
		t.Fatalf("authorization digest was dropped by API conversion: %#v", apiValue)
	}

	wire, err := json.Marshal(apiValue)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip releaseapi.Provenance
	if err := json.Unmarshal(wire, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Plan.PolicyRevision == nil || *roundTrip.Plan.PolicyRevision != provenance.Plan.PolicyRevision {
		t.Fatalf("policy revision was dropped by API JSON round-trip: %s", wire)
	}
	if roundTrip.Plan.AuthorizationDigest == nil || *roundTrip.Plan.AuthorizationDigest != provenance.Plan.AuthorizationDigest {
		t.Fatalf("authorization digest was dropped by API JSON round-trip: %s", wire)
	}
}
