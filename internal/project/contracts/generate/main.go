// Command generate emits the reviewed connector compatibility profile from the
// same APIGen IR that produces the public data-resource DTOs and schema.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"
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
	if err := run("api/gen/data-resources-ir.json", "internal/project/contracts/registry.gen.go"); err != nil {
		fmt.Fprintln(os.Stderr, "generate connector registry:", err)
		os.Exit(1)
	}
	if err := patchPathSourceSchema("internal/project/contracts/gen/data-resources.schema.json"); err != nil {
		fmt.Fprintln(os.Stderr, "patch data-resource schema:", err)
		os.Exit(1)
	}
}

// patchPathSourceSchema composes the scalar ADR format with its typed sibling
// options. TypeSpec/APIGen can express each piece and the generated DTO, but
// cannot currently express this correlated sibling union without adding a
// discriminator inside options. Keep the authored surface (`format: csv` and
// `options: {header: true}`) and seal the correlation in the generated JSON
// Schema consumed by DecodeResource.
func patchPathSourceSchema(path string) error {
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
	branches := []any{
		pathSourceSchemaBranch("csv", "CSVReaderOptions"),
		pathSourceSchemaBranch("json", "JSONReaderOptions"),
		pathSourceSchemaBranch("parquet", "ParquetReaderOptions"),
		pathSourceSchemaBranch("excel", "ExcelReaderOptions"),
		pathSourceSchemaBranch("text", "TextReaderOptions"),
		pathSourceSchemaBranch("blob", "BlobReaderOptions"),
		pathSourceSchemaBranch("vortex", "VortexReaderOptions"),
		pathSourceSchemaBranch("delta", "DeltaReaderOptions"),
		pathSourceSchemaBranch("iceberg", "IcebergReaderOptions"),
		pathSourceSchemaBranch("lance", "LanceReaderOptions"),
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

func run(input, output string) error {
	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode APIGen IR: %w", err)
	}
	profiles := make([]profile, 0)
	for name, schema := range doc.Schemas {
		value, ok := schema.Extensions["x-leapview-connector"]
		if !ok {
			continue
		}
		var item profile
		if err := json.Unmarshal(value, &item); err != nil {
			return fmt.Errorf("decode connector profile %s: %w", name, err)
		}
		if item.Key == "" || item.AdapterKey == "" || item.ActivationMode == "" || item.SecretType == "" || item.SupportStatus == "" {
			return fmt.Errorf("connector profile %s is missing required metadata", name)
		}
		item.SchemaName = name
		if err := checkRuntimeProfile(item); err != nil {
			return err
		}
		profiles = append(profiles, item)
	}
	if len(profiles) == 0 {
		return errors.New("no connector profiles found in APIGen IR")
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Key < profiles[j].Key })
	seen := map[string]struct{}{}
	adapters := map[string]string{}
	for _, item := range profiles {
		if _, ok := seen[item.Key]; ok {
			return fmt.Errorf("duplicate connector profile key %q", item.Key)
		}
		seen[item.Key] = struct{}{}
		if previous, ok := adapters[item.AdapterKey]; ok {
			return fmt.Errorf("adapter key %q is declared by both %q and %q", item.AdapterKey, previous, item.Key)
		}
		adapters[item.AdapterKey] = item.Key
	}
	for _, key := range runtimeKeys() {
		if _, ok := seen[key]; !ok {
			return fmt.Errorf("runtime connector %q has no TypeSpec declaration", key)
		}
		if owner := adapters[key]; owner != key {
			return fmt.Errorf("runtime connector %q is not mapped to exactly one adapter key", key)
		}
	}
	content := emit(profiles)
	formatted, err := format.Source([]byte(content))
	if err != nil {
		return fmt.Errorf("format generated registry: %w", err)
	}
	if err := os.WriteFile(output, formatted, 0o644); err != nil {
		return err
	}
	return nil
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
