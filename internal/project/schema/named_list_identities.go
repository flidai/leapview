package configschema

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

func mappingMember(node *yaml.Node, name string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == name {
			return node.Content[index+1]
		}
	}
	return nil
}

func checkNamedListIdentities(kind Kind, filename string, root *yaml.Node) error {
	spec := mappingMember(root, "spec")
	if spec == nil {
		return nil
	}
	check := func(parent *yaml.Node, collection, identity string) error {
		return checkNamedCollection(filename, mappingMember(parent, collection), collection, identity)
	}
	switch kind {
	case KindSource:
		return check(spec, "fields", "name")
	case KindModel:
		if err := check(spec, "fields", "name"); err != nil {
			return err
		}
		return check(spec, "entities", "name")
	case KindDashboard:
		return check(spec, "visuals", "id")
	case KindSemanticModel:
		datasets := mappingMember(spec, "datasets")
		if err := checkNamedCollection(filename, datasets, "datasets", "name"); err != nil {
			return err
		}
		if datasets != nil && datasets.Kind == yaml.SequenceNode {
			for _, dataset := range datasets.Content {
				for _, collection := range []string{"dimensions", "metrics"} {
					if err := check(dataset, collection, "name"); err != nil {
						return err
					}
				}
			}
		}
		dimensions := mappingMember(spec, "dimensions")
		if err := checkNamedCollection(filename, dimensions, "dimensions", "name"); err != nil {
			return err
		}
		if dimensions != nil && dimensions.Kind == yaml.SequenceNode {
			for _, dimension := range dimensions.Content {
				if err := check(dimension, "bindings", "dataset"); err != nil {
					return err
				}
			}
		}
		for _, collection := range []string{"accessGrants", "relationships", "filters", "metrics"} {
			if err := check(spec, collection, "name"); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkNamedCollection(filename string, node *yaml.Node, collection, identity string) error {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	seen := map[string]*yaml.Node{}
	for _, entry := range node.Content {
		id := mappingMember(entry, identity)
		if id == nil || id.Kind != yaml.ScalarNode {
			continue
		}
		if previous, exists := seen[id.Value]; exists {
			return resourceDiagnostic(filename, id, "schema.duplicate_identity", fmt.Sprintf("%s %s %q is duplicated (first defined at line %d)", collection, identity, id.Value, previous.Line))
		}
		seen[id.Value] = id
	}
	return nil
}
