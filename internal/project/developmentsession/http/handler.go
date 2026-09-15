// Package http exposes the authenticated development-session pointer API.
// These routes are deliberately hand-written: they are a small orchestration
// surface and do not create a second candidate or publication contract.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/project/developmentsession"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type Config struct {
	Store             developmentsession.Store
	CurrentPrincipal  func(*http.Request) (string, bool)
	ResolveProjectID  func(context.Context) (projectgraph.ResourceID, error)
	CheckoutID        string
	WorktreeID        string
	TargetID          string
	Environment       string
	Enabled           bool
	ValidateCandidate CandidateValidator
}

// CandidateValidation is proof returned by the candidate authority before a
// pointer can be advanced to a last-valid candidate.
type CandidateValidation struct {
	Identity    developmentsession.Identity
	OwnerID     string
	ProjectID   projectgraph.ResourceID
	TargetID    string
	Environment string
	Qualified   bool
	// Expired is returned by the candidate authority when the immutable
	// candidate was retired or crossed its governance expiry. Stable reads
	// must not follow such a pointer, even if the durable record survived it.
	Expired bool
}

type CandidateValidator func(context.Context, string, string, projectgraph.ResourceID, string, string) (CandidateValidation, error)

type Handler struct {
	config  Config
	mu      sync.Mutex
	streams map[string]map[chan developmentsession.Record]struct{}
}

func New(config Config) *Handler {
	return &Handler{config: config, streams: make(map[string]map[chan developmentsession.Record]struct{})}
}

// Mount registers only authenticated, owner-scoped routes. The caller must
// place the router under the shared bearer/API protocol middleware.
func (h *Handler) Mount(r chi.Router) {
	if h == nil || !h.config.Enabled || h.config.Store == nil {
		return
	}
	r.Route("/api/v1/projects/{project}/targets/{target}/development-session", func(session chi.Router) {
		session.Get("/", h.resolve)
		session.Get("/candidate", h.candidate)
		session.Get("/candidate/preview", h.preview)
		session.Get("/events", h.events)
		session.Put("/", h.update)
		session.Patch("/", h.update)
	})
}

type updateRequest struct {
	Revision    int64                           `json:"revision"`
	Attempted   developmentsession.Identity     `json:"attempted"`
	LastValid   developmentsession.Identity     `json:"lastValid"`
	Diagnostics []developmentsession.Diagnostic `json:"diagnostics"`
}

type handoff struct {
	SessionID      string `json:"sessionId"`
	PreviewURL     string `json:"previewUrl"`
	ProjectID      string `json:"projectId"`
	TargetID       string `json:"targetId"`
	Environment    string `json:"environment"`
	CandidateID    string `json:"candidateId"`
	ArtifactDigest string `json:"artifactDigest"`
	GraphDigest    string `json:"graphDigest"`
	Revision       int64  `json:"revision"`
}

func (h *Handler) scope(r *http.Request) (developmentsession.Key, bool) {
	if h == nil || h.config.Store == nil || !h.config.Enabled {
		return developmentsession.Key{}, false
	}
	if h.config.CurrentPrincipal == nil {
		return developmentsession.Key{}, false
	}
	owner, ok := h.config.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(owner) == "" {
		return developmentsession.Key{}, false
	}
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(chi.URLParam(r, "project")))
	if err != nil {
		return developmentsession.Key{}, false
	}
	targetID := strings.TrimSpace(chi.URLParam(r, "target"))
	if targetID == "" || targetID != strings.TrimSpace(h.config.TargetID) {
		return developmentsession.Key{}, false
	}
	if h.config.ResolveProjectID != nil {
		active, resolveErr := h.config.ResolveProjectID(r.Context())
		if resolveErr != nil || active != projectID {
			return developmentsession.Key{}, false
		}
	}
	key := developmentsession.Key{OwnerID: owner, CheckoutID: h.config.CheckoutID, WorktreeID: h.config.WorktreeID, ProjectID: projectID, TargetID: targetID, Environment: h.config.Environment}
	if err := key.Validate(); err != nil {
		return developmentsession.Key{}, false
	}
	return key, true
}

func (h *Handler) withScope(w http.ResponseWriter, r *http.Request) (developmentsession.Key, bool) {
	key, ok := h.scope(r)
	if ok {
		return key, true
	}
	if h.config.CurrentPrincipal == nil {
		transport.WriteProblem(w, r, http.StatusServiceUnavailable, "DEVELOPMENT_SESSION_UNAVAILABLE", "Development sessions are unavailable", nil)
	} else {
		_, principalOK := h.config.CurrentPrincipal(r)
		if !principalOK {
			transport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Authentication is required", nil)
		} else {
			transport.WriteProblem(w, r, http.StatusForbidden, "PROJECT_SCOPE_MISMATCH", "The requested project is not the active project", nil)
		}
	}
	return developmentsession.Key{}, false
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	key, ok := h.withScope(w, r)
	if !ok {
		return
	}
	record, ok := h.resolveRecord(w, r, key)
	if !ok {
		return
	}
	transport.WriteJSON(w, http.StatusOK, record)
}

func (h *Handler) candidate(w http.ResponseWriter, r *http.Request) {
	key, ok := h.withScope(w, r)
	if !ok {
		return
	}
	record, ok := h.resolveRecord(w, r, key)
	if !ok {
		return
	}
	if record.LastValid.CandidateID == "" {
		transport.WriteProblem(w, r, http.StatusNotFound, "NO_VALID_CANDIDATE", "This development session has no valid candidate", nil)
		return
	}
	if record.LastValid.PreviewURL == "" {
		transport.WriteProblem(w, r, http.StatusServiceUnavailable, "CANDIDATE_URL_UNAVAILABLE", "The candidate authority did not provide an exact preview URL", nil)
		return
	}
	transport.WriteJSON(w, http.StatusOK, handoff{SessionID: record.ID, PreviewURL: record.LastValid.PreviewURL, ProjectID: key.ProjectID.String(), TargetID: key.TargetID, Environment: key.Environment, CandidateID: record.LastValid.CandidateID, ArtifactDigest: record.LastValid.ArtifactDigest, GraphDigest: record.LastValid.GraphDigest, Revision: record.Revision})
}

// preview is a navigation convenience only. It resolves the durable pointer
// once and redirects to the exact candidate URL; it never rewrites that URL or
// starts browser dashboard propagation.
func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	key, ok := h.withScope(w, r)
	if !ok {
		return
	}
	record, ok := h.resolveRecord(w, r, key)
	if !ok {
		return
	}
	if record.LastValid.CandidateID == "" || record.LastValid.PreviewURL == "" {
		transport.WriteProblem(w, r, http.StatusNotFound, "NO_VALID_CANDIDATE", "This development session has no valid candidate", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, record.LastValid.PreviewURL, http.StatusTemporaryRedirect)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	key, ok := h.withScope(w, r)
	if !ok {
		return
	}
	var body updateRequest
	if err := transport.DecodeBody(w, r, &body); err != nil {
		transport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_DEVELOPMENT_SESSION", "The development session update is invalid", nil)
		return
	}
	if body.LastValid.CandidateID != "" {
		if h.config.ValidateCandidate == nil {
			transport.WriteProblem(w, r, http.StatusServiceUnavailable, "CANDIDATE_AUTHORITY_UNAVAILABLE", "The candidate authority is unavailable", nil)
			return
		}
		proof, err := h.config.ValidateCandidate(r.Context(), key.OwnerID, body.LastValid.CandidateID, key.ProjectID, key.TargetID, key.Environment)
		if err != nil {
			transport.WriteProblem(w, r, http.StatusConflict, "CANDIDATE_NOT_QUALIFIED", "The candidate is not qualified for this development session", nil)
			return
		}
		if !proof.Qualified || proof.OwnerID != key.OwnerID || proof.ProjectID != key.ProjectID || proof.TargetID != key.TargetID || proof.Environment != key.Environment || proof.Identity.CandidateID != body.LastValid.CandidateID || proof.Identity.ArtifactDigest != body.LastValid.ArtifactDigest || proof.Identity.GraphDigest == "" || body.LastValid.GraphDigest != proof.Identity.GraphDigest || (body.LastValid.PreviewURL != "" && body.LastValid.PreviewURL != proof.Identity.PreviewURL) {
			transport.WriteProblem(w, r, http.StatusConflict, "CANDIDATE_NOT_QUALIFIED", "The candidate identity does not match the authoritative candidate", nil)
			return
		}
		body.LastValid.PreviewURL = proof.Identity.PreviewURL
	}
	record := developmentsession.Record{ID: key.ID(), Key: key, Attempted: body.Attempted, LastValid: body.LastValid, Diagnostics: body.Diagnostics}
	updated, err := h.config.Store.Save(r.Context(), record, body.Revision)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	h.publish(updated)
	transport.WriteJSON(w, http.StatusOK, updated)
}

// ResolveCandidate is the app composition hook for stable document and
// command routes. It performs the same owner/scope/authority checks as the
// JSON handoff without exposing the Store or allowing a caller to select a
// different candidate.
func (h *Handler) ResolveCandidate(r *http.Request) (CandidateValidation, error) {
	if h == nil || r == nil {
		return CandidateValidation{}, developmentsession.ErrInvalid
	}
	key, ok := h.scope(r)
	if !ok {
		return CandidateValidation{}, developmentsession.ErrOwnerMismatch
	}
	record, err := h.config.Store.Resolve(r.Context(), key)
	if err != nil {
		return CandidateValidation{}, err
	}
	if record.LastValid.CandidateID == "" {
		return CandidateValidation{}, developmentsession.ErrNotFound
	}
	if h.config.ValidateCandidate == nil {
		return CandidateValidation{}, developmentsession.ErrInvalid
	}
	proof, err := h.config.ValidateCandidate(r.Context(), key.OwnerID, record.LastValid.CandidateID, key.ProjectID, key.TargetID, key.Environment)
	if err != nil {
		return CandidateValidation{}, err
	}
	if proof.Expired {
		h.markExpired(r, key, record)
		return proof, nil
	}
	if !proof.Qualified || !sameCandidateIdentity(proof.Identity, record.LastValid) ||
		proof.OwnerID != key.OwnerID || proof.ProjectID != key.ProjectID ||
		proof.TargetID != key.TargetID || proof.Environment != key.Environment {
		return CandidateValidation{}, developmentsession.ErrNotFound
	}
	return proof, nil
}

// MarkCandidateExpired records the terminal observation made by a stable
// delegation. It is idempotent and retains the historical last-valid
// identity for audit/review while making subsequent reads fail closed.
func (h *Handler) MarkCandidateExpired(r *http.Request) {
	if h == nil || r == nil {
		return
	}
	key, ok := h.scope(r)
	if !ok {
		return
	}
	if record, err := h.config.Store.Resolve(r.Context(), key); err == nil {
		h.markExpired(r, key, record)
	}
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	key, ok := h.withScope(w, r)
	if !ok {
		return
	}
	record, ok := h.resolveRecord(w, r, key)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		transport.WriteProblem(w, r, http.StatusServiceUnavailable, "SESSION_EVENTS_UNAVAILABLE", "Development session events are unavailable", nil)
		return
	}
	stream := make(chan developmentsession.Record, 1)
	sessionID := key.ID()
	h.mu.Lock()
	if h.streams == nil {
		h.streams = make(map[string]map[chan developmentsession.Record]struct{})
	}
	if h.streams[sessionID] == nil {
		h.streams[sessionID] = make(map[chan developmentsession.Record]struct{})
	}
	h.streams[sessionID][stream] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.streams[sessionID], stream)
		if len(h.streams[sessionID]) == 0 {
			delete(h.streams, sessionID)
		}
		h.mu.Unlock()
	}()
	// Close the resolve/subscribe race: an update committed after the first
	// read but before registration is not in this channel, so reread the
	// durable authority after registration. If it advanced, replay the newer
	// revision and reconnect instead of waiting forever on stale state.
	initialRevision := record.Revision
	latest, latestOK := h.resolveRecord(w, r, key)
	if !latestOK {
		return
	}
	if latest.Revision > record.Revision {
		record = latest
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Content-Type", "text/event-stream")
	writeSessionEvent(w, record)
	flusher.Flush()
	if record.Revision > initialRevision {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case updated, open := <-stream:
			if !open {
				return
			}
			writeSessionEvent(w, updated)
			flusher.Flush()
			return // reconnect resolves the newest revision after a CAS
		}
	}
}

// Events is exported for the browser-authenticated stable route. The API
// mount also exposes the same endpoint to bearer clients; both paths share the
// exact owner/session and replay semantics.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	h.events(w, r)
}

func writeSessionEvent(w http.ResponseWriter, record developmentsession.Record) {
	payload, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("event: development-session\ndata: "))
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n\n"))
}

func (h *Handler) publish(record developmentsession.Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	streams := h.streams[record.ID]
	for stream := range streams {
		select {
		case stream <- record:
		default:
		}
		close(stream)
		delete(streams, stream)
	}
	if len(streams) == 0 {
		delete(h.streams, record.ID)
	}
}

// resolveRecord revalidates the durable pointer against the candidate
// authority on every stable read. A PostgreSQL row can outlive its candidate;
// it is never safe to redirect from stale orchestration evidence alone.
func (h *Handler) resolveRecord(w http.ResponseWriter, r *http.Request, key developmentsession.Key) (developmentsession.Record, bool) {
	record, err := h.config.Store.Resolve(r.Context(), key)
	if err != nil {
		writeStoreError(w, r, err)
		return developmentsession.Record{}, false
	}
	if record.LastValid.CandidateID == "" {
		return record, true
	}
	if h.config.ValidateCandidate == nil {
		transport.WriteProblem(w, r, http.StatusServiceUnavailable, "CANDIDATE_AUTHORITY_UNAVAILABLE", "The candidate authority is unavailable", nil)
		return developmentsession.Record{}, false
	}
	proof, err := h.config.ValidateCandidate(r.Context(), key.OwnerID, record.LastValid.CandidateID, key.ProjectID, key.TargetID, key.Environment)
	if err != nil {
		transport.WriteProblem(w, r, http.StatusServiceUnavailable, "CANDIDATE_AUTHORITY_UNAVAILABLE", "The candidate authority is unavailable", nil)
		return developmentsession.Record{}, false
	}
	if proof.Expired {
		h.markExpired(r, key, record)
		transport.WriteProblem(w, r, http.StatusGone, "CANDIDATE_EXPIRED", "The development session candidate has expired or been retired", nil)
		return developmentsession.Record{}, false
	}
	if !proof.Qualified || !sameCandidateIdentity(proof.Identity, record.LastValid) ||
		proof.OwnerID != key.OwnerID || proof.ProjectID != key.ProjectID ||
		proof.TargetID != key.TargetID || proof.Environment != key.Environment {
		transport.WriteProblem(w, r, http.StatusGone, "CANDIDATE_UNAVAILABLE", "The development session candidate is no longer available", nil)
		return developmentsession.Record{}, false
	}
	return record, true
}

func sameCandidateIdentity(authority, stored developmentsession.Identity) bool {
	return authority.CandidateID == stored.CandidateID &&
		authority.ArtifactDigest == stored.ArtifactDigest &&
		authority.GraphDigest != "" &&
		authority.GraphDigest == stored.GraphDigest &&
		authority.PreviewURL == stored.PreviewURL
}

func (h *Handler) markExpired(r *http.Request, key developmentsession.Key, record developmentsession.Record) {
	for tries := 0; tries < 2; tries++ {
		if hasDiagnostic(record, "CANDIDATE_EXPIRED") {
			return
		}
		record.Diagnostics = append(record.Diagnostics, developmentsession.Diagnostic{
			Code: "CANDIDATE_EXPIRED", Message: "The selected candidate expired or was retired; no stable preview redirect was followed",
		})
		if updated, err := h.config.Store.Save(r.Context(), record, record.Revision); err == nil {
			h.publish(updated)
			return
		} else if !errors.Is(err, developmentsession.ErrConflict) {
			return
		}
		latest, err := h.config.Store.Resolve(r.Context(), key)
		if err != nil {
			return
		}
		record = latest
	}
}

func hasDiagnostic(record developmentsession.Record, code string) bool {
	for _, diagnostic := range record.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, developmentsession.ErrNotFound):
		transport.WriteProblem(w, r, http.StatusNotFound, "DEVELOPMENT_SESSION_NOT_FOUND", "The development session does not exist", nil)
	case errors.Is(err, developmentsession.ErrConflict):
		transport.WriteProblem(w, r, http.StatusConflict, "DEVELOPMENT_SESSION_CONFLICT", "The development session changed; resolve it again", nil)
	case errors.Is(err, developmentsession.ErrOwnerMismatch):
		transport.WriteProblem(w, r, http.StatusForbidden, "DEVELOPMENT_SESSION_OWNER_MISMATCH", "The development session is not owned by the authenticated principal", nil)
	default:
		transport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_DEVELOPMENT_SESSION", "The development session is invalid", nil)
	}
}
