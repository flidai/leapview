package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/flidai/leapview/pkg/duckdbsql"
)

// enumSourceSpec maps the closed parser enums to their upstream declarations.
// The source paths and hashes are pinned in upstream.lock.json and validated
// before this extractor is allowed to read them.
var enumSourceSpecs = []struct {
	Path  string
	Enums []string
}{
	{Path: "src/include/duckdb/common/enums/aggregate_handling.hpp", Enums: []string{"AggregateHandling"}},
	{Path: "src/include/duckdb/common/enums/cte_materialize.hpp", Enums: []string{"CTEMaterialize"}},
	{Path: "src/include/duckdb/common/enums/expression_type.hpp", Enums: []string{"ExpressionClass", "ExpressionType"}},
	{Path: "src/include/duckdb/common/enums/order_type.hpp", Enums: []string{"OrderByNullType", "OrderType"}},
	{Path: "src/include/duckdb/common/enums/set_operation_type.hpp", Enums: []string{"SetOperationType"}},
	{Path: "src/include/duckdb/common/types.hpp", Enums: []string{"LogicalTypeId"}},
	{Path: "src/include/duckdb/parser/expression/window_expression.hpp", Enums: []string{"WindowBoundary", "WindowExcludeMode"}},
}

//go:embed enum_schema_snapshot.json
var enumSchemaSnapshot []byte

var (
	cxxBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cxxLineComment  = regexp.MustCompile(`(?m)//[^\r\n]*`)
	cxxEnumStart    = regexp.MustCompile(`enum\s+class\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?::[^\{]+)?\{`)
	cxxIdentifier   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type enumSchema map[string][]string

func loadEnumSnapshot() (enumSchema, error) {
	var out enumSchema
	if err := json.Unmarshal(enumSchemaSnapshot, &out); err != nil {
		return nil, fmt.Errorf("decode enum schema snapshot: %w", err)
	}
	return normalizeEnumSchema(out), nil
}

func normalizeEnumSchema(in enumSchema) enumSchema {
	out := make(enumSchema, len(in))
	for name, values := range in {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out[name] = append(out[name], value)
		}
	}
	return out
}

func loadEnumSchema(source string) (enumSchema, error) {
	if strings.TrimSpace(source) == "" {
		return loadEnumSnapshot()
	}
	identity, err := duckdbsql.UpstreamSourceIdentity()
	if err != nil {
		return nil, err
	}
	if err := duckdbsql.ValidateSourceCheckout(source); err != nil {
		return nil, err
	}
	return extractEnumSchema(source, identity)
}

func extractEnumSchema(source string, identity duckdbsql.SourceIdentity) (enumSchema, error) {
	result := make(enumSchema)
	for _, spec := range enumSourceSpecs {
		path := filepath.Join(source, filepath.FromSlash(spec.Path))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read enum source %s: %w", spec.Path, err)
		}
		for _, name := range spec.Enums {
			values, err := parseCXXEnum(data, name)
			if err != nil {
				return nil, fmt.Errorf("parse %s in %s: %w", name, spec.Path, err)
			}
			result[name] = values
		}
	}
	// Keep the identity argument in this function's contract so callers cannot
	// accidentally extract from a source that was not validated against the
	// canonical lock. The validation itself is deliberately performed before
	// reading any source files above.
	_ = identity
	return normalizeEnumSchema(result), nil
}

func parseCXXEnum(data []byte, name string) ([]string, error) {
	text := cxxLineComment.ReplaceAllString(cxxBlockComment.ReplaceAllString(string(data), ""), "")
	locs := cxxEnumStart.FindAllStringSubmatchIndex(text, -1)
	for _, loc := range locs {
		if text[loc[2]:loc[3]] != name {
			continue
		}
		open := loc[1] - 1
		close := matchingBrace(text, open)
		if close < 0 {
			return nil, fmt.Errorf("enum body is not closed")
		}
		return parseCXXEnumMembers(text[open+1 : close]), nil
	}
	return nil, fmt.Errorf("enum declaration not found")
}

func matchingBrace(text string, open int) int {
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func parseCXXEnumMembers(body string) []string {
	values := make([]string, 0)
	seen := make(map[string]struct{})
	for _, member := range strings.Split(body, ",") {
		member = strings.TrimSpace(member)
		if member == "" {
			continue
		}
		if equal := strings.IndexByte(member, '='); equal >= 0 {
			member = member[:equal]
		}
		fields := strings.Fields(strings.TrimSpace(member))
		if len(fields) == 0 || !cxxIdentifier.MatchString(fields[0]) {
			continue
		}
		if _, ok := seen[fields[0]]; ok {
			continue
		}
		seen[fields[0]] = struct{}{}
		values = append(values, fields[0])
	}
	return values
}

func enumValues(schema enumSchema, name string) []string {
	return append([]string(nil), schema[name]...)
}

func sortedEnumNames(schema enumSchema) []string {
	names := make([]string, 0, len(schema))
	for name := range schema {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func renderEnumSchema(b *strings.Builder, schema enumSchema, provenance map[string]string) {
	b.WriteString("var generatedEnumManifest = []DescriptorProvenance{\n")
	for _, path := range sortedStringMapKeys(provenance) {
		fmt.Fprintf(b, "\t{Path: %q, SHA256: %q},\n", path, provenance[path])
	}
	b.WriteString("}\n\n")
	for _, name := range sortedEnumNames(schema) {
		fmt.Fprintf(b, "var generated%sValues = []string{", name)
		for i, value := range schema[name] {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(b, "%q", value)
		}
		b.WriteString("}\n")
	}
	b.WriteString("\nvar generatedEnumValues = map[string][]string{\n")
	for _, name := range sortedEnumNames(schema) {
		fmt.Fprintf(b, "\t%q: generated%sValues,\n", name, name)
	}
	b.WriteString("}\n\nfunc generatedEnumContains(enumName, value string) bool {\n")
	b.WriteString("\tfor _, candidate := range generatedEnumValues[enumName] {\n")
	b.WriteString("\t\tif candidate == value {\n\t\t\treturn true\n\t\t}\n\t}\n\treturn false\n}\n")
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
