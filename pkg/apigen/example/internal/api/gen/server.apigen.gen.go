package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	apigenagenttool "github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
	apigenchi "github.com/Yacobolo/toolbelt/apigen/runtime/chi"
)

const embeddedOpenAPISpecJSON = `{"components":{"schemas":{"CreateTodoRequest":{"example":{"title":"example"},"properties":{"title":{"example":"example","type":"string"}},"required":["title"],"type":"object"},"Error":{"example":{"code":"example","message":"example"},"properties":{"code":{"example":"example","type":"string"},"message":{"example":"example","type":"string"}},"required":["code","message"],"type":"object"},"ListTodosResponse":{"example":{"data":[{"id":"example","status":"example","title":"example"}]},"properties":{"data":{"example":[{"id":"example","status":"example","title":"example"}],"items":{"$ref":"#/components/schemas/Todo"},"type":"array"}},"required":["data"],"type":"object"},"Todo":{"example":{"id":"example","status":"example","title":"example"},"properties":{"id":{"example":"example","type":"string"},"status":{"example":"example","type":"string"},"title":{"example":"example","type":"string"}},"required":["id","title","status"],"type":"object"}},"securitySchemes":{"ApiKeyAuth":{"in":"header","name":"X-API-Key","type":"apiKey"},"BearerAuth":{"scheme":"Bearer","type":"http"}}},"info":{"description":"Small in-memory todo API authored in TypeSpec to showcase APIGen generation and strict server wiring.","title":"APIGen Todo Example","version":"0.1.0"},"openapi":"3.0.0","paths":{"/todos":{"get":{"operationId":"listTodos","parameters":[{"description":"Optional status filter.","example":"example","explode":false,"in":"query","name":"status","required":false,"schema":{"type":"string"}}],"responses":{"200":{"content":{"application/json":{"example":{"data":[{"id":"example","status":"example","title":"example"}]},"schema":{"$ref":"#/components/schemas/ListTodosResponse"}}},"description":"The request has succeeded."},"400":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"The request is invalid."},"415":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"},"500":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"}},"summary":"List todos","tags":["Todos"],"x-apigen-tool":{"confirmation":"never","effect":"read","name":"list_todos","output":{"mode":"project","select":[{"count_as":"count","select":[{"source":"/id"},{"source":"/title"},{"source":"/status"}],"source":"/data"}]},"tags":["todos","read"]}},"post":{"operationId":"createTodo","parameters":[],"requestBody":{"content":{"application/json":{"example":{"title":"example"},"schema":{"$ref":"#/components/schemas/CreateTodoRequest"}}},"required":true},"responses":{"201":{"content":{"application/json":{"example":{"id":"example","status":"example","title":"example"},"schema":{"$ref":"#/components/schemas/Todo"}}},"description":"A todo has been created."},"400":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"The request is invalid."},"415":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"},"500":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"}},"summary":"Create todo","tags":["Todos"]}},"/todos/{todo_id}":{"delete":{"operationId":"deleteTodo","parameters":[{"example":"example","in":"path","name":"todo_id","required":true,"schema":{"type":"string"}}],"responses":{"204":{"description":"The todo has been deleted."},"400":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"},"404":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"The todo was not found."},"415":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"},"500":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"}},"summary":"Delete todo","tags":["Todos"],"x-apigen-tool":{"confirmation":"always","effect":"destructive","name":"delete_todo","output":{"mode":"empty"}}},"get":{"operationId":"getTodo","parameters":[{"example":"example","in":"path","name":"todo_id","required":true,"schema":{"type":"string"}}],"responses":{"200":{"content":{"application/json":{"example":{"id":"example","status":"example","title":"example"},"schema":{"$ref":"#/components/schemas/Todo"}}},"description":"The request has succeeded."},"400":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"},"404":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"The todo was not found."},"415":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"},"500":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"}},"summary":"Get todo","tags":["Todos"]}},"/todos/{todo_id}/complete":{"post":{"operationId":"completeTodo","parameters":[{"example":"example","in":"path","name":"todo_id","required":true,"schema":{"type":"string"}}],"responses":{"200":{"content":{"application/json":{"example":{"id":"example","status":"example","title":"example"},"schema":{"$ref":"#/components/schemas/Todo"}}},"description":"The todo has been completed."},"400":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"},"404":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"The todo was not found."},"415":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"},"500":{"content":{"application/json":{"example":{"code":"example","message":"example"},"schema":{"$ref":"#/components/schemas/Error"}}},"description":"Generated transport error"}},"summary":"Complete todo","tags":["Todos"]}}},"security":[{"BearerAuth":[]},{"ApiKeyAuth":[]}],"servers":[{"description":"Example development server","url":"http://127.0.0.1:8081/","variables":{}}],"tags":[{"description":"Todo lifecycle endpoints for the APIGen example.","name":"Todos"}]}`

// GetEmbeddedOpenAPISpec returns the canonical OpenAPI document as generic JSON map.
func GetEmbeddedOpenAPISpec() (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(embeddedOpenAPISpecJSON), &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

const embeddedAPIGenToolContractsJSON = `{"delete_todo":{"name":"delete_todo","operation_id":"deleteTodo","method":"DELETE","path":"/todos/{todo_id}","description":"Delete todo","effect":"destructive","confirmation":"always","input_schema":{"additionalProperties":false,"properties":{"todo_id":{"type":"string"}},"required":["todo_id"],"type":"object"},"output_schema":{"additionalProperties":false,"properties":{"status":{"type":"integer"}},"required":["status"],"type":"object"},"bindings":[{"argument":"todo_id","source":"path","wire_name":"todo_id","mode":"model","required":true,"schema":{"type":"string"}}],"output":{"mode":"empty"}},"list_todos":{"name":"list_todos","operation_id":"listTodos","method":"GET","path":"/todos","description":"List todos","effect":"read","confirmation":"never","tags":["todos","read"],"input_schema":{"additionalProperties":false,"properties":{"status":{"description":"Optional status filter.","type":"string"}},"type":"object"},"output_schema":{"additionalProperties":false,"properties":{"count":{"type":"integer"},"data":{"items":{"additionalProperties":false,"properties":{"id":{"type":"string"},"status":{"type":"string"},"title":{"type":"string"}},"required":["id","status","title"],"type":"object"},"type":"array"}},"required":["count","data"],"type":"object"},"bindings":[{"argument":"status","source":"query","wire_name":"status","mode":"model","description":"Optional status filter.","schema":{"type":"string"}}],"output":{"mode":"project","select":[{"source":"/data","target":"data","kind":"array","schema":{"type":"array","items":{"type":"object"}},"select":[{"source":"/id","target":"id","kind":"value","schema":{"type":"string"}},{"source":"/title","target":"title","kind":"value","schema":{"type":"string"}},{"source":"/status","target":"status","kind":"value","schema":{"type":"string"}}],"count_as":"count"}]}}}`

var genAPIGenToolContracts = func() map[string]apigenagenttool.Contract {
	contracts, err := apigenagenttool.DecodeContracts([]byte(embeddedAPIGenToolContractsJSON))
	if err != nil {
		panic(err)
	}
	return contracts
}()

// GetAPIGenToolContracts returns defensive copies of generated endpoint-derived tools.
func GetAPIGenToolContracts() map[string]apigenagenttool.Contract {
	out := make(map[string]apigenagenttool.Contract, len(genAPIGenToolContracts))
	for name, contract := range genAPIGenToolContracts {
		out[name] = apigenagenttool.CloneContract(contract)
	}
	return out
}

// GetAPIGenToolContract returns one generated endpoint-derived tool.
func GetAPIGenToolContract(name string) (apigenagenttool.Contract, bool) {
	contract, ok := genAPIGenToolContracts[name]
	if !ok {
		return apigenagenttool.Contract{}, false
	}
	return apigenagenttool.CloneContract(contract), true
}

// GenOperationContract captures APIGen-owned contract metadata for one operation.
type GenOperationContract struct {
	OperationID           string
	Method                string
	Path                  string
	Tags                  []string
	DocumentedStatusCodes []int
	RequestBodyRequired   bool
	AuthzMode             string
	Protected             bool
	Manual                bool
	Extensions            map[string]any
}

var genOperationContracts = map[string]GenOperationContract{
	"listTodos":    {OperationID: "listTodos", Method: "GET", Path: "/todos", Tags: []string{"Todos"}, DocumentedStatusCodes: []int{200, 400, 415, 500}, RequestBodyRequired: false, AuthzMode: "", Protected: false, Manual: false, Extensions: nil},
	"createTodo":   {OperationID: "createTodo", Method: "POST", Path: "/todos", Tags: []string{"Todos"}, DocumentedStatusCodes: []int{201, 400, 415, 500}, RequestBodyRequired: true, AuthzMode: "", Protected: false, Manual: false, Extensions: nil},
	"deleteTodo":   {OperationID: "deleteTodo", Method: "DELETE", Path: "/todos/{todo_id}", Tags: []string{"Todos"}, DocumentedStatusCodes: []int{204, 400, 404, 415, 500}, RequestBodyRequired: false, AuthzMode: "", Protected: false, Manual: false, Extensions: nil},
	"getTodo":      {OperationID: "getTodo", Method: "GET", Path: "/todos/{todo_id}", Tags: []string{"Todos"}, DocumentedStatusCodes: []int{200, 400, 404, 415, 500}, RequestBodyRequired: false, AuthzMode: "", Protected: false, Manual: false, Extensions: nil},
	"completeTodo": {OperationID: "completeTodo", Method: "POST", Path: "/todos/{todo_id}/complete", Tags: []string{"Todos"}, DocumentedStatusCodes: []int{200, 400, 404, 415, 500}, RequestBodyRequired: false, AuthzMode: "", Protected: false, Manual: false, Extensions: nil},
}

// GetAPIGenOperationContracts returns a defensive copy of the generated contract registry.
func GetAPIGenOperationContracts() map[string]GenOperationContract {
	out := make(map[string]GenOperationContract, len(genOperationContracts))
	for operationID, contract := range genOperationContracts {
		out[operationID] = cloneAPIGenOperationContract(contract)
	}
	return out
}

// GetAPIGenOperationContract returns generated contract metadata for a single operation.
func GetAPIGenOperationContract(operationID string) (GenOperationContract, bool) {
	contract, ok := genOperationContracts[operationID]
	if !ok {
		return GenOperationContract{}, false
	}
	return cloneAPIGenOperationContract(contract), true
}

// APIGenOperationAllowsStatus reports whether a status code is documented for an operation.
//
//nolint:revive // exported generated helper name matches the APIGen contract registry namespace.
func APIGenOperationAllowsStatus(operationID string, statusCode int) bool {
	contract, ok := genOperationContracts[operationID]
	if !ok {
		return false
	}
	for _, documented := range contract.DocumentedStatusCodes {
		if documented == statusCode {
			return true
		}
	}
	return false
}

func cloneAPIGenOperationContract(contract GenOperationContract) GenOperationContract {
	contract.Tags = append([]string(nil), contract.Tags...)
	contract.DocumentedStatusCodes = append([]int(nil), contract.DocumentedStatusCodes...)
	contract.Extensions = cloneAPIGenAnyMap(contract.Extensions)
	return contract
}

func cloneAPIGenAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneAPIGenAny(value)
	}
	return out
}

func cloneAPIGenAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAPIGenAnyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAPIGenAny(item)
		}
		return out
	default:
		return typed
	}
}

// GenServerInterface dispatches generated operations.
type GenServerInterface interface {
	HandleAPIGen(operationID string, w http.ResponseWriter, r *http.Request)
}

// RegisterAPIGenRoutes mounts generated routes on the supported Chi runtime boundary.
func RegisterAPIGenRoutes(router apigenchi.Router, server GenServerInterface) {
	apigenchi.RegisterRoutes(router, []apigenchi.Route{
		{Method: "GET", Path: "/todos", OperationID: "listTodos"},
		{Method: "POST", Path: "/todos", OperationID: "createTodo"},
		{Method: "DELETE", Path: "/todos/{todo_id}", OperationID: "deleteTodo"},
		{Method: "GET", Path: "/todos/{todo_id}", OperationID: "getTodo"},
		{Method: "POST", Path: "/todos/{todo_id}/complete", OperationID: "completeTodo"},
	}, server.HandleAPIGen)
}

// RegisterAPIGenStrictRoutes mounts generated routes backed by strict handlers.
func RegisterAPIGenStrictRoutes(router apigenchi.Router, handler GenStrictServerInterface, responder GenTransportErrorResponder) {
	RegisterAPIGenRoutes(router, genStrictAdapter{handler: handler, responder: responder})
}

// GenOperationDispatcher is the dispatch target for generated operations.
type GenOperationDispatcher interface {
	ListTodos(w http.ResponseWriter, r *http.Request, params GenListTodosParams)
	CreateTodo(w http.ResponseWriter, r *http.Request)
	DeleteTodo(w http.ResponseWriter, r *http.Request, todoId string)
	GetTodo(w http.ResponseWriter, r *http.Request, todoId string)
	CompleteTodo(w http.ResponseWriter, r *http.Request, todoId string)
}

// DispatchAPIGenOperation dispatches operation IDs to generated wrapper methods.
func DispatchAPIGenOperation(operationID string, dispatcher GenOperationDispatcher, responder GenTransportErrorResponder, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "listTodos":
		var err error
		var params GenListTodosParams
		err = apigenchi.BindQueryParameter(r.URL.Query(), "status", false, &params.Status)
		if err != nil {
			writeAPIGenError(responder, w, r, GenTransportError{OperationID: "listTodos", Kind: "query_parameter", StatusCode: 400, Code: "invalid_request", PublicDetail: "Invalid request.", Cause: err})
			return true
		}
		dispatcher.ListTodos(w, r, params)
		return true
	case "createTodo":
		dispatcher.CreateTodo(w, r)
		return true
	case "deleteTodo":
		var err error
		var todoId string
		err = apigenchi.BindPathParameter("todo_id", apigenchi.URLParam(r, "todo_id"), true, &todoId)
		if err != nil {
			writeAPIGenError(responder, w, r, GenTransportError{OperationID: "deleteTodo", Kind: "path_parameter", StatusCode: 400, Code: "invalid_request", PublicDetail: "Invalid request.", Cause: err})
			return true
		}
		dispatcher.DeleteTodo(w, r, todoId)
		return true
	case "getTodo":
		var err error
		var todoId string
		err = apigenchi.BindPathParameter("todo_id", apigenchi.URLParam(r, "todo_id"), true, &todoId)
		if err != nil {
			writeAPIGenError(responder, w, r, GenTransportError{OperationID: "getTodo", Kind: "path_parameter", StatusCode: 400, Code: "invalid_request", PublicDetail: "Invalid request.", Cause: err})
			return true
		}
		dispatcher.GetTodo(w, r, todoId)
		return true
	case "completeTodo":
		var err error
		var todoId string
		err = apigenchi.BindPathParameter("todo_id", apigenchi.URLParam(r, "todo_id"), true, &todoId)
		if err != nil {
			writeAPIGenError(responder, w, r, GenTransportError{OperationID: "completeTodo", Kind: "path_parameter", StatusCode: 400, Code: "invalid_request", PublicDetail: "Invalid request.", Cause: err})
			return true
		}
		dispatcher.CompleteTodo(w, r, todoId)
		return true
	default:
		return false
	}
}

// GenTransportError describes a transport-layer failure without prescribing its wire model.
type GenTransportError struct {
	OperationID  string
	Kind         string
	StatusCode   int
	Code         string
	PublicDetail string
	Cause        error
}

// GenTransportErrorResponder owns serialization of generated transport failures.
type GenTransportErrorResponder interface {
	RespondTransportError(ctx context.Context, w http.ResponseWriter, r *http.Request, failure GenTransportError)
}

func writeAPIGenError(responder GenTransportErrorResponder, w http.ResponseWriter, r *http.Request, failure GenTransportError) {
	if responder == nil {
		panic("apigen: nil transport error responder")
	}
	responder.RespondTransportError(r.Context(), w, r, failure)
}

func validateAPIGenContentType(r *http.Request, expected string) error {
	actual, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("parse Content-Type: %w", err)
	}
	want, _, err := mime.ParseMediaType(expected)
	if err != nil {
		return fmt.Errorf("parse generated Content-Type %q: %w", expected, err)
	}
	if !strings.EqualFold(actual, want) {
		return fmt.Errorf("unsupported Content-Type %q; expected %q", actual, want)
	}
	return nil
}

func decodeAPIGenJSONBody(body io.Reader, dest any, requiredBody bool, requiredFields ...string) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		if !requiredBody {
			return nil
		}
		return fmt.Errorf("request body must not be empty")
	}
	if len(requiredFields) > 0 {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err == nil {
			for _, field := range requiredFields {
				if _, ok := envelope[field]; !ok {
					return fmt.Errorf("%s is required", field)
				}
			}
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain a single JSON value")
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func decodeAPIGenTextBody(body io.Reader, requiredBody bool) (string, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("read request body: %w", err)
	}
	if len(raw) == 0 && requiredBody {
		return "", fmt.Errorf("request body must not be empty")
	}
	return string(raw), nil
}

func decodeAPIGenBytesBody(body io.Reader, requiredBody bool) ([]byte, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(raw) == 0 && requiredBody {
		return nil, fmt.Errorf("request body must not be empty")
	}
	return raw, nil
}

func decodeAPIGenFormBody(r *http.Request, dest any, requiredBody bool, requiredFields ...string) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("parse form body: %w", err)
	}
	if len(r.PostForm) == 0 {
		if !requiredBody {
			return nil
		}
		return fmt.Errorf("request body must not be empty")
	}
	for _, field := range requiredFields {
		if strings.TrimSpace(r.PostForm.Get(field)) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	payload := map[string]any{}
	for key, values := range r.PostForm {
		if len(values) == 1 {
			payload[key] = values[0]
		} else {
			payload[key] = values
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode form body: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return fmt.Errorf("invalid form body: %w", err)
	}
	return nil
}

// GenListTodosParams represents the APIGen strict query parameter contract for ListTodos.
type GenListTodosParams struct {
	Status *string
}

// GenListTodosRequest represents the APIGen strict request contract for ListTodos.
type GenListTodosRequest struct {
	Params GenListTodosParams
}

// GenListTodosResponse represents the APIGen strict response contract for ListTodos.
type GenListTodosResponse interface {
	VisitListTodosResponse(w http.ResponseWriter) error
}

// GenListTodos200ResponseHeaders represents the APIGen-owned response headers for generated concrete responses.
type GenListTodos200ResponseHeaders struct {
	XRateLimitLimit     int32
	XRateLimitRemaining int32
	XRateLimitReset     int64
}

// GenListTodos200JSONResponse is the APIGen concrete response for ListTodos 200.
type GenListTodos200JSONResponse struct {
	Body    GenSchemaListTodosResponse
	Headers GenListTodos200ResponseHeaders
}

// VisitListTodosResponse writes ListTodos 200 responses to the client.
func (response GenListTodos200JSONResponse) VisitListTodosResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("X-RateLimit-Limit", fmt.Sprint(response.Headers.XRateLimitLimit))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(response.Headers.XRateLimitRemaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprint(response.Headers.XRateLimitReset))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenListTodos400JSONResponse is the APIGen concrete response for ListTodos 400.
type GenListTodos400JSONResponse struct {
	Body GenSchemaError
}

// VisitListTodosResponse writes ListTodos 400 responses to the client.
func (response GenListTodos400JSONResponse) VisitListTodosResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(400)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenCreateTodoRequest represents the APIGen strict request contract for CreateTodo.
type GenCreateTodoRequest struct {
	Body *GenCreateTodoBody
}

// GenCreateTodoResponse represents the APIGen strict response contract for CreateTodo.
type GenCreateTodoResponse interface {
	VisitCreateTodoResponse(w http.ResponseWriter) error
}

// GenCreateTodo201ResponseHeaders represents the APIGen-owned response headers for generated concrete responses.
type GenCreateTodo201ResponseHeaders struct {
	XRateLimitLimit     int32
	XRateLimitRemaining int32
	XRateLimitReset     int64
}

// GenCreateTodo201JSONResponse is the APIGen concrete response for CreateTodo 201.
type GenCreateTodo201JSONResponse struct {
	Body    GenSchemaTodo
	Headers GenCreateTodo201ResponseHeaders
}

// VisitCreateTodoResponse writes CreateTodo 201 responses to the client.
func (response GenCreateTodo201JSONResponse) VisitCreateTodoResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("X-RateLimit-Limit", fmt.Sprint(response.Headers.XRateLimitLimit))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(response.Headers.XRateLimitRemaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprint(response.Headers.XRateLimitReset))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenCreateTodo400JSONResponse is the APIGen concrete response for CreateTodo 400.
type GenCreateTodo400JSONResponse struct {
	Body GenSchemaError
}

// VisitCreateTodoResponse writes CreateTodo 400 responses to the client.
func (response GenCreateTodo400JSONResponse) VisitCreateTodoResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(400)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenCreateTodoBody aliases the APIGen strict request body schema for CreateTodo.
type GenCreateTodoBody = GenSchemaCreateTodoRequest

// GenDeleteTodoRequest represents the APIGen strict request contract for DeleteTodo.
type GenDeleteTodoRequest struct {
	TodoId string
}

// GenDeleteTodoResponse represents the APIGen strict response contract for DeleteTodo.
type GenDeleteTodoResponse interface {
	VisitDeleteTodoResponse(w http.ResponseWriter) error
}

// GenDeleteTodo204ResponseHeaders represents the APIGen-owned response headers for generated concrete responses.
type GenDeleteTodo204ResponseHeaders struct {
	XRateLimitLimit     int32
	XRateLimitRemaining int32
	XRateLimitReset     int64
}

// GenDeleteTodo204Response is the APIGen concrete response for DeleteTodo 204.
type GenDeleteTodo204Response struct {
	Headers GenDeleteTodo204ResponseHeaders
}

// VisitDeleteTodoResponse writes DeleteTodo 204 responses to the client.
func (response GenDeleteTodo204Response) VisitDeleteTodoResponse(w http.ResponseWriter) error {
	w.Header().Set("X-RateLimit-Limit", fmt.Sprint(response.Headers.XRateLimitLimit))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(response.Headers.XRateLimitRemaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprint(response.Headers.XRateLimitReset))
	w.WriteHeader(204)
	return nil
}

// GenDeleteTodo404JSONResponse is the APIGen concrete response for DeleteTodo 404.
type GenDeleteTodo404JSONResponse struct {
	Body GenSchemaError
}

// VisitDeleteTodoResponse writes DeleteTodo 404 responses to the client.
func (response GenDeleteTodo404JSONResponse) VisitDeleteTodoResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(404)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenGetTodoRequest represents the APIGen strict request contract for GetTodo.
type GenGetTodoRequest struct {
	TodoId string
}

// GenGetTodoResponse represents the APIGen strict response contract for GetTodo.
type GenGetTodoResponse interface {
	VisitGetTodoResponse(w http.ResponseWriter) error
}

// GenGetTodo200ResponseHeaders represents the APIGen-owned response headers for generated concrete responses.
type GenGetTodo200ResponseHeaders struct {
	XRateLimitLimit     int32
	XRateLimitRemaining int32
	XRateLimitReset     int64
}

// GenGetTodo200JSONResponse is the APIGen concrete response for GetTodo 200.
type GenGetTodo200JSONResponse struct {
	Body    GenSchemaTodo
	Headers GenGetTodo200ResponseHeaders
}

// VisitGetTodoResponse writes GetTodo 200 responses to the client.
func (response GenGetTodo200JSONResponse) VisitGetTodoResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("X-RateLimit-Limit", fmt.Sprint(response.Headers.XRateLimitLimit))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(response.Headers.XRateLimitRemaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprint(response.Headers.XRateLimitReset))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenGetTodo404JSONResponse is the APIGen concrete response for GetTodo 404.
type GenGetTodo404JSONResponse struct {
	Body GenSchemaError
}

// VisitGetTodoResponse writes GetTodo 404 responses to the client.
func (response GenGetTodo404JSONResponse) VisitGetTodoResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(404)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenCompleteTodoRequest represents the APIGen strict request contract for CompleteTodo.
type GenCompleteTodoRequest struct {
	TodoId string
}

// GenCompleteTodoResponse represents the APIGen strict response contract for CompleteTodo.
type GenCompleteTodoResponse interface {
	VisitCompleteTodoResponse(w http.ResponseWriter) error
}

// GenCompleteTodo200ResponseHeaders represents the APIGen-owned response headers for generated concrete responses.
type GenCompleteTodo200ResponseHeaders struct {
	XRateLimitLimit     int32
	XRateLimitRemaining int32
	XRateLimitReset     int64
}

// GenCompleteTodo200JSONResponse is the APIGen concrete response for CompleteTodo 200.
type GenCompleteTodo200JSONResponse struct {
	Body    GenSchemaTodo
	Headers GenCompleteTodo200ResponseHeaders
}

// VisitCompleteTodoResponse writes CompleteTodo 200 responses to the client.
func (response GenCompleteTodo200JSONResponse) VisitCompleteTodoResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("X-RateLimit-Limit", fmt.Sprint(response.Headers.XRateLimitLimit))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(response.Headers.XRateLimitRemaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprint(response.Headers.XRateLimitReset))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenCompleteTodo404JSONResponse is the APIGen concrete response for CompleteTodo 404.
type GenCompleteTodo404JSONResponse struct {
	Body GenSchemaError
}

// VisitCompleteTodoResponse writes CompleteTodo 404 responses to the client.
func (response GenCompleteTodo404JSONResponse) VisitCompleteTodoResponse(w http.ResponseWriter) error {
	payload, err := json.Marshal(response.Body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(404)
	payload = append(payload, '\n')
	_, err = w.Write(payload)
	return err
}

// GenStrictServerInterface represents strict handlers for APIGen transport dispatch.
type GenStrictServerInterface interface {
	ListTodos(ctx context.Context, request GenListTodosRequest) (GenListTodosResponse, error)
	CreateTodo(ctx context.Context, request GenCreateTodoRequest) (GenCreateTodoResponse, error)
	DeleteTodo(ctx context.Context, request GenDeleteTodoRequest) (GenDeleteTodoResponse, error)
	GetTodo(ctx context.Context, request GenGetTodoRequest) (GenGetTodoResponse, error)
	CompleteTodo(ctx context.Context, request GenCompleteTodoRequest) (GenCompleteTodoResponse, error)
}

type genStrictAdapter struct {
	handler   GenStrictServerInterface
	responder GenTransportErrorResponder
}

func (a genStrictAdapter) HandleAPIGen(operationID string, w http.ResponseWriter, r *http.Request) {
	if ok := DispatchAPIGenStrictOperation(operationID, a.handler, a.responder, w, r); !ok {
		http.NotFound(w, r)
	}
}

type genStrictBridge struct {
	handler   GenStrictServerInterface
	responder GenTransportErrorResponder
}

func (b genStrictBridge) ListTodos(w http.ResponseWriter, r *http.Request, params GenListTodosParams) {
	var request GenListTodosRequest
	request.Params = params
	response, err := b.handler.ListTodos(r.Context(), request)
	if err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "listTodos", Kind: "handler", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
		return
	}
	if err := response.VisitListTodosResponse(w); err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "listTodos", Kind: "response_serialization", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
	}
}

func (b genStrictBridge) CreateTodo(w http.ResponseWriter, r *http.Request) {
	var request GenCreateTodoRequest
	var body GenCreateTodoBody
	if r.Header.Get("Content-Type") != "" {
		if err := validateAPIGenContentType(r, "application/json"); err != nil {
			writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "createTodo", Kind: "unsupported_media_type", StatusCode: 415, Code: "unsupported_media_type", PublicDetail: "Unsupported media type.", Cause: err})
			return
		}
	}
	if err := decodeAPIGenJSONBody(r.Body, &body, true, []string{"title"}...); err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "createTodo", Kind: "malformed_body", StatusCode: 400, Code: "invalid_request", PublicDetail: "Invalid request body.", Cause: err})
		return
	}
	request.Body = &body
	response, err := b.handler.CreateTodo(r.Context(), request)
	if err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "createTodo", Kind: "handler", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
		return
	}
	if err := response.VisitCreateTodoResponse(w); err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "createTodo", Kind: "response_serialization", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
	}
}

func (b genStrictBridge) DeleteTodo(w http.ResponseWriter, r *http.Request, todoId string) {
	var request GenDeleteTodoRequest
	request.TodoId = todoId
	response, err := b.handler.DeleteTodo(r.Context(), request)
	if err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "deleteTodo", Kind: "handler", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
		return
	}
	if err := response.VisitDeleteTodoResponse(w); err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "deleteTodo", Kind: "response_serialization", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
	}
}

func (b genStrictBridge) GetTodo(w http.ResponseWriter, r *http.Request, todoId string) {
	var request GenGetTodoRequest
	request.TodoId = todoId
	response, err := b.handler.GetTodo(r.Context(), request)
	if err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "getTodo", Kind: "handler", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
		return
	}
	if err := response.VisitGetTodoResponse(w); err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "getTodo", Kind: "response_serialization", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
	}
}

func (b genStrictBridge) CompleteTodo(w http.ResponseWriter, r *http.Request, todoId string) {
	var request GenCompleteTodoRequest
	request.TodoId = todoId
	response, err := b.handler.CompleteTodo(r.Context(), request)
	if err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "completeTodo", Kind: "handler", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
		return
	}
	if err := response.VisitCompleteTodoResponse(w); err != nil {
		writeAPIGenError(b.responder, w, r, GenTransportError{OperationID: "completeTodo", Kind: "response_serialization", StatusCode: 500, Code: "internal_error", PublicDetail: "Internal server error.", Cause: err})
	}
}

// DispatchAPIGenStrictOperation dispatches to strict handlers without oapi strict wrappers.
func DispatchAPIGenStrictOperation(operationID string, handler GenStrictServerInterface, responder GenTransportErrorResponder, w http.ResponseWriter, r *http.Request) bool {
	return DispatchAPIGenOperation(operationID, genStrictBridge{handler: handler, responder: responder}, responder, w, r)
}
