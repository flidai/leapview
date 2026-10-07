package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring/application"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type explorationDashboardAppendBody struct {
	DashboardID     string                      `json:"dashboardId"`
	PageID          string                      `json:"pageId"`
	RevisionToken   string                      `json:"revisionToken"`
	PlacementChoice string                      `json:"placementChoice"`
	Spec            exploration.ExplorationSpec `json:"spec"`
}

func (h *BrowserHandler) ExplorationDashboardTargets(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	modelID := strings.TrimSpace(r.URL.Query().Get("semanticModel"))
	if modelID == "" {
		stdhttp.Error(w, "semanticModel is required", stdhttp.StatusBadRequest)
		return
	}
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindSemanticModel}) {
		return
	}
	principal, projectID, ok := h.explorationDashboardPrincipal(w, r)
	if !ok {
		return
	}
	targets, err := h.DashboardAuthoring.ExplorationTargets(r.Context(), dashboardauthoring.ExplorationTargetsRequest{
		ProjectID: projectID, ActorID: principal.ID, SourceModelID: modelID,
	})
	if err != nil {
		writeExplorationDashboardError(w, err, false)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		Items []dashboardauthoring.ExplorationTarget `json:"items"`
	}{Items: targets})
}

func (h *BrowserHandler) ExplorationDashboardTarget(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	modelID := strings.TrimSpace(r.URL.Query().Get("semanticModel"))
	if modelID == "" {
		stdhttp.Error(w, "semanticModel is required", stdhttp.StatusBadRequest)
		return
	}
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindSemanticModel}) {
		return
	}
	principal, projectID, ok := h.explorationDashboardPrincipal(w, r)
	if !ok {
		return
	}
	dashboardID := authoring.DashboardID(strings.TrimSpace(chi.URLParam(r, "dashboard")))
	target, err := h.DashboardAuthoring.ExplorationTarget(r.Context(), dashboardauthoring.ExplorationTargetRequest{
		ProjectID: projectID, ActorID: principal.ID, SourceModelID: modelID, DashboardID: dashboardID,
	})
	if err != nil {
		writeExplorationDashboardError(w, err, false)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(target)
}

func (h *BrowserHandler) AppendExplorationToDashboard(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if h == nil || h.DashboardAppendCommand.OperationID() == "" {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), h.DashboardAppendCommand.OperationID()); err != nil {
		stdhttp.Error(w, "invalid dashboard append command", stdhttp.StatusBadRequest)
		return
	}
	principal, projectID, ok := h.explorationDashboardPrincipal(w, r)
	if !ok {
		return
	}
	var body explorationDashboardAppendBody
	if err := decodeExplorationDashboardJSON(r.Body, &body); err != nil {
		stdhttp.Error(w, "invalid dashboard append request", stdhttp.StatusBadRequest)
		return
	}
	if !h.authorizeExplorationModel(w, r, body.Spec.ModelID) {
		return
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		stdhttp.Error(w, "dashboard append request identity is required", stdhttp.StatusBadRequest)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !canonicalDashboardAppendUUIDv7(requestID) || !canonicalDashboardAppendUUIDv7(idempotencyKey) {
		stdhttp.Error(w, "dashboard append identities must be canonical UUIDv7 values", stdhttp.StatusBadRequest)
		return
	}
	intent, err := buildExplorationDashboardAppendAuditIntent(r, h.DashboardAppendCommand.OperationID(), projectID, principal.ID, body.DashboardID)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	auditContext := authoring.WithAuditIntent(r.Context(), intent)
	result, err := h.DashboardAuthoring.AppendExploration(auditContext, dashboardauthoring.ExplorationAppendRequest{
		ProjectID: projectID, ActorID: principal.ID, DashboardID: authoring.DashboardID(body.DashboardID),
		PageID: body.PageID, RevisionToken: body.RevisionToken, IdempotencyKey: idempotencyKey,
		PlacementChoice: body.PlacementChoice, Spec: body.Spec,
	})
	if err != nil {
		writeExplorationDashboardError(w, err, true)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(stdhttp.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		DashboardID string                  `json:"dashboardId"`
		Revision    authoring.RevisionToken `json:"revision"`
	}{DashboardID: result.Lifecycle.ID.String(), Revision: result.Revision})
}

func buildExplorationDashboardAppendAuditIntent(r *stdhttp.Request, operationID string, projectID projectgraph.ResourceID, actorID, dashboardID string) (access.AuditIntent, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !canonicalDashboardAppendUUIDv7(requestID) || !canonicalDashboardAppendUUIDv7(idempotencyKey) {
		return access.AuditIntent{}, errors.New("dashboard append identities must be canonical UUIDv7 values")
	}
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	return authoring.BuildExplorationAppendAuditIntent(operationID, projectID.String(), actorID, dashboardID, requestID, idempotencyKey, correlationID)
}

func canonicalDashboardAppendUUIDv7(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 7 && parsed.String() == value
}

func (h *BrowserHandler) explorationDashboardPrincipal(w stdhttp.ResponseWriter, r *stdhttp.Request) (Principal, projectgraph.ResourceID, bool) {
	if h == nil || h.DashboardAuthoring == nil || h.CurrentUser == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return Principal{}, "", false
	}
	principal, ok := h.CurrentUser(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnauthorized), stdhttp.StatusUnauthorized)
		return Principal{}, "", false
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return Principal{}, "", false
	}
	return principal, projectID, true
}

func decodeExplorationDashboardJSON(body io.Reader, target any) error {
	const maxBodyBytes = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(body, maxBodyBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBodyBytes {
		return errors.New("request body exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func (h *BrowserHandler) authorizeExplorationModel(w stdhttp.ResponseWriter, r *stdhttp.Request, modelID string) bool {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || h.Catalog == nil || h.CurrentUser == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return false
	}
	principal, ok := h.CurrentUser(r)
	if !ok {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnauthorized), stdhttp.StatusUnauthorized)
		return false
	}
	if _, err := h.Catalog.Resolve(r.Context(), principal.ID, projectcatalog.Ref{ID: projectgraph.ResourceID(modelID), Kind: projectgraph.KindSemanticModel}, access.CapabilityResourceRead, principal.DevBypass); err != nil {
		uitransport.WriteBrowserAuthorizationError(w, r, stdhttp.StatusForbidden)
		return false
	}
	return true
}

func (h *BrowserHandler) authorizeExplorationDashboardAppendReplay(r *stdhttp.Request, raw []byte) bool {
	if h == nil || h.DashboardAuthoring == nil || h.CurrentUser == nil || h.Catalog == nil {
		return false
	}
	var body explorationDashboardAppendBody
	if decodeExplorationDashboardJSON(bytes.NewReader(raw), &body) != nil || strings.TrimSpace(body.PageID) == "" {
		return false
	}
	principal, ok := h.CurrentUser(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return false
	}
	modelID := strings.TrimSpace(body.Spec.ModelID)
	if modelID == "" {
		return false
	}
	if _, err := h.Catalog.Resolve(r.Context(), principal.ID, projectcatalog.Ref{ID: projectgraph.ResourceID(modelID), Kind: projectgraph.KindSemanticModel}, access.CapabilityResourceRead, principal.DevBypass); err != nil {
		return false
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		return false
	}
	target, err := h.DashboardAuthoring.ExplorationTarget(r.Context(), dashboardauthoring.ExplorationTargetRequest{
		ProjectID: projectID, ActorID: principal.ID, SourceModelID: modelID,
		DashboardID: authoring.DashboardID(strings.TrimSpace(body.DashboardID)),
	})
	if err != nil {
		return false
	}
	for _, page := range target.Pages {
		if page.ID == body.PageID {
			return true
		}
	}
	return false
}

func writeExplorationDashboardError(w stdhttp.ResponseWriter, err error, appendRequest bool) {
	status := stdhttp.StatusServiceUnavailable
	switch {
	case errors.Is(err, access.ErrForbidden):
		status = stdhttp.StatusForbidden
	case errors.Is(err, authoring.ErrNotFound):
		status = stdhttp.StatusNotFound
	case errors.Is(err, authoring.ErrStaleRevision), errors.Is(err, authoring.ErrConflict):
		status = stdhttp.StatusConflict
	default:
		if appendRequest {
			status = stdhttp.StatusUnprocessableEntity
		}
	}
	message := "dashboard target is unavailable"
	if appendRequest {
		message = "could not add this exploration to the selected dashboard"
	}
	stdhttp.Error(w, message, status)
}
