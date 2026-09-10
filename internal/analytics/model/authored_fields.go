package model

// seedDeferredDiscoveredFields gives authored semantic references provisional
// field entries when a Model keeps a partial field overlay. The entries make
// graph shape validation possible before materialization; schema discovery
// replaces them with the definition's actual output and validates the graph
// again before activation.
func (m *Model) seedDeferredDiscoveredFields() {
	add := func(tableName, field string) {
		table, ok := m.Tables[tableName]
		if !ok || table.AuthoredFields == nil || len(table.Schema.Columns) > 0 || field == "" {
			return
		}
		if table.Dimensions == nil {
			table.Dimensions = map[string]MetricDimension{}
		}
		if _, exists := table.Dimensions[field]; !exists {
			table.Dimensions[field] = MetricDimension{}
		}
		m.Tables[tableName] = table
	}
	addRef := func(ref string) {
		tableName, field, err := splitSemanticField(ref)
		if err == nil {
			add(tableName, field)
		}
	}
	for tableName, table := range m.Tables {
		if table.AuthoredFields == nil || len(table.Schema.Columns) > 0 {
			continue
		}
		for _, entity := range table.Entities {
			for _, field := range entity.Fields {
				add(tableName, field)
			}
		}
		for _, check := range table.Checks {
			add(tableName, check.Field)
			for _, field := range check.Fields {
				add(tableName, field)
			}
			addRef(check.To)
		}
	}
	for _, relationship := range m.Relationships {
		for _, field := range relationship.FromFields {
			add(relationship.FromDataset, field)
		}
		for _, field := range relationship.ToFields {
			add(relationship.ToDataset, field)
		}
	}
	for _, dimension := range m.Dimensions {
		for _, binding := range dimension.Bindings {
			addRef(binding.Field)
		}
	}
	for _, metric := range m.Metrics {
		if metric.Input != nil {
			addRef(metric.Input.Field)
		}
	}
	for _, filter := range m.Filters {
		seedDeferredFilterFields(filter, addRef)
	}
}

func seedDeferredFilterFields(filter SemanticFilterSpec, add func(string)) {
	add(filter.Field)
	for _, child := range filter.All {
		seedDeferredFilterFields(child, add)
	}
	for _, child := range filter.Any {
		seedDeferredFilterFields(child, add)
	}
	if filter.Not != nil {
		seedDeferredFilterFields(*filter.Not, add)
	}
}
