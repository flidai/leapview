package contractprojection

import (
	"fmt"
	"sort"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

type authoredSemanticFilter struct {
	Field    *string                  `json:"field,omitempty"`
	Operator *string                  `json:"operator,omitempty"`
	Value    any                      `json:"value,omitempty"`
	Path     []string                 `json:"path,omitempty"`
	All      []authoredSemanticFilter `json:"all,omitempty"`
	Any      []authoredSemanticFilter `json:"any,omitempty"`
	Not      *authoredSemanticFilter  `json:"not,omitempty"`
}

func ProjectSemanticModel(value projectcontracts.SemanticModel, contract Contract, contexts ...ReferenceContext) (SemanticModel, error) {
	resolver, err := referenceContextArgument(contexts)
	if err != nil {
		return SemanticModel{}, err
	}
	if err := validateAuthoredResource(configschema.KindSemanticModel, value); err != nil {
		return SemanticModel{}, fmt.Errorf("project SemanticModel: validate authored resource: %w", err)
	}
	var input struct {
		APIVersion string           `json:"apiVersion"`
		Kind       string           `json:"kind"`
		Metadata   authoredMetadata `json:"metadata"`
		Spec       struct {
			Datasets map[string]struct {
				Model                string                 `json:"model"`
				DefaultTimeDimension *string                `json:"defaultTimeDimension,omitempty"`
				RequiredAccessGrants []string               `json:"requiredAccessGrants,omitempty"`
				AccessFilters        []SemanticAccessFilter `json:"accessFilters,omitempty"`
			} `json:"datasets"`
			AccessGrants map[string]struct {
				UserAttribute string `json:"userAttribute"`
				AllowedValues []any  `json:"allowedValues"`
			} `json:"accessGrants,omitempty"`
			Relationships map[string]struct {
				From RelationshipEndpoint `json:"from"`
				To   RelationshipEndpoint `json:"to"`
			} `json:"relationships,omitempty"`
			Dimensions map[string]struct {
				Datatype             string                     `json:"datatype"`
				Time                 *SemanticTime              `json:"time,omitempty"`
				Bindings             map[string]SemanticBinding `json:"bindings"`
				RequiredAccessGrants []string                   `json:"requiredAccessGrants,omitempty"`
			} `json:"dimensions,omitempty"`
			Filters map[string]authoredSemanticFilter `json:"filters,omitempty"`
			Metrics map[string]struct {
				Type        string  `json:"type"`
				Dataset     *string `json:"dataset,omitempty"`
				Aggregation *string `json:"aggregation,omitempty"`
				Input       *struct {
					Field string `json:"field"`
				} `json:"input,omitempty"`
				Where                []string `json:"where,omitempty"`
				Empty                *string  `json:"empty,omitempty"`
				TimeDimension        *string  `json:"timeDimension,omitempty"`
				Expression           *string  `json:"expression,omitempty"`
				Numerator            *string  `json:"numerator,omitempty"`
				Denominator          *string  `json:"denominator,omitempty"`
				Unit                 *string  `json:"unit,omitempty"`
				Format               *string  `json:"format,omitempty"`
				RequiredAccessGrants []string `json:"requiredAccessGrants,omitempty"`
			} `json:"metrics"`
		} `json:"spec"`
	}
	if err := decodeGenerated(value, &input); err != nil {
		return SemanticModel{}, fmt.Errorf("project SemanticModel: %w", err)
	}
	metadata, err := projectMetadata(input.APIVersion, input.Kind, "SemanticModel", input.Metadata, contract)
	if err != nil {
		return SemanticModel{}, err
	}
	result := SemanticModelContract{Datasets: map[string]SemanticDataset{}, Metrics: map[string]SemanticMetric{}}
	for name, value := range input.Spec.Datasets {
		modelName, err := canonicalText(value.Model)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel dataset %q model: %w", name, err)
		}
		resolvedModel, resolveErr := resolveProjectionReference(resolver, modelName, projectgraph.KindModel, fmt.Sprintf("SemanticModel dataset %q model", name))
		if resolveErr != nil {
			return SemanticModel{}, resolveErr
		}
		modelName = resolvedModel
		grants, err := canonicalSet(value.RequiredAccessGrants)
		if err != nil {
			return SemanticModel{}, err
		}
		filters := make([]SemanticAccessFilter, 0, len(value.AccessFilters))
		for _, filter := range value.AccessFilters {
			field, err := canonicalText(filter.Field)
			if err != nil {
				return SemanticModel{}, err
			}
			attribute, err := canonicalText(filter.UserAttribute)
			if err != nil {
				return SemanticModel{}, err
			}
			filters = append(filters, SemanticAccessFilter{Field: field, UserAttribute: attribute})
		}
		sort.Slice(filters, func(i, j int) bool {
			return filters[i].Field+"\x00"+filters[i].UserAttribute < filters[j].Field+"\x00"+filters[j].UserAttribute
		})
		filters = dedupeAccessFilters(filters)
		defaultTimeDimension, err := canonicalTextPointer(value.DefaultTimeDimension)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel dataset %q default time dimension: %w", name, err)
		}
		dataset := SemanticDataset{Model: modelName, DefaultTimeDimension: defaultTimeDimension}
		if len(grants) > 0 {
			dataset.RequiredAccessGrants = &grants
		}
		if len(filters) > 0 {
			dataset.AccessFilters = &filters
		}
		result.Datasets[name] = dataset
	}
	if len(input.Spec.AccessGrants) > 0 {
		values := make(map[string]SemanticAccessGrant, len(input.Spec.AccessGrants))
		result.AccessGrants = &values
	}
	for name, value := range input.Spec.AccessGrants {
		values, err := canonicalAllowedValues(value.AllowedValues)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project access grant %q: %w", name, err)
		}
		(*result.AccessGrants)[name] = SemanticAccessGrant{UserAttribute: value.UserAttribute, AllowedValues: values}
	}
	if len(input.Spec.Relationships) > 0 {
		values := make(map[string]SemanticRelationship, len(input.Spec.Relationships))
		result.Relationships = &values
	}
	for name, value := range input.Spec.Relationships {
		from, err := projectRelationshipEndpoint(value.From)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel relationship %q from: %w", name, err)
		}
		to, err := projectRelationshipEndpoint(value.To)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel relationship %q to: %w", name, err)
		}
		(*result.Relationships)[name] = SemanticRelationship{From: from, To: to}
	}
	if len(input.Spec.Dimensions) > 0 {
		values := make(map[string]SemanticDimension, len(input.Spec.Dimensions))
		result.Dimensions = &values
	}
	for name, value := range input.Spec.Dimensions {
		grants, err := canonicalSet(value.RequiredAccessGrants)
		if err != nil {
			return SemanticModel{}, err
		}
		timeSemantics, err := projectSemanticTime(value.Time)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel dimension %q time semantics: %w", name, err)
		}
		bindings, err := projectSemanticBindings(value.Bindings)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel dimension %q bindings: %w", name, err)
		}
		datatype, err := canonicalText(value.Datatype)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel dimension %q datatype: %w", name, err)
		}
		dimension := SemanticDimension{Datatype: datatype, Time: timeSemantics, Bindings: bindings}
		if len(grants) > 0 {
			dimension.RequiredAccessGrants = &grants
		}
		(*result.Dimensions)[name] = dimension
	}
	if len(input.Spec.Filters) > 0 {
		values := make(map[string]SemanticFilter, len(input.Spec.Filters))
		result.Filters = &values
	}
	for name, value := range input.Spec.Filters {
		projected, err := projectSemanticFilter(value, input.Spec.Dimensions)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel filter %q: %w", name, err)
		}
		(*result.Filters)[name] = projected
	}
	for name, value := range input.Spec.Metrics {
		metric := SemanticMetric{}
		metric.Type, err = canonicalText(value.Type)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q type: %w", name, err)
		}
		metric.Dataset, err = canonicalTextPointer(value.Dataset)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q dataset: %w", name, err)
		}
		metric.Aggregation, err = canonicalTextPointer(value.Aggregation)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q aggregation: %w", name, err)
		}
		metric.Empty, err = canonicalTextPointer(value.Empty)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q empty: %w", name, err)
		}
		if metric.Type == "aggregate" && metric.Empty == nil && metric.Aggregation != nil {
			defaultEmpty := "null"
			if *metric.Aggregation == "count" || *metric.Aggregation == "count_distinct" {
				defaultEmpty = "zero"
			}
			metric.Empty = &defaultEmpty
		}
		metric.TimeDimension, err = canonicalTextPointer(value.TimeDimension)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q time dimension: %w", name, err)
		}
		metric.Expression, err = canonicalTextPointer(value.Expression)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q expression: %w", name, err)
		}
		metric.Numerator, err = canonicalTextPointer(value.Numerator)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q numerator: %w", name, err)
		}
		metric.Denominator, err = canonicalTextPointer(value.Denominator)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q denominator: %w", name, err)
		}
		metric.Unit, err = canonicalTextPointer(value.Unit)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q unit: %w", name, err)
		}
		metric.Format, err = canonicalTextPointer(value.Format)
		if err != nil {
			return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q format: %w", name, err)
		}
		if value.Input != nil {
			field, fieldErr := canonicalText(value.Input.Field)
			if fieldErr != nil {
				return SemanticModel{}, fmt.Errorf("project SemanticModel metric %q input: %w", name, fieldErr)
			}
			metric.Input = &projectcontracts.ContractProjectionSemanticMetricInput{Field: field}
		}
		if len(value.Where) > 0 {
			where, err := canonicalSet(value.Where)
			if err != nil {
				return SemanticModel{}, err
			}
			metric.Where = &where
		}
		grants, err := canonicalSet(value.RequiredAccessGrants)
		if err != nil {
			return SemanticModel{}, err
		}
		if len(grants) > 0 {
			metric.RequiredAccessGrants = &grants
		}
		result.Metrics[name] = metric
	}
	return SemanticModel{payload: projectcontracts.SemanticModelContractProjection{Profile: Profile, APIVersion: input.APIVersion, Kind: input.Kind, Metadata: metadata, Contract: result}}, nil
}

func projectSemanticFilter(value authoredSemanticFilter, dimensions map[string]struct {
	Datatype             string                     `json:"datatype"`
	Time                 *SemanticTime              `json:"time,omitempty"`
	Bindings             map[string]SemanticBinding `json:"bindings"`
	RequiredAccessGrants []string                   `json:"requiredAccessGrants,omitempty"`
}) (SemanticFilter, error) {
	field, err := canonicalTextPointer(value.Field)
	if err != nil {
		return SemanticFilter{}, fmt.Errorf("field: %w", err)
	}
	operator, err := canonicalTextPointer(value.Operator)
	if err != nil {
		return SemanticFilter{}, fmt.Errorf("operator: %w", err)
	}
	canonicalPath, err := canonicalTexts(value.Path)
	if err != nil {
		return SemanticFilter{}, fmt.Errorf("path: %w", err)
	}
	var path *[]string
	if len(canonicalPath) > 0 {
		path = &canonicalPath
	}
	result := SemanticFilter{Field: field, Operator: operator, Path: path}
	datatype := ""
	if value.Field != nil {
		for _, dimension := range dimensions {
			for _, binding := range dimension.Bindings {
				if binding.Field != *value.Field {
					continue
				}
				if datatype != "" && datatype != dimension.Datatype {
					return SemanticFilter{}, fmt.Errorf("field %q has conflicting binding datatypes", *value.Field)
				}
				datatype = dimension.Datatype
			}
		}
		if datatype == "" {
			return SemanticFilter{}, fmt.Errorf("field %q has no authoritative typed dimension binding", *value.Field)
		}
	}
	if value.Value != nil {
		switch values := value.Value.(type) {
		case []any:
			if operator == nil || *operator != "in" && *operator != "not_in" {
				return SemanticFilter{}, fmt.Errorf("array filter values are only admitted for in/not_in operators")
			}
			projected, err := canonicalAllowedValuesTyped(values, datatype, true)
			if err != nil {
				return SemanticFilter{}, err
			}
			result.Values = &projected
		default:
			canonical, err := canonicalLiteral(datatype, values)
			if err != nil {
				return SemanticFilter{}, err
			}
			result.Value = &canonical
		}
	}
	for _, child := range value.All {
		projected, err := projectSemanticFilter(child, dimensions)
		if err != nil {
			return SemanticFilter{}, err
		}
		if result.All == nil {
			values := []SemanticFilter{}
			result.All = &values
		}
		*result.All = append(*result.All, projected)
	}
	for _, child := range value.Any {
		projected, err := projectSemanticFilter(child, dimensions)
		if err != nil {
			return SemanticFilter{}, err
		}
		if result.Any == nil {
			values := []SemanticFilter{}
			result.Any = &values
		}
		*result.Any = append(*result.Any, projected)
	}
	if value.Not != nil {
		projected, err := projectSemanticFilter(*value.Not, dimensions)
		if err != nil {
			return SemanticFilter{}, err
		}
		result.Not = &projected
	}
	return result, nil
}

func projectRelationshipEndpoint(value RelationshipEndpoint) (RelationshipEndpoint, error) {
	dataset, err := canonicalText(value.Dataset)
	if err != nil {
		return RelationshipEndpoint{}, err
	}
	entity, err := canonicalTextPointer(value.Entity)
	if err != nil {
		return RelationshipEndpoint{}, err
	}
	fields, err := canonicalTextsPointer(value.Fields)
	if err != nil {
		return RelationshipEndpoint{}, err
	}
	return RelationshipEndpoint{Dataset: dataset, Entity: entity, Fields: fields}, nil
}

func projectSemanticTime(value *SemanticTime) (*SemanticTime, error) {
	if value == nil {
		return nil, nil
	}
	grains, err := canonicalSet(value.Grains)
	if err != nil {
		return nil, err
	}
	nativeGrain, err := canonicalText(value.NativeGrain)
	if err != nil {
		return nil, err
	}
	calendar, err := canonicalTextPointer(value.Calendar)
	if err != nil {
		return nil, err
	}
	if calendar == nil || *calendar == "" {
		defaultCalendar := "gregorian"
		calendar = &defaultCalendar
	}
	timezone, err := canonicalTextPointer(value.Timezone)
	if err != nil {
		return nil, err
	}
	if timezone == nil || *timezone == "" {
		defaultTimezone := "UTC"
		timezone = &defaultTimezone
	}
	return &SemanticTime{NativeGrain: nativeGrain, Grains: grains, Calendar: calendar, Timezone: timezone}, nil
}

func projectSemanticBindings(values map[string]SemanticBinding) (map[string]SemanticBinding, error) {
	result := make(map[string]SemanticBinding, len(values))
	for name, value := range values {
		field, err := canonicalText(value.Field)
		if err != nil {
			return nil, err
		}
		path, err := canonicalTextsPointer(value.Path)
		if err != nil {
			return nil, err
		}
		result[name] = SemanticBinding{Field: field, Path: path}
	}
	return result, nil
}

func dedupeAccessFilters(values []SemanticAccessFilter) []SemanticAccessFilter {
	result := values[:0]
	var previous string
	for index, value := range values {
		key := value.Field + "\x00" + value.UserAttribute
		if index > 0 && key == previous {
			continue
		}
		previous = key
		result = append(result, value)
	}
	return result
}
