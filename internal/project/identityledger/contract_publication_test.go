package identityledger

import (
	"bytes"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/project/contractprojection"
)

func TestPrepareContractPublicationUsesProjectionAuthority(t *testing.T) {
	datatype := "String"
	nullable := false
	fields := map[string]contractprojection.Field{"id": {Datatype: &datatype, Nullable: &nullable}}
	projection := contractprojection.Source{
		Profile: contractprojection.Profile, APIVersion: "leapview.dev/v1", Kind: "Source",
		Metadata: contractprojection.Metadata{ID: "source:orders", Name: "orders", Contract: contractprojection.Contract{Version: "1.2.3+build.7", Compatibility: "backward"}},
		Contract: contractprojection.SourceContract{Schema: contractprojection.SourceSchema{Mode: "strict", Fields: &fields}},
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
