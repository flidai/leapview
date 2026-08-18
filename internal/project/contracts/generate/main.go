// Command generate emits the reviewed connector compatibility profile from the
// same APIGen IR that produces the public data-resource DTOs and schema.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectors"
)

type document struct {
	Schemas map[string]schema `json:"schemas"`
}

type schema struct {
	Extensions map[string]json.RawMessage `json:"extensions"`
	Properties map[string]property        `json:"properties"`
}

type property struct {
	Schema schemaRef `json:"schema"`
}

type schemaRef struct {
	Ref  string   `json:"ref"`
	Type string   `json:"type"`
	Enum []string `json:"enum"`
}

type profile struct {
	Key                  string   `json:"key"`
	ActivationMode       string   `json:"activationMode"`
	LocationCapabilities []string `json:"locationCapabilities"`
	ApprovedExtensions   []string `json:"approvedExtensions"`
	SecretType           string   `json:"secretType"`
	SupportStatus        string   `json:"supportStatus"`
	AdapterKey           string   `json:"adapterKey"`
	SchemaName           string
}

func main() {
	const (
		input       = "api/gen/data-resources-ir.json"
		registryOut = "internal/project/contracts/registry.gen.go"
		schemaOut   = "internal/project/contracts/gen/data-resources.schema.json"
		goOut       = "internal/project/contracts/path_options.gen.go"
		tsOut       = "web/generated/data-resources/index.ts"
		docsOut     = "docs/reference/config/data-resource-connectors.md"
	)
	doc, err := loadDocument(input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate connector registry:", err)
		os.Exit(1)
	}
	profiles, err := buildProfiles(doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate connector registry:", err)
		os.Exit(1)
	}
	if err := writeRegistry(registryOut, profiles); err != nil {
		fmt.Fprintln(os.Stderr, "generate connector registry:", err)
		os.Exit(1)
	}
	pairs, err := derivePathFormatOptions(doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "derive path format options:", err)
		os.Exit(1)
	}
	if err := patchPathSourceSchema(schemaOut, pairs); err != nil {
		fmt.Fprintln(os.Stderr, "patch data-resource schema:", err)
		os.Exit(1)
	}
	if err := writePathOptionsGo(goOut, pairs); err != nil {
		fmt.Fprintln(os.Stderr, "generate path option validation:", err)
		os.Exit(1)
	}
	if err := patchPathOptionsTS(tsOut, pairs); err != nil {
		fmt.Fprintln(os.Stderr, "generate TypeScript path options:", err)
		os.Exit(1)
	}
	if err := writeConnectorReference(docsOut, profiles, pairs); err != nil {
		fmt.Fprintln(os.Stderr, "generate connector reference:", err)
		os.Exit(1)
	}
}

func loadDocument(input string) (document, error) {
	raw, err := os.ReadFile(input)
	if err != nil {
		return document{}, err
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return document{}, fmt.Errorf("decode APIGen IR: %w", err)
	}
	return doc, nil
}

func run(input, output string) error {
	doc, err := loadDocument(input)
	if err != nil {
		return err
	}
	profiles, err := buildProfiles(doc)
	if err != nil {
		return err
	}
	return writeRegistry(output, profiles)
}

func buildProfiles(doc document) ([]profile, error) {
	profiles := make([]profile, 0)
	for name, schema := range doc.Schemas {
		value, ok := schema.Extensions["x-leapview-connector"]
		if !ok {
			continue
		}
		var item profile
		if err := json.Unmarshal(value, &item); err != nil {
			return nil, fmt.Errorf("decode connector profile %s: %w", name, err)
		}
		if item.Key == "" || item.AdapterKey == "" || item.ActivationMode == "" || item.SecretType == "" || item.SupportStatus == "" {
			return nil, fmt.Errorf("connector profile %s is missing required metadata", name)
		}
		item.SchemaName = name
		if err := checkRuntimeProfile(item); err != nil {
			return nil, err
		}
		profiles = append(profiles, item)
	}
	if len(profiles) == 0 {
		return nil, errors.New("no connector profiles found in APIGen IR")
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Key < profiles[j].Key })
	seen := map[string]struct{}{}
	adapters := map[string]string{}
	for _, item := range profiles {
		if _, ok := seen[item.Key]; ok {
			return nil, fmt.Errorf("duplicate connector profile key %q", item.Key)
		}
		seen[item.Key] = struct{}{}
		if previous, ok := adapters[item.AdapterKey]; ok {
			return nil, fmt.Errorf("adapter key %q is declared by both %q and %q", item.AdapterKey, previous, item.Key)
		}
		adapters[item.AdapterKey] = item.Key
	}
	for _, key := range runtimeKeys() {
		if _, ok := seen[key]; !ok {
			return nil, fmt.Errorf("runtime connector %q has no TypeSpec declaration", key)
		}
		if owner := adapters[key]; owner != key {
			return nil, fmt.Errorf("runtime connector %q is not mapped to exactly one adapter key", key)
		}
	}
	return profiles, nil
}

func writeRegistry(output string, profiles []profile) error {
	content := emit(profiles)
	formatted, err := format.Source([]byte(content))
	if err != nil {
		return fmt.Errorf("format generated registry: %w", err)
	}
	return os.WriteFile(output, formatted, 0o644)
}

type pathFormatOption struct {
	Format string
	Model  string
}

func derivePathFormatOptions(doc document) ([]pathFormatOption, error) {
	pathSchema, ok := doc.Schemas["PathSourceLocation"]
	if !ok {
		return nil, errors.New("APIGen IR has no PathSourceLocation schema")
	}
	formatProperty, ok := pathSchema.Properties["format"]
	if !ok || len(formatProperty.Schema.Enum) == 0 {
		return nil, errors.New("PathSourceLocation.format has no enum")
	}
	defaults, ok := doc.Schemas["ReaderDefaults"]
	if !ok {
		return nil, errors.New("APIGen IR has no ReaderDefaults schema")
	}
	formats := make(map[string]struct{}, len(formatProperty.Schema.Enum))
	pairs := make([]pathFormatOption, 0, len(formatProperty.Schema.Enum))
	for _, format := range formatProperty.Schema.Enum {
		if _, duplicate := formats[format]; duplicate {
			return nil, fmt.Errorf("PathSourceLocation.format declares duplicate %q", format)
		}
		formats[format] = struct{}{}
		property, ok := defaults.Properties[format]
		if !ok || property.Schema.Ref == "" {
			return nil, fmt.Errorf("ReaderDefaults has no typed option model for path format %q", format)
		}
		pairs = append(pairs, pathFormatOption{Format: format, Model: property.Schema.Ref})
	}
	for name, property := range defaults.Properties {
		if _, ok := formats[name]; !ok {
			return nil, fmt.Errorf("ReaderDefaults option %q has no PathSourceLocation.format variant", name)
		}
		if property.Schema.Ref == "" {
			return nil, fmt.Errorf("ReaderDefaults option %q has no typed model reference", name)
		}
	}
	return pairs, nil
}

// patchPathSourceSchema composes the scalar ADR format with its typed sibling
// options. TypeSpec/APIGen can express each piece and the generated DTO, but
// cannot currently express this correlated sibling union without adding a
// discriminator inside options. Keep the authored surface (`format: csv` and
// `options: {header: true}`) and seal the correlation in the generated JSON
// Schema consumed by DecodeResource.
func patchPathSourceSchema(path string, pairs []pathFormatOption) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode generated schema: %w", err)
	}
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		return errors.New("generated schema has no $defs object")
	}
	if _, ok := defs["PathSourceLocation"]; !ok {
		return errors.New("generated schema has no PathSourceLocation definition")
	}
	branches := make([]any, 0, len(pairs))
	for _, pair := range pairs {
		branches = append(branches, pathSourceSchemaBranch(pair.Format, pair.Model))
	}
	defs["PathSourceLocation"] = map[string]any{"oneOf": branches}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode generated schema: %w", err)
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(path, encoded, 0o644)
}

func pathSourceSchemaBranch(format, options string) map[string]any {
	return map[string]any{
		"properties": map[string]any{
			"format":  map[string]any{"enum": []any{format}, "type": "string"},
			"options": map[string]any{"$ref": "#/$defs/" + options},
			"path":    map[string]any{"type": "string"},
			"type":    map[string]any{"enum": []any{"path"}, "type": "string"},
		},
		"required":              []any{"format", "path", "type"},
		"type":                  "object",
		"unevaluatedProperties": false,
	}
}

func writePathOptionsGo(path string, pairs []pathFormatOption) error {
	var b strings.Builder
	b.WriteString("// Code generated by internal/project/contracts/generate. DO NOT EDIT.\n")
	b.WriteString("package contracts\n\n")
	b.WriteString("import (\n\t\"bytes\"\n\t\"encoding/json\"\n\t\"fmt\"\n)\n\n")
	b.WriteString("func validatePathSourceOptions(format string, options *SourcePathOptions) error {\n")
	b.WriteString("\tif options == nil { return nil }\n")
	b.WriteString("\tdata, err := json.Marshal(options)\n\tif err != nil { return err }\n")
	b.WriteString("\tswitch format {\n")
	for _, pair := range pairs {
		b.WriteString("\tcase ")
		b.WriteString(strconv.Quote(pair.Format))
		b.WriteString(":\n\t\tvar typed ")
		b.WriteString(pair.Model)
		b.WriteString("\n\t\treturn decodePathSourceOptions(data, &typed)\n")
	}
	b.WriteString("\tdefault:\n\t\treturn fmt.Errorf(\"unsupported path source format %q\", format)\n\t}\n}\n\n")
	b.WriteString("func decodePathSourceOptions(data []byte, destination any) error {\n")
	b.WriteString("\tdecoder := json.NewDecoder(bytes.NewReader(data))\n\tdecoder.DisallowUnknownFields()\n\treturn decoder.Decode(destination)\n}\n\n")
	b.WriteString("func (value *SourceLocationPathVariant) UnmarshalJSON(data []byte) error {\n")
	b.WriteString("\tif value == nil { return fmt.Errorf(\"cannot unmarshal SourceLocationPathVariant into nil receiver\") }\n")
	b.WriteString("\ttype plain SourceLocationPathVariant\n\tvar decoded plain\n")
	b.WriteString("\tif err := decodePathSourceOptions(data, &decoded); err != nil { return err }\n")
	b.WriteString("\tif decoded.Type != \"path\" { return fmt.Errorf(\"SourceLocation path variant type must be path\") }\n")
	b.WriteString("\tif err := validatePathSourceOptions(decoded.Format, decoded.Options); err != nil { return err }\n")
	b.WriteString("\t*value = SourceLocationPathVariant(decoded)\n\treturn nil\n}\n\n")
	b.WriteString("func (value SourceLocationPathVariant) MarshalJSON() ([]byte, error) {\n")
	b.WriteString("\tif value.Type != \"path\" { return nil, fmt.Errorf(\"SourceLocation path variant type must be path\") }\n")
	b.WriteString("\tif err := validatePathSourceOptions(value.Format, value.Options); err != nil { return nil, err }\n")
	b.WriteString("\ttype plain SourceLocationPathVariant\n\treturn json.Marshal(plain(value))\n}\n")
	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return fmt.Errorf("format generated path option validation: %w", err)
	}
	return os.WriteFile(path, formatted, 0o644)
}

func patchPathOptionsTS(path string, pairs []pathFormatOption) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(content)
	start := strings.Index(text, "export interface PathSourceLocation {")
	if start < 0 {
		return errors.New("generated TypeScript has no PathSourceLocation interface")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		return errors.New("generated TypeScript PathSourceLocation interface is malformed")
	}
	end = start + end + len("\n}\n")
	var replacement strings.Builder
	replacement.WriteString("export type PathSourceLocation = ")
	for i, pair := range pairs {
		if i > 0 {
			replacement.WriteString(" | ")
		}
		replacement.WriteString(pathFormatTypeName(pair.Format))
		replacement.WriteString("PathSourceLocation")
	}
	replacement.WriteString("\n\n")
	for _, pair := range pairs {
		replacement.WriteString("export interface ")
		replacement.WriteString(pathFormatTypeName(pair.Format))
		replacement.WriteString("PathSourceLocation {\n  type: 'path'\n  path: string\n  format: '")
		replacement.WriteString(pair.Format)
		replacement.WriteString("'\n  options?: ")
		replacement.WriteString(pair.Model)
		replacement.WriteString("\n}\n\n")
	}
	text = text[:start] + replacement.String() + text[end:]
	start = strings.Index(text, "export interface SourceLocationPathVariant extends PathSourceLocation {")
	if start < 0 {
		return errors.New("generated TypeScript has no SourceLocationPathVariant interface")
	}
	end = strings.Index(text[start:], "\n}\n")
	if end < 0 {
		return errors.New("generated TypeScript SourceLocationPathVariant interface is malformed")
	}
	end = start + end + len("\n}\n")
	text = text[:start] + "export type SourceLocationPathVariant = PathSourceLocation\n\n" + text[end:]
	return os.WriteFile(path, []byte(text), 0o644)
}

func pathFormatTypeName(format string) string {
	if format == "csv" || format == "json" {
		return strings.ToUpper(format)
	}
	return strings.ToUpper(format[:1]) + format[1:]
}

func writeConnectorReference(path string, profiles []profile, pairs []pathFormatOption) error {
	var b strings.Builder
	b.WriteString("<!-- Code generated by internal/project/contracts/generate; DO NOT EDIT. -->\n\n")
	b.WriteString("# Data-resource connector capabilities\n\n")
	b.WriteString("This reference is generated from the reviewed TypeSpec connector profiles and runtime registry checks. It describes authored capabilities only; target endpoints and credentials remain target-owned.\n\n")
	b.WriteString("## Connectors\n\n")
	b.WriteString("| Key | Activation | Locations | Approved extensions | Secret type | Support | Adapter |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, item := range profiles {
		b.WriteString("| `")
		b.WriteString(item.Key)
		b.WriteString("` | `")
		b.WriteString(item.ActivationMode)
		b.WriteString("` | `")
		b.WriteString(strings.Join(item.LocationCapabilities, "`, `"))
		b.WriteString("` | `")
		if len(item.ApprovedExtensions) == 0 {
			b.WriteString("none")
		} else {
			b.WriteString(strings.Join(item.ApprovedExtensions, "`, `"))
		}
		b.WriteString("` | `")
		b.WriteString(item.SecretType)
		b.WriteString("` | `")
		b.WriteString(item.SupportStatus)
		b.WriteString("` | `")
		b.WriteString(item.AdapterKey)
		b.WriteString("` |\n")
	}
	b.WriteString("\n## Path format options\n\n")
	b.WriteString("Path Sources retain the scalar ADR shape (`format` plus sibling `options`). Each format is paired with exactly one generated reader option model; unknown or cross-format option fields are rejected.\n\n")
	b.WriteString("| Format | Option model |\n| --- | --- |\n")
	for _, pair := range pairs {
		b.WriteString("| `")
		b.WriteString(pair.Format)
		b.WriteString("` | `")
		b.WriteString(pair.Model)
		b.WriteString("` |\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func checkRuntimeProfile(item profile) error {
	runtime, ok := connectors.LookupConnection(item.Key)
	if !ok {
		return fmt.Errorf("TypeSpec connector %q is not present in runtime registry", item.Key)
	}
	if string(runtime.ActivationMode) != item.ActivationMode {
		return fmt.Errorf("connector %q activation mode %q differs from runtime %q", item.Key, item.ActivationMode, runtime.ActivationMode)
	}
	if !(runtime.SecretType == "" && item.SecretType == "none") && runtime.SecretType != item.SecretType {
		return fmt.Errorf("connector %q secret type %q differs from runtime %q", item.Key, item.SecretType, runtime.SecretType)
	}
	if !sameStrings(runtime.RequiredExtensions, item.ApprovedExtensions) {
		return fmt.Errorf("connector %q approved extensions %#v differ from runtime %#v", item.Key, item.ApprovedExtensions, runtime.RequiredExtensions)
	}
	capabilities := map[string]bool{}
	for _, capability := range item.LocationCapabilities {
		capabilities[capability] = true
	}
	if capabilities["path"] != runtime.AllowsPathSource || capabilities["relation"] != runtime.AllowsObjectSource {
		return fmt.Errorf("connector %q location capabilities %#v differ from runtime path=%v relation=%v", item.Key, item.LocationCapabilities, runtime.AllowsPathSource, runtime.AllowsObjectSource)
	}
	if item.AdapterKey != item.Key {
		return fmt.Errorf("connector %q adapter key %q does not match its registered runtime key", item.Key, item.AdapterKey)
	}
	return nil
}

func runtimeKeys() []string {
	return connectors.ConnectionKinds()
}

func sameStrings(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func emit(profiles []profile) string {
	var b strings.Builder
	b.WriteString("// Code generated by internal/project/contracts/generate. DO NOT EDIT.\n")
	b.WriteString("package contracts\n\n")
	b.WriteString("type ConnectorActivationMode string\n\n")
	b.WriteString("const (\n")
	b.WriteString("\tConnectorActivationManaged ConnectorActivationMode = \"managed\"\n")
	b.WriteString("\tConnectorActivationAuthored ConnectorActivationMode = \"authored\"\n")
	b.WriteString("\tConnectorActivationTargetBinding ConnectorActivationMode = \"target_binding\"\n")
	b.WriteString(")\n\n")
	b.WriteString("type ConnectorSupportStatus string\n\n")
	b.WriteString("const (\n")
	b.WriteString("\tConnectorSupportStable ConnectorSupportStatus = \"stable\"\n")
	b.WriteString("\tConnectorSupportExperimental ConnectorSupportStatus = \"experimental\"\n")
	b.WriteString(")\n\n")
	b.WriteString("type ConnectorProfile struct {\n")
	b.WriteString("\tKey string\n\tSchemaName string\n\tActivationMode ConnectorActivationMode\n\tLocationCapabilities []string\n\tApprovedExtensions []string\n\tSecretType string\n\tSupportStatus ConnectorSupportStatus\n\tAdapterKey string\n")
	b.WriteString("}\n\n")
	b.WriteString("var ConnectorRegistry = []ConnectorProfile{\n")
	for _, item := range profiles {
		b.WriteString("\t{Key: ")
		b.WriteString(strconv.Quote(item.Key))
		b.WriteString(", SchemaName: ")
		b.WriteString(strconv.Quote(item.SchemaName))
		b.WriteString(", ActivationMode: ConnectorActivationMode(")
		b.WriteString(strconv.Quote(item.ActivationMode))
		b.WriteString("), LocationCapabilities: []string{")
		writeStrings(&b, item.LocationCapabilities)
		b.WriteString("}, ApprovedExtensions: []string{")
		writeStrings(&b, item.ApprovedExtensions)
		b.WriteString("}, SecretType: ")
		b.WriteString(strconv.Quote(item.SecretType))
		b.WriteString(", SupportStatus: ConnectorSupportStatus(")
		b.WriteString(strconv.Quote(item.SupportStatus))
		b.WriteString("), AdapterKey: ")
		b.WriteString(strconv.Quote(item.AdapterKey))
		b.WriteString("},\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("func LookupConnector(key string) (ConnectorProfile, bool) {\n")
	b.WriteString("\tfor _, profile := range ConnectorRegistry {\n\t\tif profile.Key == key { return profile, true }\n\t}\n\treturn ConnectorProfile{}, false\n}\n")
	return b.String()
}

func writeStrings(b *strings.Builder, values []string) {
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(value))
	}
}
