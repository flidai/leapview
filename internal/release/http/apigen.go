package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
)

type APIGenHandler interface {
	CreateRelease(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	ListReleases(stdhttp.ResponseWriter, *stdhttp.Request, string, *int32, *string)
	GetRelease(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	FinalizeRelease(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	ListReleaseEvents(stdhttp.ResponseWriter, *stdhttp.Request, string, string, *int32, *string)
}

type APIGenDispatcher struct{ handler APIGenHandler }

func NewAPIGenDispatcher(handler APIGenHandler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) CreateRelease(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers releasegen.GenCreateReleaseHeaders) {
	d.handler.CreateRelease(w, r, project, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) ListReleases(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, params releasegen.GenListReleasesParams) {
	d.handler.ListReleases(w, r, project, params.Limit, params.PageToken)
}

func (d *APIGenDispatcher) GetRelease(w stdhttp.ResponseWriter, r *stdhttp.Request, project, releaseID string) {
	d.handler.GetRelease(w, r, project, releaseID)
}

func (d *APIGenDispatcher) FinalizeRelease(w stdhttp.ResponseWriter, r *stdhttp.Request, project, releaseID string, headers releasegen.GenFinalizeReleaseHeaders) {
	d.handler.FinalizeRelease(w, r, project, releaseID, headers.IdempotencyKey)
}

func (d *APIGenDispatcher) ListReleaseEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, project, releaseID string, params releasegen.GenListReleaseEventsParams, _ releasegen.GenListReleaseEventsHeaders) {
	d.handler.ListReleaseEvents(w, r, project, releaseID, params.Limit, params.PageToken)
}

type APIGenTransportErrorResponder struct{ Logger *slog.Logger }

func (responder APIGenTransportErrorResponder) RespondTransportError(ctx context.Context, w stdhttp.ResponseWriter, r *stdhttp.Request, failure releasegen.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func DispatchAPIGenOperation(operationID string, handler APIGenHandler, logger *slog.Logger, w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	return releasegen.DispatchAPIGenOperation(
		operationID, NewAPIGenDispatcher(handler), APIGenTransportErrorResponder{Logger: logger}, w, r,
	)
}
