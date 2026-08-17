package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectapi "github.com/flidai/leapview/internal/project/api"
	projectgen "github.com/flidai/leapview/internal/project/api/gen"
)

// Handler is the runtime port consumed by Project's generated HTTP adapter.
// A provider may implement it without turning the compile-time Project
// capability into a synthetic runtime module.
type Handler interface {
	GetProject(stdhttp.ResponseWriter, *stdhttp.Request, string)
	Search(stdhttp.ResponseWriter, *stdhttp.Request, projectapi.SearchParams)
}

func (d *APIGenDispatcher) Search(w stdhttp.ResponseWriter, r *stdhttp.Request, params projectgen.GenSearchParams) {
	var kindPointer *[]projectapi.SearchKind
	if params.Kind != nil {
		kinds := make([]projectapi.SearchKind, len(*params.Kind))
		for index, kind := range *params.Kind {
			kinds[index] = projectapi.SearchKind(kind)
		}
		kindPointer = &kinds
	}
	d.handler.Search(w, r, projectapi.SearchParams{
		Q: params.Q, Kind: kindPointer, Domain: params.Domain, Limit: params.Limit, Cursor: params.Cursor,
	})
}

type APIGenDispatcher struct {
	handler Handler
}

func NewAPIGenDispatcher(handler Handler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
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
