package application

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/authoring/explorationadapter"
)

// deriveExplorationAdapterOptions admits only selected semantic references.
// It never scans unrelated model fields, and every physical reference must be
// proven by an activation-owned dimension binding.
func deriveExplorationAdapterOptions(spec exploration.ExplorationSpec, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, visualID string) (explorationadapter.Options, error) {
	if model == nil || compiled == nil {
		return explorationadapter.Options{}, fmt.Errorf("active semantic model projection is required")
	}
	if !compiled.MatchesModel(model) {
		return explorationadapter.Options{}, fmt.Errorf("active compiled semantic model does not match model projection")
	}
	options := explorationadapter.Options{VisualID: visualID, Bindings: map[string]string{}, MetricRoots: map[string]string{}}
	addDimension := func(raw string) error {
		field := strings.TrimSpace(raw)
		if field == "" {
			return fmt.Errorf("dimension field is required")
		}
		if _, ok := compiled.SemanticDimension(field); ok {
			options.Bindings[field] = field
			bound := false
			for _, dataset := range compiled.DatasetNames() {
				active, activeOK := compiled.DimensionBinding(field, dataset)
				if !activeOK {
					continue
				}
				physical := strings.TrimSpace(active.Physical.Field)
				if physical == "" {
					return fmt.Errorf("dimension %q has an empty active binding for dataset %q", field, dataset)
				}
				options.Bindings[physical] = field
				bound = true
			}
			if !bound {
				return fmt.Errorf("dimension %q has no active binding", field)
			}
			return nil
		}
		if _, physical, ok := strings.Cut(field, "."); ok {
			if physical == "" {
				return fmt.Errorf("dimension field %q is malformed", field)
			}
			if _, ok := compiled.PhysicalField(field); !ok {
				return fmt.Errorf("dimension field %q is not an active physical field", field)
			}
			matched := ""
			for _, name := range compiled.SemanticDimensionNames() {
				for _, dataset := range compiled.DatasetNames() {
					binding, bound := compiled.DimensionBinding(name, dataset)
					if !bound || strings.TrimSpace(binding.Physical.Field) != field {
						continue
					}
					if matched != "" && matched != name {
						return fmt.Errorf("physical dimension field %q has ambiguous semantic bindings", field)
					}
					matched = name
				}
			}
			if matched != "" {
				options.Bindings[field], options.Bindings[matched] = matched, matched
				return nil
			}
		}
		return fmt.Errorf("dimension field %q is not a selected semantic binding", field)
	}
	addMetric := func(raw string) error {
		field := strings.TrimSpace(raw)
		if _, ok := compiled.Metric(field); !ok {
			return fmt.Errorf("metric field %q is not a selected semantic metric", field)
		}
		compiledMetric, ok := compiled.Metric(field)
		if !ok || len(compiledMetric.RootDatasets) != 1 || strings.TrimSpace(compiledMetric.RootDatasets[0]) == "" {
			return fmt.Errorf("metric field %q has ambiguous active root lineage", field)
		}
		options.Bindings[field] = field
		options.MetricRoots[field] = strings.TrimSpace(compiledMetric.RootDatasets[0])
		return nil
	}
	canonicalField := func(raw string) string {
		raw = strings.TrimSpace(raw)
		for _, dimension := range spec.Dimensions {
			if dimension.Alias != nil && strings.TrimSpace(*dimension.Alias) == raw {
				return dimension.Field
			}
		}
		if spec.Pivot != nil {
			for _, dimension := range append(append([]exploration.ExplorationDimensionRef{}, spec.Pivot.Rows...), spec.Pivot.Columns...) {
				if dimension.Alias != nil && strings.TrimSpace(*dimension.Alias) == raw {
					return dimension.Field
				}
			}
		}
		for _, metric := range spec.Metrics {
			if metric.Alias != nil && strings.TrimSpace(*metric.Alias) == raw {
				return metric.Field
			}
		}
		if spec.Pivot != nil {
			for _, metric := range spec.Pivot.Metrics {
				if metric.Alias != nil && strings.TrimSpace(*metric.Alias) == raw {
					return metric.Field
				}
			}
		}
		if spec.Time != nil && spec.Time.Alias != nil && strings.TrimSpace(*spec.Time.Alias) == raw {
			return spec.Time.Field
		}
		return raw
	}
	addSelectedField := func(raw, role string) error {
		field := canonicalField(raw)
		if err := addDimension(field); err == nil {
			return nil
		}
		if err := addMetric(field); err != nil {
			return fmt.Errorf("%s field %q: %w", role, raw, err)
		}
		return nil
	}
	for _, dimension := range spec.Dimensions {
		if err := addDimension(dimension.Field); err != nil {
			return explorationadapter.Options{}, err
		}
	}
	for _, metric := range spec.Metrics {
		if err := addMetric(metric.Field); err != nil {
			return explorationadapter.Options{}, err
		}
	}
	for _, filter := range spec.Filters {
		if err := addDimension(filter.Field); err != nil {
			return explorationadapter.Options{}, err
		}
	}
	if spec.Time != nil {
		if err := addDimension(spec.Time.Field); err != nil {
			return explorationadapter.Options{}, err
		}
	}
	for _, sort := range spec.Sort {
		if err := addSelectedField(sort.Field, "sort"); err != nil {
			return explorationadapter.Options{}, err
		}
	}
	if spec.Pivot != nil && spec.Pivot.Sort != nil {
		for _, sort := range *spec.Pivot.Sort {
			if err := addSelectedField(sort.Field, "pivot sort"); err != nil {
				return explorationadapter.Options{}, err
			}
		}
	}
	if spec.Visualization != nil && spec.Visualization.Value != nil {
		addVisualField := func(ref exploration.ExplorationVisualizationFieldRef) error {
			return addSelectedField(ref.Field, "visualization")
		}
		switch value := spec.Visualization.Value.(type) {
		case *exploration.CartesianExplorationVisualization:
			if value.X != nil {
				if err := addVisualField(*value.X); err != nil {
					return explorationadapter.Options{}, err
				}
			}
			if value.Y != nil {
				for _, ref := range *value.Y {
					if err := addVisualField(ref); err != nil {
						return explorationadapter.Options{}, err
					}
				}
			}
			if value.Series != nil {
				if err := addVisualField(*value.Series); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		case *exploration.GeographicExplorationVisualization:
			if err := addVisualField(value.Latitude); err != nil {
				return explorationadapter.Options{}, err
			}
			if err := addVisualField(value.Longitude); err != nil {
				return explorationadapter.Options{}, err
			}
			if value.Color != nil {
				if err := addVisualField(*value.Color); err != nil {
					return explorationadapter.Options{}, err
				}
			}
			if value.Size != nil {
				if err := addVisualField(*value.Size); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		case *exploration.HierarchyExplorationVisualization:
			if err := addVisualField(value.Node); err != nil {
				return explorationadapter.Options{}, err
			}
			if value.Parent != nil {
				if err := addVisualField(*value.Parent); err != nil {
					return explorationadapter.Options{}, err
				}
			}
			if value.Value != nil {
				if err := addVisualField(*value.Value); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		case *exploration.KPIExplorationVisualization:
			if err := addVisualField(value.Value); err != nil {
				return explorationadapter.Options{}, err
			}
			if value.Comparison != nil {
				if err := addVisualField(*value.Comparison); err != nil {
					return explorationadapter.Options{}, err
				}
			}
			if value.Goal != nil {
				if err := addVisualField(*value.Goal); err != nil {
					return explorationadapter.Options{}, err
				}
			}
			if value.Trend != nil {
				if err := addVisualField(value.Trend.Category); err != nil {
					return explorationadapter.Options{}, err
				}
				if err := addVisualField(value.Trend.Value); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		case *exploration.MatrixExplorationVisualization:
			for _, ref := range append(append(append([]exploration.ExplorationVisualizationFieldRef{}, value.Rows...), value.Columns...), value.Metrics...) {
				if err := addVisualField(ref); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		case *exploration.PivotExplorationVisualization:
			for _, ref := range append(append(append([]exploration.ExplorationVisualizationFieldRef{}, value.Rows...), value.Columns...), value.Metrics...) {
				if err := addVisualField(ref); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		case *exploration.PointExplorationVisualization:
			if err := addVisualField(value.X); err != nil {
				return explorationadapter.Options{}, err
			}
			if err := addVisualField(value.Y); err != nil {
				return explorationadapter.Options{}, err
			}
			if value.Size != nil {
				if err := addVisualField(*value.Size); err != nil {
					return explorationadapter.Options{}, err
				}
			}
			if value.Color != nil {
				if err := addVisualField(*value.Color); err != nil {
					return explorationadapter.Options{}, err
				}
			}
			if value.Identity != nil {
				for _, ref := range *value.Identity {
					if err := addVisualField(ref); err != nil {
						return explorationadapter.Options{}, err
					}
				}
			}
		case *exploration.PolarExplorationVisualization:
			if value.Category != nil {
				if err := addVisualField(*value.Category); err != nil {
					return explorationadapter.Options{}, err
				}
			}
			if err := addVisualField(value.Value); err != nil {
				return explorationadapter.Options{}, err
			}
			if value.Series != nil {
				if err := addVisualField(*value.Series); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		case *exploration.ProportionalExplorationVisualization:
			if err := addVisualField(value.Category); err != nil {
				return explorationadapter.Options{}, err
			}
			if err := addVisualField(value.Value); err != nil {
				return explorationadapter.Options{}, err
			}
			if value.Series != nil {
				if err := addVisualField(*value.Series); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		case *exploration.TableExplorationVisualization:
			for _, ref := range value.Columns {
				if err := addVisualField(ref); err != nil {
					return explorationadapter.Options{}, err
				}
			}
		}
	}
	if dataset := strings.TrimSpace(valueOrString(spec.DatasetID)); dataset != "" {
		if _, ok := compiled.Dataset(dataset); !ok {
			return explorationadapter.Options{}, fmt.Errorf("dataset %q is not active", dataset)
		}
	}
	return options, nil
}

func valueOrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
