package module

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform/digest"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

const maxCandidateSourceBlobBytes = 16 << 20

func (m *Module) PlanProjectCandidateSynchronization(w http.ResponseWriter, r *http.Request, project, idempotencyKey string) {
	operationID := deploymentgen.GenCommandOperationPlanProjectCandidateSynchronization()
	request, ok := m.decodeCandidateSynchronizationRequest(w, r)
	if !ok {
		return
	}
	principalID, ok := m.candidateSynchronizationPrincipal(w, r, deploymentCommandOperation(operationID))
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
		return
	}
	request.IdempotencyKey = strings.TrimSpace(idempotencyKey)
	// Planning is the first CLI side effect: claim the exact project and
	// environment before reporting missing blobs. The durable singleton claim
	// is idempotent for the same project and fails closed for a race with a
	// different project.
	if m.candidates != nil {
		if err := m.candidates.ClaimProject(r.Context(), projectID, principalID); err != nil {
			m.writeCandidateCommandFailure(w, r, operationID, err)
			return
		}
	}
	if !m.validateExpectedCandidate(w, r, project, principalID, request, deploymentCommandOperation(operationID)) {
		return
	}
	plan, err := m.candidateSources.Plan(r.Context(), deployment.CandidateSourceScope{
		ProjectID: projectID, OwnerID: principalID, CandidateKey: request.CandidateKey,
	}, request)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
		return
	}
	plan.PlanID = strings.TrimSpace(plan.PlanID)
	if plan.PlanID == "" {
		m.writeCandidateCommandFailure(w, r, operationID, apigenfailure.New("candidate_conflict", "candidate source synchronization plan did not return an identity"))
		return
	}
	if plan.ArtifactDigest != request.ArtifactDigest {
		m.writeCandidateCommandFailure(w, r, operationID, apigenfailure.New("candidate_conflict", "candidate source synchronization plan is bound to a different source digest"))
		return
	}
	if err := m.executeCandidateSourcePlanAudit(r, principalID, project, plan.ArtifactDigest, plan.PlanID); err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, apigenfailure.New("audit_unavailable", "Candidate source synchronization audit is temporarily unavailable"))
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, deploymentapi.CandidateSynchronizationPlanResponse{
		PlanID: plan.PlanID, ArtifactDigest: plan.ArtifactDigest, MissingDigests: plan.MissingDigests,
	})
}

// RetainProjectCandidateSource stores an exact immutable source snapshot for
// delivery planning. It deliberately does not create a candidate, acquire a
// preparation lease, invoke DeliveryCandidateBuilder, or touch physical
// credentials/catalog state. BuildDeliveryPlan is the first writer boundary.
func (m *Module) RetainProjectCandidateSource(w http.ResponseWriter, r *http.Request, project, _, sourceSynchronizationPlan string) {
	request, ok := m.decodeCandidateSynchronizationRequest(w, r)
	if !ok {
		return
	}
	operationID := deploymentgen.GenCommandOperationRetainProjectCandidateSource()
	principalID, ok := m.candidateSynchronizationPrincipal(w, r, deploymentCommandOperation(operationID))
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
		return
	}
	if request.ExpectedCandidateID != "" || request.ExpectedArtifactDigest != "" {
		m.writeCandidateCommandFailure(w, r, operationID, apigenfailure.New("candidate_invalid", "source retention does not accept candidate concurrency inputs"))
		return
	}
	request.SourceOnly = true
	request.PlanID = strings.TrimSpace(sourceSynchronizationPlan)
	source, err := m.candidateSources.Commit(r.Context(), deployment.CandidateSourceScope{ProjectID: projectID, OwnerID: principalID, CandidateKey: request.CandidateKey}, request)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
		return
	}
	// Source retention is a generated command even though it does not create a
	// candidate row. Complete the command through the APIGen executor before
	// writing the successful response so the generated transport guard can
	// observe the best-effort audit completion.
	if err := m.executeCandidateSourceAudit(r, operationID.APIGenOperationID(), principalID, project, source.ArtifactDigest, source.SourceAttestationDigest, 0); err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, apigenfailure.New("audit_unavailable", "Candidate source audit is temporarily unavailable"))
		return
	}
	apitransport.WriteJSON(w, http.StatusCreated, deploymentgen.CandidateSourceSnapshotResponse{
		ProjectId: project, SourceDigest: source.ArtifactDigest, ProjectDigest: source.ProjectDigest,
		SourceAttestationDigest: source.SourceAttestationDigest,
		TargetId:                m.instanceID, Environment: m.handlerEnvironment(),
	})
}

func (m *Module) UploadProjectCandidateSourceBlob(
	w http.ResponseWriter,
	r *http.Request,
	project, identity, contentType, contentDigest, sourceSynchronizationPlan string,
) {
	operationID := deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob()
	principalID, ok := m.candidateSynchronizationPrincipal(w, r, deploymentCommandOperation(operationID))
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
		return
	}
	if contentType != "application/octet-stream" ||
		identity != strings.TrimSpace(identity) || contentDigest != strings.TrimSpace(contentDigest) ||
		digest.ValidateSHA256Identity(identity) != nil ||
		contentDigest != candidateSourceContentDigest(identity) {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob(), apigenfailure.New("source_blob_invalid", "Candidate source blob headers do not match the canonical content identity"))
		return
	}
	if strings.TrimSpace(sourceSynchronizationPlan) == "" {
		m.writeCandidateCommandFailure(w, r, operationID, apigenfailure.New("candidate_invalid", "Source-Synchronization-Plan header is required"))
		return
	}
	counter := &candidateSourceCountingReader{source: http.MaxBytesReader(
		w, r.Body, maxCandidateSourceBlobBytes,
	)}
	if err := m.candidateSources.Upload(r.Context(), deployment.CandidateSourceScope{
		ProjectID: projectID, OwnerID: principalID,
	}, sourceSynchronizationPlan, identity, counter); err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob(), err)
		return
	}
	if err := m.executeCandidateSourceAudit(r, operationID.APIGenOperationID(), principalID, project, identity, "", counter.read); err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob(), apigenfailure.New("audit_unavailable", "Candidate source blob audit is temporarily unavailable"))
		return
	}
	w.Header().Set("Location", "/api/v1/projects/"+url.PathEscape(strings.TrimSpace(project))+
		"/candidate-sync/blobs/"+url.PathEscape(identity))
	apitransport.WriteJSON(w, http.StatusCreated, deploymentapi.CandidateSourceBlobResponse{
		Digest: identity, SizeBytes: counter.read,
	})
}

func (m *Module) executeCandidateSourceAudit(
	r *http.Request,
	operationID, principalID, projectID, sourceDigest, sourceAttestationDigest string,
	sizeBytes int64,
) error {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		return err
	}
	logMessage := "candidate source audit failed"
	if operationID == string(deploymentgen.GenOperationUploadProjectCandidateSourceBlob) {
		logMessage = "candidate source blob audit failed"
	}
	return executor.Execute(r.Context(), operationID, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			return m.recordCandidateSourceAudit(ctx, r, contract, principalID, projectID, sourceDigest, sourceAttestationDigest, sizeBytes)
		},
		LogMessage: logMessage,
		LogAttributes: []slog.Attr{
			slog.String("project_id", strings.TrimSpace(projectID)),
			slog.String("digest", sourceDigest),
		},
	})
}

func (m *Module) executeCandidateSourcePlanAudit(r *http.Request, principalID, projectID, sourceDigest, planID string) error {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), string(deploymentgen.GenOperationPlanProjectCandidateSynchronization), apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			return m.recordCandidateSourcePlanAudit(ctx, r, contract, principalID, projectID, sourceDigest, planID)
		},
		LogMessage: "candidate source synchronization plan audit failed",
	})
}

func (m *Module) recordCandidateSourceAudit(
	ctx context.Context,
	r *http.Request,
	contract apigencommand.Contract,
	principalID, projectID, sourceDigest, sourceAttestationDigest string,
	sizeBytes int64,
) error {
	if m == nil {
		return errors.New("required candidate source audit sink is unavailable")
	}
	if contract.OperationID == "" || contract.AuditPayload == nil {
		return errors.New("required candidate source command audit contract is unavailable")
	}
	auditSink := m.candidateSourceAudit
	if contract.OperationID == string(deploymentgen.GenOperationUploadProjectCandidateSourceBlob) && m.candidateSourceBlobAudit != nil {
		auditSink = m.candidateSourceBlobAudit
	} else if auditSink == nil {
		auditSink = m.candidateSourceBlobAudit
	}
	if auditSink == nil {
		return errors.New("required candidate source audit sink is unavailable")
	}
	parsedProjectID, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return fmt.Errorf("candidate source project ID: %w", err)
	}
	capability, err := access.ParseCapability(contract.Privilege)
	if err != nil {
		return fmt.Errorf("candidate source capability: %w", err)
	}
	surface := "api"
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Invocation-Surface")), "cli") ||
		strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Client")), "cli") {
		surface = "cli"
	}
	var metadata string
	switch contract.OperationID {
	case string(deploymentgen.GenOperationRetainProjectCandidateSource):
		metadata, err = deploymentgen.EncodeGenRetainProjectCandidateSourceAuditPayload(deploymentgen.GenSchemaCandidateSourceRetainedAuditPayload{
			OperationId: contract.OperationID, Surface: surface, ProjectId: parsedProjectID.String(),
			SourceDigest: sourceDigest, SourceAttestationDigest: sourceAttestationDigest,
		})
	case string(deploymentgen.GenOperationUploadProjectCandidateSourceBlob):
		metadata, err = deploymentgen.EncodeGenUploadProjectCandidateSourceBlobAuditPayload(deploymentgen.GenSchemaCandidateSourceBlobAuditPayload{
			OperationId: contract.OperationID, Surface: surface, Digest: sourceDigest, SizeBytes: sizeBytes,
		})
	default:
		return fmt.Errorf("unsupported candidate source audit operation %q", contract.OperationID)
	}
	if err != nil {
		return fmt.Errorf("encode candidate source audit metadata: %w", err)
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	return auditSink(ctx, CandidateSourceAuditEvent{
		PrincipalID: principalID, ProjectID: parsedProjectID,
		Digest: sourceDigest, SourceAttestationDigest: sourceAttestationDigest,
		Action: contract.AuditAction, Capability: capability, Status: "success",
		RequestID: requestID, CorrelationID: correlationID, MetadataJSON: metadata,
	})
}

func (m *Module) recordCandidateSourcePlanAudit(ctx context.Context, r *http.Request, contract apigencommand.Contract, principalID, projectID, sourceDigest, planID string) error {
	if m == nil || (m.candidateSourceAudit == nil && m.candidateSourceBlobAudit == nil) {
		return errors.New("required candidate source audit sink is unavailable")
	}
	parsedProjectID, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return err
	}
	capability, err := access.ParseCapability(contract.Privilege)
	if err != nil {
		return err
	}
	surface := "api"
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Invocation-Surface")), "cli") || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Client")), "cli") {
		surface = "cli"
	}
	metadata, err := deploymentgen.EncodeGenPlanProjectCandidateSynchronizationAuditPayload(deploymentgen.GenSchemaCandidateSourceSyncPlannedAuditPayload{
		OperationId: contract.OperationID, Surface: surface, ProjectId: parsedProjectID.String(), SourceDigest: sourceDigest, PlanId: planID,
	})
	if err != nil {
		return err
	}
	auditSink := m.candidateSourceAudit
	if auditSink == nil {
		auditSink = m.candidateSourceBlobAudit
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	return auditSink(ctx, CandidateSourceAuditEvent{PrincipalID: principalID, ProjectID: parsedProjectID, Digest: sourceDigest, Action: contract.AuditAction, Capability: capability, Status: "success", RequestID: requestID, CorrelationID: correlationID, MetadataJSON: metadata})
}

func (m *Module) recordCandidateSourceBlobAudit(
	r *http.Request,
	principalID, projectID, identity string,
	sizeBytes int64,
) error {
	contract, ok := deploymentgen.GetAPIGenCommandRuntimeContract(deploymentgen.GenOperationUploadProjectCandidateSourceBlob)
	if !ok {
		return errors.New("required candidate source blob command audit contract is unavailable")
	}
	return m.recordCandidateSourceAudit(r.Context(), r, contract, principalID, projectID, identity, "", sizeBytes)
}

func (m *Module) CommitProjectCandidateSynchronization(
	w http.ResponseWriter,
	r *http.Request,
	project, _, sourceSynchronizationPlan string,
) {
	request, ok := m.decodeCandidateSynchronizationRequest(w, r)
	if !ok {
		return
	}
	operationID := deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization()
	principalID, ok := m.candidateSynchronizationPrincipal(w, r, deploymentCommandOperation(operationID))
	if !ok {
		return
	}
	// This endpoint is the legacy candidate-producing commit path. Native
	// PostgreSQL source planning, upload, and retention do not require the
	// legacy candidate service, but this operation still does and must fail
	// before committing the source snapshot when it is absent.
	if m.candidates == nil {
		m.writeCandidateCommandFailure(w, r, operationID, deployment.ErrCandidateUnavailable)
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
		return
	}
	request.PlanID = strings.TrimSpace(sourceSynchronizationPlan)
	if !m.validateExpectedCandidate(w, r, project, principalID, request, deploymentCommandOperation(operationID)) {
		return
	}
	scope := deployment.CandidateSourceScope{ProjectID: projectID, OwnerID: principalID}
	scope.CandidateKey = request.CandidateKey
	source, err := m.candidateSources.Commit(r.Context(), scope, request)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
		return
	}
	if request.SourceOnly {
		// Source-only retention has a dedicated endpoint. Rejecting it here
		// prevents the legacy commit route from creating a candidate row.
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), apigenfailure.New("candidate_invalid", "sourceOnly requires the source retention operation"))
		return
	}
	var candidate deployment.Candidate
	if request.ExpectedCandidateID == "" {
		var started deployment.CandidateStartResult
		started, err = m.candidates.Start(r.Context(), deployment.StartCandidateRequest{
			ProjectID: projectID, OwnerID: principalID, ArtifactDigest: request.ArtifactDigest,
			Key: request.CandidateKey,
		})
		candidate = started.Candidate
		if err == nil && candidate.Status == deployment.CandidateFailed {
			candidate, err = m.candidates.Retry(r.Context(), candidateScope(candidate))
		}
	} else {
		candidate, err = m.candidates.Get(r.Context(), deployment.CandidateAccessScope{
			ProjectID: projectID, CandidateID: request.ExpectedCandidateID, OwnerID: principalID,
		})
	}
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
		return
	}
	requestedSourceRevision, err := candidateSourceRevisionProvenance(
		request.SourceRevision,
	)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), candidatePreparationError(err))
		return
	}
	if candidate.Status == deployment.CandidateReady &&
		candidate.ArtifactDigest == request.ArtifactDigest {
		provenance, verifyErr := m.verifiedCandidateProvenance(
			r.Context(),
			candidate,
		)
		if verifyErr != nil {
			m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), candidatePreparationError(verifyErr))
			return
		}
		if equalCandidateSourceRevision(
			provenance.SourceRevision,
			requestedSourceRevision,
		) {
			// MarkReady is intentionally idempotent for an exact ready replay;
			// invoking it here completes the generated candidate.ready command
			// audit while preserving the candidate revision and timestamps.
			candidate, err = m.candidates.MarkReady(
				r.Context(), candidateScope(candidate), request.ArtifactDigest, candidate.ProvenanceDigest,
			)
			if err != nil {
				m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
				return
			}
			apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
			return
		}
	}
	replaceCandidate := candidate.Status == deployment.CandidateReady
	currentArtifactDigest := candidate.ArtifactDigest
	tentative, err := tentativeCandidate(candidate, request)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
		return
	}
	preparationContext := r.Context()
	var preparationLease CandidatePreparationLease
	if m.candidateAdmission != nil {
		preparationLease, err = m.candidateAdmission.AcquireCandidatePreparation(
			preparationContext,
		)
		if err != nil {
			m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), candidatePreparationError(err))
			return
		}
		defer preparationLease.Release()
		preparationContext = preparationLease.Context()
	}
	// Production composition may provide the canonical plan -> build -> seal
	// adapter. It receives the exact immutable source snapshot and the durable
	// preparing candidate, so no second source capture or moving worktree is
	// possible. The adapter owns target fencing and ready persistence.
	if m.deliveryCandidateBuilder != nil {
		ready, buildErr := m.deliveryCandidateBuilder(preparationContext, deployment.DeliveryCandidateBuildInput{
			ProjectID: projectID, OwnerID: principalID, ArtifactDigest: request.ArtifactDigest, CandidateKey: request.CandidateKey,
			Candidate: tentative, Source: source,
		})
		if buildErr != nil {
			if request.ExpectedCandidateID == "" {
				_, _ = m.candidates.MarkFailed(r.Context(), candidateScope(candidate), candidate.ArtifactDigest, "CANONICAL_DELIVERY_UNAVAILABLE")
			}
			m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), buildErr)
			return
		}
		// The canonical adapter durably completes the delivery candidate/seal
		// first, then returns the retained provenance digest as a projection
		// update for the legacy candidate API. Persist that projection only after
		// the physical completion has succeeded.
		if ready.Status == deployment.CandidateReady {
			if ready.ProvenanceDigest == "" {
				m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), fmt.Errorf("canonical delivery returned ready candidate without provenance"))
				return
			}
			if replaceCandidate {
				candidate, err = m.candidates.ReplaceArtifact(r.Context(), candidateScope(candidate), currentArtifactDigest, request.ArtifactDigest)
				if err != nil {
					m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
					return
				}
			}
			ready, err = m.candidates.MarkReady(r.Context(), candidateScope(candidate), request.ArtifactDigest, ready.ProvenanceDigest)
			if err != nil {
				m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
				return
			}
		}
		apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(ready, false))
		return
	}
	provenance, err := m.prepareCandidate(preparationContext, tentative, source)
	if err != nil {
		m.candidateLogger().Error(
			"candidate preparation failed",
			"candidate_id", candidate.ID,
			"project_id", candidate.Scope.ProjectID.String(),
			"error", err,
		)
		if request.ExpectedCandidateID == "" {
			_, _ = m.candidates.MarkFailed(
				r.Context(),
				candidateScope(candidate),
				candidate.ArtifactDigest,
				"CANDIDATE_PREPARATION_FAILED",
			)
		}
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), candidatePreparationError(err))
		return
	}
	if replaceCandidate {
		candidate, err = m.candidates.ReplaceArtifact(
			r.Context(),
			candidateScope(candidate),
			currentArtifactDigest,
			request.ArtifactDigest,
		)
		if err != nil {
			m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
			return
		}
	}
	candidate, err = m.candidates.MarkReady(
		r.Context(),
		candidateScope(candidate),
		request.ArtifactDigest,
		provenance.Digest,
	)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
}

func (m *Module) candidateLogger() *slog.Logger {
	if m != nil && m.logger != nil {
		return m.logger
	}
	return slog.Default()
}

func (m *Module) prepareCandidate(
	ctx context.Context,
	candidate deployment.Candidate,
	source project.CandidateSourceSnapshot,
) (release.Provenance, error) {
	if m == nil || m.candidateArtifacts == nil || m.candidateRuntimes == nil {
		return release.Provenance{}, deployment.ErrCandidateUnavailable
	}
	artifacts, err := m.candidateArtifacts.PrepareCandidateArtifacts(
		ctx,
		release.CandidateArtifactRequest{
			CandidateID: candidate.ID, Scope: candidate.Scope,
			OwnerID:        candidate.OwnerID,
			ArtifactDigest: candidate.ArtifactDigest, Source: source,
		},
	)
	if err != nil {
		return release.Provenance{}, err
	}
	generation := artifacts.Generation
	identity := generation.Identity
	if candidate.Scope.BaseGenerationID != "" {
		if candidate.Scope.ProjectID != identity.ProjectID || candidate.Scope.Environment != identity.Environment {
			return release.Provenance{}, release.ErrProvenanceInvalid
		}
		_, err = candidate.Scope.BaseIdentity()
		if err != nil {
			return release.Provenance{}, err
		}
	}
	receipt, err := m.candidateRuntimes.Prepare(ctx, deployment.CandidateRuntimeRequest{
		Candidate: candidate, AuthorizationFingerprint: artifacts.AuthorizationFingerprint,
		Generation: deployment.CandidateGenerationRuntime{
			Identity: identity, ArtifactDigest: generation.ArtifactDigest,
			DataRevision: generation.DataRevision, DataMode: deployment.CandidateDataMode(generation.DataMode),
			Connections:            candidateConnectionRequirements(generation.Connections),
			AuthoredConnections:    candidateAuthoredConnections(generation.AuthoredConnections),
			ManagedDataConnections: candidateManagedDataConnections(generation.ManagedDataPins),
			Extensions:             append([]extension.Evidence(nil), artifacts.Extensions...),
		},
	})
	if err != nil {
		return release.Provenance{}, err
	}
	provenance, err := candidateReleaseProvenance(
		candidate,
		artifacts,
		receipt,
		source.SourceRevision,
	)
	if err != nil {
		return release.Provenance{}, err
	}
	retained, err := m.candidateArtifacts.RetainCandidateProvenance(
		ctx,
		candidate.Scope.ProjectID,
		provenance,
	)
	if err != nil {
		return release.Provenance{}, err
	}
	if retained.Digest != provenance.Digest {
		return release.Provenance{}, release.ErrConflict
	}
	return retained, nil
}

func candidateAuthoredConnections(
	values []release.CandidateAuthoredConnection,
) []deployment.CandidateAuthoredConnection {
	result := make([]deployment.CandidateAuthoredConnection, len(values))
	for index, value := range values {
		result[index] = deployment.CandidateAuthoredConnection{
			ConnectionID:  value.ConnectionID,
			ConnectorKind: value.ConnectorKind,
			Access:        value.Access,
		}
	}
	return result
}

func candidateConnectionRequirements(
	values []release.CandidateConnectionRequirement,
) []deployment.CandidateConnectionRequirement {
	result := make([]deployment.CandidateConnectionRequirement, len(values))
	for index, value := range values {
		result[index] = deployment.CandidateConnectionRequirement{ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind, Access: value.Access}
	}
	return result
}

func candidateManagedDataConnections(
	pins []release.ManagedDataPin,
) []string {
	result := make([]string, len(pins))
	for index, pin := range pins {
		result[index] = pin.ConnectionID
	}
	return result
}

func candidateReleaseProvenance(
	candidate deployment.Candidate,
	artifacts release.CandidateArtifactSet,
	receipt deployment.CandidateRuntimeReceipt,
	sourceRevision *project.CandidateSourceRevision,
) (release.Provenance, error) {
	if artifacts.Artifact.SourceDigest != candidate.ArtifactDigest {
		return release.Provenance{}, release.ErrProvenanceInvalid
	}
	bindings := make([]release.BindingEvidence, len(receipt.Bindings))
	for index, item := range receipt.Bindings {
		bindings[index] = release.BindingEvidence{BindingID: item.BindingID, ConnectionID: item.ConnectionID.String(), ConnectorKind: item.ConnectorKind, Revision: item.Revision, ValidatedVersion: item.ProviderVersion, EndpointConfigHash: item.EndpointConfigHash, Access: item.Access}
	}
	identity := artifacts.Generation.Identity
	var baseIdentity *projectgraph.ServingIdentity
	if candidate.Scope.BaseGenerationID != "" {
		baseIdentity, _ = candidate.Scope.BaseIdentity()
	}
	return release.NewProvenance(release.ProvenanceInput{
		Artifact: artifacts.Artifact,
		Candidate: release.CandidateProvenance{
			ID:       candidate.ID,
			Revision: candidate.Revision + 1,
			OwnerID:  candidate.OwnerID,
		},
		SourceRevision: candidateSourceRevision(sourceRevision),
		Plan: release.GenerationPlanProvenance{
			Identity: identity, BaseIdentity: baseIdentity, TargetID: candidate.TargetID,
			RuntimeVersion: receipt.RuntimeVersion, PolicyDigest: artifacts.AuthorizationFingerprint,
			DataRevision: artifacts.Generation.DataRevision, DataMode: artifacts.Generation.DataMode,
			ManagedDataPins: append([]release.ManagedDataPin(nil), artifacts.Generation.ManagedDataPins...),
			Bindings:        bindings, AuthoredConnections: candidateProvenanceAuthoredConnections(artifacts.Generation.AuthoredConnections),
			Extensions:   append([]extension.Evidence(nil), artifacts.Extensions...),
			GateEvidence: receipt.GateEvidence,
		},
	})
}

func candidateProvenanceAuthoredConnections(
	values []release.CandidateAuthoredConnection,
) []release.AuthoredConnectionEvidence {
	result := make([]release.AuthoredConnectionEvidence, len(values))
	for index, value := range values {
		result[index] = release.AuthoredConnectionEvidence{
			ConnectionID:  value.ConnectionID.String(),
			ConnectorKind: value.ConnectorKind,
			Access:        value.Access,
		}
	}
	return result
}

func (m *Module) verifiedCandidateProvenance(
	ctx context.Context,
	candidate deployment.Candidate,
) (release.Provenance, error) {
	if candidate.Status != deployment.CandidateReady ||
		digest.ValidateSHA256Identity(candidate.ProvenanceDigest) != nil {
		return release.Provenance{}, release.ErrProvenanceInvalid
	}
	provenance, err := m.candidateArtifacts.CandidateProvenance(
		ctx,
		candidate.Scope.ProjectID,
		candidate.ID,
		candidate.Revision,
	)
	if err != nil {
		return release.Provenance{}, err
	}
	if provenance.Digest != candidate.ProvenanceDigest ||
		provenance.Artifact.SourceDigest != candidate.ArtifactDigest {
		return release.Provenance{}, release.ErrProvenanceInvalid
	}
	return provenance, nil
}

func candidateSourceRevision(
	value *project.CandidateSourceRevision,
) *release.SourceRevisionProvenance {
	if value == nil {
		return nil
	}
	return &release.SourceRevisionProvenance{
		Revision: value.Revision, Repository: value.Repository,
		Ref: value.Ref, ChangeID: value.ChangeID,
	}
}

func candidateSourceRevisionProvenance(
	value *project.CandidateSourceRevision,
) (*release.SourceRevisionProvenance, error) {
	return release.NormalizeSourceRevisionProvenance(
		candidateSourceRevision(value),
	)
}

func equalCandidateSourceRevision(
	first,
	second *release.SourceRevisionProvenance,
) bool {
	if first == nil || second == nil {
		return first == second
	}
	return *first == *second
}

func candidateRuntimeRestrictions(values []release.CandidateRestriction) []deployment.CandidateRestriction {
	result := make([]deployment.CandidateRestriction, len(values))
	for index, value := range values {
		result[index] = deployment.CandidateRestriction{
			ID: value.ID, ObjectID: value.ObjectID, ObjectKind: value.ObjectKind, Subject: value.Subject,
			PolicyType: value.PolicyType, ExpressionJSON: value.ExpressionJSON,
		}
	}
	return result
}

func tentativeCandidate(
	candidate deployment.Candidate,
	request deployment.CandidateSynchronizationRequest,
) (deployment.Candidate, error) {
	if request.ExpectedCandidateID == "" {
		if candidate.Status == deployment.CandidatePreparing {
			return candidate, nil
		}
		if candidate.Status != deployment.CandidateReady {
			return deployment.Candidate{}, deployment.ErrCandidateConflict
		}
		request.ExpectedArtifactDigest = candidate.ArtifactDigest
	}
	if candidate.Status != deployment.CandidateReady ||
		request.ExpectedArtifactDigest != strings.TrimSpace(request.ExpectedArtifactDigest) ||
		request.ArtifactDigest != strings.TrimSpace(request.ArtifactDigest) ||
		candidate.ArtifactDigest != request.ExpectedArtifactDigest {
		return deployment.Candidate{}, deployment.ErrCandidateConflict
	}
	candidate.ArtifactDigest = request.ArtifactDigest
	candidate.ProvenanceDigest = ""
	candidate.Status = deployment.CandidatePreparing
	candidate.FailureReason = ""
	candidate.ReadyAt = time.Time{}
	candidate.Revision++
	return candidate, nil
}

func candidateScope(candidate deployment.Candidate) deployment.CandidateAccessScope {
	return deployment.CandidateAccessScope{
		ProjectID: candidate.Scope.ProjectID, CandidateID: candidate.ID,
		OwnerID: candidate.OwnerID, TargetID: candidate.TargetID,
	}
}

func candidatePreparationError(err error) error {
	switch {
	case errors.Is(err, release.ErrCandidateArtifactInvalid):
		return fmt.Errorf("%w: %v", deployment.ErrCandidateInvalid, err)
	case errors.Is(err, release.ErrProvenanceInvalid):
		return fmt.Errorf(
			"%w: candidate provenance is incompatible; reset target state before deploying",
			deployment.ErrCandidateInvalid,
		)
	case errors.Is(err, release.ErrConflict),
		errors.Is(err, release.ErrNotFound):
		return fmt.Errorf(
			"%w: candidate provenance validation failed",
			deployment.ErrCandidateInvalid,
		)
	case errors.Is(err, release.ErrCandidateArtifactUnavailable):
		return fmt.Errorf("%w: %v", deployment.ErrCandidateUnavailable, err)
	default:
		return err
	}
}

func (m *Module) decodeCandidateSynchronizationRequest(
	w http.ResponseWriter,
	r *http.Request,
) (deployment.CandidateSynchronizationRequest, bool) {
	var body deploymentapi.CandidateSynchronizationRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return deployment.CandidateSynchronizationRequest{}, false
	}
	request := deployment.CandidateSynchronizationRequest{
		ProjectFile: body.ProjectFile, ArtifactDigest: body.ArtifactDigest,
		SourceOnly: body.SourceOnly,
		Artifacts:  make([]deployment.CandidateSourceArtifact, len(body.Artifacts)),
	}
	if body.CandidateKey != nil {
		request.CandidateKey = *body.CandidateKey
	}
	if body.SourceRevision != nil {
		request.SourceRevision = &project.CandidateSourceRevision{
			Revision: body.SourceRevision.Revision,
		}
		if body.SourceRevision.Repository != nil {
			request.SourceRevision.Repository = *body.SourceRevision.Repository
		}
		if body.SourceRevision.Ref != nil {
			request.SourceRevision.Ref = *body.SourceRevision.Ref
		}
		if body.SourceRevision.ChangeID != nil {
			request.SourceRevision.ChangeID = *body.SourceRevision.ChangeID
		}
	}
	if body.ExpectedCandidateID != nil {
		request.ExpectedCandidateID = *body.ExpectedCandidateID
	}
	if body.ExpectedArtifactDigest != nil {
		request.ExpectedArtifactDigest = *body.ExpectedArtifactDigest
	}
	for index, artifact := range body.Artifacts {
		request.Artifacts[index] = deployment.CandidateSourceArtifact{
			Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes,
		}
	}
	return request, true
}

func (m *Module) candidateSynchronizationPrincipal(
	w http.ResponseWriter,
	r *http.Request,
	operationID *deploymentgen.GenCommandOperationID,
) (string, bool) {
	principal, ok := m.principal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return "", false
	}
	if m.candidateSources == nil {
		if operationID == nil {
			writeCandidateUnavailable(w, r)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, deployment.ErrCandidateUnavailable)
		}
		return "", false
	}
	return principal.ID, true
}

func (m *Module) validateExpectedCandidate(
	w http.ResponseWriter,
	r *http.Request,
	project, principalID string,
	request deployment.CandidateSynchronizationRequest,
	operationID *deploymentgen.GenCommandOperationID,
) bool {
	if request.ExpectedCandidateID != strings.TrimSpace(request.ExpectedCandidateID) || request.ExpectedArtifactDigest != strings.TrimSpace(request.ExpectedArtifactDigest) || request.CandidateKey != strings.TrimSpace(request.CandidateKey) {
		err := fmt.Errorf("%w: expected candidate fields must be canonical", deployment.ErrCandidateInvalid)
		if operationID == nil {
			writeCandidateAPIError(w, r, err)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, err)
		}
		return false
	}
	hasID := request.ExpectedCandidateID != ""
	hasDigest := request.ExpectedArtifactDigest != ""
	if hasID != hasDigest {
		err := fmt.Errorf(
			"%w: expected candidate identity and digest must be supplied together",
			deployment.ErrCandidateInvalid,
		)
		if operationID == nil {
			writeCandidateAPIError(w, r, err)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, err)
		}
		return false
	}
	if !hasID {
		return true
	}
	if m.candidates == nil {
		if operationID == nil {
			writeCandidateUnavailable(w, r)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, deployment.ErrCandidateUnavailable)
		}
		return false
	}
	projectID, projectErr := projectgraph.NewResourceID(project)
	if projectErr != nil {
		if operationID == nil {
			writeCandidateAPIError(w, r, projectErr)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, projectErr)
		}
		return false
	}
	candidate, err := m.candidates.Get(r.Context(), deployment.CandidateAccessScope{
		ProjectID: projectID, CandidateID: request.ExpectedCandidateID, OwnerID: principalID,
	})
	if err != nil {
		if operationID == nil {
			writeCandidateAPIError(w, r, err)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, err)
		}
		return false
	}
	if candidate.ArtifactDigest != request.ExpectedArtifactDigest {
		if operationID == nil {
			writeCandidateAPIError(w, r, deployment.ErrCandidateConflict)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, deployment.ErrCandidateConflict)
		}
		return false
	}
	candidateKey := request.CandidateKey
	if candidateKey == "" {
		candidateKey = "default"
	}
	if candidate.Key != candidateKey {
		if operationID == nil {
			writeCandidateAPIError(w, r, deployment.ErrCandidateConflict)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, deployment.ErrCandidateConflict)
		}
		return false
	}
	return true
}

func deploymentCommandOperation(operationID deploymentgen.GenCommandOperationID) *deploymentgen.GenCommandOperationID {
	return &operationID
}

func candidateSourceContentDigest(identity string) string {
	if identity != strings.TrimSpace(identity) {
		return ""
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(identity, "sha256:"))
	if err != nil || len(decoded) != 32 {
		return ""
	}
	return "sha-256=:" + base64.StdEncoding.EncodeToString(decoded) + ":"
}

type candidateSourceCountingReader struct {
	source io.Reader
	read   int64
}

func (reader *candidateSourceCountingReader) Read(buffer []byte) (int, error) {
	count, err := reader.source.Read(buffer)
	reader.read += int64(count)
	return count, err
}
