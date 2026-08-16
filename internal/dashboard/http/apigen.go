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
	ResumeDashboardPublication(stdhttp.ResponseWriter, *stdhttp.Request, string, string, dashboardgen.GenResumeDashboardPublicationHeaders)
	RotateDashboardPublication(stdhttp.ResponseWriter, *stdhttp.Request, string, string, dashboardgen.GenRotateDashboardPublicationHeaders)
	SuspendDashboardPublication(stdhttp.ResponseWriter, *stdhttp.Request, string, string, dashboardgen.GenSuspendDashboardPublicationHeaders)
	ListDashboards(stdhttp.ResponseWriter, *stdhttp.Request, dashboardgen.GenListDashboardsParams)
	GetDashboard(stdhttp.ResponseWriter, *stdhttp.Request, string)
	UpdateDashboardAppearance(stdhttp.ResponseWriter, *stdhttp.Request, string)
	GetDashboardPage(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDashboardFilter(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	ListDashboardFilterValues(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, dashboardgen.GenListDashboardFilterValuesParams)
	QueryDashboardPage(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	GetDashboardVisual(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string)
	QueryDashboardVisualData(stdhttp.ResponseWriter, *stdhttp.Request, string, string, string, dashboardgen.GenQueryDashboardVisualDataHeaders)
	ListSemanticModels(stdhttp.ResponseWriter, *stdhttp.Request, dashboardgen.GenListSemanticModelsParams)
	GetSemanticModel(stdhttp.ResponseWriter, *stdhttp.Request, string)
	ListSemanticDatasets(stdhttp.ResponseWriter, *stdhttp.Request, string, dashboardgen.GenListSemanticDatasetsParams)
	GetSemanticDataset(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	ListSemanticFields(stdhttp.ResponseWriter, *stdhttp.Request, string, string, dashboardgen.GenListSemanticFieldsParams)
	PreviewSemanticDataset(stdhttp.ResponseWriter, *stdhttp.Request, string, string, dashboardgen.GenPreviewSemanticDatasetHeaders)
	ExplainSemanticPreview(stdhttp.ResponseWriter, *stdhttp.Request, string, string)
	ListSemanticModelFields(stdhttp.ResponseWriter, *stdhttp.Request, string, dashboardgen.GenListSemanticModelFieldsParams)
	QuerySemanticModel(stdhttp.ResponseWriter, *stdhttp.Request, string, dashboardgen.GenQuerySemanticModelHeaders)
	ExplainSemanticModelQuery(stdhttp.ResponseWriter, *stdhttp.Request, string)
	ListSemanticRelationships(stdhttp.ResponseWriter, *stdhttp.Request, string, dashboardgen.GenListSemanticRelationshipsParams)
	ListSemanticSources(stdhttp.ResponseWriter, *stdhttp.Request, string, dashboardgen.GenListSemanticSourcesParams)
}

type APIGenDispatcher struct{ handler APIGenHandler }

func NewAPIGenDispatcher(handler APIGenHandler) *APIGenDispatcher {
	return &APIGenDispatcher{handler: handler}
}

func (d *APIGenDispatcher) ListDashboardAuthoringCatalog(w stdhttp.ResponseWriter, r *stdhttp.Request, project string) {
	d.handler.ListDashboardAuthoringCatalog(w, r, project)
}
func (d *APIGenDispatcher) ExecuteDashboardAuthoringCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers dashboardgen.GenExecuteDashboardAuthoringCommandHeaders) {
	d.handler.ExecuteDashboardAuthoringCommand(w, r, project, headers)
}
func (d *APIGenDispatcher) GetDashboardAuthoringDashboard(w stdhttp.ResponseWriter, r *stdhttp.Request, project, dashboard string) {
	d.handler.GetDashboardAuthoringDashboard(w, r, project, dashboard)
}
func (d *APIGenDispatcher) GetDashboardAuthoringDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, project, dashboard string) {
	d.handler.GetDashboardAuthoringDraft(w, r, project, dashboard)
}
func (d *APIGenDispatcher) PreviewDashboardAuthoringDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, project, dashboard, draft string) {
	d.handler.PreviewDashboardAuthoringDraft(w, r, project, dashboard, draft)
}
func (d *APIGenDispatcher) GetDashboardAuthoringDraftRevision(w stdhttp.ResponseWriter, r *stdhttp.Request, project, dashboard, draft, revision string) {
	d.handler.GetDashboardAuthoringDraftRevision(w, r, project, dashboard, draft, revision)
}
func (d *APIGenDispatcher) GetDashboardAuthoringPublishedRevision(w stdhttp.ResponseWriter, r *stdhttp.Request, project, dashboard, revision string) {
	d.handler.GetDashboardAuthoringPublishedRevision(w, r, project, dashboard, revision)
}
func (d *APIGenDispatcher) CreateDashboardAuthoringDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers dashboardgen.GenCreateDashboardAuthoringDraftHeaders) {
	d.handler.CreateDashboardAuthoringDraft(w, r, project, headers)
}
func (d *APIGenDispatcher) ForkDashboardAuthoringDraft(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers dashboardgen.GenForkDashboardAuthoringDraftHeaders) {
	d.handler.ForkDashboardAuthoringDraft(w, r, project, headers)
}
func (d *APIGenDispatcher) ExportDashboardAuthoringSource(w stdhttp.ResponseWriter, r *stdhttp.Request, project, kind, dashboard string) {
	d.handler.ExportDashboardAuthoringSource(w, r, project, kind, dashboard)
}

func (d *APIGenDispatcher) ListDashboardPublications(w stdhttp.ResponseWriter, r *stdhttp.Request, project string) {
	d.handler.ListDashboardPublications(w, r, project)
}
func (d *APIGenDispatcher) GetDashboardPublication(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication string) {
	d.handler.GetDashboardPublication(w, r, project, publication)
}
func (d *APIGenDispatcher) ResumeDashboardPublication(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication string, headers dashboardgen.GenResumeDashboardPublicationHeaders) {
	d.handler.ResumeDashboardPublication(w, r, project, publication, headers)
}
func (d *APIGenDispatcher) RotateDashboardPublication(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication string, headers dashboardgen.GenRotateDashboardPublicationHeaders) {
	d.handler.RotateDashboardPublication(w, r, project, publication, headers)
}
func (d *APIGenDispatcher) SuspendDashboardPublication(w stdhttp.ResponseWriter, r *stdhttp.Request, project, publication string, headers dashboardgen.GenSuspendDashboardPublicationHeaders) {
	d.handler.SuspendDashboardPublication(w, r, project, publication, headers)
}
func (d *APIGenDispatcher) ListDashboards(w stdhttp.ResponseWriter, r *stdhttp.Request, params dashboardgen.GenListDashboardsParams) {
	d.handler.ListDashboards(w, r, params)
}
func (d *APIGenDispatcher) GetDashboard(w stdhttp.ResponseWriter, r *stdhttp.Request, dashboard string) {
	d.handler.GetDashboard(w, r, dashboard)
}
func (d *APIGenDispatcher) UpdateDashboardAppearance(w stdhttp.ResponseWriter, r *stdhttp.Request, dashboard string) {
	d.handler.UpdateDashboardAppearance(w, r, dashboard)
}
func (d *APIGenDispatcher) GetDashboardPage(w stdhttp.ResponseWriter, r *stdhttp.Request, dashboard, page string) {
	d.handler.GetDashboardPage(w, r, dashboard, page)
}
func (d *APIGenDispatcher) GetDashboardFilter(w stdhttp.ResponseWriter, r *stdhttp.Request, dashboard, page, filter string) {
	d.handler.GetDashboardFilter(w, r, dashboard, page, filter)
}
func (d *APIGenDispatcher) ListDashboardFilterValues(w stdhttp.ResponseWriter, r *stdhttp.Request, dashboard, page, filter string, params dashboardgen.GenListDashboardFilterValuesParams) {
	d.handler.ListDashboardFilterValues(w, r, dashboard, page, filter, params)
}
func (d *APIGenDispatcher) QueryDashboardPage(w stdhttp.ResponseWriter, r *stdhttp.Request, dashboard, page string) {
	d.handler.QueryDashboardPage(w, r, dashboard, page)
}
func (d *APIGenDispatcher) GetDashboardVisual(w stdhttp.ResponseWriter, r *stdhttp.Request, dashboard, page, visual string) {
	d.handler.GetDashboardVisual(w, r, dashboard, page, visual)
}
func (d *APIGenDispatcher) QueryDashboardVisualData(w stdhttp.ResponseWriter, r *stdhttp.Request, dashboard, page, visual string, headers dashboardgen.GenQueryDashboardVisualDataHeaders) {
	d.handler.QueryDashboardVisualData(w, r, dashboard, page, visual, headers)
}
func (d *APIGenDispatcher) ListSemanticModels(w stdhttp.ResponseWriter, r *stdhttp.Request, params dashboardgen.GenListSemanticModelsParams) {
	d.handler.ListSemanticModels(w, r, params)
}
func (d *APIGenDispatcher) GetSemanticModel(w stdhttp.ResponseWriter, r *stdhttp.Request, model string) {
	d.handler.GetSemanticModel(w, r, model)
}
func (d *APIGenDispatcher) ListSemanticDatasets(w stdhttp.ResponseWriter, r *stdhttp.Request, model string, params dashboardgen.GenListSemanticDatasetsParams) {
	d.handler.ListSemanticDatasets(w, r, model, params)
}
func (d *APIGenDispatcher) GetSemanticDataset(w stdhttp.ResponseWriter, r *stdhttp.Request, model, dataset string) {
	d.handler.GetSemanticDataset(w, r, model, dataset)
}
func (d *APIGenDispatcher) ListSemanticFields(w stdhttp.ResponseWriter, r *stdhttp.Request, model, dataset string, params dashboardgen.GenListSemanticFieldsParams) {
	d.handler.ListSemanticFields(w, r, model, dataset, params)
}
func (d *APIGenDispatcher) PreviewSemanticDataset(w stdhttp.ResponseWriter, r *stdhttp.Request, model, dataset string, headers dashboardgen.GenPreviewSemanticDatasetHeaders) {
	d.handler.PreviewSemanticDataset(w, r, model, dataset, headers)
}
func (d *APIGenDispatcher) ExplainSemanticPreview(w stdhttp.ResponseWriter, r *stdhttp.Request, model, dataset string) {
	d.handler.ExplainSemanticPreview(w, r, model, dataset)
}
func (d *APIGenDispatcher) ListSemanticModelFields(w stdhttp.ResponseWriter, r *stdhttp.Request, model string, params dashboardgen.GenListSemanticModelFieldsParams) {
	d.handler.ListSemanticModelFields(w, r, model, params)
}
func (d *APIGenDispatcher) QuerySemanticModel(w stdhttp.ResponseWriter, r *stdhttp.Request, model string, headers dashboardgen.GenQuerySemanticModelHeaders) {
	d.handler.QuerySemanticModel(w, r, model, headers)
}
func (d *APIGenDispatcher) ExplainSemanticModelQuery(w stdhttp.ResponseWriter, r *stdhttp.Request, model string) {
	d.handler.ExplainSemanticModelQuery(w, r, model)
}
func (d *APIGenDispatcher) ListSemanticRelationships(w stdhttp.ResponseWriter, r *stdhttp.Request, model string, params dashboardgen.GenListSemanticRelationshipsParams) {
	d.handler.ListSemanticRelationships(w, r, model, params)
}
func (d *APIGenDispatcher) ListSemanticSources(w stdhttp.ResponseWriter, r *stdhttp.Request, model string, params dashboardgen.GenListSemanticSourcesParams) {
	d.handler.ListSemanticSources(w, r, model, params)
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
