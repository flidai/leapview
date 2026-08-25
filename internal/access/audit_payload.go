package access

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// RewriteGeneratedAuditEnvelopePayload rewrites fields in the payload of a
// generated audit envelope. Generated envelopes are intentionally strict:
// their version, retention, schema, and payload container are immutable, and
// callers cannot accidentally add fields beside payload. The returned value
// is canonicalized with the same metadata and security checks used by the
// durable audit-intent boundary.
func RewriteGeneratedAuditEnvelopePayload(raw string, fields map[string]any) (string, error) {
	canonical, err := canonicalAuditIntentMetadata(raw)
	if err != nil {
		return "", fmt.Errorf("audit envelope metadata: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(canonical))
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		// canonicalAuditIntentMetadata already decoded this value; retain the
		// decode error in the unlikely event the representation changes.
		return "", fmt.Errorf("decode audit envelope: %w", err)
	}
	if envelope == nil {
		return "", fmt.Errorf("audit envelope must be an object")
	}
	for key := range envelope {
		switch key {
		case "schemaVersion", "retention", "payloadSchema", "payload":
		default:
			return "", fmt.Errorf("audit envelope top-level field %q is not permitted", key)
		}
	}
	version, ok := envelope["schemaVersion"].(json.Number)
	if !ok {
		return "", fmt.Errorf("audit envelope schemaVersion must be a positive integer")
	}
	versionValue, err := strconv.ParseInt(version.String(), 10, 64)
	if err != nil || versionValue <= 0 {
		return "", fmt.Errorf("audit envelope schemaVersion must be a positive integer")
	}
	if retention, ok := envelope["retention"].(string); !ok || (retention != "short" && retention != "standard" && retention != "security") {
		return "", fmt.Errorf("audit envelope retention is unsupported")
	}
	if schema, ok := envelope["payloadSchema"].(string); !ok || strings.TrimSpace(schema) == "" {
		return "", fmt.Errorf("audit envelope payloadSchema is required")
	}
	payload, ok := envelope["payload"].(map[string]any)
	if !ok || payload == nil {
		return "", fmt.Errorf("audit envelope payload must be an object")
	}
	for key, value := range fields {
		key = strings.TrimSpace(key)
		if key == "" {
			return "", fmt.Errorf("audit envelope payload field name is required")
		}
		switch key {
		case "schemaVersion", "retention", "payloadSchema", "payload":
			return "", fmt.Errorf("audit envelope payload field %q cannot target the top level", key)
		}
		if _, exists := payload[key]; !exists {
			return "", fmt.Errorf("audit envelope payload field %q is not declared", key)
		}
		if value == nil {
			return "", fmt.Errorf("audit envelope payload field %q cannot be nil", key)
		}
		payload[key] = value
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode audit envelope: %w", err)
	}
	canonical, err = canonicalAuditIntentMetadata(string(encoded))
	if err != nil {
		return "", fmt.Errorf("audit envelope metadata after rewrite: %w", err)
	}
	return canonical, nil
}
