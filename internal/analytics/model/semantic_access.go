package model

import (
	"encoding/json"
	"fmt"
)

// SemanticAccessLiteral is the closed portable representation of one authored
// access-grant literal. Number preserves the exact JSON token emitted by the
// generated contract so artifact round trips cannot reinterpret it as a
// float64 before the target registry supplies its logical type.
type SemanticAccessLiteral struct {
	Kind string `json:"kind" yaml:"kind"`
	Text string `json:"text,omitempty" yaml:"text,omitempty"`
	Bool bool   `json:"bool,omitempty" yaml:"bool,omitempty"`
}

const (
	SemanticAccessString  = "string"
	SemanticAccessNumber  = "number"
	SemanticAccessBoolean = "boolean"
)

func NewSemanticAccessLiteral(value any) (SemanticAccessLiteral, error) {
	switch typed := value.(type) {
	case string:
		return SemanticAccessLiteral{Kind: SemanticAccessString, Text: typed}, nil
	case json.Number:
		if typed == "" {
			return SemanticAccessLiteral{}, fmt.Errorf("semantic access number is empty")
		}
		return SemanticAccessLiteral{Kind: SemanticAccessNumber, Text: string(typed)}, nil
	case bool:
		return SemanticAccessLiteral{Kind: SemanticAccessBoolean, Bool: typed}, nil
	default:
		return SemanticAccessLiteral{}, fmt.Errorf("semantic access literal has unsupported type %T", value)
	}
}

// Value restores the strict input expected by semanticvalue. It never parses
// or coerces a number token.
func (value SemanticAccessLiteral) Value() (any, error) {
	switch value.Kind {
	case SemanticAccessString:
		return value.Text, nil
	case SemanticAccessNumber:
		if value.Text == "" {
			return nil, fmt.Errorf("semantic access number is empty")
		}
		return json.Number(value.Text), nil
	case SemanticAccessBoolean:
		return value.Bool, nil
	default:
		return nil, fmt.Errorf("unsupported semantic access literal kind %q", value.Kind)
	}
}

type SemanticAccessGrantSpec struct {
	UserAttribute string                  `json:"userAttribute" yaml:"userAttribute"`
	AllowedValues []SemanticAccessLiteral `json:"allowedValues" yaml:"allowedValues"`
}

type SemanticAccessFilterSpec struct {
	Field         string `json:"field" yaml:"field"`
	UserAttribute string `json:"userAttribute" yaml:"userAttribute"`
}

type SemanticDatasetAccessSpec struct {
	RequiredAccessGrants []string                   `json:"requiredAccessGrants,omitempty" yaml:"requiredAccessGrants,omitempty"`
	AccessFilters        []SemanticAccessFilterSpec `json:"accessFilters,omitempty" yaml:"accessFilters,omitempty"`
}

// SemanticAccessPolicy is the portable authored policy retained by the
// semantic model. It contains references and allowed values, never target
// registry definitions, principal assignments, or trusted claim values.
type SemanticAccessPolicy struct {
	AccessGrants map[string]SemanticAccessGrantSpec   `json:"accessGrants,omitempty" yaml:"accessGrants,omitempty"`
	Datasets     map[string]SemanticDatasetAccessSpec `json:"datasets,omitempty" yaml:"datasets,omitempty"`
	Dimensions   map[string][]string                  `json:"dimensions,omitempty" yaml:"dimensions,omitempty"`
	Metrics      map[string][]string                  `json:"metrics,omitempty" yaml:"metrics,omitempty"`
}

func (policy SemanticAccessPolicy) Empty() bool {
	return len(policy.AccessGrants) == 0 && len(policy.Datasets) == 0 && len(policy.Dimensions) == 0 && len(policy.Metrics) == 0
}

func cloneSemanticAccessPolicy(policy SemanticAccessPolicy) SemanticAccessPolicy {
	clone := SemanticAccessPolicy{}
	if policy.AccessGrants != nil {
		clone.AccessGrants = make(map[string]SemanticAccessGrantSpec, len(policy.AccessGrants))
		for name, grant := range policy.AccessGrants {
			grant.AllowedValues = append([]SemanticAccessLiteral(nil), grant.AllowedValues...)
			clone.AccessGrants[name] = grant
		}
	}
	if policy.Datasets != nil {
		clone.Datasets = make(map[string]SemanticDatasetAccessSpec, len(policy.Datasets))
		for name, dataset := range policy.Datasets {
			dataset.RequiredAccessGrants = append([]string(nil), dataset.RequiredAccessGrants...)
			dataset.AccessFilters = append([]SemanticAccessFilterSpec(nil), dataset.AccessFilters...)
			clone.Datasets[name] = dataset
		}
	}
	if policy.Dimensions != nil {
		clone.Dimensions = make(map[string][]string, len(policy.Dimensions))
		for name, grants := range policy.Dimensions {
			clone.Dimensions[name] = append([]string(nil), grants...)
		}
	}
	if policy.Metrics != nil {
		clone.Metrics = make(map[string][]string, len(policy.Metrics))
		for name, grants := range policy.Metrics {
			clone.Metrics[name] = append([]string(nil), grants...)
		}
	}
	return clone
}
