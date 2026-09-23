package exploration

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

var (
	explorationResourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
	explorationFieldIDPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]*$`)
	explorationAliasPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	explorationIntegerPattern    = regexp.MustCompile(`^(0|-?[1-9][0-9]*)$`)
	explorationDecimalPattern    = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
)

// ValidateShape checks the renderer-neutral authored contract without needing
// model metadata. It is intentionally separate from ValidateAgainstModel so
// URL codecs and other boundary callers can fail closed before model lookup.
func ValidateShape(spec *ExplorationSpec) error {
	if spec == nil {
		return errors.New("exploration spec is required")
	}
	if spec.SchemaVersion != 1 {
		return fmt.Errorf("unsupported exploration schema version %d", spec.SchemaVersion)
	}
	if err := validateResourceID(spec.ModelID, "modelId"); err != nil {
		return err
	}
	if spec.DatasetID != nil {
		if err := validateResourceID(*spec.DatasetID, "datasetId"); err != nil {
			return err
		}
	}
	if spec.Dimensions == nil || spec.Metrics == nil || spec.Filters == nil || spec.Sort == nil {
		return errors.New("exploration dimensions, metrics, filters, and sort are required")
	}
	if len(spec.Dimensions) > 100 || len(spec.Metrics) > 100 || len(spec.Filters) > 100 || len(spec.Sort) > 100 {
		return errors.New("exploration selection, filter, or sort count exceeds 100")
	}
	if spec.Limit < 1 || spec.Limit > 1000 {
		return fmt.Errorf("exploration limit %d is outside 1..1000", spec.Limit)
	}

	selected := map[string]string{}
	for _, dimension := range spec.Dimensions {
		if err := validateFieldID(dimension.Field, "dimension field"); err != nil {
			return err
		}
		if err := validateAlias(dimension.Alias, "dimension", dimension.Field); err != nil {
			return err
		}
		if dimension.Grain != nil {
			if err := validateTimeGrain(*dimension.Grain); err != nil {
				return fmt.Errorf("invalid exploration dimension %q grain: %w", dimension.Field, err)
			}
		}
		if err := addSelection(selected, dimension.Field, stringPointerValue(dimension.Alias), "dimension"); err != nil {
			return err
		}
	}
	for _, metric := range spec.Metrics {
		if err := validateFieldID(metric.Field, "metric field"); err != nil {
			return err
		}
		if err := validateAlias(metric.Alias, "metric", metric.Field); err != nil {
			return err
		}
		if err := addSelection(selected, metric.Field, stringPointerValue(metric.Alias), "metric"); err != nil {
			return err
		}
	}
	for index, filter := range spec.Filters {
		if err := validateFieldID(filter.Field, fmt.Sprintf("filter %d field", index)); err != nil {
			return err
		}
		if filter.DatasetID != nil {
			if err := validateResourceID(*filter.DatasetID, fmt.Sprintf("filter %d datasetId", index)); err != nil {
				return err
			}
		}
		if err := validateFilterExpression(&filter.Expression, ""); err != nil {
			return fmt.Errorf("invalid exploration filter %d: %w", index, err)
		}
	}
	if spec.Time != nil {
		if err := validateFieldID(spec.Time.Field, "time field"); err != nil {
			return err
		}
		if err := validateTimeGrain(spec.Time.Grain); err != nil {
			return fmt.Errorf("invalid exploration time grain: %w", err)
		}
		if err := validateAlias(spec.Time.Alias, "time", spec.Time.Field); err != nil {
			return err
		}
		if err := addTimeSelection(selected, spec.Time.Field, stringPointerValue(spec.Time.Alias)); err != nil {
			return err
		}
		if err := validateTimeDecoration(spec); err != nil {
			return err
		}
		if err := validateTimeRange(spec.Time.Range, ""); err != nil {
			return fmt.Errorf("invalid exploration time range: %w", err)
		}
	}
	for index, sort := range spec.Sort {
		if err := validateSort(sort, selected, index); err != nil {
			return err
		}
	}
	if err := validatePivotShape(spec.Pivot, selected); err != nil {
		return err
	}
	if err := validateTable(spec.Table, selected); err != nil {
		return err
	}
	if err := validateVisualization(spec.Visualization, selected); err != nil {
		return err
	}
	return nil
}

// validateTimeDecoration prevents a time selection from changing the
// authored identity of an already selected dimension. The lowering path emits
// one output for that field, so conflicting grain or alias declarations must
// fail before query construction rather than producing duplicate columns.
func validateTimeDecoration(spec *ExplorationSpec) error {
	if spec == nil || spec.Time == nil {
		return nil
	}
	timeAlias := stringPointerValue(spec.Time.Alias)
	for _, dimension := range spec.Dimensions {
		if dimension.Field != spec.Time.Field {
			continue
		}
		if dimension.Grain != nil && *dimension.Grain != spec.Time.Grain {
			return fmt.Errorf("exploration dimension %q grain %q conflicts with time grain %q", dimension.Field, *dimension.Grain, spec.Time.Grain)
		}
		dimensionAlias := stringPointerValue(dimension.Alias)
		if dimensionAlias != "" && timeAlias != "" && dimensionAlias != timeAlias {
			return fmt.Errorf("exploration dimension %q alias %q conflicts with time alias %q", dimension.Field, dimensionAlias, timeAlias)
		}
	}
	return nil
}

func ValidateAgainstModel(model *semanticmodel.Model, spec *ExplorationSpec) error {
	if err := ValidateShape(spec); err != nil {
		return err
	}
	if model == nil {
		return errors.New("semantic model is unavailable")
	}
	if spec.DatasetID != nil {
		datasetID := *spec.DatasetID
		if err := validateExplorationDataset(model, datasetID); err != nil {
			return err
		}
	}
	selected := map[string]string{}
	addSelection := func(field, alias, kind string) error {
		field = strings.TrimSpace(field)
		if field == "" {
			return fmt.Errorf("exploration %s field is required", kind)
		}
		if _, exists := selected[field]; exists {
			return fmt.Errorf("duplicate exploration %s %q", kind, field)
		}
		selected[field] = field
		if alias == "" {
			return nil
		}
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return fmt.Errorf("exploration %s %q has an empty alias", kind, field)
		}
		if previous, exists := selected[alias]; exists && previous != field {
			return fmt.Errorf("exploration alias %q is ambiguous", alias)
		}
		selected[alias] = field
		return nil
	}
	for _, dimension := range spec.Dimensions {
		resolvedDimension, err := resolveExplorationDimension(model, dimension.Field)
		if err != nil {
			return fmt.Errorf("invalid exploration dimension %q: %w", dimension.Field, err)
		}
		if err := validateAlias(dimension.Alias, "dimension", dimension.Field); err != nil {
			return err
		}
		if dimension.Grain != nil {
			if err := validateModelGrain(model, dimension.Field, resolvedDimension, *dimension.Grain); err != nil {
				return fmt.Errorf("invalid exploration dimension %q grain: %w", dimension.Field, err)
			}
		}
		if err := addSelection(dimension.Field, stringPointerValue(dimension.Alias), "dimension"); err != nil {
			return err
		}
	}
	for _, metric := range spec.Metrics {
		if err := model.ValidateAggregateMember(metric.Field); err != nil {
			return fmt.Errorf("invalid exploration metric %q: %w", metric.Field, err)
		}
		if err := validateAlias(metric.Alias, "metric", metric.Field); err != nil {
			return err
		}
		if err := addSelection(metric.Field, stringPointerValue(metric.Alias), "metric"); err != nil {
			return err
		}
	}
	for index, filter := range spec.Filters {
		dimension, err := resolveExplorationDimension(model, filter.Field)
		if err != nil {
			return fmt.Errorf("invalid exploration filter %d field %q: %w", index, filter.Field, err)
		}
		if filter.DatasetID != nil {
			datasetID := strings.TrimSpace(*filter.DatasetID)
			if datasetID == "" {
				return fmt.Errorf("exploration filter %d datasetId cannot be empty", index)
			}
			if err := validateExplorationDataset(model, datasetID); err != nil {
				return fmt.Errorf("unknown exploration filter dataset %q", datasetID)
			}
		}
		if err := validateFilterExpression(&filter.Expression, explorationValueType(dimension)); err != nil {
			return fmt.Errorf("invalid exploration filter %d: %w", index, err)
		}
	}
	if spec.Time != nil {
		dimension, err := resolveExplorationDimension(model, spec.Time.Field)
		if err != nil {
			return fmt.Errorf("invalid exploration time field %q: %w", spec.Time.Field, err)
		}
		if err := validateTimeGrain(spec.Time.Grain); err != nil {
			return fmt.Errorf("invalid exploration time grain: %w", err)
		}
		if err := validateModelGrain(model, spec.Time.Field, dimension, spec.Time.Grain); err != nil {
			return fmt.Errorf("invalid exploration time grain: %w", err)
		}
		if !isTemporalExplorationType(explorationValueType(dimension)) {
			return fmt.Errorf("invalid exploration time field %q: field is not temporal", spec.Time.Field)
		}
		if err := validateAlias(spec.Time.Alias, "time", spec.Time.Field); err != nil {
			return err
		}
		if err := addTimeSelection(selected, spec.Time.Field, stringPointerValue(spec.Time.Alias)); err != nil {
			return err
		}
		if err := validateTimeRange(spec.Time.Range, explorationValueType(dimension)); err != nil {
			return fmt.Errorf("invalid exploration time range: %w", err)
		}
	}
	for index, sort := range spec.Sort {
		if err := validateSort(sort, selected, index); err != nil {
			return err
		}
	}
	if err := validatePivot(model, spec.Pivot, selected); err != nil {
		return err
	}
	if err := validateTable(spec.Table, selected); err != nil {
		return err
	}
	if err := validateVisualization(spec.Visualization, selected); err != nil {
		return err
	}
	return nil
}

func validateExplorationDataset(model *semanticmodel.Model, datasetID string) error {
	if len(model.Datasets) > 0 {
		if _, ok := model.Datasets[datasetID]; !ok {
			return fmt.Errorf("unknown exploration dataset %q", datasetID)
		}
		return nil
	}
	if _, ok := model.Tables[datasetID]; !ok {
		return fmt.Errorf("unknown exploration dataset %q", datasetID)
	}
	return nil
}

func validateResourceID(value, name string) error {
	if len(value) < 1 || len(value) > 200 || !explorationResourceIDPattern.MatchString(value) {
		return fmt.Errorf("exploration %s must match %s", name, explorationResourceIDPattern.String())
	}
	return nil
}

func validateFieldID(value, name string) error {
	if len(value) < 1 || len(value) > 200 || !explorationFieldIDPattern.MatchString(value) {
		return fmt.Errorf("exploration %s must match %s", name, explorationFieldIDPattern.String())
	}
	return nil
}

func validateFieldReference(value, name string) error {
	if err := validateFieldID(value, name); err != nil {
		return err
	}
	return nil
}

func addSelection(selected map[string]string, field, alias, kind string) error {
	if _, exists := selected[field]; exists {
		return fmt.Errorf("duplicate exploration %s %q", kind, field)
	}
	selected[field] = field
	if alias == "" {
		return nil
	}
	if previous, exists := selected[alias]; exists && previous != field {
		return fmt.Errorf("exploration alias %q is ambiguous", alias)
	}
	selected[alias] = field
	return nil
}

// A time selection may decorate a dimension that is already selected (for
// example, to add a range while retaining the dimension's authored grain).
// Keep the field single in the reference map, but still expose a distinct
// time alias to sort/display validation.
func addTimeSelection(selected map[string]string, field, alias string) error {
	if _, exists := selected[field]; exists {
		if alias == "" {
			return nil
		}
		if previous, exists := selected[alias]; exists && previous != field {
			return fmt.Errorf("exploration alias %q is ambiguous", alias)
		}
		selected[alias] = field
		return nil
	}
	return addSelection(selected, field, alias, "time")
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateAlias(alias *string, kind, field string) error {
	if alias == nil {
		return nil
	}
	if len(*alias) < 1 || len(*alias) > 128 || !explorationAliasPattern.MatchString(*alias) {
		return fmt.Errorf("exploration %s %q has an invalid alias", kind, field)
	}
	return nil
}

func validateTimeGrain(grain ExplorationTimeGrain) error {
	switch grain {
	case ExplorationTimeGrainSecond, ExplorationTimeGrainMinute,
		ExplorationTimeGrainHour, ExplorationTimeGrainDay,
		ExplorationTimeGrainWeek, ExplorationTimeGrainMonth,
		ExplorationTimeGrainQuarter, ExplorationTimeGrainYear:
		return nil
	default:
		return fmt.Errorf("unsupported grain %q", grain)
	}
}

func resolveExplorationDimension(model *semanticmodel.Model, field string) (semanticmodel.MetricDimension, error) {
	if strings.TrimSpace(field) != field || field == "" {
		return semanticmodel.MetricDimension{}, errors.New("field is required and must not contain surrounding whitespace")
	}
	if dimension, err := model.ResolveDimension(field); err == nil {
		return dimension, nil
	}
	dimension, err := model.ResolveSemanticDimension(field)
	if err != nil {
		return semanticmodel.MetricDimension{}, err
	}
	return semanticmodel.MetricDimension{Type: dimension.Type, Datatype: dimension.Datatype}, nil
}

func explorationValueType(dimension semanticmodel.MetricDimension) string {
	if dimension.Datatype != "" {
		return strings.ToLower(string(dimension.Datatype))
	}
	return strings.ToLower(strings.TrimSpace(dimension.Type))
}

func isTemporalExplorationType(valueType string) bool {
	switch valueType {
	case "date", "datetime", "datetimetz", "timestamp":
		return true
	default:
		return false
	}
}

var explorationGrainOrder = map[ExplorationTimeGrain]int{
	ExplorationTimeGrainSecond:  0,
	ExplorationTimeGrainMinute:  1,
	ExplorationTimeGrainHour:    2,
	ExplorationTimeGrainDay:     3,
	ExplorationTimeGrainWeek:    4,
	ExplorationTimeGrainMonth:   5,
	ExplorationTimeGrainQuarter: 6,
	ExplorationTimeGrainYear:    7,
}

func validateModelGrain(model *semanticmodel.Model, field string, dimension semanticmodel.MetricDimension, grain ExplorationTimeGrain) error {
	if err := validateTimeGrain(grain); err != nil {
		return err
	}
	if !isTemporalExplorationType(explorationValueType(dimension)) {
		return errors.New("field is not temporal")
	}
	// Physical fields have no authored grain allowlist. Semantic dimensions may
	// narrow the closed exploration grain union to the model's declared grains.
	if _, err := model.ResolveDimension(field); err == nil {
		return nil
	}
	semantic, err := model.ResolveSemanticDimension(field)
	if err != nil {
		return nil
	}
	if len(semantic.Grains) > 0 {
		for _, allowed := range semantic.Grains {
			if allowed == string(grain) {
				return nil
			}
		}
		return fmt.Errorf("grain %q is not declared for semantic dimension %q", grain, field)
	}
	if semantic.NativeGrain != "" {
		requested, requestedOK := explorationGrainOrder[grain]
		native, nativeOK := explorationGrainOrder[ExplorationTimeGrain(semantic.NativeGrain)]
		if requestedOK && nativeOK && requested < native {
			return fmt.Errorf("grain %q is finer than native grain %q", grain, semantic.NativeGrain)
		}
	}
	return nil
}

func validateTable(table *ExplorationTableDisplayConfig, selected map[string]string) error {
	if table == nil {
		return nil
	}
	if table.Density != nil && *table.Density != ExplorationTableDensityCompact && *table.Density != ExplorationTableDensityComfortable {
		return fmt.Errorf("invalid table density %q", *table.Density)
	}
	if table.RowHeight != nil && (*table.RowHeight < 1 || *table.RowHeight > 2000) {
		return fmt.Errorf("table rowHeight %d is outside 1..2000", *table.RowHeight)
	}
	if table.Columns == nil {
		return nil
	}
	if len(*table.Columns) > 100 {
		return errors.New("table columns exceed the maximum item count")
	}
	for index, column := range *table.Columns {
		if err := validateVisualizationFieldRef(ExplorationVisualizationFieldRef{Field: column.Field, Format: column.Format}, selected); err != nil {
			return fmt.Errorf("invalid table column %d: %w", index, err)
		}
		if column.Width != nil && (*column.Width < 1 || *column.Width > 2000) {
			return fmt.Errorf("table column %d width is outside 1..2000", index)
		}
	}
	return nil
}
