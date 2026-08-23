package module

import (
	"log/slog"
	"net/http"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
)

type dashboardAPIGenHandler struct{ module *Module }

func (h dashboardAPIGenHandler) authoringAPI() dashboardhttp.AuthoringAPI {
	actor := h.module.currentActor
	if actor == nil {
		actor = h.module.handler.CurrentPrincipalID
	}
	return dashboardhttp.AuthoringAPI{Application: h.module.authoring, ActorID: actor, RecordAudit: h.module.recordAudit}
}

func (h dashboardAPIGenHandler) ListDashboardAuthoringCatalog(w http.ResponseWriter, r *http.Request, _ string) {
	h.authoringAPI().ListCatalog(w, r)
}
func (h dashboardAPIGenHandler) ExecuteDashboardAuthoringCommand(w http.ResponseWriter, r *http.Request, _ string, _ dashboardgen.GenExecuteDashboardAuthoringCommandHeaders) {
	h.authoringAPI().ExecuteCommand(w, r)
}
func (h dashboardAPIGenHandler) GetDashboardAuthoringDashboard(w http.ResponseWriter, r *http.Request, _, _ string) {
	h.authoringAPI().GetDashboard(w, r)
}
func (h dashboardAPIGenHandler) GetDashboardAuthoringDraft(w http.ResponseWriter, r *http.Request, _, _ string) {
	h.authoringAPI().GetDraft(w, r)
}
func (h dashboardAPIGenHandler) PreviewDashboardAuthoringDraft(w http.ResponseWriter, r *http.Request, _, _, _ string) {
	h.authoringAPI().Preview(w, r)
}
func (h dashboardAPIGenHandler) GetDashboardAuthoringDraftRevision(w http.ResponseWriter, r *http.Request, _, _, _, _ string) {
	h.authoringAPI().GetRevision(w, r)
}
func (h dashboardAPIGenHandler) GetDashboardAuthoringPublishedRevision(w http.ResponseWriter, r *http.Request, _, _, _ string) {
	h.authoringAPI().GetRevision(w, r)
}
func (h dashboardAPIGenHandler) CreateDashboardAuthoringDraft(w http.ResponseWriter, r *http.Request, _ string, _ dashboardgen.GenCreateDashboardAuthoringDraftHeaders) {
	h.authoringAPI().CreateDraft(w, r)
}
func (h dashboardAPIGenHandler) ForkDashboardAuthoringDraft(w http.ResponseWriter, r *http.Request, _ string, _ dashboardgen.GenForkDashboardAuthoringDraftHeaders) {
	h.authoringAPI().Fork(w, r)
}
func (h dashboardAPIGenHandler) ExportDashboardAuthoringSource(w http.ResponseWriter, r *http.Request, _, _, _ string) {
	h.authoringAPI().Export(w, r)
}

func (h dashboardAPIGenHandler) ListDashboardPublications(w http.ResponseWriter, r *http.Request, project string) {
	h.module.ListDashboardPublications(w, r, project)
}
func (h dashboardAPIGenHandler) GetDashboardPublication(w http.ResponseWriter, r *http.Request, project, publication string) {
	h.module.GetDashboardPublication(w, r, project, publication)
}
func (h dashboardAPIGenHandler) ResumeDashboardPublication(w http.ResponseWriter, r *http.Request, project, publication string, _ dashboardgen.GenResumeDashboardPublicationHeaders) {
	h.module.ResumeDashboardPublication(w, r, project, publication)
}
func (h dashboardAPIGenHandler) RotateDashboardPublication(w http.ResponseWriter, r *http.Request, project, publication string, _ dashboardgen.GenRotateDashboardPublicationHeaders) {
	h.module.RotateDashboardPublication(w, r, project, publication)
}
func (h dashboardAPIGenHandler) SuspendDashboardPublication(w http.ResponseWriter, r *http.Request, project, publication string, _ dashboardgen.GenSuspendDashboardPublicationHeaders) {
	h.module.SuspendDashboardPublication(w, r, project, publication)
}
func (h dashboardAPIGenHandler) ListDashboards(w http.ResponseWriter, r *http.Request, _ dashboardgen.GenListDashboardsParams) {
	h.module.ListDashboards(w, r)
}
func (h dashboardAPIGenHandler) GetDashboard(w http.ResponseWriter, r *http.Request, _ string) {
	h.module.HTTP().GetDashboard(w, r)
}
func (h dashboardAPIGenHandler) UpdateDashboardAppearance(w http.ResponseWriter, r *http.Request, _ string) {
	http.Error(w, "dashboard appearance is unavailable", http.StatusNotImplemented)
}
func (h dashboardAPIGenHandler) GetDashboardPage(w http.ResponseWriter, r *http.Request, _, _ string) {
	h.module.HTTP().GetDashboardPage(w, r)
}
func (h dashboardAPIGenHandler) GetDashboardFilter(w http.ResponseWriter, r *http.Request, _, _, _ string) {
	h.module.HTTP().GetDashboardFilter(w, r)
}
func (h dashboardAPIGenHandler) ListDashboardFilterValues(w http.ResponseWriter, r *http.Request, dashboard, page, filter string, params dashboardgen.GenListDashboardFilterValuesParams) {
	h.module.ListDashboardFilterValues(w, r, dashboard, page, filter, params)
}
func (h dashboardAPIGenHandler) QueryDashboardPage(w http.ResponseWriter, r *http.Request, dashboard, page string) {
	h.module.QueryDashboardPage(w, r, dashboard, page)
}
func (h dashboardAPIGenHandler) GetDashboardVisual(w http.ResponseWriter, r *http.Request, dashboard, page, visual string) {
	h.module.HTTP().GetDashboardVisual(w, r)
}
func (h dashboardAPIGenHandler) QueryDashboardVisualData(w http.ResponseWriter, r *http.Request, dashboard, page, visual string, headers dashboardgen.GenQueryDashboardVisualDataHeaders) {
	h.module.QueryDashboardVisualData(w, r, dashboard, page, visual)
}
func (h dashboardAPIGenHandler) ListSemanticModels(w http.ResponseWriter, r *http.Request, params dashboardgen.GenListSemanticModelsParams) {
	h.module.ListSemanticModels(w, r)
}
func (h dashboardAPIGenHandler) GetSemanticModel(w http.ResponseWriter, r *http.Request, model string) {
	h.module.SemanticAPI().GetSemanticModel(w, r)
}
func (h dashboardAPIGenHandler) ListSemanticDatasets(w http.ResponseWriter, r *http.Request, model string, params dashboardgen.GenListSemanticDatasetsParams) {
	h.module.SemanticAPI().ListSemanticDatasets(w, r)
}
func (h dashboardAPIGenHandler) GetSemanticDataset(w http.ResponseWriter, r *http.Request, model, dataset string) {
	h.module.SemanticAPI().GetSemanticDataset(w, r)
}
func (h dashboardAPIGenHandler) ListSemanticFields(w http.ResponseWriter, r *http.Request, model, dataset string, params dashboardgen.GenListSemanticFieldsParams) {
	h.module.SemanticAPI().ListSemanticFields(w, r)
}
func (h dashboardAPIGenHandler) PreviewSemanticDataset(w http.ResponseWriter, r *http.Request, model, dataset string, headers dashboardgen.GenPreviewSemanticDatasetHeaders) {
	h.module.PreviewSemanticDataset(w, r, model, dataset)
}
func (h dashboardAPIGenHandler) ExplainSemanticPreview(w http.ResponseWriter, r *http.Request, model, dataset string) {
	h.module.SemanticAPI().ExplainSemanticPreview(w, r)
}
func (h dashboardAPIGenHandler) ListSemanticModelFields(w http.ResponseWriter, r *http.Request, model string, params dashboardgen.GenListSemanticModelFieldsParams) {
	h.module.SemanticAPI().ListSemanticModelFields(w, r)
}
func (h dashboardAPIGenHandler) QuerySemanticModel(w http.ResponseWriter, r *http.Request, model string, headers dashboardgen.GenQuerySemanticModelHeaders) {
	h.module.QuerySemanticModel(w, r, model)
}
func (h dashboardAPIGenHandler) ExplainSemanticModelQuery(w http.ResponseWriter, r *http.Request, model string) {
	h.module.SemanticAPI().ExplainSemanticModelQuery(w, r)
}
func (h dashboardAPIGenHandler) ListSemanticRelationships(w http.ResponseWriter, r *http.Request, model string, params dashboardgen.GenListSemanticRelationshipsParams) {
	h.module.SemanticAPI().ListSemanticRelationships(w, r)
}
func (h dashboardAPIGenHandler) ListSemanticSources(w http.ResponseWriter, r *http.Request, model string, params dashboardgen.GenListSemanticSourcesParams) {
	h.module.SemanticAPI().ListSemanticSources(w, r)
}

func (m *Module) DispatchAPIGenOperation(operationID string, logger *slog.Logger, w http.ResponseWriter, r *http.Request) bool {
	return dashboardhttp.DispatchAPIGenOperation(operationID, dashboardAPIGenHandler{module: m}, logger, w, r)
}
