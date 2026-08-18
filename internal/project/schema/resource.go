package configschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// NormalizeResource parses, checks, validates, and deterministically encodes a
// single authoring resource. YAML and JSON are both accepted. The returned
// bytes are a JSON representation suitable for decoding into generated wire
// models.
//
// YAML is intentionally accepted only when its values have an unambiguous JSON
// representation. In particular, aliases, anchors, explicit tags, duplicate
// keys, non-string object keys, and non-finite numbers are not part of the
// authoring contract.
func NormalizeResource(kind Kind, filename string, content []byte) ([]byte, error) {
	root, err := parseResourceDocument(filename, content)
	if err != nil {
		return nil, err
	}
	if err := checkResourceNode(filename, root); err != nil {
		return nil, err
	}
	if err := checkJSONNumbers(filename, content, root); err != nil {
		return nil, err
	}

	// Validate through the existing CUE structural/contextual facade. Keeping
	// this call here (rather than introducing a second structural contract) is
	// important: contextual checks and generated schema diagnostics remain
	// authoritative in one place.
	if err := ValidateBytes(kind, filename, content); err != nil {
		return nil, err
	}

	value, err := normalizeResourceNode(filename, root)
	if err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, resourceDiagnostic(filename, root, "schema.normalize", "resource cannot be represented as deterministic JSON: "+err.Error())
	}
	return normalized, nil
}

// DecodeResource normalizes and validates one resource, then decodes the
// canonical JSON into destination. Decoding deliberately goes through
// encoding/json so generated UnmarshalJSON union implementations remain the
// sole dispatch authority.
func DecodeResource(kind Kind, filename string, content []byte, destination any) error {
	if destination == nil {
		return resourceDiagnostic(filename, nil, "schema.decode", "destination must be a non-nil pointer")
	}
	value := reflect.ValueOf(destination)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return resourceDiagnostic(filename, nil, "schema.decode", "destination must be a non-nil pointer")
	}
	// Generated resource roots are selected by a compile-time type switch. This
	// keeps the generated DTOs as the only public contract and prevents a
	// caller from decoding one resource kind into another kind's envelope.
	switch destination := destination.(type) {
	case *projectcontracts.Connection:
		if kind != KindConnection {
			return resourceDiagnostic(filename, nil, "schema.decode", "Connection destination requires kind connection")
		}
		return decodeGeneratedResource(kind, filename, content, destination)
	case *projectcontracts.Source:
		if kind != KindSource {
			return resourceDiagnostic(filename, nil, "schema.decode", "Source destination requires kind source")
		}
		return decodeGeneratedResource(kind, filename, content, destination)
	case *projectcontracts.Model:
		if kind != KindModel {
			return resourceDiagnostic(filename, nil, "schema.decode", "Model destination requires kind model")
		}
		return decodeGeneratedResource(kind, filename, content, destination)
	}
	normalized, err := NormalizeResource(kind, filename, content)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(normalized, destination); err != nil {
		return resourceDiagnostic(filename, nil, "schema.decode", err.Error())
	}
	return nil
}

func decodeGeneratedResource(kind Kind, filename string, content []byte, destination any) error {
	if kind != KindConnection && kind != KindSource && kind != KindModel {
		return resourceDiagnostic(filename, nil, "schema.decode", "generated decoding is only available for Connection, Source, and Model")
	}
	root, err := parseResourceDocument(filename, content)
	if err != nil {
		return err
	}
	if err := checkResourceNode(filename, root); err != nil {
		return err
	}
	if err := checkJSONNumbers(filename, content, root); err != nil {
		return err
	}
	normalizedValue, err := normalizeResourceNode(filename, root)
	if err != nil {
		return err
	}
	normalized, err := json.Marshal(normalizedValue)
	if err != nil {
		return resourceDiagnostic(filename, root, "schema.normalize", err.Error())
	}
	if err := validateGeneratedJSON(filename, root, normalized); err != nil {
		return err
	}
	if err := json.Unmarshal(normalized, destination); err != nil {
		return resourceDiagnostic(filename, root, "schema.decode", err.Error())
	}
	return nil
}

var (
	generatedSchemaOnce sync.Once
	generatedSchema     *jsonschema.Schema
	generatedSchemaErr  error
)

func compiledGeneratedSchema() (*jsonschema.Schema, error) {
	generatedSchemaOnce.Do(func() {
		var schemaDocument any
		if err := json.Unmarshal(projectcontracts.DataResourcesSchema, &schemaDocument); err != nil {
			generatedSchemaErr = fmt.Errorf("decode generated data-resource schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		const schemaURL = "https://leapview.dev/schemas/data-resources.schema.json"
		if err := compiler.AddResource(schemaURL, schemaDocument); err != nil {
			generatedSchemaErr = fmt.Errorf("register generated data-resource schema: %w", err)
			return
		}
		generatedSchema, generatedSchemaErr = compiler.Compile(schemaURL)
	})
	return generatedSchema, generatedSchemaErr
}

func validateGeneratedJSON(filename string, root *yaml.Node, content []byte) error {
	schema, err := compiledGeneratedSchema()
	if err != nil {
		return resourceDiagnostic(filename, root, "schema.generated", err.Error())
	}
	var value any
	if err := json.Unmarshal(content, &value); err != nil {
		return resourceDiagnostic(filename, root, "schema.generated", "decode normalized generated resource: "+err.Error())
	}
	if err := schema.Validate(value); err != nil {
		return resourceDiagnostic(filename, root, "schema.generated", err.Error())
	}
	return nil
}

func parseResourceDocument(filename string, content []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, yamlDiagnostic(filename, err, "unable to parse resource YAML: "+err.Error())
	}
	if len(document.Content) == 0 {
		return nil, resourceDiagnostic(filename, &document, "schema.yaml", "resource document is empty")
	}

	var extra yaml.Node
	err := decoder.Decode(&extra)
	switch {
	case errors.Is(err, io.EOF):
		return document.Content[0], nil
	case err != nil:
		return nil, yamlDiagnostic(filename, err, "unable to parse resource YAML: "+err.Error())
	default:
		line, column := extra.Line, extra.Column
		if len(extra.Content) > 0 && extra.Content[0].Line > 0 {
			line, column = extra.Content[0].Line, extra.Content[0].Column
		}
		return nil, &Error{Diagnostics: []Diagnostic{{
			File:     filename,
			Line:     line,
			Column:   column,
			Severity: SeverityError,
			Code:     "schema.document",
			Message:  "multiple YAML documents are not supported for a single resource",
		}}}
	}
}

func checkResourceNode(filename string, root *yaml.Node) error {
	return walkResourceNode(filename, root, func(node *yaml.Node) error {
		if node.Anchor != "" {
			return resourceDiagnostic(filename, node, "schema.alias", "anchors are not supported in JSON-compatible resources")
		}
		if node.Alias != nil {
			return resourceDiagnostic(filename, node, "schema.alias", "aliases are not supported in JSON-compatible resources")
		}
		if node.Style&yaml.TaggedStyle != 0 {
			return resourceDiagnostic(filename, node, "schema.tag", fmt.Sprintf("explicit YAML tag %q is not supported", node.Tag))
		}
		switch node.Kind {
		case yaml.MappingNode:
			if node.Tag != "!!map" {
				return resourceDiagnostic(filename, node, "schema.tag", fmt.Sprintf("YAML tag %q is not supported for JSON objects", node.Tag))
			}
		case yaml.SequenceNode:
			if node.Tag != "!!seq" {
				return resourceDiagnostic(filename, node, "schema.tag", fmt.Sprintf("YAML tag %q is not supported for JSON arrays", node.Tag))
			}
		}
		if node.Kind == yaml.ScalarNode && !utf8.ValidString(node.Value) {
			return resourceDiagnostic(filename, node, "schema.value", "scalar contains invalid UTF-8 and cannot be represented in JSON")
		}
		if node.Kind == yaml.ScalarNode && node.Tag == "!!float" {
			if _, err := yamlFloat(node.Value); err != nil {
				return resourceDiagnostic(filename, node, "schema.number", err.Error())
			}
		}
		// yaml.v3 resolves out-of-range plain floating-point literals such as
		// 1e400 to !!str instead of !!float. Treat those literals as numbers
		// here so they cannot silently change type during normalization. Quoted
		// values intentionally remain strings, even when they look numeric.
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && node.Style == 0 && yamlFloatOverflow(node.Value) {
			return resourceDiagnostic(filename, node, "schema.number", fmt.Sprintf("float value %q cannot be represented as a finite number", node.Value))
		}
		return nil
	})
}

func yamlFloatOverflow(value string) bool {
	canonical := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	if canonical == "" {
		return false
	}
	parsed, err := strconv.ParseFloat(canonical, 64)
	return errors.Is(err, strconv.ErrRange) && math.IsInf(parsed, 0)
}

// YAML's resolver intentionally treats a few JSON spellings (for example
// 1e400) as strings. When the input is valid JSON, inspect its number tokens as
// well so an out-of-range JSON number cannot silently change type during YAML
// parsing and normalization.
func checkJSONNumbers(filename string, content []byte, root *yaml.Node) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil // The document is YAML (or will be rejected by CUE below).
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil // Not one complete JSON value; YAML parsing remains authoritative.
	}
	return checkJSONNumbersAt(filename, value, root)
}

func checkJSONNumbersAt(filename string, value any, node *yaml.Node) error {
	switch typed := value.(type) {
	case json.Number:
		if err := validateJSONNumber(typed); err != nil {
			return resourceDiagnostic(filename, node, "schema.number", err.Error())
		}
	case map[string]any:
		if node == nil || node.Kind != yaml.MappingNode {
			return resourceDiagnostic(filename, node, "schema.normalize", "JSON object shape is ambiguous during YAML normalization")
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := typed[key]
			if err := checkJSONNumbersAt(filename, child, yamlMappingValue(node, key)); err != nil {
				return err
			}
		}
	case []any:
		if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) != len(typed) {
			return resourceDiagnostic(filename, node, "schema.normalize", "JSON array shape is ambiguous during YAML normalization")
		}
		for index, child := range typed {
			if err := checkJSONNumbersAt(filename, child, node.Content[index]); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateJSONNumber(number json.Number) error {
	text := number.String()
	if strings.ContainsAny(text, ".eE") {
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return fmt.Errorf("JSON number %q cannot be represented as a finite number", text)
		}
		if parsed == 0 && numericMantissaIsNonZero(text) {
			return fmt.Errorf("JSON number %q underflows the finite normalization range", text)
		}
		return nil
	}
	if strings.HasPrefix(text, "-") {
		if _, err := strconv.ParseInt(text, 10, 64); err == nil {
			return nil
		}
	} else if _, err := strconv.ParseUint(text, 10, 64); err == nil {
		return nil
	}
	return fmt.Errorf("JSON integer %q is outside the exact normalization range", text)
}

func walkResourceNode(filename string, node *yaml.Node, visit func(*yaml.Node) error) error {
	if node == nil {
		return resourceDiagnostic(filename, nil, "schema.value", "resource contains a missing YAML value")
	}
	if err := visit(node); err != nil {
		return err
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return resourceDiagnostic(filename, node, "schema.document", "resource document must contain exactly one value")
		}
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return resourceDiagnostic(filename, node, "schema.value", "mapping contains an incomplete key/value pair")
		}
		seen := make(map[string]*yaml.Node, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return resourceDiagnostic(filename, key, "schema.key", "object keys must be strings")
			}
			if previous, ok := seen[key.Value]; ok {
				message := fmt.Sprintf("duplicate mapping key %q (first declared at line %d, column %d)", key.Value, previous.Line, previous.Column)
				return resourceDiagnostic(filename, key, "schema.duplicate_key", message)
			}
			seen[key.Value] = key
			if err := walkResourceNode(filename, key, visit); err != nil {
				return err
			}
			if err := walkResourceNode(filename, value, visit); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := walkResourceNode(filename, child, visit); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		// Scalar tags are checked while normalizing so diagnostics can mention
		// the exact unsupported value. No child nodes exist.
	case yaml.AliasNode:
		return resourceDiagnostic(filename, node, "schema.alias", "aliases are not supported in JSON-compatible resources")
	default:
		return resourceDiagnostic(filename, node, "schema.value", fmt.Sprintf("YAML node kind %d cannot be represented in JSON", node.Kind))
	}
	return nil
}

func normalizeResourceNode(filename string, node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return nil, resourceDiagnostic(filename, node, "schema.document", "resource document must contain exactly one value")
		}
		return normalizeResourceNode(filename, node.Content[0])
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return nil, resourceDiagnostic(filename, node, "schema.tag", fmt.Sprintf("YAML tag %q is not supported for JSON objects", node.Tag))
		}
		result := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, resourceDiagnostic(filename, key, "schema.key", "object keys must be strings")
			}
			normalized, err := normalizeResourceNode(filename, value)
			if err != nil {
				return nil, err
			}
			result[key.Value] = normalized
		}
		return result, nil
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return nil, resourceDiagnostic(filename, node, "schema.tag", fmt.Sprintf("YAML tag %q is not supported for JSON arrays", node.Tag))
		}
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			normalized, err := normalizeResourceNode(filename, child)
			if err != nil {
				return nil, err
			}
			result = append(result, normalized)
		}
		return result, nil
	case yaml.ScalarNode:
		if !utf8.ValidString(node.Value) {
			return nil, resourceDiagnostic(filename, node, "schema.value", "scalar contains invalid UTF-8 and cannot be represented in JSON")
		}
		switch node.Tag {
		case "!!null":
			return nil, nil
		case "!!str":
			return node.Value, nil
		case "!!bool":
			value, ok := yamlBool(node.Value)
			if !ok {
				return nil, resourceDiagnostic(filename, node, "schema.value", fmt.Sprintf("boolean value %q is not representable in JSON", node.Value))
			}
			return value, nil
		case "!!int":
			var value any
			if err := node.Decode(&value); err != nil {
				return nil, resourceDiagnostic(filename, node, "schema.value", "integer cannot be represented in JSON: "+err.Error())
			}
			switch typed := value.(type) {
			case int, int64, uint, uint64:
				return typed, nil
			case float64:
				return nil, resourceDiagnostic(filename, node, "schema.normalize", "integer is outside the exact JSON normalization range")
			default:
				return nil, resourceDiagnostic(filename, node, "schema.value", fmt.Sprintf("integer decoded to unsupported value %T", value))
			}
		case "!!float":
			value, err := yamlFloat(node.Value)
			if err != nil {
				return nil, resourceDiagnostic(filename, node, "schema.number", err.Error())
			}
			return value, nil
		default:
			return nil, resourceDiagnostic(filename, node, "schema.tag", fmt.Sprintf("YAML tag %q is not supported for JSON normalization", node.Tag))
		}
	default:
		return nil, resourceDiagnostic(filename, node, "schema.value", fmt.Sprintf("YAML node kind %d cannot be represented in JSON", node.Kind))
	}
}

func yamlBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func yamlFloat(value string) (float64, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case ".nan", "+.nan", "-.nan", ".inf", "+.inf", "-.inf":
		return 0, fmt.Errorf("non-finite float %q cannot be represented in JSON", value)
	}
	canonical := strings.ReplaceAll(value, "_", "")
	parsed, err := strconv.ParseFloat(canonical, 64)
	if err != nil {
		return 0, fmt.Errorf("float value %q cannot be represented in JSON: %v", value, err)
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("non-finite float %q cannot be represented in JSON", value)
	}
	if parsed == 0 && numericMantissaIsNonZero(canonical) {
		return 0, fmt.Errorf("float value %q underflows the finite JSON normalization range", value)
	}
	return parsed, nil
}

func numericMantissaIsNonZero(value string) bool {
	value = strings.TrimLeft(strings.TrimSpace(value), "+-")
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		value = value[:index]
	}
	for _, char := range value {
		if char >= '1' && char <= '9' {
			return true
		}
	}
	return false
}

var yamlErrorPosition = regexp.MustCompile(`(?i)line ([0-9]+)(?::([0-9]+))?`)

func yamlDiagnostic(filename string, err error, message string) error {
	line, column := 0, 0
	if match := yamlErrorPosition.FindStringSubmatch(err.Error()); len(match) > 1 {
		line, _ = strconv.Atoi(match[1])
		if len(match) > 2 && match[2] != "" {
			column, _ = strconv.Atoi(match[2])
		}
	}
	return &Error{Diagnostics: []Diagnostic{{
		File:     filename,
		Line:     line,
		Column:   column,
		Severity: SeverityError,
		Code:     "schema.yaml",
		Message:  message,
	}}}
}

func resourceDiagnostic(filename string, node *yaml.Node, code, message string) error {
	diagnostic := Diagnostic{File: filename, Severity: SeverityError, Code: code, Message: message}
	if node != nil {
		diagnostic.Line = node.Line
		diagnostic.Column = node.Column
	}
	return &Error{Diagnostics: []Diagnostic{diagnostic}}
}
