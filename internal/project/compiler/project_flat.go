package compiler

// The flat resource loader consumes resources discovered beneath the fixed
// source-root directories, keeps symbolic names in mutable compiler state, and
// emits a graph whose edges contain only canonical IDs.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func copySources(in map[string]semanticmodel.Source) map[string]semanticmodel.Source {
	out := make(map[string]semanticmodel.Source, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

type resourceResolver struct {
	byID   map[projectgraph.ResourceID]projectgraph.Resource
	byName map[string]projectgraph.Resource
}

func newResourceResolver(resources []projectgraph.Resource) (resourceResolver, error) {
	r := resourceResolver{byID: make(map[projectgraph.ResourceID]projectgraph.Resource, len(resources)), byName: make(map[string]projectgraph.Resource, len(resources))}
	for _, resource := range resources {
		if _, exists := r.byID[resource.ID]; exists {
			return resourceResolver{}, fmt.Errorf("duplicate resource id %q", resource.ID)
		}
		if _, exists := r.byName[resource.Name]; exists {
			return resourceResolver{}, fmt.Errorf("ambiguous resource name %q", resource.Name)
		}
		r.byID[resource.ID] = resource
		r.byName[resource.Name] = resource
	}
	return r, nil
}

func (r resourceResolver) resolve(ref string, expected projectgraph.Kind) (projectgraph.ResourceID, error) {
	ref = strings.TrimSpace(ref)
	resource, ok := r.byID[projectgraph.ResourceID(ref)]
	if !ok {
		resource, ok = r.byName[ref]
	}
	if !ok {
		return "", fmt.Errorf("reference %q is missing", ref)
	}
	if expected != "" && resource.Kind != expected {
		return "", fmt.Errorf("reference %q resolves to %s, want %s", ref, resource.Kind, expected)
	}
	return resource.ID, nil
}

func flatResourceIdentity(project *sourceAssembly, envelope resourceEnvelope, path, kind string) (string, string, error) {
	name := strings.TrimSpace(envelope.Metadata.Name)
	id := strings.TrimSpace(envelope.Metadata.ID)
	if id == "" {
		return "", "", resourceError(path, "", "metadata.id", "%s metadata.id is required", path)
	}
	if name == "" {
		return "", "", resourceError(path, id, "metadata.name", "%s metadata.name is required", path)
	}
	if _, err := projectgraph.NewResourceID(id); err != nil {
		return "", "", resourceError(path, id, "metadata.id", "%s metadata.id: %v", path, err)
	}
	if owner, exists := project.ResourceIDOwners[id]; exists {
		return "", "", resourceError(path, id, "metadata.id", "%s metadata.id duplicates resource %s", path, owner)
	}
	project.ResourceIDOwners[id] = kind + ":" + name
	if _, exists := project.ResourceIDs[kind+":"+name]; exists {
		return "", "", resourceError(path, id, "metadata.name", "duplicate %s %q", kind, name)
	}
	project.ResourceIDs[kind+":"+name] = id
	project.ResourcePaths[id] = path
	project.ResourceMetadata[id] = flatResourceMetadata(envelope.Metadata, name)
	return id, name, nil
}

func loadFlatModels(project *sourceAssembly, paths []string) error {
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "Model" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want Model", path, envelope.Kind)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		table, aiContext, authoredDefinition, err := decodeModelResourceWithDefinition(path, content, envelope.Metadata)
		if err != nil {
			return err
		}
		id, name, err := flatResourceIdentity(project, envelope, path, "model")
		if err != nil {
			return err
		}
		if _, exists := project.Models[name]; exists {
			return resourceError(path, id, "metadata.name", "duplicate Model %q", name)
		}
		table.AIContext = aiContext
		project.Models[name] = table
		project.ModelDefinitions[name] = authoredDefinition
		project.ModelSources[name] = string(content)
		project.ResourceSources[id] = string(content)
		project.ModelAIContexts[name] = aiContext
		project.ModelIDs[name], project.ModelPaths[name] = id, path
	}
	return nil
}

func loadFlatSemanticModels(project *sourceAssembly, paths []string) error {
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "SemanticModel" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want SemanticModel", path, envelope.Kind)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		spec, aiContext, err := decodeSemanticModelResource(path, content)
		if err != nil {
			return resourceError(path, envelopeResourceID(envelope, ""), "spec", "%s spec: %s", path, err)
		}
		id, name, err := flatResourceIdentity(project, envelope, path, "semantic_model")
		if err != nil {
			return err
		}
		if _, exists := project.SemanticModels[name]; exists {
			return resourceError(path, id, "metadata.name", "duplicate SemanticModel %q", name)
		}
		project.SemanticModels[name] = spec
		project.ResourceSources[id] = string(content)
		project.SemanticModelAIContexts[name] = aiContext
		project.SemanticModelIDs[name], project.SemanticModelPaths[name] = id, path
	}
	return nil
}

func loadFlatPipelines(project *sourceAssembly, paths []string) error {
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "Pipeline" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want Pipeline", path, envelope.Kind)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		id, name, err := flatResourceIdentity(project, envelope, path, "pipeline")
		if err != nil {
			return err
		}
		pipeline, err := LoadRefreshPipeline(path)
		if err != nil {
			return resourceError(path, id, "spec", "Pipeline %q: %v", name, err)
		}
		pipeline.ID, pipeline.Name = projectgraph.ResourceID(id), name
		project.RefreshPipelines[name] = pipeline
		project.ResourceSources[id] = string(content)
		project.PipelineIDs[name], project.PipelinePaths[name] = id, path
	}
	return nil
}

func loadFlatDashboards(project *sourceAssembly, paths []string) error {
	for _, path := range paths {
		document, err := LoadDashboardDocumentForSourceRoot(path, project.BaseDir)
		if err != nil {
			return err
		}
		content, err := ExportDashboard(document)
		if err != nil {
			return err
		}
		envelope := resourceEnvelope{APIVersion: string(document.APIVersion), Kind: string(document.Kind), Metadata: metadata{ID: document.Metadata.ID, Name: document.Metadata.Name, Description: valueOrEmpty(document.Metadata.Description), Owner: valueOrEmpty(document.Metadata.Owner), Domain: valueOrEmpty(document.Metadata.Domain), Tags: valueOrStrings(document.Metadata.Tags), Documentation: valueOrEmpty(document.Metadata.Documentation)}}
		id, name, err := flatResourceIdentity(project, envelope, path, "dashboard")
		if err != nil {
			return err
		}
		if _, exists := project.Dashboards[name]; exists {
			return resourceError(path, id, "metadata.name", "duplicate Dashboard %q", name)
		}
		document.Metadata.ID = id
		project.Dashboards[name] = &document
		project.ResourceSources[id] = string(content)
		project.DashboardIDs[name], project.DashboardPaths[name] = id, path
		project.DashboardMetadata[name] = projectgraph.Metadata{DisplayName: firstNonEmpty(valueOrEmpty(document.Metadata.DisplayName), name), Description: envelope.Metadata.Description, Owner: envelope.Metadata.Owner, Domain: envelope.Metadata.Domain, Tags: append([]string(nil), envelope.Metadata.Tags...), Documentation: envelope.Metadata.Documentation}
	}
	return nil
}

func validateSourceAssembly(project sourceAssembly) error {
	resources, err := sourceResources(project)
	if err != nil {
		return err
	}
	_, err = projectgraph.NewProjectGraph(resources, nil)
	if err != nil {
		return err
	}
	resolver, err := newResourceResolver(resources)
	if err != nil {
		return err
	}
	for name, connection := range project.Connections {
		if _, err := connection.ValidateAuthored(name); err != nil {
			return resourceError(project.ConnectionPaths[name], project.ConnectionIDs[name], "spec", "Connection %q: %v", name, err)
		}
	}
	for name, source := range project.Sources {
		if source.Path != "" && source.Format == "" {
			return resourceError(project.SourcePaths[name], project.SourceIDs[name], "spec.location.format", "Source %q path requires explicit format", name)
		}
		if connection, ok := project.Connections[source.Connection]; ok && source.Path != "" {
			effective, err := ResolveEffectivePathLocation(source, connection)
			if err != nil {
				return resourceError(project.SourcePaths[name], project.SourceIDs[name], "spec.location.options", "Source %q: %s", name, err)
			}
			source.EffectivePathLocation = effective
			project.Sources[name] = source
		}
		if err := source.Validate(localSourceName(name), project.Connections); err != nil {
			return resourceError(project.SourcePaths[name], project.SourceIDs[name], "spec", "Source %q: %v", name, err)
		}
	}
	if len(project.Models) > 0 {
		sourceAliases, sourceReverse, err := sourceAliasesForAssembly(project)
		if err != nil {
			return err
		}
		aliasedSources := make(map[string]semanticmodel.Source, len(project.Sources))
		for name, source := range project.Sources {
			alias := sourceAliases[name]
			aliasedSources[alias] = source
		}
		// Validate Model materializations through the same strict dataset/table
		// binding contract used by semantic models. A flat Model document does
		// not author datasets itself, so bind each physical model under its own
		// alias for this validation snapshot and retain the explicit ModelName
		// on the table. This keeps validation strict without inventing a
		// compatibility path that permits unbound runtime tables.
		runtimeTables := translatedTablesForRuntime(project.Models, sourceAliases)
		runtimeDatasets := make(map[string]semanticmodel.SemanticDatasetSpec, len(runtimeTables))
		for name, table := range runtimeTables {
			table.ModelName = name
			runtimeTables[name] = table
			runtimeDatasets[name] = semanticmodel.SemanticDatasetSpec{Model: name}
		}
		validatedModel := &semanticmodel.Model{Name: "source-root", Connections: copyConnections(project.Connections), Sources: aliasedSources, Datasets: runtimeDatasets, Tables: runtimeTables}
		if err := deriveModelSQLDependencies(validatedModel); err != nil {
			for name := range project.Models {
				return resourceError(project.ModelPaths[name], project.ModelIDs[name], "spec", "Model %q SQL validation: %v", name, err)
			}
			return err
		}
		if err := validatedModel.ValidateAuthored(); err != nil {
			for name := range project.Models {
				return resourceError(project.ModelPaths[name], project.ModelIDs[name], "spec", "Model %q validation: %v", name, err)
			}
			return err
		}
		for name, table := range validatedModel.Tables {
			for index, dependency := range table.SourceDependencies {
				if original, ok := sourceReverse[dependency]; ok {
					table.SourceDependencies[index] = original
				}
			}
			if original, ok := sourceReverse[table.Execution.Source]; ok {
				table.Execution.Source = original
			}
			for index, source := range table.SourceDependencies {
				if original, ok := sourceReverse[source]; ok {
					table.SourceDependencies[index] = original
				}
			}
			project.Models[name] = table
		}
	}
	for name, source := range project.Sources {
		if source.Connection == "" {
			return resourceError(project.SourcePaths[name], project.SourceIDs[name], "spec.connection", "Source %q requires connection", name)
		}
		if _, err := resolver.resolve(source.Connection, projectgraph.KindConnection); err != nil {
			return resourceError(project.SourcePaths[name], project.SourceIDs[name], "spec.connection", "Source %q: %v", name, err)
		}
	}
	for name, model := range project.Models {
		refs := append([]string{}, model.SourceDependencies...)
		// A transform may derive solely from upstream Models. ValidateAuthored
		// already resolved those physical dependencies before this project-level
		// source lineage check.
		if len(refs) == 0 && len(model.ModelDependencies) == 0 {
			return resourceError(project.ModelPaths[name], project.ModelIDs[name], "spec.definition", "Model %q requires a governed source or model dependency", name)
		}
		for _, ref := range refs {
			if _, err := resolver.resolve(ref, projectgraph.KindSource); err != nil {
				return resourceError(project.ModelPaths[name], project.ModelIDs[name], "spec.definition", "Model %q governed dependency %q: %v", name, ref, err)
			}
		}
	}
	for name, spec := range project.SemanticModels {
		if len(spec.Datasets) == 0 {
			return resourceError(project.SemanticModelPaths[name], project.SemanticModelIDs[name], "spec.datasets", "SemanticModel %q requires datasets", name)
		}
		for datasetName, dataset := range spec.Datasets {
			if _, err := resolver.resolve(dataset.Model, projectgraph.KindModel); err != nil {
				return resourceError(project.SemanticModelPaths[name], project.SemanticModelIDs[name], "spec.datasets."+datasetName+".model", "SemanticModel %q: %v", name, err)
			}
		}
	}
	for name, dashboard := range project.Dashboards {
		if _, err := resolver.resolve(dashboard.Spec.SemanticModel, projectgraph.KindSemanticModel); err != nil {
			return resourceError(project.DashboardPaths[name], project.DashboardIDs[name], "spec.semanticModel", "Dashboard %q: %v", name, err)
		}
	}
	for name, pipeline := range project.RefreshPipelines {
		selection := pipeline.SemanticModelID.String()
		if project.SemanticModelIDs[selection] == "" {
			return resourceError(project.PipelinePaths[name], project.PipelineIDs[name], "spec.selection.semanticModel", "Pipeline %q references unknown authored SemanticModel name %q", name, selection)
		}
	}
	return nil
}

func flatResourceMetadata(envelope metadata, fallback string) projectgraph.Metadata {
	return projectgraph.Metadata{DisplayName: firstNonEmpty(envelope.DisplayName, envelope.Title, fallback), Description: envelope.Description, Owner: envelope.Owner, Domain: envelope.Domain, Tags: append([]string(nil), envelope.Tags...), Documentation: envelope.Documentation}
}

func projectRelativePath(project *sourceAssembly, path string) string {
	base, err := filepath.Abs(project.BaseDir)
	if err != nil {
		return ""
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || filepath.IsAbs(relative) {
		return ""
	}
	return filepath.ToSlash(relative)
}

func sourceResources(project sourceAssembly) ([]projectgraph.Resource, error) {
	resources := make([]projectgraph.Resource, 0, len(project.ResourceIDs))
	for name, id := range project.SourceIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindSource, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "source", Path: projectRelativePath(&project, project.SourcePaths[name])}})
	}
	for name, id := range project.ModelIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindModel, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "source", Path: projectRelativePath(&project, project.ModelPaths[name])}})
	}
	for name, id := range project.SemanticModelIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindSemanticModel, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "source", Path: projectRelativePath(&project, project.SemanticModelPaths[name])}})
	}
	for name, id := range project.DashboardIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindDashboard, Name: name, Metadata: project.DashboardMetadata[name], Provenance: projectgraph.Provenance{Origin: "source", Path: projectRelativePath(&project, project.DashboardPaths[name])}})
	}
	for name, id := range project.PipelineIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindPipeline, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "source", Path: projectRelativePath(&project, project.PipelinePaths[name])}})
	}
	for name, id := range project.ConnectionIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindConnection, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "source", Path: projectRelativePath(&project, project.ConnectionPaths[name])}})
	}
	return resources, nil
}

func compileGraph(project sourceAssembly) (projectgraph.ProjectGraph, error) {
	resources, err := sourceResources(project)
	if err != nil {
		return projectgraph.ProjectGraph{}, err
	}
	_, err = projectgraph.NewProjectGraph(resources, nil)
	if err != nil {
		return projectgraph.ProjectGraph{}, err
	}
	resolver, err := newResourceResolver(resources)
	if err != nil {
		return projectgraph.ProjectGraph{}, err
	}
	edges := make([]projectgraph.Edge, 0)
	seen := map[string]struct{}{}
	addEdge := func(from, to projectgraph.ResourceID, relation string) {
		key := string(from) + "|" + string(to) + "|" + relation
		if from == "" || to == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		edges = append(edges, projectgraph.Edge{From: from, To: to, Relation: relation})
	}
	for name, source := range project.Sources {
		from, _ := resolver.resolve(project.SourceIDs[name], projectgraph.KindSource)
		to, err := resolver.resolve(source.Connection, projectgraph.KindConnection)
		if err != nil {
			return projectgraph.ProjectGraph{}, err
		}
		addEdge(from, to, "uses_connection")
	}
	for name, model := range project.Models {
		from, _ := resolver.resolve(project.ModelIDs[name], projectgraph.KindModel)
		refs := append([]string{}, model.SourceDependencies...)
		for _, ref := range refs {
			to, err := resolver.resolve(ref, projectgraph.KindSource)
			if err != nil {
				return projectgraph.ProjectGraph{}, err
			}
			addEdge(from, to, "reads_source")
		}
		for _, ref := range model.ModelDependencies {
			to, err := resolver.resolve(ref, projectgraph.KindModel)
			if err != nil {
				return projectgraph.ProjectGraph{}, err
			}
			addEdge(from, to, "uses_model")
		}
	}
	for name, spec := range project.SemanticModels {
		from, _ := resolver.resolve(project.SemanticModelIDs[name], projectgraph.KindSemanticModel)
		for _, dataset := range spec.Datasets {
			ref := dataset.Model
			to, err := resolver.resolve(ref, projectgraph.KindModel)
			if err != nil {
				return projectgraph.ProjectGraph{}, err
			}
			addEdge(from, to, "uses_model")
		}
	}
	for name, dashboard := range project.Dashboards {
		from, _ := resolver.resolve(project.DashboardIDs[name], projectgraph.KindDashboard)
		to, err := resolver.resolve(dashboard.Spec.SemanticModel, projectgraph.KindSemanticModel)
		if err != nil {
			return projectgraph.ProjectGraph{}, err
		}
		addEdge(from, to, "uses_semantic_model")
	}
	for name, pipeline := range project.RefreshPipelines {
		from, _ := resolver.resolve(project.PipelineIDs[name], projectgraph.KindPipeline)
		to, err := resolver.resolve(pipeline.SemanticModelID.String(), projectgraph.KindSemanticModel)
		if err != nil {
			return projectgraph.ProjectGraph{}, err
		}
		addEdge(from, to, "refreshes")
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Relation < edges[j].Relation
	})
	return projectgraph.NewProjectGraph(resources, edges)
}
