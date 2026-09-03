package http

import (
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/strictjson"
)

func (h *BrowserHandler) dataExplorerSignals(w stdhttp.ResponseWriter, r *stdhttp.Request) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, bool) {
	return h.dataExplorerSignalsForURL(w, r, true)
}

// dataExplorerSignalsForURL restores durable exploration state from a browser
// URL. URL state is treated as an assertion about the query, so it is checked
// strictly after the authorized active-generation projection is available.
// Interactive command payloads use the separate command path; only an empty
// initialization command may receive defaults, while authored operands fail
// closed when they are unavailable or incompatible.
func (h *BrowserHandler) dataExplorerSignalsForURL(w stdhttp.ResponseWriter, r *stdhttp.Request, executeQuery bool) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		stdhttp.Error(w, "invalid exploration URL: "+err.Error(), stdhttp.StatusBadRequest)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	if err := validateDataExplorerURLShape(values); err != nil {
		stdhttp.Error(w, "invalid exploration URL: "+err.Error(), stdhttp.StatusBadRequest)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	mode, modePresent := dataExplorerURLValue(values, "mode")
	var modeSignal *string
	if modePresent {
		modeSignal = projectsignals.Optional(mode)
	}
	object, objectPresent := dataExplorerURLValue(values, "object")
	var objectSignal *string
	if objectPresent && object != "" {
		objectSignal = projectsignals.Optional(object)
	}
	command := projectsignals.DataExplorerCommand{
		ObjectKey: objectSignal,
		Mode:      modeSignal,
		Limit:     dataExplorerDefaultLimit, Count: dataExplorerDefaultLimit, Block: projectsignals.Pointer("all"),
	}
	if mode == "explore" {
		explore, err := dataExploreCommandFromQuery(values)
		if err != nil {
			stdhttp.Error(w, "invalid exploration URL: "+err.Error(), stdhttp.StatusBadRequest)
			return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
		}
		command.Explore = &explore
	}
	version, versionPresent := dataExplorerURLValue(values, "v")
	legacyURLState := mode == "explore" && (!versionPresent || version == dataExploreURLVersion)
	return h.dataExplorerSignalsForRestoredCommand(w, r, command, executeQuery, legacyURLState)
}

const dataExploreURLVersion = "1"
const dataExploreCanonicalURLVersion = "2"

var dataExplorerURLSingletonKeys = []string{"v", "mode", "object", "model", "dataset", "time", "limit", "state"}

// dataExplorerURLValue returns the one value allowed for a singleton URL
// parameter. Repeated singleton values are ambiguous because URL parsers
// otherwise silently select the first value.
func dataExplorerURLValue(values url.Values, key string) (string, bool) {
	raw, present := values[key]
	if !present || len(raw) == 0 {
		return "", present
	}
	return strings.TrimSpace(raw[0]), true
}

func validateDataExplorerURLShape(values url.Values) error {
	if err := validateDataExplorerURLSingletons(values); err != nil {
		return err
	}
	if err := validateDataExplorerURLValues(values); err != nil {
		return err
	}
	if err := validateDataExplorerURLVersion(values); err != nil {
		return err
	}
	return validateDataExplorerURLMode(values, true)
}

func validateDataExplorerURLSingletons(values url.Values) error {
	for _, key := range dataExplorerURLSingletonKeys {
		if len(values[key]) > 1 {
			return fmt.Errorf("%s may only be specified once", key)
		}
	}
	return nil
}

func validateDataExplorerURLValues(values url.Values) error {
	for _, key := range []string{"object", "model", "dataset", "time", "limit", "state"} {
		if value, present := dataExplorerURLValue(values, key); present && value == "" {
			return fmt.Errorf("%s must not be empty", key)
		}
	}
	return nil
}

func validateDataExplorerURLVersion(values url.Values) error {
	if version, present := dataExplorerURLValue(values, "v"); present {
		if version == "" {
			return errors.New("version must be specified")
		}
		if version != dataExploreURLVersion && version != dataExploreCanonicalURLVersion {
			return fmt.Errorf("unsupported version %q", version)
		}
	}
	return nil
}

func validateDataExplorerURLMode(values url.Values, requireExploreMode bool) error {
	mode, modePresent := dataExplorerURLValue(values, "mode")
	version, versionPresent := dataExplorerURLValue(values, "v")
	if modePresent {
		switch mode {
		case "browse", "explore":
		default:
			return fmt.Errorf("unsupported mode %q; choose browse or explore", mode)
		}
	}
	if mode == "explore" {
		if _, present := values["object"]; present {
			return errors.New("object cannot be combined with explore mode")
		}
		_, statePresent := values["state"]
		if version == dataExploreCanonicalURLVersion {
			if !versionPresent || !statePresent {
				return errors.New("version 2 explore URLs require a state parameter")
			}
			if dataExplorerURLHasLegacyExploreOperands(values) {
				return errors.New("version 2 state URLs cannot include legacy exploration parameters")
			}
		} else if statePresent {
			return errors.New("state requires version 2")
		}
	} else if (modePresent || requireExploreMode) && dataExplorerURLHasExploreOperands(values) {
		return errors.New("exploration operands require mode=explore")
	}
	if versionPresent && version == dataExploreCanonicalURLVersion && mode != "explore" {
		return errors.New("version 2 URLs require mode=explore")
	}
	if mode != "explore" && values["state"] != nil {
		return errors.New("state requires mode=explore")
	}
	return nil
}

func dataExplorerURLHasExploreOperands(values url.Values) bool {
	for _, key := range []string{"model", "dataset", "dimension", "metric", "filter", "sort", "time", "limit", "state"} {
		if _, present := values[key]; present {
			return true
		}
	}
	return false
}

func dataExplorerURLHasLegacyExploreOperands(values url.Values) bool {
	for _, key := range []string{"model", "dataset", "dimension", "metric", "filter", "sort", "time", "limit"} {
		if _, present := values[key]; present {
			return true
		}
	}
	return false
}

func dataExploreCommandFromQuery(values url.Values) (projectsignals.DataExploreCommand, error) {
	if err := validateDataExplorerURLSingletons(values); err != nil {
		return projectsignals.DataExploreCommand{}, err
	}
	if err := validateDataExplorerURLValues(values); err != nil {
		return projectsignals.DataExploreCommand{}, err
	}
	if err := validateDataExplorerURLVersion(values); err != nil {
		return projectsignals.DataExploreCommand{}, err
	}
	if err := validateDataExplorerURLMode(values, false); err != nil {
		return projectsignals.DataExploreCommand{}, err
	}
	if version, present := dataExplorerURLValue(values, "v"); present && version == dataExploreCanonicalURLVersion {
		value, statePresent := dataExplorerURLValue(values, "state")
		if !statePresent || value == "" {
			return projectsignals.DataExploreCommand{}, errors.New("version 2 explore URLs require a state parameter")
		}
		var spec exploration.ExplorationSpec
		if err := decodeDataExploreURLValue(value, &spec); err != nil {
			return projectsignals.DataExploreCommand{}, fmt.Errorf("state: %w", err)
		}
		if err := exploration.ValidateShape(&spec); err != nil {
			return projectsignals.DataExploreCommand{}, fmt.Errorf("state: %w", err)
		}
		return projectsignals.DataExploreCommand{Spec: spec}, nil
	}
	spec, err := legacyExplorationSpecFromQuery(values)
	if err != nil {
		return projectsignals.DataExploreCommand{}, err
	}
	return projectsignals.DataExploreCommand{Spec: spec}, nil
}

type legacyDataExploreFilter struct {
	Dataset  *string  `json:"dataset,omitempty"`
	Field    string   `json:"field"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type legacyDataExploreSort struct {
	Direction string `json:"direction"`
	Field     string `json:"field"`
}

type legacyDataExploreTime struct {
	Alias *string `json:"alias,omitempty"`
	Field string  `json:"field"`
	Grain string  `json:"grain"`
}

func legacyExplorationSpecFromQuery(values url.Values) (exploration.ExplorationSpec, error) {
	spec := defaultExplorationSpec()
	for _, field := range append(append([]string(nil), values["dimension"]...), values["metric"]...) {
		if strings.TrimSpace(field) == "" {
			return spec, errors.New("dimension and metric identifiers must not be empty")
		}
	}
	seen := map[string]string{}
	for _, field := range values["dimension"] {
		field = strings.TrimSpace(field)
		if prior, exists := seen[field]; exists {
			return spec, fmt.Errorf("%s %q is specified more than once", prior, field)
		}
		seen[field] = "dimension"
		spec.Dimensions = append(spec.Dimensions, exploration.ExplorationDimensionRef{Field: field})
	}
	for _, field := range values["metric"] {
		field = strings.TrimSpace(field)
		if prior, exists := seen[field]; exists {
			return spec, fmt.Errorf("%s %q is specified more than once", prior, field)
		}
		seen[field] = "metric"
		spec.Metrics = append(spec.Metrics, exploration.ExplorationMetricRef{Field: field})
	}
	if value, present := dataExplorerURLValue(values, "model"); present && value != "" {
		spec.ModelID = value
	}
	if value, present := dataExplorerURLValue(values, "dataset"); present && value != "" {
		spec.DatasetID = &value
	}
	for index, value := range values["filter"] {
		var filter legacyDataExploreFilter
		if err := decodeDataExploreURLValue(value, &filter); err != nil {
			return spec, fmt.Errorf("filter %d: %w", index+1, err)
		}
		if strings.TrimSpace(filter.Field) == "" || strings.TrimSpace(filter.Operator) == "" || filter.Values == nil {
			return spec, fmt.Errorf("filter %d field, operator, and values are required", index+1)
		}
		filter.Field, filter.Operator = strings.TrimSpace(filter.Field), strings.TrimSpace(filter.Operator)
		if filter.Dataset != nil {
			dataset := strings.TrimSpace(*filter.Dataset)
			if dataset == "" {
				return spec, fmt.Errorf("filter %d dataset must not be empty", index+1)
			}
			filter.Dataset = &dataset
		}
		expression, err := legacyFilterExpression(filter)
		if err != nil {
			return spec, fmt.Errorf("filter %d: %w", index+1, err)
		}
		spec.Filters = append(spec.Filters, exploration.ExplorationFilter{Field: filter.Field, DatasetID: filter.Dataset, Expression: expression})
	}
	for index, value := range values["sort"] {
		var sorting legacyDataExploreSort
		if err := decodeDataExploreURLValue(value, &sorting); err != nil {
			return spec, fmt.Errorf("sort %d: %w", index+1, err)
		}
		sorting.Field, sorting.Direction = strings.TrimSpace(sorting.Field), strings.TrimSpace(sorting.Direction)
		if sorting.Field == "" || (sorting.Direction != "asc" && sorting.Direction != "desc") {
			return spec, errors.New("sort field and asc or desc direction are required")
		}
		for _, existing := range spec.Sort {
			if existing.Field == sorting.Field {
				return spec, fmt.Errorf("sort field %q is specified more than once", sorting.Field)
			}
		}
		spec.Sort = append(spec.Sort, exploration.ExplorationSort{Field: sorting.Field, Direction: exploration.ExplorationSortDirection(sorting.Direction)})
	}
	if value, present := dataExplorerURLValue(values, "time"); present && value != "" {
		var selection legacyDataExploreTime
		if err := decodeDataExploreURLValue(value, &selection); err != nil {
			return spec, fmt.Errorf("time: %w", err)
		}
		selection.Field, selection.Grain = strings.TrimSpace(selection.Field), strings.TrimSpace(selection.Grain)
		if selection.Field == "" || selection.Grain == "" {
			return spec, errors.New("time field and grain are required")
		}
		if selection.Alias != nil {
			alias := strings.TrimSpace(*selection.Alias)
			if alias == "" {
				selection.Alias = nil
			} else {
				selection.Alias = &alias
			}
		}
		spec.Time = &exploration.ExplorationTimeSelection{Field: selection.Field, Grain: exploration.ExplorationTimeGrain(selection.Grain), Alias: selection.Alias}
	}
	if value, present := dataExplorerURLValue(values, "limit"); present && value != "" {
		limit, err := strconv.ParseInt(value, 10, 32)
		if err != nil || limit <= 0 || limit > dataExplorerMaximumLimit {
			return spec, fmt.Errorf("limit must be between 1 and %d", dataExplorerMaximumLimit)
		}
		spec.Limit = int32(limit)
	}
	return spec, nil
}

func legacyFilterExpression(filter legacyDataExploreFilter) (exploration.ExplorationFilterExpression, error) {
	op := filter.Operator
	values := make([]exploration.ExplorationFilterValue, 0, len(filter.Values))
	for _, value := range filter.Values {
		values = append(values, exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: value}})
	}
	switch filter.Operator {
	case "is_null", "is_not_null":
		if len(filter.Values) != 0 {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("operator %q does not accept values", filter.Operator)
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.NullCheckExplorationFilterExpression{Kind: "null_check", Operator: op}}, nil
	case "in", "not_in":
		if len(values) == 0 {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("operator %q requires at least one value", filter.Operator)
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{Kind: "set", Operator: op, Values: values}}, nil
	case "equals", "not_equals", "contains", "not_contains", "starts_with", "ends_with", "greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal":
		if len(values) != 1 {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("operator %q requires exactly one value", filter.Operator)
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: op, Value: values[0]}}, nil
	default:
		return exploration.ExplorationFilterExpression{}, fmt.Errorf("unsupported filter operator %q", filter.Operator)
	}
}

// adaptLegacyExplorationFilterValues upgrades the old flat URL filter shape,
// whose values were all JSON strings, into the typed canonical value variants
// required by ExplorationSpec. This is deliberately called only for v1 (or
// unversioned) URL restores after the authorized field projection is built;
// canonical v2 state must never infer a value kind from model metadata.
func adaptLegacyExplorationFilterValues(spec *exploration.ExplorationSpec, fields []projectsignals.DataExploreFieldSignal) error {
	if spec == nil {
		return errors.New("exploration spec is required")
	}
	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(fields))
	for _, field := range fields {
		fieldByID[field.ID] = field
	}
	for index := range spec.Filters {
		filter := &spec.Filters[index]
		field, ok := fieldByID[filter.Field]
		if !ok || field.Kind != "dimension" || !field.Compatible {
			return fmt.Errorf("filter %d field %q is no longer available in the active semantic model; remove it from the URL or reload the explorer", index+1, filter.Field)
		}
		fieldType := strings.ToLower(strings.TrimSpace(projectsignals.ValueOrZero(field.Type)))
		if fieldType == "" {
			return fmt.Errorf("filter %d field %q has no logical type in the authorized projection; reload the explorer", index+1, filter.Field)
		}
		kind, err := legacyExplorationFilterKind(fieldType)
		if err != nil {
			return fmt.Errorf("filter %d field %q: %w", index+1, filter.Field, err)
		}
		adapt := func(value *exploration.ExplorationFilterValue, valueIndex int) error {
			if value == nil {
				return fmt.Errorf("filter %d value %d is required", index+1, valueIndex+1)
			}
			legacy, ok := value.Value.(*exploration.StringExplorationFilterValue)
			if !ok || legacy == nil {
				return fmt.Errorf("filter %d value %d is not a legacy string literal", index+1, valueIndex+1)
			}
			converted, err := legacyExplorationFilterValue(kind, legacy.Value)
			if err != nil {
				return fmt.Errorf("filter %d value %d: %w", index+1, valueIndex+1, err)
			}
			*value = converted
			return nil
		}
		switch expression := filter.Expression.Value.(type) {
		case *exploration.SetExplorationFilterExpression:
			for valueIndex := range expression.Values {
				if err := adapt(&expression.Values[valueIndex], valueIndex); err != nil {
					return err
				}
			}
		case *exploration.ComparisonExplorationFilterExpression:
			if err := adapt(&expression.Value, 0); err != nil {
				return err
			}
		case *exploration.NullCheckExplorationFilterExpression:
			// Null checks have no literal values to upgrade.
		case *exploration.UnfilteredExplorationFilterExpression:
			// Unfiltered expressions have no literal values to upgrade.
		default:
			return fmt.Errorf("filter %d uses unsupported legacy expression %T", index+1, filter.Expression.Value)
		}
	}
	// Legacy URLs may omit model; projection selects the active model later.
	// Validate the converted shape without turning that intentional omission
	// into a restore failure.
	shape := *spec
	if strings.TrimSpace(shape.ModelID) == "" {
		shape.ModelID = "legacy:placeholder"
	}
	if err := exploration.ValidateShape(&shape); err != nil {
		return fmt.Errorf("converted filter values: %w", err)
	}
	return nil
}

func legacyExplorationFilterKind(fieldType string) (string, error) {
	switch fieldType {
	case "":
		return "", errors.New("authorized dimension has no logical type")
	case "integer", "int", "bigint", "smallint":
		return "integer", nil
	case "decimal", "float", "number", "numeric", "double", "real":
		return "decimal", nil
	case "boolean", "bool":
		return "boolean", nil
	case "date":
		return "date", nil
	case "timestamp", "datetime", "datetimetz":
		return "timestamp", nil
	case "string", "text", "varchar", "char", "time":
		return "string", nil
	default:
		return "", fmt.Errorf("has unsupported filter type %q; choose a supported dimension", fieldType)
	}
}

func isRestoredTemporalFieldType(fieldType string) bool {
	switch fieldType {
	case "date", "datetime", "datetimetz", "timestamp":
		return true
	default:
		return false
	}
}

func legacyExplorationFilterValue(kind, value string) (exploration.ExplorationFilterValue, error) {
	switch kind {
	case "integer":
		return exploration.ExplorationFilterValue{Value: &exploration.IntegerExplorationFilterValue{Kind: "integer", Value: value}}, nil
	case "decimal":
		return exploration.ExplorationFilterValue{Value: &exploration.DecimalExplorationFilterValue{Kind: "decimal", Value: value}}, nil
	case "boolean":
		if value != "true" && value != "false" {
			return exploration.ExplorationFilterValue{}, fmt.Errorf("boolean literal %q must be true or false", value)
		}
		return exploration.ExplorationFilterValue{Value: &exploration.BooleanExplorationFilterValue{Kind: "boolean", Value: value == "true"}}, nil
	case "date":
		return exploration.ExplorationFilterValue{Value: &exploration.DateExplorationFilterValue{Kind: "date", Value: value}}, nil
	case "timestamp":
		return exploration.ExplorationFilterValue{Value: &exploration.TimestampExplorationFilterValue{Kind: "timestamp", Value: value}}, nil
	case "string":
		return exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: value}}, nil
	default:
		return exploration.ExplorationFilterValue{}, fmt.Errorf("unsupported legacy filter value kind %q", kind)
	}
}

func decodeDataExploreURLValue(value string, target any) error {
	return strictjson.DecodeWithOptions([]byte(value), target, strictjson.Options{MaxBytes: 1 << 20, MaxDepth: 32})
}

// validateRestoredDataExploreState verifies every durable URL operand against
// the authorized active-generation projection. The normal command path keeps
// its incremental normalization, but a URL restore must never turn a stale
// operand into a smaller query by dropping it.
func validateRestoredDataExploreState(command projectsignals.DataExploreCommand, projection DataExplorerProjection, model *semanticmodel.Model, compiledModels map[string]*semanticquery.CompiledModel) error {
	spec := normalizeExplorationSpec(command.Spec)
	state := dataExploreStateFromSpec(spec)
	modelID := strings.TrimSpace(spec.ModelID)
	selectedModelID := strings.TrimSpace(projection.Command.Spec.ModelID)

	if modelID != "" {
		if !explorerModelByID(projection.Models, modelID) {
			return fmt.Errorf("model %q is no longer available; choose an active semantic model", modelID)
		}
		if selectedModelID != modelID {
			return fmt.Errorf("model %q could not be restored; choose an active semantic model", modelID)
		}
	}
	if selectedModelID == "" {
		return fmt.Errorf("no active semantic model is available; choose an active semantic model")
	}
	compiled := compiledModels[selectedModelID]
	if compiled == nil || len(compiled.DatasetNames()) == 0 {
		return fmt.Errorf("model %q has no active compiled definition; reload the explorer after the serving state is ready", selectedModelID)
	}
	if model != nil {
		// Legacy explore URLs historically allowed the model operand to be
		// omitted; projection selects the first visible active model in that
		// case. Validate the restored operands against that selected model while
		// retaining the authored spec unchanged for the later projection.
		validationSpec := spec
		if modelID == "" {
			validationSpec.ModelID = selectedModelID
		}
		if err := exploration.ValidateAgainstModel(model, &validationSpec); err != nil {
			return fmt.Errorf("exploration state: %w; remove it from the URL or choose an active field", err)
		}
	}

	datasetID := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID))
	if datasetID != "" {
		if !explorerDatasetByID(projection.Datasets, datasetID) || !compiledDataset(compiled, datasetID) {
			return fmt.Errorf("dataset %q is no longer available in model %q; choose an active dataset", datasetID, selectedModelID)
		}
	}

	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(projection.Fields))
	for _, field := range projection.Fields {
		fieldByID[field.ID] = field
	}
	for index, dimension := range spec.Dimensions {
		if dimension.Grain == nil {
			continue
		}
		field, err := restoredExploreField(dimension.Field, fmt.Sprintf("dimension %d", index+1), "dimension", fieldByID)
		if err != nil {
			return err
		}
		grain := string(*dimension.Grain)
		if !restoredTimeGrain(grain) {
			return fmt.Errorf("dimension field %q uses unsupported grain %q; choose a supported time grain", dimension.Field, grain)
		}
		if declared, supported := restoredCompiledSemanticTimeGrain(compiled, dimension.Field, grain); declared && !supported {
			return fmt.Errorf("dimension field %q does not support grain %q in the active semantic model; choose a supported grain", dimension.Field, grain)
		}
		fieldType := strings.ToLower(strings.TrimSpace(projectsignals.ValueOrZero(field.Type)))
		if !isRestoredTemporalFieldType(fieldType) {
			return fmt.Errorf("dimension field %q is not a date or timestamp dimension; choose a temporal field", dimension.Field)
		}
	}
	filterDatasets := restoredFilterDatasetParticipation(command, projection, model, fieldByID)
	seenFields := make(map[string]string, len(state.Dimensions)+len(state.Metrics))
	selectedReferences := make(map[string]string, len(spec.Dimensions)+len(spec.Metrics))
	for _, fieldID := range state.Dimensions {
		if err := validateRestoredExploreField(fieldID, "dimension", fieldByID); err != nil {
			return err
		}
		if previous := seenFields[fieldID]; previous != "" {
			return fmt.Errorf("dimension field %q is selected more than once; remove the duplicate from the URL", fieldID)
		}
		seenFields[fieldID] = "dimension"
	}
	for _, dimension := range spec.Dimensions {
		selectedReferences[dimension.Field] = dimension.Field
		if dimension.Alias != nil {
			selectedReferences[*dimension.Alias] = dimension.Field
		}
	}
	for _, fieldID := range state.Metrics {
		if err := validateRestoredExploreField(fieldID, "metric", fieldByID); err != nil {
			return err
		}
		if previous := seenFields[fieldID]; previous != "" {
			return fmt.Errorf("metric field %q is selected more than once; remove the duplicate from the URL", fieldID)
		}
		seenFields[fieldID] = "metric"
	}
	for _, metric := range spec.Metrics {
		selectedReferences[metric.Field] = metric.Field
		if metric.Alias != nil {
			selectedReferences[*metric.Alias] = metric.Field
		}
	}
	// Time-only selections are valid sort targets too. Keep both the authored
	// field and its alias in the same reference map used by dimensions and
	// metrics before validating the ordered sort operands.
	if spec.Time != nil {
		selectedReferences[spec.Time.Field] = spec.Time.Field
		if spec.Time.Alias != nil {
			selectedReferences[*spec.Time.Alias] = spec.Time.Field
		}
	}
	for index, filter := range state.Filters {
		_, err := restoredExploreField(filter.Field, "filter", "dimension", fieldByID)
		if err != nil {
			return fmt.Errorf("filter %d: %w", index+1, err)
		}
		if err := validateRestoredExploreFilter(index, filter); err != nil {
			return err
		}
		if filter.Dataset == nil || strings.TrimSpace(projectsignals.ValueOrZero(filter.Dataset)) == "" {
			if filter.Dataset != nil {
				return fmt.Errorf("filter %d dataset is empty; remove the stale filter or choose an active dataset", index+1)
			}
		} else {
			filterDataset := strings.TrimSpace(projectsignals.ValueOrZero(filter.Dataset))
			if !explorerDatasetByID(projection.Datasets, filterDataset) || !compiledDataset(compiled, filterDataset) {
				return fmt.Errorf("filter %d dataset %q is no longer available; remove the stale filter or choose an active dataset", index+1, filterDataset)
			}
			if !filterDatasets[filterDataset] {
				return fmt.Errorf("filter %d dataset %q does not participate in the restored query; choose the active query dataset or a selected metric root", index+1, filterDataset)
			}
		}
	}
	for index, sorting := range state.Sort {
		resolvedSortField := sorting.Field
		if selected, ok := selectedReferences[sorting.Field]; ok {
			resolvedSortField = selected
		}
		if _, err := restoredExploreField(resolvedSortField, fmt.Sprintf("sort %d", index+1), "", fieldByID); err != nil {
			return err
		}
		if sorting.Direction != "asc" && sorting.Direction != "desc" {
			return fmt.Errorf("sort %d uses unsupported direction %q; choose asc or desc", index+1, sorting.Direction)
		}
		if _, selected := selectedReferences[sorting.Field]; !selected {
			return fmt.Errorf("sort %d field %q is not selected; choose a selected dimension or metric", index+1, sorting.Field)
		}
	}
	if state.Time != nil {
		field, err := restoredExploreField(state.Time.Field, "time", "dimension", fieldByID)
		if err != nil {
			return err
		}
		if !restoredTimeGrain(state.Time.Grain) {
			return fmt.Errorf("time field %q uses unsupported grain %q; choose a supported time grain", state.Time.Field, state.Time.Grain)
		}
		if declared, supported := restoredCompiledSemanticTimeGrain(compiled, state.Time.Field, state.Time.Grain); declared && !supported {
			return fmt.Errorf("time field %q does not support grain %q in the active semantic model; choose a supported grain", state.Time.Field, state.Time.Grain)
		}
		fieldType := strings.ToLower(strings.TrimSpace(projectsignals.ValueOrZero(field.Type)))
		if !isRestoredTemporalFieldType(fieldType) {
			return fmt.Errorf("time field %q is not a date or timestamp dimension; choose a temporal field", state.Time.Field)
		}
	}
	return nil
}

// restoredFilterDatasetParticipation returns the datasets that an explicit
// filter scope may legally name after projection has selected/rebased the
// effective query base. A normal query has one participating base. When a
// selected metric spans multiple roots, the semantic executor clears the
// dataset target and the complete recursive root union is the safe scope.
func restoredFilterDatasetParticipation(command projectsignals.DataExploreCommand, projection DataExplorerProjection, model *semanticmodel.Model, fields map[string]projectsignals.DataExploreFieldSignal) map[string]bool {
	participating := map[string]bool{}
	effectiveDataset := strings.TrimSpace(projectsignals.ValueOrZero(projection.Command.Spec.DatasetID))
	state := dataExploreStateFromSpec(command.Spec)
	if !explorerCommandHasMultiRootMetric(state.Metrics, fields) {
		if effectiveDataset != "" {
			participating[effectiveDataset] = true
		}
		return participating
	}
	for _, metric := range state.Metrics {
		for _, root := range explorerMetricRootDatasets(model, metric) {
			participating[root] = true
		}
	}
	return participating
}

func validateRestoredExploreFilter(index int, filter dataExploreFilter) error {
	operator := strings.TrimSpace(filter.Operator)
	valueCount := len(filter.Values)
	requiresOne := false
	requiresZero := false
	switch operator {
	case "equals", "not_equals", "contains", "not_contains", "starts_with", "ends_with", "greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal":
		requiresOne = true
	case "in", "not_in":
		if valueCount == 0 {
			return fmt.Errorf("filter %d operator %q requires at least one value; update or remove the stale filter", index+1, operator)
		}
	case "unfiltered":
		if valueCount != 0 {
			return fmt.Errorf("filter %d unfiltered expression does not accept values; update or remove the stale filter", index+1)
		}
	case "range":
		// The canonical range bounds are validated and lowered directly from
		// the spec; the compatibility view intentionally carries no values.
	case "is_null", "is_not_null":
		requiresZero = true
	default:
		return fmt.Errorf("filter %d uses unsupported operator %q; choose a supported filter operator", index+1, filter.Operator)
	}
	if requiresOne && valueCount != 1 {
		return fmt.Errorf("filter %d operator %q requires exactly one value; update or remove the stale filter", index+1, operator)
	}
	if requiresZero && valueCount != 0 {
		return fmt.Errorf("filter %d operator %q does not accept values; update or remove the stale filter", index+1, operator)
	}
	return nil
}

// restoredCompiledSemanticTimeGrain mirrors planner resolution: only a
// semantic-dimension reference is subject to that dimension's declared grain
// contract. A physical binding with the same authored semantic dimension is
// validated using the planner's global grain vocabulary and physical type.
func restoredCompiledSemanticTimeGrain(compiled *semanticquery.CompiledModel, field, grain string) (declared, supported bool) {
	if compiled == nil {
		return false, false
	}
	field = strings.TrimSpace(field)
	grain = strings.TrimSpace(grain)
	dimension, ok := compiled.SemanticDimension(field)
	if !ok {
		return false, false
	}
	declared = true
	for _, candidate := range dimension.Grains {
		if strings.TrimSpace(candidate) == grain {
			supported = true
		}
	}
	return declared, supported
}

func explorerModelByID(models []projectsignals.DataExploreModelSignal, id string) bool {
	for _, model := range models {
		if model.ID == id {
			return true
		}
	}
	return false
}

func explorerDatasetByID(datasets []projectsignals.DataExploreDatasetSignal, id string) bool {
	for _, dataset := range datasets {
		if dataset.ID == id {
			return true
		}
	}
	return false
}

func compiledDataset(compiled *semanticquery.CompiledModel, id string) bool {
	if compiled == nil {
		return false
	}
	_, ok := compiled.Dataset(id)
	return ok
}

func validateRestoredExploreField(fieldID, expectedKind string, fields map[string]projectsignals.DataExploreFieldSignal) error {
	_, err := restoredExploreField(fieldID, expectedKind, expectedKind, fields)
	return err
}

func restoredExploreField(fieldID, operand, expectedKind string, fields map[string]projectsignals.DataExploreFieldSignal) (projectsignals.DataExploreFieldSignal, error) {
	fieldID = strings.TrimSpace(fieldID)
	field, ok := fields[fieldID]
	if !ok {
		return projectsignals.DataExploreFieldSignal{}, fmt.Errorf("%s field %q is no longer available in the active semantic model; remove it from the URL or reload the explorer", operand, fieldID)
	}
	if expectedKind != "" && field.Kind != expectedKind {
		return projectsignals.DataExploreFieldSignal{}, fmt.Errorf("%s field %q is a %s, not a %s; choose a %s field", operand, fieldID, field.Kind, expectedKind, expectedKind)
	}
	if !field.Compatible {
		reason := strings.TrimSpace(projectsignals.ValueOrZero(field.CompatibilityReason))
		if reason == "" {
			reason = "it is incompatible with the restored dataset"
		}
		return projectsignals.DataExploreFieldSignal{}, fmt.Errorf("%s field %q is incompatible: %s; choose a compatible field or dataset", operand, fieldID, reason)
	}
	return field, nil
}

func containsRestoredExploreSelection(value string, dimensions, metrics []string) bool {
	for _, selected := range append(append([]string(nil), dimensions...), metrics...) {
		if selected == value {
			return true
		}
	}
	return false
}

func restoredTimeGrain(grain string) bool {
	switch strings.TrimSpace(grain) {
	case "second", "minute", "hour", "day", "week", "month", "quarter", "year":
		return true
	default:
		return false
	}
}
