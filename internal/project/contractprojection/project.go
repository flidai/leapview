package contractprojection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

type authoredMetadata struct {
	ID       string                             `json:"id"`
	Name     string                             `json:"name"`
	Contract *projectcontracts.ContractMetadata `json:"contract,omitempty"`
}

type authoredField struct {
	Datatype                 *string                                     `json:"datatype,omitempty"`
	Nullable                 *bool                                       `json:"nullable,omitempty"`
	CriticalDataElement      *bool                                       `json:"criticalDataElement,omitempty"`
	Classification           *string                                     `json:"classification,omitempty"`
	AuthoritativeDefinitions *[]projectcontracts.AuthoritativeDefinition `json:"authoritativeDefinitions,omitempty"`
	Deprecation              *projectcontracts.FieldDeprecation          `json:"deprecation,omitempty"`
}

type authoredDuration struct {
	Amount int64  `json:"amount"`
	Unit   string `json:"unit"`
}

func ProjectSource(value projectcontracts.Source, contract Contract) (Source, error) {
	var input struct {
		APIVersion string           `json:"apiVersion"`
		Kind       string           `json:"kind"`
		Metadata   authoredMetadata `json:"metadata"`
		Spec       struct {
			Schema    json.RawMessage `json:"schema,omitempty"`
			Freshness json.RawMessage `json:"freshness,omitempty"`
		} `json:"spec"`
	}
	if err := decodeGenerated(value, &input); err != nil {
		return Source{}, fmt.Errorf("project Source: %w", err)
	}
	metadata, err := projectMetadata(input.APIVersion, input.Kind, "Source", input.Metadata, contract)
	if err != nil {
		return Source{}, err
	}
	schema, err := projectSourceSchema(input.Spec.Schema)
	if err != nil {
		return Source{}, err
	}
	freshness, err := projectSourceFreshness(input.Spec.Freshness)
	if err != nil {
		return Source{}, err
	}
	return Source{payload: projectcontracts.SourceContractProjection{Profile: Profile, APIVersion: input.APIVersion, Kind: input.Kind, Metadata: metadata, Contract: SourceContract{Schema: schema, Freshness: freshness}}}, nil
}

func projectSourceSchema(raw json.RawMessage) (SourceSchema, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return SourceSchema{Mode: "inferred"}, nil
	}
	var input struct {
		Mode   string                   `json:"mode"`
		Fields map[string]authoredField `json:"fields,omitempty"`
	}
	if err := decodeJSON(raw, &input); err != nil {
		return SourceSchema{}, fmt.Errorf("project Source schema: %w", err)
	}
	mode, err := canonicalText(input.Mode)
	if err != nil {
		return SourceSchema{}, fmt.Errorf("project Source schema mode: %w", err)
	}
	result := SourceSchema{Mode: mode}
	if len(input.Fields) > 0 {
		fields := make(map[string]Field, len(input.Fields))
		for name, value := range input.Fields {
			field, err := projectField(value)
			if err != nil {
				return SourceSchema{}, fmt.Errorf("project Source field %q: %w", name, err)
			}
			fields[name] = field
		}
		result.Fields = &fields
	}
	return result, nil
}

func projectSourceFreshness(raw json.RawMessage) (*SourceFreshness, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var input struct {
		Basis        string            `json:"basis"`
		Field        *string           `json:"field,omitempty"`
		Revision     *string           `json:"revision,omitempty"`
		WarningAfter *authoredDuration `json:"warningAfter,omitempty"`
		ErrorAfter   *authoredDuration `json:"errorAfter,omitempty"`
	}
	if err := decodeJSON(raw, &input); err != nil {
		return nil, fmt.Errorf("project Source freshness: %w", err)
	}
	result := &SourceFreshness{Basis: input.Basis, WarningAfter: projectDuration(input.WarningAfter), ErrorAfter: projectDuration(input.ErrorAfter)}
	var err error
	result.Basis, err = canonicalText(result.Basis)
	if err != nil {
		return nil, err
	}
	if input.Field != nil {
		result.Field, err = canonicalTextPointer(input.Field)
		if err != nil {
			return nil, err
		}
	}
	if input.Revision != nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, *input.Revision)
		if parseErr != nil {
			return nil, fmt.Errorf("project Source freshness revision: %w", parseErr)
		}
		revision := parsed.UTC().Format(time.RFC3339Nano)
		result.Revision = &revision
	}
	return result, nil
}

func ProjectModel(value projectcontracts.Model, contract Contract) (Model, error) {
	var input struct {
		APIVersion string           `json:"apiVersion"`
		Kind       string           `json:"kind"`
		Metadata   authoredMetadata `json:"metadata"`
		Spec       struct {
			Definition struct {
				Type   string  `json:"type"`
				Source *string `json:"source,omitempty"`
				SQL    *string `json:"sql,omitempty"`
			} `json:"definition"`
			Entities map[string]struct {
				Type   string   `json:"type"`
				Fields []string `json:"fields"`
			} `json:"entities"`
			Grain struct {
				Entity string `json:"entity"`
			} `json:"grain"`
			Fields map[string]authoredField `json:"fields,omitempty"`
			Checks []struct {
				ID       string   `json:"id"`
				Type     string   `json:"type"`
				Field    *string  `json:"field,omitempty"`
				Fields   []string `json:"fields,omitempty"`
				Values   []string `json:"values,omitempty"`
				To       *string  `json:"to,omitempty"`
				Minimum  *int64   `json:"minimum,omitempty"`
				Maximum  *int64   `json:"maximum,omitempty"`
				Severity *string  `json:"severity,omitempty"`
			} `json:"checks,omitempty"`
		} `json:"spec"`
	}
	if err := decodeGenerated(value, &input); err != nil {
		return Model{}, fmt.Errorf("project Model: %w", err)
	}
	metadata, err := projectMetadata(input.APIVersion, input.Kind, "Model", input.Metadata, contract)
	if err != nil {
		return Model{}, err
	}
	definition := ModelDefinition{Type: input.Spec.Definition.Type}
	definition.Type, err = canonicalText(definition.Type)
	if err != nil {
		return Model{}, err
	}
	definition.Source, err = canonicalTextPointer(input.Spec.Definition.Source)
	if err != nil {
		return Model{}, err
	}
	if input.Spec.Definition.SQL != nil {
		definition.SQLAst, err = canonicalModelSQL(*input.Spec.Definition.SQL)
		if err != nil {
			return Model{}, fmt.Errorf("project Model SQL: %w", err)
		}
	}
	entities := make(map[string]ModelEntity, len(input.Spec.Entities))
	for name, value := range input.Spec.Entities {
		kind, err := canonicalText(value.Type)
		if err != nil {
			return Model{}, fmt.Errorf("project Model entity %q: %w", name, err)
		}
		fields, err := canonicalTexts(value.Fields)
		if err != nil {
			return Model{}, fmt.Errorf("project Model entity %q: %w", name, err)
		}
		entities[name] = ModelEntity{Type: kind, Fields: fields}
	}
	fields := make(map[string]Field, len(input.Spec.Fields))
	for name, value := range input.Spec.Fields {
		field, err := projectField(value)
		if err != nil {
			return Model{}, fmt.Errorf("project Model field %q: %w", name, err)
		}
		fields[name] = field
	}
	checks := make([]ModelCheck, len(input.Spec.Checks))
	for index, value := range input.Spec.Checks {
		check := ModelCheck{ID: value.ID, Type: value.Type, Minimum: value.Minimum, Maximum: value.Maximum}
		check.ID, err = canonicalText(check.ID)
		if err != nil {
			return Model{}, err
		}
		check.Type, err = canonicalText(check.Type)
		if err != nil {
			return Model{}, err
		}
		check.Field, err = canonicalTextPointer(value.Field)
		if err != nil {
			return Model{}, err
		}
		check.To, err = canonicalTextPointer(value.To)
		if err != nil {
			return Model{}, err
		}
		check.Severity, err = canonicalTextPointer(value.Severity)
		if err != nil {
			return Model{}, err
		}
		projectedFields, err := canonicalTexts(value.Fields)
		if err != nil {
			return Model{}, err
		}
		if len(projectedFields) > 0 {
			check.Fields = &projectedFields
		}
		projectedValues, err := canonicalSet(value.Values)
		if err != nil {
			return Model{}, err
		}
		if len(projectedValues) > 0 {
			check.Values = &projectedValues
		}
		checks[index] = check
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	for index := 1; index < len(checks); index++ {
		if checks[index-1].ID == checks[index].ID {
			return Model{}, fmt.Errorf("project Model: duplicate canonical check ID %q", checks[index].ID)
		}
	}
	grain, err := canonicalText(input.Spec.Grain.Entity)
	if err != nil {
		return Model{}, err
	}
	body := ModelContract{Definition: definition, Entities: entities, Grain: ModelGrain{Entity: grain}, Fields: fields}
	if len(checks) > 0 {
		body.Checks = &checks
	}
	return Model{payload: projectcontracts.ModelContractProjection{Profile: Profile, APIVersion: input.APIVersion, Kind: input.Kind, Metadata: metadata, Contract: body}}, nil
}

type authoredSemanticFilter struct {
	Field    *string                  `json:"field,omitempty"`
	Operator *string                  `json:"operator,omitempty"`
	Value    any                      `json:"value,omitempty"`
	Path     []string                 `json:"path,omitempty"`
	All      []authoredSemanticFilter `json:"all,omitempty"`
	Any      []authoredSemanticFilter `json:"any,omitempty"`
	Not      *authoredSemanticFilter  `json:"not,omitempty"`
}

func ProjectSemanticModel(value projectcontracts.SemanticModel, contract Contract) (SemanticModel, error) {
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
		dataset := SemanticDataset{Model: value.Model, DefaultTimeDimension: value.DefaultTimeDimension}
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
		(*result.Relationships)[name] = SemanticRelationship{From: value.From, To: value.To}
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
		dimension := SemanticDimension{Datatype: value.Datatype, Time: timeSemantics, Bindings: bindings}
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
		metric := SemanticMetric{Type: value.Type, Dataset: value.Dataset, Aggregation: value.Aggregation, Empty: value.Empty, TimeDimension: value.TimeDimension, Expression: value.Expression, Numerator: value.Numerator, Denominator: value.Denominator, Unit: value.Unit, Format: value.Format}
		if value.Input != nil {
			field := value.Input.Field
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
	result := SemanticFilter{Field: value.Field, Operator: value.Operator}
	if len(value.Path) > 0 {
		path := append([]string(nil), value.Path...)
		result.Path = &path
	}
	datatype := ""
	if value.Field != nil {
		name := *value.Field
		if index := strings.LastIndexByte(name, '.'); index >= 0 {
			name = name[index+1:]
		}
		if dimension, ok := dimensions[name]; ok {
			datatype = dimension.Datatype
		}
	}
	if value.Value != nil {
		switch values := value.Value.(type) {
		case []any:
			projected := make([]CanonicalValue, 0, len(values))
			seen := map[string]struct{}{}
			for _, item := range values {
				canonical, err := canonicalLiteral(datatype, item)
				if err != nil {
					return SemanticFilter{}, err
				}
				key := canonicalValueKey(canonical)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				projected = append(projected, canonical)
			}
			sort.Slice(projected, func(i, j int) bool { return canonicalValueKey(projected[i]) < canonicalValueKey(projected[j]) })
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
	timezone, err := canonicalTextPointer(value.Timezone)
	if err != nil {
		return nil, err
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

func dedupeAuthoritativeDefinitions(values []AuthoritativeDefinition) []AuthoritativeDefinition {
	result := values[:0]
	var previous string
	for index, value := range values {
		key := value.Type + "\x00" + value.URL
		if index > 0 && key == previous {
			continue
		}
		previous = key
		result = append(result, value)
	}
	return result
}

func projectMetadata(apiVersion, kind, wantKind string, authored authoredMetadata, contract Contract) (Metadata, error) {
	if apiVersion != "leapview.dev/v1" || kind != wantKind {
		return Metadata{}, fmt.Errorf("project %s: envelope is %q %q", wantKind, apiVersion, kind)
	}
	if contract.Version == "" || contract.Compatibility == "" {
		return Metadata{}, fmt.Errorf("project %s: contract version and compatibility are required", wantKind)
	}
	if !semanticVersionPattern.MatchString(contract.Version) {
		return Metadata{}, fmt.Errorf("project %s: contract version %q is not semantic versioning 2.0.0", wantKind, contract.Version)
	}
	if contract.Compatibility != "backward" {
		return Metadata{}, fmt.Errorf("project %s: unsupported compatibility %q", wantKind, contract.Compatibility)
	}
	if authored.Contract != nil && (authored.Contract.Version != contract.Version || authored.Contract.Compatibility != contract.Compatibility) {
		return Metadata{}, fmt.Errorf("project %s: supplied contract metadata does not match generated resource metadata", wantKind)
	}
	id, err := canonicalText(authored.ID)
	if err != nil {
		return Metadata{}, err
	}
	name, err := canonicalText(authored.Name)
	if err != nil {
		return Metadata{}, err
	}
	version, err := canonicalText(contract.Version)
	if err != nil {
		return Metadata{}, err
	}
	compatibility, err := canonicalText(contract.Compatibility)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{ID: id, Name: name, Contract: Contract{Version: version, Compatibility: compatibility}}, nil
}

func projectField(value authoredField) (Field, error) {
	result := Field{Nullable: value.Nullable, CriticalDataElement: value.CriticalDataElement}
	var err error
	result.Datatype, err = canonicalTextPointer(value.Datatype)
	if err != nil {
		return Field{}, err
	}
	result.Classification, err = canonicalTextPointer(value.Classification)
	if err != nil {
		return Field{}, err
	}
	if value.AuthoritativeDefinitions != nil {
		definitions := make([]AuthoritativeDefinition, 0, len(*value.AuthoritativeDefinitions))
		for _, definition := range *value.AuthoritativeDefinitions {
			typeName, err := canonicalText(definition.Type)
			if err != nil {
				return Field{}, err
			}
			url, err := canonicalURL(definition.URL)
			if err != nil {
				return Field{}, err
			}
			definitions = append(definitions, AuthoritativeDefinition{Type: typeName, URL: url})
		}
		sort.Slice(definitions, func(i, j int) bool {
			return definitions[i].Type+"\x00"+definitions[i].URL < definitions[j].Type+"\x00"+definitions[j].URL
		})
		definitions = dedupeAuthoritativeDefinitions(definitions)
		result.AuthoritativeDefinitions = &definitions
	}
	if value.Deprecation != nil {
		since, err := canonicalText(value.Deprecation.Since)
		if err != nil {
			return Field{}, err
		}
		reason, err := canonicalText(value.Deprecation.Reason)
		if err != nil {
			return Field{}, err
		}
		replacement, err := canonicalTextPointer(value.Deprecation.Replacement)
		if err != nil {
			return Field{}, err
		}
		result.Deprecation = &Deprecation{Since: since, Reason: reason, Replacement: replacement}
	}
	return result, nil
}

func projectDuration(value *authoredDuration) *Duration {
	if value == nil {
		return nil
	}
	return &Duration{Amount: value.Amount, Unit: value.Unit}
}

func canonicalAllowedValues(values []any) ([]CanonicalValue, error) {
	result := make([]CanonicalValue, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		canonical, err := canonicalLiteral("", value)
		if err != nil {
			return nil, err
		}
		key := canonicalValueKey(canonical)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, canonical)
	}
	sort.Slice(result, func(i, j int) bool { return canonicalValueKey(result[i]) < canonicalValueKey(result[j]) })
	return result, nil
}

func canonicalLiteral(datatype string, value any) (CanonicalValue, error) {
	typeName := strings.ToLower(datatype)
	semanticType := ""
	switch typeName {
	case "string":
		semanticType = "String"
	case "boolean":
		semanticType = "Boolean"
	case "integer":
		semanticType = "Integer"
	case "decimal":
		semanticType = "Decimal"
	case "date":
		semanticType = "Date"
	case "time":
		semanticType = "Time"
	case "datetime":
		semanticType = "DateTime"
	case "datetimetz":
		semanticType = "Timestamp"
	case "float":
		return CanonicalValue{}, errors.New("approximate Float values are not permitted in contract literals")
	case "opaque":
		return CanonicalValue{}, errors.New("Opaque values have no canonical contract literal representation")
	}
	if semanticType != "" {
		dimensionType := semanticType
		if semanticType == "Timestamp" {
			dimensionType = "DateTimeTz"
		}
		canonical, err := semanticmodel.CoerceSemanticLiteral(value, semanticmodel.MetricDimension{Datatype: semanticmodel.LogicalDataType(dimensionType)})
		if err != nil {
			return CanonicalValue{}, err
		}
		if boolean, ok := canonical.(bool); ok {
			return canonicalBooleanValue(boolean), nil
		}
		return canonicalTextValue(semanticType, fmt.Sprint(canonical)), nil
	}
	switch typed := value.(type) {
	case string:
		text, err := canonicalText(typed)
		return canonicalTextValue("String", text), err
	case bool:
		return canonicalBooleanValue(typed), nil
	case json.Number:
		canonical, err := semanticmodel.CoerceSemanticLiteral(typed, semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeDecimal})
		if err != nil {
			return CanonicalValue{}, err
		}
		return canonicalTextValue("Number", fmt.Sprint(canonical)), nil
	default:
		return CanonicalValue{}, fmt.Errorf("unsupported contract literal %T", value)
	}
}

func canonicalTextValue(typeName, value string) CanonicalValue {
	return CanonicalValue{Value: &projectcontracts.ContractProjectionCanonicalTextValue{Type: typeName, Value: value}}
}

func canonicalBooleanValue(value bool) CanonicalValue {
	return CanonicalValue{Value: &projectcontracts.ContractProjectionCanonicalBooleanValue{Type: "Boolean", Value: value}}
}

func canonicalValueKey(value CanonicalValue) string {
	switch variant := value.Value.(type) {
	case *projectcontracts.ContractProjectionCanonicalTextValue:
		return variant.Type + "\x00" + variant.Value
	case *projectcontracts.ContractProjectionCanonicalBooleanValue:
		return variant.Type + "\x00" + fmt.Sprint(variant.Value)
	default:
		return fmt.Sprintf("%T", value.Value)
	}
}

func decodeGenerated(input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return decodeJSON(data, output)
}

func decodeJSON(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(output)
}
