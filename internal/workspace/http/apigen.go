package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	workspaceapi "github.com/flidai/leapview/internal/workspace/api"
	workspacegen "github.com/flidai/leapview/internal/workspace/api/gen"
)

type APIGenSearchParams = workspaceapi.SearchParams

type APIGenHandler interface {
	Search(stdhttp.ResponseWriter, *stdhttp.Request, APIGenSearchParams)
	ListWorkspaces(stdhttp.ResponseWriter, *stdhttp.Request)
	GetWorkspace(stdhttp.ResponseWriter, *stdhttp.Request, string)
	GetWorkspaceAdministration(stdhttp.ResponseWriter, *stdhttp.Request, string)
	GetWorkspaceActiveAssetGraph(stdhttp.ResponseWriter, *stdhttp.Request)
	ListWorkspaceAssetEdges(stdhttp.ResponseWriter, *stdhttp.Request)
	ListWorkspaceAssets(stdhttp.ResponseWriter, *stdhttp.Request)
	GetWorkspaceAsset(stdhttp.ResponseWriter, *stdhttp.Request)
	GetWorkspaceAssetLineage(stdhttp.ResponseWriter, *stdhttp.Request)
	UpdateDashboardAppearance(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
}

type APIGenDispatcher struct{ handler APIGenHandler }

func NewAPIGenDispatcher(handler APIGenHandler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) Search(w stdhttp.ResponseWriter, r *stdhttp.Request, params workspacegen.GenSearchParams) {
	var types *[]string
	if params.Type != nil {
		values := make([]string, len(*params.Type))
		for i, value := range *params.Type {
			values[i] = string(value)
		}
		types = &values
	}
	d.handler.Search(w, r, APIGenSearchParams{
		Query: params.Q, Workspaces: params.Workspace, Types: types,
		ContextWorkspace: params.ContextWorkspace, ContextDashboard: params.ContextDashboard,
		ContextPage: params.ContextPage, Limit: params.Limit, PageToken: params.PageToken,
	})
}

func (d *APIGenDispatcher) ListWorkspaces(w stdhttp.ResponseWriter, r *stdhttp.Request, _ workspacegen.GenListWorkspacesParams) {
	d.handler.ListWorkspaces(w, r)
}

func (d *APIGenDispatcher) GetWorkspace(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace string) {
	d.handler.GetWorkspace(w, r, workspace)
}

func (d *APIGenDispatcher) GetWorkspaceAdministration(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace string) {
	d.handler.GetWorkspaceAdministration(w, r, workspace)
}

func (d *APIGenDispatcher) GetWorkspaceActiveAssetGraph(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string) {
	d.handler.GetWorkspaceActiveAssetGraph(w, r)
}

func (d *APIGenDispatcher) ListWorkspaceAssetEdges(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ workspacegen.GenListWorkspaceAssetEdgesParams) {
	d.handler.ListWorkspaceAssetEdges(w, r)
}

func (d *APIGenDispatcher) ListWorkspaceAssets(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ workspacegen.GenListWorkspaceAssetsParams) {
	d.handler.ListWorkspaceAssets(w, r)
}

func (d *APIGenDispatcher) GetWorkspaceAsset(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetWorkspaceAsset(w, r)
}

func (d *APIGenDispatcher) GetWorkspaceAssetLineage(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetWorkspaceAssetLineage(w, r)
}

func (d *APIGenDispatcher) UpdateDashboardAppearance(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, dashboard string) {
	d.handler.UpdateDashboardAppearance(w, r, workspace, dashboard)
}

type APIGenTransportErrorResponder struct{ Logger *slog.Logger }

func (responder APIGenTransportErrorResponder) RespondTransportError(ctx context.Context, w stdhttp.ResponseWriter, r *stdhttp.Request, failure workspacegen.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func DispatchAPIGenOperation(operationID string, handler APIGenHandler, logger *slog.Logger, w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	return workspacegen.DispatchAPIGenOperation(
		operationID, NewAPIGenDispatcher(handler), APIGenTransportErrorResponder{Logger: logger}, w, r,
	)
}
