package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

var reservedPartitionImportAliases = map[string]struct{}{
	"bytes": {}, "json": {}, "fmt": {},
}

func partitionContractImports(
	projection goPackageProjection,
	configuredImports ...map[string]contractImportSpec,
) (map[string]contractImportSpec, error) {
	imports := map[string]contractImportSpec{}
	usedAliases := map[string]string{}
	if len(configuredImports) > 0 {
		for namespace, binding := range configuredImports[0] {
			imports[namespace] = binding
			if binding.GoAlias == "" {
				continue
			}
			if previous, exists := usedAliases[binding.GoAlias]; exists && previous != binding.GoPackage {
				return nil, fmt.Errorf("configured imports %q and %q share Go alias %q", previous, binding.GoPackage, binding.GoAlias)
			}
			usedAliases[binding.GoAlias] = binding.GoPackage
		}
	}
	dependenciesByImportPath := map[string]*goPackageDependency{}
	for _, dependency := range projection.Dependencies {
		importPath := strings.TrimSpace(dependency.Output.ImportPath)
		if importPath == "" {
			return nil, fmt.Errorf("dependency package %q has no import path", dependency.Output.Package)
		}
		packageName := strings.TrimSpace(dependency.Output.Package)
		if !goPackagePattern.MatchString(packageName) {
			return nil, fmt.Errorf("dependency import path %q has invalid Go package %q", importPath, dependency.Output.Package)
		}
		existing := dependenciesByImportPath[importPath]
		if existing == nil {
			copy := dependency
			copy.Schemas = append([]goPackageDependencySchema(nil), dependency.Schemas...)
			dependenciesByImportPath[importPath] = &copy
			continue
		}
		if existing.Output != dependency.Output {
			return nil, fmt.Errorf("dependency import path %q has inconsistent output identity", importPath)
		}
		existing.Schemas = append(existing.Schemas, dependency.Schemas...)
	}

	importPaths := make([]string, 0, len(dependenciesByImportPath))
	packageCounts := map[string]int{}
	for importPath, dependency := range dependenciesByImportPath {
		importPaths = append(importPaths, importPath)
		packageCounts[dependency.Output.Package]++
	}
	sort.Strings(importPaths)

	aliases := make(map[string]string, len(importPaths))
	for _, importPath := range importPaths {
		packageName := dependenciesByImportPath[importPath].Output.Package
		alias := packageName
		reserved := isReservedPartitionImportAlias(alias)
		previous, aliasUsed := usedAliases[alias]
		if reserved || packageCounts[packageName] > 1 || (aliasUsed && previous != importPath) {
			var err error
			alias, err = stablePartitionImportAlias(packageName, importPath, usedAliases)
			if err != nil {
				return nil, err
			}
		}
		usedAliases[alias] = importPath
		aliases[importPath] = alias
	}

	for _, importPath := range importPaths {
		dependency := dependenciesByImportPath[importPath]
		binding := contractImportSpec{
			GoPackage:      importPath,
			GoAlias:        aliases[importPath],
			ExactNamespace: true,
		}
		schemas := append([]goPackageDependencySchema(nil), dependency.Schemas...)
		sort.Slice(schemas, func(left, right int) bool {
			if schemas[left].Namespace != schemas[right].Namespace {
				return schemas[left].Namespace < schemas[right].Namespace
			}
			return schemas[left].Name < schemas[right].Name
		})
		for _, schema := range schemas {
			namespace := strings.TrimSpace(schema.Namespace)
			if namespace == "" {
				return nil, fmt.Errorf("dependency schema %q has no namespace", schema.Name)
			}
			if previous, exists := imports[namespace]; exists && previous != binding {
				return nil, fmt.Errorf("dependency namespace %q has multiple package owners", namespace)
			}
			imports[namespace] = binding
		}
	}
	if len(imports) == 0 {
		return nil, nil
	}
	return imports, nil
}

func stablePartitionImportAlias(packageName, importPath string, used map[string]string) (string, error) {
	sum := sha256.Sum256([]byte(importPath))
	digest := hex.EncodeToString(sum[:])
	for length := 8; length <= len(digest); length += 2 {
		alias := packageName + "_" + digest[:length]
		if isReservedPartitionImportAlias(alias) {
			continue
		}
		if previous, exists := used[alias]; !exists || previous == importPath {
			return alias, nil
		}
	}
	return "", fmt.Errorf("cannot allocate a unique Go import alias for %q", importPath)
}

func isReservedPartitionImportAlias(alias string) bool {
	if _, keyword := goKeywords[alias]; keyword {
		return true
	}
	_, reserved := reservedPartitionImportAliases[alias]
	return reserved
}
