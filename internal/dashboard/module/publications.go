package module

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (m *Module) PublicationsConfigured() bool {
	return m != nil && m.publications != nil && m.publicationService != nil && m.publicationAuditConfigured
}

func (m *Module) ResolvePublic(ctx context.Context, publicID string) (publication.Publication, error) {
	if m == nil || m.publicationService == nil {
		return publication.Publication{}, publication.ErrNotFound
	}
	return m.publicationService.ResolvePublic(ctx, publicID)
}

func (m *Module) PublicationByPublicID(ctx context.Context, publicID string) (publication.Publication, error) {
	if m == nil || m.publications == nil {
		return publication.Publication{}, publication.ErrNotFound
	}
	return m.publications.GetByPublicID(ctx, publicID)
}

// MutatePublicationWithInvocation is the transport-neutral UI adapter used by
// the admin surface. It validates the generated cross-surface policy before
// mutating state, then carries a generated durable audit intent into the
// publication transaction with the same request identity.
func (m *Module) MutatePublicationWithInvocation(ctx context.Context, projectID, name, actorID string, action publication.Action, invocation publication.CommandInvocation) (publication.Publication, error) {
	if m == nil || m.publicationService == nil || !m.publicationAuditConfigured {
		return publication.Publication{}, publication.ErrNotFound
	}
	operationID, ok := publicationOperationID(action)
	if !ok {
		return publication.Publication{}, publication.ErrConflict
	}
	operationIDValue := operationID.APIGenOperationID()
	if invocation.Surface == "" {
		invocation.Surface = string(apigencommand.SurfaceUI)
	}
	if invocation.ExpectedRevision <= 0 {
		return publication.Publication{}, fmt.Errorf("%w: missing expected publication revision", publication.ErrConflict)
	}
	parsedProjectID, err := projectgraph.NewResourceID(strings.TrimSpace(projectID))
	if err != nil {
		return publication.Publication{}, err
	}
	ctx, err = beginGeneratedPublicationInvocation(ctx, action, parsedProjectID, invocation)
	if err != nil {
		return publication.Publication{}, err
	}
	current, err := m.publications.Get(ctx, parsedProjectID, name)
	if err != nil {
		return publication.Publication{}, err
	}
	if err := markPublicationConcurrencyChecked(ctx, operationIDValue, invocation.ExpectedRevision, current.Revision); err != nil {
		return publication.Publication{}, err
	}
	intent, err := buildPublicationAuditIntent(publicationCommandAuditInput{
		operationID: operationIDValue, projectID: parsedProjectID, principalID: strings.TrimSpace(actorID),
		targetID: strings.TrimSpace(name), requestID: strings.TrimSpace(invocation.RequestID),
		correlationID: strings.TrimSpace(invocation.CorrelationID), surface: string(invocation.Surface),
		idempotencyKey: strings.TrimSpace(invocation.IdempotencyKey),
		aggregateKey:   "dashboard_publication:" + parsedProjectID.String() + ":" + strings.TrimSpace(name),
	})
	if err != nil {
		return publication.Publication{}, err
	}
	ctx = publication.WithAuditIntent(ctx, intent)
	row, err := m.publicationService.Mutate(ctx, parsedProjectID, name, actorID, action, invocation.ExpectedRevision)
	if err != nil {
		return row, err
	}
	if err := m.completePublicationCommand(ctx, operationIDValue); err != nil {
		return publication.Publication{}, err
	}
	return row, nil
}

func (m *Module) completePublicationCommand(ctx context.Context, operationID string) error {
	executor, err := apigencommand.NewExecutor(dashboardgen.GetAPIGenCommandRuntimeContract, m.logger)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, operationID, apigencommand.Execution{
		Transactional: func(context.Context, apigencommand.Contract) error { return nil },
	})
}

func markPublicationConcurrencyChecked(ctx context.Context, operationID string, expectedRevision, currentRevision int64) error {
	executor, err := apigencommand.NewExecutor(dashboardgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	expectedToken := fmt.Sprintf("\"%d\"", expectedRevision)
	currentToken := fmt.Sprintf("\"%d\"", currentRevision)
	err = executor.CheckConcurrency(ctx, operationID, expectedToken, currentToken)
	// The generated command runtime owns ETag parsing/comparison, while the
	// transport resolves classified domain failures through the operation's
	// public failure vocabulary. Preserve the runtime sentinel for callers but
	// classify it so API/UI adapters emit the documented 412 contract.
	if errors.Is(err, apigencommand.ErrPreconditionRequired) || errors.Is(err, apigencommand.ErrPreconditionFailed) {
		return apigenfailure.Wrap("precondition", err)
	}
	return err
}

func beginGeneratedPublicationInvocation(ctx context.Context, action publication.Action, projectID projectgraph.ResourceID, invocation publication.CommandInvocation) (context.Context, error) {
	operationID, ok := publicationOperationID(action)
	if !ok {
		return ctx, publication.ErrConflict
	}
	if claimed := strings.TrimSpace(invocation.OperationID); claimed != "" && claimed != operationID.APIGenOperationID() {
		return ctx, apigencommand.ErrOperationMismatch
	}
	projectIDString := projectID.String()
	switch action {
	case publication.ActionSuspend:
		started, _, err := dashboardgen.BeginGenSuspendDashboardPublicationCommand(ctx, dashboardgen.GenSuspendDashboardPublicationCommandInvocation{
			Surface: apigencommand.Surface(invocation.Surface), Project: projectIDString, IdempotencyKey: invocation.IdempotencyKey,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID, ConcurrencyToken: fmt.Sprintf("\"%d\"", invocation.ExpectedRevision),
		})
		return started, err
	case publication.ActionResume:
		started, _, err := dashboardgen.BeginGenResumeDashboardPublicationCommand(ctx, dashboardgen.GenResumeDashboardPublicationCommandInvocation{
			Surface: apigencommand.Surface(invocation.Surface), Project: projectIDString, IdempotencyKey: invocation.IdempotencyKey,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID, ConcurrencyToken: fmt.Sprintf("\"%d\"", invocation.ExpectedRevision),
		})
		return started, err
	case publication.ActionRotate:
		started, _, err := dashboardgen.BeginGenRotateDashboardPublicationCommand(ctx, dashboardgen.GenRotateDashboardPublicationCommandInvocation{
			Surface: apigencommand.Surface(invocation.Surface), Project: projectIDString, IdempotencyKey: invocation.IdempotencyKey,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID, ConcurrencyToken: fmt.Sprintf("\"%d\"", invocation.ExpectedRevision),
		})
		return started, err
	default:
		return ctx, publication.ErrConflict
	}
}

func (m *Module) AllPublications(ctx context.Context) ([]publication.Publication, error) {
	if m == nil || m.publications == nil {
		return nil, publication.ErrNotFound
	}
	return m.publications.ListAll(ctx)
}

func (m *Module) PublicationEvents(ctx context.Context, publicationID string) ([]publication.Event, error) {
	if m == nil || m.publications == nil {
		return nil, publication.ErrNotFound
	}
	return m.publications.ListEvents(ctx, publicationID)
}

func (m *Module) PublicationDTO(row publication.Publication) dashboardapi.PublicationResponse {
	return m.dashboardPublicationDTO(row)
}

func (m *Module) ListDashboardPublications(w http.ResponseWriter, r *http.Request, projectID string) {
	if m == nil || m.publications == nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "PUBLICATIONS_NOT_AVAILABLE", "Dashboard publications are not available", nil)
		return
	}
	parsedProjectID, err := projectgraph.NewResourceID(strings.TrimSpace(projectID))
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PROJECT", "Project identity is invalid", nil)
		return
	}
	rows, err := m.publications.List(r.Context(), parsedProjectID)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "PUBLICATION_LIST_FAILED", "Dashboard publications could not be loaded", nil)
		return
	}
	items := make([]dashboardapi.PublicationResponse, 0, len(rows))
	for _, row := range rows {
		allowed, authErr := m.authorizeDashboardPublication(r, row.ProjectID.String(), row.Dashboard, access.CapabilityResourceRead)
		if authErr != nil {
			apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "Dashboard authorization could not be evaluated", nil)
			return
		}
		if !allowed {
			continue
		}
		items = append(items, m.dashboardPublicationDTO(row))
	}
	writeJSON(w, http.StatusOK, dashboardapi.PublicationListResponse{Items: items})
}

func (m *Module) GetDashboardPublication(w http.ResponseWriter, r *http.Request, projectID, name string) {
	row, ok := m.dashboardPublication(w, r, projectID, name)
	if !ok {
		return
	}
	allowed, err := m.authorizeDashboardPublication(r, row.ProjectID.String(), row.Dashboard, access.CapabilityResourceRead)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "AUTHORIZATION_UNAVAILABLE", "Dashboard authorization could not be evaluated", nil)
		return
	}
	if !allowed {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "PUBLICATION_NOT_FOUND", "Dashboard publication not found", nil)
		return
	}
	setPublicationETag(w, row)
	writeJSON(w, http.StatusOK, m.dashboardPublicationDTO(row))
}

func (m *Module) SuspendDashboardPublication(w http.ResponseWriter, r *http.Request, projectID, name string) {
	m.mutateDashboardPublication(w, r, projectID, name, publication.ActionSuspend)
}

func (m *Module) ResumeDashboardPublication(w http.ResponseWriter, r *http.Request, projectID, name string) {
	m.mutateDashboardPublication(w, r, projectID, name, publication.ActionResume)
}

func (m *Module) RotateDashboardPublication(w http.ResponseWriter, r *http.Request, projectID, name string) {
	m.mutateDashboardPublication(w, r, projectID, name, publication.ActionRotate)
}

func (m *Module) mutateDashboardPublication(w http.ResponseWriter, r *http.Request, projectID, name string, action publication.Action) {
	operationID, operationKnown := publicationOperationID(action)
	if !operationKnown {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "PUBLICATION_COMMAND_UNKNOWN", "Dashboard publication command is unknown", nil)
		return
	}
	operationIDValue := operationID.APIGenOperationID()
	if m == nil || m.publicationService == nil {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, publication.ErrNotFound)
		return
	}
	// Publication commands require a transaction-scoped audit recorder. Reject
	// an incompletely constructed module before performing the state transition.
	if !m.publicationAuditConfigured {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, errPublicationCommandAuditUnavailable)
		return
	}
	row, ok := m.dashboardPublication(w, r, projectID, name)
	if !ok {
		return
	}
	allowed, authErr := m.authorizeDashboardPublication(r, row.ProjectID.String(), row.Dashboard, access.CapabilityResourcePublish)
	if authErr != nil {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, apigenfailure.Wrap("authorization_unavailable", authErr))
		return
	}
	if !allowed {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, publication.ErrNotFound)
		return
	}
	actor := ""
	if m.currentActor != nil {
		actor = m.currentActor(r)
	}
	parsedProjectID, parseErr := projectgraph.NewResourceID(strings.TrimSpace(projectID))
	if parseErr != nil {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, parseErr)
		return
	}
	if !canonicalPublicationUUIDv7(strings.TrimSpace(r.Header.Get("Idempotency-Key"))) {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key must be a canonical UUIDv7", nil)
		return
	}
	// The API command guard has already begun the generated invocation on the
	// request context. Starting a nested invocation here would mark only the
	// child state complete and cause the outer guard to reject the response.
	ctx := r.Context()
	intent, intentErr := buildPublicationAuditIntent(publicationAuditRequestInput(r, operationIDValue, parsedProjectID, actor, name))
	if intentErr != nil {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, intentErr)
		return
	}
	ctx = publication.WithAuditIntent(ctx, intent)
	expectedRevision, currentRevision, parseRevisionErr := publicationExpectedRevision(r, row)
	if parseRevisionErr != nil {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, parseRevisionErr)
		return
	}
	if err := markPublicationConcurrencyChecked(ctx, operationIDValue, expectedRevision, currentRevision); err != nil {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, err)
		return
	}
	row, err := m.publicationService.Mutate(ctx, parsedProjectID, name, actor, action, expectedRevision)
	if err == nil {
		err = m.completePublicationCommand(ctx, operationIDValue)
	}
	m.writePublicationMutation(w, r, operationID, row, err)
}

func (m *Module) authorizeDashboardPublication(r *http.Request, projectID, dashboardID string, capability access.Capability) (bool, error) {
	if m == nil || m.handler.CurrentPrincipalID == nil || m.handler.AuthorizeListResource == nil {
		return false, errors.New("dashboard authorization is unavailable")
	}
	principalID := strings.TrimSpace(m.handler.CurrentPrincipalID(r))
	if principalID == "" {
		return false, nil
	}
	_, err := projectgraph.NewResourceID(strings.TrimSpace(projectID))
	if err != nil {
		return false, err
	}
	dashboard, err := projectgraph.NewResourceID(strings.TrimSpace(dashboardID))
	if err != nil {
		return false, err
	}
	resource, err := access.NewResourceRef(dashboard, projectgraph.KindDashboard)
	if err != nil {
		return false, err
	}
	return m.handler.AuthorizeListResource(r.Context(), principalID, resource, capability)
}

func (m *Module) dashboardPublication(w http.ResponseWriter, r *http.Request, projectID, name string) (publication.Publication, bool) {
	if m == nil || m.publications == nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "PUBLICATION_NOT_FOUND", "Dashboard publication not found", nil)
		return publication.Publication{}, false
	}
	parsedProjectID, parseErr := projectgraph.NewResourceID(strings.TrimSpace(projectID))
	if parseErr != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PROJECT", "Project identity is invalid", nil)
		return publication.Publication{}, false
	}
	row, err := m.publications.Get(r.Context(), parsedProjectID, name)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, publication.ErrNotFound) {
			status = http.StatusNotFound
		}
		apitransport.WriteProblem(w, r, status, "PUBLICATION_NOT_FOUND", "Dashboard publication not found", nil)
		return publication.Publication{}, false
	}
	return row, true
}

func (m *Module) writePublicationMutation(w http.ResponseWriter, r *http.Request, operationID dashboardgen.GenCommandOperationID, row publication.Publication, err error) {
	if err != nil {
		var logger *slog.Logger
		if m != nil {
			logger = m.logger
		}
		apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, logger, operationID, dashboardgen.GetAPIGenCommandFailureContracts, err)
		return
	}
	setPublicationETag(w, row)
	writeJSON(w, http.StatusOK, m.dashboardPublicationDTO(row))
}

func setPublicationETag(w http.ResponseWriter, row publication.Publication) {
	if w == nil || row.Revision <= 0 {
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", row.Revision))
}

func (m *Module) dashboardPublicationDTO(row publication.Publication) dashboardapi.PublicationResponse {
	publicPath := "/public/dashboards/" + row.PublicID
	embedPath := "/embed/dashboards/" + row.PublicID
	publicURL := m.absolutePublicURL(publicPath)
	embedURL := m.absolutePublicURL(embedPath)
	iframe := `<iframe src="` + html.EscapeString(embedURL) + `" title="` + html.EscapeString(row.Name) + `" loading="lazy" sandbox="allow-scripts allow-same-origin" referrerpolicy="no-referrer"></iframe>`
	dto := dashboardapi.PublicationResponse{
		Name: row.Name, ProjectID: row.ProjectID.String(), Dashboard: row.Dashboard,
		DefaultPage: row.DefaultPage, Status: dashboardapi.PublicationStatus(row.Status()), Configured: row.Configured,
		AllowedOrigins: append([]string(nil), row.AllowedOrigins...), PublicURL: publicURL, EmbedURL: embedURL, IFrameSnippet: iframe,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	dto.Revision = row.Revision
	optionalString := func(value string) *string {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		copy := value
		return &copy
	}
	dto.ActiveServingStateID = optionalString(row.ServingStateID)
	dto.ConfiguredAt = optionalString(row.ConfiguredAt)
	dto.DisabledAt = optionalString(row.DisabledAt)
	dto.SuspendedAt = optionalString(row.SuspendedAt)
	dto.SuspendedBy = optionalString(row.SuspendedBy)
	dto.RotatedAt = optionalString(row.RotatedAt)
	return dto
}

func publicationExpectedRevision(r *http.Request, row publication.Publication) (int64, int64, error) {
	if r == nil {
		return 0, 0, fmt.Errorf("%w: publication expected revision is required", publication.ErrConflict)
	}
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' || strings.HasPrefix(raw, "W/") {
		return 0, 0, fmt.Errorf("%w: publication expected revision must be a strong quoted ETag", publication.ErrConflict)
	}
	token := raw[1 : len(raw)-1]
	if strings.ContainsAny(token, "\"*,") {
		return 0, 0, fmt.Errorf("%w: publication expected revision ETag is malformed", publication.ErrConflict)
	}
	expected, err := strconv.ParseInt(token, 10, 64)
	if err != nil || expected <= 0 || strconv.FormatInt(expected, 10) != token {
		return 0, 0, fmt.Errorf("%w: publication expected revision ETag is malformed", publication.ErrConflict)
	}
	if row.Revision <= 0 {
		return 0, 0, fmt.Errorf("%w: publication current revision is invalid", publication.ErrConflict)
	}
	return expected, row.Revision, nil
}

func (m *Module) absolutePublicURL(path string) string {
	if m == nil || m.publicURL == "" {
		return path
	}
	return m.publicURL + path
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	apitransport.WriteJSON(w, status, value)
}
