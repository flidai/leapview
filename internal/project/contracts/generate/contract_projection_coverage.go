package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/project/contractprojection"
)

func verifyContractProjectionCoverage(doc document, manifestPath string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	manifest, err := contractprojection.ParseExclusionManifest(raw)
	if err != nil {
		return err
	}
	for _, kind := range []string{"Source", "Model", "SemanticModel"} {
		sourceFields, err := contractSourceFields(doc, kind)
		if err != nil {
			return err
		}
		projectionPaths, err := contractSourcePaths(doc, kind+"ContractProjection")
		if err != nil {
			return err
		}
		stale := make([]string, 0)
		for _, exclusion := range manifest.Resources[kind] {
			for _, pattern := range exclusion.Paths {
				matched := false
				for _, field := range sourceFields {
					if contractprojection.ExclusionPathMatches(pattern, field.Path) {
						matched = true
						break
					}
				}
				if !matched {
					stale = append(stale, pattern)
				}
			}
		}
		if len(stale) > 0 {
			return fmt.Errorf("%s exclusion paths do not match generated authoring fields: %s", kind, strings.Join(stale, ", "))
		}
		unclassified := make([]string, 0)
		overlapped := make([]string, 0)
		for _, field := range sourceFields {
			path := field.Path
			target := projectionTarget(kind, path)
			projected := authoredFieldProjected(kind, path, target, projectionPaths)
			isExcluded := manifest.Excludes(kind, path)
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
		for index, exclusion := range manifest.Resources[kind] {
			got := sourceShapeFingerprint(sourceFields, exclusion.Paths)
			if got != exclusion.SourceShapeFingerprint {
				return fmt.Errorf("%s exclusion group %d source shape changed: got %s, manifest has %s", kind, index, got, exclusion.SourceShapeFingerprint)
			}
		}
		extra := projectionExtras(kind, projectionPaths, sourceFields)
		if len(extra) > 0 {
			return fmt.Errorf("%s projection fields are not authored or explicitly reviewed: %s", kind, strings.Join(extra, ", "))
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

type sourceField struct {
	Path   string
	Shapes []string
}

// projectionAliases documents intentional representation changes between
// authored values and the external DTOs. Canonical values are tagged with a
// type and value in the projection even though authored literals are untyped
// JSON values.
type projectionAlias struct {
	Authored  string
	Projected []string
}

var projectionAliases = map[string][]projectionAlias{
	"SemanticModel": {
		{Authored: "spec.datasets.*.dimensions.*.field", Projected: []string{"contract.dimensions.*.bindings.*.field"}},
		{Authored: "spec.datasets.*.dimensions.*.datatype", Projected: []string{"contract.dimensions.*.datatype"}},
		{Authored: "spec.datasets.*.dimensions.*.requiredAccessGrants.*", Projected: []string{"contract.dimensions.*.requiredAccessGrants.*"}},
		{Authored: "spec.datasets.*.dimensions.*.time.*", Projected: []string{"contract.dimensions.*.time.*"}},
		{Authored: "spec.datasets.*.dimensions.*.time.grains.*", Projected: []string{"contract.dimensions.*.time.grains.*"}},
		{Authored: "spec.datasets.*.metrics.*.type", Projected: []string{"contract.metrics.*.type", "contract.metrics.*.dataset"}},
		{Authored: "spec.datasets.*.metrics.*.agg", Projected: []string{"contract.metrics.*.aggregation"}},
		{Authored: "spec.datasets.*.metrics.*.field", Projected: []string{"contract.metrics.*.input.field"}},
		{Authored: "spec.datasets.*.metrics.*.where.*", Projected: []string{"contract.metrics.*.where.*"}},
		{Authored: "spec.datasets.*.metrics.*.empty", Projected: []string{"contract.metrics.*.empty"}},
		{Authored: "spec.datasets.*.metrics.*.timeDimension", Projected: []string{"contract.metrics.*.timeDimension"}},
		{Authored: "spec.datasets.*.metrics.*.unit", Projected: []string{"contract.metrics.*.unit"}},
		{Authored: "spec.datasets.*.metrics.*.format", Projected: []string{"contract.metrics.*.format"}},
		{Authored: "spec.datasets.*.metrics.*.requiredAccessGrants.*", Projected: []string{"contract.metrics.*.requiredAccessGrants.*"}},
		{Authored: "spec.accessGrants.*.allowedValues.*", Projected: []string{
			"contract.accessGrants.*.allowedValues.*.type",
			"contract.accessGrants.*.allowedValues.*.value",
		}},
		{Authored: "spec.filters.*.value", Projected: []string{
			"contract.filters.*.value.type",
			"contract.filters.*.value.value",
			"contract.filters.*.values.*.type",
			"contract.filters.*.values.*.value",
		}},
		{Authored: "spec.filters.*.value.*", Projected: []string{
			"contract.filters.*.value.type",
			"contract.filters.*.value.value",
			"contract.filters.*.values.*.type",
			"contract.filters.*.values.*.value",
		}},
	},
}

var projectionOnlyAllowlist = map[string][]string{
	"Source":        {"profile"},
	"Model":         {"profile"},
	"SemanticModel": {"profile", "metadata.contract.compatibility", "metadata.contract.version"},
}

func authoredFieldProjected(kind, path, target string, projectionPaths []string) bool {
	for _, projected := range projectionPaths {
		if matchesAnyProjectionPath([]string{target}, projected) {
			return true
		}
	}
	for _, alias := range projectionAliases[kind] {
		if !contractprojection.ExclusionPathMatches(alias.Authored, path) {
			continue
		}
		for _, projected := range projectionPaths {
			for _, aliasTarget := range alias.Projected {
				if matchesAnyProjectionPath([]string{aliasTarget}, projected) {
					return true
				}
			}
		}
	}
	return false
}

func projectionExtras(kind string, projectionPaths []string, sourceFields []sourceField) []string {
	extras := make([]string, 0)
	for _, projected := range projectionPaths {
		if projectionPathAllowlisted(kind, projected) {
			continue
		}
		represented := false
		for _, field := range sourceFields {
			if matchesAnyProjectionPath([]string{projectionTarget(kind, field.Path)}, projected) {
				represented = true
				break
			}
		}
		if !represented {
			for _, alias := range projectionAliases[kind] {
				if !aliasAuthored(alias, sourceFields) {
					continue
				}
				for _, aliasTarget := range alias.Projected {
					if matchesAnyProjectionPath([]string{aliasTarget}, projected) {
						represented = true
						break
					}
				}
				if represented {
					break
				}
			}
		}
		if !represented {
			extras = append(extras, projected)
		}
	}
	sort.Strings(extras)
	return extras
}

func aliasAuthored(alias projectionAlias, sourceFields []sourceField) bool {
	for _, field := range sourceFields {
		if contractprojection.ExclusionPathMatches(alias.Authored, field.Path) {
			return true
		}
	}
	return false
}

func projectionPathAllowlisted(kind, path string) bool {
	for _, pattern := range projectionOnlyAllowlist[kind] {
		if matchesAnyProjectionPath([]string{pattern}, path) {
			return true
		}
	}
	return false
}

func contractSourcePaths(doc document, root string) ([]string, error) {
	fields, err := contractSourceFields(doc, root)
	if err != nil {
		return nil, err
	}
	return sourceFieldPaths(fields), nil
}

func contractSourceFields(doc document, root string) ([]sourceField, error) {
	paths := map[string]map[string]struct{}{}
	if err := walkContractSchema(doc, schemaRef{Ref: root}, "", map[string]bool{}, paths, false); err != nil {
		return nil, err
	}
	result := make([]sourceField, 0, len(paths))
	for path, shapes := range paths {
		field := sourceField{Path: path, Shapes: make([]string, 0, len(shapes))}
		for shape := range shapes {
			field.Shapes = append(field.Shapes, shape)
		}
		sort.Strings(field.Shapes)
		result = append(result, field)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func sourceFieldPaths(fields []sourceField) []string {
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		result = append(result, field.Path)
	}
	return result
}

func addSourceShape(paths map[string]map[string]struct{}, path, shape string) {
	if path == "" {
		return
	}
	if paths[path] == nil {
		paths[path] = map[string]struct{}{}
	}
	paths[path][shape] = struct{}{}
}

func walkContractSchema(doc document, ref schemaRef, prefix string, active map[string]bool, paths map[string]map[string]struct{}, required bool) error {
	if ref.Ref != "" {
		if active[ref.Ref] {
			addSourceShape(paths, strings.TrimSuffix(prefix, ".*"), sourceRecursiveShape(ref.Ref, required))
			return nil
		}
		value, ok := doc.Schemas[ref.Ref]
		if !ok {
			return fmt.Errorf("unknown schema reference %q", ref.Ref)
		}
		active[ref.Ref] = true
		err := walkContractValue(doc, value, prefix, active, paths, required)
		delete(active, ref.Ref)
		return err
	}
	return walkContractRefValue(doc, ref, prefix, active, paths, required)
}

func walkContractValue(doc document, value schema, prefix string, active map[string]bool, paths map[string]map[string]struct{}, required bool) error {
	if value.Base != nil {
		if err := walkContractSchema(doc, *value.Base, prefix, active, paths, required); err != nil {
			return err
		}
	}
	if len(value.OneOf) > 0 {
		for _, variant := range value.OneOf {
			if err := walkContractSchema(doc, variant, prefix, active, paths, required); err != nil {
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
			if err := walkContractSchema(doc, value.Properties[name].Schema, path, active, paths, schemaPropertyRequired(value, name)); err != nil {
				return err
			}
		}
		return nil
	}
	return walkContractRefValue(doc, schemaRef{Type: value.Type, Items: value.Items, AdditionalProperties: value.AdditionalProperties}, prefix, active, paths, required)
}

func walkContractRefValue(doc document, value schemaRef, prefix string, active map[string]bool, paths map[string]map[string]struct{}, required bool) error {
	if value.AdditionalProperties != nil {
		return walkContractSchema(doc, value.AdditionalProperties.Schema, prefix+".*", active, paths, required)
	}
	if value.Items != nil {
		return walkContractSchema(doc, *value.Items, prefix+".*", active, paths, required)
	}
	if prefix != "" {
		addSourceShape(paths, prefix, sourceRefShape(value, required))
	}
	return nil
}

func schemaPropertyRequired(value schema, name string) bool {
	for _, required := range value.Required {
		if required == name {
			return true
		}
	}
	return false
}

func sourceRecursiveShape(ref string, required bool) string {
	shape := "recursive:" + ref
	if required {
		shape += ";required"
	}
	return shape
}

func sourceRefShape(value schemaRef, required bool) string {
	shape := value.Type
	if shape == "" {
		shape = "ref:" + value.Ref
	}
	if len(value.Enum) > 0 {
		enums := append([]string(nil), value.Enum...)
		sort.Strings(enums)
		shape += "[" + strings.Join(enums, ",") + "]"
	}
	if required {
		shape += ";required"
	}
	return shape
}

func sourceShapeFingerprint(fields []sourceField, patterns []string) string {
	entries := make([]string, 0)
	for _, field := range fields {
		matched := false
		for _, pattern := range patterns {
			if contractprojection.ExclusionPathMatches(pattern, field.Path) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for _, shape := range field.Shapes {
			entries = append(entries, field.Path+"\x00"+shape)
		}
	}
	sort.Strings(entries)
	hash := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return fmt.Sprintf("sha256:%x", hash)
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
