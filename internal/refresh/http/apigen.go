package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
)

type APIGenHandler interface {
	ListRefreshRuns(stdhttp.ResponseWriter, *stdhttp.Request)
	CreateRefreshRun(stdhttp.ResponseWriter, *stdhttp.Request)
	GetRefreshRun(stdhttp.ResponseWriter, *stdhttp.Request, string)
	CancelRefreshRun(stdhttp.ResponseWriter, *stdhttp.Request, string)
	ListRefreshRunEvents(stdhttp.ResponseWriter, *stdhttp.Request, string, *int32, *string)
}

type APIGenDispatcher struct {
	handler APIGenHandler
}

func NewAPIGenDispatcher(handler APIGenHandler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) ListRefreshRuns(w stdhttp.ResponseWriter, r *stdhttp.Request, _ refreshgen.GenListRefreshRunsParams) {
	d.handler.ListRefreshRuns(w, r)
}

func (d *APIGenDispatcher) CreateRefreshRun(w stdhttp.ResponseWriter, r *stdhttp.Request, _ refreshgen.GenCreateRefreshRunHeaders) {
	d.handler.CreateRefreshRun(w, r)
}

func (d *APIGenDispatcher) GetRefreshRun(w stdhttp.ResponseWriter, r *stdhttp.Request, run string) {
	d.handler.GetRefreshRun(w, r, run)
}

func (d *APIGenDispatcher) CancelRefreshRun(w stdhttp.ResponseWriter, r *stdhttp.Request, run string, _ refreshgen.GenCancelRefreshRunHeaders) {
	d.handler.CancelRefreshRun(w, r, run)
}

func (d *APIGenDispatcher) ListRefreshRunEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, run string, params refreshgen.GenListRefreshRunEventsParams, _ refreshgen.GenListRefreshRunEventsHeaders) {
	d.handler.ListRefreshRunEvents(w, r, run, params.Limit, params.PageToken)
}

type APIGenTransportErrorResponder struct {
	Logger *slog.Logger
}

func (responder APIGenTransportErrorResponder) RespondTransportError(ctx context.Context, w stdhttp.ResponseWriter, r *stdhttp.Request, failure refreshgen.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func DispatchAPIGenOperation(operationID string, handler APIGenHandler, logger *slog.Logger, w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	return refreshgen.DispatchAPIGenOperation(
		operationID,
		NewAPIGenDispatcher(handler),
		APIGenTransportErrorResponder{Logger: logger},
		w,
		r,
	)
}
