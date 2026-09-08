package contractodcs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project/contractprojection"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
)

var odcsShorthandReference = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)

type projectionEnvelope struct {
	Profile  string `json:"profile"`
	Kind     string `json:"kind"`
	Metadata struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Contract struct {
			Version string `json:"version"`
		} `json:"contract"`
	} `json:"metadata"`
}

// Export maps one immutable FAI-622 publication into a deterministic ODCS
// 3.1.0 JSON document plus machine-readable mapping and loss reports.
func Export(publication identityledger.ContractPublication) (Result, error) {
	envelope, err := validatePublication(publication)
	if err != nil {
		return Result{}, err
	}
	reports := newReportBuilder()
	for _, id := range []string{"contract.api-version", "contract.extension", "contract.id", "contract.kind", "contract.name", "contract.status", "contract.version"} {
		reports.mapped(id)
	}
	reports.loss(LossEntry{
		Kind: LossDegraded, SourceField: "publication.publishedAt", ODCSTargetField: "status",
		Reason:              "ODCS combines publication and activation lifecycle concepts; published evidence is exported as active without claiming serving activation.",
		CompatibilityImpact: "Document conformance is preserved, but lifecycle round-trip conformance is unavailable.",
	})
	document := Document{
		Version: envelope.Metadata.Contract.Version, Kind: Kind, APIVersion: APIVersion,
		ID: publication.InstanceID + "/" + publication.AuthoredID.String(), Name: envelope.Metadata.Name, Status: "active",
		CustomProperties: []CustomProperty{{
			Property:    ExtensionProperty,
			Value:       ExtensionEvidence{Namespace: ExtensionNamespace, ResourceKind: envelope.Kind, ProjectionProfile: publication.ProjectionProfile, ProjectionDigest: publication.Digest},
			Description: "Immutable LeapView publication provenance; not executable contract semantics.",
		}},
	}

	var mappingErr error
	switch envelope.Kind {
	case "Source":
		projection, err := contractprojection.DecodeSourcePublication(publication.CanonicalBytes)
		if err != nil {
			return Result{}, fmt.Errorf("%w: decode Source projection: %v", ErrInvalidPublication, err)
		}
		mappingErr = mapSource(projection, &document, reports)
	case "Model":
		projection, err := contractprojection.DecodeModelPublication(publication.CanonicalBytes)
		if err != nil {
			return Result{}, fmt.Errorf("%w: decode Model projection: %v", ErrInvalidPublication, err)
		}
		mappingErr = mapModel(projection, &document, reports)
	case "SemanticModel":
		reports.loss(LossEntry{
			Kind: LossUnsupported, SourceField: "contract", Reason: "SemanticModel interoperability remains governed by the pinned Apache Ossie adapter, not ODCS.",
			CompatibilityImpact: "No ODCS document is emitted; use the existing Ossie export boundary.",
		})
		mappingErr = ErrUnsupportedMapping
	default:
		return Result{}, fmt.Errorf("%w: unsupported projection kind %q", ErrInvalidPublication, envelope.Kind)
	}
	mappingReport, lossReport, reportErr := reports.reports()
	if reportErr != nil {
		return Result{}, reportErr
	}
	result := Result{MappingReport: mappingReport, LossReport: lossReport}
	if mappingErr != nil {
		return result, &MappingError{Report: lossReport}
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return result, fmt.Errorf("marshal ODCS 3.1 export: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := Validate(encoded); err != nil {
		return result, err
	}
	result.Document = encoded
	return result, nil
}

func validatePublication(publication identityledger.ContractPublication) (projectionEnvelope, error) {
	if strings.TrimSpace(publication.InstanceID) == "" || publication.AuthoredID.Validate() != nil || publication.PublishedAt.IsZero() ||
		publication.ProjectionProfile != contractprojection.Profile || len(publication.CanonicalBytes) == 0 ||
		platformdigest.ValidateSHA256Identity(publication.Digest) != nil {
		return projectionEnvelope{}, ErrInvalidPublication
	}
	var envelope projectionEnvelope
	if err := json.Unmarshal(publication.CanonicalBytes, &envelope); err != nil {
		return projectionEnvelope{}, fmt.Errorf("%w: decode envelope: %v", ErrInvalidPublication, err)
	}
	if envelope.Profile != contractprojection.Profile || envelope.Metadata.ID != publication.AuthoredID.String() || envelope.Metadata.Name == "" ||
		envelope.Metadata.Contract.Version != publication.Version {
		return projectionEnvelope{}, fmt.Errorf("%w: publication fields disagree with canonical projection", ErrInvalidPublication)
	}
	wantKind := map[projectgraph.Kind]string{projectgraph.KindSource: "Source", projectgraph.KindModel: "Model", projectgraph.KindSemanticModel: "SemanticModel"}[publication.ResourceKind]
	if wantKind == "" || envelope.Kind != wantKind {
		return projectionEnvelope{}, fmt.Errorf("%w: resource kind disagrees with canonical projection", ErrInvalidPublication)
	}
	return envelope, nil
}

func mapSource(projection contractprojection.SourceView, document *Document, reports *reportBuilder) error {
	reports.mapped("schema.object")
	reports.loss(LossEntry{
		Kind: LossDropped, SourceField: "contract.schema.mode", Reason: "ODCS 3.1 has no equivalent for LeapView declared-versus-inferred schema mode.",
		CompatibilityImpact: "Schema-mode round-trip conformance is unavailable.",
	})
	object := SchemaObject{Name: projection.Metadata.Name, LogicalType: "object"}
	if projection.Contract.Schema.Fields != nil {
		properties, err := mapFields(*projection.Contract.Schema.Fields, "contract.schema.fields", reports)
		if err != nil {
			return err
		}
		object.Properties = properties
	}
	document.Schema = []SchemaObject{object}
	if projection.Contract.Freshness != nil {
		reports.mapped("source.freshness")
		freshness := projection.Contract.Freshness
		for _, threshold := range []struct {
			id    string
			value *contractprojection.Duration
		}{
			{id: "freshness-warning", value: freshness.WarningAfter},
			{id: "freshness-error", value: freshness.ErrorAfter},
		} {
			if threshold.value == nil {
				continue
			}
			entry := SLAProperty{ID: threshold.id, Property: "freshness", Value: threshold.value.Amount, Unit: threshold.value.Unit}
			if freshness.Field != nil {
				entry.Element = projection.Metadata.Name + "." + *freshness.Field
			}
			document.SLAProperties = append(document.SLAProperties, entry)
		}
		reports.loss(LossEntry{
			Kind: LossDegraded, SourceField: "contract.freshness", ODCSTargetField: "slaProperties[]",
			Reason:              "ODCS represents both LeapView warning and error thresholds as generic freshness SLA entries without normative severity.",
			CompatibilityImpact: "Threshold values remain visible, but freshness round-trip conformance is unavailable.",
		})
	}
	return nil
}

func mapModel(projection contractprojection.ModelView, document *Document, reports *reportBuilder) error {
	reports.mapped("schema.object")
	reports.loss(LossEntry{
		Kind: LossDropped, SourceField: "contract.definition", Reason: "Executable SQL AST and direct source bindings are never exported to ODCS.",
		CompatibilityImpact: "Transformation execution and dependency round-trip conformance are unavailable.",
	})
	properties, err := mapFields(projection.Contract.Fields, "contract.fields", reports)
	if err != nil {
		return err
	}
	object := SchemaObject{Name: projection.Metadata.Name, LogicalType: "object", Properties: properties}
	propertyIndex := map[string]*SchemaProperty{}
	for index := range object.Properties {
		propertyIndex[object.Properties[index].Name] = &object.Properties[index]
	}
	if err := mapEntities(projection, &object, propertyIndex, reports); err != nil {
		return err
	}
	if projection.Contract.Checks != nil {
		if err := mapChecks(*projection.Contract.Checks, &object, propertyIndex, reports); err != nil {
			return err
		}
	}
	document.Schema = []SchemaObject{object}
	return nil
}

func mapFields(fields map[string]contractprojection.Field, sourcePrefix string, reports *reportBuilder) ([]SchemaProperty, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	properties := make([]SchemaProperty, 0, len(names))
	for _, name := range names {
		field := fields[name]
		property := SchemaProperty{Name: name}
		reports.mapped("field.name")
		if field.Datatype != nil {
			logicalType, options, degraded, ok := logicalType(*field.Datatype)
			if !ok {
				reports.loss(LossEntry{
					Kind: LossUnsupported, SourceField: sourcePrefix + "." + name + ".datatype", ODCSTargetField: "schema[].properties[].logicalType",
					Reason:              "ODCS 3.1 has no safe logical representation for datatype " + *field.Datatype + ".",
					CompatibilityImpact: "Export is rejected instead of weakening the published datatype.",
				})
				return nil, ErrUnsupportedMapping
			}
			property.LogicalType, property.LogicalTypeOptions = logicalType, options
			reports.mapped("field.logical-type")
			if degraded {
				reports.loss(LossEntry{
					Kind: LossDegraded, SourceField: sourcePrefix + "." + name + ".datatype", ODCSTargetField: "schema[].properties[].logicalType",
					Reason:              "ODCS number does not distinguish LeapView exact Decimal from approximate Float.",
					CompatibilityImpact: "Document conformance is preserved, but numeric round-trip conformance is unavailable.",
				})
			}
		}
		if field.Nullable != nil {
			required := !*field.Nullable
			property.Required = &required
			reports.mapped("field.nullability")
		}
		if field.Classification != nil {
			property.Classification = *field.Classification
			reports.mapped("field.classification")
		}
		if field.CriticalDataElement != nil {
			property.CriticalDataElement = field.CriticalDataElement
			reports.mapped("field.critical")
		}
		if field.AuthoritativeDefinitions != nil {
			for _, definition := range *field.AuthoritativeDefinitions {
				property.AuthoritativeDefinitions = append(property.AuthoritativeDefinitions, AuthoritativeDefinition{URL: definition.URL, Type: definition.Type})
			}
			reports.mapped("field.authoritative-definitions")
		}
		if field.Deprecation != nil {
			reports.loss(LossEntry{
				Kind: LossDropped, SourceField: sourcePrefix + "." + name + ".deprecation",
				Reason:              "ODCS 3.1 has no closed field-deprecation shape and the initial profile does not invent one.",
				CompatibilityImpact: "Deprecation guidance is present only in the LeapView publication.",
			})
		}
		properties = append(properties, property)
	}
	return properties, nil
}

func logicalType(value string) (string, map[string]any, bool, bool) {
	switch value {
	case "String":
		return "string", nil, false, true
	case "Integer":
		return "integer", nil, false, true
	case "Decimal":
		return "number", nil, true, true
	case "Float":
		return "number", nil, false, true
	case "Boolean":
		return "boolean", nil, false, true
	case "Date":
		return "date", nil, false, true
	case "Time":
		return "time", nil, false, true
	case "DateTime":
		return "timestamp", map[string]any{"timezone": false}, false, true
	case "DateTimeTz":
		return "timestamp", map[string]any{"timezone": true}, false, true
	default:
		return "", nil, false, false
	}
}

func mapEntities(projection contractprojection.ModelView, object *SchemaObject, propertyIndex map[string]*SchemaProperty, reports *reportBuilder) error {
	grain, exists := projection.Contract.Entities[projection.Contract.Grain.Entity]
	if !exists {
		reports.loss(unsupported("contract.grain.entity", "Selected grain does not resolve to a projected entity."))
		return ErrUnsupportedMapping
	}
	reports.mapped("model.entity")
	for index, field := range grain.Fields {
		property := propertyIndex[field]
		if property == nil {
			reports.loss(unsupported("contract.entities."+projection.Contract.Grain.Entity+".fields", "Primary grain field does not resolve to a projected field."))
			return ErrUnsupportedMapping
		}
		primary, position := true, index+1
		property.PrimaryKey, property.PrimaryKeyPosition = &primary, &position
	}
	entityNames := make([]string, 0, len(projection.Contract.Entities))
	for name := range projection.Contract.Entities {
		entityNames = append(entityNames, name)
	}
	sort.Strings(entityNames)
	for _, name := range entityNames {
		entity := projection.Contract.Entities[name]
		if name == projection.Contract.Grain.Entity {
			continue
		}
		switch entity.Type {
		case "unique":
			if len(entity.Fields) == 1 {
				property := propertyIndex[entity.Fields[0]]
				if property == nil {
					reports.loss(unsupported("contract.entities."+name+".fields", "Unique entity field does not resolve to a projected field."))
					return ErrUnsupportedMapping
				}
				unique := true
				property.Unique = &unique
				continue
			}
			reports.loss(LossEntry{
				Kind: LossDegraded, SourceField: "contract.entities." + name, ODCSTargetField: "schema[].quality[]",
				Reason:              "Composite unique identity is represented through generic ODCS duplicateValues arguments.",
				CompatibilityImpact: "Identity fields remain visible, but entity round-trip conformance is unavailable.",
			})
			object.Quality = append(object.Quality, Quality{ID: name, Name: name, Type: "library", Metric: "duplicateValues", Arguments: map[string]any{"fields": entity.Fields}, MustBe: int64(0)})
		case "primary", "foreign", "natural":
			reports.loss(LossEntry{
				Kind: LossDropped, SourceField: "contract.entities." + name,
				Reason:              "ODCS property flags cannot preserve this non-grain LeapView entity kind without changing its meaning.",
				CompatibilityImpact: "Entity round-trip conformance is unavailable.",
			})
		default:
			reports.loss(unsupported("contract.entities."+name+".type", "Unknown projected entity type."))
			return ErrUnsupportedMapping
		}
	}
	return nil
}

func mapChecks(checks []contractprojection.ModelCheck, object *SchemaObject, propertyIndex map[string]*SchemaProperty, reports *reportBuilder) error {
	reports.mapped("model.quality")
	for _, check := range checks {
		quality := Quality{ID: check.ID, Name: check.ID, Type: "library", Severity: stringValue(check.Severity)}
		switch check.Type {
		case "non_null":
			property := propertyForCheck(check.Field, propertyIndex)
			if property == nil {
				return unsupportedCheck(check, reports)
			}
			quality.Metric, quality.MustBe = "nullValues", int64(0)
			property.Quality = append(property.Quality, quality)
		case "unique":
			if check.Fields == nil || len(*check.Fields) == 0 {
				return unsupportedCheck(check, reports)
			}
			quality.Metric, quality.MustBe = "duplicateValues", int64(0)
			if len(*check.Fields) == 1 {
				property := propertyIndex[(*check.Fields)[0]]
				if property == nil {
					return unsupportedCheck(check, reports)
				}
				property.Quality = append(property.Quality, quality)
			} else {
				quality.Arguments = map[string]any{"fields": *check.Fields}
				object.Quality = append(object.Quality, quality)
				reports.loss(degradedCheck(check, "Composite uniqueness uses generic ODCS library arguments."))
			}
		case "accepted_values":
			property := propertyForCheck(check.Field, propertyIndex)
			if property == nil || check.Values == nil || len(*check.Values) == 0 {
				return unsupportedCheck(check, reports)
			}
			quality.Metric, quality.MustBe = "invalidValues", int64(0)
			quality.Arguments = map[string]any{"validValues": *check.Values}
			property.Quality = append(property.Quality, quality)
			reports.loss(degradedCheck(check, "Accepted values use generic ODCS library arguments without a standardized argument vocabulary."))
		case "relationship":
			property := propertyForCheck(check.Field, propertyIndex)
			if property == nil || check.To == nil || !odcsShorthandReference.MatchString(*check.To) {
				return unsupportedCheck(check, reports)
			}
			// Shorthand is local to this ODCS document. A single publication
			// cannot establish another contract's identity or property authority.
			targetObject, targetField, _ := strings.Cut(*check.To, ".")
			if targetObject != object.Name || propertyIndex[targetField] == nil {
				reports.loss(unsupported("contract.checks."+check.ID, "Relationship shorthand must resolve to a property in the exported object; external contract authority is unavailable."))
				return ErrUnsupportedMapping
			}
			property.Relationships = append(property.Relationships, Relationship{Type: "foreignKey", To: *check.To})
			if check.Severity != nil {
				reports.loss(degradedCheck(check, "ODCS relationships do not retain LeapView quality severity."))
			}
		case "row_count":
			quality.Metric, quality.Unit = "rowCount", "rows"
			switch {
			case check.Minimum != nil && check.Maximum != nil && *check.Minimum > *check.Maximum:
				return unsupportedCheck(check, reports)
			case check.Minimum != nil && check.Maximum != nil && *check.Minimum == *check.Maximum:
				quality.MustBe = *check.Minimum
			case check.Minimum != nil && check.Maximum != nil:
				quality.MustBeBetween = []int64{*check.Minimum, *check.Maximum}
			case check.Minimum != nil:
				quality.MustBeGreaterOrEqualTo = check.Minimum
			case check.Maximum != nil:
				quality.MustBeLessOrEqualTo = check.Maximum
			default:
				return unsupportedCheck(check, reports)
			}
			object.Quality = append(object.Quality, quality)
		default:
			return unsupportedCheck(check, reports)
		}
	}
	return nil
}

func propertyForCheck(field *string, properties map[string]*SchemaProperty) *SchemaProperty {
	if field == nil {
		return nil
	}
	return properties[*field]
}

func unsupportedCheck(check contractprojection.ModelCheck, reports *reportBuilder) error {
	reports.loss(unsupported("contract.checks."+check.ID, "Quality semantics are incomplete or have no safe ODCS 3.1 representation."))
	return ErrUnsupportedMapping
}

func degradedCheck(check contractprojection.ModelCheck, reason string) LossEntry {
	return LossEntry{Kind: LossDegraded, SourceField: "contract.checks." + check.ID, ODCSTargetField: "schema[].quality", Reason: reason, CompatibilityImpact: "Document conformance is preserved, but quality round-trip conformance is unavailable."}
}

func unsupported(source, reason string) LossEntry {
	return LossEntry{Kind: LossUnsupported, SourceField: source, Reason: reason, CompatibilityImpact: "Export is rejected instead of silently weakening published semantics."}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
