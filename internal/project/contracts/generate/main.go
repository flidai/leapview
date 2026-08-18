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
	Extensions    map[string]json.RawMessage `json:"extensions"`
	Properties    map[string]property        `json:"properties"`
	OneOf         []schemaRef                `json:"one_of"`
	Discriminator *discriminator             `json:"discriminator"`
}

type discriminator struct {
	PropertyName string            `json:"property_name"`
	Mapping      map[string]string `json:"mapping"`
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
	AllowPublicAccess    bool
}

func main() {
	const (
		input       = "api/gen/data-resources-ir.json"
		registryOut = "internal/project/contracts/registry.gen.go"
		goOut       = "internal/project/contracts/path_options.gen.go"
		docsOut     = "docs/articles/reference/data-resource-connectors.md"
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
	if err := writePathOptionsGo(goOut, pairs); err != nil {
		fmt.Fprintln(os.Stderr, "generate path option validation:", err)
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
		// Public access is a structural capability: only connector variants
		// exposing the generated `access` property may declare it.
		_, item.AllowPublicAccess = schema.Properties["access"]
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
	Format            string
	Model             string
	Extensions        []string
	ScanKind          string
	ScanFunction      string
	RequiredExtension string
	SourceSecretType  string
	TableLike         bool
	AllowsOptions     bool
	Defaults          map[string]any
}

func derivePathFormatOptions(doc document) ([]pathFormatOption, error) {
	pathSchema, ok := doc.Schemas["PathSourceLocation"]
	if !ok {
		return nil, errors.New("APIGen IR has no PathSourceLocation schema")
	}
	if pathSchema.Discriminator == nil || pathSchema.Discriminator.PropertyName != "format" {
		return nil, errors.New("PathSourceLocation is not discriminated by format")
	}
	if len(pathSchema.OneOf) == 0 {
		return nil, errors.New("PathSourceLocation has no format variants")
	}
	defaults, ok := doc.Schemas["ReaderDefaults"]
	if !ok {
		return nil, errors.New("APIGen IR has no ReaderDefaults schema")
	}
	formats := make(map[string]struct{}, len(pathSchema.OneOf))
	pairs := make([]pathFormatOption, 0, len(pathSchema.OneOf))
	for _, variant := range pathSchema.OneOf {
		if variant.Ref == "" {
			return nil, errors.New("PathSourceLocation format variant has no model reference")
		}
		variantSchema, ok := doc.Schemas[variant.Ref]
		if !ok {
			return nil, fmt.Errorf("PathSourceLocation format variant %q is missing from APIGen IR", variant.Ref)
		}
		formatProperty, ok := variantSchema.Properties["format"]
		if !ok || len(formatProperty.Schema.Enum) != 1 {
			return nil, fmt.Errorf("PathSourceLocation format variant %q has no single literal format", variant.Ref)
		}
		format := formatProperty.Schema.Enum[0]
		if target, ok := pathSchema.Discriminator.Mapping[format]; !ok || target != variant.Ref {
			return nil, fmt.Errorf("PathSourceLocation format %q discriminator mapping does not target %q", format, variant.Ref)
		}
		if _, duplicate := formats[format]; duplicate {
			return nil, fmt.Errorf("PathSourceLocation.format declares duplicate %q", format)
		}
		formats[format] = struct{}{}
		property, ok := defaults.Properties[format]
		if !ok || property.Schema.Ref == "" {
			return nil, fmt.Errorf("ReaderDefaults has no typed option model for path format %q", format)
		}
		profileSchema, ok := doc.Schemas[property.Schema.Ref]
		if !ok {
			return nil, fmt.Errorf("format %q option model %q is missing from APIGen IR", format, property.Schema.Ref)
		}
		rawProfile, ok := profileSchema.Extensions["x-leapview-format"]
		if !ok {
			return nil, fmt.Errorf("format %q option model %q is missing x-leapview-format metadata", format, property.Schema.Ref)
		}
		var profile struct {
			Name              string         `json:"name"`
			Extensions        []string       `json:"extensions"`
			ScanKind          string         `json:"scanKind"`
			ScanFunction      string         `json:"scanFunction"`
			RequiredExtension string         `json:"requiredExtension"`
			SourceSecretType  string         `json:"sourceSecretType"`
			TableLike         bool           `json:"tableLike"`
			AllowsOptions     *bool          `json:"allowsOptions"`
			Defaults          map[string]any `json:"defaults"`
		}
		if err := json.Unmarshal(rawProfile, &profile); err != nil {
			return nil, fmt.Errorf("decode format %q metadata: %w", format, err)
		}
		if profile.Name != format || profile.ScanKind == "" || profile.Defaults == nil {
			return nil, fmt.Errorf("format %q metadata is incomplete", format)
		}
		allowsOptions := true
		if profile.AllowsOptions != nil {
			allowsOptions = *profile.AllowsOptions
		}
		pairs = append(pairs, pathFormatOption{Format: format, Model: property.Schema.Ref, Extensions: profile.Extensions, ScanKind: profile.ScanKind, ScanFunction: profile.ScanFunction, RequiredExtension: profile.RequiredExtension, SourceSecretType: profile.SourceSecretType, TableLike: profile.TableLike, AllowsOptions: allowsOptions, Defaults: profile.Defaults})
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

func writePathOptionsGo(path string, pairs []pathFormatOption) error {
	var b strings.Builder
	b.WriteString("// Code generated by internal/project/contracts/generate. DO NOT EDIT.\n")
	b.WriteString("package contracts\n\n")
	for _, pair := range pairs {
		b.WriteString("// Default")
		b.WriteString(pair.Model)
		b.WriteString(" returns the generated, versioned defaults for ")
		b.WriteString(pair.Format)
		b.WriteString(" path sources.\n")
		b.WriteString("func Default")
		b.WriteString(pair.Model)
		b.WriteString("() *")
		b.WriteString(pair.Model)
		b.WriteString(" {\n")
		if len(pair.Defaults) == 0 {
			b.WriteString("\treturn nil\n}\n\n")
			continue
		}
		keys := make([]string, 0, len(pair.Defaults))
		for key := range pair.Defaults {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			name := goFieldName(key)
			value := pair.Defaults[key]
			b.WriteString("\t")
			b.WriteString(strings.ToLower(name[:1]))
			b.WriteString(name[1:])
			b.WriteString(" := ")
			if err := writeGoLiteral(&b, value); err != nil {
				return err
			}
			b.WriteString("\n")
		}
		b.WriteString("\treturn &")
		b.WriteString(pair.Model)
		b.WriteString("{\n")
		for _, key := range keys {
			name := goFieldName(key)
			b.WriteString("\t\t")
			b.WriteString(name)
			b.WriteString(": &")
			b.WriteString(strings.ToLower(name[:1]))
			b.WriteString(name[1:])
			b.WriteString(",\n")
		}
		b.WriteString("\t}\n}\n\n")
	}
	b.WriteString("// PathFormatNames is generated from PathSourceLocation.format.\n")
	b.WriteString("var PathFormatNames = []string{")
	for i, pair := range pairs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(pair.Format))
	}
	b.WriteString("}\n\n")
	b.WriteString("// FormatProfile is generated from x-leapview-format metadata.\n")
	b.WriteString("type FormatProfile struct { Name string; Extensions []string; ScanKind string; ScanFunction string; RequiredExtension string; SourceSecretType string; TableLike bool; AllowsOptions bool }\n\n")
	b.WriteString("var FormatRegistry = []FormatProfile{\n")
	for _, pair := range pairs {
		b.WriteString("{Name: ")
		b.WriteString(strconv.Quote(pair.Format))
		b.WriteString(", Extensions: []string{")
		for i, extension := range pair.Extensions {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(extension))
		}
		b.WriteString("}, ScanKind: ")
		b.WriteString(strconv.Quote(pair.ScanKind))
		b.WriteString(", ScanFunction: ")
		b.WriteString(strconv.Quote(pair.ScanFunction))
		b.WriteString(", RequiredExtension: ")
		b.WriteString(strconv.Quote(pair.RequiredExtension))
		b.WriteString(", SourceSecretType: ")
		b.WriteString(strconv.Quote(pair.SourceSecretType))
		b.WriteString(", TableLike: ")
		b.WriteString(strconv.FormatBool(pair.TableLike))
		b.WriteString(", AllowsOptions: ")
		b.WriteString(strconv.FormatBool(pair.AllowsOptions))
		b.WriteString("},\n")
	}
	b.WriteString("}\n\n")
	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return fmt.Errorf("format generated path option registry: %w", err)
	}
	return os.WriteFile(path, formatted, 0o644)
}

func goFieldName(key string) string {
	if key == "" {
		return ""
	}
	return strings.ToUpper(key[:1]) + key[1:]
}

func writeGoLiteral(builder *strings.Builder, value any) error {
	switch value := value.(type) {
	case string:
		builder.WriteString(strconv.Quote(value))
	case bool:
		builder.WriteString(strconv.FormatBool(value))
	case float64:
		if value != float64(int32(value)) {
			return fmt.Errorf("unsupported non-integral generated default %v", value)
		}
		builder.WriteString(strconv.FormatInt(int64(value), 10))
	default:
		return fmt.Errorf("unsupported generated default type %T", value)
	}
	return nil
}

func writeConnectorReference(path string, profiles []profile, pairs []pathFormatOption) error {
	var b strings.Builder
	b.WriteString("<!-- Code generated by internal/project/contracts/generate; DO NOT EDIT. -->\n\n")
	b.WriteString("# Data-resource connector capabilities\n\n")
	b.WriteString("This reference is generated from the reviewed TypeSpec connector profiles and runtime registry checks. It describes authored capabilities only; target endpoints and credentials remain target-owned.\n\n")
	b.WriteString("## Connectors\n\n")
	b.WriteString("| Key | Activation | Locations | Public access | Approved extensions | Secret type | Support | Adapter |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, item := range profiles {
		b.WriteString("| `")
		b.WriteString(item.Key)
		b.WriteString("` | `")
		b.WriteString(item.ActivationMode)
		b.WriteString("` | `")
		b.WriteString(strings.Join(item.LocationCapabilities, "`, `"))
		b.WriteString("` | `")
		b.WriteString(strconv.FormatBool(item.AllowPublicAccess))
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
	if item.AllowPublicAccess != runtime.AllowPublicAccess {
		return fmt.Errorf("connector %q public access capability %v differs from runtime %v", item.Key, item.AllowPublicAccess, runtime.AllowPublicAccess)
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
	b.WriteString("\tKey string\n\tSchemaName string\n\tActivationMode ConnectorActivationMode\n\tLocationCapabilities []string\n\tApprovedExtensions []string\n\tSecretType string\n\tSupportStatus ConnectorSupportStatus\n\tAdapterKey string\n\tAllowPublicAccess bool\n")
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
		b.WriteString(", AllowPublicAccess: ")
		b.WriteString(strconv.FormatBool(item.AllowPublicAccess))
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
