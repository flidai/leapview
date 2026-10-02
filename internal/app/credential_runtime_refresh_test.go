package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/release"
)

func TestRefreshBaseCredentialsRejectsLocalPins(t *testing.T) {
	source, provenance, _ := activeCredentialPinFixture(t)
	job := refreshrun.JobRecord{Identity: provenance.Plan.Identity}
	if err := refreshBaseCredentialCheck(source)(t.Context(), job); !errors.Is(err, errRefreshLocalCredentialUnsupported) {
		t.Fatalf("local pin preflight = %v", err)
	}
}

func TestRefreshBaseCredentialsAllowsProviderAndRejectsMissingEvidence(t *testing.T) {
	source, provenance, _ := activeCredentialPinFixture(t)
	provenance.Plan.Bindings[0].CredentialVersionID = ""
	provenance.Plan.Bindings[0].ValidatedVersion = "provider:v1"
	source.releases = sourceSchemaProvenanceStub{provenance: provenance}
	job := refreshrun.JobRecord{Identity: provenance.Plan.Identity}
	if err := refreshBaseCredentialCheck(source)(t.Context(), job); err != nil {
		t.Fatalf("provider preflight = %v", err)
	}
	source.releases = sourceSchemaProvenanceStub{err: release.ErrNotFound}
	if err := refreshBaseCredentialCheck(source)(t.Context(), job); err == nil {
		t.Fatal("missing evidence allowed refresh")
	}
}

func TestRefreshBaseCredentialsPreservesCancellationAndRedactsEvidenceErrors(t *testing.T) {
	source, provenance, _ := activeCredentialPinFixture(t)
	job := refreshrun.JobRecord{Identity: provenance.Plan.Identity}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := refreshBaseCredentialCheck(source)(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	source.releases = sourceSchemaProvenanceStub{err: errors.New("private provider diagnostic")}
	if err := refreshBaseCredentialCheck(source)(t.Context(), job); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe evidence error = %v", err)
	}
}
