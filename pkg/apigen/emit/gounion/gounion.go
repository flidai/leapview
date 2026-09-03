// Package gounion renders strict tagged-union wrappers for Go emitters.
package gounion

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// Emit writes one closed tagged-union type. typeName maps IR schema names to
// the concrete Go names used by the caller.
func Emit(b *strings.Builder, doc ir.Document, name string, schema ir.Schema, typeName func(string) string) {
	unionName := typeName(name)
	markerName := "is" + unionName + "Variant"
	interfaceName := unionName + "Variant"

	b.WriteString("type " + interfaceName + " interface {\n\t" + markerName + "()\n}\n\n")
	b.WriteString("type " + unionName + " struct {\n\tValue " + interfaceName + "\n}\n\n")

	values := orderedMappingValues(schema)
	for _, value := range values {
		variantName := typeName(schema.Discriminator.Mapping[value])
		b.WriteString("func (*" + variantName + ") " + markerName + "() {}\n")
	}
	b.WriteString("\n")

	b.WriteString("func (value " + unionName + ") MarshalJSON() ([]byte, error) {\n")
	b.WriteString("\tswitch variant := value.Value.(type) {\n")
	for _, value := range values {
		variantName := typeName(schema.Discriminator.Mapping[value])
		b.WriteString("\tcase *" + variantName + ":\n\t\tif variant == nil { return nil, fmt.Errorf(\"" + unionName + " variant is nil\") }; return json.Marshal(variant)\n")
	}
	b.WriteString("\tcase nil:\n\t\treturn nil, fmt.Errorf(\"" + unionName + " variant is required\")\n")
	b.WriteString("\tdefault:\n\t\treturn nil, fmt.Errorf(\"unsupported " + unionName + " variant %T\", variant)\n\t}\n}\n\n")

	b.WriteString("func (value *" + unionName + ") UnmarshalJSON(data []byte) error {\n")
	b.WriteString("\tif value == nil { return fmt.Errorf(\"cannot unmarshal " + unionName + " into nil receiver\") }\n")
	b.WriteString("\tvar fields map[string]json.RawMessage\n")
	b.WriteString("\tif err := json.Unmarshal(data, &fields); err != nil { return fmt.Errorf(\"decode " + unionName + " object: %w\", err) }\n")
	b.WriteString("\tvar tag struct { Value string \x60json:\"" + schema.Discriminator.PropertyName + "\"\x60 }\n")
	b.WriteString("\tif err := json.Unmarshal(data, &tag); err != nil { return fmt.Errorf(\"decode " + unionName + " discriminator: %w\", err) }\n")
	b.WriteString("\tif tag.Value == \"\" { return fmt.Errorf(\"" + unionName + " discriminator " + schema.Discriminator.PropertyName + " is required\") }\n")
	b.WriteString("\tdecode := func(dest any) error { decoder := json.NewDecoder(bytes.NewReader(data)); decoder.UseNumber(); decoder.DisallowUnknownFields(); return decoder.Decode(dest) }\n")
	b.WriteString("\tswitch tag.Value {\n")
	for _, discriminatorValue := range values {
		variantSchemaName := schema.Discriminator.Mapping[discriminatorValue]
		variantName := typeName(variantSchemaName)
		fmt.Fprintf(b, "\tcase %q:\n", discriminatorValue)
		for _, propertyName := range requiredProperties(doc, variantSchemaName) {
			fmt.Fprintf(b, "\t\tif _, ok := fields[%q]; !ok { return fmt.Errorf(\"decode %s variant %%q: required property %s is missing\", tag.Value) }\n", propertyName, unionName, propertyName)
		}
		fmt.Fprintf(b, "\t\tvar variant %s\n\t\tif err := decode(&variant); err != nil { return fmt.Errorf(\"decode %s variant %%q: %%w\", tag.Value, err) }; value.Value = &variant\n", variantName, unionName)
	}
	b.WriteString("\tdefault:\n\t\treturn fmt.Errorf(\"unknown " + unionName + " discriminator %q\", tag.Value)\n\t}\n\treturn nil\n}\n\n")

	emitVisitor(b, unionName, values, schema, typeName)
	emitDiscriminatorAccessor(b, unionName, values, schema, typeName)
	if baseName, ok := commonBase(doc, values, schema); ok {
		emitBaseAccessor(b, unionName, baseName, values, schema, typeName)
	}
}

// EmitScalarObject emits a strict untagged union containing exactly one JSON
// scalar branch and exactly one object-model branch. This shape is used by
// compact authored references: an unaliased member is a string while an
// aliased member is a closed object. The generated wrapper never falls back
// to interface{} and rejects every other JSON kind during unmarshal.
func EmitScalarObject(b *strings.Builder, doc ir.Document, name string, schema ir.Schema, typeName func(string) string) error {
	unionName := typeName(name)
	scalarIndex := -1
	objectIndex := -1
	for index, variant := range schema.OneOf {
		if isScalarUnionVariant(variant) {
			if scalarIndex >= 0 {
				return fmt.Errorf("untagged union %q must contain exactly one scalar branch", name)
			}
			scalarIndex = index
		} else if variant.Ref != "" {
			if objectIndex >= 0 {
				return fmt.Errorf("untagged union %q must contain exactly one object-model branch", name)
			}
			objectIndex = index
		} else {
			return fmt.Errorf("untagged union %q variant %d must be a scalar or named object-model ref", name, index)
		}
	}
	if scalarIndex < 0 || objectIndex < 0 {
		return fmt.Errorf("untagged union %q must contain exactly one scalar branch and exactly one object-model branch", name)
	}
	objectRef := schema.OneOf[objectIndex]
	objectName, ok := ir.NormalizedSchemaRefName(objectRef)
	if !ok {
		return fmt.Errorf("untagged union %q object branch must reference a named schema", name)
	}
	objectSchema, ok := doc.Schemas[objectName]
	if !ok || objectSchema.Type != "object" {
		return fmt.Errorf("untagged union %q object branch %q must reference an object-model schema", name, objectName)
	}
	objectGoName := typeName(objectName)
	objectFieldName := objectGoName
	if strings.HasPrefix(objectFieldName, unionName) {
		objectFieldName = strings.TrimPrefix(objectFieldName, unionName)
	}
	if strings.HasSuffix(objectFieldName, "Reference") {
		objectFieldName = "Reference"
	}
	if objectFieldName == "" {
		objectFieldName = objectGoName
	}

	b.WriteString("type " + unionName + " struct {\n")
	for _, variant := range schema.OneOf {
		if variant.Ref != "" {
			b.WriteString("\t" + objectFieldName + " *" + objectGoName + "\n")
			continue
		}
		field := scalarFieldName(variant.Type)
		b.WriteString("\t" + field + " *" + scalarGoType(variant) + "\n")
	}
	b.WriteString("}\n\n")

	b.WriteString("func (value " + unionName + ") MarshalJSON() ([]byte, error) {\n")
	b.WriteString("\tcount := 0\n")
	for _, variant := range schema.OneOf {
		if variant.Ref != "" {
			b.WriteString("\tif value." + objectFieldName + " != nil { count++ }\n")
			continue
		}
		field := scalarFieldName(variant.Type)
		b.WriteString("\tif value." + field + " != nil { count++ }\n")
	}
	b.WriteString("\tif count == 0 { return nil, fmt.Errorf(\"" + unionName + " variant is required\") }\n")
	b.WriteString("\tif count > 1 { return nil, fmt.Errorf(\"" + unionName + " has multiple variants\") }\n")
	for _, variant := range schema.OneOf {
		if variant.Ref != "" {
			b.WriteString("\tif value." + objectFieldName + " != nil { return json.Marshal(value." + objectFieldName + ") }\n")
			continue
		}
		field := scalarFieldName(variant.Type)
		b.WriteString("\tif value." + field + " != nil { return json.Marshal(value." + field + ") }\n")
	}
	b.WriteString("\treturn nil, fmt.Errorf(\"" + unionName + " variant is required\")\n}\n\n")

	b.WriteString("func (value *" + unionName + ") UnmarshalJSON(data []byte) error {\n")
	b.WriteString("\tif value == nil { return fmt.Errorf(\"cannot unmarshal " + unionName + " into nil receiver\") }\n")
	b.WriteString("\ttrimmed := bytes.TrimSpace(data)\n")
	b.WriteString("\tif len(trimmed) == 0 { return fmt.Errorf(\"decode " + unionName + ": empty JSON value\") }\n")
	b.WriteString("\t*value = " + unionName + "{}\n")
	b.WriteString("\tswitch trimmed[0] {\n")
	for _, variant := range schema.OneOf {
		if variant.Ref != "" {
			continue
		}
		field := scalarFieldName(variant.Type)
		if leading := scalarLeadingBytes(variant.Type); leading != "" {
			b.WriteString("\tcase " + leading + ":\n")
		} else {
			// Unknown scalar kinds cannot be decoded safely. Keep the generated
			// contract strict by making them unreachable at runtime.
			continue
		}
		b.WriteString("\t\tvar parsed " + scalarGoType(variant) + "\n")
		b.WriteString("\t\tif err := json.Unmarshal(trimmed, &parsed); err != nil { return fmt.Errorf(\"decode " + unionName + ": %w\", err) }\n")
		b.WriteString("\t\tvalue." + field + " = &parsed\n\t\treturn nil\n")
	}
	b.WriteString("\tcase '{':\n")
	b.WriteString("\t\tvar fields map[string]json.RawMessage\n")
	b.WriteString("\t\tif err := json.Unmarshal(trimmed, &fields); err != nil { return fmt.Errorf(\"decode " + unionName + " object: %w\", err) }\n")
	for _, propertyName := range requiredProperties(doc, objectName) {
		fmt.Fprintf(b, "\t\tif _, ok := fields[%q]; !ok { return fmt.Errorf(\"decode %s object: required property %s is missing\") }\n", propertyName, unionName, propertyName)
	}
	b.WriteString("\t\tvar parsed " + objectGoName + "\n")
	b.WriteString("\t\tdecoder := json.NewDecoder(bytes.NewReader(trimmed)); decoder.DisallowUnknownFields()\n")
	b.WriteString("\t\tif err := decoder.Decode(&parsed); err != nil { return fmt.Errorf(\"decode " + unionName + " object: %w\", err) }\n")
	b.WriteString("\t\tvalue." + objectFieldName + " = &parsed\n\t\treturn nil\n")
	b.WriteString("\tdefault:\n\t\treturn fmt.Errorf(\"decode " + unionName + ": expected a string or object\")\n\t}\n}\n\n")
	return nil
}

// EmitObject emits a strict untagged union containing object-model branches.
// Each branch is decoded with DisallowUnknownFields; exactly one branch must
// accept the authored object. This permits a clean YAML object whose required
// field sets express a conditional contract without leaking a discriminator
// tag into the public document.
func EmitObject(b *strings.Builder, doc ir.Document, name string, schema ir.Schema, typeName func(string) string) error {
	unionName := typeName(name)
	if len(schema.OneOf) < 2 {
		return fmt.Errorf("untagged object union %q requires at least two variants", name)
	}
	variants := make([]string, 0, len(schema.OneOf))
	for _, ref := range schema.OneOf {
		if ref.Ref == "" {
			return fmt.Errorf("untagged object union %q variant must reference a named object-model schema", name)
		}
		variantName, ok := ir.NormalizedSchemaRefName(ref)
		if !ok {
			return fmt.Errorf("untagged object union %q has invalid variant reference", name)
		}
		variant, ok := doc.Schemas[variantName]
		if !ok || variant.Type != "object" {
			return fmt.Errorf("untagged object union %q variant %q must reference an object-model schema", name, variantName)
		}
		variants = append(variants, variantName)
	}

	b.WriteString("type " + unionName + "Variant interface {\n\tis" + unionName + "Variant()\n}\n\n")
	b.WriteString("type " + unionName + " struct {\n\tValue " + unionName + "Variant\n}\n\n")
	for _, variant := range variants {
		b.WriteString("func (*" + typeName(variant) + ") is" + unionName + "Variant() {}\n")
	}
	b.WriteString("\nfunc (value " + unionName + ") MarshalJSON() ([]byte, error) {\n")
	b.WriteString("\tswitch variant := value.Value.(type) {\n")
	for _, variant := range variants {
		b.WriteString("\tcase *" + typeName(variant) + ":\n\t\tif variant == nil { return nil, fmt.Errorf(\"" + unionName + " variant is nil\") }; return json.Marshal(variant)\n")
	}
	b.WriteString("\tcase nil:\n\t\treturn nil, fmt.Errorf(\"" + unionName + " variant is required\")\n\tdefault:\n\t\treturn nil, fmt.Errorf(\"unsupported " + unionName + " variant %T\", variant)\n\t}\n}\n\n")

	b.WriteString("func (value *" + unionName + ") UnmarshalJSON(data []byte) error {\n")
	b.WriteString("\tif value == nil { return fmt.Errorf(\"cannot unmarshal " + unionName + " into nil receiver\") }\n")
	b.WriteString("\tvar fields map[string]json.RawMessage\n")
	b.WriteString("\tif err := json.Unmarshal(data, &fields); err != nil { return fmt.Errorf(\"decode " + unionName + " object: %w\", err) }\n")
	b.WriteString("\t*value = " + unionName + "{}\n")
	b.WriteString("\tvar matched string\n")
	b.WriteString("\tvar decoded any\n")
	b.WriteString("\tvar failures []string\n")
	b.WriteString("\tdecode := func(dest any) error { decoder := json.NewDecoder(bytes.NewReader(data)); decoder.UseNumber(); decoder.DisallowUnknownFields(); return decoder.Decode(dest) }\n")
	for _, variant := range variants {
		fmt.Fprintf(b, "\t{ valid := true\n")
		for _, required := range requiredProperties(doc, variant) {
			fmt.Fprintf(b, "\t\tif _, ok := fields[%q]; !ok { valid = false; failures = append(failures, %q) }\n", required, variant+": required property "+required+" is missing")
		}
		for _, literal := range requiredStringLiterals(doc, variant) {
			failure := fmt.Sprintf("%s: required property %s must equal %q", variant, literal.Property, literal.Value)
			fmt.Fprintf(b, "\t\tif valid { var actual any; if err := json.Unmarshal(fields[%q], &actual); err != nil { valid = false; failures = append(failures, %q) } else if value, ok := actual.(string); !ok || value != %q { valid = false; failures = append(failures, %q) } }\n", literal.Property, failure, literal.Value, failure)
		}
		fmt.Fprintf(b, "\t\tif valid { var candidate %s; if err := decode(&candidate); err == nil {\n", typeName(variant))
		fmt.Fprintf(b, "\t\t\tif matched != \"\" { return fmt.Errorf(\"decode %s: object matches both %%s and %s\", matched) }\n", unionName, variant)
		fmt.Fprintf(b, "\t\t\tmatched = %q; decoded = &candidate\n", variant)
		fmt.Fprintf(b, "\t\t} else { failures = append(failures, %q + err.Error()) } } }\n", variant+": ")
	}
	b.WriteString("\tif matched == \"\" { return fmt.Errorf(\"decode " + unionName + ": no object variant matched (fields=%v, errors=%s)\", fields, strings.Join(failures, \"; \")) }\n")
	b.WriteString("\tswitch matched {\n")
	for _, variant := range variants {
		b.WriteString("\tcase \"" + variant + "\": value.Value = decoded.(*" + typeName(variant) + ")\n")
	}
	b.WriteString("\t}\n\treturn nil\n}\n\n")
	return nil
}

type requiredStringLiteral struct {
	Property string
	Value    string
}

func requiredStringLiterals(doc ir.Document, schemaName string) []requiredStringLiteral {
	schema, ok := doc.Schemas[schemaName]
	if !ok {
		return nil
	}
	schema = ir.FlattenObjectSchema(doc, schema)
	required := make(map[string]struct{}, len(schema.Required))
	for _, property := range schema.Required {
		required[property] = struct{}{}
	}
	literals := make([]requiredStringLiteral, 0)
	for property, definition := range schema.Properties {
		if _, ok := required[property]; !ok || !strings.EqualFold(strings.TrimSpace(definition.Schema.Type), "string") || len(definition.Schema.Enum) != 1 {
			continue
		}
		literals = append(literals, requiredStringLiteral{Property: property, Value: definition.Schema.Enum[0]})
	}
	sort.Slice(literals, func(i, j int) bool { return literals[i].Property < literals[j].Property })
	return literals
}

func isScalarUnionVariant(variant ir.SchemaRef) bool {
	if variant.Ref != "" || variant.Items != nil || variant.AdditionalProperties != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(variant.Type)) {
	case "boolean", "integer", "number", "string", "null":
		return true
	default:
		return false
	}
}

func scalarLeadingBytes(kind string) string {
	switch strings.ToLower(kind) {
	case "string":
		return "'\"'"
	case "boolean":
		return "'t', 'f'"
	case "integer", "number":
		return "'-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9'"
	case "null":
		return "'n'"
	default:
		return ""
	}
}

func scalarFieldName(kind string) string {
	if strings.EqualFold(kind, "integer") {
		return "Integer"
	}
	if strings.EqualFold(kind, "number") {
		return "Number"
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

func scalarGoType(ref ir.SchemaRef) string {
	switch strings.ToLower(ref.Type) {
	case "integer":
		if strings.EqualFold(ref.Format, "int64") {
			return "int64"
		}
		return "int32"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "string":
		return "string"
	default:
		return "any"
	}
}

func emitVisitor(b *strings.Builder, unionName string, values []string, schema ir.Schema, typeName func(string) string) {
	visitorName := unionName + "Visitor"
	b.WriteString("type " + visitorName + " interface {\n")
	for _, value := range values {
		variantName := typeName(schema.Discriminator.Mapping[value])
		b.WriteString("\tVisit" + variantName + "(*" + variantName + ") error\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("func (value *" + unionName + ") Visit(visitor " + visitorName + ") error {\n")
	b.WriteString("\tif value == nil { return fmt.Errorf(\"cannot visit nil " + unionName + "\") }\n")
	b.WriteString("\tif visitor == nil { return fmt.Errorf(\"" + unionName + " visitor is required\") }\n")
	b.WriteString("\tswitch variant := value.Value.(type) {\n")
	for _, value := range values {
		variantName := typeName(schema.Discriminator.Mapping[value])
		b.WriteString("\tcase *" + variantName + ":\n\t\tif variant == nil { return fmt.Errorf(\"" + unionName + " variant is nil\") }; return visitor.Visit" + variantName + "(variant)\n")
	}
	b.WriteString("\tcase nil:\n\t\treturn fmt.Errorf(\"" + unionName + " variant is required\")\n")
	b.WriteString("\tdefault:\n\t\treturn fmt.Errorf(\"unsupported " + unionName + " variant %T\", variant)\n\t}\n}\n\n")
}

func emitDiscriminatorAccessor(b *strings.Builder, unionName string, values []string, schema ir.Schema, typeName func(string) string) {
	methodName := typeName(schema.Discriminator.PropertyName)
	b.WriteString("func (value *" + unionName + ") " + methodName + "() (string, error) {\n")
	b.WriteString("\tif value == nil { return \"\", fmt.Errorf(\"cannot inspect nil " + unionName + "\") }\n")
	b.WriteString("\tswitch variant := value.Value.(type) {\n")
	for _, value := range values {
		variantName := typeName(schema.Discriminator.Mapping[value])
		fmt.Fprintf(b, "\tcase *%s:\n\t\tif variant == nil { return \"\", fmt.Errorf(\"%s variant is nil\") }; return %q, nil\n", variantName, unionName, value)
	}
	b.WriteString("\tcase nil:\n\t\treturn \"\", fmt.Errorf(\"" + unionName + " variant is required\")\n")
	b.WriteString("\tdefault:\n\t\treturn \"\", fmt.Errorf(\"unsupported " + unionName + " variant %T\", variant)\n\t}\n}\n\n")
}

func emitBaseAccessor(b *strings.Builder, unionName, baseSchemaName string, values []string, schema ir.Schema, typeName func(string) string) {
	baseName := typeName(baseSchemaName)
	b.WriteString("func (value *" + unionName + ") Base() (*" + baseName + ", error) {\n")
	b.WriteString("\tif value == nil { return nil, fmt.Errorf(\"cannot inspect nil " + unionName + "\") }\n")
	b.WriteString("\tswitch variant := value.Value.(type) {\n")
	for _, value := range values {
		variantName := typeName(schema.Discriminator.Mapping[value])
		b.WriteString("\tcase *" + variantName + ":\n\t\tif variant == nil { return nil, fmt.Errorf(\"" + unionName + " variant is nil\") }; return &variant." + baseName + ", nil\n")
	}
	b.WriteString("\tcase nil:\n\t\treturn nil, fmt.Errorf(\"" + unionName + " variant is required\")\n")
	b.WriteString("\tdefault:\n\t\treturn nil, fmt.Errorf(\"unsupported " + unionName + " variant %T\", variant)\n\t}\n}\n\n")
}

func commonBase(doc ir.Document, values []string, schema ir.Schema) (string, bool) {
	baseName := ""
	for _, value := range values {
		variant, ok := doc.Schemas[schema.Discriminator.Mapping[value]]
		if !ok || variant.Base == nil {
			return "", false
		}
		name, ok := ir.NormalizedSchemaRefName(*variant.Base)
		if !ok {
			return "", false
		}
		if baseName == "" {
			baseName = name
			continue
		}
		if baseName != name {
			return "", false
		}
	}
	return baseName, baseName != ""
}

func requiredProperties(doc ir.Document, schemaName string) []string {
	seen := make(map[string]struct{})
	for schemaName != "" {
		schema, ok := doc.Schemas[schemaName]
		if !ok {
			break
		}
		for _, propertyName := range schema.Required {
			seen[propertyName] = struct{}{}
		}
		if schema.Base == nil {
			break
		}
		baseName, ok := ir.NormalizedSchemaRefName(*schema.Base)
		if !ok {
			break
		}
		schemaName = baseName
	}
	properties := make([]string, 0, len(seen))
	for propertyName := range seen {
		properties = append(properties, propertyName)
	}
	sort.Strings(properties)
	return properties
}

func orderedMappingValues(schema ir.Schema) []string {
	values := make([]string, 0, len(schema.Discriminator.Mapping))
	for value := range schema.Discriminator.Mapping {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
