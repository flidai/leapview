package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

const projectAPIVersion = "leapview.dev/v1"

type Project struct {
	// ID and Metadata identify the project-wide authored graph.
	ID                      projectgraph.ResourceID
	Metadata                projectgraph.Metadata
	Graph                   projectgraph.ProjectGraph
	Manifest                manifest.Project
	Name                    string
	BaseDir                 string
	ProjectPath             string
	Connections             map[string]semanticmodel.Connection
	ConnectionPaths         map[string]string
	ConnectionIDs           map[string]string
	Sources                 map[string]semanticmodel.Source
	SourcePaths             map[string]string
	SourceIDs               map[string]string
	Models                  map[string]semanticmodel.Table
	ModelAIContexts         map[string]*semanticmodel.AIContext
	ModelIDs                map[string]string
	ModelPaths              map[string]string
	SemanticModels          map[string]projectSemanticModelSpec
	SemanticModelAIContexts map[string]*semanticmodel.AIContext
	SemanticModelIDs        map[string]string
	SemanticModelPaths      map[string]string
	Dashboards              map[string]*dashboardauthoring.Dashboard
	DashboardIDs            map[string]string
	DashboardPaths          map[string]string
	DashboardMetadata       map[string]projectgraph.Metadata
	PipelineIDs             map[string]string
	PipelinePaths           map[string]string
	RefreshPipelines        map[string]refreshschedule.Definition
	Publications            map[string]publication.Definition
	PublicationPaths        map[string]string
	Access                  manifest.AccessPolicy
	AccessPaths             map[string]string
	ResourceIDs             map[string]string
	ResourceIDOwners        map[string]string
	ResourcePaths           map[string]string
	ResourceMetadata        map[string]projectgraph.Metadata
}

func CompileProject(projectPath string) (projectartifact.Project, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return projectartifact.Project{}, err
	}
	return projectartifact.NewProject(project.Graph, project.Manifest)
}

// CompileProjectGraph compiles a project into the portable project graph.
func CompileProjectGraph(projectPath string) (projectgraph.ProjectGraph, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return projectgraph.ProjectGraph{}, err
	}
	return project.Graph, nil
}
