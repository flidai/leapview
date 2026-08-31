package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// deliveryEventReader is intentionally narrower than deployment.DeliveryReader:
// the canonical SQLite repository exposes immutable event evidence, while
// alternate read ports do not need to carry the event ledger contract. The
// generated command guard verifies this evidence after the mutation has
// committed; no second audit event is emitted here.
type deliveryEventReader interface {
	DeliveryEventByRequest(context.Context, string, string, string, string, string) (deployment.DeliveryEvent, error)
	DeliveryEventsByObject(context.Context, string, string, string) ([]deployment.DeliveryEvent, error)
}

func (m *Module) completeDeliveryCommand(ctx context.Context, operationID, auditAction string, verify func(context.Context, deliveryEventReader) error) error {
	activeOperation, generated := apigencommand.OperationID(ctx)
	if !generated {
		return nil
	}
	if activeOperation != operationID {
		return fmt.Errorf("%w: active %q, completing %q", apigencommand.ErrOperationMismatch, activeOperation, operationID)
	}
	reader, ok := m.deliveryReader.(deliveryEventReader)
	if !ok {
		return fmt.Errorf("delivery event reader is unavailable")
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, m.candidateLogger())
	if err != nil {
		return err
	}
	return executor.Execute(ctx, activeOperation, apigencommand.Execution{Transactional: func(txCtx context.Context, contract apigencommand.Contract) error {
		if contract.AuditAction != auditAction {
			return fmt.Errorf("delivery command audit action mismatch: got %q, want %q", contract.AuditAction, auditAction)
		}
		if verify == nil {
			return fmt.Errorf("delivery command evidence verifier is unavailable")
		}
		return verify(txCtx, reader)
	}})
}

func acceptedDeliveryEvent(event deployment.DeliveryEvent, operationID string) error {
	if event.Outcome != "accepted" {
		return fmt.Errorf("%s durable evidence outcome is %q", operationID, event.Outcome)
	}
	return nil
}

func acceptedBuildEvidence(ctx context.Context, reader deliveryEventReader, targetID string, attempt deployment.DeliveryBuildAttempt) error {
	// Build returns a sealed attempt, so its command evidence must be the
	// terminal candidate_sealed event. A pre-seal build_transitioned event is
	// not sufficient because the physical seal/candidate transaction may still
	// fail after that transition.
	if attempt.CandidateID != "" {
		if events, err := reader.DeliveryEventsByObject(ctx, targetID, "candidate", attempt.CandidateID); err == nil {
			for _, event := range events {
				if event.EventKind == "candidate_sealed" {
					return acceptedDeliveryEvent(event, "buildDeliveryPlan")
				}
			}
		} else if !errors.Is(err, deployment.ErrNotFound) {
			return err
		}
	}
	return fmt.Errorf("buildDeliveryPlan durable seal evidence is unavailable")
}

func (m *Module) completeDeliveryApprovalCommand(ctx context.Context, operationID, auditAction string, publication deployment.DeliveryPublication, approval deployment.Approval, eventKind string) error {
	return m.completeDeliveryCommand(ctx, operationID, auditAction, func(ctx context.Context, reader deliveryEventReader) error {
		event, err := reader.DeliveryEventByRequest(ctx, publication.TargetID, publication.RequestDigest, eventKind, "approval", approval.ID)
		if err != nil {
			return fmt.Errorf("%s durable approval evidence is unavailable: %w", operationID, err)
		}
		return acceptedDeliveryEvent(event, operationID)
	})
}

// DeliveryMutationPort is implemented by the canonical delivery coordinator
// supplied by composition. The HTTP layer only supplies typed identities and
// CAS expectations; target admission, build sealing, and publication remain in
// the lifecycle owner.
type DeliveryMutationPort interface {
	CreatePlan(context.Context, DeliveryPlanIntent, string) (deployment.DeliveryPlan, error)
	BuildPlan(context.Context, string, string, string, string) (deployment.DeliveryBuildAttempt, error)
	PublishCandidate(context.Context, string, string, string, string) (deployment.DeliveryPublication, error)
	RollbackGeneration(context.Context, string, string, string, string) (deployment.DeliveryPublication, error)
}

// RefreshFencedDeliveryMutationPort binds canonical refresh publication to the
// exact worker lease that built the candidate. The delivery repository checks
// this authority in the same transaction as its target CAS.
type RefreshFencedDeliveryMutationPort interface {
	PublishCandidateFenced(context.Context, string, string, string, string, deployment.RefreshPublicationFence) (deployment.DeliveryPublication, error)
}

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
	if m == nil || (m.deliveryReader == nil && m.nativeDeliveryReader == nil) {
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
	if _, ok := m.principal(r); !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return false
	}
	if m == nil || (m.deliveryMutations == nil && m.nativeDeliveryMutations == nil) {
		m.writeDeliveryMutationError(w, r, ErrDeliveryInputUnavailable)
		return false
	}
	if m.handlerEnvironment() == "" {
		m.writeDeliveryReadError(w, r, errors.New("instance environment is required"))
		return false
	}
	return true
}

// deliveryPublicationMutationReady is narrower than deliveryMutationReady:
// native plan/build handlers use the clean-slate port, while publication and
// rollback remain owned by the existing publication coordinator.
func (m *Module) deliveryPublicationMutationReady(w http.ResponseWriter, r *http.Request) bool {
	if !m.deliveryMutationReady(w, r) {
		return false
	}
	if m.deliveryMutations == nil {
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
	if m.nativeDeliveryMutations != nil {
		operation := intent.Operation
		if operation == "" {
			operation = deployment.DeliveryOperationCodeChange
		}
		nativeRequest := NativeDeliveryPlanRequest{
			ProjectID: intent.ProjectID, TargetID: intent.TargetID, Environment: intent.Environment,
			PrincipalID: intent.PrincipalID, SourceOwnerID: intent.SourceOwnerID, Operation: string(operation), SourceDigest: intent.SourceDigest,
			SourceAttestationDigest: intent.SourceAttestationDigest, IdempotencyKey: idempotencyKey,
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
		return
	}
	created, err := m.deliveryMutations.CreatePlan(r.Context(), intent, idempotencyKey)
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if err := m.completeDeliveryCommand(r.Context(), deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID(), "delivery.plan.created", func(ctx context.Context, reader deliveryEventReader) error {
		event, err := reader.DeliveryEventByRequest(ctx, created.TargetID, created.Digest, "plan_created", "plan", created.ID)
		if err != nil {
			return fmt.Errorf("createDeliveryPlan durable evidence is unavailable: %w", err)
		}
		return acceptedDeliveryEvent(event, "createDeliveryPlan")
	}); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/plans/%s", project, created.ID))
	apitransport.WriteJSON(w, http.StatusCreated, planPreviewResponse(created))
}

func (m *Module) BuildDeliveryPlan(w http.ResponseWriter, r *http.Request, project, planID, idempotencyKey string) {
	if !m.deliveryMutationReady(w, r) {
		return
	}
	principal, _ := m.principal(r)
	if m.nativeDeliveryMutations != nil {
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
		built, err := m.nativeDeliveryMutations.BuildPlan(r.Context(), nativeRequest)
		if err != nil {
			m.writeDeliveryMutationError(w, r, err)
			return
		}
		if err := built.validate(nativeRequest); err != nil {
			m.writeDeliveryMutationError(w, r, err)
			return
		}
		if err := completeNativeBuildCommand(r.Context(), m.nativeDeliveryMutations, built); err != nil {
			m.writeDeliveryMutationError(w, r, err)
			return
		}
		response := nativeBuildStatusResponse(built)
		w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/builds/%s", project, built.ID.String()))
		apitransport.WriteJSON(w, http.StatusOK, response)
		return
	}
	attempt, err := m.deliveryMutations.BuildPlan(r.Context(), project, planID, principal.ID, idempotencyKey)
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	targetID := ""
	if m.deliveryReader != nil {
		plan, planErr := m.deliveryReader.PlanByID(r.Context(), attempt.PlanID)
		if planErr != nil || plan.ProjectID.String() != project || plan.Environment != m.handlerEnvironment() {
			m.writeDeliveryReadError(w, r, sql.ErrNoRows)
			return
		}
		targetID = plan.TargetID
	}
	if err := m.completeDeliveryCommand(r.Context(), deploymentgen.GenCommandOperationBuildDeliveryPlan().APIGenOperationID(), "delivery.build.sealed", func(ctx context.Context, reader deliveryEventReader) error {
		return acceptedBuildEvidence(ctx, reader, targetID, attempt)
	}); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	response := deploymentgen.DeliveryBuildStatusResponse{Id: attempt.ID, PlanId: attempt.PlanID, PlanDigest: attempt.PlanDigest, SourceDigest: attempt.SourceDigest, ExecutionDigest: attempt.ExecutionDigest, BaseGenerationId: optionalText(attempt.BaseGenerationID), BaseCatalogDigest: optionalText(attempt.BaseCatalogDigest), BasePhysicalPoolId: optionalText(attempt.BasePhysicalPoolID), PhysicalPoolId: attempt.PhysicalPoolID, WriterLeaseId: attempt.WriterLeaseID, Status: deploymentgen.DeliveryBuildStatus(attempt.Status), SealId: optionalText(attempt.SealID), CandidateId: optionalText(attempt.CandidateID), FailureCode: optionalText(attempt.FailureCode), Revision: attempt.Revision, CreatedAt: isoTime(attempt.CreatedAt), UpdatedAt: isoTime(attempt.UpdatedAt), TerminalAt: optionalText(isoTime(attempt.TerminalAt))}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/builds/%s", project, attempt.ID))
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) PublishDeliveryCandidate(w http.ResponseWriter, r *http.Request, project, candidateID, idempotencyKey string) {
	if !m.deliveryPublicationMutationReady(w, r) {
		return
	}
	principal, _ := m.principal(r)
	publication, err := m.deliveryMutations.PublishCandidate(r.Context(), project, candidateID, principal.ID, idempotencyKey)
	if err != nil && publication.ID != "" && errors.Is(err, deployment.ErrApprovalRequired) {
		// Canonical protected publication has already persisted its exact
		// pending identity. Request approval against that publication (not a
		// legacy project_deployments row) so an independent approver can decide
		// the same candidate/plan/release tuple before the publisher retries.
		if m.approvals == nil || m.deliveryReader == nil {
			m.writeDeliveryMutationError(w, r, err)
			return
		}
		candidate, candidateErr := m.deliveryReader.DeliveryCandidateByID(r.Context(), candidateID)
		if candidateErr != nil || candidate.ProjectID.String() != project || candidate.ServingArtifactID == "" {
			if candidateErr == nil {
				candidateErr = fmt.Errorf("delivery approval candidate scope is invalid")
			}
			m.writeDeliveryMutationError(w, r, candidateErr)
			return
		}
		actor, actorOK := m.approvalActor(r, principal.ID)
		if !actorOK {
			m.writeDeliveryMutationError(w, r, err)
			return
		}
		if _, requestErr := m.approvals.Request(r.Context(), deployment.ApprovalRequest{ProjectID: project, DeploymentID: publication.ID, Environment: publication.Environment, RequestDigest: publication.RequestDigest, ReleaseID: candidate.ServingArtifactID, RequestedBy: actor}); requestErr != nil {
			m.writeDeliveryMutationError(w, r, requestErr)
			return
		}
		// The pending publication is the response identity; approval status is
		// read through the dedicated approval surface and never inferred here.
		err = nil
	}
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if publication.ProjectID.String() != project || publication.Environment != m.handlerEnvironment() {
		m.writeDeliveryReadError(w, r, sql.ErrNoRows)
		return
	}
	if err := m.completeDeliveryCommand(r.Context(), deploymentgen.GenCommandOperationPublishDeliveryCandidate().APIGenOperationID(), "delivery.publication.requested", func(ctx context.Context, reader deliveryEventReader) error {
		event, err := reader.DeliveryEventByRequest(ctx, publication.TargetID, publication.RequestDigest, "publish_requested", "publication", publication.ID)
		if err != nil {
			return fmt.Errorf("publishDeliveryCandidate durable evidence is unavailable: %w", err)
		}
		return acceptedDeliveryEvent(event, "publishDeliveryCandidate")
	}); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	response := publicationResponse(publication)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/publications/%s", project, publication.ID))
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

// canonicalPublicationScope resolves the durable publication and its exact
// ready candidate. Approval rows are keyed by the publication ID, but the
// candidate's serving-artifact identity is part of the approval scope and is
// therefore checked on every request/decision/read.
func (m *Module) canonicalPublicationScope(
	ctx context.Context,
	project,
	publicationID string,
) (deployment.DeliveryPublication, deployment.DeliveryCandidate, error) {
	if m == nil || m.deliveryReader == nil || m.approvals == nil {
		return deployment.DeliveryPublication{}, deployment.DeliveryCandidate{}, apigenfailure.New("approval_unavailable", "deployment approvals are unavailable")
	}
	publication, err := m.deliveryReader.DeliveryPublicationByID(ctx, publicationID)
	if err != nil {
		return deployment.DeliveryPublication{}, deployment.DeliveryCandidate{}, fmt.Errorf("%w: publication", deployment.ErrApprovalNotFound)
	}
	if publication.ProjectID.String() != project ||
		(m.handlerEnvironment() != "" && publication.Environment != m.handlerEnvironment()) {
		return deployment.DeliveryPublication{}, deployment.DeliveryCandidate{}, deployment.ErrApprovalScope
	}
	candidate, err := m.deliveryReader.DeliveryCandidateByID(ctx, publication.CandidateID)
	if err != nil || candidate.ProjectID != publication.ProjectID ||
		candidate.TargetID != publication.TargetID ||
		candidate.Environment != publication.Environment ||
		candidate.ServingArtifactID == "" {
		return deployment.DeliveryPublication{}, deployment.DeliveryCandidate{}, deployment.ErrApprovalScope
	}
	return publication, candidate, nil
}

func (m *Module) canonicalPublicationApproval(
	ctx context.Context,
	project,
	publicationID,
	approvalID string,
) (deployment.DeliveryPublication, deployment.DeliveryCandidate, deployment.Approval, error) {
	publication, candidate, err := m.canonicalPublicationScope(ctx, project, publicationID)
	if err != nil {
		return deployment.DeliveryPublication{}, deployment.DeliveryCandidate{}, deployment.Approval{}, err
	}
	approval, err := m.approvals.Current(ctx, publication.ID)
	if err != nil {
		return deployment.DeliveryPublication{}, deployment.DeliveryCandidate{}, deployment.Approval{}, err
	}
	if approval.ID != strings.TrimSpace(approvalID) ||
		approval.ProjectID != project ||
		approval.DeploymentID != publication.ID ||
		approval.Environment != publication.Environment ||
		approval.RequestDigest != publication.RequestDigest ||
		approval.ReleaseID != candidate.ServingArtifactID {
		return deployment.DeliveryPublication{}, deployment.DeliveryCandidate{}, deployment.Approval{}, deployment.ErrApprovalScope
	}
	return publication, candidate, approval, nil
}

func (m *Module) RequestDeliveryPublicationApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	publicationID string,
	_ string,
) {
	operationID := deploymentgen.GenCommandOperationRequestDeliveryPublicationApproval()
	principal, ok := m.principal(r)
	if !ok {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("approval_credential_required", "A bounded publication credential is required"))
		return
	}
	actor, ok := m.approvalActor(r, principal.ID)
	if !ok {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("approval_credential_required", "A bounded publication credential is required"))
		return
	}
	publication, candidate, err := m.canonicalPublicationScope(r.Context(), project, publicationID)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	approval, err := m.approvals.Request(r.Context(), deployment.ApprovalRequest{
		ProjectID: project, DeploymentID: publication.ID,
		Environment: publication.Environment, RequestDigest: publication.RequestDigest,
		ReleaseID: candidate.ServingArtifactID, RequestedBy: actor,
	})
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	if err := m.completeDeliveryApprovalCommand(r.Context(), operationID.APIGenOperationID(), "delivery.publication.approval_requested", publication, approval, "approval_requested"); err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	w.Header().Set("Location", approvalLocation(project, publication.ID, approval.ID))
	apitransport.WriteJSON(w, http.StatusCreated, approvalResponse(approval))
}

func (m *Module) GetDeliveryPublicationApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	publicationID,
	approvalID string,
) {
	if _, ok := m.principal(r); !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	_, _, approval, err := m.canonicalPublicationApproval(r.Context(), project, publicationID, approvalID)
	if err != nil {
		// Queries have no generated command failure surface; retain the
		// canonical approval status/error vocabulary used by command endpoints.
		writeAPIError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, approvalResponse(approval))
}

func (m *Module) transitionDeliveryPublicationApproval(
	w http.ResponseWriter,
	r *http.Request,
	project,
	publicationID,
	approvalID string,
	decision approvalDecision,
) {
	operationID := operationIDForDeliveryDecision(decision)
	var body deploymentapi.ApprovalDecisionRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	principal, ok := m.principal(r)
	if !ok {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("approval_credential_required", "A bounded approval credential is required"))
		return
	}
	actor, ok := m.approvalActor(r, principal.ID)
	if !ok {
		m.writeCommandFailure(w, r, operationID, apigenfailure.New("approval_credential_required", "A bounded approval credential is required"))
		return
	}
	publication, _, _, err := m.canonicalPublicationApproval(r.Context(), project, publicationID, approvalID)
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	if m.authorizeApproval != nil {
		if err := m.authorizeApproval(r.Context(), actor, project, publication.Environment); err != nil {
			m.writeCommandFailure(w, r, operationID, err)
			return
		}
	}
	transition := deployment.ApprovalTransition{ProjectID: project, DeploymentID: publication.ID, ApprovalID: approvalID, ExpectedRevision: body.ExpectedRevision, Actor: actor}
	var approval deployment.Approval
	switch decision {
	case approvalDecisionApprove:
		approval, err = m.approvals.Approve(r.Context(), transition)
	case approvalDecisionDeny:
		approval, err = m.approvals.Deny(r.Context(), transition)
	case approvalDecisionRevoke:
		approval, err = m.approvals.Revoke(r.Context(), transition)
	default:
		err = deployment.ErrApprovalInvalid
	}
	if err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	eventType := "delivery.publication.approval_revoked"
	eventKind := "approval_revoked"
	if decision == approvalDecisionApprove {
		eventType = "delivery.publication.approved"
		eventKind = "approval_granted"
	} else if decision == approvalDecisionDeny {
		eventType = "delivery.publication.denied"
		eventKind = "approval_rejected"
	}
	if err := m.completeDeliveryApprovalCommand(r.Context(), operationID.APIGenOperationID(), eventType, publication, approval, eventKind); err != nil {
		m.writeCommandFailure(w, r, operationID, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, approvalResponse(approval))
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

func (m *Module) ApproveDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publicationID, approvalID, _ string) {
	m.transitionDeliveryPublicationApproval(w, r, project, publicationID, approvalID, approvalDecisionApprove)
}

func (m *Module) DenyDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publicationID, approvalID, _ string) {
	m.transitionDeliveryPublicationApproval(w, r, project, publicationID, approvalID, approvalDecisionDeny)
}

func (m *Module) RevokeDeliveryPublicationApproval(w http.ResponseWriter, r *http.Request, project, publicationID, approvalID, _ string) {
	m.transitionDeliveryPublicationApproval(w, r, project, publicationID, approvalID, approvalDecisionRevoke)
}

func (m *Module) RollbackDeliveryGeneration(w http.ResponseWriter, r *http.Request, project, generationID, idempotencyKey string) {
	if !m.deliveryPublicationMutationReady(w, r) {
		return
	}
	principal, _ := m.principal(r)
	publication, err := m.deliveryMutations.RollbackGeneration(r.Context(), project, generationID, principal.ID, idempotencyKey)
	if err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	if publication.ProjectID.String() != project || publication.Environment != m.handlerEnvironment() {
		m.writeDeliveryMutationError(w, r, sql.ErrNoRows)
		return
	}
	if err := m.completeDeliveryCommand(r.Context(), deploymentgen.GenCommandOperationRollbackDeliveryGeneration().APIGenOperationID(), "delivery.rollback.requested", func(ctx context.Context, reader deliveryEventReader) error {
		event, err := reader.DeliveryEventByRequest(ctx, publication.TargetID, publication.RequestDigest, "rollback_requested", "rollback", publication.ID)
		if err != nil {
			return fmt.Errorf("rollbackDeliveryGeneration durable evidence is unavailable: %w", err)
		}
		return acceptedDeliveryEvent(event, "rollbackDeliveryGeneration")
	}); err != nil {
		m.writeDeliveryMutationError(w, r, err)
		return
	}
	response := publicationResponse(publication)
	w.Header().Set("Location", fmt.Sprintf("/api/v1/projects/%s/delivery/publications/%s", project, publication.ID))
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func publicationResponse(publication deployment.DeliveryPublication) deploymentgen.DeliveryPublicationEvidenceResponse {
	return deploymentgen.DeliveryPublicationEvidenceResponse{Id: publication.ID, RequestDigest: publication.RequestDigest, TargetId: publication.TargetID, ProjectId: publication.ProjectID.String(), Environment: publication.Environment, PlanId: publication.PlanID, PlanDigest: publication.PlanDigest, CandidateId: publication.CandidateID, GenerationId: publication.GenerationID, ExpectedBaseGenerationId: optionalText(publication.ExpectedBaseGenerationID), ExpectedTargetRevision: publication.ExpectedTargetRevision, ResultTargetRevision: publication.ResultTargetRevision, Status: deploymentgen.DeliveryPublicationStatus(publication.Status), Reason: optionalText(publication.Reason), CreatedAt: isoTime(publication.CreatedAt), CompletedAt: optionalText(isoTime(publication.CompletedAt))}
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
	if m.nativeDeliveryReader != nil {
		plan, err := nativeReadPlan(r.Context(), m.nativeDeliveryReader, planID)
		if err == nil {
			err = validateNativeReadScope(m, project, plan)
		}
		if err != nil {
			m.writeDeliveryReadError(w, r, err)
			return
		}
		apitransport.WriteJSON(w, http.StatusOK, nativePlanResponse(plan))
		return
	}
	plan, err := m.deliveryReader.PlanByID(r.Context(), planID)
	if err != nil || plan.ProjectID.String() != project || (m.handlerEnvironment() != "" && plan.Environment != m.handlerEnvironment()) {
		if err == nil {
			err = sql.ErrNoRows
		}
		m.writeDeliveryReadError(w, r, err)
		return
	}
	evidence := deployment.RedactedDeliveryPlanEvidence(plan)
	evidenceView := deliveryPlanEvidenceView(evidence)
	response := deploymentgen.DeliveryPlanPreviewResponse{
		Id: plan.ID, ProjectId: project, TargetId: plan.TargetID, Environment: plan.Environment, Operation: deploymentgen.DeliveryOperationKind(plan.Operation),
		SourceDigest: plan.SourceDigest, SourceAttestationDigest: plan.Provenance.AttestationDigest, BaseTargetRevision: plan.BaseTargetRevision, ExecutionDigest: plan.ExecutionDigest,
		ProvenanceDigest: plan.ProvenanceDigest, GovernanceDigest: plan.GovernanceDigest, EvidenceDigest: plan.EvidenceDigest,
		PlanDigest: plan.Digest, Status: deploymentgen.DeliveryPlanStatus(plan.Status), ExpiresAt: isoTime(plan.Governance.ExpiresAt), CreatedAt: isoTime(plan.CreatedAt), Evidence: evidenceView,
	}
	response.BaseGenerationId = optionalText(plan.BaseGenerationID)
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) GetDeliveryBuildStatus(w http.ResponseWriter, r *http.Request, project, buildID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	if m.nativeDeliveryReader != nil {
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
		seal := nativepostgres.SnapshotSeal{}
		if attempt.CandidateID != "" {
			candidate, candidateErr := m.nativeDeliveryReader.Candidate(r.Context(), attempt.CandidateID)
			if candidateErr != nil {
				m.writeDeliveryReadError(w, r, nativeReadError(candidateErr))
				return
			}
			if candidate.SnapshotSealID != "" {
				seal, candidateErr = m.nativeDeliveryReader.SnapshotSeal(r.Context(), candidate.SnapshotSealID)
				if candidateErr != nil {
					m.writeDeliveryReadError(w, r, nativeReadError(candidateErr))
					return
				}
			}
		}
		apitransport.WriteJSON(w, http.StatusOK, nativeBuildResponse(attempt, plan, seal))
		return
	}
	attempt, err := m.deliveryReader.DeliveryBuildAttemptByID(r.Context(), buildID)
	if err == nil {
		plan, planErr := m.deliveryReader.PlanByID(r.Context(), attempt.PlanID)
		if planErr != nil || plan.ProjectID.String() != project || (m.handlerEnvironment() != "" && plan.Environment != m.handlerEnvironment()) {
			err = sql.ErrNoRows
		}
	}
	if err != nil {
		m.writeDeliveryReadError(w, r, err)
		return
	}
	response := deploymentgen.DeliveryBuildStatusResponse{Id: attempt.ID, PlanId: attempt.PlanID, PlanDigest: attempt.PlanDigest, SourceDigest: attempt.SourceDigest, ExecutionDigest: attempt.ExecutionDigest, PhysicalPoolId: attempt.PhysicalPoolID, WriterLeaseId: attempt.WriterLeaseID, Status: deploymentgen.DeliveryBuildStatus(attempt.Status), Revision: attempt.Revision, CreatedAt: isoTime(attempt.CreatedAt), UpdatedAt: isoTime(attempt.UpdatedAt)}
	response.BaseGenerationId, response.BaseCatalogDigest, response.BasePhysicalPoolId = optionalText(attempt.BaseGenerationID), optionalText(attempt.BaseCatalogDigest), optionalText(attempt.BasePhysicalPoolID)
	response.SealId, response.CandidateId, response.FailureCode = optionalText(attempt.SealID), optionalText(attempt.CandidateID), optionalText(attempt.FailureCode)
	response.TerminalAt = optionalText(isoTime(attempt.TerminalAt))
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) GetDeliverySealStatus(w http.ResponseWriter, r *http.Request, project, sealID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	if m.nativeDeliveryReader != nil {
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
		return
	}
	seal, err := m.deliveryReader.DeliveryCatalogSealByID(r.Context(), sealID)
	if err == nil {
		plan, planErr := m.deliveryReader.PlanByID(r.Context(), seal.PlanID)
		if planErr != nil || plan.ProjectID.String() != project || (m.handlerEnvironment() != "" && plan.Environment != m.handlerEnvironment()) {
			err = sql.ErrNoRows
		}
	}
	if err != nil {
		m.writeDeliveryReadError(w, r, err)
		return
	}
	response := deploymentgen.DeliverySealStatusResponse{Id: seal.ID, AttemptId: seal.AttemptID, PlanId: seal.PlanID, PlanDigest: seal.PlanDigest, ExecutionDigest: seal.ExecutionDigest, PhysicalPoolId: seal.PhysicalPoolID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, ServingArtifactId: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ObjectSize: seal.ObjectSize, Status: deploymentgen.DeliverySealStatus(seal.Status), CreatedAt: isoTime(seal.CreatedAt)}
	response.ServingStateId = seal.ServingStateID
	response.BaseCatalogDigest, response.BasePhysicalPoolId = optionalText(seal.BaseCatalogDigest), optionalText(seal.BasePhysicalPoolID)
	response.ClosureDigest, response.QualificationDigest, response.FailureCode = optionalText(seal.ClosureDigest), optionalText(seal.QualificationDigest), optionalText(seal.FailureCode)
	response.VerifiedAt = optionalText(isoTime(seal.VerifiedAt))
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) GetDeliveryCandidateStatus(w http.ResponseWriter, r *http.Request, project, candidateID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	if m.nativeDeliveryReader != nil {
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
		apitransport.WriteJSON(w, http.StatusOK, nativeCandidateResponse(candidate, plan, seal))
		return
	}
	candidate, err := m.deliveryReader.DeliveryCandidateByID(r.Context(), candidateID)
	if err != nil || candidate.ProjectID.String() != project || (m.handlerEnvironment() != "" && candidate.Environment != m.handlerEnvironment()) {
		if err == nil {
			err = sql.ErrNoRows
		}
		m.writeDeliveryReadError(w, r, err)
		return
	}
	response := deploymentgen.DeliveryCandidateStatusResponse{Id: candidate.ID, PlanId: candidate.PlanID, PlanDigest: candidate.PlanDigest, TargetId: candidate.TargetID, ProjectId: project, Environment: candidate.Environment, SourceDigest: candidate.SourceDigest, ExecutionDigest: candidate.ExecutionDigest, BaseTargetRevision: candidate.BaseTargetRevision, SealId: candidate.SealID, CatalogDigest: candidate.CatalogDigest, CompatibilityDigest: candidate.CompatibilityDigest, PhysicalPoolId: candidate.PhysicalPoolID, ServingArtifactId: candidate.ServingArtifactID, ServingArtifactDigest: candidate.ServingArtifactDigest, Status: deploymentgen.DeliveryCandidateStatus(candidate.Status), ResolvedInputs: deliveryResolvedInputViews(candidate.ResolvedInputs), CreatedAt: isoTime(candidate.CreatedAt)}
	response.BaseGenerationId, response.BaseCatalogDigest, response.BasePhysicalPoolId, response.ServingStateId = optionalText(candidate.BaseGenerationID), optionalText(candidate.BaseCatalogDigest), optionalText(candidate.BasePhysicalPoolID), candidate.ServingStateID
	response.QualificationDigest, response.ResolvedInputsDigest, response.FailureCode = optionalText(candidate.QualificationDigest), optionalText(candidate.ResolvedInputs.EvidenceDigest), optionalText(candidate.FailureCode)
	response.ReadyAt, response.RetiredAt = optionalText(isoTime(candidate.ReadyAt)), optionalText(isoTime(candidate.RetiredAt))
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) GetDeliveryGenerationStatus(w http.ResponseWriter, r *http.Request, project, generationID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	if m.nativeDeliveryReader != nil {
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
		return
	}
	generation, err := m.deliveryReader.DeliveryGenerationByID(r.Context(), generationID)
	if err != nil || generation.ProjectID.String() != project || (m.handlerEnvironment() != "" && generation.Environment != m.handlerEnvironment()) {
		if err == nil {
			err = sql.ErrNoRows
		}
		m.writeDeliveryReadError(w, r, err)
		return
	}
	response := deploymentgen.DeliveryGenerationStatusResponse{Id: generation.ID, CandidateId: generation.CandidateID, PlanId: generation.PlanID, PlanDigest: generation.PlanDigest, TargetId: generation.TargetID, ProjectId: project, Environment: generation.Environment, CatalogDigest: generation.CatalogDigest, PhysicalPoolId: generation.PhysicalPoolID, ServingArtifactId: generation.ServingArtifactID, ServingArtifactDigest: generation.ServingArtifactDigest, RollbackClass: deploymentgen.DeliveryRollbackClass(generation.RollbackClass), Status: deploymentgen.DeliveryGenerationStatus(generation.Status), CreatedAt: isoTime(generation.CreatedAt)}
	response.ServingStateId, response.CompatibilityDigest = generation.ServingStateID, generation.CompatibilityDigest
	response.ActivatedAt, response.RetiredAt, response.RollbackUntil = optionalText(isoTime(generation.ActivatedAt)), optionalText(isoTime(generation.RetiredAt)), optionalText(isoTime(generation.RollbackUntil))
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) GetDeliveryPublicationEvidence(w http.ResponseWriter, r *http.Request, project, publicationID string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	if m.nativeDeliveryReader != nil {
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
		return
	}
	publication, err := m.deliveryReader.DeliveryPublicationByID(r.Context(), publicationID)
	if err != nil || publication.ProjectID.String() != project || (m.handlerEnvironment() != "" && publication.Environment != m.handlerEnvironment()) {
		if err == nil {
			err = sql.ErrNoRows
		}
		m.writeDeliveryReadError(w, r, err)
		return
	}
	response := deploymentgen.DeliveryPublicationEvidenceResponse{Id: publication.ID, RequestDigest: publication.RequestDigest, TargetId: publication.TargetID, ProjectId: project, Environment: publication.Environment, PlanId: publication.PlanID, PlanDigest: publication.PlanDigest, CandidateId: publication.CandidateID, GenerationId: publication.GenerationID, ExpectedTargetRevision: publication.ExpectedTargetRevision, ResultTargetRevision: publication.ResultTargetRevision, Status: deploymentgen.DeliveryPublicationStatus(publication.Status), CreatedAt: isoTime(publication.CreatedAt)}
	response.ExpectedBaseGenerationId, response.Reason, response.CompletedAt = optionalText(publication.ExpectedBaseGenerationID), optionalText(publication.Reason), optionalText(isoTime(publication.CompletedAt))
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) GetDeliveryOperatorSnapshot(w http.ResponseWriter, r *http.Request, project string) {
	if !m.deliveryReadReady(w, r, project) {
		return
	}
	if m.handlerEnvironment() == "" {
		m.writeDeliveryReadError(w, r, errors.New("instance environment is required for operator status"))
		return
	}
	if m.nativeDeliveryReader != nil {
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
		return
	}
	snapshot, err := m.deliveryReader.DeliveryOperatorSnapshot(r.Context(), project, m.handlerEnvironment())
	if err != nil {
		m.writeDeliveryReadError(w, r, err)
		return
	}
	response := deploymentgen.DeliveryOperatorSnapshotResponse{ProjectId: snapshot.ProjectID, Environment: snapshot.Environment, TargetId: snapshot.TargetID, TargetRevision: snapshot.TargetRevision, Degraded: snapshot.Degraded, DegradedReasons: append([]string(nil), snapshot.DegradedReasons...)}
	response.ActiveGeneration = optionalText(snapshot.ActiveGeneration)
	for _, item := range snapshot.PhysicalPools {
		response.PhysicalPools = append(response.PhysicalPools, deploymentgen.DeliveryPhysicalPoolAdmissionView{PoolId: item.PoolID, IdentityDigest: item.IdentityDigest, CompatibilityDigest: item.CompatibilityDigest, EvidenceDigest: item.EvidenceDigest, ConformanceVersion: item.ConformanceVersion, DuckdbRuntime: item.DuckDBRuntime, DucklakeExtension: item.DuckLakeExtension, CatalogFormat: item.CatalogFormat, StorageImplementation: item.StorageImplementation, ObjectNamingContract: item.ObjectNamingContract, AdmittedAt: isoTime(item.AdmittedAt)})
	}
	for _, item := range snapshot.Roots {
		v := deploymentgen.DeliveryRootView{PoolId: item.PoolID, Kind: deploymentgen.DeliveryRootKind(item.Kind), SourceId: item.SourceID, CatalogDigest: item.CatalogDigest, Status: deploymentgen.DeliveryRootStatus(item.Status), CreatedAt: isoTime(item.CreatedAt)}
		v.CandidateId, v.GenerationId, v.LeaseId = optionalText(item.CandidateID), optionalText(item.GenerationID), optionalText(item.LeaseID)
		v.ExpiresAt = optionalText(isoTime(item.ExpiresAt))
		response.Roots = append(response.Roots, v)
	}
	for _, item := range snapshot.QueryLeases {
		v := deploymentgen.DeliveryQueryLeaseView{Id: item.ID, HolderId: item.HolderID, PoolId: item.PoolID, CatalogDigest: item.CatalogDigest, Status: deploymentgen.DeliveryLeaseStatus(item.Status), CreatedAt: isoTime(item.CreatedAt), ExpiresAt: isoTime(item.ExpiresAt)}
		v.CandidateId, v.GenerationId = optionalText(item.CandidateID), optionalText(item.GenerationID)
		response.QueryLeases = append(response.QueryLeases, v)
	}
	for _, item := range snapshot.WriterLeases {
		v := deploymentgen.DeliveryWriterLeaseView{Id: item.ID, AttemptId: item.AttemptID, PoolId: item.PoolID, OwnerId: item.OwnerID, Epoch: item.Epoch, Status: deploymentgen.DeliveryLeaseStatus(item.Status), CreatedAt: isoTime(item.CreatedAt), ExpiresAt: isoTime(item.ExpiresAt)}
		v.ReleasedAt = optionalText(isoTime(item.ReleasedAt))
		response.WriterLeases = append(response.WriterLeases, v)
	}
	for _, item := range snapshot.GCCycles {
		v := deploymentgen.DeliveryGCCycleView{Id: item.ID, PoolId: item.PoolID, Epoch: item.Epoch, RootRevision: item.RootRevision, Status: deploymentgen.DeliveryGCStatus(item.Status), CreatedAt: isoTime(item.CreatedAt)}
		v.MarkDigest, v.CompletedAt, v.AbortReason = optionalText(item.MarkDigest), optionalText(isoTime(item.CompletedAt)), optionalText(item.AbortReason)
		response.GcCycles = append(response.GcCycles, v)
	}
	for _, item := range snapshot.GCDeleteIntents {
		v := deploymentgen.DeliveryGCDeleteIntentView{Id: item.ID, CycleId: item.CycleID, PoolId: item.PoolID, ObjectDigest: item.ObjectDigest, Status: deploymentgen.DeliveryGCDeleteStatus(item.Status), CreatedAt: isoTime(item.CreatedAt)}
		v.ObjectVersion, v.CompletedAt = optionalText(item.ObjectVersion), optionalText(isoTime(item.CompletedAt))
		response.GcDeleteIntents = append(response.GcDeleteIntents, v)
	}
	apitransport.WriteJSON(w, http.StatusOK, response)
}
