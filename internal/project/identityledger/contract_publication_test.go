package identityledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/project/contractprojection"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestPrepareContractPublicationUsesProjectionAuthority(t *testing.T) {
	var source projectcontracts.Source
	if err := json.Unmarshal([]byte(`{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"connection:warehouse","location":{"type":"path","path":"/tmp/orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{"id":{"datatype":"String","nullable":false}}}}}`), &source); err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSource(source, contractprojection.Contract{Version: "1.2.3+build.7", Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	input := ContractPublicationInput{
		InstanceID: "instance-a", Projection: projection,
		Validation: ValidationEvidence{Version: 1, Checks: []ValidationCheck{
			{Name: "z-test", Outcome: ValidationWarning, Reference: "warning evidence"},
			{Name: "a-test", Outcome: ValidationPassed, Reference: "passing evidence"},
		}},
	}
	publication, err := PrepareContractPublication(input)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := contractprojection.CanonicalBytes(projection)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := contractprojection.Digest(projection)
	if err != nil {
		t.Fatal(err)
	}
	if publication.AuthoredID != "source:orders" || publication.ResourceKind != "source" || publication.Version != "1.2.3+build.7" || publication.VersionBaseline != "1.2.3" || publication.ProjectionProfile != contractprojection.Profile {
		t.Fatalf("prepared identity = %#v", publication)
	}
	if !bytes.Equal(publication.CanonicalBytes, wantBytes) || publication.Digest != wantDigest {
		t.Fatal("prepared publication diverged from projection authority")
	}
	if publication.Validation.Checks[0].Name != "a-test" {
		t.Fatalf("validation evidence is not canonical: %#v", publication.Validation)
	}

	input.Validation.Checks[0].Outcome = ValidationOutcome("failed")
	if _, err := PrepareContractPublication(input); !errors.Is(err, ErrContractPublicationInvalid) {
		t.Fatalf("failed validation error = %v", err)
	}
}
