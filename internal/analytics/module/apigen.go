package module

import (
	"context"
	"log/slog"
	"net/http"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	queryaudithttp "github.com/flidai/leapview/internal/analytics/queryaudit/http"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type QueryAuditAPIGenConfig struct {
	Reader    func() (queryaudit.Reader, error)
	ProjectID func(string) projectgraph.ResourceID
}

type AnalyticsAPIGenConfig struct {
	QueryAudit        QueryAuditAPIGenConfig
	Connections       ConnectionBindingAPIGenConfig
	SavedExplorations SavedExplorationAPIGenConfig
}

type analyticsAPIGenDispatcher struct {
	queryEvents       queryaudithttp.Handler
	connections       connectionBindingAPIHandler
	savedExplorations savedExplorationAPIHandler
}

func newAnalyticsAPIGenDispatcher(config AnalyticsAPIGenConfig) *analyticsAPIGenDispatcher {
	return &analyticsAPIGenDispatcher{queryEvents: queryaudithttp.Handler{
		Reader:    queryaudithttp.ReaderProvider(config.QueryAudit.Reader),
		ProjectID: queryaudithttp.ProjectIDNormalizer(config.QueryAudit.ProjectID),
	}, connections: connectionBindingAPIHandler{config: config.Connections}, savedExplorations: savedExplorationAPIHandler{config: config.SavedExplorations}}
}

func (d *analyticsAPIGenDispatcher) ListQueryEvents(
	w http.ResponseWriter,
	r *http.Request,
	_ string,
	params analyticsgen.GenListQueryEventsParams,
) {
	d.queryEvents.ListQueryEvents(w, r)
}

func (d *analyticsAPIGenDispatcher) ListTargetConnectionBindings(
	w http.ResponseWriter,
	r *http.Request,
	project, target string,
) {
	d.connections.List(w, r, project, target)
}

func (d *analyticsAPIGenDispatcher) CreateTargetConnectionBinding(
	w http.ResponseWriter,
	r *http.Request,
	project, target string,
	_ analyticsgen.GenCreateTargetConnectionBindingHeaders,
) {
	d.connections.Create(w, r, project, target)
}

func (d *analyticsAPIGenDispatcher) GetTargetConnectionBinding(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	d.connections.Get(w, r, project, target, connection)
}

func (d *analyticsAPIGenDispatcher) UpdateTargetConnectionBinding(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	d.connections.Update(w, r, project, target, connection)
}

func (d *analyticsAPIGenDispatcher) DisableTargetConnectionBinding(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
	_ analyticsgen.GenDisableTargetConnectionBindingHeaders,
) {
	d.connections.Disable(w, r, project, target, connection)
}

func (d *analyticsAPIGenDispatcher) EnableTargetConnectionBinding(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
	_ analyticsgen.GenEnableTargetConnectionBindingHeaders,
) {
	d.connections.Enable(w, r, project, target, connection)
}

func (d *analyticsAPIGenDispatcher) GetTargetConnectionBindingHealth(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	d.connections.Health(w, r, project, target, connection)
}

func (d *analyticsAPIGenDispatcher) PlanTargetConnectionBindingChange(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	d.connections.Plan(w, r, project, target, connection)
}

func (d *analyticsAPIGenDispatcher) RefreshTargetConnectionBinding(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
	_ analyticsgen.GenRefreshTargetConnectionBindingHeaders,
) {
	d.connections.Refresh(w, r, project, target, connection)
}

func (d *analyticsAPIGenDispatcher) ListSavedExplorations(w http.ResponseWriter, r *http.Request, project string, params analyticsgen.GenListSavedExplorationsParams) {
	d.savedExplorations.List(w, r, project, params)
}

func (d *analyticsAPIGenDispatcher) CreateSavedExploration(w http.ResponseWriter, r *http.Request, project string, headers analyticsgen.GenCreateSavedExplorationHeaders) {
	d.savedExplorations.Create(w, r, project, headers)
}

func (d *analyticsAPIGenDispatcher) GetSavedExploration(w http.ResponseWriter, r *http.Request, project, exploration string) {
	d.savedExplorations.Get(w, r, project, exploration)
}

func (d *analyticsAPIGenDispatcher) UpdateSavedExploration(w http.ResponseWriter, r *http.Request, project, exploration string, headers analyticsgen.GenUpdateSavedExplorationHeaders) {
	d.savedExplorations.Update(w, r, project, exploration, headers)
}

func (d *analyticsAPIGenDispatcher) ArchiveSavedExploration(w http.ResponseWriter, r *http.Request, project, exploration string, headers analyticsgen.GenArchiveSavedExplorationHeaders) {
	d.savedExplorations.Archive(w, r, project, exploration, headers)
}

func (d *analyticsAPIGenDispatcher) DuplicateSavedExploration(w http.ResponseWriter, r *http.Request, project, exploration string, headers analyticsgen.GenDuplicateSavedExplorationHeaders) {
	d.savedExplorations.Duplicate(w, r, project, exploration, headers)
}

func (d *analyticsAPIGenDispatcher) TestTargetConnectionBinding(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
	_ analyticsgen.GenTestTargetConnectionBindingHeaders,
) {
	d.connections.Test(w, r, project, target, connection)
}

type analyticsAPIGenTransportErrorResponder struct{ logger *slog.Logger }

func (responder analyticsAPIGenTransportErrorResponder) RespondTransportError(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	failure analyticsgen.GenTransportError,
) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

func DispatchAPIGenOperation(
	config AnalyticsAPIGenConfig,
	operationID string,
	logger *slog.Logger,
	w http.ResponseWriter,
	r *http.Request,
) bool {
	return analyticsgen.DispatchAPIGenOperation(
		operationID,
		newAnalyticsAPIGenDispatcher(config),
		analyticsAPIGenTransportErrorResponder{logger: logger},
		w,
		r,
	)
}
