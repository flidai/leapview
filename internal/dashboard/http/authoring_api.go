package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// HeadlessAuthoringApplication is the narrow transport-facing dashboard authoring
// facade. Keeping this interface at the HTTP boundary makes it impossible for
// handlers to persist arbitrary patches or bypass the transactional service.
type HeadlessAuthoringApplication interface {
	Create(context.Context, authoringservice.CreateRequest) (authoringservice.Result, error)
	Execute(context.Context, projectgraph.ResourceID, authoring.Command) (authoringservice.Result, error)
	ExecuteIntent(context.Context, application.IntentRequest) (authoringservice.Result, error)
	List(context.Context, catalog.ListRequest) (catalog.ListResult, error)
	Get(context.Context, catalog.GetRequest) (catalog.Dashboard, error)
	Draft(context.Context, application.DraftRequest) (application.DraftRead, error)
	Revision(context.Context, application.RevisionRequest) (authoring.Revision, error)
	Fork(context.Context, sourceadapter.ForkRequest) (authoringservice.Result, error)
	Preview(context.Context, preview.PreviewRequest) (preview.Preview, error)
	ExportYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error)
	ExportDraftYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error)
}

type authoringMutationResponse struct {
	Revision  authoring.RevisionToken      `json:"revision"`
	Lifecycle authoring.DashboardLifecycle `json:"lifecycle"`
}

func mutationResponse(result authoringservice.Result) authoringMutationResponse {
	return authoringMutationResponse{Revision: result.Revision, Lifecycle: result.Lifecycle}
}

// AuthoringAPI is the versioned, headless dashboard authoring transport. It
// is mounted beneath the public API protocol by the dashboard module, so
// bearer authentication and request IDs are established by that middleware.
type AuthoringAPI struct {
	Application HeadlessAuthoringApplication
	ActorID     func(*nethttp.Request) string
	RecordAudit func(context.Context, access.AuditEventInput) error
}

func (h AuthoringAPI) actor(r *nethttp.Request) (string, error) {
	if h.ActorID == nil {
		return "", fmt.Errorf("authoring actor resolver is not configured")
	}
	actor := strings.TrimSpace(h.ActorID(r))
	if actor == "" {
		return "", access.ErrForbidden
	}
	return actor, nil
}

func requireProjectID(w nethttp.ResponseWriter, r *nethttp.Request) (projectgraph.ResourceID, bool) {
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(chi.URLParam(r, "project")))
	if err != nil {
		writeAuthoringError(w, r, fmt.Errorf("%w: projectId: %v", authoring.ErrInvalidAuthoring, err), nethttp.StatusBadRequest)
		return "", false
	}
	return projectID, true
}

func (h AuthoringAPI) begin(r *nethttp.Request) (string, bool) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = httptransport.NewRequestID()
		r.Header.Set("X-Request-ID", requestID)
	}
	return requestID, true
}

func (h AuthoringAPI) ListCatalog(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	result, err := h.Application.List(r.Context(), catalog.ListRequest{ProjectID: projectID, ActorID: actor})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, result)
}

func (h AuthoringAPI) GetDashboard(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	dashboardID := authoring.DashboardID(chi.URLParam(r, "dashboard"))
	result, err := h.Application.Get(r.Context(), catalog.GetRequest{ProjectID: projectID, ActorID: actor, DashboardID: dashboardID})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, result)
}

type authoringDraftResponse struct {
	ProjectID   projectgraph.ResourceID      `json:"projectId"`
	DashboardID authoring.DashboardID        `json:"dashboardId"`
	DraftID     authoring.DraftID            `json:"draftId"`
	Revision    authoring.RevisionToken      `json:"revision"`
	Lifecycle   authoring.DashboardLifecycle `json:"lifecycle"`
	Document    authoring.Dashboard          `json:"document"`
}

func draftResponse(read application.DraftRead) authoringDraftResponse {
	draftID := authoring.DraftID("")
	if read.Lifecycle.Draft != nil {
		draftID = read.Lifecycle.Draft.ID
	}
	return authoringDraftResponse{ProjectID: read.Lifecycle.ProjectID, DashboardID: read.Revision.DashboardID, DraftID: draftID, Revision: read.Revision.Token(), Lifecycle: read.Lifecycle, Document: read.Revision.Document}
}

func (h AuthoringAPI) GetDraft(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	read, err := h.Application.Draft(r.Context(), application.DraftRequest{ProjectID: projectID, ActorID: actor, DashboardID: authoring.DashboardID(chi.URLParam(r, "dashboard"))})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, draftResponse(read))
}

func (h AuthoringAPI) GetRevision(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	// A revision under an explicit draft identity may contain private
	// historical content and therefore requires EDIT. The shorter revision path
	// is restricted by the application facade to the published pointer and uses
	// VIEW authorization.
	action := authoring.AuthorizationActionEdit
	if strings.TrimSpace(chi.URLParam(r, "draft")) == "" {
		action = authoring.AuthorizationActionView
	}
	revision, err := h.Application.Revision(r.Context(), application.RevisionRequest{
		ProjectID: projectID, ActorID: actor,
		DashboardID: authoring.DashboardID(chi.URLParam(r, "dashboard")), DraftID: authoring.DraftID(chi.URLParam(r, "draft")), RevisionID: authoring.RevisionID(chi.URLParam(r, "revision")), Action: action,
	})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, map[string]any{"projectId": projectID, "dashboardId": revision.DashboardID, "revision": revision.Token(), "document": revision.Document, "provenance": revision.Provenance, "createdAt": revision.CreatedAt})
}

func (h AuthoringAPI) CreateDraft(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	var input dashboardgen.GenSchemaDashboardAuthoringCreateRequest
	if err := decodeRequiredJSONBody(r, &input); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.SemanticModel) == "" {
		writeAuthoringError(w, r, fmt.Errorf("%w: title and semanticModel are required", authoring.ErrInvalidAuthoring))
		return
	}
	origin := authoring.Origin(derefString(input.Origin))
	if origin == "" {
		origin = authoring.OriginUI
	}
	semanticModel, err := projectgraph.NewResourceID(input.SemanticModel)
	if err != nil {
		writeAuthoringError(w, r, fmt.Errorf("%w: semanticModel: %v", authoring.ErrInvalidAuthoring, err))
		return
	}
	result, err := h.Application.Create(r.Context(), authoringservice.CreateRequest{
		ProjectID: projectID, ActorID: actor,
		Title: input.Title, SemanticModel: semanticModel, Slug: derefString(input.Slug),
		Origin: origin, IdempotencyKey: key,
	})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	if err := executeGeneratedAuthoringCommand(r, "createDashboardAuthoringDraft", projectID.String(), key, actor, result.Lifecycle.ID.String(), draftIDFromLifecycle(result.Lifecycle), origin, access.CapabilityResourceEdit, h.RecordAudit); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusCreated, mutationResponse(result))
}

func (h AuthoringAPI) ExecuteCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	var input dashboardgen.GenSchemaDashboardAuthoringCommandRequest
	if err := decodeRequiredJSONBody(r, &input); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	origin := authoring.Origin(derefString(input.Origin))
	if origin == "" {
		origin = authoring.OriginUI
	}
	expectedRevision, err := revisionTokenFromAPIGen(input.ExpectedRevision)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	command := authoring.Command{
		ID: authoring.CommandID(key), DashboardID: authoring.DashboardID(input.DashboardId), DraftID: authoring.DraftID(input.DraftId),
		ExpectedRevision: expectedRevision, ContentHash: derefString(input.ContentHash),
		Provenance:    authoring.Provenance{Origin: origin, ActorID: actor},
		SetVisibility: setVisibilityPayloadFromAPIGen(input.SetVisibility),
		AddPage:       addPagePayloadFromAPIGen(input.AddPage),
		AddVisual:     addVisualPayloadFromAPIGen(input.AddVisual),
		AssignField:   assignFieldPayloadFromAPIGen(input.AssignField),
		Publish:       publishPayloadFromAPIGen(input.Publish), Archive: archivePayloadFromAPIGen(input.Archive),
	}
	var result authoringservice.Result
	if command.IsBuilderIntent() {
		result, err = h.Application.ExecuteIntent(r.Context(), application.IntentRequest{ProjectID: projectID, ActorID: actor, Command: command})
	} else {
		result, err = h.Application.Execute(r.Context(), projectID, command)
	}
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	privilege := access.CapabilityResourceEdit
	if action, actionErr := command.RequiredAction(); actionErr == nil && (action == authoring.AuthorizationActionPublish || action == authoring.AuthorizationActionArchive) {
		privilege = access.CapabilityResourcePublish
		if action == authoring.AuthorizationActionArchive {
			privilege = access.CapabilityResourceManage
		}
	}
	if err := executeGeneratedAuthoringCommand(r, "executeDashboardAuthoringCommand", projectID.String(), key, actor, command.DashboardID.String(), command.DraftID.String(), origin, privilege, h.RecordAudit); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, mutationResponse(result))
}

func (h AuthoringAPI) Fork(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	var input dashboardgen.GenSchemaDashboardAuthoringForkRequest
	if err := decodeRequiredJSONBody(r, &input); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	source := sourceadapter.SourceRef{Kind: sourceadapter.SourceKind(input.Source.Kind), ProjectID: projectID, DashboardID: authoring.DashboardID(input.Source.DashboardId)}
	if source.ProjectID == "" {
		source.ProjectID = projectID
	}
	if source.Kind == sourceadapter.SourceProject && source.ProjectID != projectID {
		writeAuthoringError(w, r, fmt.Errorf("%w: project source must use the route project", authoring.ErrInvalidAuthoring))
		return
	}
	origin := authoring.Origin(derefString(input.Origin))
	if origin == "" {
		origin = authoring.OriginUI
	}
	result, err := h.Application.Fork(r.Context(), sourceadapter.ForkRequest{Source: source, TargetProjectID: projectID, ActorID: actor, Title: derefString(input.Title), Slug: derefString(input.Slug), Origin: origin, IdempotencyKey: key})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	if err := executeGeneratedAuthoringCommand(r, "forkDashboardAuthoringDraft", projectID.String(), key, actor, result.Lifecycle.ID.String(), draftIDFromLifecycle(result.Lifecycle), origin, access.CapabilityResourceEdit, h.RecordAudit); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusCreated, mutationResponse(result))
}

func (h AuthoringAPI) Preview(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	var input dashboardgen.GenSchemaDashboardAuthoringPreviewRequest
	if err := decodeRequiredJSONBody(r, &input); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	if strings.TrimSpace(input.PageId) == "" {
		writeAuthoringError(w, r, fmt.Errorf("%w: pageId is required", authoring.ErrInvalidAuthoring))
		return
	}
	expectedRevision, err := revisionTokenFromAPIGen(input.Revision)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	filters, err := filtersFromAPIGen(input.Filters)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	result, err := h.Application.Preview(r.Context(), preview.PreviewRequest{ProjectID: projectID, ActorID: actor, DashboardID: authoring.DashboardID(chi.URLParam(r, "dashboard")), DraftID: authoring.DraftID(chi.URLParam(r, "draft")), ExpectedRevision: expectedRevision, PageID: input.PageId, Filters: filters})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, result)
}

func (h AuthoringAPI) Export(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.begin(r)
	projectID, ok := requireProjectID(w, r)
	if !ok {
		return
	}
	actor, err := h.actor(r)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	kind := sourceadapter.SourceKind(strings.TrimSpace(chi.URLParam(r, "kind")))
	if !kind.Valid() {
		writeAuthoringError(w, r, fmt.Errorf("invalid dashboard source kind %q", kind), nethttp.StatusBadRequest)
		return
	}
	request := sourceadapter.ExportRequest{Source: sourceadapter.SourceRef{Kind: kind, ProjectID: projectID, DashboardID: authoring.DashboardID(chi.URLParam(r, "dashboard"))}, ActorID: actor}
	var body []byte
	if kind == sourceadapter.SourceInstance {
		body, err = h.Application.ExportDraftYAML(r.Context(), request)
	} else {
		body, err = h.Application.ExportYAML(r.Context(), request)
	}
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="dashboard-`+safeFilename(chi.URLParam(r, "dashboard"))+`.yaml"`)
	w.WriteHeader(nethttp.StatusOK)
	_, _ = w.Write(body)
}

func decodeRequiredJSONBody(r *nethttp.Request, dst any) error {
	if r.Body == nil || r.Body == nethttp.NoBody {
		return fmt.Errorf("request body is required")
	}
	if err := decodeOptionalJSONBody(r, dst); err != nil {
		return err
	}
	return nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// revisionTokenFromAPIGen is the single boundary conversion from APIGen's
// signed JSON number representation to the domain's non-negative revision
// number. Keeping this checked avoids wrapping malformed negative values into a
// valid-looking uint64 token.
func revisionTokenFromAPIGen(value dashboardgen.GenSchemaDashboardAuthoringRevisionToken) (authoring.RevisionToken, error) {
	if value.Number < 0 {
		return authoring.RevisionToken{}, fmt.Errorf("%w: revision number must not be negative", authoring.ErrInvalidAuthoring)
	}
	return authoring.RevisionToken{RevisionID: authoring.RevisionID(value.RevisionId), Number: uint64(value.Number), ContentHash: value.ContentHash}, nil
}

func setVisibilityPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringSetVisibilityIntent) *authoring.SetVisibilityPayload {
	if value == nil {
		return nil
	}
	return &authoring.SetVisibilityPayload{Visibility: authoring.Visibility(value.Visibility)}
}

func addPagePayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringAddPageIntent) *authoring.AddPagePayload {
	if value == nil {
		return nil
	}
	return &authoring.AddPagePayload{PageID: derefString(value.PageId), Title: derefString(value.Title)}
}

func addVisualPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringAddVisualIntent) *authoring.AddVisualPayload {
	if value == nil {
		return nil
	}
	return &authoring.AddVisualPayload{PageID: value.PageId, VisualID: derefString(value.VisualId), ComponentID: derefString(value.ComponentId), Type: value.Type, Title: derefString(value.Title)}
}

func assignFieldPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringAssignFieldIntent) *authoring.AssignFieldPayload {
	if value == nil {
		return nil
	}
	return &authoring.AssignFieldPayload{PageID: value.PageId, VisualID: value.VisualId, FieldID: value.FieldId, Role: authoring.FieldRole(value.Role)}
}

func publishPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringEmptyPayload) *authoring.PublishPayload {
	if value == nil {
		return nil
	}
	return &authoring.PublishPayload{}
}

func archivePayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringEmptyPayload) *authoring.ArchivePayload {
	if value == nil {
		return nil
	}
	return &authoring.ArchivePayload{}
}

func filtersFromAPIGen(value *map[string]any) (dashboard.Filters, error) {
	if value == nil {
		return dashboard.Filters{}, nil
	}
	encoded, err := json.Marshal(*value)
	if err != nil {
		return dashboard.Filters{}, fmt.Errorf("%w: encode filters: %v", authoring.ErrInvalidAuthoring, err)
	}
	var filters dashboard.Filters
	if err := json.Unmarshal(encoded, &filters); err != nil {
		return dashboard.Filters{}, fmt.Errorf("%w: decode filters: %v", authoring.ErrInvalidAuthoring, err)
	}
	return filters, nil
}

func idempotencyKey(w nethttp.ResponseWriter, r *nethttp.Request) (string, bool) {
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" && len(key) <= 200 {
		return key, true
	}
	httptransport.WriteProblem(w, r, nethttp.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key must contain 1 to 200 characters", nil)
	return "", false
}

// executeGeneratedAuthoringCommand completes the APIGen command guard opened
// by the generated transport. The domain service persists durable command
// evidence; the APIGen audit callback records the transport event through the
// access audit recorder without introducing another idempotency cache.
func executeGeneratedAuthoringCommand(r *nethttp.Request, operationID, project, key, actor, dashboardID, draftID string, origin authoring.Origin, capability access.Capability, recorder func(context.Context, access.AuditEventInput) error) error {
	executor, err := apigencommand.NewExecutor(dashboardgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	invocation := apigencommand.SurfaceAPI
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	execution := apigencommand.Execution{BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
		if recorder == nil {
			return fmt.Errorf("dashboard authoring audit recorder is unavailable")
		}
		targetKind, targetID := "dashboard", strings.TrimSpace(dashboardID)
		if targetID == "" {
			targetKind, targetID = "project", project
		}
		payload := dashboardgen.GenSchemaDashboardAuthoringCommandAuditPayload{
			OperationId: contract.OperationID, ProjectId: project, DashboardId: dashboardID,
			DraftId: draftID, Origin: string(origin),
		}
		var metadata string
		switch operationID {
		case "createDashboardAuthoringDraft":
			metadata, err = dashboardgen.EncodeGenCreateDashboardAuthoringDraftAuditPayload(payload)
		case "executeDashboardAuthoringCommand":
			metadata, err = dashboardgen.EncodeGenExecuteDashboardAuthoringCommandAuditPayload(payload)
		case "forkDashboardAuthoringDraft":
			metadata, err = dashboardgen.EncodeGenForkDashboardAuthoringDraftAuditPayload(payload)
		}
		if err != nil {
			return err
		}
		return recorder(ctx, access.AuditEventInput{
			PrincipalID: actor, Action: contract.AuditAction,
			ResourceKind: targetKind, ResourceID: targetID, Capability: capability,
			Status: "succeeded", RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")),
			CorrelationID: strings.TrimSpace(r.Header.Get("X-Correlation-ID")), MetadataJSON: string(metadata),
		})
	}}
	switch operationID {
	case "createDashboardAuthoringDraft":
		return dashboardgen.ExecuteGenCreateDashboardAuthoringDraftCommand(r.Context(), executor, dashboardgen.GenCreateDashboardAuthoringDraftCommandInvocation{
			Surface: invocation, Project: project, IdempotencyKey: key, RequestID: requestID, CorrelationID: correlationID,
		}, execution)
	case "executeDashboardAuthoringCommand":
		return dashboardgen.ExecuteGenExecuteDashboardAuthoringCommandCommand(r.Context(), executor, dashboardgen.GenExecuteDashboardAuthoringCommandCommandInvocation{
			Surface: invocation, Project: project, IdempotencyKey: key, RequestID: requestID, CorrelationID: correlationID,
		}, execution)
	case "forkDashboardAuthoringDraft":
		return dashboardgen.ExecuteGenForkDashboardAuthoringDraftCommand(r.Context(), executor, dashboardgen.GenForkDashboardAuthoringDraftCommandInvocation{
			Surface: invocation, Project: project, IdempotencyKey: key, RequestID: requestID, CorrelationID: correlationID,
		}, execution)
	default:
		return fmt.Errorf("unknown dashboard authoring command %q", operationID)
	}
}

func draftIDFromLifecycle(lifecycle authoring.DashboardLifecycle) string {
	if lifecycle.Draft == nil {
		return ""
	}
	return lifecycle.Draft.ID.String()
}

func safeFilename(value string) string {
	value = strings.TrimSpace(value)
	if decoded, err := url.PathUnescape(value); err == nil {
		value = decoded
	}
	if value == "" {
		return "dashboard"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "dashboard"
	}
	return b.String()
}

func writeAuthoringError(w nethttp.ResponseWriter, r *nethttp.Request, err error, override ...int) {
	if err == nil {
		err = errors.New("dashboard authoring request failed")
	}
	status := nethttp.StatusInternalServerError
	code := "AUTHORING_ERROR"
	if len(override) != 0 {
		status = override[0]
	}
	switch {
	case errors.Is(err, access.ErrForbidden):
		status, code = nethttp.StatusForbidden, "FORBIDDEN"
	case errors.Is(err, authoring.ErrNotFound), errors.Is(err, catalog.ErrNotFound), errors.Is(err, preview.ErrNotFound):
		status, code = nethttp.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, authoring.ErrStaleRevision):
		status, code = nethttp.StatusConflict, "STALE_REVISION"
	case errors.Is(err, authoring.ErrConflict), errors.Is(err, authoring.ErrCommandReuse):
		status, code = nethttp.StatusConflict, "CONFLICT"
	case errors.Is(err, authoring.ErrInvalidAuthoring), errors.Is(err, authoring.ErrInvalidPayload), errors.Is(err, authoring.ErrInvalidIdentifier):
		status, code = nethttp.StatusUnprocessableEntity, "VALIDATION_ERROR"
	case errors.Is(err, preview.ErrSemanticMismatch) || strings.Contains(err.Error(), "strictly compile dashboard draft"):
		status, code = nethttp.StatusUnprocessableEntity, "COMPILER_DIAGNOSTICS"
	}
	httptransport.WriteProblem(w, r, status, code, err.Error(), nil)
}

var _ HeadlessAuthoringApplication = (*application.Application)(nil)
