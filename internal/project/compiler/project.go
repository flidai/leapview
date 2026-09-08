package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const sourceAPIVersion = "leapview.dev/v1"

type sourceAssembly struct {
	Graph                   projectgraph.ProjectGraph
	Manifest                manifest.ResourceManifest
	BaseDir                 string
	Connections             map[string]semanticmodel.Connection
	ConnectionPaths         map[string]string
	ConnectionIDs           map[string]string
	Sources                 map[string]semanticmodel.Source
	SourcePaths             map[string]string
	SourceIDs               map[string]string
	Models                  map[string]semanticmodel.Table
	ModelDefinitions        map[string]manifest.AuthoredModelDefinition
	ModelSources            map[string]string
	ModelAIContexts         map[string]*semanticmodel.AIContext
	ModelIDs                map[string]string
	ModelPaths              map[string]string
	SemanticModels          map[string]projectcontracts.SemanticModelSpec
	SemanticModelAIContexts map[string]*semanticmodel.AIContext
	SemanticModelIDs        map[string]string
	SemanticModelPaths      map[string]string
	Dashboards              map[string]*document.DashboardDocument
	DashboardIDs            map[string]string
	DashboardPaths          map[string]string
	DashboardMetadata       map[string]projectgraph.Metadata
	PipelineIDs             map[string]string
	PipelinePaths           map[string]string
	RefreshPipelines        map[string]refreshschedule.Definition
	ResourceIDs             map[string]string
	ResourceIDOwners        map[string]string
	ResourcePaths           map[string]string
	ResourceMetadata        map[string]projectgraph.Metadata
	// ResourceSources is keyed by canonical resource ID and contains validated
	// authored YAML for definition views. Connections are never inserted.
	ResourceSources map[string]string
}

// CompileGraph compiles a source root into the portable resource graph.
func CompileGraph(sourceRoot string) (projectgraph.ProjectGraph, error) {
	return compileSourceRootGraph(sourceRoot)
}
