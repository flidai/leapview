package main

import (
	"fmt"
	"sort"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

type goPackageProjection struct {
	Partition    goPackagePartition
	Document     ir.Document
	Dependencies []goPackageDependency
}

type goPackageDependency struct {
	Output  resolvedGoPackageOutput
	Schemas []goPackageDependencySchema
}

type goPackageDependencySchema struct {
	Name      string
	Namespace string
}

func projectGoPackagePartitions(
	doc ir.Document,
	partitions []goPackagePartition,
	configuredImports ...map[string]contractImportSpec,
) ([]goPackageProjection, error) {
	var imports map[string]contractImportSpec
	if len(configuredImports) > 0 {
		imports = configuredImports[0]
	}
	externalBindings := emitterContractImports(imports)
	endpoints := make(map[string]ir.Endpoint, len(doc.Endpoints))
	for _, endpoint := range doc.Endpoints {
		if _, exists := endpoints[endpoint.OperationID]; exists {
			return nil, fmt.Errorf("source document has duplicate endpoint %q", endpoint.OperationID)
		}
		endpoints[endpoint.OperationID] = endpoint
	}

	schemaOwners := make(map[string]int, len(doc.Schemas))
	for partitionIndex, partition := range partitions {
		for _, name := range partition.OwnedSchemaNames {
			if _, exists := doc.Schemas[name]; !exists {
				return nil, fmt.Errorf("partition %q references unknown schema %q", partition.Output.ImportPath, name)
			}
			if previous, exists := schemaOwners[name]; exists && previous != partitionIndex {
				return nil, fmt.Errorf("schema %q has multiple package owners", name)
			}
			schemaOwners[name] = partitionIndex
		}
	}

	projections := make([]goPackageProjection, 0, len(partitions))
	for partitionIndex, partition := range partitions {
		projected := doc
		projected.Contracts = nil
		projected.Endpoints = nil
		projected.Schemas = nil
		projected.TransportErrors = nil

		for _, operationID := range partition.EndpointOperationIDs {
			endpoint, exists := endpoints[operationID]
			if !exists {
				return nil, fmt.Errorf("partition %q references unknown endpoint %q", partition.Output.ImportPath, operationID)
			}
			projected.Endpoints = append(projected.Endpoints, endpoint)
		}
		if len(projected.Endpoints) > 0 {
			projected.TransportErrors = doc.TransportErrors
		}

		schemaSet := make(map[string]struct{}, len(partition.OwnedSchemaNames)+len(partition.DependencySchemaNames))
		for _, name := range partition.OwnedSchemaNames {
			schemaSet[name] = struct{}{}
		}

		dependenciesByOutput := map[string]*goPackageDependency{}
		for _, name := range partition.DependencySchemaNames {
			schema, exists := doc.Schemas[name]
			if !exists {
				return nil, fmt.Errorf("partition %q references unknown schema %q", partition.Output.ImportPath, name)
			}
			ownerIndex, exists := schemaOwners[name]
			if !exists {
				if _, _, external := externalBindings.Resolve(schema.Namespace); external {
					schemaSet[name] = struct{}{}
					continue
				}
				return nil, fmt.Errorf("dependency schema %q has no package owner", name)
			}
			schemaSet[name] = struct{}{}
			if ownerIndex == partitionIndex {
				continue
			}
			owner := partitions[ownerIndex].Output
			if owner.ImportPath == "" {
				return nil, fmt.Errorf("dependency schema %q owner has no import path", name)
			}
			key := goPackageOutputKey(owner)
			dependency := dependenciesByOutput[key]
			if dependency == nil {
				dependency = &goPackageDependency{Output: owner}
				dependenciesByOutput[key] = dependency
			}
			dependency.Schemas = append(dependency.Schemas, goPackageDependencySchema{
				Name:      name,
				Namespace: schema.Namespace,
			})
		}

		schemaNames := make([]string, 0, len(schemaSet))
		for name := range schemaSet {
			schemaNames = append(schemaNames, name)
		}
		sort.Strings(schemaNames)
		if len(schemaNames) > 0 {
			projected.Schemas = make(map[string]ir.Schema, len(schemaNames))
			for _, name := range schemaNames {
				schema, exists := doc.Schemas[name]
				if !exists {
					return nil, fmt.Errorf("partition %q references unknown schema %q", partition.Output.ImportPath, name)
				}
				projected.Schemas[name] = schema
			}
		}
		requiredSchemas := map[string]struct{}{}
		for _, endpoint := range projected.Endpoints {
			collectEndpointSchemaNames(endpoint, requiredSchemas)
		}
		for _, schema := range projected.Schemas {
			collectSchemaNames(schema, requiredSchemas)
		}
		if projected.TransportErrors != nil {
			collectSchemaRefNames(projected.TransportErrors.Schema, requiredSchemas)
		}
		requiredNames := make([]string, 0, len(requiredSchemas))
		for name := range requiredSchemas {
			requiredNames = append(requiredNames, name)
		}
		sort.Strings(requiredNames)
		for _, name := range requiredNames {
			if _, exists := doc.Schemas[name]; !exists {
				return nil, fmt.Errorf("partition %q requires unknown schema %q", partition.Output.ImportPath, name)
			}
			if _, exists := schemaSet[name]; !exists {
				return nil, fmt.Errorf("partition %q omits required schema %q", partition.Output.ImportPath, name)
			}
		}

		var dependencies []goPackageDependency
		if len(dependenciesByOutput) > 0 {
			dependencies = make([]goPackageDependency, 0, len(dependenciesByOutput))
		}
		for _, dependency := range dependenciesByOutput {
			sort.Slice(dependency.Schemas, func(left, right int) bool {
				if dependency.Schemas[left].Name != dependency.Schemas[right].Name {
					return dependency.Schemas[left].Name < dependency.Schemas[right].Name
				}
				return dependency.Schemas[left].Namespace < dependency.Schemas[right].Namespace
			})
			dependencies = append(dependencies, *dependency)
		}
		sort.Slice(dependencies, func(left, right int) bool {
			if dependencies[left].Output.ImportPath != dependencies[right].Output.ImportPath {
				return dependencies[left].Output.ImportPath < dependencies[right].Output.ImportPath
			}
			return goPackageOutputKey(dependencies[left].Output) < goPackageOutputKey(dependencies[right].Output)
		})

		projections = append(projections, goPackageProjection{
			Partition:    partition,
			Document:     projected,
			Dependencies: dependencies,
		})
	}
	return projections, nil
}
