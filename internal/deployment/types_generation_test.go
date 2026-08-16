package deployment

import (
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"strings"
	"testing"
)

func TestValidateCreateRequiresExactGenerationIdentity(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	identity, _ := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	base := CreateInput{ID: "deployment_1", ServingIdentity: identity, ArtifactDigest: digest, RequestDigest: digest, CreatedBy: "principal_1"}
	if err := ValidateCreate(base); err != nil {
		t.Fatalf("valid create rejected: %v", err)
	}
	for name, mutate := range map[string]func(*CreateInput){
		"missing project":          func(v *CreateInput) { v.ServingIdentity.ProjectID = "" },
		"missing environment":      func(v *CreateInput) { v.ServingIdentity.Environment = "" },
		"missing generation":       func(v *CreateInput) { v.ServingIdentity.GenerationID = "" },
		"invalid artifact":         func(v *CreateInput) { v.ArtifactDigest = "sha256:bad" },
		"invalid request":          func(v *CreateInput) { v.RequestDigest = "request" },
		"noncanonical environment": func(v *CreateInput) { v.ServingIdentity.Environment = " Prod" },
		"noncanonical generation":  func(v *CreateInput) { v.ServingIdentity.GenerationID = " Generation_1" },
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
	identity, _ := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	base := ActivationInput{DeploymentID: "deployment_1", ServingIdentity: identity, ArtifactDigest: digest, ActivationPrincipal: "principal_1", VerificationDigest: digest}
	if err := ValidateActivation(base); err != nil {
		t.Fatalf("valid activation rejected: %v", err)
	}
	base.ServingIdentity.GenerationID = ""
	if err := ValidateActivation(base); err == nil {
		t.Fatal("missing generation accepted")
	}
}
