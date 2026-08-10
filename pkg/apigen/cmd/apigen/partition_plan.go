package main

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

type goPackagePartition struct {
	Output                resolvedGoPackageOutput
	Namespaces            []string
	EndpointOperationIDs  []string
	OwnedSchemaNames      []string
	DependencySchemaNames []string
}

type goPackagePartitionAccumulator struct {
	partition      goPackagePartition
	dependencyRoot map[string]struct{}
}

func planGoPackagePartitions(
	doc ir.Document,
	plan goPackagePlan,
	configuredImports ...map[string]contractImportSpec,
) ([]goPackagePartition, error) {
	var imports map[string]contractImportSpec
	if len(configuredImports) > 0 {
		imports = configuredImports[0]
	}
	externalBindings := emitterContractImports(imports)
	byOutput := make(map[string]*goPackagePartitionAccumulator, len(plan.Packages)+1)
	byNamespace := make(map[string]*goPackagePartitionAccumulator, len(plan.Packages))

	addOutput := func(output resolvedGoPackageOutput) (*goPackagePartitionAccumulator, error) {
		key := goPackageOutputKey(output)
		if existing, ok := byOutput[key]; ok {
			if existing.partition.Output != output {
				return nil, fmt.Errorf("go_out package %q has inconsistent file outputs", output.Dir)
			}
			return existing, nil
		}
		accumulator := &goPackagePartitionAccumulator{
			partition:      goPackagePartition{Output: output},
			dependencyRoot: map[string]struct{}{},
		}
		byOutput[key] = accumulator
		return accumulator, nil
	}

	var defaultPartition *goPackagePartitionAccumulator
	if plan.Default != nil {
		var err error
		defaultPartition, err = addOutput(*plan.Default)
		if err != nil {
			return nil, err
		}
	}
	for _, configured := range plan.Packages {
		if importedNamespace, _, external := externalBindings.Resolve(configured.Namespace); external {
			return nil, fmt.Errorf(
				"go_out package namespace %q conflicts with external contract import %q",
				configured.Namespace,
				importedNamespace,
			)
		}
		partition, err := addOutput(configured.Output)
		if err != nil {
			return nil, err
		}
		partition.partition.Namespaces = append(partition.partition.Namespaces, configured.Namespace)
		byNamespace[configured.Namespace] = partition
	}

	resolveOwner := func(kind, name, namespace string) (*goPackagePartitionAccumulator, error) {
		if partition, ok := byNamespace[namespace]; ok {
			return partition, nil
		}
		if plan.Unmatched == unmatchedNamespaceDefault && defaultPartition != nil {
			return defaultPartition, nil
		}
		return nil, fmt.Errorf("%s %q namespace %q has no package mapping", kind, name, namespace)
	}

	endpoints := append([]ir.Endpoint(nil), doc.Endpoints...)
	sort.Slice(endpoints, func(left, right int) bool {
		if endpoints[left].OperationID != endpoints[right].OperationID {
			return endpoints[left].OperationID < endpoints[right].OperationID
		}
		if endpoints[left].Method != endpoints[right].Method {
			return endpoints[left].Method < endpoints[right].Method
		}
		return endpoints[left].Path < endpoints[right].Path
	})
	for _, endpoint := range endpoints {
		if importedNamespace, _, external := externalBindings.Resolve(endpoint.Namespace); external {
			return nil, fmt.Errorf(
				"endpoint %q namespace %q conflicts with external contract import %q",
				endpoint.OperationID,
				endpoint.Namespace,
				importedNamespace,
			)
		}
		partition, err := resolveOwner("endpoint", endpoint.OperationID, endpoint.Namespace)
		if err != nil {
			return nil, err
		}
		partition.partition.EndpointOperationIDs = append(partition.partition.EndpointOperationIDs, endpoint.OperationID)
		collectEndpointSchemaNames(endpoint, partition.dependencyRoot)
	}

	schemaOwners := make(map[string]*goPackagePartitionAccumulator, len(doc.Schemas))
	schemaNames := sortedSchemaNames(doc.Schemas)
	for _, name := range schemaNames {
		schema := doc.Schemas[name]
		if _, _, external := externalBindings.Resolve(schema.Namespace); external {
			continue
		}
		partition, err := resolveOwner("schema", name, schema.Namespace)
		if err != nil {
			return nil, err
		}
		schemaOwners[name] = partition
		partition.partition.OwnedSchemaNames = append(partition.partition.OwnedSchemaNames, name)
		partition.dependencyRoot[name] = struct{}{}
	}

	if doc.TransportErrors != nil {
		if name, ok := ir.NormalizedSchemaRefName(doc.TransportErrors.Schema); ok {
			for _, partition := range byOutput {
				if len(partition.partition.EndpointOperationIDs) > 0 {
					partition.dependencyRoot[name] = struct{}{}
				}
			}
		}
	}

	for _, partition := range byOutput {
		dependencies, err := transitiveExternalSchemaNames(doc.Schemas, schemaOwners, partition, partition.dependencyRoot)
		if err != nil {
			return nil, err
		}
		partition.partition.DependencySchemaNames = dependencies
		sort.Strings(partition.partition.Namespaces)
	}

	ordered := make([]*goPackagePartitionAccumulator, 0, len(byOutput))
	for _, partition := range byOutput {
		ordered = append(ordered, partition)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left] == defaultPartition {
			return ordered[right] != defaultPartition
		}
		if ordered[right] == defaultPartition {
			return false
		}
		leftKey := goPackageOutputKey(ordered[left].partition.Output)
		rightKey := goPackageOutputKey(ordered[right].partition.Output)
		return leftKey < rightKey
	})
	partitions := make([]goPackagePartition, 0, len(ordered))
	for _, partition := range ordered {
		partitions = append(partitions, partition.partition)
	}
	return partitions, nil
}

func goPackageOutputKey(output resolvedGoPackageOutput) string {
	return filepath.Clean(output.Dir) + "\x00" + output.Package
}

func sortedSchemaNames(schemas map[string]ir.Schema) []string {
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func transitiveExternalSchemaNames(
	schemas map[string]ir.Schema,
	owners map[string]*goPackagePartitionAccumulator,
	partition *goPackagePartitionAccumulator,
	roots map[string]struct{},
) ([]string, error) {
	queue := make([]string, 0, len(roots))
	for name := range roots {
		queue = append(queue, name)
	}
	sort.Strings(queue)

	seen := make(map[string]struct{}, len(queue))
	external := map[string]struct{}{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		schema, ok := schemas[name]
		if !ok {
			return nil, fmt.Errorf("schema reference %q does not exist", name)
		}
		if owners[name] != partition {
			external[name] = struct{}{}
		}
		references := map[string]struct{}{}
		collectSchemaNames(schema, references)
		names := make([]string, 0, len(references))
		for dependency := range references {
			names = append(names, dependency)
		}
		sort.Strings(names)
		queue = append(queue, names...)
	}

	names := make([]string, 0, len(external))
	for name := range external {
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil
	}
	sort.Strings(names)
	return names, nil
}

func collectEndpointSchemaNames(endpoint ir.Endpoint, names map[string]struct{}) {
	for _, parameter := range endpoint.Parameters {
		collectSchemaRefNames(parameter.Schema, names)
	}
	if endpoint.RequestBody != nil {
		for _, content := range endpoint.RequestBody.Contents {
			collectBodyContentSchemaNames(content, names)
		}
	}
	for _, response := range endpoint.Responses {
		for _, header := range response.Headers {
			collectSchemaRefNames(header.Schema, names)
		}
		for _, content := range response.Contents {
			collectBodyContentSchemaNames(content, names)
		}
	}
}

func collectBodyContentSchemaNames(content ir.BodyContent, names map[string]struct{}) {
	if content.Schema != nil {
		collectSchemaRefNames(*content.Schema, names)
	}
	for _, schema := range content.AnyOf {
		collectSchemaRefNames(schema, names)
	}
	for _, part := range content.Parts {
		if part.Schema != nil {
			collectSchemaRefNames(*part.Schema, names)
		}
	}
}

func collectSchemaNames(schema ir.Schema, names map[string]struct{}) {
	for _, property := range schema.Properties {
		collectSchemaRefNames(property.Schema, names)
	}
	if schema.Items != nil {
		collectSchemaRefNames(*schema.Items, names)
	}
	if schema.Base != nil {
		collectSchemaRefNames(*schema.Base, names)
	}
	for _, variant := range schema.OneOf {
		collectSchemaRefNames(variant, names)
	}
}

func collectSchemaRefNames(schema ir.SchemaRef, names map[string]struct{}) {
	if name, ok := ir.NormalizedSchemaRefName(schema); ok {
		names[name] = struct{}{}
	}
	if schema.Items != nil {
		collectSchemaRefNames(*schema.Items, names)
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		collectSchemaRefNames(*schema.AdditionalProperties.Schema, names)
	}
}
