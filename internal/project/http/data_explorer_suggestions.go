package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

const (
	dataExplorerSuggestionDefaultLimit = int64(25)
	dataExplorerSuggestionMaximumLimit = int64(50)
)

// dataExplorerFilterSuggestions executes one bounded DISTINCT-like semantic
// aggregate. The normal DataQueryExecutor remains the authorization boundary,
// so suggestions cannot reveal values from an unauthorized relationship or
// physical table. It intentionally returns typed canonical filter values.
func dataExplorerFilterSuggestions(ctx context.Context, executor DataQueryExecutor, projectID projectgraph.ResourceID, command projectsignals.DataExploreCommand, fields []projectsignals.DataExploreFieldSignal, compiled *semanticquery.CompiledModel) (signal projectsignals.DataExploreFilterSuggestionsSignal) {
	request := command.FilterSuggestions
	if request == nil {
		return projectsignals.DataExploreFilterSuggestionsSignal{Values: []projectsignals.DataExploreFilterValueSuggestionSignal{}}
	}
	search := strings.TrimSpace(projectsignals.ValueOrZero(request.Search))
	if len(search) > 256 {
		return projectsignals.DataExploreFilterSuggestionsSignal{Field: strings.TrimSpace(request.Field), RequestSeq: command.RequestSeq, SuggestionRequestSeq: request.SuggestionRequestSeq, Error: projectsignals.Pointer("filter suggestion search is limited to 256 characters"), Values: []projectsignals.DataExploreFilterValueSuggestionSignal{}}
	}
	fieldID := strings.TrimSpace(request.Field)
	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(fields))
	for _, field := range fields {
		fieldByID[field.ID] = field
	}
	field, ok := fieldByID[fieldID]
	requestSeq := command.RequestSeq
	signal = projectsignals.DataExploreFilterSuggestionsSignal{Field: fieldID, Values: []projectsignals.DataExploreFilterValueSuggestionSignal{}, RequestSeq: requestSeq, SuggestionRequestSeq: request.SuggestionRequestSeq, Loading: true}
	defer func() {
		// Suggestions are synchronous at this HTTP boundary. Loading is still
		// represented in the contract for stream consumers, but terminal
		// responses must never claim work is pending.
		signal.Loading = false
		if ctx.Err() == context.Canceled {
			signal.Error = nil
			signal.Stale = true
		}
	}()
	if !ok || field.Kind != "dimension" {
		signal.Error = projectsignals.Pointer(fmt.Sprintf("filter suggestion field %q is not an available dimension", fieldID))
		return signal
	}
	signal.Type = field.Type
	if !field.Compatible {
		signal.Error = projectsignals.Pointer(firstExplorerNonEmpty(projectsignals.ValueOrZero(field.CompatibilityReason), "filter suggestion field is incompatible with the selected dataset"))
		return signal
	}
	if executor == nil {
		signal.Error = projectsignals.Pointer("governed filter suggestions are unavailable")
		return signal
	}
	limit := dataExplorerSuggestionDefaultLimit
	if request.Limit != nil {
		limit = *request.Limit
	}
	if limit <= 0 {
		limit = dataExplorerSuggestionDefaultLimit
	}
	if limit > dataExplorerSuggestionMaximumLimit {
		limit = dataExplorerSuggestionMaximumLimit
	}
	target := strings.TrimSpace(projectsignals.ValueOrZero(command.Spec.DatasetID))
	if target == "" {
		target = strings.TrimSpace(field.DatasetID)
	}
	if target == "" {
		signal.Error = projectsignals.Pointer("filter suggestions require an active dataset")
		return signal
	}
	if compiled == nil {
		signal.Error = projectsignals.Pointer("filter suggestions require an active compiled semantic model")
		return signal
	}
	if _, ok := compiled.Dataset(target); !ok {
		signal.Error = projectsignals.Pointer(fmt.Sprintf("filter suggestion dataset %q is unavailable", target))
		return signal
	}
	if err := validateExplorerSuggestionFieldDataset(fieldID, target, compiled); err != nil {
		signal.Error = projectsignals.Pointer(err.Error())
		return signal
	}
	if err := validateExplorerFilterDatasets(command.Spec, fieldByID, compiled); err != nil {
		signal.Error = projectsignals.Pointer(err.Error())
		return signal
	}
	filters, err := lowerSuggestionFilters(command.Spec, fieldByID, fieldID, search)
	if err != nil {
		signal.Error = projectsignals.Pointer(err.Error())
		return signal
	}
	query := dataquery.SemanticAggregate(strings.TrimSpace(command.Spec.ModelID), target, []dataquery.Field{{Field: fieldID}}, nil, filters, []dataquery.Sort{{Field: fieldID, Direction: "asc"}}, 0, int(limit)+1)
	query = query.WithMetadata(dataquery.Metadata{ProjectID: projectID, Surface: dataquery.SurfaceDataExplorer, Operation: dataquery.OperationDataExploreFilterSuggestions, ObjectType: "semantic_dimension", ObjectID: strings.TrimSpace(command.Spec.ModelID) + ":" + fieldID})
	result, err := executor.ExecuteDataQuery(ctx, query)
	if err != nil {
		if ctx.Err() != nil {
			signal.Stale = true
			return signal
		}
		signal.Error = projectsignals.Pointer(err.Error())
		return signal
	}
	if strings.TrimSpace(result.Error) != "" {
		if errors.Is(ctx.Err(), context.Canceled) {
			signal.Stale = true
			return signal
		}
		signal.Error = projectsignals.Pointer(result.Error)
		return signal
	}
	values := make([]projectsignals.DataExploreFilterValueSuggestionSignal, 0, len(result.Rows))
	seen := map[string]struct{}{}
	for _, row := range result.Rows {
		raw, found := suggestionRowValue(row, fieldID)
		if !found || raw == nil {
			continue
		}
		value, label, ok := typedSuggestionValue(raw, projectsignals.ValueOrZero(field.Type))
		if !ok {
			continue
		}
		keyBytes, _ := json.Marshal(value)
		key := string(keyBytes)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, projectsignals.DataExploreFilterValueSuggestionSignal{Label: label, Value: value})
		if int64(len(values)) == limit {
			signal.Truncated = len(result.Rows) > int(limit)
			break
		}
	}
	signal.Values = values
	sortSuggestionValues(signal.Values)
	if len(result.Rows) > int(limit) {
		signal.Truncated = true
	}
	return signal
}

func validateExplorerSuggestionFieldDataset(fieldID, dataset string, compiled *semanticquery.CompiledModel) error {
	if _, ok := compiled.FieldBinding(dataset, fieldID); ok {
		return nil
	}
	if _, ok := compiled.DimensionBinding(fieldID, dataset); ok {
		return nil
	}
	return fmt.Errorf("filter suggestion field %q cannot be reached from dataset %q", fieldID, dataset)
}

func lowerSuggestionFilters(spec exploration.ExplorationSpec, fields map[string]projectsignals.DataExploreFieldSignal, suggestionField, search string) ([]dataquery.Filter, error) {
	retained := make([]exploration.ExplorationFilter, 0, len(spec.Filters))
	for index, authored := range spec.Filters {
		if authored.Field == suggestionField {
			continue
		}
		field, ok := fields[authored.Field]
		if !ok || field.Kind != "dimension" || !field.Compatible {
			return nil, fmt.Errorf("filter %d field %q is unavailable for suggestions", index+1, authored.Field)
		}
		retained = append(retained, authored)
	}
	// lowerExplorationFilters performs the canonical typed lowering and
	// preserves explicit dataset scopes. Lower all retained filters together so
	// spec.Time's range is appended exactly once, including when no authored
	// filters remain after removing the active suggestion field.
	loweredSpec := spec
	loweredSpec.Filters = retained
	filters, err := lowerExplorationFilters(loweredSpec, fields)
	if err != nil {
		return nil, err
	}
	if search == "" {
		return filters, nil
	}
	field, ok := fields[suggestionField]
	if !ok {
		return nil, fmt.Errorf("filter suggestion field %q is unavailable", suggestionField)
	}
	typed, err := suggestionSearchValue(search, projectsignals.ValueOrZero(field.Type))
	if err != nil {
		return nil, err
	}
	operator := "equals"
	if suggestionTypeIsString(projectsignals.ValueOrZero(field.Type)) {
		operator = "contains"
	}
	filters = append(filters, dataquery.Filter{Field: suggestionField, Operator: operator, Values: []any{typed}})
	return filters, nil
}

func suggestionRowValue(row map[string]any, fieldID string) (any, bool) {
	if value, ok := row[fieldID]; ok {
		return value, true
	}
	if dot := strings.LastIndex(fieldID, "."); dot >= 0 {
		if value, ok := row[fieldID[dot+1:]]; ok {
			return value, true
		}
	}
	if len(row) == 1 {
		for _, value := range row {
			return value, true
		}
	}
	return nil, false
}

func typedSuggestionValue(raw any, logicalType string) (exploration.ExplorationFilterValue, string, bool) {
	logicalType = strings.ToLower(strings.TrimSpace(logicalType))
	if value, ok := raw.(time.Time); ok {
		if suggestionTypeIsDateOnly(logicalType) {
			text := value.Format("2006-01-02")
			return exploration.ExplorationFilterValue{Value: &exploration.DateExplorationFilterValue{Kind: "date", Value: text}}, text, true
		}
		if suggestionTypeIsTimestamp(logicalType) || logicalType == "" {
			text := value.UTC().Format(time.RFC3339Nano)
			return exploration.ExplorationFilterValue{Value: &exploration.TimestampExplorationFilterValue{Kind: "timestamp", Value: text}}, text, true
		}
		return exploration.ExplorationFilterValue{}, "", false
	}
	switch value := raw.(type) {
	case bool:
		if logicalType != "" && !suggestionTypeIsBoolean(logicalType) {
			return exploration.ExplorationFilterValue{}, "", false
		}
		text := strconv.FormatBool(value)
		return exploration.ExplorationFilterValue{Value: &exploration.BooleanExplorationFilterValue{Kind: "boolean", Value: value}}, text, true
	case string:
		switch {
		case suggestionTypeIsDateOnly(logicalType):
			if parsed, err := time.Parse("2006-01-02", value); err != nil || parsed.Format("2006-01-02") != value {
				return exploration.ExplorationFilterValue{}, "", false
			}
			return exploration.ExplorationFilterValue{Value: &exploration.DateExplorationFilterValue{Kind: "date", Value: value}}, value, true
		case suggestionTypeIsTimestamp(logicalType):
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return exploration.ExplorationFilterValue{}, "", false
			}
			return exploration.ExplorationFilterValue{Value: &exploration.TimestampExplorationFilterValue{Kind: "timestamp", Value: value}}, value, true
		case suggestionTypeIsBoolean(logicalType):
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return exploration.ExplorationFilterValue{}, "", false
			}
			return exploration.ExplorationFilterValue{Value: &exploration.BooleanExplorationFilterValue{Kind: "boolean", Value: parsed}}, value, true
		case suggestionTypeIsInteger(logicalType) || suggestionTypeIsDecimal(logicalType):
			return typedSuggestionNumber(value, logicalType)
		default:
			// Empty and unknown logical types remain string suggestions. The
			// governed planner still validates the field binding before data is
			// returned, while this keeps labels usable for legacy dimensions.
			return exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: value}}, value, true
		}
	case json.Number:
		return typedSuggestionNumber(value.String(), logicalType)
	case int:
		return typedSuggestionNumber(strconv.Itoa(value), logicalType)
	case int8:
		return typedSuggestionNumber(strconv.FormatInt(int64(value), 10), logicalType)
	case int16:
		return typedSuggestionNumber(strconv.FormatInt(int64(value), 10), logicalType)
	case int32:
		return typedSuggestionNumber(strconv.FormatInt(int64(value), 10), logicalType)
	case int64:
		return typedSuggestionNumber(strconv.FormatInt(value, 10), logicalType)
	case uint:
		return typedSuggestionNumber(strconv.FormatUint(uint64(value), 10), logicalType)
	case uint8:
		return typedSuggestionNumber(strconv.FormatUint(uint64(value), 10), logicalType)
	case uint16:
		return typedSuggestionNumber(strconv.FormatUint(uint64(value), 10), logicalType)
	case uint32:
		return typedSuggestionNumber(strconv.FormatUint(uint64(value), 10), logicalType)
	case uint64:
		return typedSuggestionNumber(strconv.FormatUint(value, 10), logicalType)
	case float32:
		return typedSuggestionNumber(strconv.FormatFloat(float64(value), 'f', -1, 32), logicalType)
	case float64:
		return typedSuggestionNumber(strconv.FormatFloat(value, 'f', -1, 64), logicalType)
	default:
		return exploration.ExplorationFilterValue{}, "", false
	}
}

func typedSuggestionNumber(text, logicalType string) (exploration.ExplorationFilterValue, string, bool) {
	if suggestionTypeIsInteger(logicalType) {
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil || strconv.FormatInt(parsed, 10) != text {
			return exploration.ExplorationFilterValue{}, "", false
		}
		return exploration.ExplorationFilterValue{Value: &exploration.IntegerExplorationFilterValue{Kind: "integer", Value: text}}, text, true
	}
	if !suggestionTypeIsDecimal(logicalType) {
		return exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: text}}, text, true
	}
	if !canonicalSuggestionDecimal(text) {
		return exploration.ExplorationFilterValue{}, "", false
	}
	return exploration.ExplorationFilterValue{Value: &exploration.DecimalExplorationFilterValue{Kind: "decimal", Value: text}}, text, true
}

func canonicalSuggestionDecimal(text string) bool {
	if text == "" {
		return false
	}
	body := text
	if strings.HasPrefix(body, "-") {
		body = body[1:]
	}
	parts := strings.Split(body, ".")
	if len(parts) > 2 || parts[0] == "" {
		return false
	}
	if len(parts[0]) > 1 && strings.HasPrefix(parts[0], "0") {
		return false
	}
	for _, char := range parts[0] {
		if char < '0' || char > '9' {
			return false
		}
	}
	if len(parts) == 2 {
		if parts[1] == "" {
			return false
		}
		for _, char := range parts[1] {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func suggestionSearchValue(search, logicalType string) (any, error) {
	value, _, ok := typedSuggestionValue(search, logicalType)
	if !ok {
		return nil, fmt.Errorf("search value %q is not valid for field type %q", search, logicalType)
	}
	return lowerExplorationFilterValue(value)
}

func suggestionTypeIsString(logicalType string) bool {
	logicalType = strings.ToLower(strings.TrimSpace(logicalType))
	return logicalType == "" || strings.Contains(logicalType, "string") || strings.Contains(logicalType, "text") || strings.Contains(logicalType, "varchar")
}

func suggestionTypeIsBoolean(logicalType string) bool {
	logicalType = strings.ToLower(strings.TrimSpace(logicalType))
	return logicalType == "bool" || strings.Contains(logicalType, "boolean")
}

func suggestionTypeIsInteger(logicalType string) bool {
	return strings.Contains(strings.ToLower(logicalType), "int")
}

func suggestionTypeIsDecimal(logicalType string) bool {
	logicalType = strings.ToLower(logicalType)
	return logicalType == "" || strings.Contains(logicalType, "decimal") || strings.Contains(logicalType, "numeric") || strings.Contains(logicalType, "number") || strings.Contains(logicalType, "float") || strings.Contains(logicalType, "double")
}

func suggestionTypeIsTimestamp(logicalType string) bool {
	return strings.Contains(logicalType, "timestamp") || strings.Contains(logicalType, "datetime")
}

func suggestionTypeIsDate(logicalType string) bool {
	return strings.Contains(logicalType, "date") || suggestionTypeIsTimestamp(logicalType)
}

func suggestionTypeIsDateOnly(logicalType string) bool {
	return suggestionTypeIsDate(logicalType) && !suggestionTypeIsTimestamp(logicalType)
}

// Keep suggestion ordering deterministic when an executor does not preserve
// the requested sort (test doubles and remote adapters may not).
func sortSuggestionValues(values []projectsignals.DataExploreFilterValueSuggestionSignal) {
	sort.SliceStable(values, func(i, j int) bool { return values[i].Label < values[j].Label })
}
