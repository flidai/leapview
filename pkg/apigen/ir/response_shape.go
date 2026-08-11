package ir

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResponseShapeMetadata extracts typed APIGen response-shape metadata.
func ResponseShapeMetadata(response Response) (ResponseShape, bool, error) {
	if len(response.Extensions) == 0 {
		return ResponseShape{}, false, nil
	}
	raw, ok := response.Extensions[ResponseShapeExtensionKey]
	if !ok || raw == nil {
		return ResponseShape{}, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ResponseShape{}, false, fmt.Errorf("marshal response shape metadata: %w", err)
	}
	var shape ResponseShape
	if err := json.Unmarshal(data, &shape); err != nil {
		return ResponseShape{}, false, fmt.Errorf("decode response shape metadata: %w", err)
	}
	shape.Kind = strings.TrimSpace(shape.Kind)
	shape.BodyType = strings.TrimSpace(shape.BodyType)
	return shape, true, nil
}
