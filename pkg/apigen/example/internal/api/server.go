package api

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/example/apigen-example/internal/api/gen"
)

// Server implements the generated strict todo API.
type Server struct {
	mu     sync.Mutex
	order  []string
	todos  map[string]gen.Todo
	nextID int
}

var _ gen.GenStrictServerInterface = (*Server)(nil)

// NewServer returns a seeded in-memory todo server for the example.
func NewServer() *Server {
	return &Server{
		order: []string{"todo-1", "todo-2"},
		todos: map[string]gen.Todo{
			"todo-1": {Id: "todo-1", Title: "write docs", Status: "open"},
			"todo-2": {Id: "todo-2", Title: "ship example", Status: "completed"},
		},
		nextID: 3,
	}
}

func (s *Server) ListTodos(_ context.Context, request gen.GenListTodosRequest) (gen.GenListTodosResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	statusFilter := ""
	if request.Params.Status != nil {
		statusFilter = strings.TrimSpace(*request.Params.Status)
		if statusFilter != "" && statusFilter != "open" && statusFilter != "completed" {
			return gen.GenListTodos400JSONResponse{
				Body: gen.Error{Code: "invalid_status", Message: "status must be open or completed"},
			}, nil
		}
	}

	response := gen.ListTodosResponse{Data: make([]gen.Todo, 0, len(s.order))}
	for _, id := range s.order {
		todo := s.todos[id]
		if statusFilter != "" && todo.Status != statusFilter {
			continue
		}
		response.Data = append(response.Data, todo)
	}

	return gen.GenListTodos200JSONResponse{Body: response}, nil
}

func (s *Server) CreateTodo(_ context.Context, request gen.GenCreateTodoRequest) (gen.GenCreateTodoResponse, error) {
	if request.Body == nil {
		return gen.GenCreateTodo400JSONResponse{
			Body: gen.Error{Code: "invalid_request", Message: "request body is required"},
		}, nil
	}

	title := strings.TrimSpace(request.Body.Title)
	if title == "" {
		return gen.GenCreateTodo400JSONResponse{
			Body: gen.Error{Code: "invalid_title", Message: "title is required"},
		}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("todo-%d", s.nextID)
	s.nextID++
	todo := gen.Todo{Id: id, Title: title, Status: "open"}
	s.todos[id] = todo
	s.order = append(s.order, id)

	return gen.GenCreateTodo201JSONResponse{Body: todo}, nil
}

func (s *Server) GetTodo(_ context.Context, request gen.GenGetTodoRequest) (gen.GenGetTodoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	todo, ok := s.todos[request.TodoId]
	if !ok {
		return getTodoNotFoundResponse(request.TodoId), nil
	}
	return gen.GenGetTodo200JSONResponse{Body: todo}, nil
}

func (s *Server) CompleteTodo(_ context.Context, request gen.GenCompleteTodoRequest) (gen.GenCompleteTodoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	todo, ok := s.todos[request.TodoId]
	if !ok {
		return gen.GenCompleteTodo404JSONResponse{
			Body: gen.Error{Code: "not_found", Message: "todo not found"},
		}, nil
	}
	todo.Status = "completed"
	s.todos[request.TodoId] = todo
	return gen.GenCompleteTodo200JSONResponse{Body: todo}, nil
}

func (s *Server) DeleteTodo(_ context.Context, request gen.GenDeleteTodoRequest) (gen.GenDeleteTodoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.todos[request.TodoId]; !ok {
		return gen.GenDeleteTodo404JSONResponse{
			Body: gen.Error{Code: "not_found", Message: "todo not found"},
		}, nil
	}

	delete(s.todos, request.TodoId)
	nextOrder := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if id != request.TodoId {
			nextOrder = append(nextOrder, id)
		}
	}
	s.order = nextOrder

	return gen.GenDeleteTodo204Response{}, nil
}

func getTodoNotFoundResponse(todoID string) gen.GenGetTodo404JSONResponse {
	message := "todo not found"
	if strings.TrimSpace(todoID) == "" {
		message = "todo id is required"
	}
	return gen.GenGetTodo404JSONResponse{
		Body: gen.Error{Code: "not_found", Message: message},
	}
}
