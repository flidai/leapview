// Package audit validates and serializes generated command audit payloads.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type Sensitivity string

const (
	SensitivityPublic   Sensitivity = "public"
	SensitivityInternal Sensitivity = "internal"
	SensitivityPII      Sensitivity = "pii"
	SensitivitySecret   Sensitivity = "secret"
)

type Retention string

const (
	RetentionShort    Retention = "short"
	RetentionStandard Retention = "standard"
	RetentionSecurity Retention = "security"
)

type FieldContract struct {
	Name        string
	Sensitivity Sensitivity
}

type Contract struct {
	Schema        string
	SchemaVersion int
	Retention     Retention
	Fields        []FieldContract
}

var ErrInvalidContract = errors.New("invalid APIGen audit payload contract")

func (contract Contract) Validate() error {
	if strings.TrimSpace(contract.Schema) == "" || contract.SchemaVersion < 1 {
		return fmt.Errorf("%w: schema and positive schema version are required", ErrInvalidContract)
	}
	switch contract.Retention {
	case RetentionShort, RetentionStandard, RetentionSecurity:
	default:
		return fmt.Errorf("%w: unsupported retention %q", ErrInvalidContract, contract.Retention)
	}
	if len(contract.Fields) == 0 {
		return fmt.Errorf("%w: at least one field is required", ErrInvalidContract)
	}
	seen := make(map[string]struct{}, len(contract.Fields))
	for _, field := range contract.Fields {
		if strings.TrimSpace(field.Name) == "" {
			return fmt.Errorf("%w: field name is required", ErrInvalidContract)
		}
		if field.Name == "schemaVersion" || field.Name == "retention" || field.Name == "payloadSchema" {
			return fmt.Errorf("%w: field %q is reserved", ErrInvalidContract, field.Name)
		}
		switch field.Sensitivity {
		case SensitivityPublic, SensitivityInternal, SensitivityPII, SensitivitySecret:
		default:
			return fmt.Errorf("%w: field %q has unsupported sensitivity %q", ErrInvalidContract, field.Name, field.Sensitivity)
		}
		if _, exists := seen[field.Name]; exists {
			return fmt.Errorf("%w: duplicate field %q", ErrInvalidContract, field.Name)
		}
		seen[field.Name] = struct{}{}
	}
	return nil
}

// EncodeForAudit produces the durable audit envelope. Secret values are never
// persisted; other classified fields remain available to authorized audit
// readers according to the contract retention policy.
func EncodeForAudit(contract Contract, payload any) (string, error) {
	return encode(contract, payload, false)
}

// EncodeForLog produces a low-risk representation for logs. Only public
// fields retain their value; internal, PII, and secret fields are redacted.
func EncodeForLog(contract Contract, payload any) (string, error) {
	return encode(contract, payload, true)
}

func encode(contract Contract, payload any, safeLog bool) (string, error) {
	if err := contract.Validate(); err != nil {
		return "", err
	}
	if payload == nil || (reflect.ValueOf(payload).Kind() == reflect.Pointer && reflect.ValueOf(payload).IsNil()) {
		return "", fmt.Errorf("%w: payload is required", ErrInvalidContract)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode audit payload %s: %w", contract.Schema, err)
	}
	var values map[string]any
	if err := json.Unmarshal(encoded, &values); err != nil || values == nil {
		return "", fmt.Errorf("encode audit payload %s: payload must be a JSON object", contract.Schema)
	}
	fields := make(map[string]Sensitivity, len(contract.Fields))
	for _, field := range contract.Fields {
		fields[field.Name] = field.Sensitivity
		if _, ok := values[field.Name]; !ok {
			return "", fmt.Errorf("encode audit payload %s: required field %q is missing", contract.Schema, field.Name)
		}
	}
	for name := range values {
		if _, ok := fields[name]; !ok {
			return "", fmt.Errorf("encode audit payload %s: field %q is not declared", contract.Schema, name)
		}
	}
	for name, sensitivity := range fields {
		if sensitivity == SensitivitySecret || (safeLog && sensitivity != SensitivityPublic) {
			values[name] = "[REDACTED]"
		}
	}

	// Marshal through a deterministic map projection so persisted records and
	// tests remain stable independently of Go map iteration order.
	envelope := orderedEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Retention:     string(contract.Retention),
		PayloadSchema: contract.Schema,
		Payload:       values,
		fieldOrder:    sortedFieldNames(contract.Fields),
	}
	result, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode audit payload %s envelope: %w", contract.Schema, err)
	}
	return string(result), nil
}

type orderedEnvelope struct {
	SchemaVersion int
	Retention     string
	PayloadSchema string
	Payload       map[string]any
	fieldOrder    []string
}

func (envelope orderedEnvelope) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteString(`{"schemaVersion":`)
	version, _ := json.Marshal(envelope.SchemaVersion)
	b.Write(version)
	b.WriteString(`,"retention":`)
	retention, _ := json.Marshal(envelope.Retention)
	b.Write(retention)
	b.WriteString(`,"payloadSchema":`)
	schema, _ := json.Marshal(envelope.PayloadSchema)
	b.Write(schema)
	b.WriteString(`,"payload":{`)
	for index, name := range envelope.fieldOrder {
		if index > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(name)
		value, err := json.Marshal(envelope.Payload[name])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteString("}}")
	return []byte(b.String()), nil
}

func sortedFieldNames(fields []FieldContract) []string {
	names := make([]string, len(fields))
	for index, field := range fields {
		names[index] = field.Name
	}
	sort.Strings(names)
	return names
}
