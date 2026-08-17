package filesystem

import (
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
	return servingstate.Validation{
		Digest: validation.Digest, ManifestJSON: validation.ManifestJSON, RootDir: validation.RootDir,
		ProjectID: identity.ProjectID, ProjectDigest: validation.ProjectDigest,
		AccessPolicy: validation.Manifest.Access, Graph: validation.Graph,
	}, nil
}

func (Validator) Cleanup(validation servingstate.Validation) error {
	if validation.RootDir == "" {
		return nil
	}
	return os.RemoveAll(validation.RootDir)
}
