package compiler

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	modelossie "github.com/flidai/leapview/internal/analytics/model/ossie"
)

// ImportOssie loads the canonical project at projectPath, resolves every Ossie
// dataset source against that project's existing Model resources, and returns
// the validated native semantic model. The adapter never creates a source,
// connection, or model from an Ossie source string.
func ImportOssie(projectPath string, data []byte) (*semanticmodel.Model, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return nil, err
	}
	return project.ImportOssie(data)
}

// ExportOssie loads the canonical project and exports one compiled semantic
// model as deterministic JSON accepted by the pinned Ossie schema. ref may be
// either the authored semantic-model name or its stable resource ID.
func ExportOssie(projectPath, ref string) ([]byte, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return nil, err
	}
	return project.ExportOssie(ref)
}

// ExportOssieYAML is the YAML spelling of ExportOssie for documentation and
// interchange workflows that prefer authored YAML files.
func ExportOssieYAML(projectPath, ref string) ([]byte, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return nil, err
	}
	return project.ExportOssieYAML(ref)
}

// ImportOssie imports a document using this already-loaded canonical project.
// Keeping this method on Project makes the project graph/model binding explicit
// for callers that already performed compilation.
func (p Project) ImportOssie(data []byte) (*semanticmodel.Model, error) {
	return modelossie.Import(data, p.Models)
}

// ExportOssie exports a compiled semantic model from this already-loaded
// canonical project. ref may be an authored name or stable resource ID.
func (p Project) ExportOssie(ref string) ([]byte, error) {
	model, err := p.semanticModel(ref)
	if err != nil {
		return nil, err
	}
	return modelossie.Export(model)
}

// ExportOssieYAML exports a compiled semantic model in Ossie YAML.
func (p Project) ExportOssieYAML(ref string) ([]byte, error) {
	model, err := p.semanticModel(ref)
	if err != nil {
		return nil, err
	}
	return modelossie.ExportYAML(model)
}

func (p Project) semanticModel(ref string) (*semanticmodel.Model, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("semantic model reference is required")
	}
	id := ref
	if nameID := p.SemanticModelIDs[ref]; nameID != "" {
		id = nameID
	}
	model, ok := p.Manifest.SemanticModels[id]
	if !ok {
		return nil, fmt.Errorf("semantic model %q is not present in the compiled project", ref)
	}
	return model, nil
}
