package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgen "github.com/flidai/leapview/internal/project/api/gen"
)

// Handler is the runtime port consumed by Project's generated HTTP adapter.
// A provider may implement it without turning the compile-time Project
// capability into a synthetic runtime module.
type Handler interface {
	ListProjects(stdhttp.ResponseWriter, *stdhttp.Request, *int32, *string)
	GetProject(stdhttp.ResponseWriter, *stdhttp.Request, string)
	Search(stdhttp.ResponseWriter, *stdhttp.Request, projectgen.GenSearchParams)
}

func (d *APIGenDispatcher) Search(w stdhttp.ResponseWriter, r *stdhttp.Request, params projectgen.GenSearchParams) {
	d.handler.Search(w, r, params)
}

type APIGenDispatcher struct {
	handler Handler
}

func NewAPIGenDispatcher(handler Handler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) ListProjects(w stdhttp.ResponseWriter, r *stdhttp.Request, params projectgen.GenListProjectsParams) {
	d.handler.ListProjects(w, r, params.Limit, params.PageToken)
}

func (d *APIGenDispatcher) GetProject(w stdhttp.ResponseWriter, r *stdhttp.Request, project string) {
	d.handler.GetProject(w, r, project)
}

type APIGenTransportErrorResponder struct {
	Logger *slog.Logger
}

func (responder APIGenTransportErrorResponder) RespondTransportError(ctx context.Context, w stdhttp.ResponseWriter, r *stdhttp.Request, failure projectgen.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func DispatchAPIGenOperation(operationID string, handler Handler, logger *slog.Logger, w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	return projectgen.DispatchAPIGenOperation(
		operationID,
		NewAPIGenDispatcher(handler),
		APIGenTransportErrorResponder{Logger: logger},
		w,
		r,
	)
}
