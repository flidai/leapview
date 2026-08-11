package ir

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// NormalizedSchemaRefName resolves a schema ref to a registry key.
func NormalizedSchemaRefName(schema SchemaRef) (string, bool) {
	if schema.Ref == "" {
		return "", false
	}
	ref := strings.TrimSpace(schema.Ref)
	ref = strings.TrimPrefix(ref, "#/components/schemas/")
	ref = strings.TrimPrefix(ref, "#/schemas/")
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		ref = ref[idx+1:]
	}
	if ref == "" {
		return "", false
	}
	return ref, true
}

// ResolveSchema returns the concrete schema referenced by the schema ref.
func ResolveSchema(doc Document, schemaRef SchemaRef) (Schema, bool) {
	name, ok := NormalizedSchemaRefName(schemaRef)
	if !ok {
		return Schema{}, false
	}
	schema, ok := doc.Schemas[name]
	return schema, ok
}

// ResolveRequestBodySchemaName returns the concrete request body schema name when present.
func ResolveRequestBodySchemaName(doc Document, endpoint Endpoint) (string, bool) {
	content, ok := PrimaryRequestBodyContent(endpoint)
	if !ok || content.Schema == nil {
		return "", false
	}
	ref := *content.Schema
	name, ok := NormalizedSchemaRefName(ref)
	if !ok {
		return "", false
	}
	if _, ok := doc.Schemas[name]; !ok {
		return "", false
	}
	return name, true
}

// ResolveRequestBodySchema returns the concrete request body schema when present.
func ResolveRequestBodySchema(doc Document, endpoint Endpoint) (Schema, bool) {
	name, ok := ResolveRequestBodySchemaName(doc, endpoint)
	if !ok {
		return Schema{}, false
	}
	schema, ok := doc.Schemas[name]
	return schema, ok
}

// SuccessResponse returns the preferred success response for CLI generation.
func SuccessResponse(endpoint Endpoint) (*Response, bool) {
	var best *Response
	for i := range endpoint.Responses {
		response := &endpoint.Responses[i]
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			continue
		}
		if best == nil || response.StatusCode < best.StatusCode {
			best = response
		}
	}
	return best, best != nil
}

// ResolveResponseBodySchema returns the schema used for the CLI-visible success body.
func ResolveResponseBodySchema(doc Document, response Response) (Schema, bool) {
	ref, ok := ResolveResponseBodySchemaRef(response)
	if !ok {
		return Schema{}, false
	}
	return resolveConcreteSchema(doc, ref)
}

// ResolveResponseBodySchemaRef returns the schema reference used for the
// CLI-visible JSON success body.
func ResolveResponseBodySchemaRef(response Response) (SchemaRef, bool) {
	if shape, ok, _ := ResponseShapeMetadata(response); ok && shape.Kind == "wrapped_json" && shape.BodyType != "" {
		return SchemaRef{Ref: shape.BodyType}, true
	}
	content, ok := PrimaryResponseContent(response)
	if !ok || content.BodyKind != "json" || content.Schema == nil {
		return SchemaRef{}, false
	}
	return *content.Schema, true
}

// ToolSuccessSchema selects the single compatible schema exposed by all 2xx
// JSON representations. Other response media remain part of the endpoint
// contract but do not participate in agent-tool output derivation.
func ToolSuccessSchema(endpoint Endpoint) (SchemaRef, string, bool, error) {
	var selected SchemaRef
	selectedContentType := ""
	found := false
	bodyless := false
	for _, response := range endpoint.Responses {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			continue
		}
		if len(response.Contents) == 0 {
			if found {
				return SchemaRef{}, "", false, fmt.Errorf("success responses mix JSON and bodyless shapes")
			}
			bodyless = true
			continue
		}
		responseHasJSON := false
		for _, content := range response.Contents {
			if content.BodyKind != "json" || content.Schema == nil {
				continue
			}
			responseHasJSON = true
			if bodyless {
				return SchemaRef{}, "", false, fmt.Errorf("success responses mix JSON and bodyless shapes")
			}
			if found && !reflect.DeepEqual(selected, *content.Schema) {
				return SchemaRef{}, "", false, fmt.Errorf("success responses have incompatible JSON body schemas")
			}
			if !found {
				selected = *content.Schema
				selectedContentType = content.ContentType
				found = true
			}
			if preferredToolJSONContentType(content.ContentType, selectedContentType) {
				selectedContentType = content.ContentType
			}
		}
		if !responseHasJSON {
			return SchemaRef{}, "", false, fmt.Errorf("success response %d has body content but no JSON representation", response.StatusCode)
		}
	}
	return selected, selectedContentType, found, nil
}

func preferredToolJSONContentType(candidate, selected string) bool {
	candidate = strings.TrimSpace(candidate)
	selected = strings.TrimSpace(selected)
	if strings.EqualFold(candidate, "application/json") {
		return !strings.EqualFold(selected, "application/json")
	}
	return !strings.EqualFold(selected, "application/json") && candidate < selected
}

// PrimaryRequestBodyContent returns the preferred content entry for generated
// server and CLI code paths that can use only one request representation.
func PrimaryRequestBodyContent(endpoint Endpoint) (BodyContent, bool) {
	if endpoint.RequestBody == nil || len(endpoint.RequestBody.Contents) == 0 {
		return BodyContent{}, false
	}
	return preferredContent(endpoint.RequestBody.Contents)
}

// PrimaryResponseContent returns the preferred content entry for generated
// server and CLI code paths that can use only one response representation.
func PrimaryResponseContent(response Response) (BodyContent, bool) {
	if len(response.Contents) == 0 {
		return BodyContent{}, false
	}
	return preferredContent(response.Contents)
}

func preferredContent(contents []BodyContent) (BodyContent, bool) {
	for _, content := range contents {
		if content.BodyKind == "json" {
			return content, true
		}
	}
	return contents[0], true
}

// JoinAPIPath combines a contract base path with an authored endpoint path.
func JoinAPIPath(basePath string, endpointPath string) string {
	basePath = strings.TrimSpace(basePath)
	endpointPath = strings.TrimSpace(endpointPath)

	if basePath == "/" {
		basePath = ""
	}
	if endpointPath == "" {
		endpointPath = "/"
	}
	if endpointPath == "/" {
		if basePath == "" {
			return "/"
		}
		return basePath
	}
	if basePath == "" {
		return endpointPath
	}
	return strings.TrimRight(basePath, "/") + endpointPath
}

// ValidateBasePath checks APIGen API base path formatting.
func ValidateBasePath(basePath string) error {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		return fmt.Errorf("api.base_path is required")
	}
	if !strings.HasPrefix(basePath, "/") {
		return fmt.Errorf("api.base_path must start with \"/\"")
	}
	if basePath != "/" && strings.HasSuffix(basePath, "/") {
		return fmt.Errorf("api.base_path must not end with \"/\" unless it is exactly \"/\"")
	}
	return nil
}

// CLICommandString renders a CLI command path as a space-delimited string.
func CLICommandString(cli *CLI) string {
	if cli == nil {
		return ""
	}
	return strings.Join(cli.Command, " ")
}

// CloneCLI returns a deep copy of CLI metadata.
func CloneCLI(in *CLI) *CLI {
	if in == nil {
		return nil
	}
	out := *in
	if len(in.Command) > 0 {
		out.Command = append([]string(nil), in.Command...)
	}
	if len(in.Args) > 0 {
		out.Args = append([]CLIArg(nil), in.Args...)
	}
	if in.Output != nil {
		output := *in.Output
		if len(in.Output.TableColumns) > 0 {
			output.TableColumns = append([]string(nil), in.Output.TableColumns...)
		}
		if len(in.Output.QuietFields) > 0 {
			output.QuietFields = append([]string(nil), in.Output.QuietFields...)
		}
		out.Output = &output
	}
	if in.Pagination != nil {
		pagination := *in.Pagination
		out.Pagination = &pagination
	}
	return &out
}

// PathParameterNames extracts ordered "{param}" names from an endpoint path.
func PathParameterNames(path string) []string {
	params := make([]string, 0, strings.Count(path, "{"))
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			continue
		}
		j := i + 1
		for j < len(path) && path[j] != '}' {
			j++
		}
		if j >= len(path) || j == i+1 {
			continue
		}
		params = append(params, path[i+1:j])
		i = j
	}
	return params
}

// OrderedPropertyNames returns a deterministic property order for a schema.
func OrderedPropertyNames(schema Schema) []string {
	if len(schema.Properties) == 0 {
		return nil
	}
	names := make([]string, 0, len(schema.Properties))
	seen := make(map[string]bool, len(schema.Properties))
	if len(schema.PropertyOrder) > 0 {
		for _, name := range schema.PropertyOrder {
			if _, ok := schema.Properties[name]; ok && !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}
	remaining := make([]string, 0, len(schema.Properties)-len(names))
	for name := range schema.Properties {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	names = append(names, remaining...)
	return names
}

// FlattenObjectSchema returns an object schema with inherited properties and
// requirements merged into one deterministic view. Child properties override
// inherited properties while keeping the inherited field position.
func FlattenObjectSchema(doc Document, schema Schema) Schema {
	return flattenObjectSchema(doc, schema, map[string]bool{})
}

func flattenObjectSchema(doc Document, schema Schema, active map[string]bool) Schema {
	if schema.Base == nil {
		return schema
	}
	baseName, ok := NormalizedSchemaRefName(*schema.Base)
	if !ok || active[baseName] {
		return schema
	}
	base, ok := ResolveSchema(doc, *schema.Base)
	if !ok || !strings.EqualFold(base.Type, "object") {
		return schema
	}
	active[baseName] = true
	base = flattenObjectSchema(doc, base, active)
	delete(active, baseName)

	properties := make(map[string]SchemaProperty, len(base.Properties)+len(schema.Properties))
	for name, property := range base.Properties {
		properties[name] = property
	}
	for name, property := range schema.Properties {
		properties[name] = property
	}
	order := append([]string(nil), OrderedPropertyNames(base)...)
	ordered := make(map[string]bool, len(order))
	for _, name := range order {
		ordered[name] = true
	}
	for _, name := range OrderedPropertyNames(schema) {
		if !ordered[name] {
			order = append(order, name)
			ordered[name] = true
		}
	}
	requiredSet := make(map[string]bool, len(base.Required)+len(schema.Required))
	for _, name := range base.Required {
		requiredSet[name] = true
	}
	for _, name := range schema.Required {
		requiredSet[name] = true
	}
	required := make([]string, 0, len(requiredSet))
	for name := range requiredSet {
		required = append(required, name)
	}
	sort.Strings(required)

	schema.Base = nil
	schema.Properties = properties
	schema.PropertyOrder = order
	schema.Required = required
	return schema
}

// ResolvedObjectProperty is one property from an inherited object or from the
// compatible common surface of every variant in an object-shaped union.
type ResolvedObjectProperty struct {
	Property SchemaProperty
	Required bool
}

// ResolveObjectProperty resolves a property through object inheritance and
// discriminated unions. A union property is available only when every variant
// declares it with compatible wire types.
func ResolveObjectProperty(doc Document, ref SchemaRef, name string) (ResolvedObjectProperty, bool, error) {
	return resolveObjectProperty(doc, ref, name, map[string]bool{})
}

func resolveObjectProperty(doc Document, ref SchemaRef, name string, active map[string]bool) (ResolvedObjectProperty, bool, error) {
	schema, ok := resolveConcreteSchema(doc, ref)
	if !ok {
		return ResolvedObjectProperty{}, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(schema.Type)) {
	case "object":
		schema = FlattenObjectSchema(doc, schema)
		property, found := schema.Properties[name]
		if !found {
			return ResolvedObjectProperty{}, false, nil
		}
		return ResolvedObjectProperty{Property: property, Required: stringSetContains(schema.Required, name)}, true, nil
	case "union":
		key := schemaRefKey(ref)
		if key != "" {
			if active[key] {
				return ResolvedObjectProperty{}, false, fmt.Errorf("property %q traverses a recursive union", name)
			}
			active[key] = true
			defer delete(active, key)
		}
		if len(schema.OneOf) == 0 {
			return ResolvedObjectProperty{}, false, nil
		}
		var resolved ResolvedObjectProperty
		for i, variant := range schema.OneOf {
			candidate, found, err := resolveObjectProperty(doc, variant, name, active)
			if err != nil {
				return ResolvedObjectProperty{}, false, err
			}
			if !found {
				return ResolvedObjectProperty{}, false, nil
			}
			if i == 0 {
				resolved = candidate
				continue
			}
			merged, compatible := mergeCompatibleSchemaRefs(doc, resolved.Property.Schema, candidate.Property.Schema)
			if !compatible {
				return ResolvedObjectProperty{}, false, fmt.Errorf("property %q has incompatible schemas across union variants", name)
			}
			resolved.Property.Schema = merged
			resolved.Required = resolved.Required && candidate.Required
		}
		return resolved, true, nil
	default:
		return ResolvedObjectProperty{}, false, nil
	}
}

// ResolveObjectSchema returns the deterministic object surface visible on an
// inherited object or object-shaped union. Variant-only and incompatible union
// properties are intentionally excluded.
func ResolveObjectSchema(doc Document, ref SchemaRef) (Schema, bool) {
	return resolveObjectSchema(doc, ref, map[string]bool{})
}

func resolveObjectSchema(doc Document, ref SchemaRef, active map[string]bool) (Schema, bool) {
	schema, ok := resolveConcreteSchema(doc, ref)
	if !ok {
		return Schema{}, false
	}
	switch strings.ToLower(strings.TrimSpace(schema.Type)) {
	case "object":
		return FlattenObjectSchema(doc, schema), true
	case "union":
		if len(schema.OneOf) == 0 {
			return Schema{}, false
		}
		key := schemaRefKey(ref)
		if key != "" {
			if active[key] {
				return Schema{}, false
			}
			active[key] = true
			defer delete(active, key)
		}
		variantSchemas := make([]Schema, len(schema.OneOf))
		for i, variant := range schema.OneOf {
			variantSchema, object := resolveObjectSchema(doc, variant, active)
			if !object {
				return Schema{}, false
			}
			variantSchemas[i] = variantSchema
		}
		first := variantSchemas[0]
		if len(first.Properties) == 0 {
			return Schema{Type: "object", Properties: map[string]SchemaProperty{}}, true
		}
		properties := make(map[string]SchemaProperty)
		order := make([]string, 0, len(first.Properties))
		required := make([]string, 0, len(first.Required))
		for _, name := range OrderedPropertyNames(first) {
			property, found, err := ResolveObjectProperty(doc, ref, name)
			if err != nil || !found {
				continue
			}
			properties[name] = property.Property
			order = append(order, name)
			if property.Required {
				required = append(required, name)
			}
		}
		return Schema{Type: "object", Properties: properties, PropertyOrder: order, Required: required}, true
	default:
		return Schema{}, false
	}
}

// ResolveCommonObjectSchema returns the deterministic object surface shared by
// every schema reference. Properties that are absent or incompatible in any
// branch are excluded from the common view.
func ResolveCommonObjectSchema(doc Document, refs []SchemaRef) (Schema, bool) {
	if len(refs) == 0 {
		return Schema{}, false
	}
	views := make([]Schema, len(refs))
	for i, ref := range refs {
		view, ok := ResolveObjectSchema(doc, ref)
		if !ok {
			return Schema{}, false
		}
		views[i] = view
	}
	first := views[0]
	properties := make(map[string]SchemaProperty)
	order := make([]string, 0, len(first.Properties))
	required := make([]string, 0, len(first.Required))
	for _, name := range OrderedPropertyNames(first) {
		property, found, err := ResolveCommonObjectProperty(doc, refs, name)
		if err != nil || !found {
			continue
		}
		properties[name] = property.Property
		order = append(order, name)
		if property.Required {
			required = append(required, name)
		}
	}
	return Schema{Type: "object", Properties: properties, PropertyOrder: order, Required: required}, true
}

// ResolveCommonObjectProperty resolves one compatible property shared by each
// object schema reference.
func ResolveCommonObjectProperty(doc Document, refs []SchemaRef, name string) (ResolvedObjectProperty, bool, error) {
	if len(refs) == 0 {
		return ResolvedObjectProperty{}, false, nil
	}
	var resolved ResolvedObjectProperty
	for i, ref := range refs {
		candidate, found, err := ResolveObjectProperty(doc, ref, name)
		if err != nil {
			return ResolvedObjectProperty{}, false, err
		}
		if !found {
			return ResolvedObjectProperty{}, false, nil
		}
		if i == 0 {
			resolved = candidate
			continue
		}
		merged, compatible := mergeCompatibleSchemaRefs(doc, resolved.Property.Schema, candidate.Property.Schema)
		if !compatible {
			return ResolvedObjectProperty{}, false, fmt.Errorf("property %q has incompatible schemas across object variants", name)
		}
		resolved.Property.Schema = merged
		resolved.Required = resolved.Required && candidate.Required
	}
	return resolved, true, nil
}

// ResolveObjectArrayItemSchemas retains the item schema from each object or
// union branch instead of collapsing heterogeneous arrays to array<any>.
func ResolveObjectArrayItemSchemas(doc Document, ref SchemaRef, name string) ([]SchemaRef, bool, error) {
	return resolveObjectArrayItemSchemas(doc, ref, name, map[string]bool{})
}

func resolveObjectArrayItemSchemas(doc Document, ref SchemaRef, name string, active map[string]bool) ([]SchemaRef, bool, error) {
	schema, ok := resolveConcreteSchema(doc, ref)
	if !ok {
		return nil, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(schema.Type)) {
	case "object":
		schema = FlattenObjectSchema(doc, schema)
		property, found := schema.Properties[name]
		if !found {
			return nil, false, nil
		}
		kind, item := SchemaProjectionKind(doc, property.Schema)
		if kind != "array" {
			return nil, false, fmt.Errorf("property %q must be an array in every object variant", name)
		}
		return []SchemaRef{item}, true, nil
	case "union":
		key := schemaRefKey(ref)
		if key != "" {
			if active[key] {
				return nil, false, fmt.Errorf("property %q traverses a recursive union", name)
			}
			active[key] = true
			defer delete(active, key)
		}
		if len(schema.OneOf) == 0 {
			return nil, false, nil
		}
		items := make([]SchemaRef, 0, len(schema.OneOf))
		for _, variant := range schema.OneOf {
			variantItems, found, err := resolveObjectArrayItemSchemas(doc, variant, name, active)
			if err != nil {
				return nil, false, err
			}
			if !found {
				return nil, false, nil
			}
			items = append(items, variantItems...)
		}
		return items, true, nil
	default:
		return nil, false, nil
	}
}

// ResolveSchemaPointer resolves an RFC 6901 property pointer against an object
// surface and reports whether any traversed property or map entry is optional.
func ResolveSchemaPointer(doc Document, scope SchemaRef, pointer string) (SchemaRef, bool, error) {
	segments, err := JSONPointerSegments(pointer)
	if err != nil {
		return SchemaRef{}, false, err
	}
	current := scope
	optional := false
	for _, segment := range segments {
		if current.AdditionalProperties != nil {
			optional = true
			if current.AdditionalProperties.Schema != nil {
				current = *current.AdditionalProperties.Schema
			} else if current.AdditionalProperties.Any {
				current = SchemaRef{Type: "object"}
			} else {
				return SchemaRef{}, false, fmt.Errorf("pointer %q cannot traverse %q", pointer, segment)
			}
			continue
		}
		property, found, err := ResolveObjectProperty(doc, current, segment)
		if err != nil {
			return SchemaRef{}, false, fmt.Errorf("pointer %q: %w", pointer, err)
		}
		if !found {
			return SchemaRef{}, false, fmt.Errorf("pointer %q references unknown property %q", pointer, segment)
		}
		if !property.Required {
			optional = true
		}
		current = property.Property.Schema
	}
	return current, optional, nil
}

// SchemaProjectionKind classifies a projection source and returns the scope
// used for nested selections.
func SchemaProjectionKind(doc Document, ref SchemaRef) (string, SchemaRef) {
	if ref.AdditionalProperties != nil {
		if ref.AdditionalProperties.Schema != nil {
			return "map", *ref.AdditionalProperties.Schema
		}
		return "map", SchemaRef{Type: "object"}
	}
	schema, ok := resolveConcreteSchema(doc, ref)
	if ok {
		switch strings.ToLower(strings.TrimSpace(schema.Type)) {
		case "array":
			if schema.Items != nil {
				return "array", *schema.Items
			}
			return "array", SchemaRef{}
		case "object", "union":
			if _, object := ResolveObjectSchema(doc, ref); object {
				return "object", ref
			}
		}
	}
	return "value", ref
}

// JSONPointerSegments parses and unescapes an RFC 6901 pointer.
func JSONPointerSegments(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("pointer %q must be an RFC 6901 pointer", pointer)
	}
	raw := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	segments := make([]string, len(raw))
	for i, value := range raw {
		if strings.Contains(value, "~") {
			for j := 0; j < len(value); j++ {
				if value[j] == '~' && (j+1 >= len(value) || (value[j+1] != '0' && value[j+1] != '1')) {
					return nil, fmt.Errorf("pointer %q has invalid escape", pointer)
				}
			}
		}
		segments[i] = strings.ReplaceAll(strings.ReplaceAll(value, "~1", "/"), "~0", "~")
	}
	return segments, nil
}

func resolveConcreteSchema(doc Document, ref SchemaRef) (Schema, bool) {
	if ref.Ref != "" {
		return ResolveSchema(doc, ref)
	}
	if strings.TrimSpace(ref.Type) == "" {
		return Schema{}, false
	}
	return Schema{Type: strings.ToLower(strings.TrimSpace(ref.Type)), Items: ref.Items, Enum: append([]string(nil), ref.Enum...)}, true
}

func mergeCompatibleSchemaRefs(doc Document, left, right SchemaRef) (SchemaRef, bool) {
	if reflect.DeepEqual(left, right) {
		return left, true
	}
	leftSchema, leftOK := resolveConcreteSchema(doc, left)
	rightSchema, rightOK := resolveConcreteSchema(doc, right)
	if !leftOK || !rightOK {
		return SchemaRef{}, false
	}
	leftType := strings.ToLower(strings.TrimSpace(leftSchema.Type))
	rightType := strings.ToLower(strings.TrimSpace(rightSchema.Type))
	if leftType != rightType {
		return SchemaRef{}, false
	}
	switch leftType {
	case "string", "integer", "number", "boolean":
		return SchemaRef{Type: leftType}, true
	case "array":
		if leftSchema.Items == nil || rightSchema.Items == nil {
			return SchemaRef{Type: "array"}, true
		}
		items, ok := mergeCompatibleSchemaRefs(doc, *leftSchema.Items, *rightSchema.Items)
		if !ok {
			return SchemaRef{Type: "array"}, true
		}
		return SchemaRef{Type: "array", Items: &items}, true
	case "object":
		if left.AdditionalProperties != nil || right.AdditionalProperties != nil {
			return mergeCompatibleMapRefs(doc, left, right)
		}
		leftObject, leftObjectOK := ResolveObjectSchema(doc, left)
		rightObject, rightObjectOK := ResolveObjectSchema(doc, right)
		if leftObjectOK && rightObjectOK && reflect.DeepEqual(leftObject.Properties, rightObject.Properties) && reflect.DeepEqual(schemaRequiredSet(leftObject.Required), schemaRequiredSet(rightObject.Required)) {
			return left, true
		}
	}
	return SchemaRef{}, false
}

func mergeCompatibleMapRefs(doc Document, left, right SchemaRef) (SchemaRef, bool) {
	if left.AdditionalProperties == nil || right.AdditionalProperties == nil {
		return SchemaRef{}, false
	}
	if left.AdditionalProperties.Any || right.AdditionalProperties.Any {
		return SchemaRef{Type: "object", AdditionalProperties: &AdditionalProperties{Any: true}}, true
	}
	if left.AdditionalProperties.Schema == nil || right.AdditionalProperties.Schema == nil {
		return SchemaRef{}, false
	}
	value, ok := mergeCompatibleSchemaRefs(doc, *left.AdditionalProperties.Schema, *right.AdditionalProperties.Schema)
	if !ok {
		return SchemaRef{}, false
	}
	return SchemaRef{Type: "object", AdditionalProperties: &AdditionalProperties{Schema: &value}}, true
}

func schemaRefKey(ref SchemaRef) string {
	if name, ok := NormalizedSchemaRefName(ref); ok {
		return name
	}
	return ""
}

func stringSetContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func schemaRequiredSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func exportedName(value string) string {
	parts := splitIdentifier(value)
	if len(parts) == 0 {
		return "Operation"
	}
	for i := range parts {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func splitIdentifier(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	replacer := strings.NewReplacer("-", " ", "_", " ", ".", " ", "/", " ")
	value = replacer.Replace(value)
	chunks := strings.Fields(value)
	if len(chunks) > 0 {
		return chunks
	}
	return []string{value}
}
