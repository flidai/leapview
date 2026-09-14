package contractprojection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

// The pre-ADR-0023 v1 projection is still accepted for exact historical
// digest verification. These private DTOs freeze its old wire shape; new
// publications are always created through the generated shared contract.
type legacyField struct {
	ModelField
	Nullable *bool `json:"nullable,omitempty"`
}

type legacySourceSchema struct {
	Mode   string                  `json:"mode"`
	Fields *map[string]legacyField `json:"fields,omitempty"`
}

type legacyFreshness struct {
	Basis        string    `json:"basis"`
	Field        *string   `json:"field,omitempty"`
	Revision     *string   `json:"revision,omitempty"`
	WarningAfter *Duration `json:"warningAfter,omitempty"`
	ErrorAfter   *Duration `json:"errorAfter,omitempty"`
}

type legacySourceBody struct {
	Schema    legacySourceSchema `json:"schema"`
	Freshness *legacyFreshness   `json:"freshness,omitempty"`
}

type legacySourceView struct {
	Profile    string           `json:"profile"`
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Metadata   Metadata         `json:"metadata"`
	Contract   legacySourceBody `json:"contract"`
}

type legacyCheck struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Field    *string   `json:"field,omitempty"`
	Fields   *[]string `json:"fields,omitempty"`
	Values   *[]string `json:"values,omitempty"`
	To       *string   `json:"to,omitempty"`
	Minimum  *int64    `json:"minimum,omitempty"`
	Maximum  *int64    `json:"maximum,omitempty"`
	Severity *string   `json:"severity,omitempty"`
}

type legacyModelBody struct {
	Definition ModelDefinition        `json:"definition"`
	Entities   map[string]ModelEntity `json:"entities"`
	Grain      ModelGrain             `json:"grain"`
	Fields     map[string]legacyField `json:"fields"`
	Checks     *[]legacyCheck         `json:"checks,omitempty"`
}

type legacyModelView struct {
	Profile    string          `json:"profile"`
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   Metadata        `json:"metadata"`
	Contract   legacyModelBody `json:"contract"`
}

func legacyPublicationShape(data []byte, kind string) bool {
	var envelope struct {
		Contract map[string]json.RawMessage `json:"contract"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return false
	}
	if kind == "Source" {
		_, hasFields := envelope.Contract["fields"]
		return !hasFields
	}
	_, hasSchema := envelope.Contract["schema"]
	return !hasSchema
}

// IsHistoricalV1Publication identifies the pre-ADR-0023 Source and Model
// layout. Adapters must not silently treat its nullable declarations as new
// row checks when exporting the historical bytes.
func IsHistoricalV1Publication(data []byte, kind string) bool {
	if kind != "Source" && kind != "Model" {
		return false
	}
	return legacyPublicationShape(data, kind)
}

func decodeLegacyCanonical(data []byte, kind string, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode historical %s publication: %w", kind, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("decode historical %s publication: trailing JSON", kind)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return err
	}
	canonical, err := canonicalPublicationBytes(encoded, kind)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, data) {
		return fmt.Errorf("decode historical %s publication: decoded view differs from canonical bytes", kind)
	}
	return nil
}

func decodeLegacySource(data []byte) (SourceView, error) {
	var old legacySourceView
	if err := decodeLegacyCanonical(data, "Source", &old); err != nil {
		return SourceView{}, err
	}
	if old.Profile != Profile || old.APIVersion != "leapview.dev/v1" || old.Kind != "Source" {
		return SourceView{}, fmt.Errorf("decode historical Source publication: invalid envelope")
	}
	mode := old.Contract.Schema.Mode
	if mode != "inferred" && mode != "compatible" && mode != "strict" {
		return SourceView{}, fmt.Errorf("decode historical Source publication: invalid schema mode")
	}
	if mode == "inferred" && old.Contract.Schema.Fields != nil && len(*old.Contract.Schema.Fields) > 0 || mode == "strict" && (old.Contract.Schema.Fields == nil || len(*old.Contract.Schema.Fields) == 0) {
		return SourceView{}, fmt.Errorf("decode historical Source publication: invalid schema fields for mode")
	}
	if mode == "inferred" {
		mode = "compatible"
	}
	if freshness := old.Contract.Freshness; freshness != nil {
		if freshness.WarningAfter == nil && freshness.ErrorAfter == nil {
			return SourceView{}, fmt.Errorf("decode historical Source publication: freshness threshold is required")
		}
		if freshness.WarningAfter != nil {
			if err := validateProjectionDuration(*freshness.WarningAfter); err != nil {
				return SourceView{}, err
			}
		}
		if freshness.ErrorAfter != nil {
			if err := validateProjectionDuration(*freshness.ErrorAfter); err != nil {
				return SourceView{}, err
			}
		}
		switch freshness.Basis {
		case "field":
			if freshness.Field == nil || !validProjectionIdentifier(*freshness.Field) || freshness.Revision != nil {
				return SourceView{}, fmt.Errorf("decode historical Source publication: invalid freshness field")
			}
		case "revision":
			if freshness.Revision == nil || freshness.Field != nil {
				return SourceView{}, fmt.Errorf("decode historical Source publication: invalid freshness revision")
			}
			parsed, err := time.Parse(time.RFC3339Nano, *freshness.Revision)
			if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != *freshness.Revision {
				return SourceView{}, fmt.Errorf("decode historical Source publication: invalid freshness revision")
			}
		default:
			return SourceView{}, fmt.Errorf("decode historical Source publication: invalid freshness basis")
		}
	}
	fields := map[string]ModelField{}
	if old.Contract.Schema.Fields != nil {
		for name, field := range *old.Contract.Schema.Fields {
			fields[name] = field.ModelField
		}
	}
	view := SourceView{Profile: old.Profile, APIVersion: old.APIVersion, Kind: old.Kind, Metadata: old.Metadata, Contract: projectcontracts.ContractProjectionSourceBody{Schema: SourceSchema{Mode: mode}, Fields: fields}}
	if err := validatePublicationView("Source", &view); err != nil {
		return SourceView{}, err
	}
	return view, nil
}

func decodeLegacyModel(data []byte) (ModelView, error) {
	var old legacyModelView
	if err := decodeLegacyCanonical(data, "Model", &old); err != nil {
		return ModelView{}, err
	}
	if old.Profile != Profile || old.APIVersion != "leapview.dev/v1" || old.Kind != "Model" {
		return ModelView{}, fmt.Errorf("decode historical Model publication: invalid envelope")
	}
	fields := make(map[string]ModelField, len(old.Contract.Fields))
	for name, field := range old.Contract.Fields {
		fields[name] = field.ModelField
	}
	var checks *[]ModelCheck
	if old.Contract.Checks != nil {
		converted := make([]ModelCheck, len(*old.Contract.Checks))
		for index, check := range *old.Contract.Checks {
			converted[index] = ModelCheck{ID: check.ID, Type: check.Type, Field: check.Field, Fields: check.Fields, Values: check.Values, To: check.To, Minimum: check.Minimum, Maximum: check.Maximum, Severity: check.Severity}
		}
		checks = &converted
	}
	view := ModelView{Profile: old.Profile, APIVersion: old.APIVersion, Kind: old.Kind, Metadata: old.Metadata, Contract: projectcontracts.ContractProjectionModelBody{Definition: old.Contract.Definition, Entities: old.Contract.Entities, Grain: old.Contract.Grain, Schema: SourceSchema{Mode: "compatible"}, Fields: fields, Checks: checks}}
	if err := validatePublicationView("Model", &view); err != nil {
		return ModelView{}, err
	}
	return view, nil
}
