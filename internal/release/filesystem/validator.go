package filesystem

import (
	"encoding/json"
	"fmt"
	"os"

	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type Validator struct{}

type ValidateOptions struct {
	Environment servingstate.Environment
}

func (v Validator) ValidateArtifact(path string, projectID projectgraph.ResourceID, environment servingstate.Environment, servingStateID servingstate.ID) (servingstate.Validation, error) {
	return ValidateArtifactWithOptions(path, projectID, servingStateID, ValidateOptions{Environment: environment})
}

func ValidateArtifactWithOptions(path string, projectID projectgraph.ResourceID, servingStateID servingstate.ID, options ValidateOptions) (servingstate.Validation, error) {
	identity, err := projectgraph.NewServingIdentity(projectID, string(options.Environment), string(servingStateID))
	if err != nil {
		return servingstate.Validation{}, err
	}
	validation, err := projectbundle.ValidateArtifact(path)
	if err != nil {
		return servingstate.Validation{}, err
	}
	if validation.ProjectID != identity.ProjectID.String() {
		return servingstate.Validation{}, fmt.Errorf("artifact project %q does not match serving project %q", validation.ProjectID, identity.ProjectID)
	}
	publicationsJSON, err := compiledDashboardPublicationsJSON(validation.Manifest.Publications)
	if err != nil {
		return servingstate.Validation{}, fmt.Errorf("encode compiled dashboard publications: %w", err)
	}
	return servingstate.Validation{
		Digest: validation.Digest, ManifestJSON: validation.ManifestJSON, RootDir: validation.RootDir,
		ProjectID: identity.ProjectID, ProjectDigest: validation.ProjectDigest,
		AccessPolicy: validation.Manifest.Access, DashboardPublicationsJSON: publicationsJSON, Graph: validation.Graph,
	}, nil
}

func compiledDashboardPublicationsJSON(definitions any) (string, error) {
	encoded, err := json.Marshal(definitions)
	if err == nil && string(encoded) == "null" {
		return "{}", nil
	}
	return string(encoded), err
}

func (Validator) Cleanup(validation servingstate.Validation) error {
	if validation.RootDir == "" {
		return nil
	}
	return os.RemoveAll(validation.RootDir)
}
