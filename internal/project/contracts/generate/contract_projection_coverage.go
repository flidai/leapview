package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type projectionCoverageManifest struct {
	Profile   string                 `json:"profile"`
	Resources map[string][]exclusion `json:"resources"`
}

type exclusion struct {
	Paths []string `json:"paths"`
}

func verifyContractProjectionCoverage(doc document, manifestPath string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest projectionCoverageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	if manifest.Profile != "leapview.contract/v1" {
		return fmt.Errorf("manifest profile %q is not leapview.contract/v1", manifest.Profile)
	}
	for _, kind := range []string{"Source", "Model", "SemanticModel"} {
		paths, err := contractSourcePaths(doc, kind)
		if err != nil {
			return err
		}
		projectionPaths, err := contractSourcePaths(doc, kind+"ContractProjection")
		if err != nil {
			return err
		}
		excluded := make([]string, 0)
		for _, item := range manifest.Resources[kind] {
			excluded = append(excluded, item.Paths...)
		}
		unclassified := make([]string, 0)
		overlapped := make([]string, 0)
		for _, path := range paths {
			target := projectionTarget(kind, path)
			projected := projectionDTOContains(projectionPaths, target)
			isExcluded := matchesExcludedProjectionPath(excluded, path)
			if projected && isExcluded {
				overlapped = append(overlapped, path)
			}
			if !projected && !isExcluded {
				unclassified = append(unclassified, path)
			}
		}
		if len(overlapped) > 0 {
			return fmt.Errorf("%s authoring fields are both projected and excluded: %s", kind, strings.Join(overlapped, ", "))
		}
		if len(unclassified) > 0 {
			return fmt.Errorf("%s authoring fields are neither projected nor excluded: %s", kind, strings.Join(unclassified, ", "))
		}
		stale := make([]string, 0)
		for _, pattern := range excluded {
			matched := false
			for _, path := range paths {
				if matchesExcludedProjectionPath([]string{pattern}, path) {
					matched = true
					break
				}
			}
			if !matched {
				stale = append(stale, pattern)
			}
		}
		if len(stale) > 0 {
			return fmt.Errorf("%s exclusion paths do not match generated authoring fields: %s", kind, strings.Join(stale, ", "))
		}
	}
	return nil
}

func projectionTarget(kind, path string) string {
	if kind == "Model" && path == "spec.definition.sql" {
		return "contract.definition.sqlAst"
	}
	if strings.HasPrefix(path, "spec.") {
		return "contract." + strings.TrimPrefix(path, "spec.")
	}
	return path
}

func projectionDTOContains(paths []string, target string) bool {
	for _, path := range paths {
		if matchesAnyProjectionPath([]string{target}, path) || strings.HasPrefix(path, strings.TrimSuffix(target, ".*")+".") {
			return true
		}
	}
	return false
}

func contractSourcePaths(doc document, root string) ([]string, error) {
	paths := map[string]struct{}{}
	if err := walkContractSchema(doc, schemaRef{Ref: root}, "", map[string]bool{}, paths); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func walkContractSchema(doc document, ref schemaRef, prefix string, active map[string]bool, paths map[string]struct{}) error {
	if ref.Ref != "" {
		if active[ref.Ref] {
			paths[strings.TrimSuffix(prefix, ".*")] = struct{}{}
			return nil
		}
		value, ok := doc.Schemas[ref.Ref]
		if !ok {
			return fmt.Errorf("unknown schema reference %q", ref.Ref)
		}
		active[ref.Ref] = true
		err := walkContractValue(doc, value, prefix, active, paths)
		delete(active, ref.Ref)
		return err
	}
	return walkContractRefValue(doc, ref, prefix, active, paths)
}

func walkContractValue(doc document, value schema, prefix string, active map[string]bool, paths map[string]struct{}) error {
	if value.Base != nil {
		if err := walkContractSchema(doc, *value.Base, prefix, active, paths); err != nil {
			return err
		}
	}
	if len(value.OneOf) > 0 {
		for _, variant := range value.OneOf {
			if err := walkContractSchema(doc, variant, prefix, active, paths); err != nil {
				return err
			}
		}
		return nil
	}
	if len(value.Properties) > 0 {
		names := make([]string, 0, len(value.Properties))
		for name := range value.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if err := walkContractSchema(doc, value.Properties[name].Schema, path, active, paths); err != nil {
				return err
			}
		}
		return nil
	}
	return walkContractRefValue(doc, schemaRef{Type: value.Type, Items: value.Items, AdditionalProperties: value.AdditionalProperties}, prefix, active, paths)
}

func matchesExcludedProjectionPath(patterns []string, path string) bool {
	segments := strings.Split(path, ".")
	for end := len(segments); end > 0; end-- {
		if matchesAnyProjectionPath(patterns, strings.Join(segments[:end], ".")) {
			return true
		}
	}
	return false
}

func walkContractRefValue(doc document, value schemaRef, prefix string, active map[string]bool, paths map[string]struct{}) error {
	if value.AdditionalProperties != nil {
		return walkContractSchema(doc, value.AdditionalProperties.Schema, prefix+".*", active, paths)
	}
	if value.Items != nil {
		return walkContractSchema(doc, *value.Items, prefix+".*", active, paths)
	}
	if prefix != "" {
		paths[prefix] = struct{}{}
	}
	return nil
}

func matchesAnyProjectionPath(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if matchProjectionSegments(strings.Split(pattern, "."), strings.Split(path, ".")) {
			return true
		}
	}
	return false
}

func matchProjectionSegments(patterns, segments []string) bool {
	if len(patterns) == 0 {
		return len(segments) == 0
	}
	if patterns[0] == "**" {
		return matchProjectionSegments(patterns[1:], segments) || len(segments) > 0 && matchProjectionSegments(patterns, segments[1:])
	}
	return len(segments) > 0 && (patterns[0] == "*" || patterns[0] == segments[0]) && matchProjectionSegments(patterns[1:], segments[1:])
}
