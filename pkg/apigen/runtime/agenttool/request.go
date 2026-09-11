package agenttool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// BuildRequest validates tool arguments and binds them to a relative HTTP request.
func BuildRequest(contract Contract, raw json.RawMessage, context Context) (*http.Request, error) {
	arguments, err := decodeArguments(raw)
	if err != nil {
		return nil, err
	}
	if err := validatePortableSchema(arguments, contract.InputSchema); err != nil {
		return nil, runtimeError(ErrorCodeInvalidArguments, "arguments do not match input schema: %v", err)
	}
	known := map[string]struct{}{}
	for _, binding := range contract.Bindings {
		if binding.Mode == "model" {
			known[binding.Argument] = struct{}{}
		}
	}
	for name := range arguments {
		if _, ok := known[name]; !ok {
			return nil, runtimeError(ErrorCodeInvalidArguments, "unknown argument %q", name)
		}
	}

	path := contract.Path
	query := url.Values{}
	headers := http.Header{}
	bodyFields := map[string]any{}
	var scalarBody any
	hasScalarBody := false
	for _, binding := range contract.Bindings {
		value, present, err := bindingValue(binding, arguments, context)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		if !matchesSchema(value, binding.Schema) {
			return nil, runtimeError(ErrorCodeInvalidArguments, "argument %q does not match its schema", displayBindingName(binding))
		}
		switch binding.Source {
		case "path":
			text, err := scalarString(value)
			if err != nil {
				return nil, runtimeError(ErrorCodeInvalidArguments, "path argument %q: %v", displayBindingName(binding), err)
			}
			placeholder := "{" + binding.WireName + "}"
			if !strings.Contains(path, placeholder) {
				return nil, runtimeError(ErrorCodeInvalidArguments, "path placeholder %q is missing", placeholder)
			}
			path = strings.ReplaceAll(path, placeholder, url.PathEscape(text))
		case "query":
			if err := addValues(query, binding.WireName, value, binding.Explode); err != nil {
				return nil, runtimeError(ErrorCodeInvalidArguments, "query argument %q: %v", displayBindingName(binding), err)
			}
		case "header":
			values, err := stringValues(value)
			if err != nil {
				return nil, runtimeError(ErrorCodeInvalidArguments, "header argument %q: %v", displayBindingName(binding), err)
			}
			for _, item := range values {
				headers.Add(binding.WireName, item)
			}
		case "body":
			if binding.WireName == "$" {
				scalarBody, hasScalarBody = value, true
			} else {
				bodyFields[binding.WireName] = value
			}
		default:
			return nil, runtimeError(ErrorCodeInvalidArguments, "unsupported binding source %q", binding.Source)
		}
	}
	if strings.Contains(path, "{") {
		return nil, runtimeError(ErrorCodeInvalidArguments, "not all path parameters were bound")
	}

	var body io.Reader
	if hasScalarBody || len(bodyFields) > 0 {
		payload := any(bodyFields)
		if hasScalarBody {
			payload = scalarBody
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, runtimeError(ErrorCodeInvalidArguments, "encode request body: %v", err)
		}
		body = bytes.NewReader(encoded)
		headers.Set("Content-Type", "application/json")
	}
	if contract.ResponseContentType != "" {
		headers.Set("Accept", contract.ResponseContentType)
	}
	request, err := http.NewRequest(contract.Method, path, body)
	if err != nil {
		return nil, runtimeError(ErrorCodeInvalidArguments, "build request: %v", err)
	}
	request.URL.RawQuery = query.Encode()
	request.Header = headers
	return request, nil
}

func decodeArguments(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var arguments map[string]any
	if err := decoder.Decode(&arguments); err != nil {
		return nil, runtimeError(ErrorCodeInvalidArguments, "decode arguments: %v", err)
	}
	if arguments == nil {
		return nil, runtimeError(ErrorCodeInvalidArguments, "arguments must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, runtimeError(ErrorCodeInvalidArguments, "arguments contain trailing JSON")
	}
	return arguments, nil
}

func bindingValue(binding Binding, arguments map[string]any, context Context) (any, bool, error) {
	switch binding.Mode {
	case "context":
		value, ok := context[binding.ContextKey]
		if !ok {
			return nil, false, runtimeError(ErrorCodeMissingContext, "missing context %q for %s", binding.ContextKey, binding.WireName)
		}
		return value, true, nil
	case "omit":
		if binding.Default != nil {
			return binding.Default, true, nil
		}
		return nil, false, nil
	case "model", "":
		value, ok := arguments[binding.Argument]
		if ok {
			return value, true, nil
		}
		if binding.Default != nil {
			return binding.Default, true, nil
		}
		if binding.Required {
			return nil, false, runtimeError(ErrorCodeInvalidArguments, "missing required argument %q", binding.Argument)
		}
		return nil, false, nil
	default:
		return nil, false, runtimeError(ErrorCodeInvalidArguments, "unsupported binding mode %q", binding.Mode)
	}
}

func matchesSchema(value any, schema ValueSchema) bool {
	switch schema.Type {
	case "", "object":
		_, ok := value.(map[string]any)
		return schema.Type == "" || ok
	case "string":
		text, ok := value.(string)
		if !ok {
			return false
		}
		length := len([]rune(text))
		if schema.MinLength != nil && length < *schema.MinLength || schema.MaxLength != nil && length > *schema.MaxLength {
			return false
		}
		if len(schema.Enum) == 0 {
			return true
		}
		for _, allowed := range schema.Enum {
			if text == allowed {
				return true
			}
		}
		return false
	case "integer":
		switch number := value.(type) {
		case json.Number:
			integer, err := number.Int64()
			if err != nil {
				return false
			}
			return matchesValueBounds(float64(integer), schema)
		case float64:
			return number == float64(int64(number)) && matchesValueBounds(number, schema)
		case int, int32, int64, uint, uint32, uint64:
			parsed, _, ok := schemaNumber(number)
			return ok && matchesValueBounds(parsed, schema)
		}
		return false
	case "number":
		number, _, ok := schemaNumber(value)
		return ok && matchesValueBounds(number, schema)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		items, ok := value.([]any)
		if !ok {
			return false
		}
		if schema.MinItems != nil && len(items) < *schema.MinItems || schema.MaxItems != nil && len(items) > *schema.MaxItems {
			return false
		}
		if schema.Items != nil {
			for _, item := range items {
				if !matchesSchema(item, *schema.Items) {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}

func matchesValueBounds(value float64, schema ValueSchema) bool {
	return (schema.Const == nil || value == *schema.Const) &&
		(schema.Minimum == nil || value >= *schema.Minimum) && (schema.Maximum == nil || value <= *schema.Maximum)
}

func addValues(values url.Values, name string, value any, explode bool) error {
	items, err := stringValues(value)
	if err != nil {
		return err
	}
	if len(items) > 1 && !explode {
		values.Set(name, strings.Join(items, ","))
		return nil
	}
	for _, item := range items {
		values.Add(name, item)
	}
	return nil
}

func stringValues(value any) ([]string, error) {
	if values, ok := value.([]any); ok {
		out := make([]string, 0, len(values))
		for _, item := range values {
			text, err := scalarString(item)
			if err != nil {
				return nil, err
			}
			out = append(out, text)
		}
		return out, nil
	}
	text, err := scalarString(value)
	if err != nil {
		return nil, err
	}
	return []string{text}, nil
}

func scalarString(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case json.Number:
		return value.String(), nil
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(value), nil
	case int:
		return strconv.Itoa(value), nil
	case int32:
		return strconv.FormatInt(int64(value), 10), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	default:
		return "", fmt.Errorf("must be scalar")
	}
}

func displayBindingName(binding Binding) string {
	if binding.Argument != "" {
		return binding.Argument
	}
	if binding.ContextKey != "" {
		return binding.ContextKey
	}
	return binding.WireName
}
