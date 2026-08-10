package module

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	deploymentui "github.com/flidai/leapview/internal/deployment/ui"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/release"
)

func (m *Module) ResolveOwnedCandidate(ctx context.Context, candidateID, principalID string) (Candidate, error) {
	if m == nil || m.candidates == nil {
		return Candidate{}, deployment.ErrCandidateUnavailable
	}
	return m.candidates.GetOwned(
		ctx,
		strings.TrimSpace(candidateID),
		strings.TrimSpace(principalID),
	)
}

func (m *Module) ServeCandidatePreview(
	w http.ResponseWriter,
	r *http.Request,
	candidateID, principalID string,
	layout webpage.Provider,
) {
	if m == nil || m.candidates == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return
	}
	candidate, err := m.candidates.GetOwned(r.Context(), strings.TrimSpace(candidateID), strings.TrimSpace(principalID))
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

func (m *Module) StartProjectCandidate(w http.ResponseWriter, r *http.Request, project, _ string) {
	var body deploymentapi.CandidateStartRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	principalID, ok := m.candidatePrincipalID(w, r)
	if !ok {
		return
	}
	startRequest := deployment.StartCandidateRequest{
		ProjectID: project, OwnerID: principalID, ArtifactDigest: body.ArtifactDigest,
	}
	if body.CandidateKey != nil {
		startRequest.Key = *body.CandidateKey
	}
	result, err := m.candidates.Start(r.Context(), startRequest)
	if err != nil {
		writeCandidateAPIError(w, r, err)
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
	candidate, err := m.candidates.Review(
		r.Context(),
		project,
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
	principalID, ok := m.candidatePrincipalID(w, r)
	if !ok {
		return
	}
	candidate, err := m.candidates.ReplaceArtifact(r.Context(), deployment.CandidateScope{
		ProjectID: project, CandidateID: candidateID, OwnerID: principalID,
	}, body.ExpectedArtifactDigest, body.ArtifactDigest)
	if err != nil {
		writeCandidateAPIError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
}

func (m *Module) RetryProjectCandidate(w http.ResponseWriter, r *http.Request, project, candidateID, _ string) {
	principalID, ok := m.candidatePrincipalID(w, r)
	if !ok {
		return
	}
	candidate, err := m.candidates.Retry(r.Context(), deployment.CandidateScope{
		ProjectID: project, CandidateID: candidateID, OwnerID: principalID,
	})
	if err != nil {
		writeCandidateAPIError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, m.candidateResponse(candidate, false))
}

func (m *Module) CancelProjectCandidate(w http.ResponseWriter, r *http.Request, project, candidateID, _ string) {
	principalID, ok := m.candidatePrincipalID(w, r)
	if !ok {
		return
	}
	candidate, err := m.candidates.Cancel(r.Context(), deployment.CandidateScope{
		ProjectID: project, CandidateID: candidateID, OwnerID: principalID,
	})
	if err != nil {
		writeCandidateAPIError(w, r, err)
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
	principalID, ok := m.candidatePrincipalID(w, r)
	if !ok {
		return
	}
	candidate, err := m.candidates.CancelActive(
		r.Context(),
		project,
		principalID,
		candidateKey,
	)
	if err != nil {
		writeCandidateAPIError(w, r, err)
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
	candidate, ok := m.ownedCandidate(w, r, project, candidateID)
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
		writeCandidateAPIError(w, r, deployment.ErrCandidateConflict)
		return
	}
	if m.api.Releases == nil {
		apitransport.WriteProblem(
			w,
			r,
			http.StatusServiceUnavailable,
			"DEPLOYMENT_SERVICE_UNAVAILABLE",
			"Candidate publication is unavailable",
			nil,
		)
		return
	}
	published, err := m.api.Releases.PublishCandidate(
		r.Context(),
		release.PublishCandidateInput{
			ProjectID: project, CandidateID: candidate.ID,
			CandidateRevision: candidate.Revision,
			ProvenanceDigest:  candidate.ProvenanceDigest,
			TargetID:          candidate.TargetID,
			Environment:       candidate.Environment,
			IdempotencyKey:    idempotencyKey,
			CreatedBy:         candidate.OwnerID,
		},
	)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	if m.candidateRuntimeLifecycle != nil {
		m.candidateRuntimeLifecycle.RetireCandidate(candidate.ID)
	}
	m.createDeployment(
		w,
		r,
		string(deploymentgen.GenOperationPublishProjectCandidate),
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
	candidate, err := m.candidates.Get(r.Context(), deployment.CandidateScope{
		ProjectID: project, CandidateID: candidateID, OwnerID: principalID,
	})
	if err != nil {
		writeCandidateAPIError(w, r, err)
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

func (m *Module) candidateResponse(candidate deployment.Candidate, resumed bool) deploymentapi.CandidateResponse {
	response := deploymentapi.CandidateResponse{
		ID: candidate.ID, ProjectID: candidate.ProjectID, CandidateKey: candidate.Key,
		TargetID:    candidate.TargetID,
		Environment: candidate.Environment, OwnerID: candidate.OwnerID, BaseGeneration: candidate.BaseGeneration,
		ArtifactDigest: candidate.ArtifactDigest, Status: string(candidate.Status),
		PreviewURL: m.candidates.PreviewURL(candidate.ID), ExpiresAt: candidate.ExpiresAt.UTC().Format(time.RFC3339Nano),
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
	switch {
	case errors.Is(err, deployment.ErrCandidateUnavailable),
		errors.Is(err, project.ErrCandidateSourceUnavailable):
		status, code, detail = http.StatusServiceUnavailable, "CANDIDATE_SERVICE_UNAVAILABLE", "Candidate service is unavailable"
	case errors.Is(err, deployment.ErrCandidateNotFound):
		status, code, detail = http.StatusNotFound, "CANDIDATE_NOT_FOUND", "Candidate not found"
	case errors.Is(err, deployment.ErrCandidateConflict), errors.Is(err, project.ErrCandidateSourceConflict):
		status, code, detail = http.StatusConflict, "CANDIDATE_CONFLICT", err.Error()
	case errors.Is(err, deployment.ErrCandidateQuota):
		status, code, detail = http.StatusTooManyRequests, "CANDIDATE_QUOTA_EXCEEDED", "Candidate quota exceeded"
	case errors.Is(err, deployment.ErrCandidateInvalid), errors.Is(err, project.ErrCandidateSourceInvalid):
		status, code, detail = http.StatusUnprocessableEntity, "INVALID_CANDIDATE", err.Error()
	}
	apitransport.WriteProblem(w, r, status, code, detail, nil)
}
