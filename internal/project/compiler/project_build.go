package compiler

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/modelsql"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

// buildResourceManifest projects flat authored values into the portable manifest
// consumed by artifact compilation. It performs semantic-model and dashboard
// normalization once, retaining canonical resource IDs and source provenance.
func buildResourceManifest(project sourceAssembly) (manifest.ResourceManifest, error) {
	result := manifest.ResourceManifest{
		// A source bundle carries authored resources only; it never serializes a
		// root assembly node or derives identity from the source-root path.
		Connections: map[string]semanticmodel.Connection{}, Sources: map[string]semanticmodel.Source{}, Models: map[string]semanticmodel.Table{}, AuthoredModelDefinitions: map[string]manifest.AuthoredModelDefinition{}, AuthoredModelSources: map[string]string{}, AuthoredResourceSources: map[string]string{}, SemanticModels: map[string]*semanticmodel.Model{},
		DashboardDefinitions: map[string]dashboarddefinition.Definition{}, DashboardSources: map[string]manifest.DashboardSource{}, RefreshPipelines: map[string]refreshschedule.Definition{},
		NameIndex:     manifest.NameIndex{Connections: map[string]string{}, Sources: map[string]string{}, Models: map[string]string{}, SemanticModels: map[string]string{}, Dashboards: map[string]string{}, Pipelines: map[string]string{}},
		ResourceFiles: map[string]string{},
	}
	for id, path := range project.ResourcePaths {
		result.ResourceFiles[id] = projectRelativePath(&project, path)
	}
	for id, source := range project.ResourceSources {
		result.AuthoredResourceSources[id] = source
	}
	for name, value := range project.Connections {
		id := project.ConnectionIDs[name]
		if id == "" {
			return manifest.ResourceManifest{}, fmt.Errorf("connection %q has no stable id", name)
		}
		result.Connections[id] = value
		result.NameIndex.Connections[name] = id
	}
	for name, value := range project.Sources {
		id := project.SourceIDs[name]
		if id == "" {
			return manifest.ResourceManifest{}, fmt.Errorf("source %q has no stable id", name)
		}
		value.Connection = canonicalRef(project, "connection", value.Connection)
		result.Sources[id] = value
		result.NameIndex.Sources[name] = id
	}
	for name, value := range project.Models {
		id := project.ModelIDs[name]
		if id == "" {
			return manifest.ResourceManifest{}, fmt.Errorf("model %q has no stable id", name)
		}
		value.Execution.Source = canonicalRef(project, "source", value.Execution.Source)
		value.SourceDependencies = canonicalRefs(project, "source", value.SourceDependencies)
		value.ModelDependencies = canonicalRefs(project, "model", value.ModelDependencies)
		value.ModelName = name
		result.Models[id] = value
		if authored, ok := project.ModelDefinitions[name]; ok {
			result.AuthoredModelDefinitions[id] = authored
		}
		if source, ok := project.ModelSources[name]; ok {
			result.AuthoredModelSources[id] = source
		}
		result.NameIndex.Models[name] = id
	}
	semanticModelNames := make([]string, 0, len(project.SemanticModels))
	for name := range project.SemanticModels {
		semanticModelNames = append(semanticModelNames, name)
	}
	sort.Strings(semanticModelNames)
	var (
		sourceAliases           map[string]string
		translatedRuntimeTables map[string]semanticmodel.Table
		runtimeSources          map[string]semanticmodel.Source
	)
	if len(semanticModelNames) > 0 {
		var err error
		sourceAliases, _, err = sourceAliasesForAssembly(project)
		if err != nil {
			return manifest.ResourceManifest{}, err
		}
		runtimeTables := copyTables(project.Models)
		for tableName, table := range runtimeTables {
			table.Execution.Source = authoredNameByID(table.Execution.Source, project.SourceIDs)
			table.SourceDependencies = authoredNamesByID(table.SourceDependencies, project.SourceIDs)
			runtimeTables[tableName] = table
		}
		runtimeSources = make(map[string]semanticmodel.Source, len(project.Sources))
		for sourceName, source := range project.Sources {
			alias := sourceAliases[sourceName]
			runtimeSources[alias] = source
		}
		translatedRuntimeTables, err = translatedTablesForRuntime(runtimeTables, sourceAliases)
		if err != nil {
			return manifest.ResourceManifest{}, err
		}
	}
	for _, name := range semanticModelNames {
		spec := project.SemanticModels[name]
		id := project.SemanticModelIDs[name]
		if id == "" {
			return manifest.ResourceManifest{}, fmt.Errorf("semantic model %q has no stable id", name)
		}
		model := &semanticmodel.Model{Name: name, Title: name, AIContext: project.SemanticModelAIContexts[name], Connections: copyConnections(project.Connections), Sources: runtimeSources, Tables: translatedRuntimeTables}
		authoredSpec := spec
		if err := applySemanticModelSpec(model, authoredSpec); err != nil {
			return manifest.ResourceManifest{}, resourceError(project.SemanticModelPaths[name], id, "spec", "%s", err)
		}
		if err := deriveModelSQLDependencies(model); err != nil {
			return manifest.ResourceManifest{}, resourceError(project.SemanticModelPaths[name], id, "spec", "%s", err)
		}
		if err := model.ValidateAuthored(); err != nil {
			return manifest.ResourceManifest{}, resourceError(project.SemanticModelPaths[name], id, "spec", "%s", err)
		}
		result.SemanticModels[id] = model
		result.NameIndex.SemanticModels[name] = id
	}
	for name, dashboard := range project.Dashboards {
		id := project.DashboardIDs[name]
		if id == "" {
			return manifest.ResourceManifest{}, fmt.Errorf("dashboard %q has no stable id", name)
		}
		dashboardDocument := *dashboard
		dashboardDocument.Spec.SemanticModel = canonicalRef(project, "semantic_model", dashboardDocument.Spec.SemanticModel)
		compiled, err := dashboardcompiler.CompileDocument(dashboardDocument, result.SemanticModels)
		if err != nil {
			return manifest.ResourceManifest{}, resourceError(project.DashboardPaths[name], id, "spec", "loading dashboard %q: %s", name, err)
		}
		result.DashboardDefinitions[id] = compiled.Definition
		meta := project.DashboardMetadata[name]
		result.DashboardSources[id] = manifest.DashboardSource{Document: compiled.Normalized, Metadata: manifest.DashboardSourceMetadata{Name: name, Title: valueOrEmpty(dashboardDocument.Metadata.DisplayName), Description: valueOrEmpty(dashboardDocument.Metadata.Description), Owner: meta.Owner, Domain: meta.Domain, Tags: append([]string(nil), meta.Tags...)}, Path: projectRelativePath(&project, project.DashboardPaths[name])}
		result.NameIndex.Dashboards[name] = id
	}
	for name, value := range project.RefreshPipelines {
		id := project.PipelineIDs[name]
		if id == "" {
			return manifest.ResourceManifest{}, fmt.Errorf("pipeline %q has no stable id", name)
		}
		value.ID = projectgraph.ResourceID(id)
		value.Name = name
		value.SemanticModelID = projectgraph.ResourceID(canonicalRef(project, "semantic_model", value.SemanticModelID.String()))
		result.RefreshPipelines[id] = value
		result.NameIndex.Pipelines[name] = id
	}
	return result, nil
}

func canonicalRef(project sourceAssembly, kind, ref string) string {
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
	return ref
}
func canonicalRefs(project sourceAssembly, kind string, refs []string) []string {
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

func translatedTablesForRuntime(in map[string]semanticmodel.Table, sourceAliases map[string]string) (map[string]semanticmodel.Table, error) {
	out := copyTables(in)
	for name, table := range in {
		table = out[name]
		if alias, ok := sourceAliases[table.Execution.Source]; ok {
			table.Execution.Source = alias
		}
		for index, source := range table.SourceDependencies {
			if alias, ok := sourceAliases[source]; ok {
				table.SourceDependencies[index] = alias
			}
		}
		var err error
		table.Execution.SQL, err = rewriteSourceSQLForRuntime(table.Execution.SQL, sourceAliases)
		if err != nil {
			return nil, fmt.Errorf("Model %q SQL runtime rewrite: %w", name, err)
		}
		out[name] = table
	}
	return out, nil
}

func rewriteSourceSQLForRuntime(sql string, sourceAliases map[string]string) (string, error) {
	if strings.TrimSpace(sql) == "" {
		return sql, nil
	}
	analysis, err := modelsql.Analyze(context.Background(), sql)
	if err != nil {
		// Dependency validation runs immediately after this lowering pass and
		// carries the authored model identity into its diagnostic. Preserve
		// invalid SQL unchanged so that pass remains the reporting boundary.
		return sql, nil
	}
	replacements := make(map[string]string, len(analysis.SourceRefs))
	for _, source := range analysis.SourceRefs {
		alias, ok := sourceAliases[source]
		if !ok {
			// Leave unknown sources untouched so the dependency pass can report
			// the governed source error with its model identity.
			return sql, nil
		}
		replacements[source] = "source." + alias
	}
	return modelsql.RewriteSources(sql, analysis, replacements, false)
}

func localSourceName(sourceID string) string {
	return manifest.RuntimeSourceAlias(sourceID)
}

// sourceAliasesForAssembly builds the runtime source namespace used by model
// validation and semantic-model execution. Authored names may contain
// punctuation that is not valid in a semantic identifier, so localSourceName
// normalizes them. Two distinct names must never normalize to the same alias:
// silently overwriting one source would make a valid graph resolve to the
// wrong physical source. Iterating names in sorted order keeps diagnostics
// deterministic.
func sourceAliasesForAssembly(project sourceAssembly) (map[string]string, map[string]string, error) {
	aliasCapacity, err := checkedCapacitySum(len(project.Sources), len(project.Sources))
	if err != nil {
		return nil, nil, fmt.Errorf("runtime source alias capacity: %w", err)
	}
	aliases := make(map[string]string, aliasCapacity)
	reverse := make(map[string]string, len(project.Sources))
	aliasOwners := make(map[string]string, len(project.Sources))
	keyOwners := make(map[string]string, aliasCapacity)
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

func addSourceAlias(aliases map[string]string, keyOwners map[string]string, key, alias string, project sourceAssembly, sourceName string) error {
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

func applySemanticModelSpec(model *semanticmodel.Model, spec projectcontracts.SemanticModelSpec) error {
	accessPolicy, err := lowerSemanticAccessPolicy(spec)
	if err != nil {
		return err
	}
	datasets := lowerSemanticDatasets(spec.Datasets)
	relationshipsSpec, err := lowerSemanticRelationships(spec.Relationships)
	if err != nil {
		return err
	}
	dimensionsSpec := lowerSemanticDimensions(spec.Dimensions)
	dimensionsSpec, err = lowerLocalSemanticDimensions(spec.Datasets, dimensionsSpec)
	if err != nil {
		return err
	}
	filters, err := lowerSemanticFilters(spec.Filters)
	if err != nil {
		return err
	}
	metricsSpec, err := lowerSemanticMetrics(spec.Metrics, spec.Datasets)
	if err != nil {
		return err
	}
	if len(datasets) == 0 {
		return fmt.Errorf("SemanticModel %q requires datasets", model.Name)
	}
	baseTables := model.Tables
	tables := map[string]semanticmodel.Table{}
	for datasetName, dataset := range datasets {
		table, ok := baseTables[dataset.Model]
		if !ok {
			return fmt.Errorf("SemanticModel %q dataset %q references unknown Model %q", model.Name, datasetName, dataset.Model)
		}
		// The shared runtime table projection is immutable. Clone only the
		// selected table before the semantic model owns and normalizes it;
		// ValidateAuthored fills dimensions/columns and derives dependencies.
		table = copyTables(map[string]semanticmodel.Table{dataset.Model: table})[dataset.Model]
		table.ModelName = dataset.Model
		tables[datasetName] = table
	}
	relationships := make([]semanticmodel.Relationship, 0, len(relationshipsSpec))
	for id, relationship := range relationshipsSpec {
		fromDataset, fromFields, err := semanticRelationshipEndpointTuple(baseTables, datasets, relationship.From)
		if err != nil {
			return fmt.Errorf("SemanticModel %q relationship %q from: %w", model.Name, id, err)
		}
		toDataset, toFields, err := semanticRelationshipEndpointTuple(baseTables, datasets, relationship.To)
		if err != nil {
			return fmt.Errorf("SemanticModel %q relationship %q to: %w", model.Name, id, err)
		}
		cardinality := "many_to_one"
		if semanticRelationshipEndpointUnique(baseTables, datasets, relationship.From) && semanticRelationshipEndpointUnique(baseTables, datasets, relationship.To) {
			cardinality = "one_to_one"
		}
		relationships = append(relationships, semanticmodel.Relationship{ID: id, FromDataset: fromDataset, FromFields: fromFields, ToDataset: toDataset, ToFields: toFields, Cardinality: cardinality, Description: relationship.Description, AIContext: relationship.AIContext})
	}
	sort.SliceStable(relationships, func(i, j int) bool { return relationships[i].ID < relationships[j].ID })
	dimensions := map[string]semanticmodel.SemanticDimension{}
	for name, dimension := range dimensionsSpec {
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
	for name, metric := range metricsSpec {
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
	model.Datasets = datasets
	model.StructuredRelationships = relationshipsSpec
	model.Relationships = relationships
	model.Dimensions = dimensions
	model.Metrics = metrics
	model.Filters = filters
	model.AccessPolicy = accessPolicy
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
	return slices.Equal(left, right)
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
		entities := make(map[string]semanticmodel.EntityDefinition, len(value.Entities))
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
	return slices.Equal(left, right)
}
