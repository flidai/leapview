package module

import (
	"context"
	"log/slog"
	"net/http"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	queryaudithttp "github.com/flidai/leapview/internal/analytics/queryaudit/http"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

type QueryAuditAPIGenConfig struct {
	Reader    func() (queryaudit.Reader, error)
	ProjectID func(string) string
}

type AnalyticsAPIGenConfig struct {
	QueryAudit  QueryAuditAPIGenConfig
	Connections ConnectionBindingAPIGenConfig
}

type analyticsAPIGenDispatcher struct {
	queryEvents queryaudithttp.Handler
	connections connectionBindingAPIHandler
}

func newAnalyticsAPIGenDispatcher(config AnalyticsAPIGenConfig) *analyticsAPIGenDispatcher {
	return &analyticsAPIGenDispatcher{queryEvents: queryaudithttp.Handler{
		Reader:      queryaudithttp.ReaderProvider(config.QueryAudit.Reader),
		WorkspaceID: queryaudithttp.WorkspaceIDNormalizer(config.QueryAudit.ProjectID),
	}, connections: connectionBindingAPIHandler{config: config.Connections}}
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
