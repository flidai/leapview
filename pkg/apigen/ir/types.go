// Package ir defines the JSON IR schema consumed by apigen emitters.
package ir

// Document is the root JSON IR payload.
type Document struct {
	SchemaVersion   string            `json:"schema_version"`
	API             API               `json:"api"`
	Info            Info              `json:"info"`
	OpenAPI         OpenAPI           `json:"openapi,omitempty"`
	Servers         []Server          `json:"servers,omitempty"`
	Tags            []Tag             `json:"tags,omitempty"`
	Schemas         map[string]Schema `json:"schemas,omitempty"`
	Contracts       []Contract        `json:"contracts,omitempty"`
	Endpoints       []Endpoint        `json:"endpoints,omitempty"`
	TransportErrors *TransportErrors  `json:"transport_errors,omitempty"`
	Extensions      map[string]any    `json:"extensions,omitempty"`
}

// TransportErrors defines generated HTTP transport failure contracts.
type TransportErrors struct {
	Schema      SchemaRef                   `json:"schema"`
	ContentType string                      `json:"content_type"`
	Failures    map[string]TransportFailure `json:"failures"`
}

// TransportFailure maps a stable failure kind to public wire behavior.
type TransportFailure struct {
	StatusCode   int    `json:"status_code"`
	Code         string `json:"code"`
	PublicDetail string `json:"public_detail"`
}

// API contains APIGen-owned API metadata.
type API struct {
	BasePath string `json:"base_path"`
}

// Info contains API metadata.
type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
}

// Server describes a server URL entry.
type Server struct {
	URL         string                    `json:"url"`
	Description string                    `json:"description,omitempty"`
	Variables   map[string]ServerVariable `json:"variables,omitempty"`
}

// ServerVariable describes an OpenAPI server variable.
type ServerVariable struct {
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// Tag describes a logical operation grouping.
type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Contract describes one data-contract root.
type Contract struct {
	Name        string         `json:"name"`
	Schema      SchemaRef      `json:"schema"`
	Kind        string         `json:"kind,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Description string         `json:"description,omitempty"`
	Extensions  map[string]any `json:"extensions,omitempty"`
}

// OpenAPI contains canonical OpenAPI document metadata that is not directly
// represented by the generator-oriented endpoint/schema model.
type OpenAPI struct {
	Version         string                    `json:"version,omitempty"`
	TagOrder        []string                  `json:"tag_order,omitempty"`
	Security        []SecurityRequirement     `json:"security,omitempty"`
	SecuritySchemes map[string]SecurityScheme `json:"security_schemes,omitempty"`
}

// SecurityRequirement is an OpenAPI security requirement object.
type SecurityRequirement map[string][]string

// SecurityScheme describes one named OpenAPI security scheme.
type SecurityScheme struct {
	Type   string `json:"type"`
	In     string `json:"in,omitempty"`
	Name   string `json:"name,omitempty"`
	Scheme string `json:"scheme,omitempty"`
}

// Endpoint describes one API operation.
type Endpoint struct {
	Method      string                `json:"method"`
	Path        string                `json:"path"`
	OperationID string                `json:"operation_id"`
	Kind        string                `json:"kind,omitempty"`
	Namespace   string                `json:"namespace,omitempty"`
	Summary     string                `json:"summary,omitempty"`
	Description string                `json:"description,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty"`
	RequestBody *RequestBody          `json:"request_body,omitempty"`
	Responses   []Response            `json:"responses"`
	CLI         *CLI                  `json:"cli,omitempty"`
	Command     *Command              `json:"command,omitempty"`
	Tool        *Tool                 `json:"tool,omitempty"`
	Security    []SecurityRequirement `json:"security,omitempty"`
	Extensions  map[string]any        `json:"extensions,omitempty"`
}

// Command describes a transport-neutral application command derived from an endpoint.
type Command struct {
	Owner               string           `json:"owner"`
	Audit               AuditPolicy      `json:"audit"`
	Execution           *AsyncExecution  `json:"execution,omitempty"`
	Failures            []CommandFailure `json:"failures"`
	AdditionalExposures []string         `json:"additional_exposures,omitempty"`
	UI                  *UIAction        `json:"ui,omitempty"`
	Target              *OperationTarget `json:"target,omitempty"`
	Idempotency         string           `json:"idempotency,omitempty"`
	Concurrency         string           `json:"concurrency,omitempty"`
	AuthzMode           string           `json:"authz_mode,omitempty"`
	Privilege           string           `json:"privilege,omitempty"`
}

// UIAction binds a browser action identity to its transport-neutral command.
type UIAction struct {
	ActionID string `json:"action_id"`
}

// CommandFailure maps a transport-neutral domain failure kind to public behavior.
type CommandFailure struct {
	Kind         string `json:"kind"`
	StatusCode   int    `json:"status_code"`
	Code         string `json:"code"`
	PublicDetail string `json:"public_detail"`
}

// AsyncExecution describes the durable workflow started by a command.
type AsyncExecution struct {
	Mode            string `json:"mode"`
	Guarantee       string `json:"guarantee"`
	JobKind         string `json:"job_kind"`
	ResourceKind    string `json:"resource_kind"`
	InitialEvent    string `json:"initial_event"`
	InitialState    string `json:"initial_state"`
	StatusOperation string `json:"status_operation"`
	EventsOperation string `json:"events_operation"`
	Cancellation    string `json:"cancellation"`
}

// AuditPolicy describes the audit record required for a successful command.
type AuditPolicy struct {
	Required      bool          `json:"required"`
	SuccessAction string        `json:"success_action,omitempty"`
	Guarantee     string        `json:"guarantee,omitempty"`
	Payload       *AuditPayload `json:"payload,omitempty"`
}

// AuditPayload describes the typed, versioned data persisted for a command
// audit event.
type AuditPayload struct {
	Schema        SchemaRef    `json:"schema"`
	SchemaVersion int          `json:"schema_version"`
	Retention     string       `json:"retention"`
	Fields        []AuditField `json:"fields"`
}

// AuditField classifies one payload field for durable audit and safe logging.
type AuditField struct {
	Name        string `json:"name"`
	Sensitivity string `json:"sensitivity"`
}

// OperationTarget identifies the required path parameter targeted by a command.
type OperationTarget struct {
	Parameter string `json:"parameter"`
	Type      string `json:"type"`
}

// Tool describes an SDK-neutral agent tool projected from an endpoint.
type Tool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Effect       string         `json:"effect"`
	Confirmation string         `json:"confirmation,omitempty"`
	Tags         []string       `json:"tags,omitempty"`
	Input        *ToolInput     `json:"input,omitempty"`
	Output       ToolOutput     `json:"output"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// ToolInput customizes how endpoint fields become tool arguments.
type ToolInput struct {
	Fields []ToolInputField `json:"fields,omitempty"`
}

// ToolInputField overrides one endpoint parameter or request body field.
type ToolInputField struct {
	Source      string `json:"source"`
	Name        string `json:"name"`
	Mode        string `json:"mode,omitempty"`
	Alias       string `json:"alias,omitempty"`
	ContextKey  string `json:"context_key,omitempty"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
}

// ToolOutput describes the successful response presented to an agent.
type ToolOutput struct {
	Mode   string           `json:"mode"`
	Select []ToolProjection `json:"select,omitempty"`
	Cursor *ToolCursor      `json:"cursor,omitempty"`
}

// ToolProjection recursively selects one response value.
type ToolProjection struct {
	Source  string           `json:"source"`
	Target  string           `json:"target,omitempty"`
	Select  []ToolProjection `json:"select,omitempty"`
	CountAs string           `json:"count_as,omitempty"`
}

// ToolCursor exposes pagination state in a stable tool result shape.
type ToolCursor struct {
	Source        string `json:"source"`
	Target        string `json:"target,omitempty"`
	HasMoreTarget string `json:"has_more_target,omitempty"`
}

// CLI describes APIGen-owned CLI metadata for one operation.
type CLI struct {
	Command    []string       `json:"command,omitempty"`
	Args       []CLIArg       `json:"args,omitempty"`
	BodyInput  string         `json:"body_input,omitempty"`
	Confirm    string         `json:"confirm,omitempty"`
	Output     *CLIOutput     `json:"output,omitempty"`
	Pagination *CLIPagination `json:"pagination,omitempty"`
}

// CLIArg binds one positional CLI argument to a request source field.
type CLIArg struct {
	Source      string `json:"source"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
}

// CLIOutput controls generated response rendering.
type CLIOutput struct {
	Mode         string   `json:"mode,omitempty"`
	TableColumns []string `json:"table_columns,omitempty"`
	QuietFields  []string `json:"quiet_fields,omitempty"`
}

// CLIPagination declares the collection envelope used for --all style paging.
type CLIPagination struct {
	ItemsField         string `json:"items_field,omitempty"`
	NextPageTokenField string `json:"next_page_token_field,omitempty"`
}

// Parameter describes an operation parameter.
type Parameter struct {
	Name        string    `json:"name"`
	In          string    `json:"in"`
	Required    bool      `json:"required,omitempty"`
	Description string    `json:"description,omitempty"`
	Example     any       `json:"example,omitempty"`
	Explode     *bool     `json:"explode,omitempty"`
	Schema      SchemaRef `json:"schema"`
}

// RequestBody describes the operation request payload.
type RequestBody struct {
	Required    bool          `json:"required,omitempty"`
	Description string        `json:"description,omitempty"`
	Contents    []BodyContent `json:"contents,omitempty"`
}

// Response describes one operation response.
type Response struct {
	StatusCode  int            `json:"status_code"`
	Description string         `json:"description"`
	Headers     []Header       `json:"headers,omitempty"`
	Contents    []BodyContent  `json:"contents,omitempty"`
	Extensions  map[string]any `json:"extensions,omitempty"`
}

// BodyContent describes one media type variant for a request or response body.
type BodyContent struct {
	ContentType string          `json:"content_type"`
	BodyKind    string          `json:"body_kind"`
	Schema      *SchemaRef      `json:"schema,omitempty"`
	AnyOf       []SchemaRef     `json:"any_of,omitempty"`
	Parts       []MultipartPart `json:"parts,omitempty"`
	Example     any             `json:"example,omitempty"`
}

// MultipartPart describes one part in a multipart body.
type MultipartPart struct {
	Name        string     `json:"name"`
	WireName    string     `json:"wire_name,omitempty"`
	PartKind    string     `json:"part_kind,omitempty"`
	Repeated    bool       `json:"repeated,omitempty"`
	Required    bool       `json:"required,omitempty"`
	Description string     `json:"description,omitempty"`
	ContentType string     `json:"content_type,omitempty"`
	BodyKind    string     `json:"body_kind,omitempty"`
	Filename    bool       `json:"filename,omitempty"`
	Schema      *SchemaRef `json:"schema,omitempty"`
}

// Header describes one response header.
type Header struct {
	Name        string    `json:"name"`
	Required    bool      `json:"required,omitempty"`
	Description string    `json:"description,omitempty"`
	Schema      SchemaRef `json:"schema"`
}

// ResponseShapeExtensionKey stores APIGen-owned response shape metadata.
const ResponseShapeExtensionKey = "x-apigen-response-shape"

// ResponseShape describes the APIGen-owned response transport shape.
type ResponseShape struct {
	Kind     string `json:"kind"`
	BodyType string `json:"body_type,omitempty"`
}

// SchemaRef references or describes a schema.
type SchemaRef struct {
	Ref                  string                `json:"ref,omitempty"`
	Type                 string                `json:"type,omitempty"`
	Format               string                `json:"format,omitempty"`
	Enum                 []string              `json:"enum,omitempty"`
	Minimum              *float64              `json:"minimum,omitempty"`
	Maximum              *float64              `json:"maximum,omitempty"`
	MinLength            *int                  `json:"min_length,omitempty"`
	MaxLength            *int                  `json:"max_length,omitempty"`
	MinProperties        *int                  `json:"min_properties,omitempty"`
	Pattern              string                `json:"pattern,omitempty"`
	Items                *SchemaRef            `json:"items,omitempty"`
	AdditionalProperties *AdditionalProperties `json:"additional_properties,omitempty"`
	PropertyNames        *SchemaRef            `json:"property_names,omitempty"`
}

// AdditionalProperties captures OpenAPI object-map semantics for inline schema refs.
type AdditionalProperties struct {
	Any    bool       `json:"any,omitempty"`
	Schema *SchemaRef `json:"schema,omitempty"`
}

// Schema is a JSON schema subset used by apigen.
type Schema struct {
	Type          string                    `json:"type"`
	Namespace     string                    `json:"namespace,omitempty"`
	Title         string                    `json:"title,omitempty"`
	Description   string                    `json:"description,omitempty"`
	Example       any                       `json:"example,omitempty"`
	Properties    map[string]SchemaProperty `json:"properties,omitempty"`
	PropertyOrder []string                  `json:"property_order,omitempty"`
	Required      []string                  `json:"required,omitempty"`
	Items         *SchemaRef                `json:"items,omitempty"`
	Base          *SchemaRef                `json:"base,omitempty"`
	OneOf         []SchemaRef               `json:"one_of,omitempty"`
	Discriminator *Discriminator            `json:"discriminator,omitempty"`
	Enum          []string                  `json:"enum,omitempty"`
	Extensions    map[string]any            `json:"extensions,omitempty"`
}

// Discriminator selects one schema alternative using an object property.
type Discriminator struct {
	PropertyName string            `json:"property_name"`
	Mapping      map[string]string `json:"mapping"`
}

// SchemaProperty describes one schema property.
type SchemaProperty struct {
	Description string         `json:"description,omitempty"`
	Example     any            `json:"example,omitempty"`
	Schema      SchemaRef      `json:"schema"`
	Extensions  map[string]any `json:"extensions,omitempty"`
}
