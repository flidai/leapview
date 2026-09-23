package module

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// SavedExplorationAPIGenConfig is the feature-owned boundary between the
// generated REST transport and the saved-exploration application service.
// Actor, IDs, fingerprints, and evidence are all derived here or below the
// transport boundary; none are accepted from request bodies.
type SavedExplorationAPIGenConfig struct {
	Service          SavedExplorationService
	CurrentPrincipal func(*http.Request) (string, bool)
	// ReplayContext installs the canonical authenticated principal (and any
	// credential attenuation) before the read-only replay authorization runs.
	// The public protocol invokes replay authorization before APIGen's normal
	// Protect middleware, so the feature cannot assume that request context has
	// already been populated.
	ReplayContext func(context.Context, *http.Request, string) (context.Context, bool)
}

// SavedExplorationService keeps the generated transport independent from the
// concrete application implementation, which also makes transport behavior
// testable without constructing a runtime lease.
type SavedExplorationService interface {
	AuthorizeMutationReplay(context.Context, savedapplication.MutationReplayAuthorizationRequest) (bool, error)
	Create(context.Context, saved.CreateRequest) (saved.MutationResult, error)
	Reopen(context.Context, saved.ReopenRequest) (saved.ReopenResult, error)
	List(context.Context, saved.ListRequest) ([]saved.Lifecycle, error)
	ListPage(context.Context, saved.ListRequest) (saved.ListPage, error)
	UpdateVersion(context.Context, saved.UpdateVersionRequest) (saved.MutationResult, error)
	Duplicate(context.Context, saved.DuplicateRequest) (saved.MutationResult, error)
	Archive(context.Context, saved.ArchiveRequest) (saved.MutationResult, error)
}

type savedExplorationAPIHandler struct{ config SavedExplorationAPIGenConfig }

type savedExplorationReplayOperation struct {
	action    saved.MutationAction
	project   string
	target    string
	operation analyticsgen.GenCommandOperationID
}

// authorizeMutationReplay reconstructs the exact durable mutation identity
// from the REST request and asks the application service for a current,
// read-only authorization decision. It intentionally does not call any
// mutation method or return the stored response itself; the protocol owns
// replaying the captured response after this check succeeds.
func (h savedExplorationAPIHandler) authorizeMutationReplay(r *http.Request) bool {
	if r == nil || h.config.Service == nil || h.config.CurrentPrincipal == nil {
		return false
	}
	op, ok := savedExplorationReplayRoute(r.Method, r.URL.Path)
	if !ok {
		return false
	}
	actor, ok := h.config.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(actor) == "" {
		return false
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return false
	}
	request, err := h.savedMutationReplayAuthorizationRequest(r, op, actor, key)
	if err != nil {
		return false
	}
	ctx := r.Context()
	if h.config.ReplayContext == nil {
		// A real service requires the canonical actor context. A missing seam
		// therefore fails closed, while lightweight service stubs remain useful
		// in direct handler tests by providing ReplayContext explicitly.
		return false
	}
	ctx, ok = h.config.ReplayContext(ctx, r, actor)
	if !ok {
		return false
	}
	authorized, err := h.config.Service.AuthorizeMutationReplay(ctx, request)
	return err == nil && authorized
}

// IsSavedExplorationMutationRequest reports whether a request is one of the
// durable REST saved-exploration mutations. It is used by composition to
// narrow the protocol's replay callback without changing replay behavior for
// unrelated generated commands.
func IsSavedExplorationMutationRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	_, ok := savedExplorationReplayRoute(r.Method, r.URL.Path)
	return ok
}

// AuthorizeSavedExplorationMutationReplay is the composition-facing replay
// seam for REST saved-exploration mutations.
func AuthorizeSavedExplorationMutationReplay(config SavedExplorationAPIGenConfig, r *http.Request) bool {
	return savedExplorationAPIHandler{config: config}.authorizeMutationReplay(r)
}

func savedExplorationReplayRoute(method, path string) (savedExplorationReplayOperation, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "projects" || parts[4] != "saved-explorations" || strings.TrimSpace(parts[3]) == "" {
		return savedExplorationReplayOperation{}, false
	}
	op := savedExplorationReplayOperation{project: parts[3]}
	switch {
	case method == http.MethodPost && len(parts) == 5:
		op.action, op.operation = saved.MutationActionCreate, analyticsgen.GenCommandOperationCreateSavedExploration()
	case method == http.MethodPatch && len(parts) == 6 && strings.TrimSpace(parts[5]) != "":
		op.action, op.target, op.operation = saved.MutationActionUpdate, parts[5], analyticsgen.GenCommandOperationUpdateSavedExploration()
	case method == http.MethodPost && len(parts) == 7 && strings.TrimSpace(parts[5]) != "" && parts[6] == "duplicate":
		op.action, op.target, op.operation = saved.MutationActionDuplicate, parts[5], analyticsgen.GenCommandOperationDuplicateSavedExploration()
	case method == http.MethodPost && len(parts) == 7 && strings.TrimSpace(parts[5]) != "" && parts[6] == "archive":
		op.action, op.target, op.operation = saved.MutationActionArchive, parts[5], analyticsgen.GenCommandOperationArchiveSavedExploration()
	default:
		return savedExplorationReplayOperation{}, false
	}
	return op, true
}

func (h savedExplorationAPIHandler) savedMutationReplayAuthorizationRequest(r *http.Request, op savedExplorationReplayOperation, actor, key string) (savedapplication.MutationReplayAuthorizationRequest, error) {
	projectID := projectgraph.ResourceID(op.project)
	request := savedapplication.MutationReplayAuthorizationRequest{ProjectID: projectID, ActorID: actor, IdempotencyKey: key, Action: op.action}
	if op.target != "" {
		request.TargetID = saved.ExplorationID(op.target)
		if err := request.TargetID.Validate(); err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
	}
	switch op.action {
	case saved.MutationActionCreate:
		var body analyticsgen.GenCreateSavedExplorationBody
		if err := decodeSavedReplayBody(r, &body); err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		id, err := stableSavedID("exploration-", op.project, actor, key, "create")
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		request.TargetID = saved.ExplorationID(id)
		mutation := saved.CreateRequest{ProjectID: projectID, ID: request.TargetID, ActorID: actor, Title: body.Title, Slug: bodySlug(body.Slug, body.Title, id), Visibility: saved.Visibility(body.Visibility), Spec: body.Spec}
		request.Fingerprint, err = savedapplication.FingerprintCreate(mutation)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
	case saved.MutationActionUpdate:
		var body analyticsgen.GenUpdateSavedExplorationBody
		if err := decodeSavedReplayBody(r, &body); err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		expected, err := parseRevisionToken(strings.TrimSpace(r.Header.Get("If-Match")))
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		mutation := saved.UpdateVersionRequest{ProjectID: projectID, ID: request.TargetID, ActorID: actor, ExpectedRevision: expected, Title: body.Title, Slug: body.Slug, Visibility: saved.Visibility(body.Visibility), Spec: body.Spec}
		request.Fingerprint, err = savedapplication.FingerprintUpdate(mutation)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
	case saved.MutationActionDuplicate:
		var body analyticsgen.GenDuplicateSavedExplorationBody
		if err := decodeSavedReplayBody(r, &body); err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		expected, err := parseRevisionToken(strings.TrimSpace(r.Header.Get("If-Match")))
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		title := "Copy of " + op.target
		if body.Title != nil && strings.TrimSpace(*body.Title) != "" {
			title = *body.Title
		}
		visibility := saved.VisibilityPrivate
		if body.Visibility != nil {
			visibility = saved.Visibility(*body.Visibility)
		}
		id, err := stableSavedID("exploration-", op.project, actor, key, "duplicate:"+op.target)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		mutation := saved.DuplicateRequest{ProjectID: projectID, SourceID: request.TargetID, ExpectedSourceRevision: expected, ID: saved.ExplorationID(id), ActorID: actor, Title: title, Slug: bodySlug(body.Slug, title, id), Visibility: visibility}
		request.Fingerprint, err = savedapplication.FingerprintDuplicate(mutation)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
	case saved.MutationActionArchive:
		expected, err := parseRevisionToken(strings.TrimSpace(r.Header.Get("If-Match")))
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		request.Fingerprint, err = savedapplication.FingerprintArchive(saved.ArchiveRequest{ProjectID: projectID, ID: request.TargetID, ActorID: actor, ExpectedRevision: expected})
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
	default:
		return savedapplication.MutationReplayAuthorizationRequest{}, saved.ErrInvalid
	}
	return request, nil
}

func decodeSavedReplayBody(r *http.Request, target any) error {
	if r == nil || r.Body == nil {
		return errors.New("saved exploration request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20+1))
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > 16<<20 {
		return errors.New("saved exploration request body is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("saved exploration request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func (h savedExplorationAPIHandler) List(w http.ResponseWriter, r *http.Request, project string, params analyticsgen.GenListSavedExplorationsParams) {
	actor, ok := h.principal(w, r)
	if !ok {
		return
	}
	if params.Limit != nil && (*params.Limit < 1 || *params.Limit > saved.MaxListLimit) {
		// The generated router validates the TypeSpec scalar when mounted, but
		// this direct handler is also an API boundary in tests and adapters.
		// Keep an explicitly supplied zero distinct from the internal/browser
		// zero-limit convention, which means "read until exhaustion".
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid query parameter.", nil)
		return
	}
	if h.config.Service == nil {
		h.unavailable(w, r)
		return
	}
	includeArchived := params.IncludeArchived != nil && *params.IncludeArchived
	limit := saved.DefaultListLimit
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	pageToken := ""
	if params.PageToken != nil {
		pageToken = *params.PageToken
	}
	page, err := h.config.Service.ListPage(r.Context(), saved.ListRequest{ProjectID: projectgraph.ResourceID(project), ActorID: actor, IncludeArchived: includeArchived, Limit: limit, PageToken: pageToken})
	if err != nil {
		// List query shape/cursor errors are transport-level bad requests. Keep
		// the existing list contract's 400 behavior instead of classifying the
		// same ErrInvalid as a mutation-style 422.
		if errors.Is(err, saved.ErrInvalid) {
			apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_SAVED_EXPLORATION", "The saved-exploration query is invalid.", nil)
			return
		}
		writeSavedExplorationQueryFailure(w, r, "listSavedExplorations", err)
		return
	}
	response := analyticsgen.SavedExplorationListResponse{Items: make([]analyticsgen.SavedExplorationSummaryResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, savedExplorationSummary(item))
	}
	if page.NextCursor != "" {
		response.Page.NextCursor = &page.NextCursor
	}
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (h savedExplorationAPIHandler) Create(w http.ResponseWriter, r *http.Request, project string, headers analyticsgen.GenCreateSavedExplorationHeaders) {
	actor, ok := h.principal(w, r)
	if !ok {
		return
	}
	if h.config.Service == nil {
		h.unavailable(w, r)
		return
	}
	var body analyticsgen.GenCreateSavedExplorationBody
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Request body is invalid.", nil)
		return
	}
	id, err := stableSavedID("exploration-", project, actor, headers.IdempotencyKey, "create")
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationCreateSavedExploration(), err)
		return
	}
	slug := bodySlug(body.Slug, body.Title, id)
	request := saved.CreateRequest{ProjectID: projectgraph.ResourceID(project), ID: saved.ExplorationID(id), ActorID: actor, Title: body.Title, Slug: slug, Visibility: saved.Visibility(body.Visibility), Spec: body.Spec}
	fingerprint, err := savedapplication.FingerprintCreate(request)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationCreateSavedExploration(), err)
		return
	}
	evidence, err := h.evidence(r, actor, saved.MutationActionCreate, headers.IdempotencyKey, fingerprint)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationCreateSavedExploration(), err)
		return
	}
	request.Evidence = evidence
	var result saved.MutationResult
	if err := executeSavedMutation(r.Context(), analyticsgen.GenCommandOperationCreateSavedExploration(), func(ctx context.Context) error {
		var err error
		result, err = h.config.Service.Create(ctx, request)
		return err
	}, nil); err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationCreateSavedExploration(), err)
		return
	}
	if result.Revision == nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationCreateSavedExploration(), saved.ErrInvalid)
		return
	}
	response, err := savedExplorationResponse(result.Lifecycle, *result.Revision)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationCreateSavedExploration(), err)
		return
	}
	w.Header().Set("Location", savedExplorationLocation(project, result.Lifecycle.ID.String()))
	w.Header().Set("ETag", revisionETag(result.AppliedRevision))
	apitransport.WriteJSON(w, http.StatusCreated, response)
}

func (h savedExplorationAPIHandler) Get(w http.ResponseWriter, r *http.Request, project, exploration string) {
	actor, ok := h.principal(w, r)
	if !ok {
		return
	}
	if h.config.Service == nil {
		h.unavailable(w, r)
		return
	}
	opened, err := h.config.Service.Reopen(r.Context(), saved.ReopenRequest{ProjectID: projectgraph.ResourceID(project), ID: saved.ExplorationID(exploration), ActorID: actor})
	if err != nil {
		writeSavedExplorationQueryFailure(w, r, "getSavedExploration", err)
		return
	}
	w.Header().Set("ETag", revisionETag(opened.Revision.Token()))
	apitransport.WriteJSON(w, http.StatusOK, savedExplorationWorkingCopy(opened))
}

func (h savedExplorationAPIHandler) Update(w http.ResponseWriter, r *http.Request, project, exploration string, headers analyticsgen.GenUpdateSavedExplorationHeaders) {
	actor, ok := h.principal(w, r)
	if !ok {
		return
	}
	if h.config.Service == nil {
		h.unavailable(w, r)
		return
	}
	expected, err := parseRevisionToken(headers.IfMatch)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationUpdateSavedExploration(), apigenfailure.Wrap("precondition", err))
		return
	}
	var body analyticsgen.GenUpdateSavedExplorationBody
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Request body is invalid.", nil)
		return
	}
	request := saved.UpdateVersionRequest{ProjectID: projectgraph.ResourceID(project), ID: saved.ExplorationID(exploration), ActorID: actor, ExpectedRevision: expected, Title: body.Title, Slug: body.Slug, Visibility: saved.Visibility(body.Visibility), Spec: body.Spec}
	fingerprint, err := savedapplication.FingerprintUpdate(request)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationUpdateSavedExploration(), err)
		return
	}
	evidence, err := h.evidence(r, actor, saved.MutationActionUpdate, headers.IdempotencyKey, fingerprint)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationUpdateSavedExploration(), err)
		return
	}
	request.Evidence = evidence
	var result saved.MutationResult
	if err := executeSavedMutation(r.Context(), analyticsgen.GenCommandOperationUpdateSavedExploration(), func(ctx context.Context) error {
		var err error
		result, err = h.config.Service.UpdateVersion(ctx, request)
		return err
	}, func(ctx context.Context, executor *apigencommand.Executor) error {
		// The repository's compare-and-swap has already accepted ExpectedRevision.
		// Attest the revision observed by that CAS to the generated transport
		// guard; there is deliberately no pre-read between parsing If-Match and
		// mutation.
		canonicalExpected, err := apigencommand.RevisionToken(expected)
		if err != nil {
			return err
		}
		canonicalCurrent, err := savedMutationConcurrencyToken(result)
		if err != nil {
			return err
		}
		return analyticsgen.CheckGenUpdateSavedExplorationCommandConcurrency(ctx, executor, canonicalExpected, canonicalCurrent)
	}); err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationUpdateSavedExploration(), err)
		return
	}
	if result.Revision == nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationUpdateSavedExploration(), saved.ErrInvalid)
		return
	}
	response, err := savedExplorationResponse(result.Lifecycle, *result.Revision)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationUpdateSavedExploration(), err)
		return
	}
	w.Header().Set("ETag", revisionETag(result.AppliedRevision))
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func (h savedExplorationAPIHandler) Duplicate(w http.ResponseWriter, r *http.Request, project, exploration string, headers analyticsgen.GenDuplicateSavedExplorationHeaders) {
	actor, ok := h.principal(w, r)
	if !ok {
		return
	}
	if h.config.Service == nil {
		h.unavailable(w, r)
		return
	}
	expected, err := parseRevisionToken(headers.IfMatch)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationDuplicateSavedExploration(), apigenfailure.Wrap("precondition", err))
		return
	}
	var body analyticsgen.GenDuplicateSavedExplorationBody
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Request body is invalid.", nil)
		return
	}
	title := "Copy of " + exploration
	if body.Title != nil && strings.TrimSpace(*body.Title) != "" {
		title = *body.Title
	}
	visibility := saved.VisibilityPrivate
	if body.Visibility != nil {
		visibility = saved.Visibility(*body.Visibility)
	}
	id, err := stableSavedID("exploration-", project, actor, headers.IdempotencyKey, "duplicate:"+exploration)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationDuplicateSavedExploration(), err)
		return
	}
	slug := bodySlug(body.Slug, title, id)
	request := saved.DuplicateRequest{ProjectID: projectgraph.ResourceID(project), SourceID: saved.ExplorationID(exploration), ExpectedSourceRevision: expected, ID: saved.ExplorationID(id), ActorID: actor, Title: title, Slug: slug, Visibility: visibility}
	fingerprint, err := savedapplication.FingerprintDuplicate(request)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationDuplicateSavedExploration(), err)
		return
	}
	evidence, err := h.evidence(r, actor, saved.MutationActionDuplicate, headers.IdempotencyKey, fingerprint)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationDuplicateSavedExploration(), err)
		return
	}
	request.Evidence = evidence
	var result saved.MutationResult
	if err := executeSavedMutation(r.Context(), analyticsgen.GenCommandOperationDuplicateSavedExploration(), func(ctx context.Context) error {
		var err error
		result, err = h.config.Service.Duplicate(ctx, request)
		return err
	}, func(ctx context.Context, executor *apigencommand.Executor) error {
		// Duplicate's source CAS is authoritative. Attest the source revision
		// observed by that CAS without introducing a racy pre-read.
		canonicalExpected, err := apigencommand.RevisionToken(expected)
		if err != nil {
			return err
		}
		canonicalCurrent, err := savedMutationConcurrencyToken(result)
		if err != nil {
			return err
		}
		return analyticsgen.CheckGenDuplicateSavedExplorationCommandConcurrency(ctx, executor, canonicalExpected, canonicalCurrent)
	}); err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationDuplicateSavedExploration(), err)
		return
	}
	if result.Revision == nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationDuplicateSavedExploration(), saved.ErrInvalid)
		return
	}
	response, err := savedExplorationResponse(result.Lifecycle, *result.Revision)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationDuplicateSavedExploration(), err)
		return
	}
	w.Header().Set("Location", savedExplorationLocation(project, result.Lifecycle.ID.String()))
	w.Header().Set("ETag", revisionETag(result.AppliedRevision))
	apitransport.WriteJSON(w, http.StatusCreated, response)
}

func (h savedExplorationAPIHandler) Archive(w http.ResponseWriter, r *http.Request, project, exploration string, headers analyticsgen.GenArchiveSavedExplorationHeaders) {
	actor, ok := h.principal(w, r)
	if !ok {
		return
	}
	if h.config.Service == nil {
		h.unavailable(w, r)
		return
	}
	expected, err := parseRevisionToken(headers.IfMatch)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationArchiveSavedExploration(), apigenfailure.Wrap("precondition", err))
		return
	}
	request := saved.ArchiveRequest{ProjectID: projectgraph.ResourceID(project), ID: saved.ExplorationID(exploration), ActorID: actor, ExpectedRevision: expected}
	fingerprint, err := savedapplication.FingerprintArchive(request)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationArchiveSavedExploration(), err)
		return
	}
	evidence, err := h.evidence(r, actor, saved.MutationActionArchive, headers.IdempotencyKey, fingerprint)
	if err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationArchiveSavedExploration(), err)
		return
	}
	request.Evidence = evidence
	var result saved.MutationResult
	if err := executeSavedMutation(r.Context(), analyticsgen.GenCommandOperationArchiveSavedExploration(), func(ctx context.Context) error {
		var err error
		result, err = h.config.Service.Archive(ctx, request)
		return err
	}, func(ctx context.Context, executor *apigencommand.Executor) error {
		// Archive's lifecycle CAS has accepted the expected revision. Attest the
		// revision observed by that CAS without introducing a racy read.
		canonicalExpected, err := apigencommand.RevisionToken(expected)
		if err != nil {
			return err
		}
		canonicalCurrent, err := savedMutationConcurrencyToken(result)
		if err != nil {
			return err
		}
		return analyticsgen.CheckGenArchiveSavedExplorationCommandConcurrency(ctx, executor, canonicalExpected, canonicalCurrent)
	}); err != nil {
		writeSavedExplorationFailure(w, r, analyticsgen.GenCommandOperationArchiveSavedExploration(), err)
		return
	}
	w.Header().Set("ETag", revisionETag(result.AppliedRevision))
	apitransport.WriteJSON(w, http.StatusOK, savedExplorationSummary(result.Lifecycle))
}

func (h savedExplorationAPIHandler) principal(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.config.CurrentPrincipal == nil {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Authentication is required.", nil)
		return "", false
	}
	actor, ok := h.config.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(actor) == "" {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Authentication is required.", nil)
		return "", false
	}
	return actor, true
}

func (h savedExplorationAPIHandler) unavailable(w http.ResponseWriter, r *http.Request) {
	apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "SAVED_EXPLORATION_UNAVAILABLE", "Saved explorations are temporarily unavailable.", nil)
}

func (h savedExplorationAPIHandler) evidence(r *http.Request, actor string, action saved.MutationAction, idempotencyKey, fingerprint string) (saved.MutationEvidence, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = apitransport.NewRequestID()
	}
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	return saved.NewMutationEvidence(actor, action, strings.TrimSpace(idempotencyKey), fingerprint, requestID, correlationID, time.Now().UTC())
}

func savedExplorationSummary(lifecycle saved.Lifecycle) analyticsgen.SavedExplorationSummaryResponse {
	response := analyticsgen.SavedExplorationSummaryResponse{
		Id: lifecycle.ID.String(), OwnerPrincipalId: lifecycle.OwnerPrincipalID, Title: lifecycle.Title,
		Slug: lifecycle.Slug, Visibility: analyticsgen.SavedExplorationVisibility(lifecycle.Visibility),
		SemanticModelId: lifecycle.SemanticModelID.String(), Status: analyticsgen.SavedExplorationStatus(lifecycle.Status),
		CreatedAt: lifecycle.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: lifecycle.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Revision: revisionToken(lifecycle.CurrentRevision.Token()),
	}
	if lifecycle.ArchivedAt != nil {
		value := lifecycle.ArchivedAt.UTC().Format(time.RFC3339Nano)
		response.ArchivedAt = &value
	}
	return response
}

func savedExplorationResponse(lifecycle saved.Lifecycle, revision saved.Revision) (analyticsgen.SavedExplorationResponse, error) {
	spec, err := revision.Payload.Spec()
	if err != nil {
		return analyticsgen.SavedExplorationResponse{}, err
	}
	return analyticsgen.SavedExplorationResponse{SavedExplorationSummaryResponse: savedExplorationSummary(lifecycle), Spec: spec}, nil
}

func savedExplorationWorkingCopy(opened saved.ReopenResult) analyticsgen.SavedExplorationResponse {
	return analyticsgen.SavedExplorationResponse{SavedExplorationSummaryResponse: savedExplorationSummary(opened.Lifecycle), Spec: opened.Spec}
}

func revisionToken(token saved.RevisionToken) analyticsgen.SavedExplorationRevisionToken {
	return analyticsgen.SavedExplorationRevisionToken{RevisionId: token.RevisionID.String(), Number: int64(token.Number), ContentHash: token.ContentHash}
}

func savedMutationConcurrencyToken(result saved.MutationResult) (string, error) {
	if err := result.ConcurrencyRevision.ValidateComplete(); err != nil {
		return "", fmt.Errorf("mutation result concurrency revision: %w", err)
	}
	return apigencommand.RevisionToken(result.ConcurrencyRevision)
}

func revisionETag(token saved.RevisionToken) string {
	if err := token.ValidateComplete(); err != nil {
		return ""
	}
	canonical, err := json.Marshal(token)
	if err != nil {
		return ""
	}
	// ETags are HTTP entity-tags, not JSON strings. Encode the canonical
	// revision token as unpadded base64url so the opaque tag contains only
	// RFC-valid etagc characters and cannot be confused with a JSON document.
	return `"` + base64.RawURLEncoding.EncodeToString(canonical) + `"`
}

func savedExplorationLocation(project, exploration string) string {
	return "/api/v1/projects/" + project + "/saved-explorations/" + exploration
}

func parseRevisionToken(header string) (saved.RevisionToken, error) {
	header = strings.TrimSpace(header)
	if len(header) < 2 || header[0] != '"' || header[len(header)-1] != '"' {
		return saved.RevisionToken{}, fmt.Errorf("%w: If-Match must be one strong entity-tag", saved.ErrInvalidRevision)
	}
	encoded := header[1 : len(header)-1]
	if encoded == "" {
		return saved.RevisionToken{}, fmt.Errorf("%w: If-Match entity-tag is empty", saved.ErrInvalidRevision)
	}
	for _, char := range encoded {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_') {
			return saved.RevisionToken{}, fmt.Errorf("%w: malformed If-Match entity-tag", saved.ErrInvalidRevision)
		}
	}
	canonical, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != encoded {
		return saved.RevisionToken{}, fmt.Errorf("%w: malformed If-Match entity-tag", saved.ErrInvalidRevision)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var token saved.RevisionToken
	if err := decoder.Decode(&token); err != nil {
		return saved.RevisionToken{}, fmt.Errorf("%w: malformed If-Match", saved.ErrInvalidRevision)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return saved.RevisionToken{}, fmt.Errorf("%w: malformed If-Match", saved.ErrInvalidRevision)
	}
	if err := token.ValidateComplete(); err != nil {
		return saved.RevisionToken{}, err
	}
	canonicalToken, err := json.Marshal(token)
	if err != nil || !bytes.Equal(canonicalToken, canonical) {
		return saved.RevisionToken{}, fmt.Errorf("%w: non-canonical If-Match token", saved.ErrInvalidRevision)
	}
	return token, nil
}

func bodySlug(slug *string, title, fallback string) string {
	if slug != nil {
		// Explicit slugs are preserved byte-for-byte. The saved-exploration
		// domain validates them, including empty and malformed values, instead
		// of silently turning an explicit client value into a generated one.
		return *slug
	}
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	value := strings.TrimSuffix(b.String(), "-")
	// The stable exploration identity is the uniqueness suffix for generated
	// slugs. Strip only the adapter's conventional prefix; the complete hash
	// suffix remains, so repeated titles never contend for one project slug.
	suffix := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fallback)), "exploration-")
	if len(suffix) > saved.MaxSlugLength {
		suffix = suffix[len(suffix)-saved.MaxSlugLength:]
	}
	if suffix == "" {
		return value
	}
	if value == "" {
		return suffix
	}
	maxBaseLength := saved.MaxSlugLength - len(suffix) - 1
	if maxBaseLength <= 0 {
		return suffix
	}
	if len(value) > maxBaseLength {
		value = strings.TrimRight(value[:maxBaseLength], "-")
	}
	if value == "" {
		return suffix
	}
	return value + "-" + suffix
}

func stableSavedID(prefix, project, actor, idempotencyKey, operation string) (string, error) {
	return savedapplication.StableExplorationID(prefix, project, actor, idempotencyKey, operation)
}

func executeSavedMutation(ctx context.Context, operationID analyticsgen.GenCommandOperationID, mutate func(context.Context) error, attest func(context.Context, *apigencommand.Executor) error) error {
	executor, err := apigencommand.NewExecutor(analyticsgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, operationID.APIGenOperationID(), apigencommand.Execution{Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
		if mutate == nil {
			return saved.ErrUnavailable
		}
		if err := mutate(ctx); err != nil {
			return err
		}
		if attest != nil {
			return attest(ctx, executor)
		}
		return nil
	}})
}

func writeSavedExplorationFailure(w http.ResponseWriter, r *http.Request, operationID analyticsgen.GenCommandOperationID, err error) {
	err = classifySavedExplorationFailure(err)
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil, operationID, analyticsgen.GetAPIGenCommandFailureContracts, err)
}

func writeSavedExplorationQueryFailure(w http.ResponseWriter, r *http.Request, operationID string, err error) {
	err = classifySavedExplorationFailure(err)
	kind, _ := apigenfailure.KindOf(err)
	failure := apitransport.APIGenFailure{OperationID: operationID, Kind: kind, StatusCode: http.StatusInternalServerError, Code: "INTERNAL_ERROR", PublicDetail: "The request could not be completed.", Cause: err}
	switch kind {
	case "not_found":
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusNotFound, "SAVED_EXPLORATION_NOT_FOUND", "Saved exploration not found."
	case "invalid":
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusUnprocessableEntity, "INVALID_SAVED_EXPLORATION", "The saved exploration request is invalid."
	case "unavailable":
		failure.StatusCode, failure.Code, failure.PublicDetail = http.StatusServiceUnavailable, "SAVED_EXPLORATION_UNAVAILABLE", "Saved explorations are temporarily unavailable."
	}
	apitransport.WriteAPIGenFailure(r.Context(), w, r, nil, failure)
}

func classifySavedExplorationFailure(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, access.ErrForbidden), errors.Is(err, saved.ErrNotFound), errors.Is(err, saved.ErrUnauthorized):
		return apigenfailure.Wrap("not_found", err)
	case errors.Is(err, saved.ErrStaleRevision), errors.Is(err, saved.ErrInvalidRevision), errors.Is(err, apigencommand.ErrPreconditionFailed):
		return apigenfailure.Wrap("precondition", err)
	case errors.Is(err, saved.ErrConflict), errors.Is(err, saved.ErrAlreadyExists), errors.Is(err, saved.ErrArchived):
		return apigenfailure.Wrap("conflict", err)
	case errors.Is(err, saved.ErrInvalid), errors.Is(err, saved.ErrInvalidIdentifier), errors.Is(err, saved.ErrInvalidPayload), errors.Is(err, saved.ErrPayloadTooLarge), errors.Is(err, saved.ErrUnsupportedVersion):
		return apigenfailure.Wrap("invalid", err)
	case errors.Is(err, saved.ErrUnavailable), errors.Is(err, access.ErrAuditOutboxCapacity), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return apigenfailure.Wrap("unavailable", err)
	default:
		return err
	}
}
