package configschema

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	cuejsonschema "cuelang.org/go/encoding/jsonschema"
	cueyaml "cuelang.org/go/encoding/yaml"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

//go:embed contracts/contracts.cue
var contractsCUE string

// dashboardDocumentSchema is generated from api/dashboard TypeSpec and is the
// sole structural authority for canonical Dashboard resources. It is kept
// beside the CUE contracts so schema consumers do not depend on a build-time
// path outside this package.
//
//go:embed contracts/dashboard-document.schema.json
var dashboardDocumentSchema []byte

var (
	dashboardSchemaOnce sync.Once
	dashboardSchema     *jsonschema.Schema
	dashboardSchemaErr  error
)

type Kind string

const (
	KindProject              Kind = "project"
	KindConnection           Kind = "connection"
	KindSource               Kind = "source"
	KindModel                Kind = "model"
	KindSemanticModel        Kind = "semantic-model"
	KindPipeline             Kind = "pipeline"
	KindDashboard            Kind = "dashboard"
	KindGroup                Kind = "group"
	KindRoleBinding          Kind = "role-binding"
	KindGrant                Kind = "grant"
	KindDataPolicy           Kind = "data-policy"
	KindDashboardPublication Kind = "dashboard-publication"
)

type Severity string

const (
	SeverityError Severity = "error"
)

type Diagnostic struct {
	File       string   `json:"file,omitempty"`
	Line       int      `json:"line,omitempty"`
	Column     int      `json:"column,omitempty"`
	ResourceID string   `json:"resourceId,omitempty"`
	FieldPath  string   `json:"fieldPath,omitempty"`
	Severity   Severity `json:"severity"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
}

type Error struct {
	Diagnostics []Diagnostic
}

func (e *Error) Error() string {
	if len(e.Diagnostics) == 0 {
		return "configuration schema validation failed"
	}
	return e.Diagnostics[0].String()
}

func (d Diagnostic) String() string {
	location := d.File
	if d.Line > 0 {
		location += fmt.Sprintf(":%d", d.Line)
		if d.Column > 0 {
			location += fmt.Sprintf(":%d", d.Column)
		}
	}
	if location == "" {
		return fmt.Sprintf("%s %s: %s", d.Severity, d.Code, d.Message)
	}
	context := ""
	if d.ResourceID != "" {
		context += " resource=" + d.ResourceID
	}
	if d.FieldPath != "" {
		context += " field=" + d.FieldPath
	}
	return fmt.Sprintf("%s: %s %s%s: %s", location, d.Severity, d.Code, context, d.Message)
}

func ValidateFile(kind Kind, path string) error {
	content, err := readFile(path)
	if err != nil {
		return err
	}
	return ValidateBytes(kind, path, content)
}

func ValidateBytes(kind Kind, filename string, content []byte) error {
	if kind == KindDashboard {
		return validateDashboardBytes(filename, content)
	}
	ctx, value, definition, err := compiledDefinition(kind)
	if err != nil {
		return err
	}
	file, err := cueyaml.Extract(filename, content)
	if err != nil {
		return &Error{Diagnostics: []Diagnostic{{
			File:     filename,
			Severity: SeverityError,
			Code:     "schema.yaml",
			Message:  err.Error(),
		}}}
	}
	data := ctx.BuildFile(file)
	value = value.Unify(data)
	if err := value.Validate(cue.Final()); err != nil {
		return &Error{Diagnostics: diagnosticsForCUEError(filename, definition, err)}
	}
	if diagnostics := requiredCollectionDiagnostics(kind, filename, content); len(diagnostics) > 0 {
		return &Error{Diagnostics: diagnostics}
	}
	return nil
}

func JSONSchema(kind Kind) ([]byte, error) {
	if kind == KindDashboard {
		return append([]byte(nil), dashboardDocumentSchema...), nil
	}
	ctx, value, _, err := compiledDefinition(kind)
	if err != nil {
		return nil, err
	}
	expr, err := cuejsonschema.Generate(value, &cuejsonschema.GenerateConfig{Version: cuejsonschema.VersionDraft2020_12})
	if err != nil {
		return nil, err
	}
	raw, err := ctx.BuildExpr(expr).MarshalJSON()
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	hardenJSONSchema(kind, payload)
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(pretty, '\n'), nil
}

func validateDashboardBytes(filename string, content []byte) error {
	normalized, err := NormalizeJSONDocument(filename, content)
	if err != nil {
		return err
	}
	schema, err := compiledDashboardSchema()
	if err != nil {
		return resourceDiagnostic(filename, nil, "schema.generated", err.Error())
	}
	var value any
	if err := json.Unmarshal(normalized, &value); err != nil {
		return resourceDiagnostic(filename, nil, "schema.generated", "decode normalized dashboard: "+err.Error())
	}
	if err := validateDashboardCUE(filename, normalized); err != nil {
		// The generated Dashboard JSON Schema is imported into CUE above and is
		// the acceptance gate. Re-run its validator only to retain the stable
		// keyword/path diagnostics exposed by the public CLI contract.
		if schemaErr := schema.Validate(value); schemaErr != nil {
			return &Error{Diagnostics: []Diagnostic{dashboardValidationDiagnostic(filename, content, schemaErr)}}
		}
		return resourceDiagnostic(filename, nil, "schema.contract", err.Error())
	}
	return nil
}

func validateDashboardCUE(filename string, content []byte) error {
	ctx := cuecontext.New()
	schemaValue := ctx.CompileBytes(dashboardDocumentSchema, cue.Filename("dashboard-document.schema.json"))
	if err := schemaValue.Err(); err != nil {
		return fmt.Errorf("compile generated dashboard schema: %w", err)
	}
	expr, err := cuejsonschema.Extract(schemaValue, &cuejsonschema.Config{ID: "https://leapview.dev/schemas/dashboard-document.schema.json"})
	if err != nil {
		return fmt.Errorf("import generated dashboard schema into CUE: %w", err)
	}
	contract := ctx.BuildFile(expr)
	if err := contract.Err(); err != nil {
		return fmt.Errorf("compile imported dashboard CUE contract: %w", err)
	}
	file, err := cueyaml.Extract(filename, content)
	if err != nil {
		return fmt.Errorf("decode dashboard YAML for CUE: %w", err)
	}
	instance := ctx.BuildFile(file)
	if err := instance.Err(); err != nil {
		return fmt.Errorf("compile dashboard YAML for CUE: %w", err)
	}
	if err := contract.Unify(instance).Validate(cue.Final()); err != nil {
		return err
	}
	return nil
}

func dashboardValidationDiagnostic(filename string, content []byte, err error) Diagnostic {
	diagnostic := resourceDiagnostic(filename, nil, "schema.generated", err.Error()).(*Error).Diagnostics[0]
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) || validation == nil {
		return diagnostic
	}
	best := validationCause(validation)
	if best == nil {
		return diagnostic
	}
	keyword := ""
	keywords := best.ErrorKind.KeywordPath()
	if len(keywords) > 0 {
		keyword = keywords[len(keywords)-1]
	}
	switch keyword {
	case "enum":
		diagnostic.Code = "schema.enum"
	case "type":
		diagnostic.Code = "schema.type"
	case "required":
		diagnostic.Code = "schema.contract"
	case "additionalProperties", "unevaluatedProperties":
		diagnostic.Code = "schema.unknown_field"
	case "pattern", "format":
		diagnostic.Code = "schema.pattern"
	}
	diagnostic.Line, diagnostic.Column = dashboardYAMLLocation(content, best.InstanceLocation)
	diagnostic.FieldPath = dashboardFieldPath(best.InstanceLocation)
	return diagnostic
}

func dashboardFieldPath(path []string) string {
	var result strings.Builder
	for _, segment := range path {
		if _, err := strconv.Atoi(segment); err == nil {
			result.WriteByte('[')
			result.WriteString(segment)
			result.WriteByte(']')
			continue
		}
		if result.Len() > 0 {
			result.WriteByte('.')
		}
		result.WriteString(segment)
	}
	return result.String()
}

func validationCause(value *jsonschema.ValidationError) *jsonschema.ValidationError {
	if value == nil {
		return nil
	}
	best := value
	bestScore := validationKeywordScore(value)
	for _, cause := range value.Causes {
		candidate := validationCause(cause)
		if validationKeywordScore(candidate) > bestScore {
			best, bestScore = candidate, validationKeywordScore(candidate)
		}
	}
	return best
}

func validationKeywordScore(value *jsonschema.ValidationError) int {
	if value == nil || value.ErrorKind == nil {
		return -1
	}
	keywords := value.ErrorKind.KeywordPath()
	if len(keywords) == 0 {
		return 0
	}
	switch keywords[len(keywords)-1] {
	case "enum":
		return 100
	case "type":
		return 90
	case "required":
		return 80
	case "additionalProperties", "unevaluatedProperties":
		return 70
	case "pattern", "format":
		return 60
	default:
		return 10
	}
}

func dashboardYAMLLocation(content []byte, path []string) (int, int) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return 0, 0
	}
	node := yamlMappingNode(&root)
	for _, segment := range path {
		if node == nil {
			return 0, 0
		}
		if node.Kind == yaml.MappingNode {
			found := false
			for index := 0; index+1 < len(node.Content); index += 2 {
				if node.Content[index].Value == segment {
					node = node.Content[index+1]
					found = true
					break
				}
			}
			if !found {
				return 0, 0
			}
			continue
		}
		if node.Kind == yaml.SequenceNode {
			var index int
			if _, err := fmt.Sscanf(segment, "%d", &index); err != nil || index < 0 || index >= len(node.Content) {
				return 0, 0
			}
			node = node.Content[index]
		}
	}
	if node == nil {
		return 0, 0
	}
	return node.Line, node.Column
}

func compiledDashboardSchema() (*jsonschema.Schema, error) {
	dashboardSchemaOnce.Do(func() {
		var value any
		if err := json.Unmarshal(dashboardDocumentSchema, &value); err != nil {
			dashboardSchemaErr = fmt.Errorf("decode generated dashboard schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		const schemaURL = "https://leapview.dev/schemas/dashboard-document.schema.json"
		if err := compiler.AddResource(schemaURL, value); err != nil {
			dashboardSchemaErr = fmt.Errorf("register generated dashboard schema: %w", err)
			return
		}
		dashboardSchema, dashboardSchemaErr = compiler.Compile(schemaURL)
	})
	return dashboardSchema, dashboardSchemaErr
}

func compiledDefinition(kind Kind) (*cue.Context, cue.Value, string, error) {
	definition, err := definitionName(kind)
	if err != nil {
		return nil, cue.Value{}, "", err
	}
	ctx := cuecontext.New()
	contracts := ctx.CompileString(contractsCUE, cue.Filename("contracts.cue"))
	if err := contracts.Err(); err != nil {
		return nil, cue.Value{}, "", err
	}
	value := contracts.LookupPath(cue.MakePath(cue.Def(definition)))
	return ctx, value, definition, nil
}

func JSONSchemaFiles() (map[string][]byte, error) {
	kinds := []Kind{KindProject, KindConnection, KindSource, KindModel, KindSemanticModel, KindPipeline, KindDashboard, KindGroup, KindRoleBinding, KindGrant, KindDataPolicy, KindDashboardPublication}
	files := map[string][]byte{}
	for _, kind := range kinds {
		content, err := JSONSchema(kind)
		if err != nil {
			return nil, err
		}
		files[JSONSchemaFilename(kind)] = content
	}
	return files, nil
}

func JSONSchemaFilename(kind Kind) string {
	switch kind {
	case KindProject:
		return "project.schema.json"
	case KindConnection:
		return "connection.schema.json"
	case KindSource:
		return "source.schema.json"
	case KindModel:
		return "model.schema.json"
	case KindSemanticModel:
		return "semantic-model.schema.json"
	case KindPipeline:
		return "pipeline.schema.json"
	case KindDashboard:
		return "dashboard-document.schema.json"
	case KindGroup:
		return "group.schema.json"
	case KindRoleBinding:
		return "role-binding.schema.json"
	case KindGrant:
		return "grant.schema.json"
	case KindDataPolicy:
		return "data-policy.schema.json"
	case KindDashboardPublication:
		return "dashboard-publication.schema.json"
	default:
		return string(kind) + ".schema.json"
	}
}

func Diagnostics(err error) []Diagnostic {
	if err == nil {
		return nil
	}
	var schemaErr *Error
	if errors.As(err, &schemaErr) {
		return append([]Diagnostic(nil), schemaErr.Diagnostics...)
	}
	return []Diagnostic{DiagnosticForError(err)}
}

func DiagnosticForError(err error) Diagnostic {
	var provider interface {
		Diagnostic() Diagnostic
	}
	if errors.As(err, &provider) {
		return provider.Diagnostic()
	}
	return Diagnostic{
		Severity: SeverityError,
		Code:     compilerCode(err),
		Message:  err.Error(),
	}
}

func definitionName(kind Kind) (string, error) {
	switch kind {
	case KindProject:
		return "Project", nil
	case KindConnection:
		return "ConnectionResource", nil
	case KindSource:
		return "SourceResource", nil
	case KindModel:
		return "ModelResource", nil
	case KindSemanticModel:
		return "SemanticModelResource", nil
	case KindPipeline:
		return "PipelineResource", nil
	case KindDashboard:
		return "DashboardResource", nil
	case KindGroup:
		return "GroupResource", nil
	case KindRoleBinding:
		return "RoleBindingResource", nil
	case KindGrant:
		return "GrantResource", nil
	case KindDataPolicy:
		return "DataPolicyResource", nil
	case KindDashboardPublication:
		return "DashboardPublicationResource", nil
	default:
		return "", fmt.Errorf("unknown schema kind %q", kind)
	}
}

func diagnosticsForCUEError(filename, definition string, err error) []Diagnostic {
	items := cueerrors.Errors(err)
	if len(items) == 0 {
		return []Diagnostic{{
			File:     filename,
			Severity: SeverityError,
			Code:     schemaCode(err.Error()),
			Message:  cleanMessage(definition, err.Error()),
		}}
	}
	diagnostics := make([]Diagnostic, 0, len(items))
	for _, item := range items {
		message := cueerrors.String(item)
		if len(items) > 1 && strings.Contains(message, "empty disjunction") {
			continue
		}
		pos := positionFor(filename, item)
		diagnostics = append(diagnostics, Diagnostic{
			File:     pos.file,
			Line:     pos.line,
			Column:   pos.column,
			Severity: SeverityError,
			Code:     schemaCode(message),
			Message:  cleanMessage(definition, message),
		})
	}
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].File != diagnostics[j].File {
			return diagnostics[i].File < diagnostics[j].File
		}
		if diagnostics[i].Line == 0 || diagnostics[j].Line == 0 {
			return diagnostics[j].Line == 0 && diagnostics[i].Line != 0
		}
		if diagnostics[i].Line != diagnostics[j].Line {
			return diagnostics[i].Line < diagnostics[j].Line
		}
		return diagnostics[i].Column < diagnostics[j].Column
	})
	if len(diagnostics) == 0 {
		return []Diagnostic{{
			File:     filename,
			Severity: SeverityError,
			Code:     schemaCode(err.Error()),
			Message:  cleanMessage(definition, err.Error()),
		}}
	}
	return diagnostics
}

type diagnosticPosition struct {
	file   string
	line   int
	column int
}

func positionFor(filename string, err cueerrors.Error) diagnosticPosition {
	positions := cueerrors.Positions(err)
	for _, pos := range positions {
		if filepath.Clean(pos.Filename()) == filepath.Clean(filename) {
			return diagnosticPosition{file: filename, line: pos.Line(), column: pos.Column()}
		}
	}
	for _, pos := range positions {
		if pos.Filename() != "" && pos.Filename() != "contracts.cue" {
			return diagnosticPosition{file: pos.Filename(), line: pos.Line(), column: pos.Column()}
		}
	}
	return diagnosticPosition{file: filename}
}

func schemaCode(message string) string {
	switch {
	case strings.Contains(message, "field not allowed"):
		return "schema.unknown_field"
	case strings.Contains(message, "mismatched types"), strings.Contains(message, "cannot use"):
		return "schema.type"
	case strings.Contains(message, "=~"):
		return "schema.contract"
	case strings.Contains(message, "empty disjunction"), strings.Contains(message, "conflicting values"),
		strings.Contains(message, "invalid value"), strings.Contains(message, "out of bound"), strings.Contains(message, "not allowed"):
		return "schema.enum"
	default:
		return "schema.contract"
	}
}

func compilerCode(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "unknown dimension"),
		strings.Contains(message, "unknown metric"),
		strings.Contains(message, "unknown semantic model"),
		strings.Contains(message, "unknown table"),
		strings.Contains(message, "references unknown"):
		return "compiler.reference"
	default:
		return "compiler.contract"
	}
}

func cleanMessage(definition, message string) string {
	prefixes := []string{"#" + definition + ".", "#" + definition + ":"}
	for _, prefix := range prefixes {
		message = strings.ReplaceAll(message, prefix, "")
	}
	message = strings.TrimSpace(message)
	return message
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

type schemaOverlay struct {
	required    []string
	collections []collectionRule
}

type collectionKind string

const (
	collectionMapping  collectionKind = "mapping"
	collectionSequence collectionKind = "sequence"
)

type collectionRule struct {
	path     schemaPath
	kind     collectionKind
	min      int
	rootYAML bool
}

type schemaPath struct {
	definition string
	property   string
}

var schemaOverlays = map[Kind]schemaOverlay{
	KindProject: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
		collections: []collectionRule{
			definitionCollection("#Project", "connections", collectionMapping),
			definitionCollection("#Project", "sources", collectionMapping),
			definitionCollection("#Project", "models", collectionMapping),
			definitionCollection("#Project", "semanticModels", collectionMapping),
			definitionCollection("#Project", "pipelines", collectionMapping),
			definitionCollection("#Project", "dashboards", collectionMapping),
		},
	},
	KindConnection: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindSource: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindModel: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindSemanticModel: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
		collections: []collectionRule{
			definitionCollection("#ProjectSemanticModelSpec", "datasets", collectionMapping),
		},
	},
	KindPipeline: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindDashboard: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindGroup: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindRoleBinding: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindGrant: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindDataPolicy: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
	KindDashboardPublication: {
		required: []string{"apiVersion", "kind", "metadata", "spec"},
	},
}

func rootCollection(property string, kind collectionKind) collectionRule {
	return collectionRule{
		path:     schemaPath{property: property},
		kind:     kind,
		min:      1,
		rootYAML: true,
	}
}

func definitionCollection(definition, property string, kind collectionKind) collectionRule {
	return collectionRule{
		path: schemaPath{
			definition: definition,
			property:   property,
		},
		kind: kind,
		min:  1,
	}
}

func hardenJSONSchema(kind Kind, payload any) {
	normalizeGeneratedSchema(payload)
	root, ok := payload.(map[string]any)
	if !ok {
		return
	}
	overlay, ok := schemaOverlays[kind]
	if !ok {
		return
	}
	addRequired(root, overlay.required...)
	for _, collection := range overlay.collections {
		schema := collection.path.resolve(root)
		switch collection.kind {
		case collectionMapping:
			addMinProperties(schema, collection.min)
		case collectionSequence:
			addMinItems(schema, collection.min)
		}
	}
}

func normalizeGeneratedSchema(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if _, hasPatterns := typed["patternProperties"]; hasPatterns {
			if _, exists := typed["additionalProperties"]; !exists {
				typed["additionalProperties"] = false
			}
		}
		if typed["type"] == "array" {
			if minLength, ok := typed["minLength"]; ok {
				if _, exists := typed["minItems"]; !exists {
					typed["minItems"] = minLength
				}
				delete(typed, "minLength")
			}
		}
		for _, item := range typed {
			normalizeGeneratedSchema(item)
		}
	case []any:
		for _, item := range typed {
			normalizeGeneratedSchema(item)
		}
	}
}

func addRequired(schema map[string]any, fields ...string) {
	if schema == nil {
		return
	}
	seen := map[string]bool{}
	required := []any{}
	if existing, ok := schema["required"].([]any); ok {
		for _, item := range existing {
			value, ok := item.(string)
			if !ok || seen[value] {
				continue
			}
			seen[value] = true
			required = append(required, value)
		}
	}
	for _, field := range fields {
		if seen[field] {
			continue
		}
		seen[field] = true
		required = append(required, field)
	}
	sort.Slice(required, func(i, j int) bool {
		return required[i].(string) < required[j].(string)
	})
	schema["required"] = required
}

func addMinItems(schema map[string]any, min int) {
	if schema != nil {
		schema["minItems"] = min
	}
}

func addMinProperties(schema map[string]any, min int) {
	if schema != nil {
		schema["minProperties"] = min
	}
}

func propertySchema(schema map[string]any, name string) map[string]any {
	if schema == nil {
		return nil
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		return nil
	}
	return property
}

func definitionSchema(schema map[string]any, name string) map[string]any {
	if schema == nil {
		return nil
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		return nil
	}
	definition, ok := definitions[name].(map[string]any)
	if !ok {
		return nil
	}
	return definition
}

func (p schemaPath) resolve(root map[string]any) map[string]any {
	schema := root
	if p.definition != "" {
		schema = definitionSchema(root, p.definition)
	}
	return propertySchema(schema, p.property)
}

func requiredCollectionDiagnostics(kind Kind, filename string, content []byte) []Diagnostic {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return nil
	}
	root := yamlMappingNode(&document)
	if root == nil {
		return nil
	}
	var diagnostics []Diagnostic
	overlay, ok := schemaOverlays[kind]
	if !ok {
		return nil
	}
	for _, collection := range overlay.collections {
		if !collection.rootYAML {
			continue
		}
		requireNonEmptyYAMLCollection(&diagnostics, filename, root, collection)
	}
	return diagnostics
}

func requireNonEmptyYAMLCollection(diagnostics *[]Diagnostic, filename string, root *yaml.Node, collection collectionRule) {
	node := yamlMappingValue(root, collection.path.property)
	if node == nil || !collection.matchesYAMLKind(node.Kind) || collection.yamlItemCount(node) >= collection.min {
		return
	}
	*diagnostics = append(*diagnostics, collectionDiagnostic(filename, node, collection.path.property))
}

func (c collectionRule) matchesYAMLKind(kind yaml.Kind) bool {
	return c.kind == collectionMapping && kind == yaml.MappingNode ||
		c.kind == collectionSequence && kind == yaml.SequenceNode
}

func (c collectionRule) yamlItemCount(node *yaml.Node) int {
	if c.kind == collectionMapping {
		return len(node.Content) / 2
	}
	return len(node.Content)
}

func collectionDiagnostic(filename string, node *yaml.Node, key string) Diagnostic {
	return Diagnostic{
		File:     filename,
		Line:     node.Line,
		Column:   node.Column,
		Severity: SeverityError,
		Code:     "schema.contract",
		Message:  fmt.Sprintf("%s requires at least one item", key),
	}
}

func yamlMappingNode(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	if node.Kind == yaml.MappingNode {
		return node
	}
	return nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}
