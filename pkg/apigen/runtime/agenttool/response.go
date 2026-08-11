package agenttool

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// ProjectResponse converts an HTTP response into an SDK-neutral tool result.
func ProjectResponse(contract Contract, response *http.Response) (Result, error) {
	if response == nil {
		return Result{}, runtimeError(ErrorCodeInvalidResponse, "response is nil")
	}
	if response.Body == nil {
		response.Body = http.NoBody
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return Result{}, runtimeError(ErrorCodeInvalidResponse, "read response: %v", err)
	}
	decoded, hasBody, err := decodeResponseBody(body)
	if err != nil {
		return Result{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		content := map[string]any{"status": response.StatusCode}
		if hasBody {
			content["body"] = decoded
		}
		return Result{StatusCode: response.StatusCode, Content: content, IsError: true}, nil
	}

	result := Result{StatusCode: response.StatusCode}
	switch contract.Output.Mode {
	case "empty":
		result.Content = map[string]any{"status": response.StatusCode}
	case "raw":
		if hasBody {
			result.Content = decoded
		} else {
			result.Content = map[string]any{"status": response.StatusCode}
		}
	case "project":
		if !hasBody {
			return Result{}, runtimeError(ErrorCodeInvalidResponse, "tool %q expected a JSON response body", contract.Name)
		}
		projected, err := projectScope(decoded, contract.Output.Select)
		if err != nil {
			return Result{}, err
		}
		if contract.Output.Cursor != nil {
			value, ok, err := valueAtPointer(decoded, contract.Output.Cursor.Source)
			if err != nil {
				return Result{}, runtimeError(ErrorCodeProjectionFailed, "cursor: %v", err)
			}
			if ok && value != nil && value != "" {
				projected[contract.Output.Cursor.Target] = value
				projected[contract.Output.Cursor.HasMoreTarget] = true
			} else {
				projected[contract.Output.Cursor.HasMoreTarget] = false
			}
		}
		result.Content = projected
	default:
		return Result{}, runtimeError(ErrorCodeProjectionFailed, "tool %q has unsupported output mode %q", contract.Name, contract.Output.Mode)
	}
	if err := validatePortableSchema(result.Content, contract.OutputSchema); err != nil {
		return Result{}, runtimeError(ErrorCodeInvalidResponse, "tool %q output does not match schema: %v", contract.Name, err)
	}
	return result, nil
}

func decodeResponseBody(body []byte) (any, bool, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false, runtimeError(ErrorCodeInvalidResponse, "decode JSON response: %v", err)
	}
	return decoded, true, nil
}

func projectScope(scope any, projections []Projection) (map[string]any, error) {
	out := map[string]any{}
	for _, projection := range projections {
		value, ok, err := valueAtPointer(scope, projection.Source)
		if err != nil {
			return nil, runtimeError(ErrorCodeProjectionFailed, "projection %q: %v", projection.Source, err)
		}
		if !ok {
			if projection.Optional {
				continue
			}
			return nil, runtimeError(ErrorCodeProjectionFailed, "projection %q is missing", projection.Source)
		}
		if value == nil && projection.Optional {
			continue
		}
		projected, err := projectValue(value, projection)
		if err != nil {
			return nil, err
		}
		out[projection.Target] = projected
		if projection.CountAs != "" {
			switch value := value.(type) {
			case []any:
				out[projection.CountAs] = len(value)
			case map[string]any:
				out[projection.CountAs] = len(value)
			default:
				return nil, runtimeError(ErrorCodeProjectionFailed, "projection %q cannot be counted", projection.Source)
			}
		}
	}
	return out, nil
}

func projectValue(value any, projection Projection) (any, error) {
	if len(projection.Select) == 0 {
		return value, nil
	}
	switch projection.Kind {
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return nil, runtimeError(ErrorCodeProjectionFailed, "projection %q expected object", projection.Source)
		}
		return projectScope(value, projection.Select)
	case "array":
		items, ok := value.([]any)
		if !ok {
			return nil, runtimeError(ErrorCodeProjectionFailed, "projection %q expected array", projection.Source)
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			projected, err := projectScope(item, projection.Select)
			if err != nil {
				return nil, err
			}
			out = append(out, projected)
		}
		return out, nil
	case "map":
		values, ok := value.(map[string]any)
		if !ok {
			return nil, runtimeError(ErrorCodeProjectionFailed, "projection %q expected map", projection.Source)
		}
		out := make(map[string]any, len(values))
		for key, item := range values {
			projected, err := projectScope(item, projection.Select)
			if err != nil {
				return nil, err
			}
			out[key] = projected
		}
		return out, nil
	default:
		return nil, runtimeError(ErrorCodeProjectionFailed, "projection %q cannot select children from kind %q", projection.Source, projection.Kind)
	}
}

func valueAtPointer(value any, pointer string) (any, bool, error) {
	if pointer == "" {
		return value, true, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false, runtimeError(ErrorCodeProjectionFailed, "pointer %q must start with /", pointer)
	}
	current := value
	for _, raw := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		current, ok = object[segment]
		if !ok {
			return nil, false, nil
		}
	}
	return current, true, nil
}
