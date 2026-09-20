package module

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	protocolgen "github.com/flidai/leapview/internal/platform/http/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

// nativeDeliveryCollectionReader is deliberately an optional extension of
// NativeDeliveryReader. Narrow/offline readers used by existing composition
// tests remain valid, while the PostgreSQL authority exposes bounded native
// collection pages in production.
type nativeDeliveryCollectionReader interface {
	ListPublications(context.Context, string, string, string, int32, string) (nativepostgres.DeliveryPublicationPage, error)
	ListRetainedGenerations(context.Context, string, string, string, int32, string) (nativepostgres.DeliveryGenerationPage, error)
}

// Keep each extension narrow so offline/read-only test readers can expose only
// the collection they model. The production PostgreSQL repository implements
// all four interfaces.
type nativeDeliveryPlanCollectionReader interface {
	ListPlans(context.Context, string, string, string, int32, string) (nativepostgres.DeliveryPlanPage, error)
}

type nativeDeliveryBuildCollectionReader interface {
	ListBuildAttempts(context.Context, string, string, string, int32, string) (nativepostgres.DeliveryBuildAttemptPage, error)
}

type nativeDeliveryCandidateCollectionReader interface {
	ListCandidates(context.Context, string, string, string, int32, string) (nativepostgres.DeliveryCandidatePage, error)
}

type nativeDeliveryApprovalCollectionReader interface {
	ListApprovalRequests(context.Context, string, string, string, int32, string) (nativepostgres.ApprovalRequestPage, error)
}

func nativeGeneratedApprovalResponse(project, environment string, approval nativepostgres.ApprovalRequest) deploymentgen.DeploymentApprovalResponse {
	response := nativeApprovalResponse(project, environment, approval)
	return deploymentgen.DeploymentApprovalResponse{
		Id: response.ID, ProjectId: response.ProjectID, DeploymentId: response.DeploymentID,
		Environment: response.Environment, RequestDigest: response.RequestDigest, ReleaseId: response.ReleaseID,
		Status: deploymentgen.DeploymentApprovalStatus(response.Status), RequestedBy: response.RequestedBy,
		RequestedAt: response.RequestedAt, ExpiresAt: response.ExpiresAt, Revision: response.Revision,
		ApprovedBy: response.ApprovedBy, ApprovedAt: response.ApprovedAt,
		DeniedBy: response.DeniedBy, DeniedAt: response.DeniedAt,
		RevokedBy: response.RevokedBy, RevokedAt: response.RevokedAt,
	}
}

func (m *Module) nativeCollectionReader(w http.ResponseWriter, r *http.Request, project string) (nativeDeliveryCollectionReader, nativepostgres.DeliveryOperatorSnapshot, bool) {
	if !m.deliveryReadReady(w, r, project) {
		return nil, nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	reader, ok := m.nativeDeliveryReader.(nativeDeliveryCollectionReader)
	if !ok {
		m.writeDeliveryReadError(w, r, fmt.Errorf("native delivery collection reader is unavailable"))
		return nil, nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	if m.instanceID == "" || m.handlerEnvironment() == "" {
		m.writeDeliveryReadError(w, r, fmt.Errorf("%w: native delivery target scope is unavailable", deployment.ErrDeliveryInvalid))
		return nil, nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	snapshot, err := m.nativeDeliveryReader.OperatorSnapshot(r.Context(), m.instanceID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return nil, nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	if snapshot.ProjectID != project || snapshot.TargetID != m.instanceID || snapshot.Environment != m.handlerEnvironment() {
		m.writeDeliveryReadError(w, r, fmt.Errorf("%w: delivery collection target scope differs", deployment.ErrNotFound))
		return nil, nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	return reader, snapshot, true
}

func nativeCollectionParams(limit *int32, token *string) (int32, string) {
	value := int32(apitransport.DefaultListLimit)
	if limit != nil {
		value = *limit
	}
	pageToken := ""
	if token != nil {
		pageToken = *token
	}
	return value, pageToken
}

func (m *Module) nativeCollectionSnapshot(w http.ResponseWriter, r *http.Request, project string) (nativepostgres.DeliveryOperatorSnapshot, bool) {
	if !m.deliveryReadReady(w, r, project) {
		return nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	if m.instanceID == "" || m.handlerEnvironment() == "" {
		m.writeDeliveryReadError(w, r, fmt.Errorf("%w: native delivery target scope is unavailable", deployment.ErrDeliveryInvalid))
		return nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	snapshot, err := m.nativeDeliveryReader.OperatorSnapshot(r.Context(), m.instanceID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	if snapshot.ProjectID != project || snapshot.TargetID != m.instanceID || snapshot.Environment != m.handlerEnvironment() {
		m.writeDeliveryReadError(w, r, fmt.Errorf("%w: delivery collection target scope differs", deployment.ErrNotFound))
		return nativepostgres.DeliveryOperatorSnapshot{}, false
	}
	return snapshot, true
}

func (m *Module) ListDeliveryPlans(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	if _, ok := m.nativeCollectionSnapshot(w, r, project); !ok {
		return
	}
	reader, ok := m.nativeDeliveryReader.(nativeDeliveryPlanCollectionReader)
	if !ok {
		m.writeDeliveryReadError(w, r, fmt.Errorf("native delivery plan collection reader is unavailable"))
		return
	}
	pageLimit, token := nativeCollectionParams(limit, pageToken)
	page, err := reader.ListPlans(r.Context(), project, m.instanceID, m.handlerEnvironment(), pageLimit, token)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	items := make([]deploymentgen.DeliveryPlanPreviewResponse, 0, len(page.Items))
	for _, row := range page.Items {
		plan, err := nativeReadPlan(r.Context(), m.nativeDeliveryReader, row.PlanID)
		if err == nil {
			err = validateNativeReadScope(m, project, plan)
		}
		if err != nil {
			m.writeDeliveryReadError(w, r, err)
			return
		}
		items = append(items, nativePlanResponse(plan))
	}
	response := deploymentgen.DeliveryPlanListResponse{Items: items, Page: protocolgen.PageInfo{NextCursor: page.NextCursor}}
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) ListDeliveryBuildAttempts(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	if _, ok := m.nativeCollectionSnapshot(w, r, project); !ok {
		return
	}
	reader, ok := m.nativeDeliveryReader.(nativeDeliveryBuildCollectionReader)
	if !ok {
		m.writeDeliveryReadError(w, r, fmt.Errorf("native delivery build collection reader is unavailable"))
		return
	}
	pageLimit, token := nativeCollectionParams(limit, pageToken)
	page, err := reader.ListBuildAttempts(r.Context(), project, m.instanceID, m.handlerEnvironment(), pageLimit, token)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	items := make([]deploymentgen.DeliveryBuildStatusResponse, 0, len(page.Items))
	for _, attempt := range page.Items {
		plan, err := nativeReadPlan(r.Context(), m.nativeDeliveryReader, attempt.PlanID)
		if err == nil {
			err = validateNativeReadScope(m, project, plan)
		}
		if err != nil {
			m.writeDeliveryReadError(w, r, err)
			return
		}
		candidate := nativepostgres.DeliveryCandidate{}
		seal := nativepostgres.SnapshotSeal{}
		if attempt.CandidateID != "" {
			candidate, err = m.nativeDeliveryReader.Candidate(r.Context(), attempt.CandidateID)
			if err == nil && candidate.SnapshotSealID != "" {
				seal, err = m.nativeDeliveryReader.SnapshotSeal(r.Context(), candidate.SnapshotSealID)
			}
			if err != nil {
				m.writeDeliveryReadError(w, r, nativeReadError(err))
				return
			}
		}
		items = append(items, nativeBuildResponse(attempt, plan, candidate, seal))
	}
	response := deploymentgen.DeliveryBuildAttemptListResponse{Items: items, Page: protocolgen.PageInfo{NextCursor: page.NextCursor}}
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) ListDeliveryCandidates(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	if _, ok := m.nativeCollectionSnapshot(w, r, project); !ok {
		return
	}
	reader, ok := m.nativeDeliveryReader.(nativeDeliveryCandidateCollectionReader)
	if !ok {
		m.writeDeliveryReadError(w, r, fmt.Errorf("native delivery candidate collection reader is unavailable"))
		return
	}
	pageLimit, token := nativeCollectionParams(limit, pageToken)
	page, err := reader.ListCandidates(r.Context(), project, m.instanceID, m.handlerEnvironment(), pageLimit, token)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	items := make([]deploymentgen.DeliveryCandidateStatusResponse, 0, len(page.Items))
	for _, candidate := range page.Items {
		plan, err := nativeReadPlan(r.Context(), m.nativeDeliveryReader, candidate.PlanID)
		if err == nil {
			err = validateNativeReadScope(m, project, plan)
		}
		if err != nil {
			m.writeDeliveryReadError(w, r, err)
			return
		}
		seal := nativepostgres.SnapshotSeal{}
		if candidate.SnapshotSealID != "" {
			seal, err = m.nativeDeliveryReader.SnapshotSeal(r.Context(), candidate.SnapshotSealID)
			if err != nil {
				m.writeDeliveryReadError(w, r, nativeReadError(err))
				return
			}
		}
		servingStateID, err := resolveNativeCandidateServingState(r.Context(), m.nativeDeliveryReader, candidate, plan, seal)
		if err != nil {
			m.writeDeliveryReadError(w, r, err)
			return
		}
		response, err := nativeCandidateResponse(candidate, plan, seal, servingStateID)
		if err != nil {
			m.writeDeliveryReadError(w, r, err)
			return
		}
		items = append(items, response)
	}
	response := deploymentgen.DeliveryCandidateListResponse{Items: items, Page: protocolgen.PageInfo{NextCursor: page.NextCursor}}
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) ListDeliveryApprovalRequests(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	if _, ok := m.nativeCollectionSnapshot(w, r, project); !ok {
		return
	}
	reader, ok := m.nativeDeliveryReader.(nativeDeliveryApprovalCollectionReader)
	if !ok {
		m.writeDeliveryReadError(w, r, fmt.Errorf("native delivery approval collection reader is unavailable"))
		return
	}
	pageLimit, token := nativeCollectionParams(limit, pageToken)
	page, err := reader.ListApprovalRequests(r.Context(), project, m.instanceID, m.handlerEnvironment(), pageLimit, token)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	items := make([]deploymentgen.DeploymentApprovalResponse, 0, len(page.Items))
	for _, approval := range page.Items {
		if approval.TargetID != m.instanceID {
			m.writeDeliveryReadError(w, r, fmt.Errorf("%w: approval target scope differs", deployment.ErrNotFound))
			return
		}
		items = append(items, nativeGeneratedApprovalResponse(project, m.handlerEnvironment(), approval))
	}
	response := deploymentgen.DeliveryApprovalRequestListResponse{Items: items, Page: protocolgen.PageInfo{NextCursor: page.NextCursor}}
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) ListDeliveryPublications(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	reader, _, ok := m.nativeCollectionReader(w, r, project)
	if !ok {
		return
	}
	pageLimit, token := nativeCollectionParams(limit, pageToken)
	page, err := reader.ListPublications(r.Context(), project, m.instanceID, m.handlerEnvironment(), pageLimit, token)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	items := make([]deploymentgen.DeliveryPublicationEvidenceResponse, 0, len(page.Items))
	for _, publication := range page.Items {
		generation, err := m.nativeDeliveryReader.Generation(r.Context(), publication.GenerationID)
		if err != nil {
			m.writeDeliveryReadError(w, r, nativeReadError(err))
			return
		}
		plan, err := nativeReadPlan(r.Context(), m.nativeDeliveryReader, generation.PlanID)
		if err == nil {
			err = validateNativeReadScope(m, project, plan)
		}
		if err != nil || generation.TargetID != m.instanceID || publication.TargetID != m.instanceID {
			if err == nil {
				err = fmt.Errorf("%w: publication target scope differs", deployment.ErrNotFound)
			}
			m.writeDeliveryReadError(w, r, err)
			return
		}
		items = append(items, nativePublicationResponse(publication, generation, plan))
	}
	response := deploymentgen.DeliveryPublicationListResponse{Items: items, Page: protocolgen.PageInfo{NextCursor: page.NextCursor}}
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) ListRetainedDeliveryGenerations(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	reader, operator, ok := m.nativeCollectionReader(w, r, project)
	if !ok {
		return
	}
	pageLimit, token := nativeCollectionParams(limit, pageToken)
	page, err := reader.ListRetainedGenerations(r.Context(), project, m.instanceID, m.handlerEnvironment(), pageLimit, token)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	items := make([]deploymentgen.DeliveryGenerationStatusResponse, 0, len(page.Items))
	for _, generation := range page.Items {
		if generation.TargetID != m.instanceID {
			m.writeDeliveryReadError(w, r, fmt.Errorf("%w: generation target scope differs", deployment.ErrNotFound))
			return
		}
		plan, err := nativeReadPlan(r.Context(), m.nativeDeliveryReader, generation.PlanID)
		if err == nil {
			err = validateNativeReadScope(m, project, plan)
		}
		if err != nil {
			m.writeDeliveryReadError(w, r, err)
			return
		}
		seal, err := m.nativeDeliveryReader.SnapshotSeal(r.Context(), generation.SnapshotSealID)
		if err != nil {
			m.writeDeliveryReadError(w, r, nativeReadError(err))
			return
		}
		active, activatedAt, retiredAt, rollbackUntil, err := m.nativeGenerationLifecycle(r.Context(), generation, operator)
		if err != nil {
			m.writeDeliveryReadError(w, r, err)
			return
		}
		items = append(items, nativeGenerationResponse(generation, plan, seal, active, activatedAt, retiredAt, rollbackUntil))
	}
	response := deploymentgen.DeliveryRetainedGenerationListResponse{Items: items, Page: protocolgen.PageInfo{NextCursor: page.NextCursor}}
	apitransport.WriteJSON(w, http.StatusOK, response)
}
