package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

type assetDetailModel struct {
	Overview           []definitionFact
	Sections           []assetDetailSection
	SemanticModelGraph *uisignals.SemanticModelGraphSignal
}

type assetDetailSection struct {
	Title  string
	Signal string
	Table  recordTable
	Facts  []definitionFact
	Code   string
	Lang   string
}

func assetDetailUsesCodeBlock(asset projectview.DevelopAssetView) bool {
	return asset.Type == "model_table" && modelTableSQL(asset.Payload) != ""
}

func assetDetailModelForAsset(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) assetDetailModel {
	return assetDetailModelForAssetWithRefresh(project, asset, assets, edges, AssetRefreshState{})
}

func assetDetailModelForAssetWithRefresh(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, refresh AssetRefreshState) assetDetailModel {
	model := assetDetailModel{
		Overview: commonAssetOverviewFacts(asset, assets, shouldShowParentFact(asset.Type)),
	}
	switch asset.Type {
	case "semantic_model":
		semanticModelDetailModel(&model, project, asset, assets, refresh)
	case "model_table":
		modelTableDetailModel(&model, project, asset, assets, refresh)
	case "dashboard":
		dashboardDetailModel(&model, asset, assets)
	case "refresh_pipeline":
		refreshPipelineDetailModel(&model, asset, refresh)
	case "connection":
		connectionDetailModel(&model, project, asset, assets, edges)
	case "source":
		sourceDetailModel(&model, asset)
	case "metric":
		model.Overview = append(model.Overview, metricLeafFacts(asset)...)
	case "field":
		model.Overview = append(model.Overview, metricLeafFacts(asset)...)
	default:
		model.Overview = append(model.Overview, metaFacts(asset.Payload)...)
	}
	return model
}

func refreshPipelineDetailModel(model *assetDetailModel, asset projectview.DevelopAssetView, refresh AssetRefreshState) {
	semanticModel := metaString(asset.Payload, "SemanticModel", "semanticModel")
	schedules := metaSlice(asset.Payload, "Schedules", "schedules")
	lines := make([]string, 0, len(schedules)*2+1)
	for _, raw := range schedules {
		entry, _ := raw.(map[string]any)
		cron := metaString(entry, "Cron", "cron")
		timezone := metaString(entry, "Timezone", "timezone")
		lines = append(lines, "- cron: "+strconv.Quote(cron), "  timezone: "+timezone)
	}
	scheduleYAML := "Manual only"
	if len(lines) > 0 {
		scheduleYAML = "schedule:\n  " + strings.Join(lines, "\n  ")
	}
	nextLabel := "Manual only"
	if !refresh.NextRun.IsZero() {
		nextLabel = refresh.NextRun.Format(time.RFC3339)
	}
	model.Overview = append(model.Overview,
		definitionFact{Label: "Semantic model", Value: semanticModel, Code: true},
		definitionFact{Label: "Schedule", Value: scheduleYAML, Code: true, Wide: true},
		definitionFact{Label: "Next run", Value: nextLabel},
	)
	if refresh.DataVersion.SnapshotID > 0 {
		model.Overview = append(model.Overview,
			definitionFact{Label: "Current data version", Value: fmt.Sprintf("snapshot %d · %s", refresh.DataVersion.SnapshotID, refresh.DataVersion.Source), Code: true},
			definitionFact{Label: "Serving state", Value: refresh.DataVersion.ServingStateID, Code: true},
		)
	}
	model.Overview = append(model.Overview, refreshOverviewFacts(refresh)...)
}

func commonAssetOverviewFacts(asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, includeParent bool) []definitionFact {
	facts := []definitionFact{
		{Label: "Type", Value: assetTypeLabel(asset.Type)},
		{Label: "Key", Value: asset.Key, Code: true},
	}
	if includeParent {
		facts = append(facts, definitionFact{Label: "Parent", Value: assetParentTitle(asset.ParentID, assets)})
	}
	facts = append(facts, definitionFact{Label: "Description", Value: asset.Description, Wide: true})
	return facts
}

func shouldShowParentFact(typ string) bool {
	switch typ {
	case "catalog", "connection", "dashboard", "model_table", "semantic_model":
		return false
	default:
		return true
	}
}

func semanticModelDetailModel(model *assetDetailModel, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, refresh AssetRefreshState) {
	meta := asset.Payload
	datasetMeta := metaMap(meta, "Datasets")
	datasets := sortedMapKeys(datasetMeta)
	metrics := sortedMapKeys(metaMap(meta, "Metrics"))
	relationships := metaSlice(meta, "Relationships")
	model.SemanticModelGraph = semanticModelGraphSignal(meta)

	model.Overview = append(model.Overview,
		refreshOverviewFacts(refresh)...,
	)
	model.Sections = append(model.Sections,
		assetDetailSection{Title: fmt.Sprintf("Datasets (%d)", len(datasets)), Signal: "assetDetailsSemanticDatasetsTable", Table: semanticDatasetsTable(project.ID, asset, assets, meta, refresh)},
		assetDetailSection{Title: fmt.Sprintf("Metrics (%d)", len(metrics)), Signal: "assetDetailsSemanticMetricsTable", Table: semanticMetricsTable(project.ID, asset, assets, meta)},
		assetDetailSection{Title: fmt.Sprintf("Relationships (%d)", len(relationships)), Signal: "assetDetailsSemanticRelationshipsTable", Table: semanticRelationshipsTable(project.ID, asset, assets, meta)},
	)
}

func refreshOverviewFacts(refresh AssetRefreshState) []definitionFact {
	status := strings.TrimSpace(refresh.Latest.Status)
	if status == "" {
		status = "not refreshed"
	}
	return []definitionFact{
		{Label: "Refresh status", Value: status},
		{Label: "Last refreshed", Value: emptyDash(refresh.LatestSuccessful.FinishedAt)},
	}
}

func semanticFieldCount(tables map[string]any) int {
	count := 0
	for _, tableValue := range tables {
		table := asMap(tableValue)
		count += len(metaMap(table, "Dimensions"))
	}
	return count
}

func assetParentTitle(parentID string, assets []projectview.DevelopAssetView) string {
	if parentID == "" {
		return ""
	}
	for _, asset := range assets {
		if asset.ID == parentID {
			return assetTitle(asset)
		}
	}
	return parentID
}

func semanticConnectionsGrid(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	connections := metaMap(meta, "Connections")
	rows := make([]map[string]any, 0, len(connections))
	for _, name := range sortedMapKeys(connections) {
		connection := asMap(connections[name])
		child := semanticAssetByName(parent.Key, "connection", name, assets)
		rows = append(rows, map[string]any{
			"name":        name,
			"nameHref":    childHref(projectID, child),
			"kind":        emptyDash(metaString(connection, "Kind")),
			"credentials": recordTableBadgeValue(boolLabel(metaBool(connection, "credentials_configured")), "success"),
			"defaults":    compactJSON(metaValue(connection, "Defaults", "Options")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("180px")},
			{ID: "kind", Header: "Kind", Width: uisignals.Pointer("120px")},
			{ID: "credentials", Header: "Credentials", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "defaults", Header: "Defaults / options", Kind: uisignals.Pointer("expression")},
		},
		Rows:     rows,
		Empty:    "No connections are defined for this semantic model.",
		MinWidth: uisignals.Pointer("760px"),
	}
}

func semanticSourcesGrid(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	sources := metaMap(meta, "Sources")
	rows := make([]map[string]any, 0, len(sources))
	for _, name := range sortedMapKeys(sources) {
		source := asMap(sources[name])
		child := semanticAssetByName(parent.Key, "source", name, assets)
		rows = append(rows, map[string]any{
			"name":       name,
			"nameHref":   childHref(projectID, child),
			"connection": emptyDash(metaString(source, "Connection")),
			"format":     recordTableBadgeValue(metaString(source, "Format"), "accent"),
			"path":       emptyDash(firstNonEmpty(metaString(source, "Path"), metaString(source, "Object"))),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("180px")},
			{ID: "connection", Header: "Connection", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "format", Header: "Format", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("110px")},
			{ID: "path", Header: "Path / object", Kind: uisignals.Pointer("expression")},
		},
		Rows:     rows,
		Empty:    "No sources are defined for this semantic model.",
		MinWidth: uisignals.Pointer("820px"),
	}
}

func semanticDatasetsTable(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any, refresh AssetRefreshState) recordTable {
	datasets := metaMap(meta, "Datasets")
	datasetDetails := metaMap(meta, "DatasetDetails")
	metricCounts := semanticMetricCountsByDataset(metaMap(meta, "Metrics"))
	rows := make([]map[string]any, 0, len(datasets))
	lastRefreshed := emptyDash(refresh.LatestSuccessful.FinishedAt)
	refreshStatus := "not refreshed"
	if strings.TrimSpace(refresh.LatestSuccessful.Status) != "" {
		refreshStatus = refresh.LatestSuccessful.Status
	}
	for _, name := range sortedMapKeys(datasets) {
		dataset := asMap(datasets[name])
		details := asMap(datasetDetails[name])
		child := semanticAssetByName(parent.Key, "model_table", name, assets)
		rows = append(rows, map[string]any{
			"name":           name,
			"nameHref":       childHref(projectID, child),
			"model":          emptyDash(metaString(dataset, "Model")),
			"fields":         len(metaMap(details, "Dimensions")),
			"metrics":        metricCounts[name],
			"last_refreshed": lastRefreshed,
			"refresh_status": refreshStatusGridValue(refreshStatus),
			"description":    emptyDash(metaString(dataset, "Description")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("180px")},
			{ID: "model", Header: "Model", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "fields", Header: "Fields", Width: uisignals.Pointer("100px")},
			{ID: "metrics", Header: "Metrics", Width: uisignals.Pointer("110px")},
			{ID: "last_refreshed", Header: "Last refreshed", Width: uisignals.Pointer("180px")},
			{ID: "refresh_status", Header: "Refresh status", Kind: uisignals.Pointer("status"), Width: uisignals.Pointer("130px")},
			{ID: "description", Header: "Description"},
		},
		Rows:     rows,
		Empty:    "No datasets are defined for this semantic model.",
		MinWidth: uisignals.Pointer("1120px"),
	}
}

func semanticMetricCountsByDataset(metrics map[string]any) map[string]int {
	counts := map[string]int{}
	for _, name := range sortedMapKeys(metrics) {
		metric := asMap(metrics[name])
		dataset := metaString(metric, "Dataset")
		if dataset == "" {
			continue
		}
		counts[dataset]++
	}
	return counts
}

func semanticModelGraphSignal(meta map[string]any) *uisignals.SemanticModelGraphSignal {
	datasets := metaMap(meta, "Datasets")
	datasetDetails := metaMap(meta, "DatasetDetails")
	if len(datasets) == 0 {
		return nil
	}
	metrics := metaMap(meta, "Metrics")
	dimensions := metaMap(meta, "Dimensions")
	metricDatasets := semanticModelMetricDatasets(metrics)
	metricCounts := semanticMetricCountsByDataset(metrics)
	conformedCounts := semanticConformedDimensionCounts(dimensions)
	relationships := semanticModelGraphRelationships(meta, datasets)
	joinFields := semanticModelJoinFields(relationships)
	nodes := make([]uisignals.SemanticModelGraphNodeSignal, 0, len(datasets))
	for _, name := range semanticModelGraphDatasetNames(datasets, metricDatasets) {
		dataset := asMap(datasets[name])
		details := asMap(datasetDetails[name])
		badges := []string{}
		if containsString(metricDatasets, name) {
			badges = append(badges, "dataset")
		}
		if semanticModelTableIsDimension(name, relationships) {
			badges = append(badges, "dimension")
		}
		if metricCounts[name] > 0 {
			badges = append(badges, fmt.Sprintf("%d metrics", metricCounts[name]))
		}
		if conformedCounts[name] > 0 {
			badges = append(badges, fmt.Sprintf("%d conformed dimensions", conformedCounts[name]))
		}
		nodes = append(nodes, uisignals.SemanticModelGraphNodeSignal{
			ID:          name,
			Title:       name,
			Description: uisignals.Optional(metaString(dataset, "Description")),
			Entities:    uisignals.OptionalSlice(semanticModelGraphEntities(details)),
			Badges:      uisignals.OptionalSlice(badges),
			Fields:      semanticModelGraphFields(details, joinFields[name]),
			GrainEntity: uisignals.Optional(metaString(details, "GrainEntity")),
		})
	}
	return &uisignals.SemanticModelGraphSignal{
		Datasets: uisignals.OptionalSlice(metricDatasets),
		Nodes:    nodes,
		Edges:    relationships,
	}
}

func semanticModelGraphRelationships(meta map[string]any, datasets map[string]any) []uisignals.SemanticModelGraphEdgeSignal {
	raw := metaSlice(meta, "Relationships")
	edges := make([]uisignals.SemanticModelGraphEdgeSignal, 0, len(raw))
	for _, item := range raw {
		relationship := asMap(item)
		fromTable, fromFields := semanticCompiledRelationshipEndpointMeta(relationship, "From")
		toTable, toFields := semanticCompiledRelationshipEndpointMeta(relationship, "To")
		fromField, toField := strings.Join(fromFields, ", "), strings.Join(toFields, ", ")
		if fromTable == "" || fromField == "" || toTable == "" || toField == "" {
			continue
		}
		if _, ok := datasets[fromTable]; !ok {
			continue
		}
		if _, ok := datasets[toTable]; !ok {
			continue
		}
		id := metaString(relationship, "ID")
		if id == "" {
			id = fromTable + "_" + fromField + "_" + toTable + "_" + toField
		}
		cardinality := metaString(relationship, "Cardinality", "cardinality")
		edges = append(edges, uisignals.SemanticModelGraphEdgeSignal{
			ID:          id,
			Source:      fromTable,
			Target:      toTable,
			SourceField: fromField,
			TargetField: toField,
			Cardinality: cardinality,
			Label:       semanticModelGraphCardinalityLabel(cardinality),
		})
	}
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].Source != edges[j].Source {
			return edges[i].Source < edges[j].Source
		}
		if edges[i].Target != edges[j].Target {
			return edges[i].Target < edges[j].Target
		}
		return edges[i].ID < edges[j].ID
	})
	return edges
}

// semanticCompiledRelationshipEndpointMeta reads physical endpoint tuples from
// compiled relationships. Entity endpoints are resolved by the compiler, so
// FromFields/ToFields always contain the ordered physical field tuple rather
// than an entity name (which cannot address a graph handle).
func semanticCompiledRelationshipEndpointMeta(relationship map[string]any, prefix string) (string, []string) {
	return metaString(relationship, prefix+"Dataset"), metaStringSlice(relationship, prefix+"Fields")
}

func semanticModelJoinFields(edges []uisignals.SemanticModelGraphEdgeSignal) map[string]map[string][]string {
	joinFields := map[string]map[string][]string{}
	add := func(table, field, relationship string) {
		if joinFields[table] == nil {
			joinFields[table] = map[string][]string{}
		}
		joinFields[table][field] = append(joinFields[table][field], relationship)
	}
	for _, edge := range edges {
		addEndpointFields(add, edge.Source, edge.SourceField, edge.ID)
		addEndpointFields(add, edge.Target, edge.TargetField, edge.ID)
	}
	for _, fields := range joinFields {
		for field := range fields {
			sort.Strings(fields[field])
		}
	}
	return joinFields
}

func addEndpointFields(add func(string, string, string), table, fields, relationship string) {
	for _, field := range strings.Split(fields, ",") {
		field = strings.TrimSpace(field)
		if field != "" {
			add(table, field, relationship)
		}
	}
}

func semanticModelGraphDatasetNames(datasets map[string]any, metricDatasets []string) []string {
	names := sortedMapKeys(datasets)
	if len(metricDatasets) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, dataset := range metricDatasets {
		if _, ok := datasets[dataset]; ok {
			out = append(out, dataset)
		}
	}
	for _, name := range names {
		if !containsString(metricDatasets, name) {
			out = append(out, name)
		}
	}
	return out
}

func semanticModelMetricDatasets(metrics map[string]any) []string {
	seen := map[string]bool{}
	for _, metric := range metrics {
		if dataset := metaString(asMap(metric), "Dataset"); dataset != "" {
			seen[dataset] = true
		}
	}
	datasets := make([]string, 0, len(seen))
	for dataset := range seen {
		datasets = append(datasets, dataset)
	}
	sort.Strings(datasets)
	return datasets
}

func semanticConformedDimensionCounts(dimensions map[string]any) map[string]int {
	counts := map[string]int{}
	for _, dimension := range dimensions {
		for dataset := range metaMap(asMap(dimension), "Bindings") {
			counts[dataset]++
		}
	}
	return counts
}

func semanticModelTableIsDimension(table string, edges []uisignals.SemanticModelGraphEdgeSignal) bool {
	for _, edge := range edges {
		if edge.Target == table {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func semanticModelGraphFields(table map[string]any, joins map[string][]string) []uisignals.SemanticModelGraphFieldSignal {
	fields := metaMap(table, "Dimensions")
	columns := modelTableSchemaColumns(fields, metaMap(table, "Schema"))
	entityNames, grainFields := semanticModelGraphFieldIdentity(table)
	seen := map[string]struct{}{}
	out := make([]uisignals.SemanticModelGraphFieldSignal, 0, len(columns)+len(joins))
	for _, column := range columns {
		name := metaString(column, "Name", "name")
		if name == "" {
			continue
		}
		field := asMap(fields[name])
		out = append(out, semanticModelGraphField(name, field, column, entityNames[name], grainFields[name], joins[name]))
		seen[name] = struct{}{}
	}
	for _, name := range sortedMapKeysString(joins) {
		if _, ok := seen[name]; ok {
			continue
		}
		out = append(out, uisignals.SemanticModelGraphFieldSignal{
			Name:          name,
			Label:         uisignals.Optional(labelFromKey(name)),
			Entities:      uisignals.OptionalSlice(entityNames[name]),
			Grain:         uisignals.Optional(grainFields[name]),
			Join:          uisignals.Pointer(true),
			Relationships: uisignals.OptionalSlice(joins[name]),
		})
	}
	return out
}

func semanticModelGraphField(name string, field, column map[string]any, entities []string, grain bool, relationships []string) uisignals.SemanticModelGraphFieldSignal {
	return uisignals.SemanticModelGraphFieldSignal{
		Name:          name,
		Label:         uisignals.Optional(firstNonEmpty(metaString(field, "Label"), labelFromKey(name))),
		Type:          uisignals.Optional(firstNonEmpty(metaString(column, "PhysicalType", "physicalType"), metaString(column, "Type", "type"))),
		Entities:      uisignals.OptionalSlice(entities),
		Grain:         uisignals.Optional(grain),
		Join:          uisignals.Optional(len(relationships) > 0),
		Relationships: uisignals.OptionalSlice(relationships),
	}
}

func semanticModelGraphEntities(table map[string]any) []uisignals.SemanticModelGraphEntitySignal {
	entities := metaMap(table, "Entities")
	grainEntity := metaString(table, "GrainEntity")
	out := make([]uisignals.SemanticModelGraphEntitySignal, 0, len(entities))
	for _, name := range sortedMapKeys(entities) {
		entity := asMap(entities[name])
		out = append(out, uisignals.SemanticModelGraphEntitySignal{
			Name:   name,
			Type:   metaString(entity, "Type"),
			Fields: metaStringSlice(entity, "Fields"),
			Grain:  uisignals.Optional(name == grainEntity),
		})
	}
	return out
}

func semanticModelGraphFieldIdentity(table map[string]any) (map[string][]string, map[string]bool) {
	entityNames := map[string][]string{}
	grainFields := map[string]bool{}
	grainEntity := metaString(table, "GrainEntity")
	for _, entityName := range sortedMapKeys(metaMap(table, "Entities")) {
		entity := asMap(metaMap(table, "Entities")[entityName])
		for _, field := range metaStringSlice(entity, "Fields") {
			entityNames[field] = append(entityNames[field], entityName)
			if entityName == grainEntity {
				grainFields[field] = true
			}
		}
	}
	return entityNames, grainFields
}

func semanticModelGraphCardinalityLabel(cardinality string) string {
	switch strings.ToLower(strings.TrimSpace(cardinality)) {
	case "many_to_one":
		return "*:1"
	case "one_to_one":
		return "1:1"
	default:
		return cardinality
	}
}

func sortedMapKeysString(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func modelTableDetailModel(model *assetDetailModel, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, refresh AssetRefreshState) {
	modelKey, tableName := modelTableKeyParts(asset)
	fields := modelTableFields(asset.Payload)
	sources := modelTableSourceNames(asset.Payload)
	mode := "Unspecified"
	if modelTableSQL(asset.Payload) != "" {
		mode = "Definition"
	} else if modelTableSourceNames(asset.Payload) != nil {
		mode = "Direct source"
	}
	entities := metaMap(asset.Payload, "Entities", "entities")
	grainEntity := metaString(asset.Payload, "GrainEntity", "grainEntity")
	model.Overview = append(model.Overview,
		definitionFact{Label: "Grain entity", Value: grainEntity, Code: true},
		definitionFact{Label: "Entities", Value: fmt.Sprint(len(entities))},
		definitionFact{Label: "Fields", Value: fmt.Sprint(len(fields))},
		definitionFact{Label: "Input sources", Value: fmt.Sprint(len(sources))},
		definitionFact{Label: "Mode", Value: mode},
	)
	model.Overview = append(model.Overview, refreshOverviewFacts(refresh)...)
	model.Sections = append(model.Sections,
		assetDetailSection{Title: fmt.Sprintf("Entities (%d)", len(entities)), Signal: "assetDetailsModelTableEntitiesTable", Table: modelTableEntitiesGrid(asset.Payload)},
		assetDetailSection{Title: fmt.Sprintf("Fields (%d)", len(fields)), Signal: "assetDetailsModelTableFieldsTable", Table: modelTableFieldsGrid(project.ID, modelKey, tableName, asset.Payload, assets)},
	)
	if sql := modelTableSQL(asset.Payload); sql != "" {
		model.Sections = append(model.Sections, assetDetailSection{Title: "SQL", Lang: "sql", Code: sql})
	}
}

func modelTableKeyParts(asset projectview.DevelopAssetView) (string, string) {
	parts := strings.SplitN(asset.Key, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", asset.Key
}

func modelTableFields(meta map[string]any) map[string]any {
	return metaMap(meta, "Dimensions", "dimensions", "Fields", "fields")
}

func sourceDetailModel(model *assetDetailModel, asset projectview.DevelopAssetView) {
	fields := metaMap(asset.Payload, "Fields", "fields")
	schema := metaMap(asset.Payload, "Schema", "schema")
	columns := modelTableSchemaColumns(fields, schema)
	model.Overview = append(model.Overview, sourceFacts(asset)...)
	model.Overview = append(model.Overview, definitionFact{Label: "Fields", Value: fmt.Sprint(len(columns))})
	model.Sections = append(model.Sections,
		assetDetailSection{Title: fmt.Sprintf("Fields (%d)", len(columns)), Signal: "assetDetailsSourceFieldsTable", Table: sourceFieldsGrid(fields, schema)},
	)
}

func modelTableSourceNames(meta map[string]any) []string {
	definition := metaMap(meta, "Definition", "definition")
	if source := metaString(definition, "Source", "source"); source != "" {
		return []string{source}
	}
	for _, value := range []any{
		metaValue(meta, "SourceDependencies", "source_dependencies"),
		metaValue(meta, "Sources", "sources"),
	} {
		sources := stringSlice(value)
		if len(sources) > 0 {
			sort.Strings(sources)
			return sources
		}
	}
	return nil
}

func modelTableSQL(meta map[string]any) string {
	return metaString(metaMap(meta, "Definition", "definition"), "SQL", "sql")
}

func modelTableFieldsGrid(projectID, modelKey, tableName string, table map[string]any, assets []projectview.DevelopAssetView) recordTable {
	fields := modelTableFields(table)
	schema := metaMap(table, "Schema", "schema")
	schemaColumns := modelTableSchemaColumns(fields, schema)
	entityNames, grainFields := semanticModelGraphFieldIdentity(table)
	rows := make([]map[string]any, 0, len(schemaColumns))
	for _, column := range schemaColumns {
		name := metaString(column, "Name", "name")
		field := asMap(fields[name])
		child := assetByTypeKey("field", modelKey+"."+tableName+"."+name, assets)
		rows = append(rows, map[string]any{
			"name":          name,
			"nameHref":      childHref(projectID, child),
			"label":         firstNonEmpty(metaString(field, "Label", "label"), labelFromKey(name)),
			"physical_type": recordTableBadgeValue(metaString(column, "PhysicalType", "physicalType"), "muted"),
			"nullable":      nullableLabel(column, "Nullable", "nullable"),
			"entities":      emptyDash(strings.Join(entityNames[name], ", ")),
			"grain":         boolLabel(grainFields[name]),
			"description":   emptyDash(metaString(field, "Description", "description")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("170px")},
			{ID: "label", Header: "Label", Width: uisignals.Pointer("180px")},
			{ID: "physical_type", Header: "Physical type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("140px")},
			{ID: "nullable", Header: "Nullable", Width: uisignals.Pointer("100px")},
			{ID: "entities", Header: "Entities", Width: uisignals.Pointer("180px")},
			{ID: "grain", Header: "Grain", Width: uisignals.Pointer("90px")},
			{ID: "description", Header: "Description"},
		},
		Rows:     rows,
		Empty:    "No schema is available for this model table.",
		MinWidth: uisignals.Pointer("900px"),
	}
}

func modelTableEntitiesGrid(table map[string]any) recordTable {
	entities := semanticModelGraphEntities(table)
	rows := make([]map[string]any, 0, len(entities))
	for _, entity := range entities {
		rows = append(rows, map[string]any{
			"name":   entity.Name,
			"type":   recordTableBadgeValue(entity.Type, "muted"),
			"fields": strings.Join(entity.Fields, ", "),
			"grain":  boolLabel(entity.Grain != nil && *entity.Grain),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("180px")},
			{ID: "type", Header: "Type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "fields", Header: "Ordered key fields"},
			{ID: "grain", Header: "Grain", Width: uisignals.Pointer("90px")},
		},
		Rows:     rows,
		Empty:    "No entities are defined for this model.",
		MinWidth: uisignals.Pointer("720px"),
	}
}

func sourceFieldsGrid(fields, schema map[string]any) recordTable {
	schemaColumns := modelTableSchemaColumns(fields, schema)
	rows := make([]map[string]any, 0, len(schemaColumns))
	for _, column := range schemaColumns {
		name := metaString(column, "Name", "name")
		field := asMap(fields[name])
		rows = append(rows, map[string]any{
			"name":          name,
			"description":   emptyDash(metaString(field, "Description", "description")),
			"physical_type": recordTableBadgeValue(metaString(column, "PhysicalType", "physicalType"), "muted"),
			"nullable":      nullableLabel(column, "Nullable", "nullable"),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("170px")},
			{ID: "description", Header: "Description"},
			{ID: "physical_type", Header: "Physical type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("140px")},
			{ID: "nullable", Header: "Nullable", Width: uisignals.Pointer("100px")},
		},
		Rows:     rows,
		Empty:    "No schema is available for this source.",
		MinWidth: uisignals.Pointer("900px"),
	}
}

func modelTableSchemaColumns(fields map[string]any, schema map[string]any) []map[string]any {
	if schema != nil {
		if raw := metaSlice(schema, "Columns", "columns"); len(raw) > 0 {
			columns := make([]map[string]any, 0, len(raw))
			for _, item := range raw {
				columns = append(columns, asMap(item))
			}
			sort.Slice(columns, func(i, j int) bool {
				return metaInt(columns[i], "Ordinal", "ordinal") < metaInt(columns[j], "Ordinal", "ordinal")
			})
			return columns
		}
	}
	columns := make([]map[string]any, 0, len(fields))
	for _, name := range sortedMapKeys(fields) {
		columns = append(columns, map[string]any{"name": name})
	}
	return columns
}

func semanticFieldsGrid(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	datasets := metaMap(meta, "Datasets")
	datasetDetails := metaMap(meta, "DatasetDetails")
	rows := []map[string]any{}
	for _, datasetName := range sortedMapKeys(datasets) {
		dataset := asMap(datasetDetails[datasetName])
		fields := metaMap(dataset, "Dimensions")
		for _, fieldName := range sortedMapKeys(fields) {
			field := asMap(fields[fieldName])
			key := parent.Key + "." + datasetName + "." + fieldName
			child := assetByTypeKey("field", key, assets)
			rows = append(rows, map[string]any{
				"name":       fieldName,
				"nameHref":   childHref(projectID, child),
				"dataset":    datasetName,
				"expression": datasetName + "." + fieldName,
				"type":       recordTableBadgeValue(metaString(field, "Type"), "muted"),
				"filter":     emptyDash(metaString(field, "Where")),
			})
		}
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("170px")},
			{ID: "dataset", Header: "Dataset", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "expression", Header: "Expression", Kind: uisignals.Pointer("expression"), Width: uisignals.Pointer("260px")},
			{ID: "type", Header: "Type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("110px")},
			{ID: "filter", Header: "Filter", Kind: uisignals.Pointer("expression"), Width: uisignals.Pointer("220px")},
		},
		Rows:     rows,
		Empty:    "No fields are defined for this semantic model.",
		MinWidth: uisignals.Pointer("1100px"),
	}
}

func semanticMetricsTable(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	metrics := metaMap(meta, "Metrics", "metrics")
	rows := make([]map[string]any, 0, len(metrics))
	for _, name := range sortedMapKeys(metrics) {
		metric := asMap(metrics[name])
		input := metaMap(metric, "Input", "input")
		child := childAssetByName(parent.ID, "metric", name, assets)
		rows = append(rows, map[string]any{
			"name":        name,
			"nameHref":    childHref(projectID, child),
			"dataset":     emptyDash(metaString(metric, "Dataset", "dataset")),
			"aggregation": recordTableBadgeValue(firstNonEmpty(metaString(metric, "Aggregation", "aggregation"), metaString(metric, "Type", "type")), "muted"),
			"input":       firstNonEmpty(metaString(input, "Field", "field"), metaString(metric, "Expression", "expression")),
			"format":      recordTableBadgeValue(metaString(metric, "Format", "format"), "accent"),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("160px")},
			{ID: "dataset", Header: "Dataset", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("140px")},
			{ID: "aggregation", Header: "Aggregation", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "input", Header: "Input", Kind: uisignals.Pointer("expression")},
			{ID: "format", Header: "Format", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("100px")},
		},
		Rows:     rows,
		Empty:    "No metrics are defined for this semantic model.",
		MinWidth: uisignals.Pointer("900px"),
	}
}

func semanticRelationshipsTable(projectID string, parent projectview.DevelopAssetView, assets []projectview.DevelopAssetView, meta map[string]any) recordTable {
	relationships := metaSlice(meta, "Relationships")
	rows := make([]map[string]any, 0, len(relationships))
	for _, item := range relationships {
		relationship := asMap(item)
		id := metaString(relationship, "ID", "id")
		child := semanticAssetByName(parent.Key, "relationship", id, assets)
		fromTable, fromFields := semanticCompiledRelationshipEndpointMeta(relationship, "From")
		toTable, toFields := semanticCompiledRelationshipEndpointMeta(relationship, "To")
		cardinality := metaString(relationship, "Cardinality", "cardinality")
		fromField, toField := strings.Join(fromFields, ", "), strings.Join(toFields, ", ")
		rows = append(rows, map[string]any{
			"id":          id,
			"idHref":      childHref(projectID, child),
			"from_table":  emptyDash(fromTable),
			"from_field":  emptyDash(fromField),
			"to_table":    emptyDash(toTable),
			"to_field":    emptyDash(toField),
			"cardinality": recordTableBadgeValue(cardinality, "muted"),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "id", Header: "ID", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("idHref"), Width: uisignals.Pointer("180px")},
			{ID: "from_table", Header: "From dataset", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("140px")},
			{ID: "from_field", Header: "From field", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("160px")},
			{ID: "to_table", Header: "To dataset", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("140px")},
			{ID: "to_field", Header: "To field", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("160px")},
			{ID: "cardinality", Header: "Cardinality", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("140px")},
		},
		Rows:     rows,
		Empty:    "No relationships are defined for this semantic model.",
		MinWidth: uisignals.Pointer("920px"),
	}
}

func splitSemanticFieldRef(ref string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(ref), ".", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(ref), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func dashboardDetailModel(model *assetDetailModel, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView) {
	pages := childrenByType(asset.ID, "page", assets)
	filters := childrenByType(asset.ID, "filter", assets)
	visuals := childrenByType(asset.ID, "visual", assets)
	model.Overview = append(model.Overview,
		definitionFact{Label: "Semantic model", Value: metaString(asset.Payload, "SemanticModel", "semantic_model")},
		definitionFact{Label: "Tags", Value: strings.Join(stringSlice(metaValue(asset.Payload, "Tags", "tags")), ", ")},
	)
	model.Sections = append(model.Sections,
		assetDetailSection{Title: fmt.Sprintf("Pages (%d)", len(pages)), Signal: "assetDetailsPagesTable", Table: dashboardPagesTable(asset, pages)},
		assetDetailSection{Title: fmt.Sprintf("Filters (%d)", len(filters)), Signal: "assetDetailsFiltersTable", Table: dashboardFiltersTable(asset, filters)},
		assetDetailSection{Title: fmt.Sprintf("Visuals (%d)", len(visuals)), Signal: "assetDetailsVisualsTable", Table: dashboardVisualsTable(asset, visuals)},
	)
}

func dashboardPagesTable(parent projectview.DevelopAssetView, pages []projectview.DevelopAssetView) recordTable {
	rows := make([]map[string]any, 0, len(pages))
	for _, page := range pages {
		key := assetChildName(parent, page)
		rows = append(rows, map[string]any{
			"page":        assetTitle(page),
			"pageHref":    assetnav.ProjectAssetSectionHref(page.ID, "details"),
			"key":         key,
			"description": emptyDash(page.Description),
			"runtime":     "Open",
			"runtimeHref": "/dashboards/" + parent.Key + "/pages/" + key,
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "page", Header: "Page", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("pageHref"), Width: uisignals.Pointer("220px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("190px")},
			{ID: "description", Header: "Description"},
			{ID: "runtime", Header: "Runtime", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("runtimeHref"), Width: uisignals.Pointer("110px")},
		},
		Rows:     rows,
		Empty:    "No pages are defined for this dashboard.",
		MinWidth: uisignals.Pointer("860px"),
	}
}

func dashboardFiltersTable(parent projectview.DevelopAssetView, filters []projectview.DevelopAssetView) recordTable {
	sortAssetChildren(parent, filters)
	rows := make([]map[string]any, 0, len(filters))
	for _, filter := range filters {
		rows = append(rows, map[string]any{
			"filter":     assetTitle(filter),
			"filterHref": assetnav.ProjectAssetSectionHref(filter.ID, "details"),
			"key":        assetChildName(parent, filter),
			"field":      emptyDash(metaString(filter.Payload, "Dimension", "dimension", "Field", "field")),
			"type":       emptyDash(metaString(filter.Payload, "Type", "type", "Kind", "kind")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "filter", Header: "Filter", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("filterHref"), Width: uisignals.Pointer("190px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("160px")},
			{ID: "field", Header: "Field", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("120px")},
		},
		Rows:     rows,
		Empty:    "No filters are defined for this dashboard.",
		MinWidth: uisignals.Pointer("820px"),
	}
}

func dashboardVisualsTable(parent projectview.DevelopAssetView, visuals []projectview.DevelopAssetView) recordTable {
	sortAssetChildren(parent, visuals)
	rows := make([]map[string]any, 0, len(visuals))
	for _, visual := range visuals {
		query := metaMap(visual.Payload, "Query", "query")
		rows = append(rows, map[string]any{
			"visual":     assetTitle(visual),
			"visualHref": assetnav.ProjectAssetSectionHref(visual.ID, "details"),
			"key":        assetChildName(parent, visual),
			"type":       emptyDash(firstNonEmpty(metaString(visual.Payload, "Type", "type"), metaString(visual.Payload, "Shape", "shape"))),
			"metrics":    emptyDash(strings.Join(stringSlice(metaValue(query, "Metrics", "metrics")), ", ")),
			"dimensions": emptyDash(strings.Join(stringSlice(metaValue(query, "Dimensions", "dimensions")), ", ")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "visual", Header: "Visual", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("visualHref"), Width: uisignals.Pointer("230px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("180px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("120px")},
			{ID: "metrics", Header: "Metrics", Kind: uisignals.Pointer("expression"), Width: uisignals.Pointer("220px")},
			{ID: "dimensions", Header: "Dimensions", Kind: uisignals.Pointer("expression")},
		},
		Rows:     rows,
		Empty:    "No visuals are defined for this dashboard.",
		MinWidth: uisignals.Pointer("1040px"),
	}
}

func connectionDetailModel(model *assetDetailModel, project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) {
	sources := sourcesUsingConnection(asset.ID, assets, edges)
	model.Overview = append(model.Overview, connectionFacts(asset)...)
	model.Overview = append(model.Overview, definitionFact{Label: "Sources", Value: fmt.Sprint(len(sources))})
	model.Sections = append(model.Sections,
		assetDetailSection{
			Title:  fmt.Sprintf("Sources (%d)", len(sources)),
			Signal: "assetDetailsConnectionSourcesTable",
			Table:  connectionSourcesGrid(project.ID, sources, edges),
		},
	)
}

func connectionSourcesGrid(projectID string, sources []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) recordTable {
	sort.SliceStable(sources, func(i, j int) bool {
		return strings.ToLower(assetTitle(sources[i])) < strings.ToLower(assetTitle(sources[j]))
	})
	rows := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		fields := metaMap(source.Payload, "Fields", "fields")
		schema := metaMap(source.Payload, "Schema", "schema")
		rows = append(rows, map[string]any{
			"source":     assetTitle(source),
			"sourceHref": assetnav.CanonicalAssetSectionHref(source, "details"),
			"format":     emptyDash(metaString(source.Payload, "Format", "format")),
			"path":       emptyDash(metaString(source.Payload, "Path", "path")),
			"fields":     len(modelTableSchemaColumns(fields, schema)),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "source", Header: "Source", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("sourceHref"), Width: uisignals.Pointer("240px")},
			{ID: "format", Header: "Format", Width: uisignals.Pointer("140px")},
			{ID: "path", Header: "Path", Kind: uisignals.Pointer("code")},
			{ID: "fields", Header: "Fields", Align: uisignals.Pointer("right"), Width: uisignals.Pointer("100px")},
		},
		Rows:     rows,
		Empty:    "No sources use this connection.",
		MinWidth: uisignals.Pointer("760px"),
	}
}

func sourcesUsingConnection(connectionID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) []projectview.DevelopAssetView {
	byID := assetsByID(assets)
	sources := []projectview.DevelopAssetView{}
	seen := map[string]struct{}{}
	for _, edge := range edges {
		if edge.Type != "uses_connection" || edge.ToAssetID != connectionID {
			continue
		}
		source, ok := byID[edge.FromAssetID]
		if !ok || source.Type != "source" {
			continue
		}
		if _, ok := seen[source.ID]; ok {
			continue
		}
		seen[source.ID] = struct{}{}
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool {
		return assetTitle(sources[i]) < assetTitle(sources[j])
	})
	return sources
}

func connectionFacts(asset projectview.DevelopAssetView) []definitionFact {
	options := compactJSON(metaValue(asset.Payload, "Options", "options"))
	if options == "" {
		options = compactJSON(metaValue(asset.Payload, "Defaults", "defaults"))
	}
	return []definitionFact{
		{Label: "Kind", Value: metaString(asset.Payload, "Kind", "kind")},
		{Label: "Scope", Value: metaString(asset.Payload, "Scope", "scope")},
		{Label: "Root", Value: metaString(asset.Payload, "Root", "root")},
		{Label: "Path", Value: metaString(asset.Payload, "Path", "path")},
		{Label: "Credentials", Value: boolLabel(metaBool(asset.Payload, "credentials_configured"))},
		{Label: "Options", Value: options},
	}
}

func sourceFacts(asset projectview.DevelopAssetView) []definitionFact {
	return []definitionFact{
		{Label: "Connection", Value: metaString(asset.Payload, "Connection", "connection")},
		{Label: "Format", Value: metaString(asset.Payload, "Format", "format")},
		{Label: "Path", Value: metaString(asset.Payload, "Path", "path")},
		{Label: "Object", Value: metaString(asset.Payload, "Object", "object")},
		{Label: "Options", Value: compactJSON(metaValue(asset.Payload, "Options", "options"))},
	}
}

func metricLeafFacts(asset projectview.DevelopAssetView) []definitionFact {
	facts := []definitionFact{}
	for _, key := range []string{"Expression", "Where", "Unit", "Format"} {
		if value := metaString(asset.Payload, key); strings.TrimSpace(value) != "" {
			facts = append(facts, definitionFact{Label: labelFromKey(key), Value: value, Code: strings.Contains(strings.ToLower(key), "expr") || strings.EqualFold(key, "expression")})
		}
	}
	return facts
}

type definitionFact struct {
	Label string
	Value string
	Code  bool
	Wide  bool
}

func childAssetGrid(projectID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, empty string) recordTable {
	sort.Slice(assets, func(i, j int) bool {
		return assetTitle(assets[i]) < assetTitle(assets[j])
	})
	rows := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		rows = append(rows, map[string]any{
			"name":        assetTitle(asset),
			"nameHref":    assetnav.CanonicalAssetSectionHref(asset, "details"),
			"key":         asset.Key,
			"type":        assetTypeLabel(asset.Type),
			"description": emptyDash(asset.Description),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("nameHref"), Width: uisignals.Pointer("220px")},
			{ID: "key", Header: "Key", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("150px")},
			{ID: "description", Header: "Description"},
		},
		Rows:     rows,
		Empty:    empty,
		MinWidth: uisignals.Pointer("860px"),
	}
}

func childDependencyGrid(projectID, assetID string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) recordTable {
	byID := assetsByID(assets)
	rows := []map[string]any{}
	for _, edge := range edges {
		if edge.FromAssetID != assetID && edge.ToAssetID != assetID {
			continue
		}
		peerID := edge.ToAssetID
		direction := recordTableBadge{Label: "Outgoing", Tone: uisignals.Pointer("accent")}
		if edge.ToAssetID == assetID {
			peerID = edge.FromAssetID
			direction = recordTableBadge{Label: "Incoming", Tone: uisignals.Pointer("muted")}
		}
		peer, ok := byID[peerID]
		if !ok {
			continue
		}
		rows = append(rows, map[string]any{
			"direction": direction,
			"relation":  labelFromKey(edge.Type),
			"asset":     assetTitle(peer),
			"assetHref": assetnav.CanonicalAssetSectionHref(peer, "details"),
			"type":      assetTypeLabel(peer.Type),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return fmt.Sprint(rows[i]["relation"], rows[i]["asset"]) < fmt.Sprint(rows[j]["relation"], rows[j]["asset"])
	})
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "direction", Header: "Direction", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "relation", Header: "Relationship", Width: uisignals.Pointer("180px")},
			{ID: "asset", Header: "Asset", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("assetHref"), Width: uisignals.Pointer("240px")},
			{ID: "type", Header: "Type", Width: uisignals.Pointer("140px")},
		},
		Rows:     rows,
		Empty:    "No direct dependencies for this asset.",
		MinWidth: uisignals.Pointer("720px"),
	}
}

func metaFacts(meta map[string]any) []definitionFact {
	keys := make([]string, 0, len(meta))
	for key := range meta {
		if assetDefinitionDetailKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	facts := make([]definitionFact, 0, len(keys))
	for _, key := range keys {
		facts = append(facts, definitionFact{Label: labelFromKey(key), Value: assetDefinitionValue(meta[key]), Code: looksLikeCodeKey(key)})
	}
	return facts
}

func assetDefinitionDetailKey(key string) bool {
	switch strings.ToLower(key) {
	case "description", "id", "name", "title", "auth":
		return true
	default:
		return false
	}
}

func assetDefinitionValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		if data, err := json.MarshalIndent(typed, "", "  "); err == nil {
			return string(data)
		}
		return fmt.Sprint(value)
	}
}

func looksLikeCodeKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "expr") || strings.Contains(key, "sql")
}

func asMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func metaValue(meta map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := meta[key]; ok {
			return value
		}
	}
	return nil
}

func metaMap(meta map[string]any, keys ...string) map[string]any {
	return asMap(metaValue(meta, keys...))
}

func metaSlice(meta map[string]any, keys ...string) []any {
	if typed, ok := metaValue(meta, keys...).([]any); ok {
		return typed
	}
	return nil
}

func metaStringSlice(meta map[string]any, keys ...string) []string {
	value := metaValue(meta, keys...)
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, strings.TrimSpace(value))
			}
		}
		return out
	default:
		return nil
	}
}

func metaString(meta map[string]any, keys ...string) string {
	value := metaValue(meta, keys...)
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%g", typed)
	default:
		return compactJSON(typed)
	}
}

func metaBool(meta map[string]any, keys ...string) bool {
	switch typed := metaValue(meta, keys...).(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
	default:
		return false
	}
}

func metaInt(meta map[string]any, keys ...string) int {
	switch typed := metaValue(meta, keys...).(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func compactJSON(value any) string {
	if value == nil {
		return ""
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	text := string(bytes)
	if text == "null" || text == "{}" || text == "[]" {
		return ""
	}
	return text
}

func boolLabel(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}

func nullableLabel(meta map[string]any, keys ...string) string {
	value := metaValue(meta, keys...)
	if value == nil {
		return "-"
	}
	switch typed := value.(type) {
	case bool:
		return boolLabel(typed)
	case string:
		if strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes") {
			return "Yes"
		}
		if strings.EqualFold(typed, "false") || strings.EqualFold(typed, "no") {
			return "No"
		}
	}
	return "-"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func childAssetByName(parentID, typ, name string, assets []projectview.DevelopAssetView) projectview.DevelopAssetView {
	for _, asset := range assets {
		if asset.ParentID != parentID || asset.Type != typ {
			continue
		}
		if asset.Title == name || asset.Key == name || strings.HasSuffix(asset.Key, "."+name) {
			return asset
		}
	}
	return projectview.DevelopAssetView{}
}

func semanticAssetByName(modelKey, typ, name string, assets []projectview.DevelopAssetView) projectview.DevelopAssetView {
	key := modelKey + "." + name
	if asset := assetByTypeKey(typ, key, assets); asset.ID != "" {
		return asset
	}
	for _, asset := range assets {
		if asset.Type != typ {
			continue
		}
		if asset.Title == name || asset.Key == name || strings.HasSuffix(asset.Key, "."+name) {
			return asset
		}
	}
	return projectview.DevelopAssetView{}
}

func assetByTypeKey(typ, key string, assets []projectview.DevelopAssetView) projectview.DevelopAssetView {
	for _, asset := range assets {
		if asset.Type == typ && asset.Key == key {
			return asset
		}
	}
	return projectview.DevelopAssetView{}
}

func childrenByType(parentID, typ string, assets []projectview.DevelopAssetView) []projectview.DevelopAssetView {
	out := []projectview.DevelopAssetView{}
	for _, asset := range assets {
		if asset.ParentID == parentID && asset.Type == typ {
			out = append(out, asset)
		}
	}
	return out
}

func metricChildName(parent, child projectview.DevelopAssetView) string {
	return assetChildName(parent, child)
}

func assetChildName(parent, child projectview.DevelopAssetView) string {
	prefix := parent.Key + "."
	if strings.HasPrefix(child.Key, prefix) {
		return strings.TrimPrefix(child.Key, prefix)
	}
	if child.Key != "" {
		return child.Key
	}
	return assetTitle(child)
}

func sortAssetChildren(parent projectview.DevelopAssetView, children []projectview.DevelopAssetView) {
	sort.Slice(children, func(i, j int) bool {
		left := assetChildName(parent, children[i])
		right := assetChildName(parent, children[j])
		if left == right {
			return assetTitle(children[i]) < assetTitle(children[j])
		}
		return left < right
	})
}

func childHref(projectID string, asset projectview.DevelopAssetView) string {
	if asset.ID == "" {
		return ""
	}
	return assetnav.CanonicalAssetSectionHref(asset, "details")
}

func assetsByID(assets []projectview.DevelopAssetView) map[string]projectview.DevelopAssetView {
	byID := map[string]projectview.DevelopAssetView{}
	for _, asset := range assets {
		byID[asset.ID] = asset
	}
	return byID
}

func dependentAssetNames(assetID, edgeType string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) []string {
	byID := assetsByID(assets)
	names := []string{}
	for _, edge := range edges {
		if edge.FromAssetID != assetID || edge.Type != edgeType {
			continue
		}
		if asset, ok := byID[edge.ToAssetID]; ok {
			names = append(names, assetTitle(asset))
		}
	}
	sort.Strings(names)
	return names
}

func assetTitle(asset projectview.DevelopAssetView) string {
	return displayLabel(asset.Title, asset.Key)
}

func assetTypeLabel(typ string) string {
	switch typ {
	case "semantic_model":
		return "Semantic model"
	case "model_table":
		return "Model table"
	case "page_item":
		return "Page item"
	case "refresh_pipeline":
		return "Refresh pipeline"
	default:
		return strings.Title(strings.ReplaceAll(typ, "_", " "))
	}
}

func labelFromKey(key string) string {
	switch key {
	case "reads_source":
		return "Reads source"
	case "uses_connection":
		return "Uses connection"
	case "uses_field":
		return "Uses field"
	case "filters_field":
		return "Filters field"
	case "uses_filter":
		return "Uses filter"
	case "uses_model_table":
		return "Uses model table"
	case "uses_metric":
		return "Uses metric"
	case "uses_semantic_model":
		return "Uses semantic model"
	case "uses_table":
		return "Uses table"
	case "uses_visual":
		return "Uses visual"
	case "refreshes_semantic_model":
		return "Refreshes semantic model"
	}
	return strings.Title(strings.ReplaceAll(key, "_", " "))
}
