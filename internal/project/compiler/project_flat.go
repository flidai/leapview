package compiler

// The flat project loader consumes the project-wide authoring contract (one include list
// per graph kind), keeps symbolic names in mutable compiler state, and emits a
// graph whose edges contain only canonical IDs.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
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

func loadFlatResources(project *Project, spec projectResource) error {
	if err := loadFlatModels(project, spec.Models.Include); err != nil {
		return err
	}
	if err := loadFlatSemanticModels(project, spec.SemanticModels.Include); err != nil {
		return err
	}
	if err := loadFlatPipelines(project, spec.Pipelines.Include); err != nil {
		return err
	}
	if err := loadFlatDashboards(project, spec.Dashboards.Include); err != nil {
		return err
	}
	if err := loadFlatPublications(project, spec.Publications.Include); err != nil {
		return err
	}
	if err := loadFlatAccess(project, spec.Access.Include); err != nil {
		return err
	}
	return nil
}

func flatResourceIdentity(project *Project, envelope resourceEnvelope, path, kind string) (string, string, error) {
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
	if id == string(project.ID) {
		return "", "", resourceError(path, id, "metadata.id", "%s metadata.id duplicates project id", path)
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

func loadFlatModels(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
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

func loadFlatSemanticModels(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
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
		var spec projectSemanticModelSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
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
		project.SemanticModelAIContexts[name] = envelope.AIContext
		project.SemanticModelIDs[name], project.SemanticModelPaths[name] = id, path
	}
	return nil
}

func loadFlatPipelines(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
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

func loadFlatDashboards(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		document, err := LoadDashboardDocumentForProject(path, project.BaseDir)
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

// Kept as a tiny indirection so the dashboard decoder stays focused on the
// project resource envelope.
func appearanceValidate(p dashboardappearance.Patch) error {
	return dashboardappearance.ValidatePatch(p)
}

func loadFlatPublications(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		if envelope.Kind != "DashboardPublication" {
			return resourceError(path, envelopeResourceID(envelope, ""), "kind", "%s kind = %q, want DashboardPublication", path, envelope.Kind)
		}
		var spec dashboardPublicationSpec
		if err := envelope.Spec.Decode(&spec); err != nil {
			return resourceError(path, envelopeResourceID(envelope, ""), "spec", "%s spec: %s", path, err)
		}
		id, name, err := flatResourceIdentity(project, envelope, path, "dashboard_publication")
		if err != nil {
			return err
		}
		project.Publications[name] = publication.Definition{Name: id, Dashboard: strings.TrimSpace(spec.Dashboard), DefaultPage: strings.TrimSpace(spec.DefaultPage), AllowedOrigins: append([]string(nil), spec.Embedding.AllowedOrigins...)}
		project.PublicationPaths[name] = path
	}
	return nil
}

func loadFlatAccess(project *Project, includes []string) error {
	paths, err := expandIncludes(project.BaseDir, includes)
	if err != nil {
		return err
	}
	for _, path := range paths {
		envelope, err := readEnvelope(path)
		if err != nil {
			return err
		}
		id, name, err := flatResourceIdentity(project, envelope, path, strings.ToLower(envelope.Kind))
		if err != nil {
			return err
		}
		switch envelope.Kind {
		case "Group":
			var spec projectGroupSpec
			if err := envelope.Spec.Decode(&spec); err != nil {
				return err
			}
			group := projectAccessGroup(name, spec)
			group.ID = id
			project.Access.Groups[name] = group
		case "RoleBinding":
			var spec projectRoleBindingSpec
			if err := envelope.Spec.Decode(&spec); err != nil {
				return err
			}
			binding := projectAccessRoleBinding(name, spec)
			binding.ID = id
			project.Access.RoleBindings[name] = binding
		case "Grant":
			var spec projectGrantSpec
			if err := envelope.Spec.Decode(&spec); err != nil {
				return err
			}
			grant := projectAccessGrant(name, spec)
			grant.ID, grant.Name = id, name
			project.Access.Grants[name] = grant
		case "DataPolicy":
			var spec projectDataPolicySpec
			if err := envelope.Spec.Decode(&spec); err != nil {
				return err
			}
			policy, err := projectAccessDataPolicy(name, spec)
			if err != nil {
				return err
			}
			policy.ID, policy.Name = id, name
			project.Access.DataPolicies[name] = policy
		default:
			return resourceError(path, id, "kind", "%s kind = %q is not a project access sidecar", path, envelope.Kind)
		}
		project.AccessPaths[name], project.ResourcePaths[id] = path, path
	}
	return nil
}

func validateFlatProject(project Project) error {
	if project.ID == "" {
		return resourceError("", "", "metadata.id", "project metadata.id is required")
	}
	if _, err := projectgraph.NewResourceID(string(project.ID)); err != nil {
		return err
	}
	resources, err := flatResources(project)
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
		sourceAliases, sourceReverse, err := sourceAliasesForProject(project)
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
		validatedModel := &semanticmodel.Model{Name: project.Name, Connections: copyConnections(project.Connections), Sources: aliasedSources, Datasets: runtimeDatasets, Tables: runtimeTables}
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
	for name, pub := range project.Publications {
		if _, err := resolver.resolve(pub.Dashboard, projectgraph.KindDashboard); err != nil {
			return resourceError(project.PublicationPaths[name], project.ResourceIDs["dashboard_publication:"+name], "spec.dashboard", "DashboardPublication %q: %v", name, err)
		}
		dashboard := project.Dashboards[authoredNameByID(pub.Dashboard, project.DashboardIDs)]
		if dashboard == nil {
			return resourceError(project.PublicationPaths[name], project.ResourceIDs["dashboard_publication:"+name], "spec.dashboard", "DashboardPublication %q references unknown Dashboard %q", name, pub.Dashboard)
		}
		if strings.TrimSpace(pub.DefaultPage) != "" {
			found := false
			for _, page := range dashboard.Spec.Pages {
				if page.ID == pub.DefaultPage {
					found = true
					break
				}
			}
			if !found {
				return resourceError(project.PublicationPaths[name], project.ResourceIDs["dashboard_publication:"+name], "spec.defaultPage", "DashboardPublication %q references unknown page %q", name, pub.DefaultPage)
			}
		}
		origins := make([]string, 0, len(pub.AllowedOrigins))
		seenOrigins := map[string]struct{}{}
		for index, authored := range pub.AllowedOrigins {
			origin, err := validatePublicationOrigin(authored)
			if err != nil {
				return resourceError(project.PublicationPaths[name], project.ResourceIDs["dashboard_publication:"+name], fmt.Sprintf("spec.embedding.allowedOrigins[%d]", index), "DashboardPublication %q origin %q: %v", name, authored, err)
			}
			if _, exists := seenOrigins[origin]; exists {
				return resourceError(project.PublicationPaths[name], project.ResourceIDs["dashboard_publication:"+name], "spec.embedding.allowedOrigins", "DashboardPublication %q has duplicate origin %q", name, origin)
			}
			seenOrigins[origin] = struct{}{}
			origins = append(origins, origin)
		}
		sort.Strings(origins)
		pub.AllowedOrigins = origins
		project.Publications[name] = pub
	}
	return validateFlatAccess(project, resolver)
}

func validateFlatAccess(project Project, resolver resourceResolver) error {
	validRoles := map[string]struct{}{"owner": {}, "admin": {}, "deployer": {}, "data_deployer": {}, "contributor": {}, "editor": {}, "member": {}, "viewer": {}}
	for name, group := range project.Access.Groups {
		for index, member := range group.Members {
			if strings.TrimSpace(member.PrincipalID) == "" && strings.TrimSpace(member.Email) == "" {
				return resourceError(project.AccessPaths[name], project.ResourceIDs["group:"+name], fmt.Sprintf("spec.members[%d]", index), "Group %q member requires principalId or email", name)
			}
		}
	}
	for name, binding := range project.Access.RoleBindings {
		if _, ok := validRoles[binding.Role]; !ok {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["rolebinding:"+name], "spec.role", "RoleBinding %q references unknown role %q", name, binding.Role)
		}
		if err := validateFlatAccessSubject(project, name, "RoleBinding", binding.Subject, false); err != nil {
			return err
		}
	}
	validObjectKinds := map[string]projectgraph.Kind{"project": projectgraph.KindProject, "connection": projectgraph.KindConnection, "source": projectgraph.KindSource, "model": projectgraph.KindModel, "semantic_model": projectgraph.KindSemanticModel, "pipeline": projectgraph.KindPipeline, "dashboard": projectgraph.KindDashboard}
	for name, grant := range project.Access.Grants {
		kind, ok := validObjectKinds[grant.Object.Kind]
		if !ok {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["grant:"+name], "spec.object.kind", "Grant %q has unsupported object kind %q", name, grant.Object.Kind)
		}
		if !validCapabilityForKind(grant.Capability, grant.Object.Kind) {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["grant:"+name], "spec.capability", "Grant %q has unsupported capability %q for %s", name, grant.Capability, grant.Object.Kind)
		}
		if _, err := resolver.resolve(grant.Object.ID, kind); err != nil {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["grant:"+name], "spec.object.id", "Grant %q: %v", name, err)
		}
		if grant.Subject.Kind == "dashboard_publication" {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["grant:"+name], "spec.subject.kind", "Grant %q cannot target dashboard publication", name)
		}
		if err := validateFlatAccessSubject(project, name, "Grant", grant.Subject, false); err != nil {
			return err
		}
	}
	for name, policy := range project.Access.DataPolicies {
		if policy.PolicyType != "row_filter" && policy.PolicyType != "column_mask" {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["datapolicy:"+name], "spec.policyType", "DataPolicy %q has unsupported policyType %q", name, policy.PolicyType)
		}
		if strings.TrimSpace(policy.ExpressionJSON) == "" {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["datapolicy:"+name], "spec.expression", "DataPolicy %q requires expression", name)
		}
		if policy.Object.Kind != "source" && policy.Object.Kind != "model" && policy.Object.Kind != "semantic_model" {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["datapolicy:"+name], "spec.object.kind", "DataPolicy %q has unsupported object kind %q", name, policy.Object.Kind)
		}
		if strings.TrimSpace(policy.Object.ID) == "" {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["datapolicy:"+name], "spec.object.id", "DataPolicy %q object id is required", name)
		}
		if kind, ok := validObjectKinds[policy.Object.Kind]; ok {
			if _, err := resolver.resolve(policy.Object.ID, kind); err != nil {
				return resourceError(project.AccessPaths[name], project.ResourceIDs["datapolicy:"+name], "spec.object.id", "DataPolicy %q: %v", name, err)
			}
		}
		if _, err := accesspolicy.Compile(policy.ID, policy.PolicyType, policy.ExpressionJSON); err != nil {
			return resourceError(project.AccessPaths[name], project.ResourceIDs["datapolicy:"+name], "spec.expression", "DataPolicy %q expression: %v", name, err)
		}
		if err := validateFlatAccessSubject(project, name, "DataPolicy", policy.Subject, true); err != nil {
			return err
		}
	}
	return nil
}

func validateFlatAccessSubject(project Project, name, kind string, subject manifest.Subject, allowPublication bool) error {
	path := project.AccessPaths[name]
	id := project.ResourceIDs[strings.ToLower(kind)+":"+name]
	switch subject.Kind {
	case "principal":
		if strings.TrimSpace(subject.PrincipalID) == "" && strings.TrimSpace(subject.Email) == "" {
			return resourceError(path, id, "spec.subject", "%s %q principal subject requires principalId or email", kind, name)
		}
	case "service_principal":
		if strings.TrimSpace(subject.PrincipalID) == "" {
			return resourceError(path, id, "spec.subject.principalId", "%s %q service_principal subject requires principalId", kind, name)
		}
	case "group":
		if strings.TrimSpace(subject.Group) == "" {
			return resourceError(path, id, "spec.subject.group", "%s %q group subject requires group", kind, name)
		}
		if _, ok := project.Access.Groups[authoredNameByID(subject.Group, accessIDsByName(project, "group"))]; !ok {
			return resourceError(path, id, "spec.subject.group", "%s %q references unknown Group %q", kind, name, subject.Group)
		}
	case "dashboard_publication":
		if !allowPublication {
			return resourceError(path, id, "spec.subject.kind", "%s %q does not support dashboard publication subjects", kind, name)
		}
		if _, ok := project.Publications[authoredNameByID(subject.Publication, accessIDsByName(project, "dashboard_publication"))]; !ok {
			return resourceError(path, id, "spec.subject.publication", "%s %q references unknown DashboardPublication %q", kind, name, subject.Publication)
		}
	default:
		return resourceError(path, id, "spec.subject.kind", "%s %q has unsupported subject kind %q", kind, name, subject.Kind)
	}
	return nil
}

func validCapabilityForKind(capability, kind string) bool {
	resourceKind, err := projectgraph.ParseKind(kind)
	if err != nil {
		return false
	}
	canonicalCapability, err := access.ParseCapability(capability)
	if err != nil {
		return false
	}
	return access.SupportsCapability(resourceKind, canonicalCapability)
}

func accessIDsByName(project Project, kind string) map[string]string {
	result := map[string]string{}
	for key, id := range project.ResourceIDs {
		prefix, name, ok := strings.Cut(key, ":")
		if ok && prefix == kind {
			result[name] = id
		}
	}
	return result
}

func flatResourceMetadata(envelope metadata, fallback string) projectgraph.Metadata {
	return projectgraph.Metadata{DisplayName: firstNonEmpty(envelope.DisplayName, envelope.Title, fallback), Description: envelope.Description, Owner: envelope.Owner, Domain: envelope.Domain, Tags: append([]string(nil), envelope.Tags...), Documentation: envelope.Documentation}
}

func projectRelativePath(project *Project, path string) string {
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

func flatResources(project Project) ([]projectgraph.Resource, error) {
	resources := make([]projectgraph.Resource, 0, len(project.ResourceIDs)+1)
	resources = append(resources, projectgraph.Resource{ID: project.ID, Kind: projectgraph.KindProject, Name: project.Name, Metadata: project.Metadata, Provenance: projectgraph.Provenance{Origin: "project", Path: projectRelativePath(&project, project.ProjectPath)}})
	for name, id := range project.SourceIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindSource, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "project", Path: projectRelativePath(&project, project.SourcePaths[name])}})
	}
	for name, id := range project.ModelIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindModel, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "project", Path: projectRelativePath(&project, project.ModelPaths[name])}})
	}
	for name, id := range project.SemanticModelIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindSemanticModel, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "project", Path: projectRelativePath(&project, project.SemanticModelPaths[name])}})
	}
	for name, id := range project.DashboardIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindDashboard, Name: name, Metadata: project.DashboardMetadata[name], Provenance: projectgraph.Provenance{Origin: "project", Path: projectRelativePath(&project, project.DashboardPaths[name])}})
	}
	for name, id := range project.PipelineIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindPipeline, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "project", Path: projectRelativePath(&project, project.PipelinePaths[name])}})
	}
	for name, id := range project.ConnectionIDs {
		resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindConnection, Name: name, Metadata: project.ResourceMetadata[id], Provenance: projectgraph.Provenance{Origin: "project", Path: projectRelativePath(&project, project.ConnectionPaths[name])}})
	}
	return resources, nil
}

func compileProjectGraph(project Project) (projectgraph.ProjectGraph, error) {
	resources, err := flatResources(project)
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

// Graph returns the compiled graph retained by a flat Project.
func (project Project) GraphValue() projectgraph.ProjectGraph { return project.Graph }
