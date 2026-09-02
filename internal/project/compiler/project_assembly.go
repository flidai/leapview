package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const (
	syntheticSourceRootID   projectgraph.ResourceID = "project:source-root"
	syntheticSourceRootName                         = "source-root"
)

// newProjectAssembly initializes the mutable compiler state shared by the
// legacy manifest loader and the conventional source-root loader. Project is
// retained as an internal compiler term while FAI-616 removes its authored and
// public meaning.
func newProjectAssembly(id projectgraph.ResourceID, name, baseDir, entrypoint string, metadata projectgraph.Metadata) Project {
	return Project{
		ID:                      id,
		Metadata:                metadata,
		Name:                    name,
		BaseDir:                 baseDir,
		ProjectPath:             entrypoint,
		Connections:             map[string]semanticmodel.Connection{},
		ConnectionPaths:         map[string]string{},
		ConnectionIDs:           map[string]string{},
		Sources:                 map[string]semanticmodel.Source{},
		SourcePaths:             map[string]string{},
		SourceIDs:               map[string]string{},
		Models:                  map[string]semanticmodel.Table{},
		ModelDefinitions:        map[string]projectmanifest.AuthoredModelDefinition{},
		ModelSources:            map[string]string{},
		ModelAIContexts:         map[string]*semanticmodel.AIContext{},
		ModelIDs:                map[string]string{},
		ModelPaths:              map[string]string{},
		SemanticModels:          map[string]projectSemanticModelSpec{},
		SemanticModelAIContexts: map[string]*semanticmodel.AIContext{},
		SemanticModelIDs:        map[string]string{},
		SemanticModelPaths:      map[string]string{},
		Dashboards:              map[string]*document.DashboardDocument{},
		DashboardIDs:            map[string]string{},
		DashboardPaths:          map[string]string{},
		DashboardMetadata:       map[string]projectgraph.Metadata{},
		PipelineIDs:             map[string]string{},
		PipelinePaths:           map[string]string{},
		RefreshPipelines:        map[string]refreshschedule.Definition{},
		Publications:            map[string]publication.Definition{},
		PublicationPaths:        map[string]string{},
		Access:                  projectAccessPolicy(),
		AccessPaths:             map[string]string{},
		ResourceIDs:             map[string]string{},
		ResourceIDOwners:        map[string]string{},
		ResourcePaths:           map[string]string{},
		ResourceMetadata:        map[string]projectgraph.Metadata{},
		ResourceSources:         map[string]string{},
	}
}
