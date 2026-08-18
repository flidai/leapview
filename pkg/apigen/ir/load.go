package ir

import (
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// CurrentSchemaVersion is the supported JSON IR schema version.
const CurrentSchemaVersion = "v4"

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var auditActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
var stableNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var uiActionPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*(\.[a-z][a-z0-9]*(-[a-z0-9]+)*)+$`)
var jobKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
var failureCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Load parses and validates an IR document from disk.
func Load(path string) (Document, error) {
	cleanPath := filepath.Clean(path)
	// #nosec G304 -- path is an explicit CLI/task input by design.
	content, err := os.ReadFile(cleanPath)
	if err != nil {
		return Document{}, fmt.Errorf("read ir file: %w", err)
	}

	dec := json.NewDecoder(strings.NewReader(string(content)))
	dec.DisallowUnknownFields()

	var doc Document
	if err := dec.Decode(&doc); err != nil {
		return Document{}, fmt.Errorf("decode ir json: %w", err)
	}
	if err := Validate(doc); err != nil {
		return Document{}, err
	}
	if err := Normalize(&doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// Validate checks required fields and uniqueness constraints.
func Validate(doc Document) error {
	if strings.TrimSpace(doc.SchemaVersion) == "" {
		return fmt.Errorf("schema_version is required")
	}
	if doc.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported schema_version %q", doc.SchemaVersion)
	}
	if err := ValidateBasePath(doc.API.BasePath); err != nil {
		return err
	}
	if strings.TrimSpace(doc.Info.Title) == "" {
		return fmt.Errorf("info.title is required")
	}
	if strings.TrimSpace(doc.Info.Version) == "" {
		return fmt.Errorf("info.version is required")
	}
	if len(doc.Endpoints) == 0 && len(doc.Contracts) == 0 {
		return fmt.Errorf("at least one endpoint or contract is required")
	}

	seenContract := make(map[string]struct{}, len(doc.Contracts))
	for _, contract := range doc.Contracts {
		if strings.TrimSpace(contract.Name) == "" {
			return fmt.Errorf("contract name is required")
		}
		if _, exists := seenContract[contract.Name]; exists {
			return fmt.Errorf("duplicate contract name %q", contract.Name)
		}
		seenContract[contract.Name] = struct{}{}
		if err := validateSchemaRefExists(doc, contract.Schema, fmt.Sprintf("contract %q schema", contract.Name)); err != nil {
			return err
		}
		if err := validateGenericExtensions(contract.Extensions, fmt.Sprintf("contract %q", contract.Name)); err != nil {
			return err
		}
	}

	seenOperation := make(map[string]struct{}, len(doc.Endpoints))
	seenRoute := make(map[string]struct{}, len(doc.Endpoints))
	seenTool := make(map[string]string, len(doc.Endpoints))
	seenCLI := make(map[string]string, len(doc.Endpoints))
	seenUIAction := make(map[string]string, len(doc.Endpoints))
	commandPaths := make(map[string][]string, len(doc.Endpoints))
	for _, endpoint := range doc.Endpoints {
		if strings.TrimSpace(endpoint.Method) == "" {
			return fmt.Errorf("endpoint method is required")
		}
		if strings.TrimSpace(endpoint.Path) == "" {
			return fmt.Errorf("endpoint path is required")
		}
		if !strings.HasPrefix(strings.TrimSpace(endpoint.Path), "/") {
			return fmt.Errorf("endpoint %q path must start with \"/\"", endpoint.OperationID)
		}
		if strings.TrimSpace(endpoint.OperationID) == "" {
			return fmt.Errorf("endpoint operation_id is required")
		}
		switch endpoint.Kind {
		case "", "query":
			if endpoint.Kind == "query" && endpoint.Command != nil {
				return fmt.Errorf("endpoint %q kind query cannot declare a command contract", endpoint.OperationID)
			}
		case "command":
			if endpoint.Command == nil {
				return fmt.Errorf("endpoint %q kind command requires a command contract", endpoint.OperationID)
			}
		default:
			return fmt.Errorf("endpoint %q has unsupported kind %q", endpoint.OperationID, endpoint.Kind)
		}
		if err := validateEndpointExtensions(endpoint.Extensions, fmt.Sprintf("endpoint %q", endpoint.OperationID)); err != nil {
			return err
		}
		if endpoint.Command != nil {
			if err := validateCommand(doc, endpoint); err != nil {
				return err
			}
			if endpoint.Command.UI != nil {
				actionID := endpoint.Command.UI.ActionID
				if operationID, exists := seenUIAction[actionID]; exists {
					return fmt.Errorf("duplicate ui action_id %q for operations %q and %q", actionID, operationID, endpoint.OperationID)
				}
				seenUIAction[actionID] = endpoint.OperationID
			}
		}
		if endpoint.Tool != nil {
			if err := validateTool(doc, endpoint); err != nil {
				return err
			}
			if operationID, exists := seenTool[endpoint.Tool.Name]; exists {
				return fmt.Errorf("duplicate tool name %q for operations %q and %q", endpoint.Tool.Name, operationID, endpoint.OperationID)
			}
			seenTool[endpoint.Tool.Name] = endpoint.OperationID
		}
		for _, parameter := range endpoint.Parameters {
			if err := validateParameterSchema(doc, endpoint, parameter); err != nil {
				return err
			}
		}
		if endpoint.RequestBody != nil {
			if err := validateRequestBodySchema(doc, endpoint); err != nil {
				return err
			}
		}
		if len(endpoint.Responses) == 0 {
			return fmt.Errorf("endpoint %q must have at least one response", endpoint.OperationID)
		}
		seenResponseStatus := make(map[int]struct{}, len(endpoint.Responses))
		for _, response := range endpoint.Responses {
			if err := validateResponseExtensions(response.Extensions, fmt.Sprintf("endpoint %q response %d", endpoint.OperationID, response.StatusCode)); err != nil {
				return err
			}
			if response.StatusCode <= 0 {
				return fmt.Errorf("endpoint %q has invalid response status_code %d", endpoint.OperationID, response.StatusCode)
			}
			if _, exists := seenResponseStatus[response.StatusCode]; exists {
				return fmt.Errorf("endpoint %q has duplicate response status_code %d", endpoint.OperationID, response.StatusCode)
			}
			seenResponseStatus[response.StatusCode] = struct{}{}
			if strings.TrimSpace(response.Description) == "" {
				return fmt.Errorf("endpoint %q response %d description is required", endpoint.OperationID, response.StatusCode)
			}
			if shape, ok, err := ResponseShapeMetadata(response); err != nil {
				return fmt.Errorf("endpoint %q response %d shape metadata: %w", endpoint.OperationID, response.StatusCode, err)
			} else if ok {
				switch shape.Kind {
				case "wrapped_json":
					if shape.BodyType == "" {
						return fmt.Errorf("endpoint %q response %d wrapped_json body_type is required", endpoint.OperationID, response.StatusCode)
					}
				default:
					return fmt.Errorf("endpoint %q response %d has unsupported shape kind %q", endpoint.OperationID, response.StatusCode, shape.Kind)
				}
			}
			seenHeaders := make(map[string]struct{}, len(response.Headers))
			for _, header := range response.Headers {
				name := strings.TrimSpace(header.Name)
				if name == "" {
					return fmt.Errorf("endpoint %q response %d header name is required", endpoint.OperationID, response.StatusCode)
				}
				if err := validateSchemaRefExists(doc, header.Schema, fmt.Sprintf("endpoint %q response %d header %q", endpoint.OperationID, response.StatusCode, header.Name)); err != nil {
					return err
				}
				if _, exists := seenHeaders[strings.ToLower(name)]; exists {
					return fmt.Errorf("endpoint %q response %d has duplicate header %q", endpoint.OperationID, response.StatusCode, header.Name)
				}
				seenHeaders[strings.ToLower(name)] = struct{}{}
			}
			if err := validateUniqueContentTypes(response.Contents, fmt.Sprintf("endpoint %q response %d", endpoint.OperationID, response.StatusCode)); err != nil {
				return err
			}
			for idx, content := range response.Contents {
				if err := validateBodyContent(doc, content, fmt.Sprintf("endpoint %q response %d contents[%d]", endpoint.OperationID, response.StatusCode, idx)); err != nil {
					return err
				}
			}
		}

		normalizedCLI, err := normalizeEndpointCLI(doc, endpoint)
		if err != nil {
			return err
		}
		if err := validateEndpointCLI(doc, endpoint, normalizedCLI); err != nil {
			return err
		}
		if normalizedCLI != nil {
			command := CLICommandString(normalizedCLI)
			if existing, ok := seenCLI[command]; ok {
				return fmt.Errorf("duplicate cli.command %q for operations %q and %q", command, existing, endpoint.OperationID)
			}
			for other, otherPath := range commandPaths {
				if commandPathPrefix(normalizedCLI.Command, otherPath) || commandPathPrefix(otherPath, normalizedCLI.Command) {
					return fmt.Errorf("cli.command %q for operation %q conflicts with %q for operation %q", command, endpoint.OperationID, other, seenCLI[other])
				}
			}
			seenCLI[command] = endpoint.OperationID
			commandPaths[command] = append([]string(nil), normalizedCLI.Command...)
		}

		opKey := endpoint.OperationID
		if _, exists := seenOperation[opKey]; exists {
			return fmt.Errorf("duplicate operation_id %q", opKey)
		}
		seenOperation[opKey] = struct{}{}

		routeKey := strings.ToLower(endpoint.Method) + " " + endpoint.Path
		if _, exists := seenRoute[routeKey]; exists {
			return fmt.Errorf("duplicate endpoint route %q", routeKey)
		}
		seenRoute[routeKey] = struct{}{}
	}
	if err := validateExecutionReferences(doc); err != nil {
		return err
	}

	for name, schema := range doc.Schemas {
		if err := validateSchemaDefinition(doc, name, schema); err != nil {
			return err
		}
		if err := validateGenericExtensions(schema.Extensions, fmt.Sprintf("schema %q", name)); err != nil {
			return err
		}
		if len(schema.PropertyOrder) > 0 {
			for _, propertyName := range schema.PropertyOrder {
				if _, ok := schema.Properties[propertyName]; !ok {
					return fmt.Errorf("schema %q property_order references unknown property %q", name, propertyName)
				}
			}
		}
	}
	if err := validateTransportErrors(doc); err != nil {
		return err
	}

	return nil
}

func validateCommand(doc Document, endpoint Endpoint) error {
	command := endpoint.Command
	context := fmt.Sprintf("endpoint %q command", endpoint.OperationID)
	if strings.TrimSpace(command.Owner) == "" {
		return fmt.Errorf("%s owner is required", context)
	}
	action := strings.TrimSpace(command.Audit.SuccessAction)
	if command.Audit.Required && action == "" {
		return fmt.Errorf("%s audit.success_action is required when audit.required is true", context)
	}
	if action != "" && !auditActionPattern.MatchString(action) {
		return fmt.Errorf("%s audit.success_action %q must be a stable dotted lower_snake_case name", context, action)
	}
	if guarantee := strings.TrimSpace(command.Audit.Guarantee); guarantee != "" && guarantee != "transactional" && guarantee != "best-effort" {
		return fmt.Errorf("%s audit has unsupported guarantee %q", context, guarantee)
	}
	if command.Audit.Required && command.Audit.Payload == nil {
		return fmt.Errorf("%s required audit must declare a typed payload", context)
	}
	if payload := command.Audit.Payload; payload != nil {
		name, ok := NormalizedSchemaRefName(payload.Schema)
		if !ok {
			return fmt.Errorf("%s audit payload requires a named schema", context)
		}
		schema, ok := doc.Schemas[name]
		if !ok || schema.Type != "object" {
			return fmt.Errorf("%s audit payload schema %q must be a declared object", context, name)
		}
		if payload.SchemaVersion < 1 {
			return fmt.Errorf("%s audit payload schema_version must be positive", context)
		}
		switch payload.Retention {
		case "short", "standard", "security":
		default:
			return fmt.Errorf("%s audit payload has unsupported retention %q", context, payload.Retention)
		}
		required := make(map[string]struct{}, len(schema.Required))
		for _, field := range schema.Required {
			required[field] = struct{}{}
		}
		if len(required) != len(schema.Properties) {
			return fmt.Errorf("%s audit payload schema %q requires every field to be required", context, name)
		}
		seen := make(map[string]struct{}, len(payload.Fields))
		for _, field := range payload.Fields {
			if _, ok := schema.Properties[field.Name]; !ok {
				return fmt.Errorf("%s audit payload field %q is absent from schema %q", context, field.Name, name)
			}
			switch field.Sensitivity {
			case "public", "internal", "pii", "secret":
			default:
				return fmt.Errorf("%s audit payload field %q has unsupported sensitivity %q", context, field.Name, field.Sensitivity)
			}
			if _, exists := seen[field.Name]; exists {
				return fmt.Errorf("%s audit payload field %q is duplicated", context, field.Name)
			}
			seen[field.Name] = struct{}{}
		}
		if len(seen) != len(schema.Properties) {
			return fmt.Errorf("%s audit payload schema %q requires sensitivity for every field", context, name)
		}
	}
	if command.Failures == nil {
		return fmt.Errorf("%s failures declaration is required; use an empty array when the command has no operation-owned failures", context)
	}
	if execution := command.Execution; execution != nil {
		if execution.Mode != "async" {
			return fmt.Errorf("%s execution has unsupported mode %q", context, execution.Mode)
		}
		if execution.Guarantee != "transactional" {
			return fmt.Errorf("%s async execution requires transactional workflow guarantee", context)
		}
		if !jobKindPattern.MatchString(execution.JobKind) {
			return fmt.Errorf("%s execution job_kind must be a stable lower_snake_case identifier", context)
		}
		if !auditActionPattern.MatchString(execution.InitialEvent) {
			return fmt.Errorf("%s execution initial_event must be a stable dotted lower_snake_case name", context)
		}
		if !stableNamePattern.MatchString(execution.ResourceKind) || !stableNamePattern.MatchString(execution.InitialState) {
			return fmt.Errorf("%s execution resource_kind and initial_state must be stable lower_snake_case names", context)
		}
		if strings.TrimSpace(execution.StatusOperation) == "" || strings.TrimSpace(execution.EventsOperation) == "" || execution.StatusOperation == execution.EventsOperation {
			return fmt.Errorf("%s execution status_operation and events_operation must be distinct operation IDs", context)
		}
		switch execution.Cancellation {
		case "supported", "unsupported":
		default:
			return fmt.Errorf("%s execution has unsupported cancellation policy %q", context, execution.Cancellation)
		}
		hasAccepted := false
		for _, response := range endpoint.Responses {
			if response.StatusCode == 202 {
				hasAccepted = true
				break
			}
		}
		if !hasAccepted {
			return fmt.Errorf("%s async execution requires a 202 response", context)
		}
	}
	statusCodes := make(map[int]struct{}, len(endpoint.Responses))
	for _, response := range endpoint.Responses {
		statusCodes[response.StatusCode] = struct{}{}
	}
	seenFailureKinds := make(map[string]struct{}, len(command.Failures))
	seenFailureCodes := make(map[string]struct{}, len(command.Failures))
	for _, failure := range command.Failures {
		if !stableNamePattern.MatchString(failure.Kind) {
			return fmt.Errorf("%s failure kind %q must be a stable lower_snake_case name", context, failure.Kind)
		}
		if failure.StatusCode < 400 || failure.StatusCode > 599 {
			return fmt.Errorf("%s failure %q has invalid status_code %d", context, failure.Kind, failure.StatusCode)
		}
		if _, ok := statusCodes[failure.StatusCode]; !ok {
			return fmt.Errorf("%s failure %q status_code %d is not documented by the operation", context, failure.Kind, failure.StatusCode)
		}
		if !failureCodePattern.MatchString(failure.Code) {
			return fmt.Errorf("%s failure %q code %q must be stable UPPER_SNAKE_CASE", context, failure.Kind, failure.Code)
		}
		if strings.TrimSpace(failure.PublicDetail) == "" {
			return fmt.Errorf("%s failure %q public_detail is required", context, failure.Kind)
		}
		if _, exists := seenFailureKinds[failure.Kind]; exists {
			return fmt.Errorf("%s has duplicate failure kind %q", context, failure.Kind)
		}
		if _, exists := seenFailureCodes[failure.Code]; exists {
			return fmt.Errorf("%s has duplicate failure code %q", context, failure.Code)
		}
		seenFailureKinds[failure.Kind] = struct{}{}
		seenFailureCodes[failure.Code] = struct{}{}
	}
	seenExposures := make(map[string]struct{}, len(command.AdditionalExposures))
	for _, exposure := range command.AdditionalExposures {
		switch exposure {
		case "ui", "agent", "automation":
		default:
			return fmt.Errorf("%s has unsupported additional exposure %q", context, exposure)
		}
		if _, exists := seenExposures[exposure]; exists {
			return fmt.Errorf("%s has duplicate additional exposure %q", context, exposure)
		}
		seenExposures[exposure] = struct{}{}
	}
	if command.UI != nil {
		actionID := strings.TrimSpace(command.UI.ActionID)
		if !uiActionPattern.MatchString(actionID) {
			return fmt.Errorf("%s ui.action_id %q must be a stable dotted lower-kebab-case name", context, actionID)
		}
		if _, exposed := seenExposures["ui"]; !exposed {
			return fmt.Errorf("%s ui metadata requires the ui additional exposure", context)
		}
	} else if _, exposed := seenExposures["ui"]; exposed {
		return fmt.Errorf("%s ui additional exposure requires ui metadata", context)
	}
	if command.Target != nil {
		if strings.TrimSpace(command.Target.Parameter) == "" || strings.TrimSpace(command.Target.Type) == "" {
			return fmt.Errorf("%s target parameter and type are required", context)
		}
		matched := false
		for _, parameter := range endpoint.Parameters {
			if parameter.In == "path" && parameter.Required && parameter.Name == command.Target.Parameter {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s target parameter %q must name a required path parameter", context, command.Target.Parameter)
		}
	}
	switch command.Idempotency {
	case "", "required":
	default:
		return fmt.Errorf("%s has unsupported idempotency policy %q", context, command.Idempotency)
	}
	switch command.Concurrency {
	case "", "if-match":
	default:
		return fmt.Errorf("%s has unsupported concurrency policy %q", context, command.Concurrency)
	}
	hasRequiredHeader := func(name string) bool {
		for _, parameter := range endpoint.Parameters {
			if parameter.In == "header" && parameter.Required && strings.EqualFold(parameter.Name, name) {
				return true
			}
		}
		return false
	}
	if command.Idempotency == "required" && !hasRequiredHeader("Idempotency-Key") {
		return fmt.Errorf("%s idempotency policy requires a required Idempotency-Key header", context)
	}
	if strings.EqualFold(endpoint.Method, "post") && command.Idempotency != "required" {
		return fmt.Errorf("%s POST commands require idempotency policy %q", context, "required")
	}
	if command.Concurrency == "if-match" && !hasRequiredHeader("If-Match") {
		return fmt.Errorf("%s concurrency policy requires a required If-Match header", context)
	}
	if strings.EqualFold(endpoint.Method, "patch") && command.Concurrency != "if-match" {
		return fmt.Errorf("%s PATCH commands require concurrency policy %q", context, "if-match")
	}
	if command.Privilege != "" && command.AuthzMode != "privilege" {
		return fmt.Errorf("%s privilege requires authz_mode %q", context, "privilege")
	}
	if authz, ok := endpoint.Extensions["x-authz"].(map[string]any); ok {
		if mode, _ := authz["mode"].(string); command.AuthzMode != mode {
			return fmt.Errorf("%s authz_mode %q does not match x-authz mode %q", context, command.AuthzMode, mode)
		}
		if privilege, _ := authz["privilege"].(string); command.Privilege != privilege {
			return fmt.Errorf("%s privilege %q does not match x-authz privilege %q", context, command.Privilege, privilege)
		}
	}
	return nil
}

func validateExecutionReferences(doc Document) error {
	operations := make(map[string]Endpoint, len(doc.Endpoints))
	for _, endpoint := range doc.Endpoints {
		operations[endpoint.OperationID] = endpoint
	}
	for _, endpoint := range doc.Endpoints {
		if endpoint.Command == nil || endpoint.Command.Execution == nil {
			continue
		}
		execution := endpoint.Command.Execution
		for label, operationID := range map[string]string{
			"status_operation": execution.StatusOperation,
			"events_operation": execution.EventsOperation,
		} {
			referenced, ok := operations[operationID]
			if !ok {
				return fmt.Errorf("endpoint %q command execution %s references unknown operation %q", endpoint.OperationID, label, operationID)
			}
			if referenced.Kind != "query" || referenced.Command != nil || !strings.EqualFold(referenced.Method, "get") {
				return fmt.Errorf("endpoint %q command execution %s must reference a GET query", endpoint.OperationID, label)
			}
		}
	}
	return nil
}

func validateEndpointExtensions(extensions map[string]any, context string) error {
	for key, value := range extensions {
		if !strings.HasPrefix(key, "x-") {
			return fmt.Errorf("%s extension %q must start with \"x-\"", context, key)
		}
		switch key {
		case "x-authz":
			if err := validateAuthzExtension(value); err != nil {
				return fmt.Errorf("%s extension %q: %w", context, key, err)
			}
		case "x-apigen-manual":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s extension %q: x-apigen-manual must be boolean", context, key)
			}
		default:
			if key == "x-agent" {
				return fmt.Errorf("%s extension %q is reserved; use endpoint.tool", context, key)
			}
			if strings.HasPrefix(key, "x-apigen-") {
				return fmt.Errorf("%s unsupported APIGen-owned extension %q", context, key)
			}
		}
		if err := validateJSONCompatibleExtensionValue(value); err != nil {
			return fmt.Errorf("%s extension %q: %w", context, key, err)
		}
	}
	return nil
}

func validateTool(doc Document, endpoint Endpoint) error {
	tool := endpoint.Tool
	context := fmt.Sprintf("endpoint %q tool", endpoint.OperationID)
	if !toolNamePattern.MatchString(tool.Name) {
		return fmt.Errorf("%s name %q must match %s", context, tool.Name, toolNamePattern.String())
	}
	switch tool.Effect {
	case "read", "idempotent-write", "write", "destructive":
	default:
		return fmt.Errorf("%s has unsupported effect %q", context, tool.Effect)
	}
	confirmation := tool.Confirmation
	if confirmation == "" {
		confirmation = defaultToolConfirmation(tool.Effect)
	}
	switch confirmation {
	case "never", "policy", "always":
	default:
		return fmt.Errorf("%s has unsupported confirmation %q", context, confirmation)
	}
	minimum := defaultToolConfirmation(tool.Effect)
	if confirmationStrength(confirmation) < confirmationStrength(minimum) {
		return fmt.Errorf("%s confirmation %q is weaker than required %q for effect %q", context, confirmation, minimum, tool.Effect)
	}
	if err := validateGenericExtensions(tool.Metadata, context+" metadata"); err != nil {
		return err
	}
	if err := validateToolInput(doc, endpoint); err != nil {
		return err
	}
	return validateToolOutput(doc, endpoint)
}

func defaultToolConfirmation(effect string) string {
	switch effect {
	case "read":
		return "never"
	case "destructive":
		return "always"
	default:
		return "policy"
	}
}

func confirmationStrength(value string) int {
	switch value {
	case "always":
		return 2
	case "policy":
		return 1
	default:
		return 0
	}
}

type toolInputSource struct {
	Schema      SchemaRef
	Required    bool
	Description string
}

func validateToolInput(doc Document, endpoint Endpoint) error {
	context := fmt.Sprintf("endpoint %q tool input", endpoint.OperationID)
	sources := make(map[string]toolInputSource, len(endpoint.Parameters))
	for _, parameter := range endpoint.Parameters {
		sources[parameter.In+"\x00"+parameter.Name] = toolInputSource{Schema: parameter.Schema, Required: parameter.Required, Description: parameter.Description}
	}
	if endpoint.RequestBody != nil {
		if len(endpoint.RequestBody.Contents) != 1 || endpoint.RequestBody.Contents[0].BodyKind != "json" || endpoint.RequestBody.Contents[0].Schema == nil {
			return fmt.Errorf("%s requires exactly one JSON request body shape", context)
		}
		bodyRef := *endpoint.RequestBody.Contents[0].Schema
		if bodySchema, ok := concreteSchema(doc, bodyRef); ok && bodySchema.Type == "object" {
			required := stringSet(bodySchema.Required)
			for name, property := range bodySchema.Properties {
				sources["body\x00"+name] = toolInputSource{Schema: property.Schema, Required: required[name], Description: property.Description}
			}
		} else {
			sources["body\x00$"] = toolInputSource{Schema: bodyRef, Required: endpoint.RequestBody.Required}
		}
	}

	overrides := map[string]ToolInputField{}
	if endpoint.Tool.Input != nil {
		for _, field := range endpoint.Tool.Input.Fields {
			key := field.Source + "\x00" + field.Name
			if _, exists := overrides[key]; exists {
				return fmt.Errorf("%s has duplicate field override %s.%s", context, field.Source, field.Name)
			}
			source, exists := sources[key]
			if !exists {
				return fmt.Errorf("%s field %s.%s does not exist on the endpoint", context, field.Source, field.Name)
			}
			mode := field.Mode
			if mode == "" {
				mode = "model"
			}
			switch mode {
			case "model":
				if field.ContextKey != "" {
					return fmt.Errorf("%s field %s.%s model mode cannot set context_key", context, field.Source, field.Name)
				}
			case "context":
				if strings.TrimSpace(field.ContextKey) == "" {
					return fmt.Errorf("%s field %s.%s context mode requires context_key", context, field.Source, field.Name)
				}
				if field.Alias != "" || field.Default != nil {
					return fmt.Errorf("%s field %s.%s context mode cannot set alias or default", context, field.Source, field.Name)
				}
			case "omit":
				if source.Required && field.Default == nil {
					return fmt.Errorf("%s field %s.%s is required and omitted without a default", context, field.Source, field.Name)
				}
				if field.Alias != "" || field.ContextKey != "" {
					return fmt.Errorf("%s field %s.%s omit mode cannot set alias or context_key", context, field.Source, field.Name)
				}
			default:
				return fmt.Errorf("%s field %s.%s has unsupported mode %q", context, field.Source, field.Name, field.Mode)
			}
			if field.Default != nil {
				if err := validateJSONCompatibleExtensionValue(field.Default); err != nil {
					return fmt.Errorf("%s field %s.%s default: %w", context, field.Source, field.Name, err)
				}
				if !toolValueMatchesSchema(doc, field.Default, source.Schema) {
					return fmt.Errorf("%s field %s.%s default does not match its schema", context, field.Source, field.Name)
				}
			}
			overrides[key] = field
		}
	}

	arguments := map[string]string{}
	for key := range sources {
		field, hasOverride := overrides[key]
		mode := field.Mode
		if mode == "" {
			mode = "model"
		}
		if hasOverride && mode != "model" {
			continue
		}
		name := field.Alias
		if name == "" {
			_, name, _ = strings.Cut(key, "\x00")
			if name == "$" {
				name = "body"
			}
		}
		if previous, exists := arguments[name]; exists {
			return fmt.Errorf("%s argument %q collides between %s and %s; add an alias", context, name, previous, strings.ReplaceAll(key, "\x00", "."))
		}
		arguments[name] = strings.ReplaceAll(key, "\x00", ".")
	}
	return nil
}

func validateToolOutput(doc Document, endpoint Endpoint) error {
	output := endpoint.Tool.Output
	context := fmt.Sprintf("endpoint %q tool output", endpoint.OperationID)
	successRef, hasBody, err := compatibleToolSuccessSchema(endpoint)
	if err != nil {
		return fmt.Errorf("%s: %w", context, err)
	}
	switch output.Mode {
	case "empty":
		if hasBody {
			return fmt.Errorf("%s mode empty requires bodyless success responses", context)
		}
	case "raw":
		if len(output.Select) > 0 || output.Cursor != nil {
			return fmt.Errorf("%s mode raw cannot declare select or cursor", context)
		}
	case "project":
		if !hasBody {
			return fmt.Errorf("%s mode project requires a JSON success response", context)
		}
		if len(output.Select) == 0 {
			return fmt.Errorf("%s mode project requires select", context)
		}
		targets := map[string]struct{}{}
		for i, projection := range output.Select {
			if err := validateToolProjection(doc, successRef, projection, targets, fmt.Sprintf("%s select[%d]", context, i)); err != nil {
				return err
			}
		}
		if output.Cursor != nil {
			if _, err := resolveToolPointer(doc, successRef, output.Cursor.Source); err != nil {
				return fmt.Errorf("%s cursor: %w", context, err)
			}
			for _, target := range []string{defaultString(output.Cursor.Target, "nextCursor"), defaultString(output.Cursor.HasMoreTarget, "hasMore")} {
				if _, exists := targets[target]; exists {
					return fmt.Errorf("%s cursor target %q collides with projected output", context, target)
				}
				targets[target] = struct{}{}
			}
		}
	default:
		return fmt.Errorf("%s has unsupported mode %q", context, output.Mode)
	}
	return nil
}

func compatibleToolSuccessSchema(endpoint Endpoint) (SchemaRef, bool, error) {
	ref, _, found, err := ToolSuccessSchema(endpoint)
	return ref, found, err
}

func validateToolProjection(doc Document, scope SchemaRef, projection ToolProjection, targets map[string]struct{}, context string) error {
	selected, err := resolveToolPointer(doc, scope, projection.Source)
	if err != nil {
		return fmt.Errorf("%s: %w", context, err)
	}
	target := projection.Target
	if target == "" {
		segments, _ := toolPointerSegments(projection.Source)
		if len(segments) == 0 {
			return fmt.Errorf("%s target is required for the root pointer", context)
		}
		target = segments[len(segments)-1]
	}
	if _, exists := targets[target]; exists {
		return fmt.Errorf("%s target %q is duplicated", context, target)
	}
	targets[target] = struct{}{}
	childScope, kind := toolProjectionChildScope(doc, selected)
	if projection.CountAs != "" {
		if kind != "array" && kind != "map" {
			return fmt.Errorf("%s count_as requires an array or map source", context)
		}
		if _, exists := targets[projection.CountAs]; exists {
			return fmt.Errorf("%s count target %q is duplicated", context, projection.CountAs)
		}
		targets[projection.CountAs] = struct{}{}
	}
	if len(projection.Select) > 0 {
		if kind != "object" && kind != "array" && kind != "map" {
			return fmt.Errorf("%s select requires an object, array, or map source", context)
		}
		childTargets := map[string]struct{}{}
		for i, child := range projection.Select {
			if err := validateToolProjection(doc, childScope, child, childTargets, fmt.Sprintf("%s select[%d]", context, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveToolPointer(doc Document, scope SchemaRef, pointer string) (SchemaRef, error) {
	resolved, _, err := ResolveSchemaPointer(doc, scope, pointer)
	return resolved, err
}

func toolPointerSegments(pointer string) ([]string, error) {
	return JSONPointerSegments(pointer)
}

func toolProjectionChildScope(doc Document, ref SchemaRef) (SchemaRef, string) {
	kind, child := SchemaProjectionKind(doc, ref)
	return child, kind
}

func concreteSchema(doc Document, ref SchemaRef) (Schema, bool) {
	if ref.Ref != "" {
		return ResolveSchema(doc, ref)
	}
	if ref.Type == "" {
		return Schema{}, false
	}
	return Schema{Type: ref.Type, Items: ref.Items}, true
}

func toolValueMatchesSchema(doc Document, value any, ref SchemaRef) bool {
	typeName := ref.Type
	if schema, ok := concreteSchema(doc, ref); ok {
		typeName = schema.Type
	}
	switch typeName {
	case "string":
		_, ok := value.(string)
		return ok
	case "integer", "number":
		switch value.(type) {
		case int, int32, int64, uint, uint32, uint64, float32, float64, json.Number:
			return true
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return true
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func validateResponseExtensions(extensions map[string]any, context string) error {
	for key, value := range extensions {
		if !strings.HasPrefix(key, "x-") {
			return fmt.Errorf("%s extension %q must start with \"x-\"", context, key)
		}
		if key != ResponseShapeExtensionKey {
			if strings.HasPrefix(key, "x-apigen-") {
				return fmt.Errorf("%s unsupported APIGen-owned extension %q", context, key)
			}
			return fmt.Errorf("%s unsupported response extension %q", context, key)
		}
		if err := validateJSONCompatibleExtensionValue(value); err != nil {
			return fmt.Errorf("%s extension %q: %w", context, key, err)
		}
	}
	return nil
}

func validateGenericExtensions(extensions map[string]any, context string) error {
	for key, value := range extensions {
		if !strings.HasPrefix(key, "x-") {
			return fmt.Errorf("%s extension %q must start with \"x-\"", context, key)
		}
		if strings.HasPrefix(key, "x-apigen-") {
			return fmt.Errorf("%s unsupported APIGen-owned extension %q", context, key)
		}
		if err := validateJSONCompatibleExtensionValue(value); err != nil {
			return fmt.Errorf("%s extension %q: %w", context, key, err)
		}
	}
	return nil
}

func validateAuthzExtension(value any) error {
	extension, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("x-authz must be an object")
	}
	mode, ok := extension["mode"]
	if !ok {
		return fmt.Errorf("x-authz.mode is required")
	}
	if _, ok := mode.(string); !ok {
		return fmt.Errorf("x-authz.mode must be string")
	}
	return nil
}

func validateJSONCompatibleExtensionValue(value any) error {
	switch typed := value.(type) {
	case nil, string, bool:
		return nil
	case float64:
		if math.IsInf(typed, 0) || math.IsNaN(typed) {
			return fmt.Errorf("number must be finite")
		}
		return nil
	case float32:
		if math.IsInf(float64(typed), 0) || math.IsNaN(float64(typed)) {
			return fmt.Errorf("number must be finite")
		}
		return nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nil
	case []any:
		for _, item := range typed {
			if err := validateJSONCompatibleExtensionValue(item); err != nil {
				return err
			}
		}
		return nil
	case []string:
		return nil
	case map[string]any:
		for _, item := range typed {
			if err := validateJSONCompatibleExtensionValue(item); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported JSON value type %T", value)
	}
}

func validateParameterSchema(doc Document, endpoint Endpoint, parameter Parameter) error {
	if parameter.In == "" {
		return fmt.Errorf("endpoint %q parameter %q location is required", endpoint.OperationID, parameter.Name)
	}
	switch parameter.In {
	case "path", "query", "header":
	default:
		return fmt.Errorf("endpoint %q parameter %q has unsupported parameter location %q", endpoint.OperationID, parameter.Name, parameter.In)
	}

	schemaType, format, err := resolvedParameterSchemaType(doc, parameter.Schema, fmt.Sprintf("endpoint %q parameter %q", endpoint.OperationID, parameter.Name))
	if err != nil {
		return err
	}

	switch schemaType {
	case "string":
		if format == "date-time" || format == "" {
			return nil
		}
		return nil
	case "array":
		if parameter.In != "query" {
			return fmt.Errorf("endpoint %q parameter %q arrays are only supported in query parameters", endpoint.OperationID, parameter.Name)
		}
		itemType, itemFormat, err := resolvedParameterArrayItemType(doc, parameter.Schema, fmt.Sprintf("endpoint %q parameter %q", endpoint.OperationID, parameter.Name))
		if err != nil {
			return err
		}
		switch itemType {
		case "string":
			if itemFormat == "" || itemFormat == "date-time" {
				return nil
			}
			return nil
		case "boolean", "bool":
			return nil
		case "integer":
			switch itemFormat {
			case "", "int32", "int64":
				return nil
			default:
				return fmt.Errorf("endpoint %q parameter %q has unsupported array item integer format %q", endpoint.OperationID, parameter.Name, itemFormat)
			}
		default:
			return fmt.Errorf("endpoint %q parameter %q has unsupported array item schema type %q", endpoint.OperationID, parameter.Name, itemType)
		}
	case "boolean", "bool":
		return nil
	case "integer":
		switch format {
		case "", "int32", "int64":
			return nil
		default:
			return fmt.Errorf("endpoint %q parameter %q has unsupported integer format %q", endpoint.OperationID, parameter.Name, format)
		}
	default:
		return fmt.Errorf("endpoint %q parameter %q has unsupported schema type %q", endpoint.OperationID, parameter.Name, schemaType)
	}
}

func validateRequestBodySchema(doc Document, endpoint Endpoint) error {
	if endpoint.RequestBody == nil {
		return nil
	}
	if len(endpoint.RequestBody.Contents) == 0 {
		return fmt.Errorf("endpoint %q request_body must declare at least one content", endpoint.OperationID)
	}
	if err := validateUniqueContentTypes(endpoint.RequestBody.Contents, fmt.Sprintf("endpoint %q request_body", endpoint.OperationID)); err != nil {
		return err
	}
	for idx, content := range endpoint.RequestBody.Contents {
		if err := validateBodyContent(doc, content, fmt.Sprintf("endpoint %q request_body contents[%d]", endpoint.OperationID, idx)); err != nil {
			return err
		}
	}
	return nil
}

func validateUniqueContentTypes(contents []BodyContent, context string) error {
	seen := make(map[string]struct{}, len(contents))
	for idx, content := range contents {
		key := strings.ToLower(strings.TrimSpace(content.ContentType))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s has duplicate content_type %q at contents[%d]", context, content.ContentType, idx)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateBodyContent(doc Document, content BodyContent, context string) error {
	if strings.TrimSpace(content.ContentType) == "" {
		return fmt.Errorf("%s content_type is required", context)
	}
	switch content.BodyKind {
	case "json", "text", "binary", "file", "form_urlencoded", "multipart":
	default:
		return fmt.Errorf("%s has unsupported body_kind %q", context, content.BodyKind)
	}
	if content.Schema != nil {
		if err := validateSchemaRefExists(doc, *content.Schema, context+" schema"); err != nil {
			return err
		}
	}
	for idx, schemaRef := range content.AnyOf {
		if err := validateSchemaRefExists(doc, schemaRef, fmt.Sprintf("%s any_of[%d]", context, idx)); err != nil {
			return err
		}
	}
	for idx, part := range content.Parts {
		if strings.TrimSpace(part.Name) == "" {
			return fmt.Errorf("%s parts[%d] name is required", context, idx)
		}
		switch strings.TrimSpace(part.PartKind) {
		case "", "model", "tuple":
		default:
			return fmt.Errorf("%s parts[%d] has unsupported part_kind %q", context, idx, part.PartKind)
		}
		if strings.TrimSpace(part.WireName) == "" && part.PartKind == "model" {
			return fmt.Errorf("%s parts[%d] model part wire_name is required", context, idx)
		}
		if strings.TrimSpace(part.BodyKind) == "" {
			return fmt.Errorf("%s parts[%d] body_kind is required", context, idx)
		}
		switch part.BodyKind {
		case "json", "text", "binary", "file":
		default:
			return fmt.Errorf("%s parts[%d] has unsupported body_kind %q", context, idx, part.BodyKind)
		}
		if part.Filename && part.BodyKind != "file" {
			return fmt.Errorf("%s parts[%d] filename metadata requires body_kind file", context, idx)
		}
		if part.Schema != nil {
			if err := validateSchemaRefExists(doc, *part.Schema, fmt.Sprintf("%s parts[%d] schema", context, idx)); err != nil {
				return err
			}
		}
	}
	switch content.BodyKind {
	case "multipart":
		if len(content.Parts) == 0 {
			return fmt.Errorf("%s multipart content must declare parts", context)
		}
	case "json", "text", "binary", "file", "form_urlencoded":
		if content.Schema == nil && len(content.AnyOf) == 0 {
			return fmt.Errorf("%s %s content must declare schema or any_of", context, content.BodyKind)
		}
	}
	return nil
}

func validateSchemaDefinition(doc Document, name string, schema Schema) error {
	if strings.TrimSpace(schema.Type) == "" {
		return fmt.Errorf("schema %q type is required", name)
	}
	for propertyName, property := range schema.Properties {
		if err := validateSchemaRefExists(doc, property.Schema, fmt.Sprintf("schema %q property %q", name, propertyName)); err != nil {
			return err
		}
		if err := validateGenericExtensions(property.Extensions, fmt.Sprintf("schema %q property %q", name, propertyName)); err != nil {
			return err
		}
	}
	if schema.Items != nil {
		if err := validateSchemaRefExists(doc, *schema.Items, fmt.Sprintf("schema %q items", name)); err != nil {
			return err
		}
	}
	if schema.Base != nil {
		if err := validateSchemaRefExists(doc, *schema.Base, fmt.Sprintf("schema %q base", name)); err != nil {
			return err
		}
	}
	for idx, variant := range schema.OneOf {
		if err := validateSchemaRefExists(doc, variant, fmt.Sprintf("schema %q one_of[%d]", name, idx)); err != nil {
			return err
		}
	}
	if schema.Type == "union" {
		if len(schema.OneOf) == 0 {
			return fmt.Errorf("schema %q union must declare one_of", name)
		}
		if schema.Discriminator == nil {
			seenVariants := make(map[string]struct{}, len(schema.OneOf))
			scalarCount := 0
			objectCount := 0
			for idx, variant := range schema.OneOf {
				if isScalarUnionVariant(variant) {
					scalarCount++
				} else if variant.Ref == "" {
					return fmt.Errorf("schema %q union one_of[%d] must be an inline scalar when discriminator is omitted", name, idx)
				} else {
					objectCount++
					variantName, ok := NormalizedSchemaRefName(variant)
					if !ok {
						return fmt.Errorf("schema %q union one_of[%d] object branch must be a named schema ref", name, idx)
					}
					variantSchema, ok := doc.Schemas[variantName]
					if !ok || variantSchema.Type != "object" {
						return fmt.Errorf("schema %q union one_of[%d] object branch %q must reference an object schema", name, idx, variantName)
					}
				}
				key := variant.Ref
				if key == "" {
					key = fmt.Sprintf("%s:%s:%v", variant.Type, variant.Format, variant.Enum)
				}
				if _, exists := seenVariants[key]; exists {
					return fmt.Errorf("schema %q union has duplicate one_of variant %q", name, key)
				}
				seenVariants[key] = struct{}{}
			}
			// Pure scalar unions retain the existing untagged scalar behavior. A
			// mixed scalar/object union is intentionally narrower: compact authored
			// references have exactly one scalar and one closed object branch.
			if objectCount == 0 {
				if scalarCount == 0 {
					return fmt.Errorf("schema %q union one_of[0] must be an inline scalar when discriminator is omitted", name)
				}
				return nil
			}
			if scalarCount != 1 || objectCount != 1 {
				return fmt.Errorf("schema %q untagged union must contain exactly one scalar branch and exactly one object branch", name)
			}
			return nil
		}
		seenVariants := make(map[string]struct{}, len(schema.OneOf))
		for idx, variant := range schema.OneOf {
			variantName, ok := NormalizedSchemaRefName(variant)
			if !ok {
				return fmt.Errorf("schema %q union one_of[%d] must be a named schema ref", name, idx)
			}
			if _, exists := seenVariants[variantName]; exists {
				return fmt.Errorf("schema %q union has duplicate one_of variant %q", name, variantName)
			}
			seenVariants[variantName] = struct{}{}
		}
	}
	if schema.Discriminator != nil {
		if strings.TrimSpace(schema.Discriminator.PropertyName) == "" {
			return fmt.Errorf("schema %q discriminator property_name is required", name)
		}
		if len(schema.Discriminator.Mapping) == 0 {
			return fmt.Errorf("schema %q discriminator mapping is required", name)
		}
		variants := make(map[string]struct{}, len(schema.OneOf))
		for _, variant := range schema.OneOf {
			if variantName, ok := NormalizedSchemaRefName(variant); ok {
				variants[variantName] = struct{}{}
			}
		}
		mappedTargets := make(map[string]struct{}, len(schema.Discriminator.Mapping))
		for value, target := range schema.Discriminator.Mapping {
			if strings.TrimSpace(value) == "" || strings.TrimSpace(target) == "" {
				return fmt.Errorf("schema %q discriminator mapping keys and targets are required", name)
			}
			if _, ok := doc.Schemas[target]; !ok {
				return fmt.Errorf("schema %q discriminator mapping %q references unknown schema %q", name, value, target)
			}
			if schema.Type == "union" {
				if _, ok := variants[target]; !ok {
					return fmt.Errorf("schema %q discriminator mapping %q target %q is not in one_of", name, value, target)
				}
				if _, exists := mappedTargets[target]; exists {
					return fmt.Errorf("schema %q discriminator maps multiple values to variant %q", name, target)
				}
				mappedTargets[target] = struct{}{}
				variantSchema := doc.Schemas[target]
				property, ok := variantSchema.Properties[schema.Discriminator.PropertyName]
				if !ok || len(property.Schema.Enum) != 1 || property.Schema.Enum[0] != value {
					return fmt.Errorf("schema %q discriminator mapping %q target %q must declare matching literal property %q", name, value, target, schema.Discriminator.PropertyName)
				}
				if !containsString(variantSchema.Required, schema.Discriminator.PropertyName) {
					return fmt.Errorf("schema %q discriminator mapping %q target %q must require property %q", name, value, target, schema.Discriminator.PropertyName)
				}
			}
		}
		if schema.Type == "union" && len(mappedTargets) != len(variants) {
			return fmt.Errorf("schema %q discriminator mapping must cover every one_of variant", name)
		}
	}
	return nil
}

func isScalarUnionVariant(variant SchemaRef) bool {
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

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func validateTransportErrors(doc Document) error {
	policy := doc.TransportErrors
	if policy == nil {
		return nil
	}
	if err := validateSchemaRefExists(doc, policy.Schema, "transport_errors schema"); err != nil {
		return err
	}
	policySchemaName, ok := NormalizedSchemaRefName(policy.Schema)
	if !ok {
		return fmt.Errorf("transport_errors schema must be a named schema ref")
	}
	if strings.TrimSpace(policy.ContentType) == "" {
		return fmt.Errorf("transport_errors content_type is required")
	}
	policyMediaType, _, err := mime.ParseMediaType(policy.ContentType)
	if err != nil {
		return fmt.Errorf("transport_errors content_type is invalid: %w", err)
	}
	if len(policy.Failures) == 0 {
		return fmt.Errorf("transport_errors failures are required")
	}
	for kind, failure := range policy.Failures {
		if strings.TrimSpace(kind) == "" {
			return fmt.Errorf("transport_errors failure kind is required")
		}
		if failure.StatusCode < 400 || failure.StatusCode > 599 {
			return fmt.Errorf("transport_errors failure %q has invalid status_code %d", kind, failure.StatusCode)
		}
		if strings.TrimSpace(failure.Code) == "" {
			return fmt.Errorf("transport_errors failure %q code is required", kind)
		}
		if strings.TrimSpace(failure.PublicDetail) == "" {
			return fmt.Errorf("transport_errors failure %q public_detail is required", kind)
		}
	}
	for _, endpoint := range doc.Endpoints {
		for _, response := range endpoint.Responses {
			if !transportStatusConfigured(policy.Failures, response.StatusCode) {
				continue
			}
			matched := false
			for _, content := range response.Contents {
				mediaType, _, err := mime.ParseMediaType(content.ContentType)
				if err != nil || !strings.EqualFold(mediaType, policyMediaType) || content.Schema == nil {
					continue
				}
				responseSchemaName, ok := NormalizedSchemaRefName(*content.Schema)
				if ok && responseSchemaName == policySchemaName {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("endpoint %q response %d conflicts with transport_errors schema %q and content_type %q", endpoint.OperationID, response.StatusCode, policySchemaName, policy.ContentType)
			}
		}
	}
	return nil
}

func transportStatusConfigured(failures map[string]TransportFailure, statusCode int) bool {
	for _, failure := range failures {
		if failure.StatusCode == statusCode {
			return true
		}
	}
	return false
}

func validateSchemaRefExists(doc Document, schemaRef SchemaRef, context string) error {
	if schemaRef.Ref != "" {
		name, ok := NormalizedSchemaRefName(schemaRef)
		if !ok {
			return fmt.Errorf("%s has invalid schema ref %q", context, schemaRef.Ref)
		}
		if _, ok := doc.Schemas[name]; !ok {
			return fmt.Errorf("%s references unknown schema %q", context, name)
		}
	}
	if schemaRef.Items != nil {
		if err := validateSchemaRefExists(doc, *schemaRef.Items, context+" items"); err != nil {
			return err
		}
	}
	for idx, value := range schemaRef.Enum {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s enum[%d] is required", context, idx)
		}
	}
	if schemaRef.Minimum != nil && (math.IsNaN(*schemaRef.Minimum) || math.IsInf(*schemaRef.Minimum, 0)) || schemaRef.Maximum != nil && (math.IsNaN(*schemaRef.Maximum) || math.IsInf(*schemaRef.Maximum, 0)) {
		return fmt.Errorf("%s numeric bounds must be finite", context)
	}
	if schemaRef.Minimum != nil && schemaRef.Maximum != nil && *schemaRef.Minimum > *schemaRef.Maximum {
		return fmt.Errorf("%s minimum must not exceed maximum", context)
	}
	if schemaRef.MinLength != nil && *schemaRef.MinLength < 0 || schemaRef.MaxLength != nil && *schemaRef.MaxLength < 0 {
		return fmt.Errorf("%s length bounds must be non-negative", context)
	}
	if schemaRef.MinLength != nil && schemaRef.MaxLength != nil && *schemaRef.MinLength > *schemaRef.MaxLength {
		return fmt.Errorf("%s min_length must not exceed max_length", context)
	}
	if schemaRef.AdditionalProperties != nil && schemaRef.AdditionalProperties.Schema != nil {
		if err := validateSchemaRefExists(doc, *schemaRef.AdditionalProperties.Schema, context+" additional_properties"); err != nil {
			return err
		}
	}
	return nil
}

func resolvedParameterSchemaType(doc Document, schemaRef SchemaRef, context string) (string, string, error) {
	if schemaRef.Ref != "" {
		schema, ok := ResolveSchema(doc, schemaRef)
		if !ok {
			name, _ := NormalizedSchemaRefName(schemaRef)
			return "", "", fmt.Errorf("%s references unknown schema %q", context, name)
		}
		return strings.ToLower(strings.TrimSpace(schema.Type)), "", nil
	}
	return strings.ToLower(strings.TrimSpace(schemaRef.Type)), strings.ToLower(strings.TrimSpace(schemaRef.Format)), nil
}

func resolvedParameterArrayItemType(doc Document, schemaRef SchemaRef, context string) (string, string, error) {
	if schemaRef.Ref != "" {
		schema, ok := ResolveSchema(doc, schemaRef)
		if !ok {
			name, _ := NormalizedSchemaRefName(schemaRef)
			return "", "", fmt.Errorf("%s references unknown schema %q", context, name)
		}
		if schema.Items == nil {
			return "", "", fmt.Errorf("%s array schema must declare items", context)
		}
		return resolvedParameterSchemaType(doc, *schema.Items, context+" items")
	}
	if schemaRef.Items == nil {
		return "", "", fmt.Errorf("%s array schema must declare items", context)
	}
	return resolvedParameterSchemaType(doc, *schemaRef.Items, context+" items")
}

// Normalize applies deterministic ordering for generation.
func Normalize(doc *Document) error {
	sort.Slice(doc.Contracts, func(i, j int) bool {
		return doc.Contracts[i].Name < doc.Contracts[j].Name
	})
	sort.Slice(doc.Endpoints, func(i, j int) bool {
		if doc.Endpoints[i].Path == doc.Endpoints[j].Path {
			return strings.ToLower(doc.Endpoints[i].Method) < strings.ToLower(doc.Endpoints[j].Method)
		}
		return doc.Endpoints[i].Path < doc.Endpoints[j].Path
	})
	for i := range doc.Endpoints {
		if doc.Endpoints[i].Kind == "" {
			if doc.Endpoints[i].Command != nil {
				doc.Endpoints[i].Kind = "command"
			} else {
				doc.Endpoints[i].Kind = "query"
			}
		}
		if command := doc.Endpoints[i].Command; command != nil {
			sort.Strings(command.AdditionalExposures)
			sort.Slice(command.Failures, func(left, right int) bool {
				return command.Failures[left].Kind < command.Failures[right].Kind
			})
		}
		if tool := doc.Endpoints[i].Tool; tool != nil && tool.Confirmation == "" {
			tool.Confirmation = defaultToolConfirmation(tool.Effect)
		}
		normalizedCLI, err := normalizeEndpointCLI(*doc, doc.Endpoints[i])
		if err != nil {
			return err
		}
		doc.Endpoints[i].CLI = normalizedCLI
		for j := range doc.Endpoints[i].Parameters {
			if doc.Endpoints[i].Parameters[j].In == "query" && doc.Endpoints[i].Parameters[j].Explode == nil {
				explode := false
				doc.Endpoints[i].Parameters[j].Explode = &explode
			}
		}
		sort.Slice(doc.Endpoints[i].Responses, func(a, b int) bool {
			return doc.Endpoints[i].Responses[a].StatusCode < doc.Endpoints[i].Responses[b].StatusCode
		})
		for j := range doc.Endpoints[i].Responses {
			sort.Slice(doc.Endpoints[i].Responses[j].Headers, func(a, b int) bool {
				return strings.ToLower(doc.Endpoints[i].Responses[j].Headers[a].Name) < strings.ToLower(doc.Endpoints[i].Responses[j].Headers[b].Name)
			})
		}
	}
	return nil
}

func validateEndpointCLI(doc Document, endpoint Endpoint, cli *CLI) error {
	if cli == nil {
		return nil
	}
	if len(cli.Command) == 0 {
		return fmt.Errorf("endpoint %q cli.command is required when cli is present", endpoint.OperationID)
	}
	for _, segment := range cli.Command {
		if strings.TrimSpace(segment) == "" {
			return fmt.Errorf("endpoint %q cli.command must not contain empty segments", endpoint.OperationID)
		}
	}

	switch cli.BodyInput {
	case "none", "json", "flags", "flags_or_json", "text", "binary", "file", "multipart":
	default:
		return fmt.Errorf("endpoint %q cli.body_input has unsupported value %q", endpoint.OperationID, cli.BodyInput)
	}

	switch cli.Confirm {
	case "none", "always":
	default:
		return fmt.Errorf("endpoint %q cli.confirm has unsupported value %q", endpoint.OperationID, cli.Confirm)
	}

	bodySchema, hasBodySchema := ResolveRequestBodySchema(doc, endpoint)
	if endpoint.RequestBody == nil && cli.BodyInput != "none" {
		return fmt.Errorf("endpoint %q cli.body_input=%q requires request_body", endpoint.OperationID, cli.BodyInput)
	}
	if endpoint.RequestBody != nil && (cli.BodyInput == "flags" || cli.BodyInput == "flags_or_json") && (!hasBodySchema || bodySchema.Type != "object") {
		return fmt.Errorf("endpoint %q cli.body_input=%q requires an object request_body schema", endpoint.OperationID, cli.BodyInput)
	}
	parametersByLocation := map[string]map[string]struct{}{
		"path":   {},
		"query":  {},
		"header": {},
		"body":   {},
	}
	for _, parameter := range endpoint.Parameters {
		if _, ok := parametersByLocation[parameter.In]; ok {
			parametersByLocation[parameter.In][parameter.Name] = struct{}{}
		}
	}
	if hasBodySchema && bodySchema.Type == "object" {
		for name := range bodySchema.Properties {
			parametersByLocation["body"][name] = struct{}{}
		}
	}

	seenArgs := make(map[string]struct{}, len(cli.Args))
	for _, arg := range cli.Args {
		switch arg.Source {
		case "path", "query", "header", "body":
		default:
			return fmt.Errorf("endpoint %q cli.args source %q is unsupported", endpoint.OperationID, arg.Source)
		}
		if strings.TrimSpace(arg.Name) == "" {
			return fmt.Errorf("endpoint %q cli.args name is required", endpoint.OperationID)
		}
		key := arg.Source + ":" + arg.Name
		if _, ok := seenArgs[key]; ok {
			return fmt.Errorf("endpoint %q cli.args contains duplicate binding %q", endpoint.OperationID, key)
		}
		seenArgs[key] = struct{}{}
		if _, ok := parametersByLocation[arg.Source][arg.Name]; !ok {
			return fmt.Errorf("endpoint %q cli.args references unknown %s field %q", endpoint.OperationID, arg.Source, arg.Name)
		}
		if arg.Source == "body" && !(cli.BodyInput == "flags" || cli.BodyInput == "flags_or_json") {
			return fmt.Errorf("endpoint %q cli.args body binding %q requires cli.body_input=flags or flags_or_json", endpoint.OperationID, arg.Name)
		}
	}

	if cli.Output == nil {
		return nil
	}
	switch cli.Output.Mode {
	case "detail", "collection", "empty", "raw":
	default:
		return fmt.Errorf("endpoint %q cli.output.mode has unsupported value %q", endpoint.OperationID, cli.Output.Mode)
	}

	successResponse, ok := SuccessResponse(endpoint)
	if !ok {
		return fmt.Errorf("endpoint %q cli output requires a success response", endpoint.OperationID)
	}
	successRef, hasSuccessSchema := ResolveResponseBodySchemaRef(*successResponse)
	switch cli.Output.Mode {
	case "collection":
		item, err := validateCLICollectionSchema(doc, endpoint.OperationID, successRef, hasSuccessSchema, cli)
		if err != nil {
			return err
		}
		for _, name := range cli.Output.TableColumns {
			found, err := resolveCLIOutputPropertySet(doc, item.Refs, name)
			if err != nil {
				return fmt.Errorf("endpoint %q cli.output.table_columns item field %q: %w", endpoint.OperationID, name, err)
			}
			if !found {
				return fmt.Errorf("endpoint %q cli.output.table_columns references unknown item field %q", endpoint.OperationID, name)
			}
		}
		for _, name := range cli.Output.QuietFields {
			found, err := resolveCLIOutputPropertySet(doc, item.Refs, name)
			if err != nil {
				return fmt.Errorf("endpoint %q cli.output.quiet_fields item field %q: %w", endpoint.OperationID, name, err)
			}
			if !found {
				return fmt.Errorf("endpoint %q cli.output.quiet_fields references unknown item field %q", endpoint.OperationID, name)
			}
		}
	case "detail":
		if _, object := ResolveObjectSchema(doc, successRef); !hasSuccessSchema || !object {
			return fmt.Errorf("endpoint %q cli.output.mode=detail requires an object success schema", endpoint.OperationID)
		}
		for _, name := range append(append([]string(nil), cli.Output.TableColumns...), cli.Output.QuietFields...) {
			found, err := resolveCLIOutputProperty(doc, successRef, name)
			if err != nil {
				return fmt.Errorf("endpoint %q cli.output response field %q: %w", endpoint.OperationID, name, err)
			}
			if !found {
				return fmt.Errorf("endpoint %q cli.output references unknown response field %q", endpoint.OperationID, name)
			}
		}
	case "empty", "raw":
		if cli.Pagination != nil {
			return fmt.Errorf("endpoint %q cli.pagination requires cli.output.mode=collection", endpoint.OperationID)
		}
	}

	if cli.Pagination != nil && cli.Output.Mode != "collection" {
		return fmt.Errorf("endpoint %q cli.pagination requires cli.output.mode=collection", endpoint.OperationID)
	}

	return nil
}

type cliCollectionItemSchema struct {
	Refs []SchemaRef
}

func validateCLICollectionSchema(doc Document, operationID string, successRef SchemaRef, hasSuccessSchema bool, cli *CLI) (cliCollectionItemSchema, error) {
	if _, object := ResolveObjectSchema(doc, successRef); !hasSuccessSchema || !object {
		return cliCollectionItemSchema{}, fmt.Errorf("endpoint %q cli.output.mode=collection requires an object success schema", operationID)
	}
	itemsField := "data"
	if cli.Pagination != nil && strings.TrimSpace(cli.Pagination.ItemsField) != "" {
		itemsField = cli.Pagination.ItemsField
	}
	itemRefs, ok, err := ResolveObjectArrayItemSchemas(doc, successRef, itemsField)
	if err != nil {
		return cliCollectionItemSchema{}, fmt.Errorf("endpoint %q cli collection items field %q: %w", operationID, itemsField, err)
	}
	if !ok {
		return cliCollectionItemSchema{}, fmt.Errorf("endpoint %q cli collection items field %q is missing", operationID, itemsField)
	}
	_, ok = ResolveCommonObjectSchema(doc, itemRefs)
	if !ok {
		return cliCollectionItemSchema{}, fmt.Errorf("endpoint %q cli collection item schemas could not be resolved to object variants", operationID)
	}
	return cliCollectionItemSchema{Refs: itemRefs}, nil
}

func resolveCLIOutputProperty(doc Document, scope SchemaRef, name string) (bool, error) {
	_, found, err := ResolveObjectProperty(doc, scope, name)
	if err != nil {
		return false, err
	}
	return found, nil
}

func resolveCLIOutputPropertySet(doc Document, scopes []SchemaRef, name string) (bool, error) {
	_, found, err := ResolveCommonObjectProperty(doc, scopes, name)
	if err != nil {
		return false, err
	}
	return found, nil
}

func normalizeEndpointCLI(doc Document, endpoint Endpoint) (*CLI, error) {
	cli := CloneCLI(endpoint.CLI)
	if cli == nil {
		return nil, nil
	}
	for i := range cli.Command {
		cli.Command[i] = strings.TrimSpace(cli.Command[i])
	}
	if endpoint.RequestBody == nil && cli.BodyInput == "" {
		cli.BodyInput = "none"
	}
	if endpoint.RequestBody != nil && cli.BodyInput == "" {
		content, ok := PrimaryRequestBodyContent(endpoint)
		switch {
		case !ok:
			cli.BodyInput = "none"
		case content.BodyKind == "json":
			requestBodySchema, ok := ResolveRequestBodySchema(doc, endpoint)
			if ok && strings.EqualFold(requestBodySchema.Type, "object") {
				cli.BodyInput = "flags_or_json"
			} else {
				cli.BodyInput = "json"
			}
		case content.BodyKind == "form_urlencoded":
			cli.BodyInput = "flags"
		case content.BodyKind == "text":
			cli.BodyInput = "text"
		case content.BodyKind == "binary":
			cli.BodyInput = "binary"
		case content.BodyKind == "file":
			cli.BodyInput = "file"
		case content.BodyKind == "multipart":
			cli.BodyInput = "multipart"
		default:
			cli.BodyInput = "json"
		}
	}
	if cli.Confirm == "" {
		if strings.EqualFold(endpoint.Method, "DELETE") {
			cli.Confirm = "always"
		} else {
			cli.Confirm = "none"
		}
	}
	if len(cli.Args) == 0 {
		cli.Args = defaultCLIArgs(doc, endpoint, cli.BodyInput)
	}
	output, pagination := deriveDefaultCLIOutput(doc, endpoint)
	if cli.Output == nil {
		cli.Output = output
	} else if output != nil {
		if cli.Output.Mode == "" {
			cli.Output.Mode = output.Mode
		}
		if cli.Output.Mode == output.Mode {
			if len(cli.Output.TableColumns) == 0 {
				cli.Output.TableColumns = append([]string(nil), output.TableColumns...)
			}
			if len(cli.Output.QuietFields) == 0 {
				cli.Output.QuietFields = append([]string(nil), output.QuietFields...)
			}
		}
	}
	if cli.Pagination == nil && cli.Output != nil && cli.Output.Mode == "collection" {
		cli.Pagination = pagination
	}
	return cli, nil
}

func defaultCLIArgs(doc Document, endpoint Endpoint, _ string) []CLIArg {
	pathArgs := defaultPositionalPathArgs(endpoint)
	args := make([]CLIArg, 0, len(pathArgs)+1)
	for _, name := range pathArgs {
		args = append(args, CLIArg{Source: "path", Name: name, DisplayName: strings.ReplaceAll(name, "_", "-")})
	}
	if shouldDefaultBodyNameArg(doc, endpoint) {
		args = append(args, CLIArg{Source: "body", Name: "name", DisplayName: "name"})
	}
	return args
}

func defaultPositionalPathArgs(endpoint Endpoint) []string {
	pathParams := PathParameterNames(endpoint.Path)
	if len(pathParams) == 0 {
		return nil
	}
	if strings.HasPrefix(endpoint.OperationID, "create") {
		selected := make([]string, 0, len(pathParams))
		for _, name := range pathParams {
			if name == "catalog_name" {
				continue
			}
			selected = append(selected, name)
		}
		return selected
	}
	if strings.HasPrefix(endpoint.OperationID, "list") {
		return append([]string(nil), pathParams...)
	}
	selected := make([]string, 0, len(pathParams))
	for _, name := range pathParams {
		if name == "catalog_name" {
			continue
		}
		selected = append(selected, name)
	}
	if len(selected) == 0 {
		selected = append(selected, pathParams[len(pathParams)-1])
	}
	return selected
}

func shouldDefaultBodyNameArg(doc Document, endpoint Endpoint) bool {
	if !strings.HasPrefix(endpoint.OperationID, "create") {
		return false
	}
	pathParams := PathParameterNames(endpoint.Path)
	if len(pathParams) == 0 {
		return false
	}
	for _, name := range pathParams {
		if name != "catalog_name" {
			return false
		}
	}
	bodySchema, ok := ResolveRequestBodySchema(doc, endpoint)
	if !ok || bodySchema.Type != "object" {
		return false
	}
	if _, ok := bodySchema.Properties["name"]; !ok {
		return false
	}
	for _, name := range bodySchema.Required {
		if name == "name" {
			return true
		}
	}
	return false
}

func deriveDefaultCLIOutput(doc Document, endpoint Endpoint) (*CLIOutput, *CLIPagination) {
	successResponse, ok := SuccessResponse(endpoint)
	if !ok {
		return nil, nil
	}
	if strings.EqualFold(endpoint.Method, "DELETE") || successResponse.StatusCode == 204 {
		return &CLIOutput{Mode: "empty"}, nil
	}
	successRef, ok := ResolveResponseBodySchemaRef(*successResponse)
	if !ok {
		return &CLIOutput{Mode: "raw"}, nil
	}
	if successSchema, object := ResolveObjectSchema(doc, successRef); object {
		if itemRefs, found, err := ResolveObjectArrayItemSchemas(doc, successRef, "data"); err == nil && found {
			output := &CLIOutput{Mode: "collection"}
			pagination := &CLIPagination{}
			pagination.ItemsField = "data"
			if nextProperty, found, err := ResolveObjectProperty(doc, successRef, "next_page_token"); err == nil && found && schemaRefHasType(doc, nextProperty.Property.Schema, "string") {
				pagination.NextPageTokenField = "next_page_token"
			}
			if itemSchema, ok := ResolveCommonObjectSchema(doc, itemRefs); ok {
				output.TableColumns = OrderedPropertyNames(itemSchema)
				output.QuietFields = defaultQuietFields(itemSchema)
			}
			return output, pagination
		}
		return &CLIOutput{
			Mode:        "detail",
			QuietFields: defaultQuietFields(successSchema),
		}, nil
	}
	return &CLIOutput{Mode: "raw"}, nil
}

func schemaRefHasType(doc Document, ref SchemaRef, expected string) bool {
	schema, ok := resolveConcreteSchema(doc, ref)
	return ok && strings.EqualFold(schema.Type, expected)
}

func defaultQuietFields(schema Schema) []string {
	fields := make([]string, 0, 3)
	for _, name := range []string{"id", "name", "key"} {
		if _, ok := schema.Properties[name]; ok {
			fields = append(fields, name)
		}
	}
	return fields
}

func commandPathPrefix(shorter []string, longer []string) bool {
	if len(shorter) >= len(longer) {
		return false
	}
	for i := range shorter {
		if shorter[i] != longer[i] {
			return false
		}
	}
	return true
}
