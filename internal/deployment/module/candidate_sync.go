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
	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/digest"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/release"
)

const maxCandidateSourceBlobBytes = 16 << 20

func (m *Module) PlanProjectCandidateSynchronization(w http.ResponseWriter, r *http.Request, project string) {
	request, ok := m.decodeCandidateSynchronizationRequest(w, r)
	if !ok {
		return
	}
	principalID, ok := m.candidateSynchronizationPrincipal(w, r, nil)
	if !ok {
		return
	}
	if !m.validateExpectedCandidate(w, r, project, principalID, request, nil) {
		return
	}
	missing, err := m.candidateSources.Plan(r.Context(), deployment.CandidateSourceScope{
		ProjectID: project, OwnerID: principalID, CandidateKey: request.CandidateKey,
	}, request)
	if err != nil {
		writeCandidateAPIError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, deploymentapi.CandidateSynchronizationPlanResponse{
		ArtifactDigest: request.ArtifactDigest, MissingDigests: missing,
	})
}

func (m *Module) UploadProjectCandidateSourceBlob(
	w http.ResponseWriter,
	r *http.Request,
	project, identity, contentType, contentDigest string,
) {
	operationID := deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob()
	principalID, ok := m.candidateSynchronizationPrincipal(w, r, deploymentCommandOperation(operationID))
	if !ok {
		return
	}
	identity = strings.TrimSpace(identity)
	if contentType != "application/octet-stream" ||
		digest.ValidateSHA256Identity(identity) != nil ||
		strings.TrimSpace(contentDigest) != candidateSourceContentDigest(identity) {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob(), apigenfailure.New("source_blob_invalid", "Candidate source blob headers do not match the canonical content identity"))
		return
	}
	counter := &candidateSourceCountingReader{source: http.MaxBytesReader(
		w, r.Body, maxCandidateSourceBlobBytes,
	)}
	if err := m.candidateSources.Upload(r.Context(), deployment.CandidateSourceScope{
		ProjectID: project, OwnerID: principalID,
	}, identity, counter); err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob(), err)
		return
	}
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(deploymentgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob(), apigenfailure.New("audit_unavailable", "Candidate source blob audit is temporarily unavailable"))
		return
	}
	if err := executor.Execute(r.Context(), operationID.APIGenOperationID(), apigencommand.Execution{
		BestEffortAudit: func(context.Context, apigencommand.Contract) error {
			return m.recordCandidateSourceBlobAudit(r, principalID, project, identity, counter.read)
		},
		LogMessage: "candidate source blob audit failed",
		LogAttributes: []slog.Attr{
			slog.String("project_id", strings.TrimSpace(project)),
			slog.String("digest", identity),
		},
	}); err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationUploadProjectCandidateSourceBlob(), apigenfailure.New("audit_unavailable", "Candidate source blob audit is temporarily unavailable"))
		return
	}
	w.Header().Set("Location", "/api/v1/projects/"+url.PathEscape(strings.TrimSpace(project))+
		"/candidate-sync/blobs/"+url.PathEscape(identity))
	apitransport.WriteJSON(w, http.StatusCreated, deploymentapi.CandidateSourceBlobResponse{
		Digest: identity, SizeBytes: counter.read,
	})
}

func (m *Module) recordCandidateSourceBlobAudit(
	r *http.Request,
	principalID, projectID, identity string,
	sizeBytes int64,
) error {
	contract, ok := deploymentgen.GetAPIGenOperationContract(
		deploymentgen.GenOperationUploadProjectCandidateSourceBlob,
	)
	if !ok || contract.Command == nil || !contract.Command.Audit.Required {
		return errors.New("required candidate source blob command audit contract is unavailable")
	}
	if m == nil || m.candidateSourceBlobAudit == nil {
		return errors.New("required candidate source blob audit sink is unavailable")
	}
	// Caller surface headers affect audit attribution only. APIGen authorization
	// has already enforced the generated privilege independently of this value.
	surface := "api"
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Invocation-Surface")), "cli") ||
		strings.EqualFold(strings.TrimSpace(r.Header.Get("X-LeapView-Client")), "cli") {
		surface = "cli"
	}
	metadata, err := deploymentgen.EncodeGenUploadProjectCandidateSourceBlobAuditPayload(deploymentgen.GenSchemaCandidateSourceBlobAuditPayload{
		OperationId: contract.OperationID, Surface: surface, Digest: identity, SizeBytes: sizeBytes,
	})
	if err != nil {
		return fmt.Errorf("encode candidate source blob audit metadata: %w", err)
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	return m.candidateSourceBlobAudit(r.Context(), CandidateSourceBlobAuditEvent{
		PrincipalID: principalID, ProjectID: strings.TrimSpace(projectID), Digest: identity,
		Action: contract.Command.Audit.SuccessAction, Privilege: contract.Command.Privilege,
		Status: "success", RequestID: requestID, CorrelationID: correlationID,
		MetadataJSON: metadata,
	})
}

func (m *Module) CommitProjectCandidateSynchronization(
	w http.ResponseWriter,
	r *http.Request,
	project, _ string,
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
	if !m.validateExpectedCandidate(w, r, project, principalID, request, deploymentCommandOperation(operationID)) {
		return
	}
	scope := deployment.CandidateSourceScope{ProjectID: project, OwnerID: principalID}
	scope.CandidateKey = request.CandidateKey
	source, err := m.candidateSources.Commit(r.Context(), scope, request)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCommitProjectCandidateSynchronization(), err)
		return
	}
	var candidate deployment.Candidate
	if request.ExpectedCandidateID == "" {
		var started deployment.CandidateStartResult
		started, err = m.candidates.Start(r.Context(), deployment.StartCandidateRequest{
			ProjectID: project, OwnerID: principalID, ArtifactDigest: request.ArtifactDigest,
			Key: request.CandidateKey,
		})
		candidate = started.Candidate
		if err == nil && candidate.Status == deployment.CandidateFailed {
			candidate, err = m.candidates.Retry(r.Context(), candidateScope(candidate))
		}
	} else {
		candidate, err = m.candidates.Get(r.Context(), deployment.CandidateScope{
			ProjectID: project, CandidateID: request.ExpectedCandidateID, OwnerID: principalID,
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
	provenance, err := m.prepareCandidate(preparationContext, tentative, source)
	if err != nil {
		m.candidateLogger().Error(
			"candidate preparation failed",
			"candidate_id", candidate.ID,
			"project_id", candidate.ProjectID,
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
			CandidateID: candidate.ID, ProjectID: candidate.ProjectID,
			OwnerID: candidate.OwnerID, Environment: candidate.Environment,
			ArtifactDigest: candidate.ArtifactDigest, Source: source,
		},
	)
	if err != nil {
		return release.Provenance{}, err
	}
	workspaces := make([]deployment.CandidateWorkspaceRuntime, len(artifacts.Workspaces))
	for index, workspace := range artifacts.Workspaces {
		requirements := make(
			[]deployment.CandidateConnectionRequirement,
			len(workspace.Connections),
		)
		for requirementIndex, requirement := range workspace.Connections {
			requirements[requirementIndex] = deployment.CandidateConnectionRequirement{
				LogicalConnectionID: requirement.LogicalConnectionID,
				ConnectorKind:       requirement.ConnectorKind,
			}
		}
		workspaces[index] = deployment.CandidateWorkspaceRuntime{
			WorkspaceID: workspace.WorkspaceID, ServingStateID: workspace.ServingStateID,
			ArtifactDigest: workspace.ArtifactDigest, DataRevision: workspace.DataRevision,
			DataMode: deployment.CandidateDataMode(workspace.DataMode), Connections: requirements,
			AuthoredConnections: candidateAuthoredConnections(workspace.AuthoredConnections),
			ManagedDataConnections: candidateManagedDataConnections(
				workspace.ManagedDataPins,
			),
			Restrictions: candidateRuntimeRestrictions(workspace.Restrictions),
		}
	}
	receipt, err := m.candidateRuntimes.Prepare(ctx, deployment.CandidateRuntimeRequest{
		Candidate: candidate, AuthorizationFingerprint: artifacts.AuthorizationFingerprint,
		Workspaces: workspaces,
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
		candidate.ProjectID,
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
			LogicalConnectionID: value.LogicalConnectionID,
			ConnectorKind:       value.ConnectorKind,
		}
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
	if strings.TrimSpace(artifacts.Artifact.SourceDigest) !=
		strings.TrimSpace(candidate.ArtifactDigest) {
		return release.Provenance{}, release.ErrProvenanceInvalid
	}
	bindings := make(
		map[string][]release.BindingEvidence,
		len(receipt.Workspaces),
	)
	for _, workspace := range receipt.Workspaces {
		workspaceID := strings.TrimSpace(workspace.WorkspaceID)
		if workspaceID == "" {
			return release.Provenance{}, release.ErrProvenanceInvalid
		}
		if _, exists := bindings[workspaceID]; exists {
			return release.Provenance{}, release.ErrProvenanceInvalid
		}
		evidence := make([]release.BindingEvidence, len(workspace.Bindings))
		for index, item := range workspace.Bindings {
			evidence[index] = release.BindingEvidence{
				BindingID: item.BindingID, LogicalConnection: item.LogicalConnection,
				ConnectorKind: item.ConnectorKind, Revision: item.Revision,
				ValidatedVersion: item.ProviderVersion, EndpointConfigHash: item.EndpointConfigHash,
			}
		}
		bindings[workspaceID] = evidence
	}
	plans := make(
		[]release.TargetWorkspacePlan,
		len(artifacts.Workspaces),
	)
	for index, workspace := range artifacts.Workspaces {
		workspaceID := strings.TrimSpace(workspace.WorkspaceID)
		workspaceBindings, exists := bindings[workspaceID]
		if !exists {
			return release.Provenance{}, release.ErrProvenanceInvalid
		}
		delete(bindings, workspaceID)
		artifactDigest, err := candidateProvenanceArtifactDigest(
			workspace.ArtifactDigest,
		)
		if err != nil {
			return release.Provenance{}, err
		}
		plans[index] = release.TargetWorkspacePlan{
			WorkspaceID:    workspaceID,
			ServingStateID: workspace.ServingStateID,
			ArtifactDigest: artifactDigest,
			DataRevision:   workspace.DataRevision,
			DataMode:       release.TargetDataMode(workspace.DataMode),
			ManagedDataPins: append(
				[]release.ManagedDataPin(nil),
				workspace.ManagedDataPins...,
			),
			Bindings: workspaceBindings,
			AuthoredConnections: candidateProvenanceAuthoredConnections(
				workspace.AuthoredConnections,
			),
		}
	}
	if len(bindings) != 0 {
		return release.Provenance{}, release.ErrProvenanceInvalid
	}
	return release.NewProvenance(release.ProvenanceInput{
		Artifact: artifacts.Artifact,
		Candidate: release.CandidateProvenance{
			ID:       candidate.ID,
			Revision: candidate.Revision + 1,
			OwnerID:  candidate.OwnerID,
		},
		SourceRevision: candidateSourceRevision(sourceRevision),
		Plan: release.TargetPlanProvenance{
			TargetID:       candidate.TargetID,
			Environment:    candidate.Environment,
			BaseGeneration: candidate.BaseGeneration,
			RuntimeVersion: receipt.RuntimeVersion,
			PolicyDigest:   artifacts.AuthorizationFingerprint,
			Workspaces:     plans,
		},
	})
}

func candidateProvenanceAuthoredConnections(
	values []release.CandidateAuthoredConnection,
) []release.AuthoredConnectionEvidence {
	result := make([]release.AuthoredConnectionEvidence, len(values))
	for index, value := range values {
		result[index] = release.AuthoredConnectionEvidence{
			LogicalConnection: value.LogicalConnectionID,
			ConnectorKind:     value.ConnectorKind,
		}
	}
	return result
}

func candidateProvenanceArtifactDigest(value string) (string, error) {
	value = strings.TrimSpace(value)
	if digest.ValidateSHA256Identity(value) == nil {
		return value, nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || strings.ToLower(value) != value {
		return "", release.ErrProvenanceInvalid
	}
	return "sha256:" + value, nil
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
		candidate.ProjectID,
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
			ID: value.ID, WorkspaceID: value.WorkspaceID, ObjectID: value.ObjectID,
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
		candidate.ArtifactDigest != strings.TrimSpace(request.ExpectedArtifactDigest) {
		return deployment.Candidate{}, deployment.ErrCandidateConflict
	}
	candidate.ArtifactDigest = strings.TrimSpace(request.ArtifactDigest)
	candidate.ProvenanceDigest = ""
	candidate.Status = deployment.CandidatePreparing
	candidate.FailureReason = ""
	candidate.ReadyAt = time.Time{}
	candidate.Revision++
	return candidate, nil
}

func candidateScope(candidate deployment.Candidate) deployment.CandidateScope {
	return deployment.CandidateScope{
		ProjectID: candidate.ProjectID, CandidateID: candidate.ID,
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
		Artifacts: make([]deployment.CandidateSourceArtifact, len(body.Artifacts)),
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
			Path: artifact.Path, Digest: artifact.Digest,
		}
	}
	return request, true
}

func (m *Module) candidateSynchronizationPrincipal(
	w http.ResponseWriter,
	r *http.Request,
	operationID *deploymentgen.GenCommandOperationID,
) (string, bool) {
	var principalID string
	var ok bool
	if operationID == nil {
		principalID, ok = m.candidatePrincipalID(w, r)
	} else {
		principalID, ok = m.candidatePrincipalIDCommand(w, r, *operationID)
	}
	if !ok {
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
	return principalID, true
}

func (m *Module) validateExpectedCandidate(
	w http.ResponseWriter,
	r *http.Request,
	project, principalID string,
	request deployment.CandidateSynchronizationRequest,
	operationID *deploymentgen.GenCommandOperationID,
) bool {
	hasID := strings.TrimSpace(request.ExpectedCandidateID) != ""
	hasDigest := strings.TrimSpace(request.ExpectedArtifactDigest) != ""
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
	candidate, err := m.candidates.Get(r.Context(), deployment.CandidateScope{
		ProjectID: project, CandidateID: request.ExpectedCandidateID, OwnerID: principalID,
	})
	if err != nil {
		if operationID == nil {
			writeCandidateAPIError(w, r, err)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, err)
		}
		return false
	}
	if candidate.ArtifactDigest != strings.TrimSpace(request.ExpectedArtifactDigest) {
		if operationID == nil {
			writeCandidateAPIError(w, r, deployment.ErrCandidateConflict)
		} else {
			m.writeCandidateCommandFailure(w, r, *operationID, deployment.ErrCandidateConflict)
		}
		return false
	}
	candidateKey := strings.TrimSpace(request.CandidateKey)
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
	decoded, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(identity), "sha256:"))
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
