// Package contractutil contains small helpers shared by contract model emitters.
package contractutil

import (
	"sort"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// DependencyNames returns the sorted transitive schema dependency set for all
// contract roots in doc.
func DependencyNames(doc ir.Document) []string {
	local, _ := DependencyNamesPartition(doc, nil)
	return local
}

// DependencyNamesPartition returns local dependencies and the first imported
// schema roots crossed while traversing contract dependencies.
func DependencyNamesPartition(doc ir.Document, external func(string, ir.Schema) bool) ([]string, []string) {
	seen := map[string]struct{}{}
	imported := map[string]struct{}{}
	queue := []string{}
	for _, contract := range doc.Contracts {
		if name, ok := ir.NormalizedSchemaRefName(contract.Schema); ok && external != nil {
			if schema, exists := doc.Schemas[name]; exists && external(name, schema) {
				continue
			}
		}
		collectRef(contract.Schema, seen, &queue)
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		schema, ok := doc.Schemas[name]
		if !ok {
			continue
		}
		if external != nil && external(name, schema) {
			imported[name] = struct{}{}
			continue
		}
		collectSchema(schema, seen, &queue)
	}
	names := make([]string, 0, len(seen)-len(imported))
	for name := range seen {
		if _, ok := imported[name]; ok {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	importedNames := make([]string, 0, len(imported))
	for name := range imported {
		importedNames = append(importedNames, name)
	}
	sort.Strings(importedNames)
	return names, importedNames
}

// OrderedProperties returns schema properties in authored order followed by any
// remaining properties in stable lexical order.
func OrderedProperties(schema ir.Schema) []string {
	seen := map[string]struct{}{}
	names := []string{}
	for _, name := range schema.PropertyOrder {
		if _, ok := schema.Properties[name]; ok {
			names = append(names, name)
			seen[name] = struct{}{}
		}
	}
	remaining := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		if _, ok := seen[name]; !ok {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	return append(names, remaining...)
}

// RequiredSet returns a lookup set for required property names.
func RequiredSet(schema ir.Schema) map[string]struct{} {
	required := make(map[string]struct{}, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = struct{}{}
	}
	return required
}

func collectSchema(schema ir.Schema, seen map[string]struct{}, queue *[]string) {
	for _, property := range schema.Properties {
		collectRef(property.Schema, seen, queue)
	}
	if schema.Items != nil {
		collectRef(*schema.Items, seen, queue)
	}
	if schema.Base != nil {
		collectRef(*schema.Base, seen, queue)
	}
	for _, variant := range schema.OneOf {
		collectRef(variant, seen, queue)
	}
}

func collectRef(ref ir.SchemaRef, seen map[string]struct{}, queue *[]string) {
	if name, ok := ir.NormalizedSchemaRefName(ref); ok {
		if _, exists := seen[name]; !exists {
			seen[name] = struct{}{}
			*queue = append(*queue, name)
		}
	}
	if ref.Items != nil {
		collectRef(*ref.Items, seen, queue)
	}
	if ref.PropertyNames != nil {
		collectRef(*ref.PropertyNames, seen, queue)
	}
	if ref.AdditionalProperties != nil && ref.AdditionalProperties.Schema != nil {
		collectRef(*ref.AdditionalProperties.Schema, seen, queue)
	}
}
