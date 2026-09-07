package contractversion

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// SemanticRegistryTypes is an allowlisted comparison input, not a registry.
// Publication adapters must derive it from the Access-owned registry under the
// publication transaction. Definitions are the union referenced by the two
// contracts; assignments, claims, owners, and other mutable metadata are absent.
type SemanticRegistryTypes struct {
	InstanceID      string                   `json:"instanceId"`
	ProjectID       string                   `json:"projectId"`
	ControlRevision int64                    `json:"controlRevision"`
	Profile         string                   `json:"profile"`
	Revision        int64                    `json:"revision"`
	Digest          string                   `json:"digest"`
	Definitions     []RegisteredSemanticType `json:"definitions"`
}

type RegisteredSemanticType struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Type    semanticvalue.Type `json:"type"`
	Shape   string             `json:"shape"`
	Version int64              `json:"version"`
	Enabled bool               `json:"enabled"`
}

func (r SemanticRegistryTypes) Validate() error {
	if r.InstanceID == "" || len(r.InstanceID) > 255 || strings.TrimSpace(r.InstanceID) != r.InstanceID || strings.IndexFunc(r.InstanceID, unicode.IsControl) >= 0 || projectgraph.ResourceID(r.ProjectID).Validate() != nil || r.ControlRevision <= 0 || r.Profile != semanticvalue.Profile || r.Revision <= 0 {
		return fmt.Errorf("%w: registered type scope/revision is missing", ErrInvalidContract)
	}
	if err := platformdigest.ValidateSHA256Identity(r.Digest); err != nil {
		return fmt.Errorf("%w: registered type digest: %v", ErrInvalidContract, err)
	}
	ids := map[string]bool{}
	for index, d := range r.Definitions {
		if projectgraph.ResourceID(d.ID).Validate() != nil || ids[d.ID] || semanticvalue.ValidateAttributeName(d.Name) != nil || d.Version <= 0 || (d.Shape != "scalar" && d.Shape != "list") || (index > 0 && r.Definitions[index-1].Name >= d.Name) {
			return fmt.Errorf("%w: registered type identity/order/version", ErrInvalidContract)
		}
		ids[d.ID] = true
		switch d.Type {
		case semanticvalue.TypeString, semanticvalue.TypeBoolean, semanticvalue.TypeInteger, semanticvalue.TypeDecimal, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		default:
			return fmt.Errorf("%w: unsupported registered type", ErrInvalidContract)
		}
	}
	return nil
}

func (r SemanticRegistryTypes) Clone() SemanticRegistryTypes {
	r.Definitions = slices.Clone(r.Definitions)
	return r
}

// ReferencedSemanticAttributes is a structural projection used by the trusted
// adapter to select retained type evidence. It makes no authorization decision.
func ReferencedSemanticAttributes(data []byte) ([]string, error) {
	d, err := decodeDocument(data)
	if err != nil {
		return nil, err
	}
	if d.Kind != "SemanticModel" {
		return nil, nil
	}
	names := map[string]bool{}
	contract, _ := d.Value["contract"].(map[string]any)
	grants, _ := contract["accessGrants"].(map[string]any)
	for _, raw := range grants {
		grant, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: access grant", ErrInvalidContract)
		}
		name, _ := grant["userAttribute"].(string)
		if semanticvalue.ValidateAttributeName(name) != nil {
			return nil, fmt.Errorf("%w: attribute name", ErrInvalidContract)
		}
		names[name] = true
	}
	datasets, _ := contract["datasets"].(map[string]any)
	for _, raw := range datasets {
		dataset, _ := raw.(map[string]any)
		filters, _ := dataset["accessFilters"].([]any)
		for _, rawFilter := range filters {
			filter, _ := rawFilter.(map[string]any)
			name, _ := filter["userAttribute"].(string)
			if semanticvalue.ValidateAttributeName(name) != nil {
				return nil, fmt.Errorf("%w: filter attribute name", ErrInvalidContract)
			}
			names[name] = true
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

// bindRegisteredTypes normalizes only the private comparison tree using the
// established semanticvalue authority. Original projection bytes and digests
// are untouched. Existing classification rules still produce every decision.
func bindRegisteredTypes(d *document, registry SemanticRegistryTypes, candidate bool) error {
	if d.Kind != "SemanticModel" {
		return nil
	}
	definitions := map[string]RegisteredSemanticType{}
	for _, definition := range registry.Definitions {
		definitions[definition.Name] = definition
	}
	definitionFor := func(name string) (RegisteredSemanticType, error) {
		definition, found := definitions[name]
		if !found || (candidate && !definition.Enabled) {
			return RegisteredSemanticType{}, fmt.Errorf("%w: registered attribute %q unavailable", ErrInvalidContract, name)
		}
		return definition, nil
	}
	contract, _ := d.Value["contract"].(map[string]any)
	grants, _ := contract["accessGrants"].(map[string]any)
	for _, raw := range grants {
		grant, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: grant shape", ErrInvalidContract)
		}
		name, _ := grant["userAttribute"].(string)
		definition, err := definitionFor(name)
		if err != nil {
			return err
		}
		values, ok := grant["allowedValues"].([]any)
		if !ok {
			return fmt.Errorf("%w: grant values", ErrInvalidContract)
		}
		inputs := make([]any, len(values))
		for index, rawValue := range values {
			literal, ok := rawValue.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: canonical grant literal", ErrInvalidContract)
			}
			switch literal["type"] {
			case "Number", "Integer", "Decimal":
				text, ok := literal["value"].(string)
				if !ok {
					return fmt.Errorf("%w: canonical numeric literal", ErrInvalidContract)
				}
				inputs[index] = json.Number(text)
			case "String", "Date", "Timestamp", "Boolean":
				inputs[index] = literal["value"]
			default:
				return fmt.Errorf("%w: canonical literal type", ErrInvalidContract)
			}
		}
		set, err := semanticvalue.CanonicalizeSet(definition.Type, inputs)
		if err != nil {
			return fmt.Errorf("%w: registered grant values: %v", ErrInvalidContract, err)
		}
		normalized := make([]any, 0, len(set.Values()))
		for _, value := range set.Values() {
			normalized = append(normalized, map[string]any{"type": string(definition.Type), "value": value.Canonical()})
		}
		grant["allowedValues"] = normalized
	}
	datasets, _ := contract["datasets"].(map[string]any)
	dimensions, _ := contract["dimensions"].(map[string]any)
	for _, raw := range datasets {
		dataset, _ := raw.(map[string]any)
		filters, _ := dataset["accessFilters"].([]any)
		for _, rawFilter := range filters {
			filter, _ := rawFilter.(map[string]any)
			attribute, _ := filter["userAttribute"].(string)
			definition, err := definitionFor(attribute)
			if err != nil {
				return err
			}
			field, _ := filter["field"].(string)
			dimension, _ := dimensions[field].(map[string]any)
			datatype, _ := dimension["datatype"].(string)
			if datatype == "DateTimeTz" {
				datatype = "Timestamp"
			}
			if datatype != string(definition.Type) {
				return fmt.Errorf("%w: filter dimension and registered type differ", ErrInvalidContract)
			}
		}
	}
	return nil
}
