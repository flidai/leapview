package module

import (
	"context"
	"encoding/base64"
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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
	if contentType != "application/octet-stream" ||
		identity != strings.TrimSpace(identity) || contentDigest != strings.TrimSpace(contentDigest) ||
		digest.ValidateSHA256Identity(identity) != nil ||
		contentDigest != candidateSourceContentDigest(identity) {
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
			ProjectID: projectgraph.ResourceID(project), OwnerID: principalID, ArtifactDigest: request.ArtifactDigest,
			Key: request.CandidateKey,
		})
		candidate = started.Candidate
		if err == nil && candidate.Status == deployment.CandidateFailed {
			candidate, err = m.candidates.Retry(r.Context(), candidateScope(candidate))
		}
	} else {
		candidate, err = m.candidates.Get(r.Context(), deployment.CandidateAccessScope{
			ProjectID: projectgraph.ResourceID(project), CandidateID: request.ExpectedCandidateID, OwnerID: principalID,
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
	baseIdentity := (*projectgraph.ServingIdentity)(nil)
	if candidate.Scope.BaseGenerationID != "" {
		if candidate.Scope.ProjectID != identity.ProjectID || candidate.Scope.Environment != identity.Environment {
			return release.Provenance{}, release.ErrProvenanceInvalid
		}
		baseIdentity, err = candidate.Scope.BaseIdentity()
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
		}
	}
	return result
}

func candidateConnectionRequirements(
	values []release.CandidateConnectionRequirement,
) []deployment.CandidateConnectionRequirement {
	result := make([]deployment.CandidateConnectionRequirement, len(values))
	for index, value := range values {
		result[index] = deployment.CandidateConnectionRequirement{ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind}
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
		bindings[index] = release.BindingEvidence{BindingID: item.BindingID, ConnectionID: item.ConnectionID, ConnectorKind: item.ConnectorKind, Revision: item.Revision, ValidatedVersion: item.ProviderVersion, EndpointConfigHash: item.EndpointConfigHash}
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
	candidate, err := m.candidates.Get(r.Context(), deployment.CandidateAccessScope{
		ProjectID: projectgraph.ResourceID(project), CandidateID: request.ExpectedCandidateID, OwnerID: principalID,
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
