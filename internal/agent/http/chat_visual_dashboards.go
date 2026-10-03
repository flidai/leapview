package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	dashboardauthoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ListChatVisualDashboards returns only destinations that are compatible with
// the persisted query_visual artifact and that the caller can edit or copy.
func (h *Handler) ListChatVisualDashboards(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.chatService(w, r)
	if !ok {
		return
	}
	artifact, err := h.loadChatVisualArtifact(r.Context(), service, scope, chi.URLParam(r, "conversation"), chi.URLParam(r, "artifact"))
	if err != nil {
		h.writeChatVisualReadError(w, err)
		return
	}
	options, err := h.chatVisualDashboardOptions(r.Context(), scope, artifact)
	if err != nil {
		h.writeChatVisualReadError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, options)
}

// AddChatVisualToDashboard handles the generated headless API invocation.
// Browser requests use AddChatVisualToDashboardUI to also enforce the shared
// generated Datastar operation claim.
func (h *Handler) AddChatVisualToDashboard(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.chatService(w, r)
	if !ok {
		return
	}
	input, err := decodeChatVisualDashboardRequest(r)
	if err != nil {
		h.writeChatVisualCommandError(w, r, err)
		return
	}
	h.addChatVisualToDashboard(w, r, service, scope, input)
}

// AddChatVisualToDashboardUI is the authenticated browser command adapter.
// beginUICommandInvocation consumes the compiler-generated operation claim
// and carries the stable UUIDv7 through the authoring transaction.
func (h *Handler) AddChatVisualToDashboardUI(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	service, scope, ok := h.chatService(w, r)
	if !ok {
		return
	}
	input, err := decodeChatVisualDashboardRequest(r)
	if err != nil {
		h.writeChatVisualCommandError(w, r, err)
		return
	}
	conversationID := chi.URLParam(r, "conversation")
	ctx, err := beginUICommandInvocation(r, agentUIBinding(addChatVisualToDashboardOperation), nil, conversationID, fmt.Sprintf("%s\x00%s\x00%s\x00%s", conversationID, chi.URLParam(r, "artifact"), input.DashboardID, input.PageID)+input.Title, "")
	if err != nil {
		h.writeChatVisualCommandError(w, r, err)
		return
	}
	r = r.WithContext(ctx)
	h.addChatVisualToDashboard(w, r, service, scope, input)
}

type chatVisualDashboardRequest struct {
	DashboardID string
	PageID      string
	Title       string
}

func decodeChatVisualDashboardRequest(r *stdhttp.Request) (chatVisualDashboardRequest, error) {
	var body agentgen.AddChatVisualToDashboardRequest
	if err := decodeAgentJSON(r, &body); err != nil {
		return chatVisualDashboardRequest{}, fmt.Errorf("%w: %v", authoring.ErrInvalidPayload, err)
	}
	input := chatVisualDashboardRequest{
		DashboardID: strings.TrimSpace(pointerString(body.DashboardId)),
		PageID:      strings.TrimSpace(pointerString(body.PageId)),
		Title:       strings.TrimSpace(pointerString(body.Title)),
	}
	existing := input.DashboardID != "" && input.PageID != "" && input.Title == ""
	creating := input.DashboardID == "" && input.PageID == "" && input.Title != ""
	if !existing && !creating {
		return chatVisualDashboardRequest{}, fmt.Errorf("%w: choose a dashboard page or provide a new dashboard title", authoring.ErrInvalidPayload)
	}
	return input, nil
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (h *Handler) addChatVisualToDashboard(w stdhttp.ResponseWriter, r *stdhttp.Request, agentService *agent.Service, scope agent.Scope, input chatVisualDashboardRequest) {
	conversationID := chi.URLParam(r, "conversation")
	artifactID := chi.URLParam(r, "artifact")
	artifact, err := h.loadChatVisualArtifact(r.Context(), agentService, scope, conversationID, artifactID)
	if err != nil {
		h.writeChatVisualCommandError(w, r, err)
		return
	}
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(scope.ProjectID))
	if err != nil {
		h.writeChatVisualCommandError(w, r, fmt.Errorf("%w: active project is unavailable", authoring.ErrInvalidPayload))
		return
	}
	key, err := chatVisualIdempotencyKey(r)
	if err != nil {
		h.writeChatVisualCommandError(w, r, err)
		return
	}
	commandID := authoring.CommandID(key)
	imported := applicationChatVisualImport(artifact)

	var targetID authoring.DashboardID
	var targetTitle, pageID string
	var lifecycle dashboardauthoringservice.Result

	if input.Title != "" {
		if err := h.options.DashboardAuthoring.AuthorizeNewDashboard(r.Context(), projectID, scope.PrincipalID, imported.SemanticModelID); err != nil {
			h.writeChatVisualCommandError(w, r, err)
			return
		}
		targetTitle = input.Title
		pageID = "overview"
		documentValue := newChatVisualDashboardDocument(imported.SemanticModelID)
		if _, err := applicationAddChatVisualToDocument(&documentValue, pageID, imported, commandID); err != nil {
			h.writeChatVisualCommandError(w, r, err)
			return
		}
		ctx, err := chatVisualAuditContext(r.Context(), r, scope, key, "pending-dashboard", "pending-draft")
		if err != nil {
			h.writeChatVisualCommandError(w, r, err)
			return
		}
		lifecycle, err = h.options.DashboardAuthoring.CreateFromDocument(ctx, dashboardauthoringservice.CreateFromDocumentRequest{
			ProjectID: projectID, ActorID: scope.PrincipalID, Document: documentValue, Title: targetTitle, Slug: chatDashboardCreateSlug(key),
			Origin: authoring.OriginAgent, ConversationID: conversationID, ToolCallID: artifact.ToolCallID,
			IdempotencyKey: key, OperationKind: "create",
		})
		if err != nil {
			h.writeChatVisualCommandError(w, r, err)
			return
		}
	} else {
		replay, found, err := h.options.DashboardAuthoring.LookupChatVisualCopyReplay(r.Context(), application.AddChatVisualRequest{
			ProjectID: projectID, ActorID: scope.PrincipalID, DashboardID: authoring.DashboardID(input.DashboardID), PageID: input.PageID,
			Source: imported, CommandID: commandID,
			Provenance: authoring.Provenance{Origin: authoring.OriginAgent, ActorID: scope.PrincipalID, ConversationID: conversationID, ToolCallID: artifact.ToolCallID},
		})
		if err != nil {
			h.writeChatVisualCommandError(w, r, err)
			return
		}
		if found {
			h.writeChatVisualDashboardResult(w, r, scope, replay.Lifecycle.ID, replay.Lifecycle.Title, input.PageID)
			return
		}
		item, err := h.findChatVisualDashboard(r.Context(), projectID, scope.PrincipalID, input.DashboardID)
		if err != nil {
			h.writeChatVisualCommandError(w, r, err)
			return
		}
		if item.SemanticModel != imported.SemanticModelID {
			h.writeChatVisualCommandError(w, r, fmt.Errorf("%w: dashboard uses a different semantic model", authoring.ErrConflict))
			return
		}
		targetTitle = item.Title
		pageID = input.PageID
		if chatVisualDashboardHasEditableDraft(item) {
			dashboardID := authoring.DashboardID(item.ID.String())
			read, err := h.options.DashboardAuthoring.Draft(r.Context(), application.DraftRequest{ProjectID: projectID, ActorID: scope.PrincipalID, DashboardID: dashboardID})
			if err != nil {
				h.writeChatVisualCommandError(w, r, err)
				return
			}
			ctx, err := chatVisualAuditContext(r.Context(), r, scope, key, item.ID.String(), string(read.Lifecycle.Draft.ID))
			if err != nil {
				h.writeChatVisualCommandError(w, r, err)
				return
			}
			lifecycle, err = h.options.DashboardAuthoring.AddChatVisualToDraft(ctx, application.AddChatVisualRequest{
				ProjectID: projectID, ActorID: scope.PrincipalID, DashboardID: dashboardID, PageID: pageID,
				Source: imported, CommandID: commandID,
				Provenance: authoring.Provenance{Origin: authoring.OriginAgent, ActorID: scope.PrincipalID, ConversationID: conversationID, ToolCallID: artifact.ToolCallID},
			})
			if err != nil {
				h.writeChatVisualCommandError(w, r, err)
				return
			}
			targetID = lifecycle.Lifecycle.ID
		} else {
			ref := sourceadapter.SourceRef{Kind: sourceadapter.SourceKind(item.Source), ProjectID: projectID, DashboardID: authoring.DashboardID(item.ID.String())}
			source, err := h.options.DashboardAuthoring.LoadSource(r.Context(), sourceadapter.ExportRequest{Source: ref, ActorID: scope.PrincipalID})
			if err != nil {
				h.writeChatVisualCommandError(w, r, err)
				return
			}
			if source.Document.Spec.SemanticModel != string(imported.SemanticModelID) {
				h.writeChatVisualCommandError(w, r, fmt.Errorf("%w: source dashboard uses a different semantic model", authoring.ErrConflict))
				return
			}
			if err := h.options.DashboardAuthoring.AuthorizeNewDashboard(r.Context(), projectID, scope.PrincipalID, imported.SemanticModelID); err != nil {
				h.writeChatVisualCommandError(w, r, err)
				return
			}
			targetTitle = strings.TrimSpace(targetTitle) + " (copy)"
			if _, err := applicationAddChatVisualToDocument(&source.Document, pageID, imported, commandID); err != nil {
				h.writeChatVisualCommandError(w, r, err)
				return
			}
			sourceMetadata, forkEvidence, baseIdentity := chatVisualForkProvenance(source)
			ctx, err := chatVisualAuditContext(r.Context(), r, scope, key, "pending-dashboard", "pending-draft")
			if err != nil {
				h.writeChatVisualCommandError(w, r, err)
				return
			}
			lifecycle, err = h.options.DashboardAuthoring.CreateFromDocument(ctx, dashboardauthoringservice.CreateFromDocumentRequest{
				ProjectID: projectID, ActorID: scope.PrincipalID, Document: source.Document, Title: targetTitle,
				Origin: authoring.OriginAgent, Source: sourceMetadata, ForkedFrom: forkEvidence,
				ConversationID: conversationID, ToolCallID: artifact.ToolCallID,
				IdempotencyKey: key, OperationKind: "fork", BaseSemanticIdentity: baseIdentity,
			})
			if err != nil {
				h.writeChatVisualCommandError(w, r, err)
				return
			}
			targetID = lifecycle.Lifecycle.ID
		}
	}
	if targetID == "" {
		targetID = lifecycle.Lifecycle.ID
	}
	h.writeChatVisualDashboardResult(w, r, scope, targetID, targetTitle, pageID)
}

func (h *Handler) writeChatVisualDashboardResult(w stdhttp.ResponseWriter, r *stdhttp.Request, scope agent.Scope, targetID authoring.DashboardID, targetTitle, pageID string) {
	result := agentgen.AddChatVisualToDashboardResponse{DashboardId: targetID.String(), Title: targetTitle, PageId: pageID, Href: chatDashboardHref(targetID.String(), pageID)}
	h.recordLegacyCommandAudit(r, addChatVisualToDashboardOperation, scope, "dashboard", targetID.String())
	writeJSON(w, stdhttp.StatusOK, result)
}

func (h *Handler) loadChatVisualArtifact(ctx context.Context, service *agent.Service, scope agent.Scope, conversationID, artifactID string) (agent.ChatVisualArtifact, error) {
	if h.options.DashboardAuthoring == nil {
		return agent.ChatVisualArtifact{}, fmt.Errorf("dashboard authoring is unavailable")
	}
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(artifactID) == "" {
		return agent.ChatVisualArtifact{}, fmt.Errorf("%w: conversation and artifact are required", authoring.ErrNotFound)
	}
	artifact, err := service.ConversationVisualArtifact(ctx, scope, conversationID, artifactID)
	if err != nil {
		return agent.ChatVisualArtifact{}, err
	}
	modelID, err := projectgraph.NewResourceID(strings.TrimSpace(artifact.SemanticModelID))
	if err != nil {
		return agent.ChatVisualArtifact{}, fmt.Errorf("%w: visual semantic model is invalid", authoring.ErrInvalidPayload)
	}
	if h.options.AuthorizeSemanticModel == nil {
		return agent.ChatVisualArtifact{}, fmt.Errorf("semantic-model use authorization is unavailable")
	}
	if err := h.options.AuthorizeSemanticModel(ctx, scope, modelID.String()); err != nil {
		return agent.ChatVisualArtifact{}, err
	}
	artifact.Visual, err = agenttools.NormalizeChatVisualDefinition(artifact.Visual)
	if err != nil {
		return agent.ChatVisualArtifact{}, fmt.Errorf("%w: visual query limits are invalid: %v", authoring.ErrInvalidPayload, err)
	}
	artifact.Visual = normalizeImportedChatLegend(artifact.Visual)
	return artifact, nil
}

func normalizeImportedChatLegend(visual document.DashboardVisual) document.DashboardVisual {
	if visual.Type != document.DashboardVisualTypePie && visual.Type != document.DashboardVisualTypeDonut {
		return visual
	}
	presentation, ok := visual.Presentation.Value.(*document.ProportionalDashboardPresentation)
	if !ok || presentation == nil {
		return visual
	}
	if presentation.Legend != nil && (*presentation.Legend == document.DashboardLegendPositionNone || *presentation.Legend == document.DashboardLegendPositionBottom) {
		return visual
	}
	copy := *presentation
	legend := document.DashboardLegendPositionBottom
	copy.Legend = &legend
	visual.Presentation.Value = &copy
	return visual
}

func applicationChatVisualImport(artifact agent.ChatVisualArtifact) application.ChatVisualImport {
	modelID, _ := projectgraph.NewResourceID(artifact.SemanticModelID)
	return application.ChatVisualImport{ArtifactID: artifact.ArtifactID, ToolCallID: artifact.ToolCallID, SemanticModelID: modelID, Visual: artifact.Visual, Filters: artifact.Filters}
}

func (h *Handler) chatVisualDashboardOptions(ctx context.Context, scope agent.Scope, artifact agent.ChatVisualArtifact) (agentgen.ChatVisualDashboardOptions, error) {
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(scope.ProjectID))
	if err != nil {
		return agentgen.ChatVisualDashboardOptions{}, fmt.Errorf("%w: active project is unavailable", authoring.ErrInvalidPayload)
	}
	modelID, err := projectgraph.NewResourceID(strings.TrimSpace(artifact.SemanticModelID))
	if err != nil {
		return agentgen.ChatVisualDashboardOptions{}, fmt.Errorf("%w: visual semantic model is invalid", authoring.ErrInvalidPayload)
	}
	options := agentgen.ChatVisualDashboardOptions{Dashboards: []agentgen.ChatVisualDashboardDestination{}}
	if err := h.options.DashboardAuthoring.AuthorizeNewDashboard(ctx, projectID, scope.PrincipalID, modelID); err == nil {
		options.CanCreate = true
	} else if !errors.Is(err, access.ErrForbidden) {
		return agentgen.ChatVisualDashboardOptions{}, err
	}
	dashboards, editableDrafts, err := h.chatVisualDashboardCatalog(ctx, projectID, scope.PrincipalID)
	if err != nil {
		return agentgen.ChatVisualDashboardOptions{}, err
	}
	for _, item := range dashboards {
		if item.SemanticModel != modelID {
			continue
		}
		if chatVisualDashboardHasEditableDraft(item) {
			read, ok := editableDrafts[item.ID.String()]
			if !ok {
				continue
			}
			if read.Revision.Document.Spec.SemanticModel != modelID.String() {
				continue
			}
			options.Dashboards = append(options.Dashboards, chatVisualDestination(item.ID.String(), item.Title, read.Revision.Document.Spec.Pages, false))
			continue
		}
		source, err := h.options.DashboardAuthoring.LoadSource(ctx, sourceadapter.ExportRequest{
			Source:  sourceadapter.SourceRef{Kind: sourceadapter.SourceKind(item.Source), ProjectID: projectID, DashboardID: authoring.DashboardID(item.ID.String())},
			ActorID: scope.PrincipalID,
		})
		if err != nil {
			if errors.Is(err, access.ErrForbidden) || errors.Is(err, sourceadapter.ErrSourceUnavailable) || errors.Is(err, authoring.ErrNotFound) {
				continue
			}
			return agentgen.ChatVisualDashboardOptions{}, err
		}
		if source.Document.Spec.SemanticModel != modelID.String() {
			continue
		}
		if err := h.options.DashboardAuthoring.AuthorizeNewDashboard(ctx, projectID, scope.PrincipalID, modelID); err != nil {
			if errors.Is(err, access.ErrForbidden) {
				continue
			}
			return agentgen.ChatVisualDashboardOptions{}, err
		}
		options.Dashboards = append(options.Dashboards, chatVisualDestination(item.ID.String(), item.Title, source.Document.Spec.Pages, true))
	}
	return options, nil
}

// A published lifecycle may retain a draft pointer, but destinations with
// published status are copied from their exact published revision. Only an
// unpublished current draft is an in-place add target.
func chatVisualDashboardHasEditableDraft(item catalog.Dashboard) bool {
	return item.Source == catalog.SourceInstance && item.Status == authoring.LifecycleStatusDraft && item.DraftID != ""
}

func chatVisualDestination(id, title string, pages []document.DashboardPage, createsCopy bool) agentgen.ChatVisualDashboardDestination {
	result := agentgen.ChatVisualDashboardDestination{Id: id, Title: title, Pages: make([]agentgen.ChatVisualDashboardPage, 0, len(pages))}
	if createsCopy {
		copy := true
		result.CreatesCopy = &copy
	}
	for _, page := range pages {
		result.Pages = append(result.Pages, agentgen.ChatVisualDashboardPage{Id: page.ID, Title: page.Title})
	}
	return result
}

func (h *Handler) findChatVisualDashboard(ctx context.Context, projectID projectgraph.ResourceID, actorID, id string) (catalog.Dashboard, error) {
	dashboards, _, err := h.chatVisualDashboardCatalog(ctx, projectID, actorID)
	if err != nil {
		return catalog.Dashboard{}, err
	}
	for _, dashboard := range dashboards {
		if dashboard.ID.String() == strings.TrimSpace(id) {
			return dashboard, nil
		}
	}
	return catalog.Dashboard{}, catalog.ErrNotFound
}

func (h *Handler) chatVisualDashboardCatalog(ctx context.Context, projectID projectgraph.ResourceID, actorID string) ([]catalog.Dashboard, map[string]application.DraftRead, error) {
	listed, err := h.options.DashboardAuthoring.List(ctx, catalog.ListRequest{ProjectID: projectID, ActorID: actorID})
	if err != nil {
		return nil, nil, err
	}
	draftReads, err := h.options.DashboardAuthoring.EditableDrafts(ctx, catalog.ListRequest{ProjectID: projectID, ActorID: actorID})
	if err != nil {
		return nil, nil, err
	}
	dashboards := append([]catalog.Dashboard(nil), listed.Items...)
	editableDrafts := make(map[string]application.DraftRead, len(draftReads))
	seen := make(map[string]struct{}, len(dashboards))
	for _, dashboard := range dashboards {
		seen[dashboard.ID.String()] = struct{}{}
	}
	for _, read := range draftReads {
		lifecycle := read.Lifecycle
		id := lifecycle.ID.String()
		editableDrafts[id] = read
		if _, ok := seen[id]; ok {
			continue
		}
		dashboards = append(dashboards, catalog.Dashboard{
			ID: projectgraph.ResourceID(id), ProjectID: projectID, Title: lifecycle.Title,
			SemanticModel: lifecycle.SemanticModel, Source: catalog.SourceInstance, Status: lifecycle.Status,
			Visibility: lifecycle.Visibility, Owner: lifecycle.OwnerPrincipalID, DraftID: lifecycle.Draft.ID,
		})
		seen[id] = struct{}{}
	}
	return dashboards, editableDrafts, nil
}

func newChatVisualDashboardDocument(modelID projectgraph.ResourceID) document.DashboardDocument {
	title := "New dashboard"
	return document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{DisplayName: &title},
		Spec: document.DashboardSpec{
			SemanticModel: modelID.String(), Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}},
		},
	}
}

func applicationAddChatVisualToDocument(value *document.DashboardDocument, pageID string, source application.ChatVisualImport, commandID authoring.CommandID) (bool, error) {
	return application.AddChatVisualToDocument(value, pageID, source, commandID)
}

func chatVisualForkProvenance(source sourceadapter.Source) (*authoring.SourceMetadata, *authoring.ForkEvidence, projectgraph.ServingIdentity) {
	switch source.Provenance.Kind {
	case sourceadapter.SourceProject:
		project := source.Provenance.Project
		if project == nil {
			return nil, nil, projectgraph.ServingIdentity{}
		}
		metadata := &authoring.SourceMetadata{Path: project.Path}
		fork := &authoring.ForkEvidence{Kind: authoring.ForkSourceProject, Project: &authoring.ProjectForkEvidence{
			SourceProjectID: project.ProjectID, SourceDashboardID: project.DashboardID, Identity: project.Identity, Path: project.Path,
		}}
		return metadata, fork, project.Identity
	case sourceadapter.SourceInstance:
		instance := source.Provenance.Instance
		if instance == nil {
			return nil, nil, projectgraph.ServingIdentity{}
		}
		fork := &authoring.ForkEvidence{Kind: authoring.ForkSourceInstance, Instance: &authoring.InstanceForkEvidence{
			SourceProjectID: instance.ProjectID, SourceDashboardID: instance.DashboardID, SourceRevision: instance.PublishedRevision,
		}}
		return instance.SourceEvidence, fork, projectgraph.ServingIdentity{}
	default:
		return nil, nil, projectgraph.ServingIdentity{}
	}
}

func chatVisualAuditContext(ctx context.Context, r *stdhttp.Request, scope agent.Scope, idempotencyKey, dashboardID, draftID string) (context.Context, error) {
	// The generated operation payload is intentionally encoded by APIGen.
	// Its schema is the dashboard-authoring audit contract so the same durable
	// audit intent can be completed inside the authoring repository transaction.
	operationID := addChatVisualToDashboardOperation.APIGenOperationID()
	contract, ok := agentgen.GetAPIGenCommandRuntimeContract(operationID)
	if !ok || contract.Guarantee != apigencommand.GuaranteeTransactional {
		return nil, fmt.Errorf("generated chat visual dashboard command audit contract is unavailable")
	}
	metadata, err := agentgen.EncodeGenAddChatVisualToDashboardAuditPayload(agentgen.GenSchemaChatVisualDashboardCommandAuditPayload{
		OperationId: operationID, ProjectId: strings.TrimSpace(scope.ProjectID), DashboardId: dashboardID,
		DraftId: draftID, Origin: string(authoring.OriginAgent),
	})
	if err != nil {
		return nil, err
	}
	key, err := chatVisualIdempotencyKey(r)
	if err != nil || key != idempotencyKey {
		return nil, fmt.Errorf("chat visual dashboard audit key is not canonical")
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	return authoring.WithAuditIntent(ctx, access.AuditIntent{
		EventID: key, Source: "dashboard.authoring", Operation: operationID,
		ActorID: scope.PrincipalID, PrincipalID: scope.PrincipalID, Action: contract.AuditAction,
		ResourceKind: "dashboard", ResourceID: dashboardID, Capability: access.CapabilityResourceEdit,
		Outcome: "success", RequestID: requestID, CorrelationID: correlationID, MetadataJSON: metadata,
	}), nil
}

func chatVisualIdempotencyKey(r *stdhttp.Request) (string, error) {
	value := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 7 || parsed.String() != strings.ToLower(value) {
		return "", fmt.Errorf("%w: Idempotency-Key must be a canonical UUIDv7", authoring.ErrInvalidPayload)
	}
	return parsed.String(), nil
}

// Chat imports create independent private drafts even when their titles match.
// The validated UUIDv7 command key keeps the slug stable on retry and unique
// across separate previews without changing the user-facing title.
func chatDashboardCreateSlug(key string) string {
	return "chat-" + key
}

func chatDashboardHref(dashboardID, pageID string) string {
	return "/dashboards/" + url.PathEscape(dashboardID) + "/edit?page=" + url.QueryEscape(pageID)
}

func (h *Handler) writeChatVisualReadError(w stdhttp.ResponseWriter, err error) {
	status := chatVisualErrorStatus(err)
	writeJSONError(w, err, status)
}

func (h *Handler) writeChatVisualCommandError(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	kind := "unavailable"
	switch {
	case errors.Is(err, authoring.ErrInvalidPayload), errors.Is(err, authoring.ErrInvalidAuthoring):
		kind = "invalid"
	case errors.Is(err, access.ErrForbidden):
		kind = "forbidden"
	case errors.Is(err, agent.ErrNotFound), errors.Is(err, sql.ErrNoRows), errors.Is(err, catalog.ErrNotFound), errors.Is(err, authoring.ErrNotFound):
		kind = "not_found"
	case errors.Is(err, authoring.ErrConflict), errors.Is(err, authoring.ErrStaleRevision), errors.Is(err, authoring.ErrCommandReuse):
		kind = "conflict"
	case errors.Is(err, agent.ErrDisabled):
		kind = "unavailable"
	}
	h.writeCommandFailure(w, r, addChatVisualToDashboardOperation, apigenfailure.Wrap(kind, err))
}

func chatVisualErrorStatus(err error) int {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return stdhttp.StatusForbidden
	case errors.Is(err, agent.ErrNotFound), errors.Is(err, sql.ErrNoRows), errors.Is(err, catalog.ErrNotFound), errors.Is(err, authoring.ErrNotFound):
		return stdhttp.StatusNotFound
	case errors.Is(err, authoring.ErrInvalidPayload), errors.Is(err, authoring.ErrInvalidAuthoring):
		return stdhttp.StatusUnprocessableEntity
	case errors.Is(err, authoring.ErrConflict), errors.Is(err, authoring.ErrStaleRevision), errors.Is(err, authoring.ErrCommandReuse):
		return stdhttp.StatusConflict
	default:
		return stdhttp.StatusServiceUnavailable
	}
}
