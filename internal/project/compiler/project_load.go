package compiler

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
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
		ID:                      projectgraph.ResourceID(envelope.Metadata.ID),
		Metadata:                projectgraph.Metadata{DisplayName: firstNonEmpty(envelope.Metadata.DisplayName, envelope.Metadata.Title, envelope.Metadata.Name), Description: envelope.Metadata.Description, Owner: envelope.Metadata.Owner, Domain: envelope.Metadata.Domain, Tags: append([]string(nil), envelope.Metadata.Tags...), Documentation: envelope.Metadata.Documentation},
		Name:                    envelope.Metadata.Name,
		BaseDir:                 baseDir,
		ProjectPath:             projectPath,
		Connections:             map[string]semanticmodel.Connection{},
		ConnectionPaths:         map[string]string{},
		ConnectionIDs:           map[string]string{},
		Sources:                 map[string]semanticmodel.Source{},
		SourcePaths:             map[string]string{},
		SourceIDs:               map[string]string{},
		Models:                  map[string]semanticmodel.Table{},
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
	}
	if envelope.Metadata.ID == "" {
		return Project{}, resourceError(projectPath, envelopeResourceID(envelope, ""), "metadata.id", "%s metadata.id is required", projectPath)
	}
	project.ResourceIDOwners[envelope.Metadata.ID] = "project"
	if err := loadConnections(&project, spec.Connections.Include); err != nil {
		return Project{}, err
	}
	if err := loadSources(&project, spec.Sources.Include); err != nil {
		return Project{}, err
	}
	if err := loadFlatResources(&project, spec); err != nil {
		return Project{}, err
	}
	if err := validateFlatProject(project); err != nil {
		return Project{}, err
	}
	graphValue, err := compileProjectGraph(project)
	if err != nil {
		return Project{}, err
	}
	project.Graph = graphValue
	project.Manifest, err = projectManifest(project)
	if err != nil {
		return Project{}, err
	}
	return project, nil
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
		if envelope.Metadata.ID == "" {
			return resourceError(path, "", "metadata.id", "%s metadata.id is required", path)
		}
		if _, err := projectgraph.NewResourceID(envelope.Metadata.ID); err != nil {
			return resourceError(path, envelope.Metadata.ID, "metadata.id", "%s metadata.id: %v", path, err)
		}
		if _, exists := project.Connections[name]; exists {
			return resourceError(path, "connection:"+name, "metadata.name", "duplicate Connection %q", name)
		}
		project.Connections[name] = spec
		project.ConnectionPaths[name] = path
		if owner, exists := project.ResourceIDOwners[envelope.Metadata.ID]; exists {
			return resourceError(path, envelope.Metadata.ID, "metadata.id", "duplicate resource id %q already used by %s", envelope.Metadata.ID, owner)
		}
		project.ResourceIDOwners[envelope.Metadata.ID] = "connection:" + name
		project.ConnectionIDs[name] = envelope.Metadata.ID
		project.ResourceIDs["connection:"+name] = envelope.Metadata.ID
		project.ResourcePaths[envelope.Metadata.ID] = path
		project.ResourceMetadata[envelope.Metadata.ID] = flatResourceMetadata(envelope.Metadata, name)
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
		if envelope.Metadata.ID == "" {
			return resourceError(path, "", "metadata.id", "%s metadata.id is required", path)
		}
		if _, err := projectgraph.NewResourceID(envelope.Metadata.ID); err != nil {
			return resourceError(path, envelope.Metadata.ID, "metadata.id", "%s metadata.id: %v", path, err)
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
		if owner, exists := project.ResourceIDOwners[envelope.Metadata.ID]; exists {
			return resourceError(path, envelope.Metadata.ID, "metadata.id", "duplicate resource id %q already used by %s", envelope.Metadata.ID, owner)
		}
		project.ResourceIDOwners[envelope.Metadata.ID] = "source:" + name
		project.SourceIDs[name] = envelope.Metadata.ID
		project.ResourceIDs["source:"+name] = envelope.Metadata.ID
		project.ResourcePaths[envelope.Metadata.ID] = path
		project.ResourceMetadata[envelope.Metadata.ID] = flatResourceMetadata(envelope.Metadata, name)
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

func resourceIDForHeader(content []byte, fallback string) string {
	var envelope resourceEnvelope
	if err := yaml.Unmarshal(content, &envelope); err != nil {
		return ""
	}
	return envelopeResourceID(envelope, fallback)
}

func envelopeResourceID(envelope resourceEnvelope, fallback string) string {
	name := strings.TrimSpace(envelope.Metadata.Name)
	if name == "" {
		return strings.TrimSpace(envelope.Metadata.ID)
	}
	if id := strings.TrimSpace(envelope.Metadata.ID); id != "" {
		return id
	}
	prefix := map[string]string{
		"Project": "project:", "Connection": "connection:", "Source": "source:",
		"Model": "model:", "SemanticModel": "semantic_model:", "Pipeline": "pipeline:",
		"Dashboard": "dashboard:", "DashboardPublication": "dashboard_publication:",
		"Group": "group:", "RoleBinding": "role_binding:", "Grant": "grant:", "DataPolicy": "data_policy:",
	}[envelope.Kind]
	if prefix == "" {
		return strings.TrimSpace(fallback)
	}
	return prefix + name
}

func schemaKindForEnvelope(content []byte) (configschema.Kind, bool) {
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &header); err != nil || header.APIVersion != projectAPIVersion {
		return "", false
	}
	kinds := map[string]configschema.Kind{
		"Project": configschema.KindProject, "Connection": configschema.KindConnection, "Source": configschema.KindSource,
		"Model": configschema.KindModel, "SemanticModel": configschema.KindSemanticModel, "Pipeline": configschema.KindPipeline,
		"Group": configschema.KindGroup, "RoleBinding": configschema.KindRoleBinding, "Grant": configschema.KindGrant,
		"DataPolicy": configschema.KindDataPolicy, "Dashboard": configschema.KindDashboard,
		"DashboardPublication": configschema.KindDashboardPublication,
	}
	kind, ok := kinds[header.Kind]
	return kind, ok
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
	root, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project boundary: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project boundary: %w", err)
	}
	// Glob against the canonical absolute root. A relative base directory can
	// otherwise produce relative matches that compare incorrectly with the
	// canonical absolute path above.
	baseDir = root
	var paths []string
	seen := map[string]struct{}{}
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
			canonical, err := filepath.EvalSymlinks(match)
			if err != nil {
				return nil, fmt.Errorf("include pattern %q match %q cannot be resolved: %w", pattern, includeDisplayPath(baseDir, match), err)
			}
			relative, err := filepath.Rel(root, canonical)
			if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("include pattern %q match %q resolves outside project boundary", pattern, includeDisplayPath(baseDir, match))
			}
			if _, duplicate := seen[canonical]; duplicate {
				continue
			}
			info, err := os.Stat(match)
			if err != nil {
				return nil, fmt.Errorf("include pattern %q match %q: %w", pattern, includeDisplayPath(baseDir, match), err)
			}
			if info.IsDir() {
				return nil, fmt.Errorf("include pattern %q matched directory %s", pattern, includeDisplayPath(baseDir, match))
			}
			ext := strings.ToLower(filepath.Ext(match))
			if ext != ".yaml" && ext != ".yml" {
				return nil, fmt.Errorf("include pattern %q matched non-YAML file %s", pattern, includeDisplayPath(baseDir, match))
			}
			seen[canonical] = struct{}{}
			paths = append(paths, match)
		}
	}
	return paths, nil
}

func includeDisplayPath(baseDir, path string) string {
	relative, err := filepath.Rel(baseDir, path)
	if err != nil || filepath.IsAbs(relative) {
		return filepath.ToSlash(filepath.Base(path))
	}
	return filepath.ToSlash(relative)
}
