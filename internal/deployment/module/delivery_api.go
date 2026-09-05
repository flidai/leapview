package module

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

type approvalDecision uint8

const (
	approvalDecisionApprove approvalDecision = iota + 1
	approvalDecisionDeny
	approvalDecisionRevoke
)

// DeliveryPlanIntent contains only portable authoring intent. The canonical
// coordinator resolves target bindings, policies, qualification, and the
// authoritative base fence before persisting a DeliveryPlan.
type DeliveryPlanIntent struct {
	ProjectID               projectgraph.ResourceID
	PrincipalID             string
	SourceOwnerID           string
	Environment             string
	TargetID                string
	Operation               deployment.DeliveryOperationKind
	SourceDigest            string
	SourceAttestationDigest string
	PipelinePlan            *deployment.PipelinePlan
}

func decodePlanIntent(project, environment, principalID string, body deploymentgen.DeliveryPlanRequest) (DeliveryPlanIntent, error) {
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		return DeliveryPlanIntent{}, fmt.Errorf("%w: project", deployment.ErrDeliveryInvalid)
	}
	return DeliveryPlanIntent{ProjectID: projectID, PrincipalID: principalID, SourceOwnerID: principalID, Environment: environment, TargetID: body.TargetId, Operation: deployment.DeliveryOperationKind(body.Operation), SourceDigest: body.SourceDigest, SourceAttestationDigest: body.SourceAttestationDigest}, nil
}

func (m *Module) deliveryReadReady(w http.ResponseWriter, r *http.Request, project string) bool {
	if _, ok := m.principal(r); !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return false
	}
	if m == nil || m.nativeDeliveryReader == nil {
		m.writeDeliveryReadError(w, r, errors.New("delivery reader is unavailable"))
		return false
	}
	if _, err := projectgraph.NewResourceID(project); err != nil {
		m.writeDeliveryReadError(w, r, fmt.Errorf("%w: invalid project", deployment.ErrDeliveryInvalid))
		return false
	}
	return true
}

func (m *Module) writeDeliveryReadError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusServiceUnavailable, "DELIVERY_READ_UNAVAILABLE", "Delivery status is temporarily unavailable"
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, deployment.ErrNotFound):
		status, code, message = http.StatusNotFound, "DELIVERY_OBJECT_NOT_FOUND", "The requested delivery object was not found"
	case errors.Is(err, deployment.ErrDeliveryInvalid):
		status, code, message = http.StatusUnprocessableEntity, "INVALID_DELIVERY_READ", "The delivery status request is invalid"
	case errors.Is(err, deployment.ErrDeliveryConflict), errors.Is(err, deployment.ErrDeliveryStale):
		status, code, message = http.StatusConflict, "DELIVERY_READ_CONFLICT", "Delivery status changed while it was being read"
	}
	apitransport.WriteProblem(w, r, status, code, message, nil)
}

func (m *Module) writeDeliveryMutationError(w http.ResponseWriter, r *http.Request, err error) {
	m.candidateLogger().WarnContext(
		r.Context(),
		"canonical delivery mutation failed",
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
	status, code, message := http.StatusServiceUnavailable, "DELIVERY_INPUT_UNAVAILABLE", "Target-owned delivery inputs are temporarily unavailable"
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, deployment.ErrNotFound):
		status, code, message = http.StatusNotFound, "DELIVERY_OBJECT_NOT_FOUND", "The requested delivery object was not found"
	case errors.Is(err, deployment.ErrDeliveryInvalid):
		status, code, message = http.StatusUnprocessableEntity, "INVALID_DELIVERY_MUTATION", "The delivery mutation request is invalid"
	case errors.Is(err, deployment.ErrDeliveryPlanExpired):
		status, code, message = http.StatusConflict, "DELIVERY_PLAN_EXPIRED", "The immutable delivery plan has expired"
	case errors.Is(err, deployment.ErrDeliveryStale):
		status, code, message = http.StatusConflict, "DELIVERY_STALE", "The delivery plan or target fence is stale"
	case errors.Is(err, ErrDeliveryIdempotencyDrift), errors.Is(err, deployment.ErrDeliveryIdempotencyDrift):
		status, code, message = http.StatusConflict, "DELIVERY_IDEMPOTENCY_DRIFT", "The delivery idempotency key was reused with different inputs"
	case errors.Is(err, ErrDeliveryForbidden), errors.Is(err, ErrPublicationForbidden), errors.Is(err, ErrApprovalForbidden), errors.Is(err, ErrActivationForbidden):
		status, code, message = http.StatusForbidden, "DELIVERY_FORBIDDEN", "The caller is not authorized for this delivery operation"
	case errors.Is(err, deployment.ErrDeliveryConflict), errors.Is(err, deployment.ErrDeliveryTransition):
		status, code, message = http.StatusConflict, "DELIVERY_CONFLICT", "The delivery operation conflicts with current target state"
	case errors.Is(err, ErrDeliveryApprovalRequired), errors.Is(err, deployment.ErrApprovalRequired):
		status, code, message = http.StatusConflict, "DELIVERY_APPROVAL_REQUIRED", "Delivery approval is required by target policy"
	case errors.Is(err, deployment.ErrApprovalExpired):
		status, code, message = http.StatusConflict, "DELIVERY_PLAN_EXPIRED", "The delivery approval or plan has expired"
	case errors.Is(err, ErrDeliveryInputUnavailable):
		status, code, message = http.StatusServiceUnavailable, "DELIVERY_INPUT_UNAVAILABLE", "Target-owned delivery inputs are temporarily unavailable"
	}
	apitransport.WriteProblem(w, r, status, code, message, nil)
}

func (m *Module) deliveryMutationReady(w http.ResponseWriter, r *http.Request) bool {
	if !m.deliveryAuthReady(w, r) {
		return false
	}
	if m == nil || m.nativeDeliveryMutations == nil {
		m.writeDeliveryMutationError(w, r, ErrDeliveryInputUnavailable)
		return false
	}
	return true
}

func (m *Module) deliveryAuthReady(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := m.principal(r); !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return false
	}
	if m.handlerEnvironment() == "" {
		m.writeDeliveryReadError(w, r, errors.New("instance environment is required"))
		return false
	}
	return true
}

// deliveryPublicationMutationReady requires the clean-slate native
// publication port. Publication and rollback are target-owned operations and
// must never fall back to a broad compatibility coordinator.
func (m *Module) deliveryPublicationMutationReady(w http.ResponseWriter, r *http.Request) bool {
	if !m.deliveryAuthReady(w, r) {
		return false
	}
	if m == nil || m.nativeDeliveryPublication == nil {
		m.writeDeliveryMutationError(w, r, ErrDeliveryInputUnavailable)
		return false
	}
	return true
}

func planPreviewResponse(plan deployment.DeliveryPlan) deploymentgen.DeliveryPlanPreviewResponse {
	e := deployment.RedactedDeliveryPlanEvidence(plan)
	return deploymentgen.DeliveryPlanPreviewResponse{Id: plan.ID, ProjectId: plan.ProjectID.String(), TargetId: plan.TargetID, Environment: plan.Environment, Operation: deploymentgen.DeliveryOperationKind(plan.Operation), SourceDigest: plan.SourceDigest, SourceAttestationDigest: plan.Provenance.AttestationDigest, BaseGenerationId: optionalText(plan.BaseGenerationID), BaseTargetRevision: plan.BaseTargetRevision, ExecutionDigest: plan.ExecutionDigest, ProvenanceDigest: plan.ProvenanceDigest, GovernanceDigest: plan.GovernanceDigest, EvidenceDigest: plan.EvidenceDigest, PlanDigest: plan.Digest, Status: deploymentgen.DeliveryPlanStatus(plan.Status), ExpiresAt: isoTime(plan.Governance.ExpiresAt), CreatedAt: isoTime(plan.CreatedAt), Evidence: deliveryPlanEvidenceView(e)}
}

func deliveryPlanEvidenceView(e deployment.DeliveryPlanEvidenceView) deploymentgen.DeliveryPlanEvidenceView {
	result := deploymentgen.DeliveryPlanEvidenceView{
		Digest: e.Digest, ImpactStatement: optionalText(e.ImpactStatement), PhysicalWorkStatement: optionalText(e.PhysicalWorkStatement), ReuseStatement: optionalText(e.ReuseStatement),
		CompatibilityBreaking: e.CompatibilityBreaking, AddedCount: int32(e.AddedCount), RemovedCount: int32(e.RemovedCount), DirectlyModifiedCount: int32(e.DirectlyModifiedCount), IndirectlyAffectedCount: int32(e.IndirectlyAffectedCount), ReuseCount: int32(e.ReuseCount), QualificationStepCount: int32(e.QualificationStepCount), RollbackClass: optionalRollbackClass(e.RollbackClass), QualificationPolicy: e.QualificationPolicy,
		StalePolicy: deploymentgen.DeliveryStalePolicyView{Mode: deploymentgen.DeliveryStalePolicyMode(e.StalePolicy.Mode), AllowRetainedBase: e.StalePolicy.AllowRetainedBase, Description: optionalText(e.StalePolicy.Description)},
	}
	for _, input := range e.PlannedInputs {
		result.PlannedInputs = append(result.PlannedInputs, deploymentgen.DeliveryPlannedInputView{Id: input.ID, Mode: deploymentgen.DeliveryDataInputMode(input.Mode), Revision: optionalText(input.Revision), Bound: optionalText(input.Bound)})
	}
	for _, step := range e.QualificationSteps {
		result.QualificationSteps = append(result.QualificationSteps, deploymentgen.DeliveryQualificationStepView{Id: step.ID, Kind: step.Kind, Description: step.Description, Required: step.Required, Blocking: step.Blocking})
	}
	for _, decision := range e.ReuseDecisions {
		result.ReuseDecisions = append(result.ReuseDecisions, deploymentgen.DeliveryReuseDecisionView{ResourceId: decision.ResourceID, Reusable: decision.Reusable, RetainBase: decision.RetainBase, Reason: decision.Reason, ReuseKeyDigest: optionalText(decision.ReuseKeyDigest)})
	}
	return result
}

func (m *Module) CreateDeliveryPlan(w http.ResponseWriter, r *http.Request, project, idempotencyKey string) {
	if !m.deliveryMutationReady(w, r) {
		return
	}
	var body deploymentgen.DeliveryPlanRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	principal, _ := m.principal(r)
	intent, err := decodePlanIntent(project, m.handlerEnvironment(), principal.ID, body)
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	operation := intent.Operation
	if operation == "" {
		operation = deployment.DeliveryOperationCodeChange
	}
	nativeRequest := NativeDeliveryPlanRequest{
		ProjectID: intent.ProjectID, TargetID: intent.TargetID, Environment: intent.Environment,
		PrincipalID: intent.PrincipalID, SourceOwnerID: intent.SourceOwnerID, Operation: string(operation), SourceDigest: intent.SourceDigest,
		SourceAttestationDigest: intent.SourceAttestationDigest, IdempotencyKey: idempotencyKey, PipelinePlan: intent.PipelinePlan,
	}
	if err := nativeRequest.validate(m.handlerEnvironment()); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	created, err := m.nativeDeliveryMutations.CreatePlan(r.Context(), nativeRequest)
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := created.validate(nativeRequest, m.handlerEnvironment()); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := completeNativePlanCommand(r.Context(), m.nativeDeliveryMutations, created); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/plans/%s", project, created.ID.String()))
	apitransport.WriteJSON(w, http.StatusCreated, nativePlanPreviewResponse(created))
}

func (m *Module) BuildDeliveryPlan(w http.ResponseWriter, r *http.Request, project, planID, idempotencyKey string) {
	if !m.deliveryMutationReady(w, r) {
		return
	}
	principal, _ := m.principal(r)
	parsedPlanID, parseErr := uuid.Parse(planID)
	if parseErr != nil || parsedPlanID == uuid.Nil || parsedPlanID.String() != planID {
		m.writeDeliveryMutationError(w, r, fmt.Errorf("%w: plan identity must be a canonical UUID", deployment.ErrDeliveryInvalid))
		return
	}
	projectID, projectErr := projectgraph.NewResourceID(project)
	if projectErr != nil {
		m.writeDeliveryMutationError(w, r, fmt.Errorf("%w: project", deployment.ErrDeliveryInvalid))
		return
	}
	nativeRequest := NativeDeliveryBuildRequest{ProjectID: projectID, TargetID: m.instanceID, Environment: m.handlerEnvironment(), PlanID: parsedPlanID, PrincipalID: principal.ID, IdempotencyKey: idempotencyKey}
	if err := nativeRequest.validate(m.handlerEnvironment()); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	// Native builds cross the physical DuckDB boundary and therefore must
	// execute under the candidate-preparation admission authority. The
	// adapter reuses an outer refresh admission (when present) instead of
	// attempting a conflicting nested control admission.
	if m.candidateAdmission == nil {
		m.writeDeliveryMutationError(w, r, ErrDeliveryInputUnavailable)
		return
	}
	preparationLease, err := m.candidateAdmission.AcquireCandidatePreparation(r.Context())
	if err != nil {
		m.writeDeliveryMutationError(w, r, candidatePreparationError(err))
		return
	}
	if preparationLease == nil {
		m.writeDeliveryMutationError(w, r, ErrDeliveryInputUnavailable)
		return
	}
	defer preparationLease.Release()
	buildContext := preparationLease.Context()
	if buildContext == nil {
		m.writeDeliveryMutationError(w, r, ErrDeliveryInputUnavailable)
		return
	}
	built, err := m.nativeDeliveryMutations.BuildPlan(buildContext, nativeRequest)
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := built.validate(nativeRequest); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := completeNativeBuildCommand(buildContext, m.nativeDeliveryMutations, built); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	response := nativeBuildStatusResponse(built)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/builds/%s", project, built.ID.String()))
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) PublishDeliveryCandidate(w http.ResponseWriter, r *http.Request, project, candidateID, idempotencyKey string) {
	if !m.deliveryPublicationMutationReady(w, r) {
		return
	}
	principal, _ := m.principal(r)
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil || project != strings.TrimSpace(project) {
		m.writeDeliveryMutationError(w, r, fmt.Errorf("%w: project identity must be canonical", deployment.ErrDeliveryInvalid))
		return
	}
	parsedCandidate, parseErr := uuid.Parse(candidateID)
	if parseErr != nil || parsedCandidate == uuid.Nil || parsedCandidate.String() != candidateID {
		m.writeDeliveryMutationError(w, r, fmt.Errorf("%w: candidate identity must be a canonical UUID", deployment.ErrDeliveryInvalid))
		return
	}
	nativeRequest := NativeDeliveryPublishRequest{ProjectID: projectID, TargetID: m.instanceID, Environment: m.handlerEnvironment(), CandidateID: parsedCandidate, PrincipalID: principal.ID, IdempotencyKey: idempotencyKey}
	if err := nativeRequest.validate(m.handlerEnvironment()); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	publication, err := m.nativeDeliveryPublication.PublishCandidate(r.Context(), nativeRequest)
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := publication.validate(projectID, m.instanceID, m.handlerEnvironment()); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := completeNativePublishCommand(r.Context(), m.nativeDeliveryPublication, publication); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	response := nativePublicationEvidenceResponse(publication)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/publications/%s", project, publication.ID.String()))
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func (m *Module) RequestDeliveryPublicationApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	publicationID string,
	idempotencyKey string,
) {
	operationID := deploymentgen.GenCommandOperationRequestDeliveryPublicationApproval()
	if m == nil || m.nativeDeliveryApproval == nil {
		m.writeCommandFailure(w, r, operationID, ErrDeliveryInputUnavailable)
		return
	}
	principal, ok := m.principal(r)
	if !ok {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(nativepostgres.ErrApprovalUnauthorized))
		return
	}
	actor, ok := m.approvalActor(r, principal.ID)
	if !ok {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(nativepostgres.ErrApprovalUnauthorized))
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	publicationUUID, parseErr := uuid.Parse(publicationID)
	if err != nil || project != strings.TrimSpace(project) || parseErr != nil || publicationUUID.String() != publicationID {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(nativepostgres.ErrApprovalInvalid))
		return
	}
	approval, err := m.nativeDeliveryApproval.RequestPublicationApproval(r.Context(), NativeApprovalRequest{ProjectID: projectID.String(), TargetID: m.instanceID, Environment: m.handlerEnvironment(), PublicationID: publicationUUID, PrincipalID: principal.ID, IdempotencyKey: idempotencyKey, Actor: actor})
	if err != nil {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(err))
		return
	}
	if err := completeNativeApprovalCommand(r.Context(), m.nativeDeliveryApproval, operationID.APIGenOperationID(), "delivery.publication.approval_requested", approval, nativepostgres.ApprovalActionRequest); err != nil {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(err))
		return
	}
	w.Header().Set("Location", approvalLocation(project, publicationID, approval.RequestID))
	apitransport.WriteJSON(w, http.StatusCreated, nativeApprovalResponse(project, m.handlerEnvironment(), approval))
}

func (m *Module) GetDeliveryPublicationApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	publicationID,
	approvalID string,
) {
	if m == nil || m.nativeDeliveryApproval == nil {
		writeAPIError(w, r, ErrDeliveryInputUnavailable)
		return
	}
	if _, ok := m.principal(r); !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	publicationUUID, parseErr := uuid.Parse(publicationID)
	if parseErr != nil || publicationUUID.String() != publicationID {
		writeAPIError(w, r, deployment.ErrApprovalNotFound)
		return
	}
	approval, err := m.nativeDeliveryApproval.GetPublicationApproval(r.Context(), NativeApprovalLookup{ProjectID: project, TargetID: m.instanceID, Environment: m.handlerEnvironment(), PublicationID: publicationUUID.String(), RequestID: approvalID})
	if err != nil {
		writeAPIError(w, r, mapNativeApprovalReadError(err))
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, nativeApprovalResponse(project, m.handlerEnvironment(), approval))
}

func (m *Module) transitionDeliveryPublicationApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	publicationID,
	approvalID,
	idempotencyKey string,
	decision approvalDecision,
) {
	operationID := operationIDForDeliveryDecision(decision)
	if m == nil || m.nativeDeliveryApproval == nil {
		m.writeCommandFailure(w, r, operationID, ErrDeliveryInputUnavailable)
		return
	}
	m.transitionNativeDeliveryPublicationApproval(w, r, project, publicationID, approvalID, idempotencyKey, decision)
}

func operationIDForDeliveryDecision(decision approvalDecision) deploymentgen.GenCommandOperationID {
	switch decision {
	case approvalDecisionApprove:
		return deploymentgen.GenCommandOperationApproveDeliveryPublicationApproval()
	case approvalDecisionDeny:
		return deploymentgen.GenCommandOperationDenyDeliveryPublicationApproval()
	default:
		return deploymentgen.GenCommandOperationRevokeDeliveryPublicationApproval()
	}
}

func (m *Module) ApproveDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publicationID, approvalID, idempotencyKey string) {
	m.transitionDeliveryPublicationApproval(w, r, project, publicationID, approvalID, idempotencyKey, approvalDecisionApprove)
}

func (m *Module) DenyDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publicationID, approvalID, idempotencyKey string) {
	m.transitionDeliveryPublicationApproval(w, r, project, publicationID, approvalID, idempotencyKey, approvalDecisionDeny)
}

func (m *Module) RevokeDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publicationID, approvalID, idempotencyKey string) {
	m.transitionDeliveryPublicationApproval(w, r, project, publicationID, approvalID, idempotencyKey, approvalDecisionRevoke)
}

func (m *Module) transitionNativeDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publicationID, approvalID, idempotencyKey string, decision approvalDecision) {
	operationID := operationIDForDeliveryDecision(decision)
	var body deploymentapi.ApprovalDecisionRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	principal, ok := m.principal(r)
	if !ok {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(nativepostgres.ErrApprovalUnauthorized))
		return
	}
	actor, ok := m.approvalActor(r, principal.ID)
	if !ok {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(nativepostgres.ErrApprovalUnauthorized))
		return
	}
	pub, parseErr := uuid.Parse(publicationID)
	if parseErr != nil || pub.String() != publicationID || strings.TrimSpace(idempotencyKey) != idempotencyKey || idempotencyKey == "" {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(nativepostgres.ErrApprovalInvalid))
		return
	}
	input := NativeApprovalDecision{ProjectID: project, TargetID: m.instanceID, Environment: m.handlerEnvironment(), PublicationID: publicationID, RequestID: approvalID, ExpectedRevision: body.ExpectedRevision, IdempotencyKey: idempotencyKey, Actor: actor}
	var approval nativepostgres.ApprovalRequest
	var err error
	switch decision {
	case approvalDecisionApprove:
		approval, err = m.nativeDeliveryApproval.ApprovePublicationApproval(r.Context(), input)
	case approvalDecisionDeny:
		approval, err = m.nativeDeliveryApproval.DenyPublicationApproval(r.Context(), input)
	case approvalDecisionRevoke:
		approval, err = m.nativeDeliveryApproval.RevokePublicationApproval(r.Context(), input)
	default:
		err = nativepostgres.ErrApprovalInvalid
	}
	if err != nil {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(err))
		return
	}
	action := nativepostgres.ApprovalActionRevoke
	eventType := "delivery.publication.approval_revoked"
	if decision == approvalDecisionApprove {
		action = nativepostgres.ApprovalActionApprove
		eventType = "delivery.publication.approved"
	} else if decision == approvalDecisionDeny {
		action = nativepostgres.ApprovalActionDeny
		eventType = "delivery.publication.denied"
	}
	if err := completeNativeApprovalCommand(r.Context(), m.nativeDeliveryApproval, operationID.APIGenOperationID(), eventType, approval, action); err != nil {
		m.writeCommandFailure(w, r, operationID, mapNativeApprovalError(err))
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, nativeApprovalResponse(project, m.handlerEnvironment(), approval))
}

func (m *Module) RollbackDeliveryGeneration(w http.ResponseWriter, r *http.Request, project, generationID, idempotencyKey string) {
	if !m.deliveryPublicationMutationReady(w, r) {
		return
	}
	principal, _ := m.principal(r)
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil || project != strings.TrimSpace(project) {
		m.writeDeliveryMutationError(w, r, fmt.Errorf("%w: project identity must be canonical", deployment.ErrDeliveryInvalid))
		return
	}
	parsedGeneration, parseErr := uuid.Parse(generationID)
	if parseErr != nil || parsedGeneration == uuid.Nil || parsedGeneration.String() != generationID {
		m.writeDeliveryMutationError(w, r, fmt.Errorf("%w: generation identity must be a canonical UUID", deployment.ErrDeliveryInvalid))
		return
	}
	nativeRequest := NativeDeliveryRollbackRequest{ProjectID: projectID, TargetID: m.instanceID, Environment: m.handlerEnvironment(), GenerationID: parsedGeneration, PrincipalID: principal.ID, IdempotencyKey: idempotencyKey}
	if err := nativeRequest.validate(m.handlerEnvironment()); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	publication, err := m.nativeDeliveryPublication.RollbackGeneration(r.Context(), nativeRequest)
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := publication.validate(projectID, m.instanceID, m.handlerEnvironment()); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := completeNativeRollbackCommand(r.Context(), m.nativeDeliveryPublication, publication); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	response := nativePublicationEvidenceResponse(publication)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/publications/%s", project, publication.ID.String()))
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func nativePublicationEvidenceResponse(publication NativeDeliveryPublication) deploymentgen.DeliveryPublicationEvidenceResponse {
	return deploymentgen.DeliveryPublicationEvidenceResponse{
		Id: publication.ID.String(), RequestDigest: publication.RequestDigest, TargetId: publication.TargetID,
		ProjectId: publication.ProjectID.String(), Environment: publication.Environment,
		PlanId: publication.PlanID.String(), PlanDigest: publication.PlanDigest,
		CandidateId: publication.CandidateID.String(), GenerationId: publication.GenerationID.String(),
		ExpectedBaseGenerationId: nativeOptionalUUID(publication.ExpectedBaseGenerationID),
		ExpectedTargetRevision:   publication.ExpectedTargetRevision, ResultTargetRevision: publication.ResultTargetRevision,
		Status: deploymentgen.DeliveryPublicationStatus(publication.Status), CreatedAt: isoTime(publication.CreatedAt),
		CompletedAt: optionalText(isoTime(publication.CompletedAt)),
	}
}

func nativeOptionalUUID(value uuid.UUID) *string {
	if value == uuid.Nil {
		return nil
	}
	text := value.String()
	return &text
}

func isoTime(value interface {
	IsZero() bool
	Format(string) string
}) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02T15:04:05.999999999Z07:00")
}

func optionalText(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func optionalRollbackClass(value string) *deploymentgen.DeliveryRollbackClass {
	if value == "" {
		return nil
	}
	converted := deploymentgen.DeliveryRollbackClass(value)
	return &converted
}

func deliveryResolvedInputViews(inputs deployment.DeliveryResolvedBuildInputs) []deploymentgen.DeliveryResolvedInputView {
	if len(inputs.Inputs) == 0 {
		return []deploymentgen.DeliveryResolvedInputView{}
	}
	result := make([]deploymentgen.DeliveryResolvedInputView, 0, len(inputs.Inputs))
	for _, input := range inputs.Inputs {
		result = append(result, deploymentgen.DeliveryResolvedInputView{Id: input.ID, Mode: deploymentgen.DeliveryDataInputMode(input.Mode), PlannedRevision: optionalText(input.PlannedRevision), PlannedBound: optionalText(input.PlannedBound), ActualRevision: optionalText(input.ActualRevision), ActualBound: optionalText(input.ActualBound), ObservationDigest: optionalText(input.ObservationDigest), Explanation: input.Explanation})
	}
	return result
}

func (m *Module) GetDeliveryPlanPreview(w http.ResponseWriter, r *http.Request, project, planID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	plan, err := nativeReadPlan(r.Context(), m.nativeDeliveryReader, planID)
	if err == nil {
		err = validateNativeReadScope(m, project, plan)
	}
	if err != nil {
		m.writeDeliveryReadError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, nativePlanResponse(plan))
}

func (m *Module) GetDeliveryBuildStatus(w http.ResponseWriter, r *http.Request, project, buildID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	attempt, err := m.nativeDeliveryReader.BuildAttempt(r.Context(), buildID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
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
		if err != nil {
			m.writeDeliveryReadError(w, r, nativeReadError(err))
			return
		}
		if candidate.SnapshotSealID != "" {
			seal, err = m.nativeDeliveryReader.SnapshotSeal(r.Context(), candidate.SnapshotSealID)
			if err != nil {
				m.writeDeliveryReadError(w, r, nativeReadError(err))
				return
			}
		}
	}
	apitransport.WriteJSON(w, http.StatusOK, nativeBuildResponse(attempt, plan, candidate, seal))
}

func (m *Module) GetDeliverySealStatus(w http.ResponseWriter, r *http.Request, project, sealID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	seal, err := m.nativeDeliveryReader.SnapshotSeal(r.Context(), sealID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	attempt, err := m.nativeDeliveryReader.BuildAttempt(r.Context(), seal.AttemptID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	plan, err := nativeReadPlan(r.Context(), m.nativeDeliveryReader, attempt.PlanID)
	if err == nil {
		err = validateNativeReadScope(m, project, plan)
	}
	if err != nil {
		m.writeDeliveryReadError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, nativeSealResponse(seal, plan))
}

func (m *Module) GetDeliveryCandidateStatus(w http.ResponseWriter, r *http.Request, project, candidateID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	candidate, err := m.nativeDeliveryReader.Candidate(r.Context(), candidateID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
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
	apitransport.WriteJSON(w, http.StatusOK, nativeCandidateResponse(candidate, plan, seal, servingStateID))
}

func (m *Module) GetDeliveryGenerationStatus(w http.ResponseWriter, r *http.Request, project, generationID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	generation, err := m.nativeDeliveryReader.Generation(r.Context(), generationID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
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
	operator, err := m.nativeDeliveryReader.OperatorSnapshot(r.Context(), generation.TargetID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	if operator.ProjectID != project || (m.handlerEnvironment() != "" && operator.Environment != m.handlerEnvironment()) {
		m.writeDeliveryReadError(w, r, fmt.Errorf("%w: generation target scope differs", deployment.ErrNotFound))
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, nativeGenerationResponse(generation, plan, seal, operator.ActiveGenerationID == generation.GenerationID))
}

func (m *Module) GetDeliveryPublicationEvidence(w http.ResponseWriter, r *http.Request, project, publicationID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	publication, err := m.nativeDeliveryReader.Publication(r.Context(), publicationID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	generation, err := m.nativeDeliveryReader.Generation(r.Context(), publication.GenerationID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
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
	apitransport.WriteJSON(w, http.StatusOK, nativePublicationResponse(publication, generation, plan))
}

func (m *Module) GetDeliveryOperatorSnapshot(w http.ResponseWriter, r *http.Request, project string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	if m.handlerEnvironment() == "" {
		m.writeDeliveryReadError(w, r, errors.New("instance environment is required for operator status"))
		return
	}
	snapshot, err := m.nativeDeliveryReader.OperatorSnapshot(r.Context(), m.instanceID)
	if err != nil {
		m.writeDeliveryReadError(w, r, nativeReadError(err))
		return
	}
	if snapshot.ProjectID != project || snapshot.Environment != m.handlerEnvironment() {
		m.writeDeliveryReadError(w, r, fmt.Errorf("%w: operator target scope differs", deployment.ErrNotFound))
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, nativeOperatorResponse(snapshot))
}
