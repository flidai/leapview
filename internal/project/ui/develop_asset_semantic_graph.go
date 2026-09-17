package ui

import (
	"fmt"
	"sort"
	"strings"

	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

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
		if semanticDatasetIsDimension(name, relationships) {
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

func semanticDatasetIsDimension(table string, edges []uisignals.SemanticModelGraphEdgeSignal) bool {
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
	columns := modelSchemaColumns(fields, metaMap(table, "Schema"))
	entityNames, grainFields := semanticModelGraphFieldIdentity(table)
	seen := map[string]struct{}{}
	// Do not add payload-derived collection lengths for a capacity hint; the
	// sum can overflow before make observes it.
	out := make([]uisignals.SemanticModelGraphFieldSignal, 0)
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
