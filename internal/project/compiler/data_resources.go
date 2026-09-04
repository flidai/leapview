package compiler

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

// decodeConnectionResource is the sole compiler boundary from the generated
// typed authoring DTO into the runtime semantic model. No legacy structural
// translator is kept: schema validation and this lowering consume the same
// generated document.
func decodeConnectionResource(path string, content []byte, metadata metadata) (semanticmodel.Connection, error) {
	var authored projectcontracts.Connection
	if err := configschema.DecodeResource(configschema.KindConnection, path, content, &authored); err != nil {
		return semanticmodel.Connection{}, err
	}
	// Generated variants intentionally expose no shared option interface. The
	// type switch keeps the closed union explicit and mechanically extracts only
	// portable fields.
	kind, access, defaults, err := connectionVariant(authored.Spec.Value)
	if err != nil {
		return semanticmodel.Connection{}, err
	}
	return semanticmodel.Connection{
		Kind: kind, Description: metadata.Description,
		Access:         access,
		ReaderDefaults: defaults,
	}, nil
}

func connectionVariant(value projectcontracts.ConnectionSpecVariant) (string, semanticmodel.ConnectionAccess, *projectcontracts.ReaderDefaults, error) {
	switch variant := value.(type) {
	case *projectcontracts.ManagedConnection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.S3Connection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.R2Connection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.GCSConnection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.HTTPConnection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.AzureBlobConnection:
		return variant.Type, lowerConnectionAccess(variant.Access), variant.Defaults, nil
	case *projectcontracts.PostgresConnection:
		return variant.Type, "", nil, nil
	case *projectcontracts.MySQLConnection:
		return variant.Type, "", nil, nil
	case *projectcontracts.SQLiteConnection:
		return variant.Type, "", nil, nil
	case *projectcontracts.DuckLakeConnection:
		return variant.Type, "", nil, nil
	case *projectcontracts.QuackConnection:
		return variant.Type, "", nil, nil
	case nil:
		return "", "", nil, fmt.Errorf("connection spec variant is required")
	default:
		return "", "", nil, fmt.Errorf("unsupported connection spec variant %T", value)
	}
}

func lowerConnectionAccess(value *projectcontracts.PublicAccess) semanticmodel.ConnectionAccess {
	if value == nil {
		return ""
	}
	return semanticmodel.ConnectionAccess(*value)
}

func decodeSourceResource(path string, content []byte, metadata metadata) (semanticmodel.Source, error) {
	var authored projectcontracts.Source
	if err := configschema.DecodeResource(configschema.KindSource, path, content, &authored); err != nil {
		return semanticmodel.Source{}, err
	}
	source := semanticmodel.Source{Connection: authored.Spec.Connection, Description: metadata.Description, Fields: map[string]semanticmodel.SourceField{}}
	switch location := authored.Spec.Location.Value.(type) {
	case *projectcontracts.SourceLocationPathVariant:
		path, format, err := lowerPathLocation(&location.PathSourceLocation)
		if err != nil {
			return semanticmodel.Source{}, fmt.Errorf("decode path location: %w", err)
		}
		source.LocationType, source.Path, source.Format = semanticmodel.KindPath, path, format
		source.PathLocation = &location.PathSourceLocation
	case *projectcontracts.SourceLocationRelationVariant:
		source.LocationType, source.Catalog, source.SchemaName, source.RelationName = semanticmodel.KindObject, optionalString(location.Catalog), optionalString(location.Schema), location.Name
		parts := make([]string, 0, 3)
		for _, part := range []string{source.Catalog, source.SchemaName, source.RelationName} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		source.Object = strings.Join(parts, ".")
	default:
		return semanticmodel.Source{}, fmt.Errorf("source location variant is required")
	}
	if authored.Spec.Schema != nil {
		mode, fields, err := sourceSchema(authored.Spec.Schema)
		if err != nil {
			return semanticmodel.Source{}, err
		}
		source.SchemaMode, source.Fields = mode, fields
	}
	if authored.Spec.Freshness != nil {
		freshness, err := lowerSourceFreshness(authored.Spec.Freshness)
		if err != nil {
			return semanticmodel.Source{}, err
		}
		schemaMode := strings.ToLower(strings.TrimSpace(source.SchemaMode))
		if schemaMode == "" {
			schemaMode = "inferred"
		}
		if freshness != nil && freshness.Basis == "field" && schemaMode != "inferred" {
			if _, ok := source.Fields[freshness.Field]; !ok {
				return semanticmodel.Source{}, fmt.Errorf("source freshness field %q is not declared in source schema", freshness.Field)
			}
		}
		source.Freshness = freshness
	}
	return source, nil
}

// decodeModelResource is the sole model authoring boundary. The generated
// TypeSpec DTO owns the closed definition union; compiler state receives only
// the lowered runtime representation. SQL dependencies are intentionally not
// accepted here and are derived later by the canonical AST analyzer.
func decodeModelResource(path string, content []byte, metadata metadata) (semanticmodel.Table, *semanticmodel.AIContext, error) {
	table, aiContext, _, err := decodeModelResourceWithDefinition(path, content, metadata)
	return table, aiContext, err
}

// decodeSemanticModelResource is the sole SemanticModel authoring boundary.
// The TypeSpec-generated document remains intact in compiler state until the
// project graph is lowered into analytics runtime types.
func decodeSemanticModelResource(path string, content []byte) (projectcontracts.SemanticModelSpec, *semanticmodel.AIContext, error) {
	var authored projectcontracts.SemanticModel
	if err := configschema.DecodeResource(configschema.KindSemanticModel, path, content, &authored); err != nil {
		return projectcontracts.SemanticModelSpec{}, nil, err
	}
	return authored.Spec, lowerAIContext(authored.AiContext), nil
}

// rejectSemanticAccessPolicy prevents policy-bearing generated fields from
// being silently discarded while the runtime policy compiler is still
// pending. Keep this check at the generated-to-runtime boundary so every
// compiler path has the same fail-closed behavior.
func rejectSemanticAccessPolicy(spec projectcontracts.SemanticModelSpec) error {
	const pending = "compiled access-policy support is not available"
	if spec.AccessGrants != nil {
		return fmt.Errorf("SemanticModel spec accessGrants: %s", pending)
	}
	datasetNames := make([]string, 0, len(spec.Datasets))
	for name := range spec.Datasets {
		datasetNames = append(datasetNames, name)
	}
	sort.Strings(datasetNames)
	for _, name := range datasetNames {
		dataset := spec.Datasets[name]
		if dataset.RequiredAccessGrants != nil {
			return fmt.Errorf("SemanticModel dataset %q requiredAccessGrants: %s", name, pending)
		}
		if dataset.AccessFilters != nil {
			return fmt.Errorf("SemanticModel dataset %q accessFilters: %s", name, pending)
		}
	}
	if spec.Dimensions != nil {
		dimensionNames := make([]string, 0, len(*spec.Dimensions))
		for name := range *spec.Dimensions {
			dimensionNames = append(dimensionNames, name)
		}
		sort.Strings(dimensionNames)
		for _, name := range dimensionNames {
			dimension := (*spec.Dimensions)[name]
			if dimension.RequiredAccessGrants != nil {
				return fmt.Errorf("SemanticModel dimension %q requiredAccessGrants: %s", name, pending)
			}
		}
	}
	metricNames := make([]string, 0, len(spec.Metrics))
	for name := range spec.Metrics {
		metricNames = append(metricNames, name)
	}
	sort.Strings(metricNames)
	for _, name := range metricNames {
		metric := spec.Metrics[name]
		var required *[]string
		switch variant := metric.Value.(type) {
		case *projectcontracts.SemanticMetricAggregateVariant:
			required = variant.RequiredAccessGrants
		case *projectcontracts.SemanticMetricDerivedVariant:
			required = variant.RequiredAccessGrants
		case *projectcontracts.SemanticMetricRatioVariant:
			required = variant.RequiredAccessGrants
		}
		if required != nil {
			return fmt.Errorf("SemanticModel metric %q requiredAccessGrants: %s", name, pending)
		}
	}
	return nil
}

func lowerSemanticDatasets(values map[string]projectcontracts.SemanticDataset) map[string]semanticmodel.SemanticDatasetSpec {
	result := make(map[string]semanticmodel.SemanticDatasetSpec, len(values))
	for name, value := range values {
		result[name] = semanticmodel.SemanticDatasetSpec{
			Model:                value.Model,
			DefaultTimeDimension: optionalString(value.DefaultTimeDimension),
			DisplayName:          optionalString(value.DisplayName),
			Description:          optionalString(value.Description),
			AIContext:            lowerAIContext(value.AiContext),
		}
	}
	return result
}

func lowerSemanticRelationships(values *map[string]projectcontracts.SemanticRelationship) (map[string]semanticmodel.RelationshipSpec, error) {
	if values == nil {
		return nil, nil
	}
	result := make(map[string]semanticmodel.RelationshipSpec, len(*values))
	for name, value := range *values {
		from, err := lowerSemanticRelationshipEndpoint(value.From)
		if err != nil {
			return nil, fmt.Errorf("relationship %q from: %w", name, err)
		}
		to, err := lowerSemanticRelationshipEndpoint(value.To)
		if err != nil {
			return nil, fmt.Errorf("relationship %q to: %w", name, err)
		}
		result[name] = semanticmodel.RelationshipSpec{From: from, To: to, Description: optionalString(value.Description), AIContext: lowerAIContext(value.AiContext)}
	}
	return result, nil
}

func lowerSemanticRelationshipEndpoint(value projectcontracts.SemanticRelationshipEndpoint) (semanticmodel.RelationshipEndpointSpec, error) {
	switch variant := value.Value.(type) {
	case *projectcontracts.NamedSemanticRelationshipEndpoint:
		return semanticmodel.RelationshipEndpointSpec{Dataset: variant.Dataset, Entity: variant.Entity}, nil
	case *projectcontracts.FieldsSemanticRelationshipEndpoint:
		return semanticmodel.RelationshipEndpointSpec{Dataset: variant.Dataset, Fields: append([]string(nil), variant.Fields...)}, nil
	case nil:
		return semanticmodel.RelationshipEndpointSpec{}, fmt.Errorf("endpoint variant is required")
	default:
		return semanticmodel.RelationshipEndpointSpec{}, fmt.Errorf("unsupported endpoint variant %T", value.Value)
	}
}

func lowerSemanticDimensions(values *map[string]projectcontracts.SemanticDimension) map[string]semanticmodel.SemanticDimensionSpec {
	if values == nil {
		return nil
	}
	result := make(map[string]semanticmodel.SemanticDimensionSpec, len(*values))
	for name, value := range *values {
		bindings := make(map[string]semanticmodel.DimensionBinding, len(value.Bindings))
		for dataset, binding := range value.Bindings {
			bindings[dataset] = semanticmodel.DimensionBinding{Field: binding.Field, Path: optionalStrings(binding.Path)}
		}
		dimension := semanticmodel.SemanticDimensionSpec{
			Label: optionalString(value.Label), Description: optionalString(value.Description), AIContext: lowerAIContext(value.AiContext),
			Datatype: semanticmodel.LogicalDataType(value.Datatype), Bindings: bindings,
		}
		if value.Time != nil {
			dimension.Time = &semanticmodel.TimeSemanticsSpec{
				NativeGrain: value.Time.NativeGrain, Grains: append([]string(nil), value.Time.Grains...),
				Calendar: optionalString(value.Time.Calendar), Timezone: optionalString(value.Time.Timezone),
			}
		}
		result[name] = dimension
	}
	return result
}

func lowerSemanticFilters(values *map[string]projectcontracts.SemanticFilter) (map[string]semanticmodel.SemanticFilterSpec, error) {
	if values == nil {
		return nil, nil
	}
	result := make(map[string]semanticmodel.SemanticFilterSpec, len(*values))
	for name, value := range *values {
		filter, err := lowerSemanticFilter(value)
		if err != nil {
			return nil, fmt.Errorf("filter %q: %w", name, err)
		}
		result[name] = filter
	}
	return result, nil
}

func lowerSemanticFilter(value projectcontracts.SemanticFilter) (semanticmodel.SemanticFilterSpec, error) {
	lowerLeaf := func(field, operator string, literal any, path *[]string, aiContext *projectcontracts.AIContext) semanticmodel.SemanticFilterSpec {
		return semanticmodel.SemanticFilterSpec{Field: field, Operator: operator, Value: literal, Path: optionalStrings(path), AIContext: lowerAIContext(aiContext)}
	}
	switch variant := value.Value.(type) {
	case *projectcontracts.EqualsSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, variant.Value, variant.Path, variant.AiContext), nil
	case *projectcontracts.NotEqualsSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, variant.Value, variant.Path, variant.AiContext), nil
	case *projectcontracts.InSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, append([]any(nil), variant.Value...), variant.Path, variant.AiContext), nil
	case *projectcontracts.NotInSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, append([]any(nil), variant.Value...), variant.Path, variant.AiContext), nil
	case *projectcontracts.LessThanSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, variant.Value, variant.Path, variant.AiContext), nil
	case *projectcontracts.LessThanOrEqualSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, variant.Value, variant.Path, variant.AiContext), nil
	case *projectcontracts.GreaterThanSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, variant.Value, variant.Path, variant.AiContext), nil
	case *projectcontracts.GreaterThanOrEqualSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, variant.Value, variant.Path, variant.AiContext), nil
	case *projectcontracts.IsNullSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, nil, variant.Path, variant.AiContext), nil
	case *projectcontracts.IsNotNullSemanticFilter:
		return lowerLeaf(variant.Field, variant.Operator, nil, variant.Path, variant.AiContext), nil
	case *projectcontracts.AllSemanticFilter:
		children, err := lowerSemanticFilterList(variant.All)
		return semanticmodel.SemanticFilterSpec{All: children}, err
	case *projectcontracts.AnySemanticFilter:
		children, err := lowerSemanticFilterList(variant.Any)
		return semanticmodel.SemanticFilterSpec{Any: children}, err
	case *projectcontracts.NotSemanticFilter:
		child, err := lowerSemanticFilter(variant.Not)
		return semanticmodel.SemanticFilterSpec{Not: &child}, err
	case nil:
		return semanticmodel.SemanticFilterSpec{}, fmt.Errorf("filter variant is required")
	default:
		return semanticmodel.SemanticFilterSpec{}, fmt.Errorf("unsupported filter variant %T", value.Value)
	}
}

func lowerSemanticFilterList(values []projectcontracts.SemanticFilter) ([]semanticmodel.SemanticFilterSpec, error) {
	result := make([]semanticmodel.SemanticFilterSpec, 0, len(values))
	for index, value := range values {
		filter, err := lowerSemanticFilter(value)
		if err != nil {
			return nil, fmt.Errorf("child %d: %w", index, err)
		}
		result = append(result, filter)
	}
	return result, nil
}

func lowerSemanticMetrics(values map[string]projectcontracts.SemanticMetric) (map[string]semanticmodel.SemanticMetricSpec, error) {
	result := make(map[string]semanticmodel.SemanticMetricSpec, len(values))
	for name, value := range values {
		metric := semanticmodel.SemanticMetricSpec{}
		switch variant := value.Value.(type) {
		case *projectcontracts.SemanticMetricAggregateVariant:
			metric.Type, metric.Dataset, metric.Aggregation = variant.Type, variant.Dataset, variant.Aggregation
			metric.Input = &semanticmodel.MetricInput{Field: variant.Input.Field}
			metric.Where, metric.Empty, metric.TimeDimension = optionalStrings(variant.Where), optionalString(variant.Empty), optionalString(variant.TimeDimension)
			lowerSemanticMetricCommon(&metric, variant.Label, variant.Description, variant.AiContext, variant.Unit, variant.Format, variant.Hidden)
		case *projectcontracts.SemanticMetricDerivedVariant:
			metric.Type, metric.Expression = variant.Type, variant.Expression
			lowerSemanticMetricCommon(&metric, variant.Label, variant.Description, variant.AiContext, variant.Unit, variant.Format, variant.Hidden)
		case *projectcontracts.SemanticMetricRatioVariant:
			metric.Type, metric.Numerator, metric.Denominator = variant.Type, variant.Numerator, variant.Denominator
			lowerSemanticMetricCommon(&metric, variant.Label, variant.Description, variant.AiContext, variant.Unit, variant.Format, variant.Hidden)
		case nil:
			return nil, fmt.Errorf("metric %q variant is required", name)
		default:
			return nil, fmt.Errorf("metric %q has unsupported variant %T", name, value.Value)
		}
		result[name] = metric
	}
	return result, nil
}

func lowerSemanticMetricCommon(metric *semanticmodel.SemanticMetricSpec, label, description *string, aiContext *projectcontracts.AIContext, unit, format *string, hidden *bool) {
	metric.Label = optionalString(label)
	metric.Description = optionalString(description)
	metric.AIContext = lowerAIContext(aiContext)
	metric.Unit = optionalString(unit)
	metric.Format = optionalString(format)
	if hidden != nil {
		metric.Hidden = *hidden
	}
}

// decodeModelResourceWithDefinition lowers the executable model contract while
// retaining the non-secret authored definition union for the project detail
// read model. Runtime consumers receive only table.Execution; the authored
// value is carried separately so target binding can never rewrite what the
// browser presents as source.
func decodeModelResourceWithDefinition(path string, content []byte, metadata metadata) (semanticmodel.Table, *semanticmodel.AIContext, projectmanifest.AuthoredModelDefinition, error) {
	var authored projectcontracts.Model
	if err := configschema.DecodeResource(configschema.KindModel, path, content, &authored); err != nil {
		return semanticmodel.Table{}, nil, projectmanifest.AuthoredModelDefinition{}, err
	}
	table := semanticmodel.Table{
		Entities:    map[string]semanticmodel.EntityDefinition{},
		Dimensions:  map[string]semanticmodel.MetricDimension{},
		Columns:     map[string]semanticmodel.ModelColumn{},
		Description: metadata.Description,
	}
	for name, entity := range authored.Spec.Entities {
		table.Entities[name] = semanticmodel.EntityDefinition{
			Type:        entity.Type,
			Fields:      append([]string(nil), entity.Fields...),
			Description: optionalString(entity.Description),
			AIContext:   lowerAIContext(entity.AiContext),
		}
	}
	table.GrainEntity = authored.Spec.Grain.Entity
	fields := map[string]projectcontracts.ModelField{}
	if authored.Spec.Fields != nil {
		fields = *authored.Spec.Fields
	}
	for name, field := range fields {
		datatype := optionalString(field.Datatype)
		table.Dimensions[name] = semanticmodel.MetricDimension{
			Label:       optionalString(field.Label),
			Description: optionalString(field.Description),
			Type:        canonicalDimensionTypeName(datatype),
			Datatype:    semanticmodel.LogicalDataType(datatype),
			AIContext:   lowerAIContext(field.AiContext),
		}
		table.Columns[name] = semanticmodel.ModelColumn{
			Name:        name,
			Field:       name,
			Type:        canonicalDimensionTypeName(datatype),
			Datatype:    semanticmodel.LogicalDataType(datatype),
			Description: optionalString(field.Description),
			AIContext:   lowerAIContext(field.AiContext),
		}
	}
	definition, err := modelDefinition(authored.Spec.Definition)
	if err != nil {
		return semanticmodel.Table{}, nil, projectmanifest.AuthoredModelDefinition{}, err
	}
	table.Execution.Source = definition.source
	table.Execution.SQL = definition.sql
	if definition.source != "" {
		table.SourceDependencies = []string{definition.source}
	}
	table.Checks, err = lowerModelChecks(authored.Spec.Checks)
	if err != nil {
		return semanticmodel.Table{}, nil, projectmanifest.AuthoredModelDefinition{}, err
	}
	return table, lowerAIContext(authored.AiContext), projectmanifest.AuthoredModelDefinition{Type: definition.kind, Source: definition.source, SQL: definition.sql}, nil
}

type loweredModelDefinition struct {
	kind   string
	source string
	sql    string
}

func modelDefinition(value projectcontracts.ModelDefinition) (loweredModelDefinition, error) {
	switch variant := value.Value.(type) {
	case *projectcontracts.DirectModelDefinition:
		source := strings.TrimSpace(variant.Source)
		if source == "" {
			return loweredModelDefinition{}, fmt.Errorf("direct model definition source is required")
		}
		return loweredModelDefinition{kind: "direct", source: source}, nil
	case *projectcontracts.SQLModelDefinition:
		sql := strings.TrimSpace(variant.SQL)
		if sql == "" {
			return loweredModelDefinition{}, fmt.Errorf("sql model definition sql is required")
		}
		return loweredModelDefinition{kind: "sql", sql: sql}, nil
	case nil:
		return loweredModelDefinition{}, fmt.Errorf("model definition variant is required")
	default:
		return loweredModelDefinition{}, fmt.Errorf("unsupported model definition variant %T", value.Value)
	}
}

func lowerAIContext(value *projectcontracts.AIContext) *semanticmodel.AIContext {
	if value == nil {
		return nil
	}
	return &semanticmodel.AIContext{
		Instructions: optionalString(value.Instructions),
		Synonyms:     optionalStrings(value.Synonyms),
		Examples:     optionalStrings(value.Examples),
	}
}

func optionalStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return append([]string(nil), (*value)...)
}

func lowerModelChecks(value *[]projectcontracts.ModelCheck) ([]semanticmodel.ModelCheck, error) {
	if value == nil {
		return nil, nil
	}
	checks := make([]semanticmodel.ModelCheck, 0, len(*value))
	for index, check := range *value {
		lowered := semanticmodel.ModelCheck{}
		switch variant := check.Value.(type) {
		case *projectcontracts.ModelCheckNonNullVariant:
			if strings.TrimSpace(variant.Field) == "" {
				return nil, fmt.Errorf("checks[%d] non_null requires field", index)
			}
			lowered.Type, lowered.Field, lowered.Severity = variant.Type, variant.Field, optionalString(variant.Severity)
		case *projectcontracts.ModelCheckUniqueVariant:
			if len(variant.Fields) == 0 {
				return nil, fmt.Errorf("checks[%d] unique requires fields", index)
			}
			seenFields := make(map[string]struct{}, len(variant.Fields))
			for _, field := range variant.Fields {
				if strings.TrimSpace(field) == "" {
					return nil, fmt.Errorf("checks[%d] unique fields must be non-empty", index)
				}
				if _, exists := seenFields[field]; exists {
					return nil, fmt.Errorf("checks[%d] unique contains duplicate field %q", index, field)
				}
				seenFields[field] = struct{}{}
			}
			lowered.Type, lowered.Fields, lowered.Severity = variant.Type, append([]string(nil), variant.Fields...), optionalString(variant.Severity)
		case *projectcontracts.ModelCheckAcceptedValuesVariant:
			if strings.TrimSpace(variant.Field) == "" || len(variant.Values) == 0 {
				return nil, fmt.Errorf("checks[%d] accepted_values requires field and values", index)
			}
			seenValues := make(map[string]struct{}, len(variant.Values))
			for _, accepted := range variant.Values {
				if _, exists := seenValues[accepted]; exists {
					return nil, fmt.Errorf("checks[%d] accepted_values contains duplicate value %q", index, accepted)
				}
				seenValues[accepted] = struct{}{}
			}
			lowered.Type, lowered.Field, lowered.Values, lowered.Severity = variant.Type, variant.Field, append([]string(nil), variant.Values...), optionalString(variant.Severity)
		case *projectcontracts.ModelCheckRelationshipVariant:
			if strings.TrimSpace(variant.Field) == "" || strings.TrimSpace(variant.To) == "" {
				return nil, fmt.Errorf("checks[%d] relationship requires field and to", index)
			}
			lowered.Type, lowered.Field, lowered.To, lowered.Severity = variant.Type, variant.Field, variant.To, optionalString(variant.Severity)
		case *projectcontracts.ModelCheckRowCountVariant:
			if variant.Minimum == nil && variant.Maximum == nil {
				return nil, fmt.Errorf("checks[%d] row_count requires minimum or maximum", index)
			}
			if variant.Minimum != nil && *variant.Minimum < 0 {
				return nil, fmt.Errorf("checks[%d] row_count minimum must be non-negative", index)
			}
			if variant.Maximum != nil && *variant.Maximum < 0 {
				return nil, fmt.Errorf("checks[%d] row_count maximum must be non-negative", index)
			}
			if variant.Minimum != nil && variant.Maximum != nil && *variant.Minimum > *variant.Maximum {
				return nil, fmt.Errorf("checks[%d] row_count minimum exceeds maximum", index)
			}
			lowered.Type, lowered.Minimum, lowered.Maximum, lowered.Severity = variant.Type, variant.Minimum, variant.Maximum, optionalString(variant.Severity)
		case nil:
			return nil, fmt.Errorf("model check variant is required")
		default:
			return nil, fmt.Errorf("unsupported model check variant %T", check.Value)
		}
		if lowered.Severity != "" && !strings.EqualFold(lowered.Severity, "warning") && !strings.EqualFold(lowered.Severity, "error") {
			return nil, fmt.Errorf("checks[%d] severity must be warning or error", index)
		}
		checks = append(checks, lowered)
	}
	return checks, nil
}

func lowerSourceFreshness(value *projectcontracts.SourceFreshness) (*semanticmodel.SourceFreshnessSpec, error) {
	if value == nil {
		return nil, nil
	}
	result := &semanticmodel.SourceFreshnessSpec{}
	switch variant := value.Value.(type) {
	case *projectcontracts.SourceFreshnessFieldVariant:
		if strings.TrimSpace(variant.Field) == "" {
			return nil, fmt.Errorf("source freshness field is required")
		}
		result.Basis, result.Field = "field", variant.Field
		var err error
		result.WarningAfter, err = lowerFreshnessDuration(variant.WarningAfter)
		if err != nil {
			return nil, fmt.Errorf("source freshness warningAfter: %w", err)
		}
		result.ErrorAfter, err = lowerFreshnessDuration(variant.ErrorAfter)
		if err != nil {
			return nil, fmt.Errorf("source freshness errorAfter: %w", err)
		}
	case *projectcontracts.SourceFreshnessRevisionVariant:
		if strings.TrimSpace(variant.Revision) == "" {
			return nil, fmt.Errorf("source freshness revision is required")
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, variant.Revision)
		if parseErr != nil {
			return nil, fmt.Errorf("source freshness revision must be RFC3339 utcDateTime: %w", parseErr)
		}
		if parsed.Location() != time.UTC {
			return nil, fmt.Errorf("source freshness revision must use UTC")
		}
		parsed = parsed.UTC()
		result.Basis, result.Revision, result.RevisionAt = "revision", parsed.Format(time.RFC3339Nano), &parsed
		var err error
		result.WarningAfter, err = lowerFreshnessDuration(variant.WarningAfter)
		if err != nil {
			return nil, fmt.Errorf("source freshness warningAfter: %w", err)
		}
		result.ErrorAfter, err = lowerFreshnessDuration(variant.ErrorAfter)
		if err != nil {
			return nil, fmt.Errorf("source freshness errorAfter: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported source freshness variant %T", value.Value)
	}
	if result.WarningAfter == nil && result.ErrorAfter == nil {
		return nil, fmt.Errorf("source freshness requires warningAfter or errorAfter")
	}
	if result.WarningAfter != nil && result.ErrorAfter != nil && result.WarningAfter.Duration() >= result.ErrorAfter.Duration() {
		return nil, fmt.Errorf("source freshness warningAfter must be shorter than errorAfter")
	}
	return result, nil
}

func lowerFreshnessDuration(value *projectcontracts.FreshnessDuration) (*semanticmodel.FreshnessDurationSpec, error) {
	if value == nil {
		return nil, nil
	}
	if value.Amount <= 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}
	unit := strings.TrimSpace(strings.ToLower(value.Unit))
	switch unit {
	case "second", "minute", "hour", "day":
	default:
		return nil, fmt.Errorf("unit %q is not supported", value.Unit)
	}
	multiplier := int64(time.Second)
	switch unit {
	case "minute":
		multiplier *= 60
	case "hour":
		multiplier *= 60 * 60
	case "day":
		multiplier *= 24 * 60 * 60
	}
	if value.Amount > int64(^uint64(0)>>1)/multiplier {
		return nil, fmt.Errorf("amount overflows time.Duration")
	}
	return &semanticmodel.FreshnessDurationSpec{Amount: value.Amount, Unit: unit}, nil
}

func sourceSchema(value *projectcontracts.SourceSchema) (string, map[string]semanticmodel.SourceField, error) {
	if value == nil {
		return "inferred", map[string]semanticmodel.SourceField{}, nil
	}
	fields := map[string]semanticmodel.SourceField{}
	switch variant := value.Value.(type) {
	case *projectcontracts.SourceSchemaInferredVariant:
		return variant.Mode, fields, nil
	case *projectcontracts.SourceSchemaCompatibleVariant:
		for name, field := range variant.Fields {
			fields[name] = lowerSourceField(name, field)
		}
		return variant.Mode, fields, nil
	case *projectcontracts.SourceSchemaStrictVariant:
		for name, field := range variant.Fields {
			fields[name] = lowerSourceField(name, field)
		}
		return variant.Mode, fields, nil
	default:
		return "", nil, fmt.Errorf("unsupported source schema variant %T", value.Value)
	}
}

func lowerSourceField(name string, field projectcontracts.SourceSchemaField) semanticmodel.SourceField {
	return semanticmodel.SourceField{Field: name, Name: name, Datatype: semanticmodel.LogicalDataType(field.Datatype), Nullable: field.Nullable, Description: optionalString(field.Description)}
}

func lowerPathLocation(value *projectcontracts.PathSourceLocation) (string, string, error) {
	if value == nil {
		return "", "", fmt.Errorf("path location is required")
	}
	switch variant := value.Value.(type) {
	case *projectcontracts.CSVPathSourceLocation:
		return variant.Path, "csv", nil
	case *projectcontracts.JSONPathSourceLocation:
		return variant.Path, "json", nil
	case *projectcontracts.ParquetPathSourceLocation:
		return variant.Path, "parquet", nil
	case *projectcontracts.ExcelPathSourceLocation:
		return variant.Path, "excel", nil
	case *projectcontracts.TextPathSourceLocation:
		return variant.Path, "text", nil
	case *projectcontracts.BlobPathSourceLocation:
		return variant.Path, "blob", nil
	case *projectcontracts.VortexPathSourceLocation:
		return variant.Path, "vortex", nil
	case *projectcontracts.DeltaPathSourceLocation:
		return variant.Path, "delta", nil
	case *projectcontracts.IcebergPathSourceLocation:
		return variant.Path, "iceberg", nil
	case *projectcontracts.LancePathSourceLocation:
		return variant.Path, "lance", nil
	case nil:
		return "", "", fmt.Errorf("path location variant is required")
	default:
		return "", "", fmt.Errorf("unsupported path location variant %T", variant)
	}
}

func structMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
