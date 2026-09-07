package contractprojection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// DecodeSourcePublication strictly decodes canonical Source publication bytes
// into a generated read view. The result intentionally is not a Projection;
// only ProjectSource can produce a value accepted by canonicalization.
func DecodeSourcePublication(data []byte) (SourceView, error) {
	var value SourceView
	if err := decodeCanonicalPublication(data, "Source", &value); err != nil {
		return SourceView{}, err
	}
	return value, nil
}

// DecodeModelPublication strictly decodes canonical Model publication bytes
// into a generated read view.
func DecodeModelPublication(data []byte) (ModelView, error) {
	var value ModelView
	if err := decodeCanonicalPublication(data, "Model", &value); err != nil {
		return ModelView{}, err
	}
	return value, nil
}

// DecodeSemanticModelPublication strictly decodes canonical SemanticModel
// publication bytes into a generated read view.
func DecodeSemanticModelPublication(data []byte) (SemanticModelView, error) {
	var value SemanticModelView
	if err := decodeCanonicalPublication(data, "SemanticModel", &value); err != nil {
		return SemanticModelView{}, err
	}
	return value, nil
}

// DigestSourcePublication validates canonical Source publication bytes and
// returns the SHA-256 identity of those exact bytes.
func DigestSourcePublication(data []byte) (string, error) {
	if _, err := DecodeSourcePublication(data); err != nil {
		return "", err
	}
	return digestCanonicalPublication(data), nil
}

// DigestModelPublication validates canonical Model publication bytes and
// returns the SHA-256 identity of those exact bytes.
func DigestModelPublication(data []byte) (string, error) {
	if _, err := DecodeModelPublication(data); err != nil {
		return "", err
	}
	return digestCanonicalPublication(data), nil
}

// DigestSemanticModelPublication validates canonical SemanticModel publication
// bytes and returns the SHA-256 identity of those exact bytes.
func DigestSemanticModelPublication(data []byte) (string, error) {
	if _, err := DecodeSemanticModelPublication(data); err != nil {
		return "", err
	}
	return digestCanonicalPublication(data), nil
}

func digestCanonicalPublication(data []byte) string {
	return digestCanonicalBytes(data)
}

func decodeCanonicalPublication(data []byte, expectedKind string, output any) error {
	if len(data) == 0 {
		return errors.New("decode contract publication: empty bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode %s publication: %w", expectedKind, err)
	}
	if err := validatePublicationView(expectedKind, output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode %s publication: trailing JSON value", expectedKind)
		}
		return fmt.Errorf("decode %s publication: %w", expectedKind, err)
	}

	// Re-encode the generated view and canonicalize that representation. Some
	// generated union decoders own their UnmarshalJSON implementation, so an
	// unknown nested property can be accepted and then omitted by MarshalJSON.
	// Comparing the canonical DTO bytes to the input closes that publication
	// boundary: accepted bytes must be exactly the bytes represented by output.
	encoded, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("decode %s publication: encode generated view: %w", expectedKind, err)
	}
	canonical, err := canonicalPublicationBytes(encoded, expectedKind)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, data) {
		return fmt.Errorf("decode %s publication: decoded view differs from canonical bytes", expectedKind)
	}
	return nil
}

func validatePublicationView(expectedKind string, output any) error {
	switch expectedKind {
	case "Source":
		value, ok := output.(*SourceView)
		if !ok {
			return fmt.Errorf("decode Source publication: invalid generated view")
		}
		return validateSourceView(*value)
	case "Model":
		value, ok := output.(*ModelView)
		if !ok {
			return fmt.Errorf("decode Model publication: invalid generated view")
		}
		return validateModelView(*value)
	case "SemanticModel":
		value, ok := output.(*SemanticModelView)
		if !ok {
			return fmt.Errorf("decode SemanticModel publication: invalid generated view")
		}
		return validateSemanticModelView(*value)
	default:
		return fmt.Errorf("decode %s publication: unsupported kind", expectedKind)
	}
}

func validatePublicationMetadata(metadata Metadata, kind string) error {
	if !validSemanticVersion(metadata.Contract.Version) || metadata.Contract.Compatibility != "backward" {
		return fmt.Errorf("decode %s publication: invalid contract metadata", kind)
	}
	if _, err := projectgraph.NewResourceID(metadata.ID); err != nil {
		return fmt.Errorf("decode %s publication: invalid resource id: %w", kind, err)
	}
	if !validProjectionName(metadata.Name) {
		return fmt.Errorf("decode %s publication: required metadata is missing", kind)
	}
	return nil
}

func validateSourceView(value SourceView) error {
	if err := validatePublicationMetadata(value.Metadata, "Source"); err != nil {
		return err
	}
	if value.Contract.Schema.Mode != "inferred" && value.Contract.Schema.Mode != "compatible" && value.Contract.Schema.Mode != "strict" {
		return fmt.Errorf("decode Source publication: invalid schema mode %q", value.Contract.Schema.Mode)
	}
	if value.Contract.Schema.Fields != nil {
		for name, field := range *value.Contract.Schema.Fields {
			if !validProjectionIdentifier(name) {
				return fmt.Errorf("decode Source publication: empty schema field name")
			}
			if err := validateProjectionField(field); err != nil {
				return fmt.Errorf("decode Source publication field %q: %w", name, err)
			}
		}
	}
	if freshness := value.Contract.Freshness; freshness != nil {
		if freshness.Basis != "field" && freshness.Basis != "revision" {
			return fmt.Errorf("decode Source publication: invalid freshness basis %q", freshness.Basis)
		}
		if freshness.Basis == "field" && freshness.Field == nil {
			return fmt.Errorf("decode Source publication: field freshness requires field")
		}
		if freshness.Basis == "revision" && freshness.Revision == nil {
			return fmt.Errorf("decode Source publication: revision freshness requires revision")
		}
		if freshness.Field != nil && !validProjectionIdentifier(*freshness.Field) {
			return fmt.Errorf("decode Source publication: invalid freshness field")
		}
		if freshness.WarningAfter != nil {
			if err := validateProjectionDuration(*freshness.WarningAfter); err != nil {
				return err
			}
		}
		if freshness.ErrorAfter != nil {
			if err := validateProjectionDuration(*freshness.ErrorAfter); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateModelView(value ModelView) error {
	if err := validatePublicationMetadata(value.Metadata, "Model"); err != nil {
		return err
	}
	definition := value.Contract.Definition
	if definition.Type != "direct" && definition.Type != "sql" {
		return fmt.Errorf("decode Model publication: invalid definition type %q", definition.Type)
	}
	if definition.Type == "direct" && definition.Source == nil {
		return fmt.Errorf("decode Model publication: direct definition requires source")
	}
	if definition.Type == "sql" && definition.SQLAst == nil {
		return fmt.Errorf("decode Model publication: SQL definition requires sqlAst")
	}
	if definition.Source != nil {
		if _, err := projectgraph.NewResourceID(*definition.Source); err != nil {
			return fmt.Errorf("decode Model publication: invalid source resource id: %w", err)
		}
	}
	if definition.SQLAst != nil {
		if err := validateSQLAst(*definition.SQLAst); err != nil {
			return fmt.Errorf("decode Model publication: invalid sqlAst: %w", err)
		}
	}
	for name, field := range value.Contract.Fields {
		if !validProjectionIdentifier(name) {
			return fmt.Errorf("decode Model publication: empty field name")
		}
		if err := validateModelProjectionField(field); err != nil {
			return fmt.Errorf("decode Model publication field %q: %w", name, err)
		}
	}
	if value.Contract.Checks != nil {
		seenIDs := make(map[string]struct{}, len(*value.Contract.Checks))
		for _, check := range *value.Contract.Checks {
			if !validProjectionIdentifier(check.ID) {
				return fmt.Errorf("decode Model publication: invalid check id %q", check.ID)
			}
			if _, exists := seenIDs[check.ID]; exists {
				return fmt.Errorf("decode Model publication: duplicate check id %q", check.ID)
			}
			seenIDs[check.ID] = struct{}{}
			if check.Type != "non_null" && check.Type != "unique" && check.Type != "accepted_values" && check.Type != "relationship" && check.Type != "row_count" {
				return fmt.Errorf("decode Model publication: invalid check type %q", check.Type)
			}
			if check.Field != nil && !validProjectionIdentifier(*check.Field) {
				return fmt.Errorf("decode Model publication: invalid check field")
			}
			if check.To != nil && !validModelReference(*check.To) {
				return fmt.Errorf("decode Model publication: invalid relationship check target")
			}
			if check.Fields != nil {
				for _, field := range *check.Fields {
					if !validProjectionIdentifier(field) {
						return fmt.Errorf("decode Model publication: invalid check field list")
					}
				}
			}
			if check.Severity != nil && *check.Severity != "warning" && *check.Severity != "error" {
				return fmt.Errorf("decode Model publication: invalid check severity %q", *check.Severity)
			}
		}
	}
	return nil
}

func validateSemanticModelView(value SemanticModelView) error {
	if err := validatePublicationMetadata(value.Metadata, "SemanticModel"); err != nil {
		return err
	}
	for name, dataset := range value.Contract.Datasets {
		if !validProjectionIdentifier(name) {
			return fmt.Errorf("decode SemanticModel publication: invalid dataset %q", name)
		}
		if _, err := projectgraph.NewResourceID(dataset.Model); err != nil {
			return fmt.Errorf("decode SemanticModel publication dataset %q: invalid model resource id: %w", name, err)
		}
		if dataset.DefaultTimeDimension != nil && !validProjectionIdentifier(*dataset.DefaultTimeDimension) {
			return fmt.Errorf("decode SemanticModel publication: invalid dataset time dimension")
		}
		if err := validateIdentifierList(dataset.RequiredAccessGrants); err != nil {
			return fmt.Errorf("decode SemanticModel publication dataset %q: %w", name, err)
		}
		if dataset.AccessFilters != nil {
			for _, filter := range *dataset.AccessFilters {
				if !validProjectionIdentifier(filter.Field) || !validProjectionIdentifier(filter.UserAttribute) {
					return fmt.Errorf("decode SemanticModel publication dataset %q: invalid access filter", name)
				}
			}
		}
	}
	if value.Contract.AccessGrants != nil {
		for name, grant := range *value.Contract.AccessGrants {
			if !validProjectionIdentifier(name) || !validProjectionIdentifier(grant.UserAttribute) {
				return fmt.Errorf("decode SemanticModel publication: invalid access grant %q", name)
			}
			for _, item := range grant.AllowedValues {
				if err := validateCanonicalValue(item); err != nil {
					return fmt.Errorf("decode SemanticModel publication grant %q: %w", name, err)
				}
			}
		}
	}
	if value.Contract.Dimensions != nil {
		for name, dimension := range *value.Contract.Dimensions {
			if !validProjectionIdentifier(name) || !validProjectionDatatypeName(dimension.Datatype) {
				return fmt.Errorf("decode SemanticModel publication: invalid dimension %q", name)
			}
			if err := validateSemanticTime(dimension.Time); err != nil {
				return fmt.Errorf("decode SemanticModel publication dimension %q: %w", name, err)
			}
			if err := validateIdentifierList(dimension.RequiredAccessGrants); err != nil {
				return fmt.Errorf("decode SemanticModel publication dimension %q: %w", name, err)
			}
			for binding, value := range dimension.Bindings {
				if !validProjectionIdentifier(binding) || !validSemanticFieldReference(value.Field) {
					return fmt.Errorf("decode SemanticModel publication: invalid binding %q", binding)
				}
				if err := validateIdentifierList(value.Path); err != nil {
					return fmt.Errorf("decode SemanticModel publication binding %q: %w", binding, err)
				}
			}
		}
	}
	if value.Contract.Filters != nil {
		for name, filter := range *value.Contract.Filters {
			if err := validateSemanticFilter(filter); err != nil {
				return fmt.Errorf("decode SemanticModel publication filter %q: %w", name, err)
			}
		}
	}
	for name, metric := range value.Contract.Metrics {
		if !validProjectionIdentifier(name) || (metric.Type != "aggregate" && metric.Type != "derived" && metric.Type != "ratio") {
			return fmt.Errorf("decode SemanticModel publication: invalid metric %q", name)
		}
		if err := validateSemanticMetric(metric); err != nil {
			return fmt.Errorf("decode SemanticModel publication metric %q: %w", name, err)
		}
	}
	return nil
}

func validateProjectionField(value Field) error {
	if err := validateProjectionDatatype(value.Datatype); err != nil {
		return err
	}
	if err := validateProjectionIdentifierPointer(value.Classification); err != nil {
		return fmt.Errorf("invalid classification: %w", err)
	}
	return validateProjectionGovernance(value.AuthoritativeDefinitions, value.Deprecation)
}

func validateModelProjectionField(value ModelField) error {
	if err := validateProjectionDatatype(value.Datatype); err != nil {
		return err
	}
	if err := validateProjectionIdentifierPointer(value.Classification); err != nil {
		return fmt.Errorf("invalid classification: %w", err)
	}
	return validateProjectionGovernance(value.AuthoritativeDefinitions, value.Deprecation)
}

func validateProjectionGovernance(definitions *[]projectcontracts.ContractProjectionAuthoritativeDefinition, deprecation *projectcontracts.ContractProjectionFieldDeprecation) error {
	if definitions != nil {
		var previous string
		for index, definition := range *definitions {
			if definition.Type != "businessDefinition" && definition.Type != "transformationImplementation" {
				return fmt.Errorf("invalid authoritative definition type %q", definition.Type)
			}
			canonical, err := canonicalURL(definition.URL)
			if err != nil {
				return fmt.Errorf("invalid authoritative definition URL: %w", err)
			}
			if canonical != definition.URL {
				return fmt.Errorf("authoritative definition URL is not canonical")
			}
			key := definition.Type + "\x00" + definition.URL
			if index > 0 && key <= previous {
				return fmt.Errorf("authoritative definitions must be sorted and unique")
			}
			previous = key
		}
	}
	if deprecation == nil {
		return nil
	}
	if !validSemanticVersion(deprecation.Since) {
		return fmt.Errorf("invalid deprecation since version %q", deprecation.Since)
	}
	if canonical, err := canonicalText(deprecation.Reason); err != nil || canonical != deprecation.Reason {
		return fmt.Errorf("deprecation reason is not canonical")
	}
	if err := validateProjectionIdentifierPointer(deprecation.Replacement); err != nil {
		return fmt.Errorf("invalid deprecation replacement: %w", err)
	}
	return nil
}

func validateProjectionIdentifierPointer(value *string) error {
	if value != nil && !validProjectionIdentifier(*value) {
		return fmt.Errorf("invalid identifier %q", *value)
	}
	return nil
}

func validateProjectionDatatype(value *string) error {
	if value != nil {
		if !validProjectionDatatypeName(*value) {
			return fmt.Errorf("invalid datatype %q", *value)
		}
	}
	return nil
}

func validProjectionDatatypeName(value string) bool {
	switch value {
	case "String", "Integer", "Decimal", "Float", "Boolean", "Date", "Time", "DateTime", "DateTimeTz", "Opaque":
		return true
	default:
		return false
	}
}

func validProjectionIdentifier(value string) bool {
	if value == "" || !isASCIIIdentifierStart(value[0]) {
		return false
	}
	for _, character := range value[1:] {
		if !isASCIIIdentifierContinue(byte(character)) {
			return false
		}
	}
	return true
}

func validProjectionName(value string) bool {
	if value == "" || !isASCIIIdentifierStart(value[0]) {
		return false
	}
	for _, character := range value[1:] {
		if character != '_' && character != '-' && character != '.' && !isASCIIIdentifierContinue(byte(character)) {
			return false
		}
	}
	return true
}

func isASCIIIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isASCIIIdentifierContinue(value byte) bool {
	return isASCIIIdentifierStart(value) || value >= '0' && value <= '9'
}

func validateSQLAst(value string) error {
	var document struct {
		Statements json.RawMessage `json:"statements"`
	}
	if err := json.Unmarshal([]byte(value), &document); err != nil {
		return err
	}
	if len(document.Statements) == 0 || bytes.Equal(document.Statements, []byte("null")) {
		return errors.New("statements are required")
	}
	var statements []json.RawMessage
	if err := json.Unmarshal(document.Statements, &statements); err != nil {
		return errors.New("statements must be an array")
	}
	if len(statements) == 0 {
		return errors.New("statements must not be empty")
	}
	return nil
}

func validSemanticFieldReference(value string) bool {
	parts := strings.Split(value, ".")
	return len(parts) == 2 && validProjectionIdentifier(parts[0]) && validProjectionIdentifier(parts[1])
}

func validModelReference(value string) bool {
	separator := strings.LastIndexByte(value, '.')
	if separator <= 0 || separator == len(value)-1 {
		return false
	}
	if _, err := projectgraph.NewResourceID(value[:separator]); err != nil {
		return false
	}
	return validProjectionIdentifier(value[separator+1:])
}

func validateIdentifierList(values *[]string) error {
	if values == nil {
		return nil
	}
	for _, value := range *values {
		if !validProjectionIdentifier(value) {
			return fmt.Errorf("invalid identifier %q", value)
		}
	}
	return nil
}

func validateSemanticTime(value *SemanticTime) error {
	if value == nil {
		return nil
	}
	if !validSemanticGrain(value.NativeGrain) || len(value.Grains) == 0 {
		return errors.New("invalid time semantics grain")
	}
	for _, grain := range value.Grains {
		if !validSemanticGrain(grain) {
			return fmt.Errorf("invalid time semantics grain %q", grain)
		}
	}
	return nil
}

func validSemanticGrain(value string) bool {
	switch value {
	case "second", "minute", "hour", "day", "week", "month", "quarter", "year":
		return true
	default:
		return false
	}
}

func validateSemanticMetric(value SemanticMetric) error {
	if value.Dataset != nil && !validProjectionIdentifier(*value.Dataset) {
		return errors.New("invalid metric dataset")
	}
	if value.Aggregation != nil {
		switch *value.Aggregation {
		case "sum", "count", "count_distinct", "avg", "min", "max":
		default:
			return fmt.Errorf("invalid metric aggregation %q", *value.Aggregation)
		}
	}
	if value.Input != nil && !validSemanticFieldReference(value.Input.Field) {
		return errors.New("invalid metric input field")
	}
	if err := validateIdentifierList(value.Where); err != nil {
		return err
	}
	if value.Empty != nil && *value.Empty != "zero" && *value.Empty != "null" {
		return fmt.Errorf("invalid metric empty value %q", *value.Empty)
	}
	if value.TimeDimension != nil && !validProjectionIdentifier(*value.TimeDimension) {
		return errors.New("invalid metric time dimension")
	}
	if value.Numerator != nil && !validProjectionIdentifier(*value.Numerator) {
		return errors.New("invalid metric numerator")
	}
	if value.Denominator != nil && !validProjectionIdentifier(*value.Denominator) {
		return errors.New("invalid metric denominator")
	}
	if err := validateIdentifierList(value.RequiredAccessGrants); err != nil {
		return err
	}
	switch value.Type {
	case "aggregate":
		if value.Dataset == nil || value.Aggregation == nil || value.Input == nil {
			return errors.New("aggregate metric requires dataset, aggregation, and input")
		}
	case "derived":
		if value.Expression == nil {
			return errors.New("derived metric requires expression")
		}
	case "ratio":
		if value.Numerator == nil || value.Denominator == nil {
			return errors.New("ratio metric requires numerator and denominator")
		}
	}
	return nil
}

func validateProjectionDuration(value Duration) error {
	if value.Amount <= 0 || (value.Unit != "second" && value.Unit != "minute" && value.Unit != "hour" && value.Unit != "day") {
		return fmt.Errorf("invalid freshness duration")
	}
	return nil
}

func validateCanonicalValue(value CanonicalValue) error {
	switch variant := value.Value.(type) {
	case *projectcontracts.ContractProjectionCanonicalTextValue:
		return validateCanonicalTextValue(variant.Type, variant.Value)
	case *projectcontracts.ContractProjectionCanonicalBooleanValue:
		if variant.Type != "Boolean" {
			return fmt.Errorf("invalid boolean value type %q", variant.Type)
		}
		return nil
	default:
		return errors.New("canonical value variant is required")
	}
}

func validateCanonicalTextValue(typeName, value string) error {
	var typeValue semanticvalue.Type
	switch typeName {
	case "String":
		typeValue = semanticvalue.TypeString
	case "Integer":
		typeValue = semanticvalue.TypeInteger
	case "Decimal", "Number":
		typeValue = semanticvalue.TypeDecimal
	case "Date":
		typeValue = semanticvalue.TypeDate
	case "Timestamp":
		typeValue = semanticvalue.TypeTimestamp
	case "Time":
		if _, err := time.Parse("15:04:05.999999999", value); err != nil {
			return fmt.Errorf("invalid time literal")
		}
		return nil
	case "DateTime":
		if _, err := time.Parse("2006-01-02T15:04:05.999999999", value); err != nil {
			return fmt.Errorf("invalid datetime literal")
		}
		return nil
	default:
		return fmt.Errorf("invalid canonical text value type %q", typeName)
	}
	var input any = value
	if typeValue == semanticvalue.TypeInteger {
		// The wire value is text because RFC 8785 numbers cannot preserve
		// every int64. Its explicit Integer tag authorizes exact parsing;
		// this does not enable string-to-number coercion for authored inputs.
		input = json.Number(value)
	}
	canonical, err := semanticvalue.Canonicalize(typeValue, input)
	if err != nil || canonical.Canonical() != value {
		return fmt.Errorf("invalid %s literal", typeName)
	}
	return nil
}

func validateSemanticFilter(value SemanticFilter) error {
	if value.Operator != nil {
		switch *value.Operator {
		case "equals", "not_equals", "in", "not_in", "less_than", "less_than_or_equal", "greater_than", "greater_than_or_equal", "is_null", "is_not_null":
		default:
			return fmt.Errorf("invalid operator %q", *value.Operator)
		}
	}
	if value.Value != nil {
		if err := validateCanonicalValue(*value.Value); err != nil {
			return err
		}
	}
	if value.Values != nil {
		for _, item := range *value.Values {
			if err := validateCanonicalValue(item); err != nil {
				return err
			}
		}
	}
	if value.All != nil {
		for _, child := range *value.All {
			if err := validateSemanticFilter(child); err != nil {
				return err
			}
		}
	}
	if value.Any != nil {
		for _, child := range *value.Any {
			if err := validateSemanticFilter(child); err != nil {
				return err
			}
		}
	}
	if value.Not != nil {
		return validateSemanticFilter(*value.Not)
	}
	return nil
}

func canonicalPublicationBytes(data []byte, expectedKind string) ([]byte, error) {
	var envelope struct {
		Profile    string `json:"profile"`
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Contract struct {
				Version       string `json:"version"`
				Compatibility string `json:"compatibility"`
			} `json:"contract"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode %s publication envelope: %w", expectedKind, err)
	}
	if envelope.Profile != Profile || envelope.APIVersion != "leapview.dev/v1" || envelope.Kind != expectedKind {
		return nil, fmt.Errorf("decode %s publication: invalid envelope", expectedKind)
	}
	if envelope.Metadata.ID == "" || envelope.Metadata.Name == "" || envelope.Metadata.Contract.Version == "" || envelope.Metadata.Contract.Compatibility == "" {
		return nil, fmt.Errorf("decode %s publication: required envelope field is missing", expectedKind)
	}
	normalized, err := normalizeJSONStrings(data)
	if err != nil {
		return nil, fmt.Errorf("decode %s publication: normalize JSON: %w", expectedKind, err)
	}
	canonical, err := canonicalizeRFC8785(normalized)
	if err != nil {
		return nil, fmt.Errorf("decode %s publication: RFC 8785 canonicalize: %w", expectedKind, err)
	}
	return canonical, nil
}
