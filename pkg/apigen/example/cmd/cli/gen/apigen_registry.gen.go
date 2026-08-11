package gen

import apigencobra "github.com/Yacobolo/toolbelt/apigen/runtime/cobra"

// APIGenCommandSpec is generated from JSON IR.
type APIGenCommandSpec = apigencobra.CommandSpec

// APIGenParam is generated parameter metadata from JSON IR.
type APIGenParam = apigencobra.Param

// APIGenField is generated request body field metadata from JSON IR.
type APIGenField = apigencobra.Field

// APIGenRequestBody is generated request body metadata from JSON IR.
type APIGenRequestBody = apigencobra.RequestBodySpec

// APIGenMultipartPart is generated multipart request body metadata from JSON IR.
type APIGenMultipartPart = apigencobra.MultipartPartSpec

// APIGenArgBinding is generated positional argument metadata from JSON IR.
type APIGenArgBinding = apigencobra.ArgBinding

// APIGenOutput is generated output rendering metadata from JSON IR.
type APIGenOutput = apigencobra.OutputSpec

// APIGenPagination is generated pagination metadata from JSON IR.
type APIGenPagination = apigencobra.PaginationSpec

// APIGeneratedCommandSpecs contains operation metadata for generated CLI execution.
var APIGeneratedCommandSpecs = []APIGenCommandSpec{
	{OperationID: "listTodos", Method: "GET", Path: "/todos", Summary: "List todos", Description: "", Tags: []string{"Todos"}, Parameters: []apigencobra.Param{{Name: "status", In: "query", Type: "string", Description: "Optional status filter.", Required: false, Enum: nil, SchemaJSON: "{\"type\":\"string\"}"}}, RequestBody: nil, Command: []string{"todos", "list"}, Args: nil, Confirm: "none", Output: apigencobra.OutputSpec{Mode: "collection", TableColumns: []string{"id", "title", "status"}, QuietFields: []string{"id"}}, Pagination: &apigencobra.PaginationSpec{ItemsField: "data", NextPageTokenField: ""}},
	{OperationID: "createTodo", Method: "POST", Path: "/todos", Summary: "Create todo", Description: "", Tags: []string{"Todos"}, Parameters: nil, RequestBody: &apigencobra.RequestBodySpec{Required: true, ContentType: "application/json", BodyKind: "json", SchemaType: "object", SchemaJSON: "{\"additionalProperties\":false,\"properties\":{\"title\":{\"type\":\"string\"}},\"required\":[\"title\"],\"type\":\"object\"}", InputMode: "flags_or_json", Fields: []apigencobra.Field{{Name: "title", Type: "string", Description: "", Required: true, Enum: nil, SchemaJSON: "{\"type\":\"string\"}"}}, Parts: nil}, Command: []string{"todos", "create"}, Args: []apigencobra.ArgBinding{{Source: "body", Name: "title", DisplayName: "title"}}, Confirm: "none", Output: apigencobra.OutputSpec{Mode: "detail", TableColumns: nil, QuietFields: []string{"id"}}, Pagination: nil},
	{OperationID: "deleteTodo", Method: "DELETE", Path: "/todos/{todo_id}", Summary: "Delete todo", Description: "", Tags: []string{"Todos"}, Parameters: []apigencobra.Param{{Name: "todo_id", In: "path", Type: "string", Description: "", Required: true, Enum: nil, SchemaJSON: "{\"type\":\"string\"}"}}, RequestBody: nil, Command: []string{"todos", "delete"}, Args: []apigencobra.ArgBinding{{Source: "path", Name: "todo_id", DisplayName: "todo-id"}}, Confirm: "always", Output: apigencobra.OutputSpec{Mode: "empty", TableColumns: nil, QuietFields: nil}, Pagination: nil},
	{OperationID: "getTodo", Method: "GET", Path: "/todos/{todo_id}", Summary: "Get todo", Description: "", Tags: []string{"Todos"}, Parameters: []apigencobra.Param{{Name: "todo_id", In: "path", Type: "string", Description: "", Required: true, Enum: nil, SchemaJSON: "{\"type\":\"string\"}"}}, RequestBody: nil, Command: []string{"todos", "get"}, Args: []apigencobra.ArgBinding{{Source: "path", Name: "todo_id", DisplayName: "todo-id"}}, Confirm: "none", Output: apigencobra.OutputSpec{Mode: "detail", TableColumns: nil, QuietFields: []string{"id"}}, Pagination: nil},
	{OperationID: "completeTodo", Method: "POST", Path: "/todos/{todo_id}/complete", Summary: "Complete todo", Description: "", Tags: []string{"Todos"}, Parameters: []apigencobra.Param{{Name: "todo_id", In: "path", Type: "string", Description: "", Required: true, Enum: nil, SchemaJSON: "{\"type\":\"string\"}"}}, RequestBody: nil, Command: []string{"todos", "complete"}, Args: []apigencobra.ArgBinding{{Source: "path", Name: "todo_id", DisplayName: "todo-id"}}, Confirm: "none", Output: apigencobra.OutputSpec{Mode: "detail", TableColumns: nil, QuietFields: []string{"id"}}, Pagination: nil},
}
