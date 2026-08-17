package ossie

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// The schema is vendored from apache/ossie at InspectedCommit. Keeping the
// exact schema beside the adapter makes validation independent of the network.
//
//go:embed schema/osi-schema.json
var officialSchema []byte

const officialSchemaURL = "https://github.com/apache/ossie/core-spec/osi-schema.json"

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

func official() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		var raw any
		if err := json.Unmarshal(officialSchema, &raw); err != nil {
			schemaErr = fmt.Errorf("ossie official schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(officialSchemaURL, raw); err != nil {
			schemaErr = fmt.Errorf("ossie official schema resource: %w", err)
			return
		}
		schema, schemaErr = compiler.Compile(officialSchemaURL)
	})
	return schema, schemaErr
}

// OfficialSchema returns a copy of the pinned Apache schema for tooling and
// fixture tests.
func OfficialSchema() []byte { return append([]byte(nil), officialSchema...) }

// Validate validates JSON or YAML bytes against Apache Ossie's official
// 0.2.0.dev0 schema. It deliberately runs before Import can construct any
// native object.
func Validate(data []byte) error {
	value, err := yamlJSON(data)
	if err != nil {
		return fmt.Errorf("ossie document: %w", err)
	}
	sch, err := official()
	if err != nil {
		return err
	}
	if err := sch.Validate(value); err != nil {
		return fmt.Errorf("ossie official schema validation: %w", err)
	}
	return nil
}

func yamlJSON(data []byte) (any, error) {
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return normalizeYAML(value)
}

func normalizeYAML(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("mapping key %v is not a string", key)
			}
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			out[name] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	default:
		return value, nil
	}
}

type extensionPayload struct {
	Version       string                                         `json:"version"`
	Description   string                                         `json:"description,omitempty"`
	AIContext     *semanticmodel.AIContext                       `json:"aiContext,omitempty"`
	Datasets      map[string]semanticmodel.SemanticDatasetSpec   `json:"datasets,omitempty"`
	Relationships map[string]semanticmodel.RelationshipSpec      `json:"relationships,omitempty"`
	Dimensions    map[string]semanticmodel.SemanticDimensionSpec `json:"dimensions,omitempty"`
	Filters       map[string]semanticmodel.SemanticFilterSpec    `json:"filters,omitempty"`
	Metrics       map[string]semanticmodel.Metric                `json:"metrics,omitempty"`
}

// model types predate JSON tags and intentionally use YAML authoring names.
// Marshal the extension members through their YAML tags so the structured
// payload remains readable and stable (for example, `empty`, not `Empty`).
func (p extensionPayload) MarshalJSON() ([]byte, error) {
	type wire struct {
		Version       string          `json:"version"`
		Description   string          `json:"description,omitempty"`
		AIContext     json.RawMessage `json:"aiContext,omitempty"`
		Datasets      json.RawMessage `json:"datasets,omitempty"`
		Relationships json.RawMessage `json:"relationships,omitempty"`
		Dimensions    json.RawMessage `json:"dimensions,omitempty"`
		Filters       json.RawMessage `json:"filters,omitempty"`
		Metrics       json.RawMessage `json:"metrics,omitempty"`
	}
	result := wire{Version: p.Version, Description: p.Description}
	var err error
	if p.AIContext != nil {
		result.AIContext, err = yamlTaggedJSON(p.AIContext)
		if err != nil {
			return nil, err
		}
	}
	for target, value := range map[*json.RawMessage]any{
		&result.Datasets: p.Datasets, &result.Relationships: p.Relationships,
		&result.Dimensions: p.Dimensions, &result.Filters: p.Filters, &result.Metrics: p.Metrics,
	} {
		if value == nil {
			continue
		}
		*target, err = yamlTaggedJSON(value)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(result)
}

func (p *extensionPayload) UnmarshalJSON(data []byte) error {
	type wire struct {
		Version       string          `json:"version"`
		Description   string          `json:"description"`
		AIContext     json.RawMessage `json:"aiContext"`
		Datasets      json.RawMessage `json:"datasets"`
		Relationships json.RawMessage `json:"relationships"`
		Dimensions    json.RawMessage `json:"dimensions"`
		Filters       json.RawMessage `json:"filters"`
		Metrics       json.RawMessage `json:"metrics"`
	}
	var source wire
	if err := decodeStrictJSON(data, &source); err != nil {
		return err
	}
	p.Version, p.Description = source.Version, source.Description
	if len(source.AIContext) > 0 {
		var context semanticmodel.AIContext
		if err := decodeStrictYAML(source.AIContext, &context); err != nil {
			return err
		}
		p.AIContext = &context
	}
	if err := unmarshalExtensionMember(source.Datasets, &p.Datasets); err != nil {
		return fmt.Errorf("datasets: %w", err)
	}
	if err := unmarshalExtensionMember(source.Relationships, &p.Relationships); err != nil {
		return fmt.Errorf("relationships: %w", err)
	}
	if err := unmarshalExtensionMember(source.Dimensions, &p.Dimensions); err != nil {
		return fmt.Errorf("dimensions: %w", err)
	}
	if err := unmarshalExtensionMember(source.Filters, &p.Filters); err != nil {
		return fmt.Errorf("filters: %w", err)
	}
	if err := validateRawMetricTags(source.Metrics); err != nil {
		return err
	}
	if err := unmarshalExtensionMember(source.Metrics, &p.Metrics); err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	if err := validateExtensionMetricTags(p.Metrics); err != nil {
		return err
	}
	return nil
}

func unmarshalExtensionMember(data json.RawMessage, target any) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return decodeStrictYAML(data, target)
}

// decodeStrictJSON rejects unknown fields in the extension envelope. A
// regular json.Unmarshal would silently ignore a misspelled extension member,
// making an export appear portable while dropping result-affecting behavior.
func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("trailing JSON value")
	} else if err != io.EOF {
		return fmt.Errorf("trailing JSON data: %w", err)
	}
	return nil
}

// decodeStrictYAML applies the native YAML tags while retaining strict field
// checking for nested extension members. Extension data is JSON on the Ossie
// wire, but the native semantic contract is authored with YAML names such as
// timeDimension and aiContext.
func decodeStrictYAML(data []byte, target any) error {
	normalized, err := yamlJSON(data)
	if err != nil {
		return err
	}
	encoded, err := yaml.Marshal(normalized)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("trailing YAML value")
	} else if err != io.EOF {
		return fmt.Errorf("trailing YAML data: %w", err)
	}
	return nil
}

// validateExtensionMetricTags enforces the native metric tagged union at the
// interchange boundary. The native project schema gets this guarantee from
// CUE, while extension JSON is decoded independently and therefore needs the
// same explicit rejection here. Fields from another metric tag are never
// ignored and cannot be used to smuggle contradictory executable behavior.
func validateExtensionMetricTags(metrics map[string]semanticmodel.Metric) error {
	for name, metric := range metrics {
		var contradictory string
		switch metric.Type {
		case "aggregate":
			switch {
			case metric.Expression != "":
				contradictory = "expression"
			case metric.Numerator != "":
				contradictory = "numerator"
			case metric.Denominator != "":
				contradictory = "denominator"
			}
		case "derived":
			switch {
			case metric.Dataset != "":
				contradictory = "dataset"
			case metric.Aggregation != "":
				contradictory = "aggregation"
			case metric.Input != nil:
				contradictory = "input"
			case len(metric.Where) > 0:
				contradictory = "where"
			case metric.Empty != "":
				contradictory = "empty"
			case metric.TimeDimension != "":
				contradictory = "timeDimension"
			case metric.Numerator != "":
				contradictory = "numerator"
			case metric.Denominator != "":
				contradictory = "denominator"
			}
		case "ratio":
			switch {
			case metric.Dataset != "":
				contradictory = "dataset"
			case metric.Aggregation != "":
				contradictory = "aggregation"
			case metric.Input != nil:
				contradictory = "input"
			case len(metric.Where) > 0:
				contradictory = "where"
			case metric.Empty != "":
				contradictory = "empty"
			case metric.TimeDimension != "":
				contradictory = "timeDimension"
			case metric.Expression != "":
				contradictory = "expression"
			}
		}
		if contradictory != "" {
			return fmt.Errorf("LeapView extension metric %q type %q contains contradictory field %q", name, metric.Type, contradictory)
		}
	}
	return nil
}

// validateRawMetricTags preserves field presence while checking the tagged
// union. Decoding into a Go struct alone cannot distinguish an omitted field
// from an explicitly authored zero value such as where: [] or expression: "";
// both are still contradictory fields when they belong to another tag.
func validateRawMetricTags(data json.RawMessage) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var metrics map[string]json.RawMessage
	if err := decodeStrictJSON(data, &metrics); err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	for name, raw := range metrics {
		var fields map[string]json.RawMessage
		if err := decodeStrictJSON(raw, &fields); err != nil {
			return fmt.Errorf("metrics.%s: %w", name, err)
		}
		var tag string
		if value, ok := fields["type"]; ok {
			if err := json.Unmarshal(value, &tag); err != nil {
				return fmt.Errorf("metrics.%s type: %w", name, err)
			}
		}
		for field := range metricTagFields(tag) {
			if _, present := fields[field]; present {
				return fmt.Errorf("LeapView extension metric %q type %q contains contradictory field %q", name, tag, field)
			}
		}
	}
	return nil
}

func metricTagFields(tag string) map[string]struct{} {
	fields := map[string]struct{}{}
	add := func(values ...string) {
		for _, value := range values {
			fields[value] = struct{}{}
		}
	}
	switch tag {
	case "aggregate":
		add("expression", "numerator", "denominator")
	case "derived":
		add("dataset", "aggregation", "input", "where", "empty", "timeDimension", "numerator", "denominator")
	case "ratio":
		add("dataset", "aggregation", "input", "where", "empty", "timeDimension", "expression")
	}
	return fields
}

func yamlTaggedJSON(value any) (json.RawMessage, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	normalized, err := yamlJSON(data)
	if err != nil {
		return nil, err
	}
	output, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return output, nil
}

// Import converts one supported Ossie semantic model into LeapView's native
// typed model. projectModels is authoritative: every dataset source must be
// an existing project Model key. No connection, source, transform, or table
// is ever synthesized from an Ossie source string.
func Import(data []byte, projectModels map[string]semanticmodel.Table) (*semanticmodel.Model, error) {
	if err := Validate(data); err != nil {
		return nil, err
	}
	var document Document
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("ossie decode: %w", err)
	}
	if document.Version != Version {
		return nil, fmt.Errorf("unsupported Ossie version %q (supported %q)", document.Version, Version)
	}
	if len(document.SemanticModel) != 1 {
		return nil, fmt.Errorf("Ossie import requires exactly one semantic_model, got %d", len(document.SemanticModel))
	}
	source := document.SemanticModel[0]
	if strings.TrimSpace(source.Name) == "" {
		return nil, fmt.Errorf("Ossie semantic model name is required")
	}
	var extension *extensionPayload
	for _, ext := range source.CustomExtensions {
		if ext.VendorName != ExtensionVendor {
			return nil, fmt.Errorf("unsupported Ossie custom extension vendor %q", ext.VendorName)
		}
		if extension != nil {
			return nil, fmt.Errorf("Ossie document contains multiple %s extensions", ExtensionVendor)
		}
		var decoded extensionPayload
		if err := json.Unmarshal([]byte(ext.Data), &decoded); err != nil {
			return nil, fmt.Errorf("%s extension is not valid JSON: %w", ExtensionVendor, err)
		}
		if decoded.Version != ExtensionVersion {
			return nil, fmt.Errorf("unsupported LeapView Ossie extension version %q (supported %q)", decoded.Version, ExtensionVersion)
		}
		extension = &decoded
	}
	for _, dataset := range source.Datasets {
		if err := rejectUnsupportedDatasetExtensions(dataset); err != nil {
			return nil, err
		}
	}
	for _, relationship := range source.Relationships {
		if err := rejectUnsupportedRelationshipExtensions(relationship); err != nil {
			return nil, err
		}
	}
	for _, metric := range source.Metrics {
		if err := rejectUnsupportedMetricExtensions(metric); err != nil {
			return nil, err
		}
		if metric.Datatype != "" {
			return nil, fmt.Errorf("Ossie metric %q datatype %q is not preserved by the native metric contract", metric.Name, metric.Datatype)
		}
	}

	result := &semanticmodel.Model{
		Name: source.Name, Title: source.Name, Description: source.Description,
		Tables: map[string]semanticmodel.Table{}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{},
		StructuredRelationships: map[string]semanticmodel.RelationshipSpec{}, Dimensions: map[string]semanticmodel.SemanticDimension{},
		Filters: map[string]semanticmodel.SemanticFilterSpec{}, Metrics: map[string]semanticmodel.Metric{},
		Relationships: []semanticmodel.Relationship{},
	}
	result.AIContext = fromAIContext(source.AIContext)

	for _, dataset := range source.Datasets {
		if dataset.Name == "" {
			return nil, fmt.Errorf("Ossie dataset name is required")
		}
		if _, exists := result.Datasets[dataset.Name]; exists {
			return nil, fmt.Errorf("duplicate Ossie dataset %q", dataset.Name)
		}
		table, ok := projectModels[dataset.Source]
		if !ok {
			return nil, fmt.Errorf("Ossie dataset %q source %q does not resolve to an existing project Model", dataset.Name, dataset.Source)
		}
		table = cloneImportedTable(table)
		// Copy the project-owned table. The source string is only a lookup key;
		// it is never interpreted as SQL or used to create a source resource.
		if err := applyDatasetFields(&table, dataset); err != nil {
			return nil, err
		}
		result.Tables[dataset.Name] = table
		ds := semanticmodel.SemanticDatasetSpec{Model: dataset.Source, Description: dataset.Description, AIContext: fromAIContext(dataset.AIContext)}
		result.Datasets[dataset.Name] = ds
	}

	for _, relationship := range source.Relationships {
		if err := importRelationship(result, relationship); err != nil {
			return nil, err
		}
	}

	for _, metric := range source.Metrics {
		converted, err := importCoreMetric(metric, result)
		if err != nil {
			if extension == nil {
				return nil, err
			}
			// The versioned LeapView extension is authoritative for executable
			// constructs Ossie core cannot express. Do not weaken the model if
			// the extension does not actually preserve this member.
			if extension.Metrics == nil {
				return nil, fmt.Errorf("Ossie metric %q contains unsupported executable behavior without a LeapView extension: %w", metric.Name, err)
			}
			if _, preserved := extension.Metrics[metric.Name]; !preserved {
				return nil, fmt.Errorf("Ossie metric %q contains unsupported executable behavior not preserved by the LeapView extension: %w", metric.Name, err)
			}
			continue
		}
		result.Metrics[metric.Name] = converted
	}
	if extension != nil {
		if err := applyExtension(result, extension, projectModels); err != nil {
			return nil, err
		}
	}
	if err := normalizeImportedGraph(result); err != nil {
		return nil, err
	}
	if err := result.ValidateSemanticGraph(); err != nil {
		return nil, err
	}
	return result, nil
}

// applyDatasetFields carries Ossie's portable field annotations onto the
// copied canonical table. It never creates a source or transformation. Scalar
// computed expressions are rejected because ModelFieldSpec must own those
// executable transformations in the project resource.
func applyDatasetFields(table *semanticmodel.Table, dataset Dataset) error {
	if len(dataset.PrimaryKey) > 0 {
		if table.GrainEntity == "" {
			return fmt.Errorf("Ossie dataset %q primary_key %v requires an existing Model grain", dataset.Name, dataset.PrimaryKey)
		}
		if !sameFields(table.GrainFields(), dataset.PrimaryKey) {
			return fmt.Errorf("Ossie dataset %q primary_key %v disagrees with existing Model grain %v", dataset.Name, dataset.PrimaryKey, table.GrainFields())
		}
	}
	if len(dataset.UniqueKeys) > 0 {
		for index, key := range dataset.UniqueKeys {
			if len(key) == 0 {
				return fmt.Errorf("Ossie dataset %q unique_keys[%d] is empty", dataset.Name, index)
			}
			if !tableHasUniqueEntity(table, key) {
				return fmt.Errorf("Ossie dataset %q unique_keys[%d] %v is not declared on the existing Model", dataset.Name, index, key)
			}
		}
	}
	for _, field := range dataset.Fields {
		if field.Name == "" || len(field.Expression.Dialects) == 0 {
			return fmt.Errorf("Ossie dataset %q field %q requires an expression", dataset.Name, field.Name)
		}
		for _, dialect := range field.Expression.Dialects {
			if dialect.Dialect != "ANSI_SQL" {
				return fmt.Errorf("Ossie dataset %q field %q has unsupported expression dialect %q", dataset.Name, field.Name, dialect.Dialect)
			}
			if dialect.Expression != field.Name {
				return fmt.Errorf("Ossie dataset %q field %q has unsupported computed expression; project Model transformations are authoritative", dataset.Name, field.Name)
			}
		}
		column, hasColumn := table.Columns[field.Name]
		dimension, hasDimension := table.Dimensions[field.Name]
		if !hasColumn && !hasDimension {
			return fmt.Errorf("Ossie dataset %q field %q is not declared on the existing Model", dataset.Name, field.Name)
		}
		modelDatatype := column.Datatype
		if modelDatatype == "" {
			modelDatatype = dimension.Datatype
		}
		if field.Datatype == "" {
			if modelDatatype == "" {
				return fmt.Errorf("Ossie dataset %q field %q has no compatible logical datatype on the existing Model", dataset.Name, field.Name)
			}
		} else if modelDatatype == "" || semanticmodel.LogicalDataType(field.Datatype) != modelDatatype {
			return fmt.Errorf("Ossie dataset %q field %q datatype %q disagrees with existing Model datatype %q", dataset.Name, field.Name, field.Datatype, modelDatatype)
		}
		if field.Dimension != nil && !hasDimension {
			return fmt.Errorf("Ossie dataset %q field %q is marked dimension but missing from existing Model fields", dataset.Name, field.Name)
		}
		if len(field.CustomExtensions) > 0 {
			return fmt.Errorf("Ossie dataset %q field %q contains unsupported custom extensions", dataset.Name, field.Name)
		}
		if field.Label != "" && hasDimension && dimension.Label != "" && field.Label != dimension.Label {
			return fmt.Errorf("Ossie dataset %q field %q label %q disagrees with existing Model label %q", dataset.Name, field.Name, field.Label, dimension.Label)
		}
		if field.Description != "" && hasDimension && dimension.Description != "" && field.Description != dimension.Description {
			return fmt.Errorf("Ossie dataset %q field %q description disagrees with existing Model metadata", dataset.Name, field.Name)
		}
	}
	return nil
}

func tableHasUniqueEntity(table *semanticmodel.Table, fields []string) bool {
	if table == nil {
		return false
	}
	for _, entity := range table.Entities {
		if (entity.Type == "primary" || entity.Type == "unique") && sameFields(entity.Fields, fields) {
			return true
		}
	}
	return false
}

func rejectUnsupportedDatasetExtensions(dataset Dataset) error {
	if len(dataset.CustomExtensions) > 0 {
		return fmt.Errorf("Ossie dataset %q contains unsupported custom extensions", dataset.Name)
	}
	for _, field := range dataset.Fields {
		if len(field.CustomExtensions) > 0 {
			return fmt.Errorf("Ossie dataset %q field %q contains unsupported custom extensions", dataset.Name, field.Name)
		}
	}
	return nil
}

func rejectUnsupportedRelationshipExtensions(relationship Relationship) error {
	if len(relationship.CustomExtensions) > 0 {
		return fmt.Errorf("Ossie relationship %q contains unsupported custom extensions", relationship.Name)
	}
	return nil
}

func rejectUnsupportedMetricExtensions(metric Metric) error {
	if len(metric.CustomExtensions) > 0 {
		return fmt.Errorf("Ossie metric %q contains unsupported custom extensions", metric.Name)
	}
	return nil
}

func sameFields(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// ImportYAML is an explicit spelling for callers handling authored YAML.
// Import accepts both YAML and JSON because JSON is a YAML subset.
func ImportYAML(data []byte, projectModels map[string]semanticmodel.Table) (*semanticmodel.Model, error) {
	return Import(data, projectModels)
}

func importRelationship(result *semanticmodel.Model, value Relationship) error {
	if value.Name == "" || value.From == "" || value.To == "" || len(value.FromColumns) == 0 || len(value.ToColumns) == 0 {
		return fmt.Errorf("Ossie relationship %q requires from/to datasets and non-empty key columns", value.Name)
	}
	if len(value.FromColumns) != len(value.ToColumns) {
		return fmt.Errorf("Ossie relationship %q key arity mismatch", value.Name)
	}
	if _, ok := result.Datasets[value.From]; !ok {
		return fmt.Errorf("Ossie relationship %q references unknown from dataset %q", value.Name, value.From)
	}
	if _, ok := result.Datasets[value.To]; !ok {
		return fmt.Errorf("Ossie relationship %q references unknown to dataset %q", value.Name, value.To)
	}
	r := semanticmodel.Relationship{ID: value.Name, FromDataset: value.From, ToDataset: value.To, FromFields: append([]string(nil), value.FromColumns...), ToFields: append([]string(nil), value.ToColumns...), Description: ""}
	r.Cardinality = "many_to_one"
	result.Relationships = append(result.Relationships, r)
	result.StructuredRelationships[value.Name] = semanticmodel.RelationshipSpec{
		From: semanticmodel.RelationshipEndpointSpec{Dataset: value.From, Fields: append([]string(nil), value.FromColumns...)},
		To:   semanticmodel.RelationshipEndpointSpec{Dataset: value.To, Fields: append([]string(nil), value.ToColumns...)},
	}
	return nil
}

func importCoreMetric(value Metric, model *semanticmodel.Model) (semanticmodel.Metric, error) {
	if value.Name == "" || len(value.Expression.Dialects) == 0 {
		return semanticmodel.Metric{}, fmt.Errorf("Ossie metric %q requires an expression", value.Name)
	}
	expression := value.Expression.Dialects[0]
	for _, dialect := range value.Expression.Dialects {
		if dialect.Dialect != "ANSI_SQL" {
			return semanticmodel.Metric{}, fmt.Errorf("Ossie metric %q has unsupported executable dialect %q", value.Name, dialect.Dialect)
		}
		if dialect.Expression != expression.Expression {
			return semanticmodel.Metric{}, fmt.Errorf("Ossie metric %q has conflicting executable dialect expressions", value.Name)
		}
	}
	if expression.Dialect != "ANSI_SQL" {
		return semanticmodel.Metric{}, fmt.Errorf("Ossie metric %q has unsupported executable dialect %q", value.Name, expression.Dialect)
	}
	if isCountStarExpression(expression.Expression) {
		datasetNames := sortedKeys(model.Datasets)
		if len(datasetNames) != 1 {
			return semanticmodel.Metric{}, fmt.Errorf("Ossie metric %q COUNT(*) requires exactly one dataset or a LeapView extension", value.Name)
		}
		dataset := datasetNames[0]
		table := model.Tables[dataset]
		inputFields := table.GrainFields()
		if len(inputFields) == 0 {
			return semanticmodel.Metric{}, fmt.Errorf("Ossie metric %q COUNT(*) dataset %q requires a declared Model grain for the native input contract", value.Name, dataset)
		}
		return semanticmodel.Metric{
			Type: "aggregate", Dataset: dataset, Aggregation: "count",
			Input: &semanticmodel.MetricInput{Field: dataset + "." + inputFields[0]}, Empty: "zero",
			Label: value.Name, Description: value.Description, AIContext: fromAIContext(value.AIContext),
		}, nil
	}
	call := regexp.MustCompile(`(?i)^\s*(SUM|AVG|MIN|MAX|COUNT|COUNT_DISTINCT)\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\s*\)\s*$`).FindStringSubmatch(expression.Expression)
	if len(call) == 0 {
		if distinct := regexp.MustCompile(`(?i)^\s*COUNT\s*\(\s*DISTINCT\s+([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\s*\)\s*$`).FindStringSubmatch(expression.Expression); len(distinct) == 3 {
			call = []string{distinct[0], "COUNT_DISTINCT", distinct[1], distinct[2]}
		}
	}
	if len(call) == 4 {
		aggregation := strings.ToLower(call[1])
		if aggregation == "count_distinct" {
			aggregation = "count_distinct"
		}
		empty := "null"
		if aggregation == "count" || aggregation == "count_distinct" {
			empty = "zero"
		}
		return semanticmodel.Metric{Type: "aggregate", Dataset: call[2], Aggregation: aggregation, Input: &semanticmodel.MetricInput{Field: call[2] + "." + call[3]}, Empty: empty, Label: value.Name, Description: value.Description, AIContext: fromAIContext(value.AIContext)}, nil
	}
	return semanticmodel.Metric{}, fmt.Errorf("Ossie metric %q contains unsupported executable expression %q; import a LeapView extension", value.Name, expression.Expression)
}

func isCountStarExpression(expression string) bool {
	return regexp.MustCompile(`(?i)^\s*COUNT\s*\(\s*\*\s*\)\s*$`).MatchString(expression)
}

func normalizeImportedGraph(value *semanticmodel.Model) error {
	if value == nil {
		return fmt.Errorf("native semantic model is required")
	}
	if len(value.StructuredRelationships) == 0 && len(value.Relationships) > 0 {
		return fmt.Errorf("native semantic model relationships must use StructuredRelationships")
	}
	for name, table := range value.Tables {
		for field, column := range table.Columns {
			dimension, ok := table.Dimensions[field]
			if !ok {
				return fmt.Errorf("Ossie dataset %q Model field %q is missing from the existing Model semantic fields", name, field)
			}
			dimension.Name, dimension.Field, dimension.Table = field, name+"."+field, name
			if column.Datatype != "" && dimension.Datatype != "" && column.Datatype != dimension.Datatype {
				return fmt.Errorf("Ossie dataset %q Model field %q column datatype %q disagrees with semantic field datatype %q", name, field, column.Datatype, dimension.Datatype)
			}
			if column.Datatype != "" && dimension.Datatype == "" {
				return fmt.Errorf("Ossie dataset %q Model field %q semantic field is missing logical datatype", name, field)
			}
			if dimension.Datatype != "" && dimension.Type != "" && nativeDimensionType(dimension.Datatype) != dimension.Type {
				return fmt.Errorf("Ossie dataset %q Model field %q semantic type %q disagrees with logical datatype %q", name, field, dimension.Type, dimension.Datatype)
			}
			table.Dimensions[field] = dimension
		}
		for entityName, entity := range table.Entities {
			for _, field := range entity.Fields {
				if _, ok := table.Dimensions[field]; !ok {
					return fmt.Errorf("Ossie dataset %q Model entity %q field %q is missing from the existing Model semantic fields", name, entityName, field)
				}
			}
		}
		value.Tables[name] = table
	}
	for name, dimension := range value.Dimensions {
		dimension.Name = name
		if dimension.Datatype != "" && dimension.Type != "" && nativeDimensionType(dimension.Datatype) != dimension.Type {
			return fmt.Errorf("Ossie semantic dimension %q type %q disagrees with logical datatype %q", name, dimension.Type, dimension.Datatype)
		}
		value.Dimensions[name] = dimension
	}
	for index := range value.Relationships {
		relation := &value.Relationships[index]
		fromUnique := relationEndpointUnique(value.Tables[relation.FromDataset], relation.FromFields)
		toUnique := relationEndpointUnique(value.Tables[relation.ToDataset], relation.ToFields)
		if fromUnique && toUnique {
			relation.Cardinality = "one_to_one"
		} else {
			relation.Cardinality = "many_to_one"
		}
	}
	return nil
}

func nativeDimensionType(value semanticmodel.LogicalDataType) string {
	switch value {
	case semanticmodel.DataTypeString:
		return "string"
	case semanticmodel.DataTypeInteger, semanticmodel.DataTypeDecimal, semanticmodel.DataTypeFloat:
		return "number"
	case semanticmodel.DataTypeBoolean:
		return "boolean"
	case semanticmodel.DataTypeDate:
		return "date"
	case semanticmodel.DataTypeTime, semanticmodel.DataTypeDateTime, semanticmodel.DataTypeDateTimeTZ:
		return "timestamp"
	default:
		return strings.ToLower(string(value))
	}
}

func relationEndpointUnique(table semanticmodel.Table, fields []string) bool {
	for _, entity := range table.Entities {
		if (entity.Type == "primary" || entity.Type == "unique") && sameFields(entity.Fields, fields) {
			return true
		}
	}
	return false
}

func applyExtension(result *semanticmodel.Model, extension *extensionPayload, projectModels map[string]semanticmodel.Table) error {
	if extension.Description != "" {
		result.Description = extension.Description
	}
	if extension.AIContext != nil {
		result.AIContext = extension.AIContext
	}
	if extension.Datasets != nil {
		if len(extension.Datasets) != len(result.Datasets) {
			return fmt.Errorf("LeapView extension datasets do not match Ossie datasets")
		}
		for name, dataset := range extension.Datasets {
			coreDataset, ok := result.Datasets[name]
			if !ok {
				return fmt.Errorf("LeapView extension introduces unknown dataset %q", name)
			}
			if coreDataset.Model != dataset.Model {
				return fmt.Errorf("LeapView extension dataset %q model %q disagrees with Ossie source %q", name, dataset.Model, coreDataset.Model)
			}
			table, ok := projectModels[dataset.Model]
			if !ok {
				return fmt.Errorf("LeapView extension dataset %q model %q does not resolve to an existing project Model", name, dataset.Model)
			}
			if _, exists := result.Tables[name]; !exists {
				result.Tables[name] = cloneImportedTable(table)
			}
		}
		result.Datasets = extension.Datasets
	}
	if extension.Relationships != nil {
		for name, extensionRelationship := range extension.Relationships {
			if coreRelationship, ok := result.StructuredRelationships[name]; ok {
				if err := validatePortableRelationshipAgreement(name, coreRelationship, extensionRelationship, result.Tables); err != nil {
					return err
				}
			}
			result.StructuredRelationships[name] = extensionRelationship
		}
	}
	if extension.Dimensions != nil {
		for name, value := range extension.Dimensions {
			result.Dimensions[name] = semanticDimension(value)
		}
	}
	if extension.Filters != nil {
		result.Filters = extension.Filters
	}
	if extension.Metrics != nil {
		for name, metric := range extension.Metrics {
			if core, ok := result.Metrics[name]; ok {
				if err := validatePortableMetricAgreement(name, core, metric); err != nil {
					return err
				}
			}
			metric.Name = name
			result.Metrics[name] = metric
		}
	}
	result.Relationships = result.Relationships[:0]
	for id, relation := range result.StructuredRelationships {
		fromFields := endpointFieldsResolved(relation.From, result.Tables)
		toFields := endpointFieldsResolved(relation.To, result.Tables)
		if len(fromFields) == 0 || len(toFields) == 0 {
			return fmt.Errorf("relationship %q extension endpoint requires a declared entity or non-empty fields", id)
		}
		from := relation.From.Dataset
		to := relation.To.Dataset
		r := semanticmodel.Relationship{ID: id, FromDataset: from, FromFields: fromFields, ToDataset: to, ToFields: toFields, Description: relation.Description, AIContext: relation.AIContext, Cardinality: "many_to_one"}
		result.Relationships = append(result.Relationships, r)
	}
	sort.Slice(result.Relationships, func(i, j int) bool { return result.Relationships[i].ID < result.Relationships[j].ID })
	return nil
}

func validatePortableRelationshipAgreement(name string, core, extension semanticmodel.RelationshipSpec, tables map[string]semanticmodel.Table) error {
	if core.From.Dataset != extension.From.Dataset || core.To.Dataset != extension.To.Dataset {
		return fmt.Errorf("LeapView extension relationship %q endpoints disagree with Ossie core", name)
	}
	coreFrom, coreTo := endpointFieldsResolved(core.From, tables), endpointFieldsResolved(core.To, tables)
	extensionFrom, extensionTo := endpointFieldsResolved(extension.From, tables), endpointFieldsResolved(extension.To, tables)
	if !sameFields(coreFrom, extensionFrom) || !sameFields(coreTo, extensionTo) {
		return fmt.Errorf("LeapView extension relationship %q key definition disagrees with Ossie core", name)
	}
	return nil
}

func validatePortableMetricAgreement(name string, core, extension semanticmodel.Metric) error {
	if core.Type != extension.Type {
		return fmt.Errorf("LeapView extension metric %q type %q disagrees with Ossie core type %q", name, extension.Type, core.Type)
	}
	if core.Type != "aggregate" {
		return nil
	}
	coreInput, extensionInput := "", ""
	if core.Input != nil {
		coreInput = core.Input.Field
	}
	if extension.Input != nil {
		extensionInput = extension.Input.Field
	}
	if core.Dataset != extension.Dataset || core.Aggregation != extension.Aggregation || coreInput != extensionInput {
		return fmt.Errorf("LeapView extension metric %q executable definition disagrees with Ossie core", name)
	}
	return nil
}

func endpointFields(endpoint semanticmodel.RelationshipEndpointSpec) []string {
	if len(endpoint.Fields) > 0 {
		return append([]string(nil), endpoint.Fields...)
	}
	return nil
}

func endpointFieldsResolved(endpoint semanticmodel.RelationshipEndpointSpec, tables map[string]semanticmodel.Table) []string {
	if fields := endpointFields(endpoint); len(fields) > 0 {
		return fields
	}
	if endpoint.Entity == "" {
		return nil
	}
	if table, ok := tables[endpoint.Dataset]; ok {
		if entity, ok := table.Entities[endpoint.Entity]; ok {
			return append([]string(nil), entity.Fields...)
		}
	}
	return nil
}

func semanticDimension(value semanticmodel.SemanticDimensionSpec) semanticmodel.SemanticDimension {
	result := semanticmodel.SemanticDimension{Label: value.Label, Description: value.Description, Type: nativeDimensionType(value.Datatype), Datatype: value.Datatype, Bindings: value.Bindings, AIContext: value.AIContext}
	if value.Time != nil {
		result.Grains = append([]string(nil), value.Time.Grains...)
		result.NativeGrain = value.Time.NativeGrain
		result.Calendar, result.Timezone = value.Time.Calendar, value.Time.Timezone
	}
	return result
}

func fromAIContext(value *AIContext) *semanticmodel.AIContext {
	if value == nil {
		return nil
	}
	return &semanticmodel.AIContext{Instructions: value.Instructions, Synonyms: append([]string(nil), value.Synonyms...), Examples: append([]string(nil), value.Examples...)}
}

func toAIContext(value *semanticmodel.AIContext) *AIContext {
	if value == nil {
		return nil
	}
	return &AIContext{Instructions: value.Instructions, Synonyms: append([]string(nil), value.Synonyms...), Examples: append([]string(nil), value.Examples...)}
}

// Export serializes a native model as deterministic JSON accepted by the
// official Ossie schema. JSON is a YAML subset, so callers may feed the result
// directly to YAML tooling as well.
func Export(value *semanticmodel.Model) ([]byte, error) {
	if err := validateNativeForExport(value); err != nil {
		return nil, err
	}
	document, err := exportDocument(value)
	if err != nil {
		return nil, err
	}
	output, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	output = append(output, '\n')
	if err := Validate(output); err != nil {
		return nil, fmt.Errorf("exported Ossie document failed official validation: %w", err)
	}
	return output, nil
}

// validateNativeForExport validates the native semantic graph before any
// Ossie representation is produced. It validates a detached copy so default
// labels/timezone metadata and derived relationship slices cannot mutate the
// caller. Physical Model columns are already authoritative; when a validated
// table exposes a column without a duplicate semantic field, the detached
// copy exposes that existing column as a field solely for graph validation.
func validateNativeForExport(value *semanticmodel.Model) error {
	if value == nil {
		return fmt.Errorf("native semantic model is required")
	}
	if len(value.StructuredRelationships) == 0 && len(value.Relationships) > 0 {
		return fmt.Errorf("native semantic model relationships must use StructuredRelationships")
	}
	clone := *value
	clone.Tables = make(map[string]semanticmodel.Table, len(value.Tables))
	for name, table := range value.Tables {
		copied := table
		copied.Columns = copyModelColumns(table.Columns)
		copied.Dimensions = copyMetricDimensions(table.Dimensions)
		if copied.Dimensions == nil {
			copied.Dimensions = map[string]semanticmodel.MetricDimension{}
		}
		for field, column := range copied.Columns {
			if _, ok := copied.Dimensions[field]; ok {
				continue
			}
			if column.Datatype == "" {
				return fmt.Errorf("native semantic model table %q column %q has no semantic field metadata", name, field)
			}
			copied.Dimensions[field] = semanticmodel.MetricDimension{Name: field, Field: name + "." + field, Table: name, Type: nativeDimensionType(column.Datatype), Datatype: column.Datatype}
		}
		clone.Tables[name] = copied
	}
	clone.Datasets = copySemanticDatasets(value.Datasets)
	clone.StructuredRelationships = copyRelationshipSpecs(value.StructuredRelationships)
	clone.Dimensions = copySemanticDimensions(value.Dimensions)
	for name, dimension := range clone.Dimensions {
		if dimension.Datatype != "" {
			dimension.Type = nativeDimensionType(dimension.Datatype)
			clone.Dimensions[name] = dimension
		}
	}
	clone.Filters = copySemanticFilters(value.Filters)
	clone.Metrics = copyMetrics(value.Metrics)
	if len(clone.StructuredRelationships) > 0 {
		relationships, err := relationshipsFromSpecs(clone.StructuredRelationships, clone.Tables)
		if err != nil {
			return fmt.Errorf("native semantic model relationships: %w", err)
		}
		clone.Relationships = relationships
	}
	if err := clone.ValidateSemanticGraph(); err != nil {
		return fmt.Errorf("native semantic model validation: %w", err)
	}
	return nil
}

func relationshipsFromSpecs(specs map[string]semanticmodel.RelationshipSpec, tables map[string]semanticmodel.Table) ([]semanticmodel.Relationship, error) {
	ids := sortedKeys(specs)
	result := make([]semanticmodel.Relationship, 0, len(ids))
	for _, id := range ids {
		spec := specs[id]
		fromFields := endpointFieldsResolved(spec.From, tables)
		toFields := endpointFieldsResolved(spec.To, tables)
		if spec.From.Dataset == "" || spec.To.Dataset == "" || len(fromFields) == 0 || len(toFields) == 0 {
			return nil, fmt.Errorf("relationship %q endpoint requires declared datasets and fields", id)
		}
		if _, ok := tables[spec.From.Dataset]; !ok {
			return nil, fmt.Errorf("relationship %q references unknown from dataset %q", id, spec.From.Dataset)
		}
		if _, ok := tables[spec.To.Dataset]; !ok {
			return nil, fmt.Errorf("relationship %q references unknown to dataset %q", id, spec.To.Dataset)
		}
		fromUnique := relationEndpointUnique(tables[spec.From.Dataset], fromFields)
		toUnique := relationEndpointUnique(tables[spec.To.Dataset], toFields)
		cardinality := "many_to_one"
		if fromUnique && toUnique {
			cardinality = "one_to_one"
		}
		relationship := semanticmodel.Relationship{ID: id, FromDataset: spec.From.Dataset, FromFields: fromFields, ToDataset: spec.To.Dataset, ToFields: toFields, Cardinality: cardinality, Description: spec.Description, AIContext: spec.AIContext}
		result = append(result, relationship)
	}
	return result, nil
}

func copyModelColumns(values map[string]semanticmodel.ModelColumn) map[string]semanticmodel.ModelColumn {
	result := make(map[string]semanticmodel.ModelColumn, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneImportedTable(table semanticmodel.Table) semanticmodel.Table {
	clone := table
	clone.AIContext = cloneAIContext(table.AIContext)
	clone.Sources = append([]string(nil), table.Sources...)
	clone.SourceDependencies = append([]string(nil), table.SourceDependencies...)
	clone.ModelDependencies = append([]string(nil), table.ModelDependencies...)
	if table.SourceReads != nil {
		clone.SourceReads = make(map[string][]string, len(table.SourceReads))
		for name, reads := range table.SourceReads {
			clone.SourceReads[name] = append([]string(nil), reads...)
		}
	}
	clone.Columns = copyModelColumns(table.Columns)
	for name, column := range clone.Columns {
		column.AIContext = cloneAIContext(column.AIContext)
		clone.Columns[name] = column
	}
	clone.Dimensions = copyMetricDimensions(table.Dimensions)
	for name, dimension := range clone.Dimensions {
		dimension.AIContext = cloneAIContext(dimension.AIContext)
		clone.Dimensions[name] = dimension
	}
	clone.Entities = make(map[string]semanticmodel.ModelEntitySpec, len(table.Entities))
	for name, entity := range table.Entities {
		entity.Fields = append([]string(nil), entity.Fields...)
		entity.AIContext = cloneAIContext(entity.AIContext)
		clone.Entities[name] = entity
	}
	clone.Schema.Columns = append([]semanticmodel.ColumnSchema(nil), table.Schema.Columns...)
	return clone
}

func cloneAIContext(value *semanticmodel.AIContext) *semanticmodel.AIContext {
	if value == nil {
		return nil
	}
	return &semanticmodel.AIContext{Instructions: value.Instructions, Synonyms: append([]string(nil), value.Synonyms...), Examples: append([]string(nil), value.Examples...)}
}

func copyMetricDimensions(values map[string]semanticmodel.MetricDimension) map[string]semanticmodel.MetricDimension {
	result := make(map[string]semanticmodel.MetricDimension, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func copySemanticDatasets(values map[string]semanticmodel.SemanticDatasetSpec) map[string]semanticmodel.SemanticDatasetSpec {
	result := make(map[string]semanticmodel.SemanticDatasetSpec, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func copyRelationshipSpecs(values map[string]semanticmodel.RelationshipSpec) map[string]semanticmodel.RelationshipSpec {
	result := make(map[string]semanticmodel.RelationshipSpec, len(values))
	for key, value := range values {
		value.From.Fields = append([]string(nil), value.From.Fields...)
		value.To.Fields = append([]string(nil), value.To.Fields...)
		result[key] = value
	}
	return result
}

func copySemanticDimensions(values map[string]semanticmodel.SemanticDimension) map[string]semanticmodel.SemanticDimension {
	result := make(map[string]semanticmodel.SemanticDimension, len(values))
	for key, value := range values {
		value.Grains = append([]string(nil), value.Grains...)
		value.Bindings = copyDimensionBindings(value.Bindings)
		result[key] = value
	}
	return result
}

func copyDimensionBindings(values map[string]semanticmodel.DimensionBinding) map[string]semanticmodel.DimensionBinding {
	result := make(map[string]semanticmodel.DimensionBinding, len(values))
	for key, value := range values {
		value.Path = append([]string(nil), value.Path...)
		result[key] = value
	}
	return result
}

func copySemanticFilters(values map[string]semanticmodel.SemanticFilterSpec) map[string]semanticmodel.SemanticFilterSpec {
	result := make(map[string]semanticmodel.SemanticFilterSpec, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func copyMetrics(values map[string]semanticmodel.Metric) map[string]semanticmodel.Metric {
	result := make(map[string]semanticmodel.Metric, len(values))
	for key, value := range values {
		if value.Input != nil {
			input := *value.Input
			value.Input = &input
		}
		value.Where = append([]string(nil), value.Where...)
		result[key] = value
	}
	return result
}

// ExportJSON is an explicit alias for callers that want to document the wire
// encoding at the call site.
func ExportJSON(value *semanticmodel.Model) ([]byte, error) { return Export(value) }

// ExportYAML emits the same document in YAML while retaining official schema
// validation.
func ExportYAML(value *semanticmodel.Model) ([]byte, error) {
	if err := validateNativeForExport(value); err != nil {
		return nil, err
	}
	document, err := exportDocument(value)
	if err != nil {
		return nil, err
	}
	output, err := yaml.Marshal(document)
	if err != nil {
		return nil, err
	}
	if err := Validate(output); err != nil {
		return nil, fmt.Errorf("exported Ossie document failed official validation: %w", err)
	}
	return output, nil
}

func exportDocument(value *semanticmodel.Model) (Document, error) {
	if value == nil {
		return Document{}, fmt.Errorf("native semantic model is required")
	}
	if len(value.Datasets) == 0 {
		return Document{}, fmt.Errorf("native semantic model %q has no datasets", value.Name)
	}
	result := SemanticModel{Name: value.Name, Description: value.Description, AIContext: toAIContext(value.AIContext)}
	datasetNames := sortedKeys(value.Datasets)
	for _, name := range datasetNames {
		dataset := value.Datasets[name]
		if dataset.Model == "" {
			return Document{}, fmt.Errorf("dataset %q has no project Model reference", name)
		}
		table, ok := value.Tables[name]
		if !ok {
			return Document{}, fmt.Errorf("dataset %q has no bound project Model table", name)
		}
		converted := Dataset{Name: name, Source: dataset.Model, Description: dataset.Description, AIContext: toAIContext(dataset.AIContext)}
		if converted.Description == "" {
			converted.Description = table.Description
		}
		if table.GrainEntity != "" {
			converted.PrimaryKey = append([]string(nil), table.GrainFields()...)
		}
		for entityName, entity := range table.Entities {
			if entityName != table.GrainEntity && (entity.Type == "unique" || entity.Type == "primary") {
				converted.UniqueKeys = append(converted.UniqueKeys, append([]string(nil), entity.Fields...))
			}
		}
		sort.Slice(converted.UniqueKeys, func(i, j int) bool {
			return strings.Join(converted.UniqueKeys[i], "\x00") < strings.Join(converted.UniqueKeys[j], "\x00")
		})
		converted.Fields = exportFields(table, value.Dimensions)
		result.Datasets = append(result.Datasets, converted)
	}

	structuredRelationships := value.StructuredRelationships
	relationNames := sortedKeys(structuredRelationships)
	for _, name := range relationNames {
		relation := structuredRelationships[name]
		from, fromFields, err := endpointForExport(relation.From, relation.From.Dataset, value.Tables)
		if err != nil {
			return Document{}, fmt.Errorf("relationship %q from: %w", name, err)
		}
		to, toFields, err := endpointForExport(relation.To, relation.To.Dataset, value.Tables)
		if err != nil {
			return Document{}, fmt.Errorf("relationship %q to: %w", name, err)
		}
		result.Relationships = append(result.Relationships, Relationship{Name: name, From: from, To: to, FromColumns: fromFields, ToColumns: toFields, AIContext: toAIContext(relation.AIContext)})
	}

	metricNames := sortedKeys(value.Metrics)
	for _, name := range metricNames {
		metric := value.Metrics[name]
		expression, err := metricExpression(metric)
		if err != nil {
			return Document{}, err
		}
		result.Metrics = append(result.Metrics, Metric{Name: name, Expression: Expression{Dialects: []DialectExpression{{Dialect: "ANSI_SQL", Expression: expression}}}, Description: metric.Description, AIContext: toAIContext(metric.AIContext)})
	}

	extension := extensionPayload{Version: ExtensionVersion, Description: value.Description, AIContext: value.AIContext, Datasets: value.Datasets, Relationships: structuredRelationships, Filters: value.Filters, Metrics: value.Metrics}
	// Dimensions are reconstructed into their authored spec form for the
	// extension. The Ossie core fields intentionally carry only portable field
	// metadata; bindings/time grains remain fact-relative native semantics.
	if len(value.Dimensions) > 0 {
		extension.Dimensions = make(map[string]semanticmodel.SemanticDimensionSpec, len(value.Dimensions))
		for name, dimension := range value.Dimensions {
			spec := semanticmodel.SemanticDimensionSpec{Label: dimension.Label, Description: dimension.Description, AIContext: dimension.AIContext, Datatype: dimension.Datatype, Bindings: dimension.Bindings}
			if len(dimension.Grains) > 0 || dimension.NativeGrain != "" || dimension.Calendar != "" || dimension.Timezone != "" {
				nativeGrain := dimension.NativeGrain
				if nativeGrain == "" {
					nativeGrain = firstGrain(dimension.Grains)
				}
				spec.Time = &semanticmodel.TimeSemanticsSpec{NativeGrain: nativeGrain, Grains: append([]string(nil), dimension.Grains...), Calendar: dimension.Calendar, Timezone: dimension.Timezone}
			}
			extension.Dimensions[name] = spec
		}
	}
	textData, err := json.Marshal(extension)
	if err != nil {
		return Document{}, fmt.Errorf("LeapView Ossie extension: %w", err)
	}
	result.CustomExtensions = []CustomExtension{{VendorName: ExtensionVendor, Data: string(textData)}}
	return Document{Version: Version, SemanticModel: []SemanticModel{result}}, nil
}

func exportFields(table semanticmodel.Table, dimensions map[string]semanticmodel.SemanticDimension) []Field {
	fields := map[string]Field{}
	for name, column := range table.Columns {
		field := Field{Name: name, Expression: Expression{Dialects: []DialectExpression{{Dialect: "ANSI_SQL", Expression: name}}}, Description: column.Description, Datatype: string(column.Datatype), AIContext: toAIContext(column.AIContext)}
		fields[name] = field
	}
	for name, dimension := range table.Dimensions {
		field := fields[name]
		field.Name = name
		field.Label, field.Description, field.AIContext = dimension.Label, dimension.Description, toAIContext(dimension.AIContext)
		field.Datatype = string(dimension.Datatype)
		if semantic, ok := dimensions[name]; ok {
			isTime := len(semantic.Grains) > 0
			field.Dimension = &Dimension{IsTime: &isTime}
		}
		if len(field.Expression.Dialects) == 0 {
			field.Expression = Expression{Dialects: []DialectExpression{{Dialect: "ANSI_SQL", Expression: name}}}
		}
		fields[name] = field
	}
	result := make([]Field, 0, len(fields))
	for _, field := range fields {
		result = append(result, field)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func endpointForExport(endpoint semanticmodel.RelationshipEndpointSpec, fallback string, tables map[string]semanticmodel.Table) (string, []string, error) {
	dataset := endpoint.Dataset
	if dataset == "" {
		dataset = fallback
	}
	if dataset == "" {
		return "", nil, fmt.Errorf("endpoint dataset is required")
	}
	fields := append([]string(nil), endpoint.Fields...)
	if endpoint.Entity != "" {
		table, ok := tables[dataset]
		if !ok {
			return "", nil, fmt.Errorf("endpoint references unknown dataset %q", dataset)
		}
		entity, ok := table.Entities[endpoint.Entity]
		if !ok {
			return "", nil, fmt.Errorf("endpoint references unknown entity %q", endpoint.Entity)
		}
		fields = append([]string(nil), entity.Fields...)
	}
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("endpoint %q has no fields", dataset)
	}
	return dataset, fields, nil
}

func metricExpression(metric semanticmodel.Metric) (string, error) {
	switch metric.Type {
	case "aggregate":
		if metric.Input == nil || metric.Input.Field == "" {
			return "", fmt.Errorf("aggregate metric has no input")
		}
		aggregation := strings.ToUpper(metric.Aggregation)
		if aggregation == "COUNT" {
			return aggregation + "(" + metric.Input.Field + ")", nil
		}
		if aggregation == "COUNT_DISTINCT" {
			aggregation = "COUNT(DISTINCT"
			return aggregation + " " + metric.Input.Field + ")", nil
		}
		switch aggregation {
		case "SUM", "AVG", "MIN", "MAX", "COUNT":
			return aggregation + "(" + metric.Input.Field + ")", nil
		default:
			return "", fmt.Errorf("aggregate metric has unsupported aggregation %q", metric.Aggregation)
		}
	case "derived":
		if strings.TrimSpace(metric.Expression) == "" {
			return "", fmt.Errorf("derived metric has no expression")
		}
		return metric.Expression, nil
	case "ratio":
		if metric.Numerator == "" || metric.Denominator == "" {
			return "", fmt.Errorf("ratio metric requires numerator and denominator")
		}
		return "SAFE_DIVIDE(" + metric.Numerator + ", " + metric.Denominator + ")", nil
	default:
		return "", fmt.Errorf("metric %q has unsupported type %q", metric.Name, metric.Type)
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstGrain(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
