package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/release"
)

func TestPublicationLocalPinEvidenceRequiresUnambiguousSealedBindings(t *testing.T) {
	tests := []struct {
		name string
		edit func(*GenerationAdmissionInput)
	}{
		{name: "exact sealed pin"},
		{name: "missing seal fingerprint", edit: func(input *GenerationAdmissionInput) {
			input.Seal.QualificationEvidence = json.RawMessage(`{"checks":["schema"]}`)
		}},
		{name: "different seal fingerprint", edit: func(input *GenerationAdmissionInput) {
			input.Seal.QualificationEvidence = json.RawMessage(`{"gates":{"bindingGeneration":"` + admissionDigest('9') + `"}}`)
		}},
		{name: "two local bindings for one connection", edit: func(input *GenerationAdmissionInput) {
			duplicate := input.Provenance.Plan.Bindings[0]
			duplicate.BindingID = "another-local-binding"
			input.Provenance.Plan.Bindings = append(input.Provenance.Plan.Bindings, duplicate)
			resealCredentialPinTestInput(t, input)
		}},
		{name: "provider and local binding for one connection", edit: func(input *GenerationAdmissionInput) {
			provider := input.Provenance.Plan.Bindings[0]
			provider.BindingID, provider.CredentialVersionID, provider.ValidatedVersion = "provider-binding", "", "provider-v1"
			input.Provenance.Plan.Bindings = append(input.Provenance.Plan.Bindings, provider)
			resealCredentialPinTestInput(t, input)
		}},
		{name: "authored and local connection overlap", edit: func(input *GenerationAdmissionInput) {
			input.Provenance.Plan.AuthoredConnections = []release.AuthoredConnectionEvidence{{ConnectionID: "connection-admission", ConnectorKind: "postgres"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCredentialPublicationFixtureWithOptions(t, credentialPublicationFixtureOptions{
				localPin: true, editInput: test.edit,
			})
			tx, err := fixture.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			target, err := fixture.delivery.TargetTx(t.Context(), tx, fixture.publication.TargetID)
			if err != nil {
				t.Fatal(err)
			}
			_, pins, err := publicationGenerationCredentialPins(t.Context(), tx, fixture.delivery, fixture.provenance, target, fixture.publication.GenerationID)
			if test.edit == nil {
				if err != nil || len(pins) != 1 || pins["connection-admission"].CredentialVersionID == "" {
					t.Fatalf("exact sealed local evidence = %#v, %v", pins, err)
				}
			} else if !errors.Is(err, deploymentpostgres.ErrConflict) {
				t.Fatalf("ambiguous or unsealed local evidence = %#v, %v; want conflict", pins, err)
			}
		})
	}
}

func resealCredentialPinTestInput(t *testing.T, input *GenerationAdmissionInput) {
	t.Helper()
	gate := *input.Provenance.Plan.GateEvidence
	gate.BindingGeneration = release.BindingFingerprint(input.Provenance.Plan.Bindings)
	input.Provenance.Plan.GateEvidence = &gate
	qualification, err := json.Marshal(map[string]any{"gates": map[string]string{"bindingGeneration": gate.BindingGeneration}})
	if err != nil {
		t.Fatal(err)
	}
	input.Seal.QualificationEvidence = qualification
}
