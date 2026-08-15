package compiler

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	appearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/project/schema"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/workspace"
	"gopkg.in/yaml.v3"
)

func IsProjectConfigFile(path string) bool {
	return projectConfigFile(path)
}

func LoadProject(projectPath string) (Project, error) {
	envelope, err := readEnvelope(projectPath)
	if err != nil {
		return Project{}, err
	}
	if envelope.Kind != "Project" {
		return Project{}, resourceError(projectPath, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want Project", projectPath, envelope.Kind)
	}
	var spec projectResource
	if err := envelope.Spec.Decode(&spec); err != nil {
		return Project{}, resourceError(projectPath, envelopeResourceID(envelope, ""), "spec", "%s spec: %s", projectPath, err.Error())
	}
	baseDir := filepath.Dir(projectPath)
	project := Project{
		Name:            envelope.Metadata.Name,
		BaseDir:         baseDir,
		Connections:     map[string]semanticmodel.Connection{},
		ConnectionPaths: map[string]string{},
		Sources:         map[string]semanticmodel.Source{},
		SourcePaths:     map[string]string{},
		Workspaces:      map[string]*WorkspaceProject{},
	}
	if err := loadConnections(&project, spec.Connections.Include); err != nil {
		return Project{}, err
	}
	if err := loadSources(&project, spec.Sources.Include); err != nil {
		return Project{}, err
	}
	if err := loadWorkspaces(&project, spec.Workspaces.Include); err != nil {
		return Project{}, err
	}
	return project, validateProject(project)
}

func loadConnections(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "Connection" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want Connection", path, envelope.Kind)
		}
		var spec semanticmodel.Connection
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, envelopeResourceID(envelope, ""), "spec", "%s spec: %s", path, err.Error())
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if _, exists := project.Connections[name]; exists {
			return resourceError(path, "connection:"+name, "metadata.name", "duplicate Connection %q", name)
		}
		project.Connections[name] = spec
		project.ConnectionPaths[name] = path
	}
	return nil
}

func loadSources(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "Source" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want Source", path, envelope.Kind)
		}
		var spec sourceSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, envelopeResourceID(envelope, ""), "spec", "%s spec: %s", path, err.Error())
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if _, exists := project.Sources[name]; exists {
			return resourceError(path, "source:"+name, "metadata.name", "duplicate Source %q", name)
		}
		source := semanticmodel.Source{
			Format:      spec.Format,
			Description: firstNonEmpty(spec.Description, envelope.Metadata.Description),
			Path:        spec.Path,
			Connection:  spec.Connection,
			Object:      spec.Object,
			Options:     spec.Options,
			Fields:      map[string]semanticmodel.SourceField{},
		}
		for field, cfg := range spec.Fields {
			source.Fields[field] = semanticmodel.SourceField{Type: cfg.Type, Description: cfg.Description}
		}
		project.Sources[name] = source
		project.SourcePaths[name] = path
	}
	return nil
}

func loadWorkspaces(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "Workspace" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want Workspace", path, envelope.Kind)
		}
		var spec workspaceSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, envelopeResourceID(envelope, ""), "spec", "%s spec: %s", path, err.Error())
		}
		id := envelope.Metadata.Name
		if id == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if _, exists := project.Workspaces[id]; exists {
			return resourceError(path, "workspace:"+id, "metadata.name", "duplicate Workspace %q", id)
		}
		workspaceProject := &WorkspaceProject{
			ID:                    id,
			Title:                 firstNonEmpty(envelope.Metadata.Title, id),
			Description:           envelope.Metadata.Description,
			AllowedSources:        map[string]struct{}{},
			Models:                map[string]semanticmodel.Table{},
			SemanticModels:        map[string]projectSemanticModelSpec{},
			Dashboards:            map[string]*dashboardauthoring.Dashboard{},
			Publications:          map[string]publication.Definition{},
			AccessGroups:          map[string]workspace.WorkspaceGroup{},
			AccessRoleBindings:    map[string]workspace.WorkspaceRoleBinding{},
			AccessGrants:          map[string]workspace.WorkspaceGrant{},
			AccessDataPolicies:    map[string]workspace.WorkspaceDataPolicy{},
			RefreshPipelines:      map[string]refreshschedule.Definition{},
			ModelTitles:           map[string]string{},
			ModelDescriptions:     map[string]string{},
			DashboardTitles:       map[string]string{},
			DashboardDescriptions: map[string]string{},
			DashboardTags:         map[string][]string{},
			Path:                  path,
			ModelPaths:            map[string]string{},
			SemanticModelPaths:    map[string]string{},
			DashboardPaths:        map[string]string{},
			PublicationPaths:      map[string]string{},
			AccessPaths:           map[string]string{},
			RefreshPipelinePaths:  map[string]string{},
		}
		for _, source := range spec.Uses.Sources {
			workspaceProject.AllowedSources[source] = struct{}{}
		}
		workspaceDir := filepath.Dir(path)
		if err := loadWorkspaceModels(workspaceProject, workspaceDir, spec.Models.Include); err != nil {
			return err
		}
		if err := loadWorkspaceSemanticModels(workspaceProject, workspaceDir, spec.SemanticModels.Include); err != nil {
			return err
		}
		if err := loadWorkspaceRefreshPipelines(workspaceProject, workspaceDir, spec.RefreshPipelines.Include); err != nil {
			return err
		}
		if err := loadWorkspaceDashboards(workspaceProject, workspaceDir, spec.Dashboards.Include); err != nil {
			return err
		}
		if err := loadWorkspacePublications(workspaceProject, workspaceDir, spec.Publications.Include); err != nil {
			return err
		}
		if err := loadWorkspaceAccess(workspaceProject, workspaceDir, spec.Access.Include); err != nil {
			return err
		}
		project.Workspaces[id] = workspaceProject
	}
	return nil
}

func loadWorkspacePublications(workspaceProject *WorkspaceProject, baseDir string, includes []string) error {
	paths, err := expandIncludes(baseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		resourceID := envelopeResourceID(envelope, workspaceProject.ID)
		if envelope.Kind != "DashboardPublication" {
			return resourceError(path, resourceID, "kind", "%s kind = %q, want DashboardPublication", path, envelope.Kind)
		}
		if envelope.Metadata.Workspace != "" && envelope.Metadata.Workspace != workspaceProject.ID {
			return resourceError(path, resourceID, "metadata.workspace", "%s workspace = %q, want %q", path, envelope.Metadata.Workspace, workspaceProject.ID)
		}
		var spec dashboardPublicationSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, resourceID, "spec", "%s spec: %s", path, err.Error())
		}
		name := strings.TrimSpace(envelope.Metadata.Name)
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if _, exists := workspaceProject.Publications[name]; exists {
			return resourceError(path, "dashboard_publication:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate DashboardPublication %q in workspace %q", name, workspaceProject.ID)
		}
		workspaceProject.Publications[name] = publication.Definition{
			Name: name, Dashboard: strings.TrimSpace(spec.Dashboard), DefaultPage: strings.TrimSpace(spec.DefaultPage),
			AllowedOrigins: append([]string(nil), spec.Embedding.AllowedOrigins...),
		}
		workspaceProject.PublicationPaths[name] = path
	}
	return nil
}

func loadWorkspaceRefreshPipelines(workspaceProject *WorkspaceProject, baseDir string, includes []string) error {
	paths, err := expandIncludes(baseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "RefreshPipeline" {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "kind", "%s kind = %q, want RefreshPipeline", path, envelope.Kind)
		}
		if envelope.Metadata.Workspace != "" && envelope.Metadata.Workspace != workspaceProject.ID {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "metadata.workspace", "%s workspace = %q, want %q", path, envelope.Metadata.Workspace, workspaceProject.ID)
		}
		var spec refreshPipelineSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec", "%s spec: %s", path, err.Error())
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if _, exists := workspaceProject.RefreshPipelines[name]; exists {
			return resourceError(path, "refresh_pipeline:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate RefreshPipeline %q in workspace %q", name, workspaceProject.ID)
		}
		pipeline := refreshschedule.Definition{ID: name, Name: name, SemanticModel: spec.SemanticModel}
		seenSchedules := map[string]struct{}{}
		for _, authored := range spec.On.Schedule {
			schedule, err := refreshschedule.ParseSchedule(authored.Cron, authored.Timezone)
			if err != nil {
				return resourceError(path, "refresh_pipeline:"+workspaceProject.ID+"."+name, "spec.on.schedule", "RefreshPipeline %q.%q has invalid schedule: %s", workspaceProject.ID, name, err.Error())
			}
			key := schedule.Expression + "|" + schedule.Timezone
			if _, exists := seenSchedules[key]; exists {
				return resourceError(path, "refresh_pipeline:"+workspaceProject.ID+"."+name, "spec.on.schedule", "RefreshPipeline %q.%q has duplicate schedule %q in %q", workspaceProject.ID, name, schedule.Expression, schedule.Timezone)
			}
			seenSchedules[key] = struct{}{}
			pipeline.Schedules = append(pipeline.Schedules, schedule)
		}
		workspaceProject.RefreshPipelines[name] = pipeline
		workspaceProject.RefreshPipelinePaths[name] = path
	}
	return nil
}

func loadWorkspaceModels(workspaceProject *WorkspaceProject, baseDir string, includes []string) error {
	paths, err := expandIncludes(baseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "ModelTable" {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "kind", "%s kind = %q, want ModelTable", path, envelope.Kind)
		}
		if envelope.Metadata.Workspace != "" && envelope.Metadata.Workspace != workspaceProject.ID {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "metadata.workspace", "%s workspace = %q, want %q", path, envelope.Metadata.Workspace, workspaceProject.ID)
		}
		var spec projectModelTableSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec", "%s spec: %s", path, err.Error())
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if _, exists := workspaceProject.Models[name]; exists {
			return resourceError(path, "model_table:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate ModelTable %q in workspace %q", name, workspaceProject.ID)
		}
		workspaceProject.Models[name] = projectModelTable(spec)
		workspaceProject.ModelTitles[name] = envelope.Metadata.Title
		workspaceProject.ModelDescriptions[name] = envelope.Metadata.Description
		workspaceProject.ModelPaths[name] = path
	}
	return nil
}

func loadWorkspaceSemanticModels(workspaceProject *WorkspaceProject, baseDir string, includes []string) error {
	paths, err := expandIncludes(baseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "SemanticModel" {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "kind", "%s kind = %q, want SemanticModel", path, envelope.Kind)
		}
		if envelope.Metadata.Workspace != "" && envelope.Metadata.Workspace != workspaceProject.ID {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "metadata.workspace", "%s workspace = %q, want %q", path, envelope.Metadata.Workspace, workspaceProject.ID)
		}
		var spec projectSemanticModelSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec", "%s spec: %s", path, err.Error())
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if _, exists := workspaceProject.SemanticModels[name]; exists {
			return resourceError(path, "semantic_model:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate SemanticModel %q in workspace %q", name, workspaceProject.ID)
		}
		workspaceProject.SemanticModels[name] = spec
		workspaceProject.ModelTitles[name] = envelope.Metadata.Title
		workspaceProject.ModelDescriptions[name] = envelope.Metadata.Description
		workspaceProject.SemanticModelPaths[name] = path
	}
	return nil
}

func loadWorkspaceDashboards(workspaceProject *WorkspaceProject, baseDir string, includes []string) error {
	paths, err := expandIncludes(baseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "Dashboard" {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "kind", "%s kind = %q, want Dashboard", path, envelope.Kind)
		}
		if envelope.Metadata.Workspace != "" && envelope.Metadata.Workspace != workspaceProject.ID {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "metadata.workspace", "%s workspace = %q, want %q", path, envelope.Metadata.Workspace, workspaceProject.ID)
		}
		var spec dashboardSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec", "%s spec: %s", path, err.Error())
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		if _, exists := workspaceProject.Dashboards[name]; exists {
			return resourceError(path, "dashboard:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate Dashboard %q in workspace %q", name, workspaceProject.ID)
		}
		dashboard := &dashboardauthoring.Dashboard{
			ID:                name,
			Appearance:        spec.Appearance,
			Title:             envelope.Metadata.Title,
			Description:       envelope.Metadata.Description,
			SemanticModel:     spec.SemanticModel,
			FilterDefinitions: spec.Filters,
			FilterBindings:    spec.FilterBindings,
			FilterApplication: spec.FilterApplication,
			Visuals:           spec.Visuals,
			Pages:             projectDashboardPages(spec.Pages),
		}
		if err := appearance.ValidatePatch(spec.Appearance); err != nil {
			return resourceError(path, "dashboard:"+workspaceProject.ID+"."+name, "spec.appearance", "%s", err.Error())
		}
		workspaceProject.Dashboards[name] = dashboard
		workspaceProject.DashboardTitles[name] = envelope.Metadata.Title
		workspaceProject.DashboardDescriptions[name] = envelope.Metadata.Description
		workspaceProject.DashboardTags[name] = append([]string{}, envelope.Metadata.Tags...)
		workspaceProject.DashboardPaths[name] = path
	}
	return nil
}

func loadWorkspaceAccess(workspaceProject *WorkspaceProject, baseDir string, includes []string) error {
	paths, err := expandIncludes(baseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Metadata.Workspace != "" && envelope.Metadata.Workspace != workspaceProject.ID {
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "metadata.workspace", "%s workspace = %q, want %q", path, envelope.Metadata.Workspace, workspaceProject.ID)
		}
		name := envelope.Metadata.Name
		if name == "" {
			return resourceError(path, "", "metadata.name", "%s metadata.name is required", path)
		}
		switch envelope.Kind {
		case "WorkspaceGroup":
			var spec workspaceGroupSpec
			if err := envelope.Spec.Decode(&spec); err != nil {
				return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec", "%s spec: %s", path, err.Error())
			}
			if _, exists := workspaceProject.AccessGroups[name]; exists {
				return resourceError(path, "workspace_group:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate WorkspaceGroup %q in workspace %q", name, workspaceProject.ID)
			}
			workspaceProject.AccessGroups[name] = projectWorkspaceGroup(name, spec)
			workspaceProject.AccessPaths["WorkspaceGroup:"+name] = path
		case "WorkspaceRoleBinding":
			var spec workspaceRoleBindingSpec
			if err := envelope.Spec.Decode(&spec); err != nil {
				return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec", "%s spec: %s", path, err.Error())
			}
			if _, exists := workspaceProject.AccessRoleBindings[name]; exists {
				return resourceError(path, "workspace_role_binding:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate WorkspaceRoleBinding %q in workspace %q", name, workspaceProject.ID)
			}
			workspaceProject.AccessRoleBindings[name] = projectWorkspaceRoleBinding(name, spec)
			workspaceProject.AccessPaths["WorkspaceRoleBinding:"+name] = path
		case "Grant":
			var spec workspaceGrantSpec
			if err := envelope.Spec.Decode(&spec); err != nil {
				return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec", "%s spec: %s", path, err.Error())
			}
			if _, exists := workspaceProject.AccessGrants[name]; exists {
				return resourceError(path, "grant:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate Grant %q in workspace %q", name, workspaceProject.ID)
			}
			workspaceProject.AccessGrants[name] = projectWorkspaceGrant(name, spec)
			workspaceProject.AccessPaths["Grant:"+name] = path
		case "DataPolicy":
			var spec workspaceDataPolicySpec
			if err := envelope.Spec.Decode(&spec); err != nil {
				return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec", "%s spec: %s", path, err.Error())
			}
			if _, exists := workspaceProject.AccessDataPolicies[name]; exists {
				return resourceError(path, "data_policy:"+workspaceProject.ID+"."+name, "metadata.name", "duplicate DataPolicy %q in workspace %q", name, workspaceProject.ID)
			}
			policy, err := projectWorkspaceDataPolicy(name, spec)
			if err != nil {
				return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "spec.expression", "%s spec.expression: %s", path, err.Error())
			}
			workspaceProject.AccessDataPolicies[name] = policy
			workspaceProject.AccessPaths["DataPolicy:"+name] = path
		default:
			return resourceError(path, envelopeResourceID(envelope, workspaceProject.ID), "kind", "%s kind = %q, want WorkspaceGroup, WorkspaceRoleBinding, Grant, or DataPolicy", path, envelope.Kind)
		}
	}
	return nil
}

func readEnvelope(path string) (resourceEnvelope, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return resourceEnvelope{}, err
	}
	if kind, ok := schemaKindForEnvelope(content); ok {
		if err := configschema.ValidateBytes(kind, path, content); err != nil {
			return resourceEnvelope{}, annotateSchemaError(err, path, resourceIDForHeader(content, ""), "spec")
		}
	}
	var envelope resourceEnvelope
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&envelope); err != nil {
		return resourceEnvelope{}, fmt.Errorf("%s: %w", path, err)
	}
	if envelope.APIVersion != projectAPIVersion {
		return resourceEnvelope{}, resourceError(path, envelopeResourceID(envelope, ""), "apiVersion", "%s apiVersion = %q, want %q", path, envelope.APIVersion, projectAPIVersion)
	}
	if envelope.Kind == "" {
		return resourceEnvelope{}, resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind is required", path)
	}
	return envelope, nil
}

func resourceIDForHeader(content []byte, fallbackWorkspace string) string {
	var envelope resourceEnvelope
	if err := yaml.Unmarshal(content, &envelope); err != nil {
		return ""
	}
	return envelopeResourceID(envelope, fallbackWorkspace)
}

func envelopeResourceID(envelope resourceEnvelope, fallbackWorkspace string) string {
	name := envelope.Metadata.Name
	if name == "" {
		return ""
	}
	workspaceID := firstNonEmpty(envelope.Metadata.Workspace, fallbackWorkspace)
	switch envelope.Kind {
	case "Project":
		return "project:" + name
	case "Connection":
		return "connection:" + name
	case "Source":
		return "source:" + name
	case "Workspace":
		return "workspace:" + name
	case "ModelTable":
		if workspaceID == "" {
			return ""
		}
		return "model_table:" + workspaceID + "." + name
	case "SemanticModel":
		if workspaceID == "" {
			return ""
		}
		return "semantic_model:" + workspaceID + "." + name
	case "Dashboard":
		if workspaceID == "" {
			return ""
		}
		return "dashboard:" + workspaceID + "." + name
	case "DashboardPublication":
		if workspaceID == "" {
			return ""
		}
		return "dashboard_publication:" + workspaceID + "." + name
	case "WorkspaceGroup":
		if workspaceID == "" {
			return ""
		}
		return "workspace_group:" + workspaceID + "." + name
	case "WorkspaceRoleBinding":
		if workspaceID == "" {
			return ""
		}
		return "workspace_role_binding:" + workspaceID + "." + name
	case "Grant":
		if workspaceID == "" {
			return ""
		}
		return "grant:" + workspaceID + "." + name
	case "DataPolicy":
		if workspaceID == "" {
			return ""
		}
		return "data_policy:" + workspaceID + "." + name
	case "RefreshPipeline":
		if workspaceID == "" {
			return ""
		}
		return "refresh_pipeline:" + workspaceID + "." + name
	default:
		return ""
	}
}

func schemaKindForEnvelope(content []byte) (configschema.Kind, bool) {
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &header); err != nil {
		return "", false
	}
	if header.APIVersion != projectAPIVersion {
		return "", false
	}
	switch header.Kind {
	case "Project":
		return configschema.KindProject, true
	case "Connection":
		return configschema.KindConnection, true
	case "Source":
		return configschema.KindSource, true
	case "Workspace":
		return configschema.KindWorkspace, true
	case "WorkspaceGroup":
		return configschema.KindWorkspaceGroup, true
	case "WorkspaceRoleBinding":
		return configschema.KindWorkspaceRoleBinding, true
	case "Grant":
		return configschema.KindGrant, true
	case "DataPolicy":
		return configschema.KindDataPolicy, true
	case "RefreshPipeline":
		return configschema.KindRefreshPipeline, true
	case "ModelTable":
		return configschema.KindModelTable, true
	case "SemanticModel":
		return configschema.KindSemanticModelResource, true
	case "Dashboard":
		return configschema.KindDashboardResource, true
	case "DashboardPublication":
		return configschema.KindDashboardPublication, true
	default:
		return "", false
	}
}

func projectConfigFile(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var envelope struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &envelope); err != nil {
		return false
	}
	return envelope.APIVersion == projectAPIVersion && envelope.Kind == "Project"
}

func expandIncludes(baseDir string, includes []string) ([]string, error) {
	var paths []string
	for _, pattern := range includes {
		if strings.TrimSpace(pattern) == "" {
			return nil, fmt.Errorf("include pattern is required")
		}
		if filepath.IsAbs(pattern) {
			return nil, fmt.Errorf("include pattern %q must be relative", pattern)
		}
		if strings.Contains(filepath.ToSlash(pattern), "**") {
			return nil, fmt.Errorf("include pattern %q uses unsupported ** glob", pattern)
		}
		for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(pattern)), "/") {
			if part == ".." {
				return nil, fmt.Errorf("include pattern %q escapes project boundary", pattern)
			}
		}
		matches, err := filepath.Glob(filepath.Join(baseDir, pattern))
		if err != nil {
			return nil, fmt.Errorf("include pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("include pattern %q matched no files", pattern)
		}
		sort.Strings(matches)
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				return nil, fmt.Errorf("include pattern %q matched directory %s", pattern, match)
			}
			ext := strings.ToLower(filepath.Ext(match))
			if ext != ".yaml" && ext != ".yml" {
				return nil, fmt.Errorf("include pattern %q matched non-YAML file %s", pattern, match)
			}
		}
		paths = append(paths, matches...)
	}
	return paths, nil
}
