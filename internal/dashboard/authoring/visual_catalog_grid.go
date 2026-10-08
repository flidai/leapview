package authoring

import (
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// Field options are derived from the current query, rather than accepting an
// arbitrary presentation path from a client. Each path stores its alias as
// one segment even when an authored name contains a dot.
func gridCellContentFormatSpecs(visual document.DashboardVisual) []visualFormatSpec {
	presentation, ok := visual.Presentation.Value.(*document.TableDashboardPresentation)
	if !ok || presentation == nil {
		return nil
	}
	fields := gridCellContentDeliveredFields(visual)
	collapsed := gridCellContentCollapsedFields(presentation.Hierarchy)
	metrics := gridCellContentMetricFields(visual)
	result := make([]visualFormatSpec, 0, len(fields))
	for _, field := range fields {
		if collapsed[field] || metrics[field] {
			continue
		}
		section := "Column · " + field
		key := "cellContent." + field + "."
		path := []string{"cellContent", field}
		display := "text"
		if presentation.CellContent != nil {
			if current, exists := (*presentation.CellContent)[field]; exists {
				switch value := current.Value.(type) {
				case *visualizationir.TableImageCellContent:
					if value != nil {
						display = "image_" + string(value.Display)
					}
				case *visualizationir.TableLinkCellContent:
					if value != nil {
						display = "link"
					}
				}
			}
		}
		displaySpec := formatSpec(key+"displayAs", "Display as", section, "select", path, "text", false)
		displaySpec.cellDisplay = true
		displaySpec.choices = []VisualFormatChoice{
			{Value: "text", Label: "Text"},
			{Value: "image_inline", Label: "Inline image"},
			{Value: "image_tooltip", Label: "Image on hover"},
			{Value: "link", Label: "Hyperlink"},
		}
		displaySpec.description = "Images and hyperlinks use the URL already returned in this field."
		result = append(result, displaySpec)
		property := func(name, label, control, defaultValue string, optional bool) visualFormatSpec {
			return formatSpec(key+name, label, section, control, []string{"cellContent", field, name}, defaultValue, optional)
		}
		auxiliary := func(name, label, defaultLabel string) visualFormatSpec {
			spec := property(name, label, "select", "", true)
			spec.choices = []VisualFormatChoice{{Value: "", Label: defaultLabel}}
			for _, alias := range fields {
				spec.choices = append(spec.choices, VisualFormatChoice{Value: alias, Label: alias})
			}
			spec.description = "Choose a field already returned by this visual's query."
			return spec
		}
		switch display {
		case "image_inline", "image_tooltip":
			for _, dimension := range []struct{ name, label string }{{"width", "Image width (px)"}, {"height", "Image height (px)"}} {
				spec := property(dimension.name, dimension.label, "number", "", true)
				spec.placeholder = "Automatic"
				spec.integer = true
				minimum, maximum, step := float64(1), float64(512), float64(1)
				spec.minimum, spec.maximum, spec.step = &minimum, &maximum, &step
				spec.description = "Use 1–512 pixels, or leave empty for automatic sizing. Inline images fit within the row height."
				result = append(result, spec)
			}
			result = append(result, auxiliary("altField", "Image description field", "Automatic"))
		case "link":
			result = append(result, auxiliary("labelField", "Link label field", "Use URL"))
			newTab := property("newTab", "Open in new tab", "toggle", "true", true)
			result = append(result, newTab)
		}
	}
	return result
}

func gridCellContentMetricFields(visual document.DashboardVisual) map[string]bool {
	var metrics []document.DashboardMetricSelection
	switch query := visual.Query.Value.(type) {
	case *document.AggregateDashboardQuery:
		if query != nil {
			metrics = query.Metrics
		}
	case *document.PivotDashboardQuery:
		if query != nil {
			metrics = query.Metrics
		}
	}
	result := map[string]bool{}
	for _, metric := range metrics {
		_, alias := canonicalMetricSelection(metric)
		if metric.String != nil {
			alias = gridCellContentMemberName(alias)
		}
		result[strings.TrimSpace(alias)] = true
	}
	return result
}

func gridCellContentDisplayValue(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return "text"
	}
	switch object["kind"] {
	case "image":
		if object["display"] == "tooltip" {
			return "image_tooltip"
		}
		return "image_inline"
	case "link":
		return "link"
	default:
		return "text"
	}
}

func applyGridCellContentDisplay(raw map[string]any, path []string, display string) {
	if display == "text" {
		deleteFormatPath(raw, path)
		return
	}
	existing, _ := lookupFormatPath(raw, path)
	configuration, _ := existing.(map[string]any)
	if display == "link" {
		if configuration == nil || configuration["kind"] != "link" {
			configuration = map[string]any{"kind": "link"}
		}
	} else {
		if configuration == nil || configuration["kind"] != "image" {
			configuration = map[string]any{"kind": "image"}
		}
		if display == "image_tooltip" {
			configuration["display"] = "tooltip"
		} else {
			configuration["display"] = "inline"
		}
	}
	setFormatPath(raw, path, configuration)
}

// Matrix and pivot row fields survive cross-tab shaping; their column
// dimensions and metric aliases do not appear as individual URL cells.
func gridCellContentDeliveredFields(visual document.DashboardVisual) []string {
	var result []string
	addDimension := func(value document.DashboardDimensionSelection) {
		_, alias := canonicalDimensionSelection(value)
		if value.String != nil {
			alias = gridCellContentMemberName(alias)
		}
		result = append(result, strings.TrimSpace(alias))
	}
	addMetric := func(value document.DashboardMetricSelection) {
		_, alias := canonicalMetricSelection(value)
		if value.String != nil {
			alias = gridCellContentMemberName(alias)
		}
		result = append(result, strings.TrimSpace(alias))
	}
	switch visual.Type {
	case document.DashboardVisualTypeTable:
		switch query := visual.Query.Value.(type) {
		case *document.RecordsDashboardQuery:
			if query != nil {
				for _, value := range query.Fields {
					_, alias := canonicalRecordSelection(value)
					if value.String != nil || (value.Reference != nil && value.Reference.Alias == nil) {
						alias = gridCellContentMemberName(alias)
					}
					result = append(result, strings.TrimSpace(alias))
				}
			}
		case *document.AggregateDashboardQuery:
			if query != nil {
				for _, dimension := range query.Dimensions {
					addDimension(dimension)
				}
				for _, metric := range query.Metrics {
					addMetric(metric)
				}
			}
		case *document.PivotDashboardQuery:
			if query != nil {
				for _, dimension := range append(append([]document.DashboardDimensionSelection(nil), query.Rows...), query.Columns...) {
					addDimension(dimension)
				}
				for _, metric := range query.Metrics {
					addMetric(metric)
				}
			}
		}
	case document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		switch query := visual.Query.Value.(type) {
		case *document.PivotDashboardQuery:
			if query != nil {
				for _, dimension := range query.Rows {
					addDimension(dimension)
				}
			}
		case *document.AggregateDashboardQuery:
			if query != nil && visual.Type == document.DashboardVisualTypeMatrix && len(query.Dimensions) >= 2 {
				addDimension(query.Dimensions[0])
			}
		}
	}
	unique := make([]string, 0, len(result))
	seen := map[string]bool{}
	for _, field := range result {
		if field != "" && !seen[field] {
			unique = append(unique, field)
			seen[field] = true
		}
	}
	return unique
}

func gridCellContentMemberName(name string) string {
	name = strings.TrimSpace(name)
	if index := strings.LastIndexByte(name, '.'); index >= 0 {
		return name[index+1:]
	}
	return name
}

func gridCellContentCollapsedFields(hierarchy *visualizationir.GridRowHierarchy) map[string]bool {
	result := map[string]bool{}
	if hierarchy == nil {
		return result
	}
	switch value := hierarchy.Value.(type) {
	case *visualizationir.GridLevelHierarchy:
		if value != nil {
			for _, field := range value.Fields {
				result[field] = true
			}
		}
	case *visualizationir.GridParentChildHierarchy:
		if value != nil {
			for _, field := range []string{value.IDField, value.ParentField} {
				if field != value.LabelField {
					result[field] = true
				}
			}
		}
	case *visualizationir.GridNestedHierarchy:
		if value != nil {
			result[value.ChildrenField] = true
			if value.IDField != nil && *value.IDField != value.LabelField {
				result[*value.IDField] = true
			}
		}
	}
	return result
}
