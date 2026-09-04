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

// RetainProjectCandidateSourceOperationID exposes the generated source-retention
// command identity through the deployment module boundary. Native production
// composition uses it for the narrowly reviewed expired-lease reclaim policy.
const RetainProjectCandidateSourceOperationID = string(deploymentgen.GenOperationRetainProjectCandidateSource)

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
	if err := m.claimCandidateSynchronizationProject(r.Context(), projectID, principalID); err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
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

func (m *Module) claimCandidateSynchronizationProject(ctx context.Context, projectID projectgraph.ResourceID, principalID string) error {
	if m == nil {
		return deployment.ErrCandidateUnavailable
	}
	if m.projectClaims == nil {
		return deployment.ErrCandidateUnavailable
	}
	_, err := m.projectClaims.ClaimProject(ctx, deployment.ProjectClaimInput{
		ProjectID: projectID, Environment: m.instanceEnvironment, ClaimedBy: principalID,
	})
	if err != nil {
		return err
	}
	if m.bindClaimedProject != nil {
		if err := m.bindClaimedProject(ctx, projectID, m.instanceEnvironment); err != nil {
			return fmt.Errorf("bind claimed project: %w", err)
		}
	}
	return nil
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
	apitransport.WriteJSON(w, http.StatusOK, deploymentgen.CandidateSourceSnapshotResponse{
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
			Restrictions:           candidateRuntimeRestrictions(generation.Restrictions),
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
