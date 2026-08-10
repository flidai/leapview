using Http;
using TypeSpec.OpenAPI;

@service(#{ title: "APIGen Todo Example" })
@doc("Small in-memory todo API authored in TypeSpec to showcase APIGen generation and strict server wiring.")
@info(#{ version: "0.1.0" })
@server("http://127.0.0.1:8081", "Example development server")
@useAuth(BearerAuth | ApiKeyAuth<ApiKeyLocation.header, "X-API-Key">)
@tagMetadata(#[#{ name: "Todos", description: "Todo lifecycle endpoints for the APIGen example." }])
@apigen.transportErrors(Error, #{
  contentType: "application/json",
  failures: #[
    #{ kind: "path_parameter", statusCode: 400, code: "invalid_request", publicDetail: "Invalid request." },
    #{ kind: "query_parameter", statusCode: 400, code: "invalid_request", publicDetail: "Invalid request." },
    #{ kind: "header_parameter", statusCode: 400, code: "invalid_request", publicDetail: "Invalid request." },
    #{ kind: "malformed_body", statusCode: 400, code: "invalid_request", publicDetail: "Invalid request body." },
    #{ kind: "multipart", statusCode: 400, code: "invalid_request", publicDetail: "Invalid multipart body." },
    #{ kind: "unsupported_media_type", statusCode: 415, code: "unsupported_media_type", publicDetail: "Unsupported media type." },
    #{ kind: "handler", statusCode: 500, code: "internal_error", publicDetail: "Internal server error." },
    #{ kind: "response_serialization", statusCode: 500, code: "internal_error", publicDetail: "Internal server error." },
  ],
})
namespace TodoAPI;

model Todo {
  id: string;
  title: string;
  status: string;
}

model CreateTodoRequest {
  title: string;
}

model ListTodosResponse {
  data: Todo[];
}

model Error {
  code: string;
  message: string;
}

@doc("The request has succeeded.")
model ListTodosOk {
  @statusCode statusCode: 200;
  @body body: ListTodosResponse;
}

@doc("The request is invalid.")
model BadRequest {
  @statusCode statusCode: 400;
  @body body: Error;
}

@doc("A todo has been created.")
model CreateTodoCreated {
  @statusCode statusCode: 201;
  @body body: Todo;
}

@doc("The todo was not found.")
model NotFound {
  @statusCode statusCode: 404;
  @body body: Error;
}

@doc("The todo has been completed.")
model CompleteTodoOk {
  @statusCode statusCode: 200;
  @body body: Todo;
}

@doc("The todo has been deleted.")
model DeleteTodoNoContent {
  @statusCode statusCode: 204;
}

@tag("Todos")
@summary("List todos")
@route("/todos")
@get
@apigen.tool(#{
  name: "list_todos",
  effect: "read",
  tags: #["todos", "read"],
  output: #{
    mode: "project",
    select: #[#{ source: "/data", countAs: "count", select: #[#{ source: "/id" }, #{ source: "/title" }, #{ source: "/status" }] }],
  },
})
@apigen.cli(#{
  command: #["todos", "list"],
  output: #{ mode: "collection", tableColumns: #["id", "title", "status"], quietFields: #["id"] }
})
op listTodos(
  /** Optional status filter. */
  @query status?: string,
): ListTodosOk | BadRequest;

@tag("Todos")
@summary("Create todo")
@route("/todos")
@post
@apigen.cli(#{
  command: #["todos", "create"],
  bodyInput: "flags_or_json",
  args: #[#{ source: "body", name: "title", displayName: "title" }],
  output: #{ mode: "detail", quietFields: #["id"] }
})
op createTodo(@body body: CreateTodoRequest): CreateTodoCreated | BadRequest;

@tag("Todos")
@summary("Get todo")
@route("/todos/{todo_id}")
@get
@apigen.cli(#{
  command: #["todos", "get"],
  args: #[#{ source: "path", name: "todo_id", displayName: "todo-id" }],
  output: #{ mode: "detail", quietFields: #["id"] }
})
op getTodo(@path todo_id: string): Todo | NotFound;

@tag("Todos")
@summary("Complete todo")
@route("/todos/{todo_id}/complete")
@post
@apigen.cli(#{
  command: #["todos", "complete"],
  args: #[#{ source: "path", name: "todo_id", displayName: "todo-id" }],
  output: #{ mode: "detail", quietFields: #["id"] }
})
op completeTodo(@path todo_id: string): CompleteTodoOk | NotFound;

@tag("Todos")
@summary("Delete todo")
@route("/todos/{todo_id}")
@delete
@apigen.tool(#{
  name: "delete_todo",
  effect: "destructive",
  output: #{ mode: "empty" },
})
@apigen.cli(#{
  command: #["todos", "delete"],
  args: #[#{ source: "path", name: "todo_id", displayName: "todo-id" }],
  confirm: "always",
  output: #{ mode: "empty" }
})
op deleteTodo(@path todo_id: string): DeleteTodoNoContent | NotFound;
