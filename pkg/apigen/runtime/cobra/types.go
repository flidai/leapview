// Package cobra defines the supported Cobra runtime boundary for generated CLIs.
package cobra

import spcobra "github.com/spf13/cobra"

// CommandSpec is generated CLI metadata for one API operation.
type CommandSpec struct {
	OperationID string           `json:"operation_id"`
	Method      string           `json:"method"`
	Path        string           `json:"path"`
	Summary     string           `json:"summary"`
	Description string           `json:"description,omitempty"`
	Tags        []string         `json:"tags,omitempty"`
	Parameters  []Param          `json:"parameters,omitempty"`
	RequestBody *RequestBodySpec `json:"request_body,omitempty"`
	Command     []string         `json:"command,omitempty"`
	Args        []ArgBinding     `json:"args,omitempty"`
	Confirm     string           `json:"confirm,omitempty"`
	Output      OutputSpec       `json:"output,omitempty"`
	Pagination  *PaginationSpec  `json:"pagination,omitempty"`
}

// Param describes one generated endpoint parameter.
type Param struct {
	Name        string   `json:"name"`
	In          string   `json:"in"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	SchemaJSON  string   `json:"schema_json,omitempty"`
}

// Field describes one generated JSON request body field.
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	SchemaJSON  string   `json:"schema_json,omitempty"`
}

// RequestBodySpec describes generated request body input behavior.
type RequestBodySpec struct {
	Required    bool                `json:"required,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
	BodyKind    string              `json:"body_kind,omitempty"`
	SchemaType  string              `json:"schema_type,omitempty"`
	SchemaJSON  string              `json:"schema_json,omitempty"`
	InputMode   string              `json:"input_mode,omitempty"`
	Fields      []Field             `json:"fields,omitempty"`
	Parts       []MultipartPartSpec `json:"parts,omitempty"`
}

// MultipartPartSpec describes one generated multipart CLI input part.
type MultipartPartSpec struct {
	Name        string `json:"name"`
	WireName    string `json:"wire_name,omitempty"`
	PartKind    string `json:"part_kind,omitempty"`
	Repeated    bool   `json:"repeated,omitempty"`
	Required    bool   `json:"required,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	BodyKind    string `json:"body_kind,omitempty"`
	Filename    bool   `json:"filename,omitempty"`
	SchemaType  string `json:"schema_type,omitempty"`
	SchemaJSON  string `json:"schema_json,omitempty"`
}

// MultipartBody is the parsed request body passed from generated commands to Client.Do.
type MultipartBody struct {
	ContentType string
	Parts       []MultipartPart
}

// MultipartPart is one parsed outgoing multipart part.
type MultipartPart struct {
	Name        string
	ContentType string
	BodyKind    string
	Filename    *string
	Data        []byte
}

// ArgBinding binds a positional argument to a request source field.
type ArgBinding struct {
	Source      string `json:"source"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
}

// OutputSpec declares how a response should be rendered.
type OutputSpec struct {
	Mode         string   `json:"mode,omitempty"`
	TableColumns []string `json:"table_columns,omitempty"`
	QuietFields  []string `json:"quiet_fields,omitempty"`
}

// PaginationSpec describes a paginated collection envelope.
type PaginationSpec struct {
	ItemsField         string `json:"items_field,omitempty"`
	NextPageTokenField string `json:"next_page_token_field,omitempty"`
}

// CommandGroup configures a top-level Cobra group for generated commands.
type CommandGroup struct {
	ID    string
	Title string
}

// RuntimeOptions customizes generated Cobra command construction.
type RuntimeOptions struct {
	RunOverrides             map[string]func(*Client) func(*spcobra.Command, []string) error
	CommandMutators          map[string]func(*spcobra.Command)
	ResponseRenderers        map[string]func(*spcobra.Command, []byte) error
	RootGroupResolver        func(commandPath []string) *CommandGroup
	GroupDescriptionResolver func(commandPath []string) string
}

// PaginatedResponse is the minimal envelope used by FetchAllPages.
type PaginatedResponse struct {
	Data          []interface{} `json:"data"`
	NextPageToken string        `json:"next_page_token"`
}
