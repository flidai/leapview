package module

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	deploymentui "github.com/flidai/leapview/internal/deployment/ui"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func (m *Module) ResolveOwnedCandidate(ctx context.Context, candidateID, principalID string) (Candidate, error) {
	candidateID = strings.TrimSpace(candidateID)
	principalID = strings.TrimSpace(principalID)
	if m == nil {
		return Candidate{}, deployment.ErrCandidateUnavailable
	}
	if m.candidates != nil {
		candidate, err := m.candidates.GetOwned(ctx, candidateID, principalID)
		if err != nil {
			return Candidate{}, err
		}
		candidate.PreviewURL = m.candidates.PreviewURL(candidate.ID)
		return candidate, nil
	}
	if m.nativeDeliveryReader == nil {
		return Candidate{}, deployment.ErrCandidateUnavailable
	}
	return m.resolveNativeOwnedCandidate(ctx, candidateID, principalID)
}

// ResolveCandidateForReview returns bounded candidate evidence for a reviewer
// authorized at the project boundary. It deliberately does not acquire the
// candidate runtime or expose an owner principal; governed candidate preview
// remains owner-only.
func (m *Module) ResolveCandidateForReview(ctx context.Context, projectID projectgraph.ResourceID, candidateID string) (Candidate, error) {
	if m == nil || m.candidates == nil {
		return Candidate{}, deployment.ErrCandidateUnavailable
	}
	return m.candidates.Review(ctx, projectID, strings.TrimSpace(candidateID))
}

// MarkCanonicalCandidateReady is used by the plan-driven adapter after the
// durable catalog seal has completed. It keeps the legacy candidate API view
// synchronized with the immutable delivery candidate without allowing the
// HTTP layer to bypass sealing.
func (m *Module) MarkCanonicalCandidateReady(ctx context.Context, candidate Candidate, provenanceDigest string) (Candidate, error) {
	if m == nil || m.candidates == nil {
		return Candidate{}, deployment.ErrCandidateUnavailable
	}
	return m.candidates.MarkReady(ctx, candidateScope(candidate), candidate.ArtifactDigest, provenanceDigest)
}

func (m *Module) ServeCandidatePreview(
	w http.ResponseWriter,
	r *http.Request,
	candidateID, principalID string,
	layout webpage.Provider,
) {
	if m == nil || (m.candidates == nil && m.nativeDeliveryReader == nil) {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return
	}
	candidate, err := m.ResolveOwnedCandidate(r.Context(), candidateID, principalID)
	if err != nil {
		if errors.Is(err, deployment.ErrCandidateNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return
	}

	status := http.StatusOK
	switch candidate.Status {
	case deployment.CandidatePreparing:
		status = http.StatusAccepted
		w.Header().Set("Retry-After", "1")
	case deployment.CandidateFailed:
		status = http.StatusConflict
	case deployment.CandidateCancelled, deployment.CandidateExpired:
		status = http.StatusGone
	case deployment.CandidateReady:
	default:
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = deploymentui.CandidatePage(candidate, layout).Render(w)
}

// resolveNativeOwnedCandidate rehydrates the legacy owner-bound candidate
// view from the canonical PostgreSQL delivery evidence. Native candidate rows
// intentionally do not persist an owner; ownership is proved by the exact
// candidate -> seal -> build-attempt -> plan chain, with the attempt owner
// compared to the authenticated principal before any candidate fields are
// returned.
func (m *Module) resolveNativeOwnedCandidate(ctx context.Context, candidateID, principalID string) (Candidate, error) {
	if candidateID == "" || principalID == "" || candidateID != strings.TrimSpace(candidateID) || principalID != strings.TrimSpace(principalID) {
		return Candidate{}, deployment.ErrCandidateNotFound
	}
	reader := m.nativeDeliveryReader
	row, err := reader.Candidate(ctx, candidateID)
	if err != nil {
		return Candidate{}, nativeCandidateReadError(err)
	}
	if row.CandidateID != candidateID || row.TargetID == "" || (m.instanceID != "" && row.TargetID != m.instanceID) || row.PlanID == "" || row.SnapshotSealID == "" {
		return Candidate{}, deployment.ErrCandidateNotFound
	}
	seal, err := reader.SnapshotSeal(ctx, row.SnapshotSealID)
	if err != nil {
		return Candidate{}, nativeCandidateReadError(err)
	}
	if seal.SealID != row.SnapshotSealID || seal.CandidateID != row.CandidateID || seal.AttemptID == "" || (row.AttemptID != "" && row.AttemptID != seal.AttemptID) {
		return Candidate{}, nativeCandidateEvidenceUnavailable("candidate snapshot seal identity is inconsistent")
	}
	attempt, err := reader.BuildAttempt(ctx, seal.AttemptID)
	if err != nil {
		return Candidate{}, nativeCandidateReadError(err)
	}
	if attempt.AttemptID != seal.AttemptID || attempt.CandidateID != row.CandidateID || attempt.PlanID != row.PlanID {
		return Candidate{}, nativeCandidateEvidenceUnavailable("candidate build attempt identity is inconsistent")
	}
	// A foreign candidate must be indistinguishable from a missing candidate.
	if attempt.OwnerID != principalID {
		return Candidate{}, deployment.ErrCandidateNotFound
	}
	plan, err := nativeReadPlan(ctx, reader, row.PlanID)
	if err != nil {
		return Candidate{}, nativeCandidateReadError(err)
	}
	if plan.ID != row.PlanID || plan.TargetID != row.TargetID || plan.ProjectID.Validate() != nil || plan.Environment == "" ||
		(m.instanceEnvironment != "" && plan.Environment != string(m.instanceEnvironment)) ||
		attempt.PlanDigest != plan.Digest || seal.PlanDigest != plan.Digest || seal.AttemptID != attempt.AttemptID ||
		seal.ServingArtifactDigest == "" || row.ArtifactDigest == "" || row.ArtifactDigest != seal.ServingArtifactDigest ||
		plan.ServingArtifactDigest != seal.ServingArtifactDigest || row.CandidateRevision < 1 || row.CreatedAt.IsZero() ||
		plan.Governance.ExpiresAt.IsZero() || platformdigest.ValidateSHA256Identity(plan.SourceDigest) != nil || platformdigest.ValidateSHA256Identity(plan.ProvenanceDigest) != nil {
		return Candidate{}, nativeCandidateEvidenceUnavailable("candidate delivery evidence is inconsistent")
	}

	nativeStatus := strings.ToLower(strings.TrimSpace(row.Status))
	if (nativeStatus == "qualified" || nativeStatus == "ready") && (attempt.State != nativepostgres.AttemptCommitted || row.QualifiedAt.IsZero()) {
		return Candidate{}, nativeCandidateEvidenceUnavailable("qualified candidate evidence is incomplete")
	}
	// The durable row may remain qualified after its governance deadline. Keep
	// the owner-facing projection terminal at the deadline so callers return
	// the normal expired-candidate response instead of attempting preparation
	// against an already-invalid plan. Capture one UTC instant for this check.
	now := time.Now().UTC()
	status := nativeCandidateStatusForPreviewAt(row.Status, attempt.State, plan.Governance.ExpiresAt, now)
	previewURL, err := m.candidatePreviewURL(row.CandidateID)
	if err != nil {
		return Candidate{}, nativeCandidateEvidenceUnavailable("candidate preview origin is unavailable")
	}
	updatedAt := row.CreatedAt.UTC()
	if !attempt.UpdatedAt.IsZero() && attempt.UpdatedAt.After(updatedAt) {
		updatedAt = attempt.UpdatedAt.UTC()
	}
	if !row.QualifiedAt.IsZero() && row.QualifiedAt.After(updatedAt) {
		updatedAt = row.QualifiedAt.UTC()
	}
	result := Candidate{
		ID: row.CandidateID, Key: row.CandidateID, TargetID: row.TargetID, OwnerID: attempt.OwnerID, PreviewURL: previewURL,
		Scope: deployment.CandidateScope{ProjectID: plan.ProjectID, Environment: plan.Environment, BaseGenerationID: plan.BaseGenerationID},
		// The legacy candidate view calls this field ArtifactDigest, but its
		// authoring/CLI contract is the retained source identity. Native
		// serving-artifact identity remains in the seal and is checked above.
		ArtifactDigest: plan.SourceDigest, ProvenanceDigest: plan.ProvenanceDigest,
		Status: status, ExpiresAt: plan.Governance.ExpiresAt.UTC(), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: updatedAt,
		Revision: row.CandidateRevision,
	}
	if status == deployment.CandidateReady {
		result.ReadyAt = row.QualifiedAt.UTC()
	}
	return result, nil
}

func nativeCandidateStatusForPreview(status string, attemptState nativepostgres.BuildAttemptState) deployment.CandidateStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "qualified", "ready":
		return deployment.CandidateReady
	case "rejected", "failed":
		return deployment.CandidateFailed
	case "retired":
		return deployment.CandidateExpired
	}
	switch attemptState {
	case nativepostgres.AttemptAborted, nativepostgres.AttemptFenced:
		return deployment.CandidateFailed
	default:
		return deployment.CandidatePreparing
	}
}

func nativeCandidateStatusForPreviewAt(status string, attemptState nativepostgres.BuildAttemptState, expiresAt, now time.Time) deployment.CandidateStatus {
	previewStatus := nativeCandidateStatusForPreview(status, attemptState)
	if previewStatus == deployment.CandidateReady && !expiresAt.After(now) {
		return deployment.CandidateExpired
	}
	return previewStatus
}

func nativeCandidateReadError(err error) error {
	if errors.Is(err, nativepostgres.ErrNotFound) || errors.Is(err, deployment.ErrNotFound) {
		return fmt.Errorf("%w: native candidate not found", deployment.ErrCandidateNotFound)
	}
	return fmt.Errorf("%w: native candidate evidence read failed: %v", deployment.ErrCandidateUnavailable, err)
}

func nativeCandidateEvidenceUnavailable(detail string) error {
	return fmt.Errorf("%w: %s", deployment.ErrCandidateUnavailable, detail)
}

func (m *Module) candidatePreviewURL(candidateID string) (string, error) {
	origin := strings.TrimRight(strings.TrimSpace(m.canonicalOrigin), "/")
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") {
		return "", fmt.Errorf("resolved target has no canonical HTTP origin")
	}
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return "", fmt.Errorf("native delivery candidate identity is missing")
	}
	return parsed.Scheme + "://" + parsed.Host + "/candidates/" + url.PathEscape(candidateID), nil
}

// ServeCandidateReview renders non-sensitive candidate evidence for an
// authorized reviewer without acquiring the candidate runtime. The review
// operation is intentionally separate from preview so this surface cannot
// widen owner-only data-query authorization.
func (m *Module) ServeCandidateReview(
	w http.ResponseWriter,
	r *http.Request,
	candidateID string,
	projectID projectgraph.ResourceID,
	layout webpage.Provider,
) {
	if m == nil || m.candidates == nil {
		http.Error(w, "Candidate review is unavailable", http.StatusServiceUnavailable)
		return
	}
	candidate, err := m.ResolveCandidateForReview(r.Context(), projectID, candidateID)
	if err != nil {
		if errors.Is(err, deployment.ErrCandidateNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Candidate review is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	status := http.StatusOK
	switch candidate.Status {
	case deployment.CandidatePreparing:
		status = http.StatusAccepted
		w.Header().Set("Retry-After", "1")
	case deployment.CandidateFailed:
		status = http.StatusConflict
	case deployment.CandidateCancelled, deployment.CandidateExpired:
		status = http.StatusGone
	}
	w.WriteHeader(status)
	_ = deploymentui.CandidateReviewPage(candidate, layout).Render(w)
}

func (m *Module) StartProjectCandidate(w http.ResponseWriter, r *http.Request, project, _ string) {
	var body deploymentapi.CandidateStartRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	principalID, ok := m.candidatePrincipalIDCommand(w, r, deploymentgen.GenCommandOperationStartProjectCandidate())
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationStartProjectCandidate(), err)
		return
	}
	startRequest := deployment.StartCandidateRequest{
		ProjectID: projectID, OwnerID: principalID, ArtifactDigest: body.ArtifactDigest,
	}
	if body.CandidateKey != nil {
		startRequest.Key = *body.CandidateKey
	}
	result, err := m.candidates.Start(r.Context(), startRequest)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationStartProjectCandidate(), err)
		return
	}
	w.Header().Set("Location", candidateLocation(project, result.Candidate.ID))
	apitransport.WriteJSON(w, http.StatusCreated, m.candidateResponse(result.Candidate, result.Resumed))
}

func (m *Module) GetProjectCandidate(w http.ResponseWriter, r *http.Request, project, candidateID string) {
	candidate, ok := m.ownedCandidate(w, r, project, candidateID)
	if !ok {
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
}

func (m *Module) ReviewProjectCandidate(
	w http.ResponseWriter,
	r *http.Request,
	project,
	candidateID string,
) {
	if _, ok := m.candidatePrincipalID(w, r); !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		writeCandidateAPIError(w, r, err)
		return
	}
	candidate, err := m.candidates.Review(
		r.Context(),
		projectID,
		candidateID,
	)
	if err != nil {
		writeCandidateAPIError(w, r, err)
		return
	}
	apitransport.WriteJSON(
		w,
		http.StatusOK,
		m.candidateResponse(candidate, false),
	)
}

func (m *Module) ReplaceProjectCandidateArtifact(w http.ResponseWriter, r *http.Request, project, candidateID, _ string) {
	var body deploymentapi.CandidateArtifactRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	principalID, ok := m.candidatePrincipalIDCommand(w, r, deploymentgen.GenCommandOperationReplaceProjectCandidateArtifact())
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationReplaceProjectCandidateArtifact(), err)
		return
	}
	candidate, err := m.candidates.ReplaceArtifact(r.Context(), deployment.CandidateAccessScope{
		ProjectID: projectID, CandidateID: candidateID, OwnerID: principalID,
	}, body.ExpectedArtifactDigest, body.ArtifactDigest)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationReplaceProjectCandidateArtifact(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
}

func (m *Module) RetryProjectCandidate(w http.ResponseWriter, r *http.Request, project, candidateID, _ string) {
	principalID, ok := m.candidatePrincipalIDCommand(w, r, deploymentgen.GenCommandOperationRetryProjectCandidate())
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationRetryProjectCandidate(), err)
		return
	}
	candidate, err := m.candidates.Retry(r.Context(), deployment.CandidateAccessScope{
		ProjectID: projectID, CandidateID: candidateID, OwnerID: principalID,
	})
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationRetryProjectCandidate(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
}

func (m *Module) CancelProjectCandidate(w http.ResponseWriter, r *http.Request, project, candidateID, _ string) {
	principalID, ok := m.candidatePrincipalIDCommand(w, r, deploymentgen.GenCommandOperationCancelProjectCandidate())
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCancelProjectCandidate(), err)
		return
	}
	candidate, err := m.candidates.Cancel(r.Context(), deployment.CandidateAccessScope{
		ProjectID: projectID, CandidateID: candidateID, OwnerID: principalID,
	})
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCancelProjectCandidate(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
}

func (m *Module) CancelProjectCandidateByKey(
	w http.ResponseWriter,
	r *http.Request,
	project,
	candidateKey,
	_ string,
) {
	principalID, ok := m.candidatePrincipalIDCommand(w, r, deploymentgen.GenCommandOperationCancelProjectCandidateByKey())
	if !ok {
		return
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCancelProjectCandidateByKey(), err)
		return
	}
	candidate, err := m.candidates.CancelActive(
		r.Context(),
		projectID,
		principalID,
		candidateKey,
	)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationCancelProjectCandidateByKey(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
}

func (m *Module) PublishProjectCandidate(
	w http.ResponseWriter,
	r *http.Request,
	project,
	candidateID,
	idempotencyKey string,
) {
	var body deploymentapi.CandidatePublishRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(
			w,
			r,
			http.StatusBadRequest,
			"INVALID_JSON",
			err.Error(),
			nil,
		)
		return
	}
	candidate, ok := m.ownedCandidateCommand(w, r, project, candidateID, deploymentgen.GenCommandOperationPublishProjectCandidate())
	if !ok {
		return
	}
	provenanceDigest := strings.TrimSpace(body.ProvenanceDigest)
	targetID := strings.TrimSpace(body.TargetID)
	if candidate.Status != deployment.CandidateReady ||
		candidate.Revision != body.ExpectedRevision ||
		candidate.ProvenanceDigest != provenanceDigest ||
		candidate.TargetID != targetID ||
		targetID != strings.TrimSpace(m.instanceID) {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationPublishProjectCandidate(), deployment.ErrCandidateConflict)
		return
	}
	if m.api.Releases == nil {
		m.writeCandidateCommandFailure(w, r, deploymentgen.GenCommandOperationPublishProjectCandidate(), apigenfailure.New("service_unavailable", "Candidate publication is unavailable"))
		return
	}
	published, err := m.api.Releases.PublishCandidate(
		r.Context(),
		release.PublishCandidateInput{
			Scope: candidate.Scope, CandidateID: candidate.ID,
			CandidateRevision: candidate.Revision,
			ProvenanceDigest:  candidate.ProvenanceDigest,
			TargetID:          candidate.TargetID,
			IdempotencyKey:    idempotencyKey,
			CreatedBy:         candidate.OwnerID,
		},
	)
	if err != nil {
		m.writeCommandFailure(w, r, deploymentgen.GenCommandOperationPublishProjectCandidate(), err)
		return
	}
	if m.candidateRuntimeLifecycle != nil {
		m.candidateRuntimeLifecycle.RetireCandidate(candidate.ID)
	}
	if body.Bootstrap {
		m.createDeploymentWithBootstrap(
			w,
			r,
			deploymentgen.GenCommandOperationPublishProjectCandidate(),
			project,
			published.ID,
			idempotencyKey,
			"",
			true,
		)
		return
	}
	m.createDeployment(
		w,
		r,
		deploymentgen.GenCommandOperationPublishProjectCandidate(),
		project,
		published.ID,
		idempotencyKey,
		"",
	)
}

func (m *Module) ownedCandidate(w http.ResponseWriter, r *http.Request, project, candidateID string) (deployment.Candidate, bool) {
	principalID, ok := m.candidatePrincipalID(w, r)
	if !ok {
		return deployment.Candidate{}, false
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		writeCandidateAPIError(w, r, err)
		return deployment.Candidate{}, false
	}
	candidate, err := m.candidates.Get(r.Context(), deployment.CandidateAccessScope{
		ProjectID: projectID, CandidateID: candidateID, OwnerID: principalID,
	})
	if err != nil {
		writeCandidateAPIError(w, r, err)
		return deployment.Candidate{}, false
	}
	return candidate, true
}

func (m *Module) ownedCandidateCommand(w http.ResponseWriter, r *http.Request, project, candidateID string, operationID deploymentgen.GenCommandOperationID) (deployment.Candidate, bool) {
	principalID, ok := m.candidatePrincipalIDCommand(w, r, operationID)
	if !ok {
		return deployment.Candidate{}, false
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
		return deployment.Candidate{}, false
	}
	candidate, err := m.candidates.Get(r.Context(), deployment.CandidateAccessScope{
		ProjectID: projectID, CandidateID: candidateID, OwnerID: principalID,
	})
	if err != nil {
		m.writeCandidateCommandFailure(w, r, operationID, err)
		return deployment.Candidate{}, false
	}
	return candidate, true
}

func (m *Module) candidatePrincipalID(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal, ok := m.principal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return "", false
	}
	if m.candidates == nil {
		writeCandidateUnavailable(w, r)
		return "", false
	}
	return principal.ID, true
}

func (m *Module) candidatePrincipalIDCommand(w http.ResponseWriter, r *http.Request, operationID deploymentgen.GenCommandOperationID) (string, bool) {
	principal, ok := m.principal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return "", false
	}
	if m.candidates == nil {
		m.writeCandidateCommandFailure(w, r, operationID, deployment.ErrCandidateUnavailable)
		return "", false
	}
	return principal.ID, true
}

func (m *Module) candidateResponse(candidate deployment.Candidate, resumed bool) deploymentapi.CandidateResponse {
	previewURL := candidate.PreviewURL
	if previewURL == "" && m != nil && m.candidates != nil {
		previewURL = m.candidates.PreviewURL(candidate.ID)
	}
	response := deploymentapi.CandidateResponse{
		ID: candidate.ID, ProjectID: candidate.Scope.ProjectID.String(), CandidateKey: candidate.Key,
		TargetID:    candidate.TargetID,
		Environment: candidate.Scope.Environment, OwnerID: candidate.OwnerID, BaseGeneration: candidate.Scope.BaseGenerationID,
		ArtifactDigest: candidate.ArtifactDigest, Status: string(candidate.Status),
		PreviewURL: previewURL, ExpiresAt: candidate.ExpiresAt.UTC().Format(time.RFC3339Nano),
		CreatedAt: candidate.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: candidate.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Revision: candidate.Revision,
	}
	if candidate.FailureReason != "" {
		response.FailureReason = &candidate.FailureReason
	}
	if candidate.ProvenanceDigest != "" {
		response.ProvenanceDigest = &candidate.ProvenanceDigest
	}
	if resumed {
		response.Resumed = &resumed
	}
	return response
}

func candidateLocation(project, candidateID string) string {
	return "/api/v1/projects/" + url.PathEscape(strings.TrimSpace(project)) + "/candidates/" + url.PathEscape(strings.TrimSpace(candidateID))
}

func writeCandidateUnavailable(w http.ResponseWriter, r *http.Request) {
	apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "CANDIDATE_SERVICE_UNAVAILABLE", "Candidate service is unavailable", nil)
}

func writeCandidateAPIError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, detail := http.StatusInternalServerError, "INTERNAL_ERROR", "The candidate request could not be completed"
	if kind, ok := apigenfailure.KindOf(err); ok {
		switch kind {
		case "candidate_unavailable":
			status, code, detail = http.StatusServiceUnavailable, "CANDIDATE_SERVICE_UNAVAILABLE", "Candidate service is unavailable"
		case "candidate_not_found":
			status, code, detail = http.StatusNotFound, "CANDIDATE_NOT_FOUND", "Candidate not found"
		case "candidate_conflict":
			status, code, detail = http.StatusConflict, "CANDIDATE_CONFLICT", err.Error()
		case "candidate_quota":
			status, code, detail = http.StatusTooManyRequests, "CANDIDATE_QUOTA_EXCEEDED", "Candidate quota exceeded"
		case "candidate_invalid":
			status, code, detail = http.StatusUnprocessableEntity, "INVALID_CANDIDATE", err.Error()
		case "source_blob_invalid":
			status, code, detail = http.StatusUnprocessableEntity, "INVALID_CANDIDATE_SOURCE_BLOB", err.Error()
		case "audit_unavailable":
			status, code, detail = http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Candidate source blob audit is temporarily unavailable"
		}
	}
	switch {
	case errors.Is(err, deployment.ErrCandidateUnavailable),
		errors.Is(err, project.ErrCandidateSourceUnavailable):
		status, code, detail = http.StatusServiceUnavailable, "CANDIDATE_SERVICE_UNAVAILABLE", "Candidate service is unavailable"
	case errors.Is(err, deployment.ErrCandidateNotFound):
		status, code, detail = http.StatusNotFound, "CANDIDATE_NOT_FOUND", "Candidate not found"
	case errors.Is(err, deployment.ErrCandidateConflict), errors.Is(err, project.ErrCandidateSourceConflict):
		status, code, detail = http.StatusConflict, "CANDIDATE_CONFLICT", err.Error()
	case errors.Is(err, deployment.ErrProjectClaimConflict):
		status, code, detail = http.StatusConflict, "CANDIDATE_CONFLICT", err.Error()
	case errors.Is(err, deployment.ErrCandidateQuota):
		status, code, detail = http.StatusTooManyRequests, "CANDIDATE_QUOTA_EXCEEDED", "Candidate quota exceeded"
	case errors.Is(err, deployment.ErrCandidateInvalid), errors.Is(err, project.ErrCandidateSourceInvalid):
		status, code, detail = http.StatusUnprocessableEntity, "INVALID_CANDIDATE", err.Error()
	case errors.Is(err, deployment.ErrProjectClaimInvalid):
		status, code, detail = http.StatusUnprocessableEntity, "INVALID_CANDIDATE", err.Error()
	}
	apitransport.WriteProblem(w, r, status, code, detail, nil)
}

func (m *Module) writeCandidateCommandFailure(w http.ResponseWriter, r *http.Request, operationID deploymentgen.GenCommandOperationID, err error) {
	err = classifyCandidateFailure(err)
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, m.logger, operationID, deploymentgen.GetAPIGenCommandFailureContracts, err)
}

func classifyCandidateFailure(err error) error {
	switch {
	case errors.Is(err, deployment.ErrProjectClaimConflict):
		return apigenfailure.Wrap("candidate_conflict", err)
	case errors.Is(err, deployment.ErrProjectClaimInvalid):
		return apigenfailure.Wrap("candidate_invalid", err)
	case errors.Is(err, project.ErrCandidateSourceUnavailable):
		return apigenfailure.Wrap("candidate_unavailable", err)
	case errors.Is(err, project.ErrCandidateSourceConflict):
		return apigenfailure.Wrap("candidate_conflict", err)
	case errors.Is(err, project.ErrCandidateSourceInvalid):
		return apigenfailure.Wrap("candidate_invalid", err)
	default:
		return err
	}
}
