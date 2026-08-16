package deployment

import (
	"strings"
	"testing"
)

func TestValidateCreateRequiresExactGenerationIdentity(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	base := CreateInput{ID: "deployment_1", ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_1", ArtifactDigest: digest, RequestDigest: digest, CreatedBy: "principal_1"}
	if err := ValidateCreate(base); err != nil {
		t.Fatalf("valid create rejected: %v", err)
	}
	for name, mutate := range map[string]func(*CreateInput){
		"missing project":     func(v *CreateInput) { v.ProjectID = "" },
		"missing environment": func(v *CreateInput) { v.Environment = "" },
		"missing generation":  func(v *CreateInput) { v.GenerationID = "" },
		"invalid artifact":    func(v *CreateInput) { v.ArtifactDigest = "sha256:bad" },
		"invalid request":     func(v *CreateInput) { v.RequestDigest = "request" },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := ValidateCreate(value); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestValidateActivationRequiresIdentity(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	base := ActivationInput{DeploymentID: "deployment_1", ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_1", ArtifactDigest: digest, ActivationPrincipal: "principal_1", VerificationDigest: digest}
	if err := ValidateActivation(base); err != nil {
		t.Fatalf("valid activation rejected: %v", err)
	}
	base.GenerationID = ""
	if err := ValidateActivation(base); err == nil {
		t.Fatal("missing generation accepted")
	}
}
