package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func projectModelTable(spec projectModelTableSpec) semanticmodel.Table {
	table := semanticmodel.Table{
		Source:      spec.Source,
		Sources:     append([]string{}, spec.Sources...),
		SourceReads: copyStringSliceMap(spec.SourceReads),
		SQL:         spec.SQL,
		Transform:   spec.Transform,
		Entities:    spec.Entities,
		GrainEntity: spec.Grain.Entity,
		Dimensions:  map[string]semanticmodel.MetricDimension{},
		Description: spec.Description,
	}
	for name, field := range spec.Fields {
		table.Dimensions[name] = semanticmodel.MetricDimension{
			Label:       field.Label,
			Description: field.Description,
			Type:        canonicalDimensionTypeName(string(field.Datatype)),
			Datatype:    field.Datatype,
			AIContext:   field.AIContext,
		}
		if field.Datatype != "" {
			if table.Columns == nil {
				table.Columns = map[string]semanticmodel.ModelColumn{}
			}
			column := table.Columns[name]
			column.Type = canonicalDimensionTypeName(string(field.Datatype))
			column.Datatype = field.Datatype
			column.Description = firstNonEmpty(column.Description, field.Description)
			column.AIContext = field.AIContext
			table.Columns[name] = column
		}
	}
	return table
}

func projectDashboardPages(pages []projectDashboardPage) []dashboard.Page {
	out := make([]dashboard.Page, 0, len(pages))
	for _, page := range pages {
		out = append(out, dashboard.Page{
			ID:             page.ID,
			Title:          page.Title,
			Description:    page.Description,
			Canvas:         page.Canvas,
			Grid:           page.Grid,
			FilterBindings: page.FilterBindings,
			Visuals:        append([]dashboard.PageVisual(nil), page.Components...),
		})
	}
	return out
}

func projectAccessGroup(name string, spec projectGroupSpec) manifest.Group {
	group := manifest.Group{ID: name, Name: name, Description: spec.Description, Members: make([]manifest.GroupMember, 0, len(spec.Members))}
	for _, member := range spec.Members {
		group.Members = append(group.Members, manifest.GroupMember{PrincipalID: strings.TrimSpace(member.PrincipalID), Email: strings.TrimSpace(member.Email), DisplayName: strings.TrimSpace(member.DisplayName)})
	}
	sort.SliceStable(group.Members, func(i, j int) bool {
		return accessMemberSortKey(group.Members[i]) < accessMemberSortKey(group.Members[j])
	})
	return group
}

func projectAccessRoleBinding(name string, spec projectRoleBindingSpec) manifest.RoleBinding {
	return manifest.RoleBinding{ID: name, Name: name, Role: strings.TrimSpace(spec.Role), Subject: manifest.Subject{Kind: strings.TrimSpace(spec.Subject.Kind), PrincipalID: strings.TrimSpace(spec.Subject.PrincipalID), Email: strings.TrimSpace(spec.Subject.Email), DisplayName: strings.TrimSpace(spec.Subject.DisplayName), Group: strings.TrimSpace(spec.Subject.Group), Publication: strings.TrimSpace(spec.Subject.Publication)}}
}

func projectAccessGrant(name string, spec projectGrantSpec) manifest.Grant {
	return manifest.Grant{ID: name, Name: name, Object: manifest.SecurableRef{Kind: strings.TrimSpace(spec.Object.Kind), ID: strings.TrimSpace(spec.Object.ID)}, Subject: manifest.Subject{Kind: strings.TrimSpace(spec.Subject.Kind), PrincipalID: strings.TrimSpace(spec.Subject.PrincipalID), Email: strings.TrimSpace(spec.Subject.Email), DisplayName: strings.TrimSpace(spec.Subject.DisplayName), Group: strings.TrimSpace(spec.Subject.Group), Publication: strings.TrimSpace(spec.Subject.Publication)}, Capability: strings.TrimSpace(spec.Capability)}
}

func projectAccessDataPolicy(name string, spec projectDataPolicySpec) (manifest.DataPolicy, error) {
	expressionJSON := "{}"
	if spec.Expression.Kind != 0 {
		var expression any
		if err := spec.Expression.Decode(&expression); err != nil {
			return manifest.DataPolicy{}, err
		}
		expression = normalizeYAMLValue(expression)
		bytes, err := json.Marshal(expression)
		if err != nil {
			return manifest.DataPolicy{}, err
		}
		expressionJSON = string(bytes)
	}
	return manifest.DataPolicy{ID: name, Name: name, Object: manifest.SecurableRef{Kind: strings.TrimSpace(spec.Object.Kind), ID: strings.TrimSpace(spec.Object.ID)}, Subject: manifest.Subject{Kind: strings.TrimSpace(spec.Subject.Kind), PrincipalID: strings.TrimSpace(spec.Subject.PrincipalID), Email: strings.TrimSpace(spec.Subject.Email), DisplayName: strings.TrimSpace(spec.Subject.DisplayName), Group: strings.TrimSpace(spec.Subject.Group), Publication: strings.TrimSpace(spec.Subject.Publication)}, PolicyType: strings.TrimSpace(spec.PolicyType), ExpressionJSON: expressionJSON}, nil
}

func normalizeYAMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeYAMLValue(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[fmt.Sprint(key)] = normalizeYAMLValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeYAMLValue(item)
		}
		return out
	default:
		return value
	}
}

func sortedUniqueTrimmed(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// projectManifest projects flat authored values into the portable manifest
// consumed by artifact compilation. It performs semantic-model and dashboard
// normalization once, retaining canonical resource IDs and source provenance.
func projectManifest(project Project) (manifest.Project, error) {
	result := manifest.Project{
		ID: string(project.ID), Name: project.Name, Title: project.Metadata.DisplayName, Description: project.Metadata.Description,
		Connections: map[string]semanticmodel.Connection{}, Sources: map[string]semanticmodel.Source{}, Models: map[string]semanticmodel.Table{}, SemanticModels: map[string]*semanticmodel.Model{},
		DashboardDefinitions: map[string]dashboarddefinition.Definition{}, DashboardSources: map[string]manifest.DashboardSource{}, Publications: map[string]publication.Definition{}, RefreshPipelines: map[string]refreshschedule.Definition{},
		NameIndex:     manifest.NameIndex{Connections: map[string]string{}, Sources: map[string]string{}, Models: map[string]string{}, SemanticModels: map[string]string{}, Dashboards: map[string]string{}, Pipelines: map[string]string{}, Publications: map[string]string{}},
		ResourceFiles: map[string]string{},
	}
	result.ResourceFiles[string(project.ID)] = projectRelativePath(&project, project.ProjectPath)
	for id, path := range project.ResourcePaths {
		result.ResourceFiles[id] = projectRelativePath(&project, path)
	}
	for name, value := range project.Connections {
		id := project.ConnectionIDs[name]
		if id == "" {
			return manifest.Project{}, fmt.Errorf("connection %q has no stable id", name)
		}
		result.Connections[id] = value
		result.NameIndex.Connections[name] = id
	}
	for name, value := range project.Sources {
		id := project.SourceIDs[name]
		if id == "" {
			return manifest.Project{}, fmt.Errorf("source %q has no stable id", name)
		}
		value.Connection = canonicalRef(project, "connection", value.Connection)
		result.Sources[id] = value
		result.NameIndex.Sources[name] = id
	}
	for name, value := range project.Models {
		id := project.ModelIDs[name]
		if id == "" {
			return manifest.Project{}, fmt.Errorf("model %q has no stable id", name)
		}
		value.Source = canonicalRef(project, "source", value.Source)
		value.Sources = canonicalRefs(project, "source", value.Sources)
		value.SourceDependencies = canonicalRefs(project, "source", value.SourceDependencies)
		value.ModelDependencies = canonicalRefs(project, "model", value.ModelDependencies)
		result.Models[id] = value
		result.NameIndex.Models[name] = id
	}
	for name, spec := range project.SemanticModels {
		id := project.SemanticModelIDs[name]
		if id == "" {
			return manifest.Project{}, fmt.Errorf("semantic model %q has no stable id", name)
		}
		runtimeTables := copyTables(project.Models)
		for tableName, table := range runtimeTables {
			table.Source = authoredNameByID(table.Source, project.SourceIDs)
			table.Sources = authoredNamesByID(table.Sources, project.SourceIDs)
			runtimeTables[tableName] = table
		}
		sourceAliases, _, err := sourceAliasesForProject(project)
		if err != nil {
			return manifest.Project{}, err
		}
		runtimeSources := make(map[string]semanticmodel.Source, len(project.Sources))
		for sourceName, source := range project.Sources {
			alias := sourceAliases[sourceName]
			runtimeSources[alias] = source
		}
		model := &semanticmodel.Model{Name: name, Title: name, AIContext: project.SemanticModelAIContexts[name], Connections: copyConnections(project.Connections), Sources: runtimeSources, Tables: translatedTablesForRuntime(runtimeTables, sourceAliases)}
		authoredSpec := spec
		if err := applySemanticModelSpec(model, authoredSpec); err != nil {
			return manifest.Project{}, resourceError(project.SemanticModelPaths[name], id, "spec", "%s", err)
		}
		if err := model.ValidateAuthored(); err != nil {
			return manifest.Project{}, resourceError(project.SemanticModelPaths[name], id, "spec", "%s", err)
		}
		result.SemanticModels[id] = model
		result.NameIndex.SemanticModels[name] = id
	}
	for name, dashboard := range project.Dashboards {
		id := project.DashboardIDs[name]
		if id == "" {
			return manifest.Project{}, fmt.Errorf("dashboard %q has no stable id", name)
		}
		authoredDashboard := *dashboard
		authoredDashboard.SemanticModel = projectgraph.ResourceID(canonicalRef(project, "semantic_model", dashboard.SemanticModel.String()))
		compiled, err := dashboardcompiler.Compile(authoredDashboard, result.SemanticModels)
		if err != nil {
			return manifest.Project{}, resourceError(project.DashboardPaths[name], id, "spec", "loading dashboard %q: %s", name, err)
		}
		result.DashboardDefinitions[id] = compiled.Definition
		meta := project.DashboardMetadata[name]
		result.DashboardSources[id] = manifest.DashboardSource{Document: compiled.Normalized, Metadata: manifest.DashboardSourceMetadata{Name: name, Title: dashboard.Title, Description: dashboard.Description, Owner: meta.Owner, Domain: meta.Domain, Tags: append([]string(nil), meta.Tags...)}, Path: projectRelativePath(&project, project.DashboardPaths[name])}
		result.NameIndex.Dashboards[name] = id
	}
	for name, value := range project.Publications {
		id := project.ResourceIDs["dashboard_publication:"+name]
		if id == "" {
			return manifest.Project{}, fmt.Errorf("publication %q has no stable id", name)
		}
		value.Name = id
		value.Dashboard = canonicalRef(project, "dashboard", value.Dashboard)
		value.DependencyAssetIDs = projectDependencyClosure(project.Graph, value.Dashboard)
		value.ConfigurationDigest = publicationConfigurationDigest(value)
		result.Publications[id] = value
		result.NameIndex.Publications[name] = id
	}
	for name, value := range project.RefreshPipelines {
		id := project.PipelineIDs[name]
		if id == "" {
			return manifest.Project{}, fmt.Errorf("pipeline %q has no stable id", name)
		}
		value.ID = projectgraph.ResourceID(id)
		value.Name = name
		value.SemanticModelID = projectgraph.ResourceID(canonicalRef(project, "semantic_model", value.SemanticModelID.String()))
		result.RefreshPipelines[id] = value
		result.NameIndex.Pipelines[name] = id
	}
	access, err := canonicalAccessPolicy(project)
	if err != nil {
		return manifest.Project{}, err
	}
	result.Access = access
	return result, nil
}

// projectDependencyClosure returns the canonical resource IDs reachable from
// a dashboard through the authored graph. The dashboard itself is included so
// publication authorization can enforce the complete project-wide closure.
func projectDependencyClosure(graph projectgraph.ProjectGraph, root string) []string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	seen := map[projectgraph.ResourceID]struct{}{projectgraph.ResourceID(root): {}}
	queue := []projectgraph.ResourceID{projectgraph.ResourceID(root)}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range graph.Edges() {
			if edge.From != current {
				continue
			}
			if _, ok := seen[edge.To]; ok {
				continue
			}
			seen[edge.To] = struct{}{}
			queue = append(queue, edge.To)
		}
	}
	closure := make([]string, 0, len(seen))
	for id := range seen {
		closure = append(closure, string(id))
	}
	sort.Strings(closure)
	return closure
}

func publicationConfigurationDigest(value publication.Definition) string {
	value.ConfigurationDigest = ""
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalRef(project Project, kind, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if _, ok := project.ResourceIDOwners[ref]; ok {
		return ref
	}
	if id := project.ResourceIDs[kind+":"+ref]; id != "" {
		return id
	}
	if kind == "semantic_model" {
		if id := project.ResourceIDs["semantic_model:"+ref]; id != "" {
			return id
		}
	}
	return ref
}
func canonicalRefs(project Project, kind string, refs []string) []string {
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = canonicalRef(project, kind, ref)
	}
	return out
}

func authoredNameByID(ref string, ids map[string]string) string {
	for name, id := range ids {
		if ref == id {
			return name
		}
	}
	return ref
}
func authoredNamesByID(refs []string, ids map[string]string) []string {
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = authoredNameByID(ref, ids)
	}
	return out
}

func canonicalAccessPolicy(project Project) (manifest.AccessPolicy, error) {
	result := projectAccessPolicy()
	for name, value := range project.Access.Groups {
		if id := project.ResourceIDs["group:"+name]; id != "" {
			value.ID = id
			result.Groups[id] = value
		}
	}
	for name, value := range project.Access.RoleBindings {
		if id := project.ResourceIDs["rolebinding:"+name]; id != "" {
			value.ID = id
			value.Subject.Group = canonicalRef(project, "group", value.Subject.Group)
			value.Subject.Publication = canonicalRef(project, "dashboard_publication", value.Subject.Publication)
			result.RoleBindings[id] = value
		}
	}
	for name, value := range project.Access.Grants {
		if id := project.ResourceIDs["grant:"+name]; id != "" {
			value.ID = id
			value.Object.ID = canonicalRef(project, accessObjectKind(value.Object.Kind), value.Object.ID)
			value.Subject.Group = canonicalRef(project, "group", value.Subject.Group)
			value.Subject.Publication = canonicalRef(project, "dashboard_publication", value.Subject.Publication)
			result.Grants[id] = value
		}
	}
	for name, value := range project.Access.DataPolicies {
		if id := project.ResourceIDs["datapolicy:"+name]; id != "" {
			value.ID = id
			value.Object.ID = canonicalRef(project, accessObjectKind(value.Object.Kind), value.Object.ID)
			value.Subject.Group = canonicalRef(project, "group", value.Subject.Group)
			value.Subject.Publication = canonicalRef(project, "dashboard_publication", value.Subject.Publication)
			result.DataPolicies[id] = value
		}
	}
	return result, nil
}
func accessObjectKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "project":
		return "project"
	case "model":
		return "model"
	case "semantic_model":
		return "semantic_model"
	case "dashboard":
		return "dashboard"
	case "source":
		return "source"
	case "connection":
		return "connection"
	default:
		return strings.TrimSpace(kind)
	}
}

func translatedTablesForRuntime(in map[string]semanticmodel.Table, sourceAliases map[string]string) map[string]semanticmodel.Table {
	out := make(map[string]semanticmodel.Table, len(in))
	for name, table := range in {
		if alias, ok := sourceAliases[table.Source]; ok {
			table.Source = alias
		}
		for index, source := range table.Sources {
			if alias, ok := sourceAliases[source]; ok {
				table.Sources[index] = alias
			}
		}
		table.SQL = rewriteSourceSQLForRuntime(table.SQL, sourceAliases)
		table.Transform.SQL = rewriteSourceSQLForRuntime(table.Transform.SQL, sourceAliases)
		out[name] = table
	}
	return out
}

func rewriteSourceSQLForRuntime(sql string, sourceAliases map[string]string) string {
	for global, local := range sourceAliases {
		sql = strings.ReplaceAll(sql, `source."`+global+`"`, "source."+local)
		sql = strings.ReplaceAll(sql, "source."+global, "source."+local)
	}
	return sql
}

func localSourceName(sourceID string) string {
	var builder strings.Builder
	for index, char := range sourceID {
		valid := char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9'
		if valid {
			builder.WriteRune(char)
			continue
		}
		builder.WriteByte('_')
	}
	out := builder.String()
	if out == "" || out[0] >= '0' && out[0] <= '9' {
		out = "source_" + out
	}
	return out
}

// sourceAliasesForProject builds the runtime source namespace used by model
// validation and semantic-model execution. Authored names may contain
// punctuation that is not valid in a semantic identifier, so localSourceName
// normalizes them. Two distinct names must never normalize to the same alias:
// silently overwriting one source would make a valid graph resolve to the
// wrong physical source. Iterating names in sorted order keeps diagnostics
// deterministic.
func sourceAliasesForProject(project Project) (map[string]string, map[string]string, error) {
	aliases := make(map[string]string, len(project.Sources)*2)
	reverse := make(map[string]string, len(project.Sources))
	aliasOwners := make(map[string]string, len(project.Sources))
	keyOwners := make(map[string]string, len(project.Sources)*2)
	names := make([]string, 0, len(project.Sources))
	for name := range project.Sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		alias := localSourceName(name)
		if previous, ok := aliasOwners[alias]; ok && previous != name {
			return nil, nil, fmt.Errorf(
				"sources %q (id %q) and %q (id %q) map to runtime source alias %q",
				previous, project.SourceIDs[previous], name, project.SourceIDs[name], alias,
			)
		}
		aliasOwners[alias] = name
		reverse[alias] = name
		if err := addSourceAlias(aliases, keyOwners, name, alias, project, name); err != nil {
			return nil, nil, err
		}
		if sourceID := project.SourceIDs[name]; sourceID != "" {
			if err := addSourceAlias(aliases, keyOwners, sourceID, alias, project, name); err != nil {
				return nil, nil, err
			}
		}
	}
	return aliases, reverse, nil
}

func addSourceAlias(aliases map[string]string, keyOwners map[string]string, key, alias string, project Project, sourceName string) error {
	if previous, ok := aliases[key]; ok && previous != alias {
		previousName := keyOwners[key]
		return fmt.Errorf(
			"sources %q (id %q) and %q (id %q) map reference %q to different runtime aliases %q and %q",
			previousName, project.SourceIDs[previousName], sourceName, project.SourceIDs[sourceName], key, previous, alias,
		)
	}
	aliases[key] = alias
	keyOwners[key] = sourceName
	return nil
}

func applySemanticModelSpec(model *semanticmodel.Model, spec projectSemanticModelSpec) error {
	if len(spec.Datasets) == 0 {
		return fmt.Errorf("SemanticModel %q requires datasets", model.Name)
	}
	baseTables := model.Tables
	tables := map[string]semanticmodel.Table{}
	for datasetName, dataset := range spec.Datasets {
		table, ok := baseTables[dataset.Model]
		if !ok {
			return fmt.Errorf("SemanticModel %q dataset %q references unknown Model %q", model.Name, datasetName, dataset.Model)
		}
		tables[datasetName] = table
	}
	relationships := make([]semanticmodel.Relationship, 0, len(spec.Relationships))
	for id, relationship := range spec.Relationships {
		fromDataset, fromFields, err := semanticRelationshipEndpointTuple(baseTables, spec.Datasets, relationship.From)
		if err != nil {
			return fmt.Errorf("SemanticModel %q relationship %q from: %w", model.Name, id, err)
		}
		toDataset, toFields, err := semanticRelationshipEndpointTuple(baseTables, spec.Datasets, relationship.To)
		if err != nil {
			return fmt.Errorf("SemanticModel %q relationship %q to: %w", model.Name, id, err)
		}
		cardinality := "many_to_one"
		if semanticRelationshipEndpointUnique(baseTables, spec.Datasets, relationship.From) && semanticRelationshipEndpointUnique(baseTables, spec.Datasets, relationship.To) {
			cardinality = "one_to_one"
		}
		relationships = append(relationships, semanticmodel.Relationship{ID: id, FromDataset: fromDataset, FromFields: fromFields, ToDataset: toDataset, ToFields: toFields, Cardinality: cardinality, Description: relationship.Description, AIContext: relationship.AIContext})
	}
	sort.SliceStable(relationships, func(i, j int) bool { return relationships[i].ID < relationships[j].ID })
	dimensions := map[string]semanticmodel.SemanticDimension{}
	for name, dimension := range spec.Dimensions {
		converted := semanticmodel.SemanticDimension{Label: dimension.Label, Description: dimension.Description, Type: canonicalDimensionTypeName(string(dimension.Datatype)), Datatype: dimension.Datatype, Bindings: dimension.Bindings, AIContext: dimension.AIContext}
		if dimension.Time != nil {
			converted.NativeGrain = dimension.Time.NativeGrain
			converted.Grains = append([]string(nil), dimension.Time.Grains...)
			converted.Calendar = dimension.Time.Calendar
			converted.Timezone = dimension.Time.Timezone
		}
		dimensions[name] = converted
	}
	metrics := map[string]semanticmodel.Metric{}
	for name, metric := range spec.Metrics {
		common := semanticmodel.Metric{Label: metric.Label, Description: metric.Description, Unit: metric.Unit, Format: metric.Format, Hidden: metric.Hidden, AIContext: metric.AIContext}
		switch metric.Type {
		case "aggregate":
			if metric.Input == nil {
				return fmt.Errorf("metric %q aggregate input is required", name)
			}
			empty := metric.Empty
			if empty == "" {
				empty = "null"
				if metric.Aggregation == "count" || metric.Aggregation == "count_distinct" {
					empty = "zero"
				}
			}
			common.Type, common.Dataset, common.Aggregation, common.Input = metric.Type, metric.Dataset, metric.Aggregation, metric.Input
			common.Where, common.Empty, common.TimeDimension = append([]string(nil), metric.Where...), empty, metric.TimeDimension
			metrics[name] = common
		case "derived":
			common.Type, common.Expression = metric.Type, metric.Expression
			metrics[name] = common
		case "ratio":
			common.Type, common.Numerator, common.Denominator = metric.Type, metric.Numerator, metric.Denominator
			metrics[name] = common
		default:
			return fmt.Errorf("metric %q has unsupported type %q", name, metric.Type)
		}
	}
	model.Tables = tables
	model.Datasets = spec.Datasets
	model.StructuredRelationships = spec.Relationships
	model.Relationships = relationships
	model.Dimensions = dimensions
	model.Metrics = metrics
	model.Filters = spec.Filters
	return nil
}

func semanticRelationshipEndpointUnique(tables map[string]semanticmodel.Table, datasets map[string]semanticmodel.SemanticDatasetSpec, endpoint semanticmodel.RelationshipEndpointSpec) bool {
	dataset, ok := datasets[endpoint.Dataset]
	if !ok {
		return false
	}
	table, ok := tables[dataset.Model]
	if !ok {
		return false
	}
	if endpoint.Entity != "" {
		entity, ok := table.Entities[endpoint.Entity]
		return ok && (entity.Type == "primary" || entity.Type == "unique")
	}
	if len(endpoint.Fields) == 0 {
		return false
	}
	for _, entity := range table.Entities {
		if (entity.Type == "primary" || entity.Type == "unique") && sameOrderedFields(entity.Fields, endpoint.Fields) {
			return true
		}
	}
	return false
}

func sameOrderedFields(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func semanticRelationshipEndpointTuple(tables map[string]semanticmodel.Table, datasets map[string]semanticmodel.SemanticDatasetSpec, endpoint semanticmodel.RelationshipEndpointSpec) (string, []string, error) {
	dataset, ok := datasets[endpoint.Dataset]
	if !ok {
		return "", nil, fmt.Errorf("unknown dataset %q", endpoint.Dataset)
	}
	if len(endpoint.Fields) > 0 {
		return endpoint.Dataset, append([]string(nil), endpoint.Fields...), nil
	}
	if endpoint.Entity == "" {
		return "", nil, fmt.Errorf("endpoint requires entity or fields")
	}
	table, ok := tables[dataset.Model]
	if !ok {
		return "", nil, fmt.Errorf("unknown Model %q", dataset.Model)
	}
	entity, ok := table.Entities[endpoint.Entity]
	if !ok {
		return "", nil, fmt.Errorf("entity %q is not declared on Model %q", endpoint.Entity, dataset.Model)
	}
	return endpoint.Dataset, append([]string(nil), entity.Fields...), nil
}

func canonicalDimensionTypeName(value string) string {
	switch value {
	case "String":
		return "string"
	case "Integer", "Decimal", "Float":
		return "number"
	case "Boolean":
		return "boolean"
	case "Date":
		return "date"
	case "Time", "DateTime", "DateTimeTz":
		return "timestamp"
	default:
		return strings.ToLower(value)
	}
}

func firstConnectionName(connections map[string]semanticmodel.Connection) string {
	names := make([]string, 0, len(connections))
	for name := range connections {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func copyConnections(in map[string]semanticmodel.Connection) map[string]semanticmodel.Connection {
	out := make(map[string]semanticmodel.Connection, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyTables(in map[string]semanticmodel.Table) map[string]semanticmodel.Table {
	out := make(map[string]semanticmodel.Table, len(in))
	for key, value := range in {
		value.AIContext = copyAIContext(value.AIContext)
		value.Sources = append([]string(nil), value.Sources...)
		value.SourceReads = copyStringSliceMap(value.SourceReads)
		value.SourceDependencies = append([]string(nil), value.SourceDependencies...)
		value.ModelDependencies = append([]string(nil), value.ModelDependencies...)
		value.Columns = copyModelColumns(value.Columns)
		for name, column := range value.Columns {
			column.AIContext = copyAIContext(column.AIContext)
			value.Columns[name] = column
		}
		value.Dimensions = copyMetricDimensions(value.Dimensions)
		for name, dimension := range value.Dimensions {
			dimension.AIContext = copyAIContext(dimension.AIContext)
			value.Dimensions[name] = dimension
		}
		entities := make(map[string]semanticmodel.ModelEntitySpec, len(value.Entities))
		for name, entity := range value.Entities {
			entity.Fields = append([]string(nil), entity.Fields...)
			entity.AIContext = copyAIContext(entity.AIContext)
			entities[name] = entity
		}
		value.Entities = entities
		value.Schema.Columns = append([]semanticmodel.ColumnSchema(nil), value.Schema.Columns...)
		for index := range value.Schema.Columns {
			if value.Schema.Columns[index].Nullable != nil {
				nullable := *value.Schema.Columns[index].Nullable
				value.Schema.Columns[index].Nullable = &nullable
			}
		}
		out[key] = value
	}
	return out
}

func copyAIContext(in *semanticmodel.AIContext) *semanticmodel.AIContext {
	if in == nil {
		return nil
	}
	return &semanticmodel.AIContext{
		Instructions: in.Instructions,
		Synonyms:     append([]string(nil), in.Synonyms...),
		Examples:     append([]string(nil), in.Examples...),
	}
}

func copyMetricDimensions(in map[string]semanticmodel.MetricDimension) map[string]semanticmodel.MetricDimension {
	if in == nil {
		return nil
	}
	out := make(map[string]semanticmodel.MetricDimension, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyStringSliceMap(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, value := range in {
		out[key] = append([]string{}, value...)
	}
	return out
}

func copyModelColumns(in map[string]semanticmodel.ModelColumn) map[string]semanticmodel.ModelColumn {
	if in == nil {
		return nil
	}
	out := make(map[string]semanticmodel.ModelColumn, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func projectAccessPolicy() manifest.AccessPolicy {
	return manifest.AccessPolicy{Groups: map[string]manifest.Group{}, RoleBindings: map[string]manifest.RoleBinding{}, Grants: map[string]manifest.Grant{}, DataPolicies: map[string]manifest.DataPolicy{}}
}

func accessMemberSortKey(member manifest.GroupMember) string {
	return member.Email + "\x00" + member.PrincipalID + "\x00" + member.DisplayName
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sortedSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sameStringList(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
