// Package ossie implements the pinned Apache Ossie 0.2.0.dev0 interchange
// adapter.  The schema and version are intentionally pinned: a document from
// another Ossie revision must be rejected before any native resource is built.
package ossie

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	// Version is the Ossie core-spec version inspected by this adapter.
	Version = "0.2.0.dev0"
	// InspectedCommit identifies the exact upstream schema/spec used here.
	InspectedCommit = "88e0011148283302c9a04cd0287e00e0b9d87354"
	// ExtensionVendor is the sole LeapView extension namespace in an Ossie
	// document.  Its data is a versioned JSON object, never an executable SQL
	// escape hatch.
	ExtensionVendor  = "LEAPVIEW"
	ExtensionVersion = "leapview.dev/ossie-extension/v1"
)

// Document is the official Ossie document envelope.
type Document struct {
	Version       string          `yaml:"version" json:"version"`
	SemanticModel []SemanticModel `yaml:"semantic_model" json:"semantic_model"`
}

type SemanticModel struct {
	Name             string            `yaml:"name" json:"name"`
	Description      string            `yaml:"description,omitempty" json:"description,omitempty"`
	AIContext        *AIContext        `yaml:"ai_context,omitempty" json:"ai_context,omitempty"`
	Datasets         []Dataset         `yaml:"datasets" json:"datasets"`
	Relationships    []Relationship    `yaml:"relationships,omitempty" json:"relationships,omitempty"`
	Metrics          []Metric          `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	CustomExtensions []CustomExtension `yaml:"custom_extensions,omitempty" json:"custom_extensions,omitempty"`
}

// AIContext accepts both forms allowed by the core schema. String context is
// represented as Instructions when crossing the native boundary.
type AIContext struct {
	Instructions string   `json:"instructions,omitempty" yaml:"instructions,omitempty"`
	Synonyms     []string `json:"synonyms,omitempty" yaml:"synonyms,omitempty"`
	Examples     []string `json:"examples,omitempty" yaml:"examples,omitempty"`
}

func (c *AIContext) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Instructions = text
		return nil
	}
	type context AIContext
	var value context
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("ossie ai_context: %w", err)
	}
	*c = AIContext(value)
	return nil
}

func (c *AIContext) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		c.Instructions = value.Value
		return nil
	}
	type context AIContext
	var decoded context
	if err := value.Decode(&decoded); err != nil {
		return fmt.Errorf("ossie ai_context: %w", err)
	}
	*c = AIContext(decoded)
	return nil
}

type CustomExtension struct {
	VendorName string `yaml:"vendor_name" json:"vendor_name"`
	Data       string `yaml:"data" json:"data"`
}

type Dataset struct {
	Name             string            `yaml:"name" json:"name"`
	Source           string            `yaml:"source" json:"source"`
	PrimaryKey       []string          `yaml:"primary_key,omitempty" json:"primary_key,omitempty"`
	UniqueKeys       [][]string        `yaml:"unique_keys,omitempty" json:"unique_keys,omitempty"`
	Description      string            `yaml:"description,omitempty" json:"description,omitempty"`
	AIContext        *AIContext        `yaml:"ai_context,omitempty" json:"ai_context,omitempty"`
	Fields           []Field           `yaml:"fields,omitempty" json:"fields,omitempty"`
	CustomExtensions []CustomExtension `yaml:"custom_extensions,omitempty" json:"custom_extensions,omitempty"`
}

type Relationship struct {
	Name             string            `yaml:"name" json:"name"`
	From             string            `yaml:"from" json:"from"`
	To               string            `yaml:"to" json:"to"`
	FromColumns      []string          `yaml:"from_columns" json:"from_columns"`
	ToColumns        []string          `yaml:"to_columns" json:"to_columns"`
	AIContext        *AIContext        `yaml:"ai_context,omitempty" json:"ai_context,omitempty"`
	CustomExtensions []CustomExtension `yaml:"custom_extensions,omitempty" json:"custom_extensions,omitempty"`
}

type Field struct {
	Name             string            `yaml:"name" json:"name"`
	Expression       Expression        `yaml:"expression" json:"expression"`
	Dimension        *Dimension        `yaml:"dimension,omitempty" json:"dimension,omitempty"`
	Label            string            `yaml:"label,omitempty" json:"label,omitempty"`
	Description      string            `yaml:"description,omitempty" json:"description,omitempty"`
	Datatype         string            `yaml:"datatype,omitempty" json:"datatype,omitempty"`
	AIContext        *AIContext        `yaml:"ai_context,omitempty" json:"ai_context,omitempty"`
	CustomExtensions []CustomExtension `yaml:"custom_extensions,omitempty" json:"custom_extensions,omitempty"`
}

type Dimension struct {
	IsTime *bool `yaml:"is_time,omitempty" json:"is_time,omitempty"`
}

type Expression struct {
	Dialects []DialectExpression `yaml:"dialects" json:"dialects"`
}

type DialectExpression struct {
	Dialect    string `yaml:"dialect" json:"dialect"`
	Expression string `yaml:"expression" json:"expression"`
}

type Metric struct {
	Name             string            `yaml:"name" json:"name"`
	Expression       Expression        `yaml:"expression" json:"expression"`
	Description      string            `yaml:"description,omitempty" json:"description,omitempty"`
	Datatype         string            `yaml:"datatype,omitempty" json:"datatype,omitempty"`
	AIContext        *AIContext        `yaml:"ai_context,omitempty" json:"ai_context,omitempty"`
	CustomExtensions []CustomExtension `yaml:"custom_extensions,omitempty" json:"custom_extensions,omitempty"`
}
