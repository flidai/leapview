package http

import (
	"context"
	"log/slog"
	stdhttp "net/http"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

type APIGenHandler interface {
	ListDashboardAuthoringCatalog(stdhttp.ResponseWriter, *stdhttp.Request, string)
	ExecuteDashboardAuthoringCommand(stdhttp.ResponseWriter, *stdhttp.Request, string, dashboardgen.GenExecuteDashboardAuthoringCommandHeaders)
	GetDashboardAuthoringDashboard(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDashboardAuthoringDraft(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	PreviewDashboardAuthoringDraft(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	GetDashboardAuthoringDraftRevision(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, string)
	GetDashboardAuthoringPublishedRevision(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	CreateDashboardAuthoringDraft(stdhttp.ResponseWriter, *stdhttp.Request, string, dashboardgen.GenCreateDashboardAuthoringDraftHeaders)
	ForkDashboardAuthoringDraft(stdhttp.ResponseWriter, *stdhttp.Request, string, dashboardgen.GenForkDashboardAuthoringDraftHeaders)
	ExportDashboardAuthoringSource(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	ListDashboardPublications(stdhttp.ResponseWriter, *stdhttp.Request, string)
	GetDashboardPublication(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	ResumeDashboardPublication(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	RotateDashboardPublication(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	SuspendDashboardPublication(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	ListDashboards(stdhttp.ResponseWriter, *stdhttp.Request)
	GetDashboard(stdhttp.ResponseWriter, *stdhttp.Request)
	GetDashboardPage(stdhttp.ResponseWriter, *stdhttp.Request)
	GetDashboardFilter(stdhttp.ResponseWriter, *stdhttp.Request)
	ListDashboardFilterValues(stdhttp.ResponseWriter, *stdhttp.Request, string)
	QueryDashboardPage(stdhttp.ResponseWriter, *stdhttp.Request, string)
	GetDashboardVisual(stdhttp.ResponseWriter, *stdhttp.Request)
	QueryDashboardVisualData(stdhttp.ResponseWriter, *stdhttp.Request, string)
	ListSemanticModels(stdhttp.ResponseWriter, *stdhttp.Request)
	GetSemanticModel(stdhttp.ResponseWriter, *stdhttp.Request)
	ListSemanticDatasets(stdhttp.ResponseWriter, *stdhttp.Request)
	GetSemanticDataset(stdhttp.ResponseWriter, *stdhttp.Request)
	ListSemanticFields(stdhttp.ResponseWriter, *stdhttp.Request)
	PreviewSemanticDataset(stdhttp.ResponseWriter, *stdhttp.Request, string)
	ExplainSemanticPreview(stdhttp.ResponseWriter, *stdhttp.Request)
	ListSemanticModelFields(stdhttp.ResponseWriter, *stdhttp.Request)
	QuerySemanticModel(stdhttp.ResponseWriter, *stdhttp.Request, string)
	ExplainSemanticModelQuery(stdhttp.ResponseWriter, *stdhttp.Request)
	ListSemanticRelationships(stdhttp.ResponseWriter, *stdhttp.Request)
	ListSemanticSources(stdhttp.ResponseWriter, *stdhttp.Request)
}

type APIGenDispatcher struct{ handler APIGenHandler }

func NewAPIGenDispatcher(handler APIGenHandler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) ListDashboardAuthoringCatalog(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace string) {
	d.handler.ListDashboardAuthoringCatalog(w, r, workspace)
}
func (d *APIGenDispatcher) ExecuteDashboardAuthoringCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace string, headers dashboardgen.GenExecuteDashboardAuthoringCommandHeaders) {
	d.handler.ExecuteDashboardAuthoringCommand(w, r, workspace, headers)
}
func (d *APIGenDispatcher) GetDashboardAuthoringDashboard(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, dashboard string) {
	d.handler.GetDashboardAuthoringDashboard(w, r, workspace, dashboard)
}
func (d *APIGenDispatcher) GetDashboardAuthoringDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, dashboard string) {
	d.handler.GetDashboardAuthoringDraft(w, r, workspace, dashboard)
}
func (d *APIGenDispatcher) PreviewDashboardAuthoringDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, dashboard, draft string) {
	d.handler.PreviewDashboardAuthoringDraft(w, r, workspace, dashboard, draft)
}
func (d *APIGenDispatcher) GetDashboardAuthoringDraftRevision(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, dashboard, draft, revision string) {
	d.handler.GetDashboardAuthoringDraftRevision(w, r, workspace, dashboard, draft, revision)
}
func (d *APIGenDispatcher) GetDashboardAuthoringPublishedRevision(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, dashboard, revision string) {
	d.handler.GetDashboardAuthoringPublishedRevision(w, r, workspace, dashboard, revision)
}
func (d *APIGenDispatcher) CreateDashboardAuthoringDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace string, headers dashboardgen.GenCreateDashboardAuthoringDraftHeaders) {
	d.handler.CreateDashboardAuthoringDraft(w, r, workspace, headers)
}
func (d *APIGenDispatcher) ForkDashboardAuthoringDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace string, headers dashboardgen.GenForkDashboardAuthoringDraftHeaders) {
	d.handler.ForkDashboardAuthoringDraft(w, r, workspace, headers)
}
func (d *APIGenDispatcher) ExportDashboardAuthoringSource(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, kind, dashboard string) {
	d.handler.ExportDashboardAuthoringSource(w, r, workspace, kind, dashboard)
}

func (d *APIGenDispatcher) ListDashboardPublications(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace string) {
	d.handler.ListDashboardPublications(w, r, workspace)
}
func (d *APIGenDispatcher) GetDashboardPublication(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, publication string) {
	d.handler.GetDashboardPublication(w, r, workspace, publication)
}
func (d *APIGenDispatcher) ResumeDashboardPublication(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, publication string, _ dashboardgen.GenResumeDashboardPublicationHeaders) {
	d.handler.ResumeDashboardPublication(w, r, workspace, publication)
}
func (d *APIGenDispatcher) RotateDashboardPublication(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, publication string, _ dashboardgen.GenRotateDashboardPublicationHeaders) {
	d.handler.RotateDashboardPublication(w, r, workspace, publication)
}
func (d *APIGenDispatcher) SuspendDashboardPublication(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, publication string, _ dashboardgen.GenSuspendDashboardPublicationHeaders) {
	d.handler.SuspendDashboardPublication(w, r, workspace, publication)
}
func (d *APIGenDispatcher) ListDashboards(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ dashboardgen.GenListDashboardsParams) {
	d.handler.ListDashboards(w, r)
}
func (d *APIGenDispatcher) GetDashboard(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetDashboard(w, r)
}
func (d *APIGenDispatcher) GetDashboardPage(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _ string) {
	d.handler.GetDashboardPage(w, r)
}
func (d *APIGenDispatcher) GetDashboardFilter(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _, _ string) {
	d.handler.GetDashboardFilter(w, r)
}
func (d *APIGenDispatcher) ListDashboardFilterValues(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, _, _, _ string, _ dashboardgen.GenListDashboardFilterValuesParams) {
	d.handler.ListDashboardFilterValues(w, r, workspace)
}
func (d *APIGenDispatcher) QueryDashboardPage(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, _, _ string) {
	d.handler.QueryDashboardPage(w, r, workspace)
}
func (d *APIGenDispatcher) GetDashboardVisual(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _, _ string) {
	d.handler.GetDashboardVisual(w, r)
}
func (d *APIGenDispatcher) QueryDashboardVisualData(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, _, _, _ string, _ dashboardgen.GenQueryDashboardVisualDataHeaders) {
	d.handler.QueryDashboardVisualData(w, r, workspace)
}
func (d *APIGenDispatcher) ListSemanticModels(w stdhttp.ResponseWriter, r *stdhttp.Request, _ string, _ dashboardgen.GenListSemanticModelsParams) {
	d.handler.ListSemanticModels(w, r)
}
func (d *APIGenDispatcher) GetSemanticModel(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.GetSemanticModel(w, r)
}
func (d *APIGenDispatcher) ListSemanticDatasets(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ dashboardgen.GenListSemanticDatasetsParams) {
	d.handler.ListSemanticDatasets(w, r)
}
func (d *APIGenDispatcher) GetSemanticDataset(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _ string) {
	d.handler.GetSemanticDataset(w, r)
}
func (d *APIGenDispatcher) ListSemanticFields(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _ string, _ dashboardgen.GenListSemanticFieldsParams) {
	d.handler.ListSemanticFields(w, r)
}
func (d *APIGenDispatcher) PreviewSemanticDataset(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, _, _ string, _ dashboardgen.GenPreviewSemanticDatasetHeaders) {
	d.handler.PreviewSemanticDataset(w, r, workspace)
}
func (d *APIGenDispatcher) ExplainSemanticPreview(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _, _ string) {
	d.handler.ExplainSemanticPreview(w, r)
}
func (d *APIGenDispatcher) ListSemanticModelFields(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ dashboardgen.GenListSemanticModelFieldsParams) {
	d.handler.ListSemanticModelFields(w, r)
}
func (d *APIGenDispatcher) QuerySemanticModel(w stdhttp.ResponseWriter, r *stdhttp.Request, workspace, _ string, _ dashboardgen.GenQuerySemanticModelHeaders) {
	d.handler.QuerySemanticModel(w, r, workspace)
}
func (d *APIGenDispatcher) ExplainSemanticModelQuery(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string) {
	d.handler.ExplainSemanticModelQuery(w, r)
}
func (d *APIGenDispatcher) ListSemanticRelationships(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ dashboardgen.GenListSemanticRelationshipsParams) {
	d.handler.ListSemanticRelationships(w, r)
}
func (d *APIGenDispatcher) ListSemanticSources(w stdhttp.ResponseWriter, r *stdhttp.Request, _, _ string, _ dashboardgen.GenListSemanticSourcesParams) {
	d.handler.ListSemanticSources(w, r)
}

type APIGenTransportErrorResponder struct{ Logger *slog.Logger }

func (responder APIGenTransportErrorResponder) RespondTransportError(ctx context.Context, w stdhttp.ResponseWriter, r *stdhttp.Request, failure dashboardgen.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func DispatchAPIGenOperation(operationID string, handler APIGenHandler, logger *slog.Logger, w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	return dashboardgen.DispatchAPIGenOperation(
		operationID, NewAPIGenDispatcher(handler), APIGenTransportErrorResponder{Logger: logger}, w, r,
	)
}
