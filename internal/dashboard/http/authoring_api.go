package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"net/url"
	"strings"
	"time"

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
	"github.com/flidai/leapview/internal/dashboard/document"
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

func mutationResponse(result authoringservice.Result) (dashboardgen.DashboardAuthoringMutationResponse, error) {
	if err := result.Lifecycle.Validate(); err != nil {
		return dashboardgen.DashboardAuthoringMutationResponse{}, err
	}
	if err := result.Revision.ValidateComplete(); err != nil {
		return dashboardgen.DashboardAuthoringMutationResponse{}, err
	}
	var response dashboardgen.DashboardAuthoringMutationResponse
	if err := decodeGeneratedProjection(result, &response); err != nil {
		return dashboardgen.DashboardAuthoringMutationResponse{}, err
	}
	return response, nil
}

// AuthoringAPI is the versioned, headless dashboard authoring transport. It
// is mounted beneath the public API protocol by the dashboard module, so
// bearer authentication and request IDs are established by that middleware.
type AuthoringAPI struct {
	Application HeadlessAuthoringApplication
	ActorID     func(*nethttp.Request) string
	// RecordAudit is retained for source compatibility with older focused
	// fixtures. Production authoring mutations use the transaction-bound
	// Access recorder carried by the authoring repository instead.
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
	response, err := catalogListResponse(result)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
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
	response, err := catalogDashboardResponse(result)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
}

func catalogDashboardResponse(value catalog.Dashboard) (dashboardgen.DashboardAuthoringSummary, error) {
	response := dashboardgen.DashboardAuthoringSummary{
		Id:            value.ID.String(),
		StableId:      value.StableID,
		ProjectId:     value.ProjectID.String(),
		Title:         value.Title,
		SemanticModel: value.SemanticModel.String(),
		Source:        string(value.Source),
		Origin:        string(value.Origin),
		Status:        string(value.Status),
		Visibility:    string(value.Visibility),
	}
	if value.Description != "" {
		description := value.Description
		response.Description = &description
	}
	if value.Owner != "" {
		owner := value.Owner
		response.Owner = &owner
	}
	if len(value.Tags) > 0 {
		tags := append([]string(nil), value.Tags...)
		response.Tags = &tags
	}
	if value.SourcePath != "" {
		sourcePath := value.SourcePath
		response.SourcePath = &sourcePath
	}
	if value.ServingIdentity != (projectgraph.ServingIdentity{}) {
		if err := value.ServingIdentity.Validate(); err != nil {
			return dashboardgen.DashboardAuthoringSummary{}, fmt.Errorf("catalog serving identity: %w", err)
		}
		response.ServingIdentity = &dashboardgen.DashboardAuthoringServingIdentity{
			ProjectId:    value.ServingIdentity.ProjectID.String(),
			Environment:  value.ServingIdentity.Environment,
			GenerationId: value.ServingIdentity.GenerationID,
		}
	}
	if value.Revision != nil {
		revision, err := catalogRevisionEvidenceResponse(*value.Revision)
		if err != nil {
			return dashboardgen.DashboardAuthoringSummary{}, err
		}
		response.Revision = &revision
	}
	if value.Publication != nil {
		publication, err := catalogPublicationEvidenceResponse(*value.Publication)
		if err != nil {
			return dashboardgen.DashboardAuthoringSummary{}, err
		}
		response.Publication = &publication
	}
	return response, nil
}

func catalogListResponse(value catalog.ListResult) (dashboardgen.DashboardAuthoringCatalogResponse, error) {
	count, err := catalogCountResponse(value.Count)
	if err != nil {
		return dashboardgen.DashboardAuthoringCatalogResponse{}, err
	}
	instanceCount, err := catalogCountResponse(value.InstanceCount)
	if err != nil {
		return dashboardgen.DashboardAuthoringCatalogResponse{}, err
	}
	projectCount, err := catalogCountResponse(value.ProjectCount)
	if err != nil {
		return dashboardgen.DashboardAuthoringCatalogResponse{}, err
	}
	items := make([]dashboardgen.DashboardAuthoringSummary, 0, len(value.Items))
	for _, item := range value.Items {
		converted, err := catalogDashboardResponse(item)
		if err != nil {
			return dashboardgen.DashboardAuthoringCatalogResponse{}, err
		}
		items = append(items, converted)
	}
	return dashboardgen.DashboardAuthoringCatalogResponse{Items: items, Count: count, InstanceCount: instanceCount, ProjectCount: projectCount}, nil
}

func catalogCountResponse(value int) (int32, error) {
	if value < 0 || int64(value) > int64(1<<31-1) {
		return 0, fmt.Errorf("catalog count %d does not fit generated int32 response", value)
	}
	return int32(value), nil
}

func catalogRevisionEvidenceResponse(value catalog.RevisionEvidence) (dashboardgen.DashboardAuthoringRevisionEvidence, error) {
	if value.Number > uint64(1<<63-1) {
		return dashboardgen.DashboardAuthoringRevisionEvidence{}, fmt.Errorf("catalog revision number %d does not fit generated int64 response", value.Number)
	}
	response := dashboardgen.DashboardAuthoringRevisionEvidence{Id: value.ID, Number: int64(value.Number), ContentHash: value.ContentHash}
	if !value.CreatedAt.IsZero() {
		createdAt := value.CreatedAt.UTC().Format(time.RFC3339Nano)
		response.CreatedAt = &createdAt
	}
	return response, nil
}

func catalogPublicationEvidenceResponse(value catalog.PublicationEvidence) (dashboardgen.DashboardAuthoringPublicationEvidence, error) {
	revision, err := catalogRevisionEvidenceResponse(value.Revision)
	if err != nil {
		return dashboardgen.DashboardAuthoringPublicationEvidence{}, err
	}
	if err := value.SemanticIdentity.Validate(); err != nil {
		return dashboardgen.DashboardAuthoringPublicationEvidence{}, fmt.Errorf("catalog publication semantic identity: %w", err)
	}
	response := dashboardgen.DashboardAuthoringPublicationEvidence{
		Revision: revision,
		SemanticIdentity: dashboardgen.DashboardAuthoringServingIdentity{
			ProjectId:    value.SemanticIdentity.ProjectID.String(),
			Environment:  value.SemanticIdentity.Environment,
			GenerationId: value.SemanticIdentity.GenerationID,
		},
	}
	if value.DefinitionHash != "" {
		definitionHash := value.DefinitionHash
		response.DefinitionHash = &definitionHash
	}
	if !value.PublishedAt.IsZero() {
		publishedAt := value.PublishedAt.UTC().Format(time.RFC3339Nano)
		response.PublishedAt = &publishedAt
	}
	return response, nil
}

func draftResponse(read application.DraftRead) (dashboardgen.DashboardAuthoringDraftResponse, error) {
	if err := read.Lifecycle.Validate(); err != nil {
		return dashboardgen.DashboardAuthoringDraftResponse{}, err
	}
	if err := read.Revision.Validate(); err != nil {
		return dashboardgen.DashboardAuthoringDraftResponse{}, err
	}
	draftID := authoring.DraftID("")
	if read.Lifecycle.Draft != nil {
		draftID = read.Lifecycle.Draft.ID
	}
	var response dashboardgen.DashboardAuthoringDraftResponse
	value := struct {
		ProjectID   string                       `json:"projectId"`
		DashboardID string                       `json:"dashboardId"`
		DraftID     string                       `json:"draftId"`
		Revision    authoring.RevisionToken      `json:"revision"`
		Lifecycle   authoring.DashboardLifecycle `json:"lifecycle"`
		Document    document.DashboardDocument   `json:"document"`
	}{ProjectID: read.Lifecycle.ProjectID.String(), DashboardID: read.Revision.DashboardID.String(), DraftID: draftID.String(), Revision: read.Revision.Token(), Lifecycle: read.Lifecycle, Document: read.Revision.Document}
	if err := decodeGeneratedProjection(value, &response); err != nil {
		return dashboardgen.DashboardAuthoringDraftResponse{}, err
	}
	return response, nil
}

func decodeGeneratedProjection(source any, destination any) error {
	encoded, err := json.Marshal(source)
	if err != nil {
		return fmt.Errorf("encode authoring projection: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode generated authoring projection: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode generated authoring projection: trailing value")
		}
		return fmt.Errorf("decode generated authoring projection: %w", err)
	}
	return nil
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
	response, err := draftResponse(read)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
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
	if err := revision.Validate(); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	var response dashboardgen.DashboardAuthoringRevisionResponse
	value := struct {
		ProjectID   string                     `json:"projectId"`
		DashboardID string                     `json:"dashboardId"`
		Revision    authoring.RevisionToken    `json:"revision"`
		Document    document.DashboardDocument `json:"document"`
		Provenance  authoring.Provenance       `json:"provenance"`
		CreatedAt   time.Time                  `json:"createdAt"`
	}{ProjectID: projectID.String(), DashboardID: revision.DashboardID.String(), Revision: revision.Token(), Document: revision.Document, Provenance: revision.Provenance, CreatedAt: revision.CreatedAt}
	if err := decodeGeneratedProjection(value, &response); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
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
	var result authoringservice.Result
	target := authoringAuditTarget{}
	err = executeAuthoringMutation(r, "createDashboardAuthoringDraft", projectID.String(), key, actor, "", "", origin, access.CapabilityResourceEdit, h.RecordAudit, &target, func(ctx context.Context) error {
		var mutationErr error
		result, mutationErr = h.Application.Create(ctx, authoringservice.CreateRequest{
			ProjectID: projectID, ActorID: actor,
			Title: input.Title, SemanticModel: semanticModel, Slug: derefString(input.Slug),
			Origin: origin, IdempotencyKey: key,
		})
		if mutationErr == nil {
			target.dashboardID, target.draftID = result.Lifecycle.ID.String(), draftIDFromLifecycle(result.Lifecycle)
		}
		return mutationErr
	})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	response, err := mutationResponse(result)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusCreated, response)
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
	command, origin, err := commandFromAPIGen(input, key, actor)
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
	var result authoringservice.Result
	err = executeAuthoringMutation(r, "executeDashboardAuthoringCommand", projectID.String(), key, actor, command.DashboardID.String(), command.DraftID.String(), origin, privilege, h.RecordAudit, nil, func(ctx context.Context) error {
		var mutationErr error
		if command.IsBuilderIntent() {
			result, mutationErr = h.Application.ExecuteIntent(ctx, application.IntentRequest{ProjectID: projectID, ActorID: actor, Command: command})
		} else {
			result, mutationErr = h.Application.Execute(ctx, projectID, command)
		}
		return mutationErr
	})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	response, err := mutationResponse(result)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
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
	var result authoringservice.Result
	target := authoringAuditTarget{}
	err = executeAuthoringMutation(r, "forkDashboardAuthoringDraft", projectID.String(), key, actor, "", "", origin, access.CapabilityResourceEdit, h.RecordAudit, &target, func(ctx context.Context) error {
		var mutationErr error
		result, mutationErr = h.Application.Fork(ctx, sourceadapter.ForkRequest{Source: source, TargetProjectID: projectID, ActorID: actor, Title: derefString(input.Title), Slug: derefString(input.Slug), Origin: origin, IdempotencyKey: key})
		if mutationErr == nil {
			target.dashboardID, target.draftID = result.Lifecycle.ID.String(), draftIDFromLifecycle(result.Lifecycle)
		}
		return mutationErr
	})
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	response, err := mutationResponse(result)
	if err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusCreated, response)
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
	var response dashboardgen.DashboardAuthoringPreviewResponse
	if err := decodeGeneratedProjection(result, &response); err != nil {
		writeAuthoringError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
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

func setVisibilityPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringSetVisibilityIntent) (*authoring.SetVisibilityPayload, error) {
	if value == nil {
		return nil, nil
	}
	visibility := authoring.Visibility(value.Visibility)
	if err := visibility.Validate(); err != nil {
		return nil, err
	}
	return &authoring.SetVisibilityPayload{Visibility: visibility}, nil
}

func metadataPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringMetadataIntent) (*authoring.MetadataPatch, error) {
	if value == nil {
		return nil, nil
	}
	var visibility *authoring.Visibility
	if value.Visibility != nil {
		converted := authoring.Visibility(*value.Visibility)
		if err := converted.Validate(); err != nil {
			return nil, err
		}
		visibility = &converted
	}
	return &authoring.MetadataPatch{
		Title: value.Title, Description: value.Description, Slug: value.Slug,
		SemanticModel: value.SemanticModel, Visibility: visibility, Appearance: value.Appearance,
	}, nil
}

func upsertPagePayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardPage) *authoring.UpsertPagePayload {
	if value == nil {
		return nil
	}
	return &authoring.UpsertPagePayload{Page: *value}
}

func removePagePayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringRemovePageIntent) *authoring.RemovePagePayload {
	if value == nil {
		return nil
	}
	return &authoring.RemovePagePayload{PageID: value.PageId}
}

func upsertVisualPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringUpsertVisualIntent) *authoring.UpsertVisualPayload {
	if value == nil {
		return nil
	}
	return &authoring.UpsertVisualPayload{VisualID: value.VisualId, Visual: value.Visual}
}

func removeVisualPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringRemoveVisualIntent) *authoring.RemoveVisualPayload {
	if value == nil {
		return nil
	}
	payload := &authoring.RemoveVisualPayload{VisualID: value.VisualId}
	if value.PageId != nil {
		payload.PageID = *value.PageId
	}
	return payload
}

func setVisualTypePayloadFromAPIGen(value *dashboardgen.DashboardAuthoringSetVisualTypeIntent) *authoring.SetVisualTypePayload {
	if value == nil {
		return nil
	}
	return &authoring.SetVisualTypePayload{PageID: value.PageId, VisualID: value.VisualId, Type: value.Type}
}

func renameVisualPayloadFromAPIGen(value *dashboardgen.DashboardAuthoringRenameVisualIntent) *authoring.RenameVisualPayload {
	if value == nil {
		return nil
	}
	return &authoring.RenameVisualPayload{PageID: value.PageId, VisualID: value.VisualId, Title: value.Title}
}

func duplicateVisualPayloadFromAPIGen(value *dashboardgen.DashboardAuthoringDuplicateVisualIntent) *authoring.DuplicateVisualPayload {
	if value == nil {
		return nil
	}
	payload := &authoring.DuplicateVisualPayload{PageID: value.PageId, VisualID: value.VisualId, Title: derefString(value.Title)}
	payload.NewVisualID = derefString(value.NewVisualId)
	payload.NewComponentID = derefString(value.NewComponentId)
	return payload
}

func restoreRevisionPayloadFromAPIGen(value *dashboardgen.DashboardAuthoringRestoreRevisionIntent) (*authoring.RestoreRevisionPayload, error) {
	if value == nil {
		return nil, nil
	}
	target, err := revisionTokenFromAPIGen(value.TargetRevision)
	if err != nil {
		return nil, err
	}
	return &authoring.RestoreRevisionPayload{TargetRevision: target}, nil
}

func updateVisualFormatPayloadFromAPIGen(value *dashboardgen.DashboardAuthoringUpdateVisualFormatIntent) *authoring.UpdateVisualFormatPayload {
	if value == nil {
		return nil
	}
	return &authoring.UpdateVisualFormatPayload{PageID: value.PageId, VisualID: value.VisualId, Title: value.Title, TitleVisible: value.TitleVisible, LegendVisible: value.LegendVisible, AxisVisible: value.AxisVisible, DataLabelsVisible: value.DataLabelsVisible}
}

func removeFieldPayloadFromAPIGen(value *dashboardgen.DashboardAuthoringRemoveFieldIntent) *authoring.RemoveFieldPayload {
	if value == nil {
		return nil
	}
	return &authoring.RemoveFieldPayload{PageID: value.PageId, VisualID: value.VisualId, FieldID: value.FieldId, Role: authoring.FieldRole(value.Role)}
}

func moveFieldPayloadFromAPIGen(value *dashboardgen.DashboardAuthoringMoveFieldIntent) *authoring.MoveFieldPayload {
	if value == nil {
		return nil
	}
	payload := &authoring.MoveFieldPayload{PageID: value.PageId, VisualID: value.VisualId, FieldID: value.FieldId, Role: authoring.FieldRole(value.Role)}
	if value.TargetRole != nil {
		payload.TargetRole = authoring.FieldRole(*value.TargetRole)
	}
	if value.Direction != nil {
		payload.Direction = *value.Direction
	}
	if value.Index != nil {
		index := int(*value.Index)
		payload.Index = &index
	}
	return payload
}

func setLayoutPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringSetLayoutIntent) *authoring.SetLayoutPayload {
	if value == nil {
		return nil
	}
	return &authoring.SetLayoutPayload{PageID: value.PageId, Layout: value.Layout}
}

func setFiltersPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringSetFiltersIntent) *authoring.SetFiltersPayload {
	if value == nil {
		return nil
	}
	payload := &authoring.SetFiltersPayload{}
	if value.Filters != nil {
		payload.Filters = append([]document.DashboardFilter(nil), (*value.Filters)...)
	}
	if value.Clear != nil {
		payload.Clear = *value.Clear
	}
	return payload
}

func setInteractionPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringSetInteractionIntent) *authoring.SetInteractionPayload {
	if value == nil {
		return nil
	}
	payload := &authoring.SetInteractionPayload{Interaction: value.Interaction}
	if value.PageId != nil {
		payload.PageID = *value.PageId
	}
	if value.VisualId != nil {
		payload.VisualID = *value.VisualId
	}
	if value.Clear != nil {
		payload.Clear = *value.Clear
	}
	return payload
}

func addPagePayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringAddPageIntent) *authoring.AddPagePayload {
	if value == nil {
		return nil
	}
	return &authoring.AddPagePayload{PageID: derefString(value.PageId), Title: derefString(value.Title)}
}

func addVisualPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringAddVisualIntent) (*authoring.AddVisualPayload, error) {
	if value == nil {
		return nil, nil
	}
	if !authoring.CanonicalVisualTypeSupported(value.Type) {
		return nil, fmt.Errorf("%w: unsupported visual type %q", authoring.ErrInvalidPayload, value.Type)
	}
	return &authoring.AddVisualPayload{PageID: value.PageId, VisualID: derefString(value.VisualId), ComponentID: derefString(value.ComponentId), Type: string(value.Type), Title: derefString(value.Title)}, nil
}

func assignFieldPayloadFromAPIGen(value *dashboardgen.GenSchemaDashboardAuthoringAssignFieldIntent) *authoring.AssignFieldPayload {
	if value == nil {
		return nil
	}
	return &authoring.AssignFieldPayload{PageID: value.PageId, VisualID: value.VisualId, FieldID: value.FieldId, Role: authoring.FieldRole(value.Role)}
}

func commandFromAPIGen(input dashboardgen.GenSchemaDashboardAuthoringCommandRequest, id, actor string) (authoring.Command, authoring.Origin, error) {
	if input.Value == nil {
		return authoring.Command{}, "", fmt.Errorf("%w: command variant is required", authoring.ErrInvalidPayload)
	}
	var base *dashboardgen.GenSchemaDashboardAuthoringCommandRequestBase
	command := authoring.Command{ID: authoring.CommandID(id), Provenance: authoring.Provenance{ActorID: actor}}
	var payloadErr error
	switch value := input.Value.(type) {
	case *dashboardgen.DashboardAuthoringMetadataCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.Metadata, payloadErr = metadataPayloadFromAPIGen(&value.Metadata)
	case *dashboardgen.DashboardAuthoringSetVisibilityCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.SetVisibility, payloadErr = setVisibilityPayloadFromAPIGen(&value.SetVisibility)
	case *dashboardgen.DashboardAuthoringAddPageCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.AddPage = addPagePayloadFromAPIGen(&value.AddPage)
	case *dashboardgen.DashboardAuthoringAddVisualCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.AddVisual, payloadErr = addVisualPayloadFromAPIGen(&value.AddVisual)
	case *dashboardgen.DashboardAuthoringAssignFieldCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.AssignField = assignFieldPayloadFromAPIGen(&value.AssignField)
	case *dashboardgen.DashboardAuthoringSetVisualTypeCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.SetVisualType = setVisualTypePayloadFromAPIGen(&value.SetVisualType)
	case *dashboardgen.DashboardAuthoringRenameVisualCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.RenameVisual = renameVisualPayloadFromAPIGen(&value.RenameVisual)
	case *dashboardgen.DashboardAuthoringDuplicateVisualCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.DuplicateVisual = duplicateVisualPayloadFromAPIGen(&value.DuplicateVisual)
	case *dashboardgen.DashboardAuthoringRestoreRevisionCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.RestoreRevision, payloadErr = restoreRevisionPayloadFromAPIGen(&value.RestoreRevision)
	case *dashboardgen.DashboardAuthoringUpdateVisualFormatCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.UpdateVisualFormat = updateVisualFormatPayloadFromAPIGen(&value.UpdateVisualFormat)
	case *dashboardgen.DashboardAuthoringRemoveFieldCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.RemoveField = removeFieldPayloadFromAPIGen(&value.RemoveField)
	case *dashboardgen.DashboardAuthoringMoveFieldCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.MoveField = moveFieldPayloadFromAPIGen(&value.MoveField)
	case *dashboardgen.DashboardAuthoringUpsertPageCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.UpsertPage = upsertPagePayloadFromAPIGen(&value.UpsertPage)
	case *dashboardgen.DashboardAuthoringRemovePageCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.RemovePage = removePagePayloadFromAPIGen(&value.RemovePage)
	case *dashboardgen.DashboardAuthoringUpsertVisualCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.UpsertVisual = upsertVisualPayloadFromAPIGen(&value.UpsertVisual)
	case *dashboardgen.DashboardAuthoringRemoveVisualCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.RemoveVisual = removeVisualPayloadFromAPIGen(&value.RemoveVisual)
	case *dashboardgen.DashboardAuthoringSetLayoutCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.SetLayout = setLayoutPayloadFromAPIGen(&value.SetLayout)
	case *dashboardgen.DashboardAuthoringSetFiltersCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.SetFilters = setFiltersPayloadFromAPIGen(&value.SetFilters)
	case *dashboardgen.DashboardAuthoringSetInteractionCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.SetInteraction = setInteractionPayloadFromAPIGen(&value.SetInteraction)
	case *dashboardgen.DashboardAuthoringPublishCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.Publish = &authoring.PublishPayload{}
	case *dashboardgen.DashboardAuthoringArchiveCommand:
		base = &value.DashboardAuthoringCommandRequestBase
		command.Archive = &authoring.ArchivePayload{}
	default:
		return authoring.Command{}, "", fmt.Errorf("%w: unsupported command variant %T", authoring.ErrInvalidPayload, input.Value)
	}
	if payloadErr != nil {
		return authoring.Command{}, "", payloadErr
	}
	if base == nil {
		return authoring.Command{}, "", fmt.Errorf("%w: command envelope is required", authoring.ErrInvalidPayload)
	}
	expected, err := revisionTokenFromAPIGen(base.ExpectedRevision)
	if err != nil {
		return authoring.Command{}, "", err
	}
	origin := authoring.Origin(derefString(base.Origin))
	if origin == "" {
		origin = authoring.OriginUI
	}
	command.DashboardID = authoring.DashboardID(base.DashboardId)
	command.DraftID = authoring.DraftID(base.DraftId)
	command.ExpectedRevision = expected
	command.ContentHash = derefString(base.ContentHash)
	command.Provenance.Origin = origin
	return command, origin, nil
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

func filtersFromAPIGen(value *dashboardgen.DashboardAuthoringPreviewFilters) (dashboard.Filters, error) {
	if value == nil {
		return dashboard.Filters{}, nil
	}
	for _, selection := range value.Selections {
		if selection.SourceKind != "visual" {
			return dashboard.Filters{}, fmt.Errorf("%w: preview selection sourceKind must be visual", authoring.ErrInvalidAuthoring)
		}
		if strings.TrimSpace(selection.InteractionKind) == "" {
			return dashboard.Filters{}, fmt.Errorf("%w: preview interactionKind must reference a compiled interaction ID", authoring.ErrInvalidAuthoring)
		}
		for _, entry := range selection.Entries {
			for _, mapping := range entry.Mappings {
				if mapping.Grain != nil && !validDashboardTimeGrain(*mapping.Grain) {
					return dashboard.Filters{}, fmt.Errorf("%w: unsupported preview grain %q", authoring.ErrInvalidAuthoring, *mapping.Grain)
				}
				switch mapping.Value.(type) {
				case nil, string, bool, float64:
				default:
					return dashboard.Filters{}, fmt.Errorf("%w: preview selection values must be JSON scalars", authoring.ErrInvalidAuthoring)
				}
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return dashboard.Filters{}, fmt.Errorf("%w: encode filters: %v", authoring.ErrInvalidAuthoring, err)
	}
	var filters dashboard.Filters
	if err := json.Unmarshal(encoded, &filters); err != nil {
		return dashboard.Filters{}, fmt.Errorf("%w: decode filters: %v", authoring.ErrInvalidAuthoring, err)
	}
	return filters, nil
}

func validDashboardTimeGrain(value document.DashboardTimeGrain) bool {
	switch value {
	case document.DashboardTimeGrainSecond, document.DashboardTimeGrainMinute, document.DashboardTimeGrainHour,
		document.DashboardTimeGrainDay, document.DashboardTimeGrainWeek, document.DashboardTimeGrainMonth,
		document.DashboardTimeGrainQuarter, document.DashboardTimeGrainYear:
		return true
	default:
		return false
	}
}

func idempotencyKey(w nethttp.ResponseWriter, r *nethttp.Request) (string, bool) {
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" && len(key) <= 200 {
		return key, true
	}
	httptransport.WriteProblem(w, r, nethttp.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key must contain 1 to 200 characters", nil)
	return "", false
}

// executeAuthoringMutation applies the generated transactional command policy
// around the source-owned authoring mutation. The repository receives the
// intent through context and records it in the same SQLite transaction as the
// lifecycle/revision write; no post-commit audit callback is involved.
type authoringAuditTarget struct {
	dashboardID string
	draftID     string
}

func executeAuthoringMutation(r *nethttp.Request, operationID, project, key, actor, dashboardID, draftID string, origin authoring.Origin, capability access.Capability, legacyRecorder func(context.Context, access.AuditEventInput) error, target *authoringAuditTarget, mutate func(context.Context) error) error {
	executor, err := apigencommand.NewExecutor(dashboardgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	invocation := apigencommand.SurfaceAPI
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	execution := apigencommand.Execution{Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
		intent, intentErr := buildAuthoringAuditIntent(contract, project, key, actor, dashboardID, draftID, origin, capability, requestID, correlationID)
		if intentErr != nil {
			return intentErr
		}
		if err := mutate(authoring.WithAuditIntent(ctx, intent)); err != nil {
			return err
		}
		if target != nil {
			dashboardID, draftID = target.dashboardID, target.draftID
		}
		if legacyRecorder != nil {
			// The source repository has now resolved any IDs allocated during
			// create/fork. Refresh the compatibility payload solely for old
			// in-memory fixtures; durable production rows are already complete
			// inside the repository transaction.
			if finalized, finalizeErr := buildAuthoringAuditIntent(contract, project, key, actor, dashboardID, draftID, origin, capability, requestID, correlationID); finalizeErr == nil {
				intent.MetadataJSON = finalized.MetadataJSON
			}
		}
		// Focused in-memory transport fixtures predate the transaction-bound
		// recorder. Keep their assertions working without enabling this legacy
		// callback in production module wiring.
		if legacyRecorder != nil {
			_ = legacyRecorder(ctx, access.AuditEventInput{
				PrincipalID: actor, Action: contract.AuditAction, ResourceKind: "dashboard", ResourceID: strings.TrimSpace(dashboardID), Capability: capability,
				Status: "succeeded", RequestID: requestID, CorrelationID: correlationID, MetadataJSON: intent.MetadataJSON,
			})
		}
		return nil
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

func buildAuthoringAuditIntent(contract apigencommand.Contract, project, idempotencyKey, actor, dashboardID, draftID string, origin authoring.Origin, capability access.Capability, requestID, correlationID string) (access.AuditIntent, error) {
	if contract.Guarantee != apigencommand.GuaranteeTransactional {
		return access.AuditIntent{}, fmt.Errorf("dashboard authoring operation %q does not provide transactional auditing", contract.OperationID)
	}
	// Create/fork allocate dashboard and draft IDs inside the source
	// transaction. Valid placeholders are replaced by the repository before
	// recording the intent; they never escape into the durable audit row.
	if strings.TrimSpace(dashboardID) == "" {
		dashboardID = "pending-dashboard"
	}
	if strings.TrimSpace(draftID) == "" {
		draftID = "pending-draft"
	}
	payload := dashboardgen.GenSchemaDashboardAuthoringCommandAuditPayload{OperationId: contract.OperationID, ProjectId: project, DashboardId: dashboardID, DraftId: draftID, Origin: string(origin)}
	var metadata string
	var err error
	switch contract.OperationID {
	case "createDashboardAuthoringDraft":
		metadata, err = dashboardgen.EncodeGenCreateDashboardAuthoringDraftAuditPayload(payload)
	case "executeDashboardAuthoringCommand":
		metadata, err = dashboardgen.EncodeGenExecuteDashboardAuthoringCommandAuditPayload(payload)
	case "forkDashboardAuthoringDraft":
		metadata, err = dashboardgen.EncodeGenForkDashboardAuthoringDraftAuditPayload(payload)
	default:
		return access.AuditIntent{}, fmt.Errorf("unknown dashboard authoring command %q", contract.OperationID)
	}
	if err != nil {
		return access.AuditIntent{}, err
	}
	return access.AuditIntent{
		EventID: "dashboard-authoring:pending", Source: "dashboard.authoring", Operation: contract.OperationID,
		PrincipalID: strings.TrimSpace(actor), Action: contract.AuditAction, ResourceKind: "dashboard", ResourceID: strings.TrimSpace(dashboardID), Capability: capability,
		Outcome: "success", RequestID: strings.TrimSpace(requestID), CorrelationID: strings.TrimSpace(correlationID), MetadataJSON: metadata,
	}, nil
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
