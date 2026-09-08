package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
)

func (h *BrowserHandler) savedExplorationStateForBrowser(r *stdhttp.Request, selected string, includeArchived bool) projectui.DataExplorerSavedExplorationBootstrap {
	bootstrap := projectui.DataExplorerSavedExplorationBootstrap{
		Enabled: h != nil && h.SavedExplorations != nil,
		State: projectsignals.SavedExplorationStateSignal{
			Enabled: h != nil && h.SavedExplorations != nil,
			List:    projectsignals.SavedExplorationListSignal{Items: []projectsignals.SavedExplorationListItemSignal{}, IncludeArchived: includeArchived},
			Command: projectsignals.SavedExplorationCommandSignal{Action: "create"},
			Save:    projectsignals.SavedExplorationSaveStateSignal{State: "saved"},
		},
	}
	if !bootstrap.Enabled || r == nil || h.CurrentUser == nil {
		return bootstrap
	}
	principal, ok := h.CurrentUser(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return bootstrap
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		message := "Saved explorations are temporarily unavailable."
		bootstrap.State.Save = projectsignals.SavedExplorationSaveStateSignal{State: "error", Message: &message}
		return bootstrap
	}
	items, err := h.SavedExplorations.List(r.Context(), saved.ListRequest{ProjectID: projectID, ActorID: principal.ID, IncludeArchived: includeArchived})
	if err != nil {
		message := "Saved explorations are temporarily unavailable."
		bootstrap.State.Save = projectsignals.SavedExplorationSaveStateSignal{State: "error", Message: &message}
		return bootstrap
	}
	selectedID := strings.TrimSpace(selected)
	for _, lifecycle := range items {
		item := savedExplorationListItemSignal(lifecycle)
		bootstrap.State.List.Items = append(bootstrap.State.List.Items, item)
		if selectedID != "" && lifecycle.ID.String() == selectedID {
			id := lifecycle.ID.String()
			bootstrap.State.List.SelectedID = &id
			bootstrap.State.Current = savedExplorationCurrentSignal(lifecycle, false, nil)
		}
	}
	return bootstrap
}

func savedExplorationIncludeArchived(r *stdhttp.Request) bool {
	return r != nil && strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("includeArchived")), "true")
}

func savedExplorationListItemSignal(lifecycle saved.Lifecycle) projectsignals.SavedExplorationListItemSignal {
	item := projectsignals.SavedExplorationListItemSignal{
		ID: lifecycle.ID.String(), Title: lifecycle.Title, Slug: lifecycle.Slug,
		Visibility: string(lifecycle.Visibility), Status: string(lifecycle.Status), SemanticModelID: lifecycle.SemanticModelID.String(),
		CreatedAt: lifecycle.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: lifecycle.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Revision: savedExplorationRevisionSignal(lifecycle.CurrentRevision.Token()),
	}
	if lifecycle.ArchivedAt != nil {
		value := lifecycle.ArchivedAt.UTC().Format(time.RFC3339Nano)
		item.ArchivedAt = &value
	}
	return item
}

func savedExplorationCurrentSignal(lifecycle saved.Lifecycle, detached bool, spec *canonicalExplorationSpec) *projectsignals.SavedExplorationCurrentSignal {
	current := &projectsignals.SavedExplorationCurrentSignal{
		ID: lifecycle.ID.String(), Title: lifecycle.Title, Slug: lifecycle.Slug,
		Visibility: string(lifecycle.Visibility), Status: string(lifecycle.Status), SemanticModelID: lifecycle.SemanticModelID.String(),
		Revision: savedExplorationRevisionSignal(lifecycle.CurrentRevision.Token()), Detached: detached,
	}
	if spec != nil {
		value := *spec
		current.Spec = &value
	}
	return current
}

// canonicalExplorationSpec is an alias used only to keep the signal mapping
// helpers independent of the browser command envelope's pointer ownership.
type canonicalExplorationSpec = exploration.ExplorationSpec

func savedExplorationRevisionSignal(token saved.RevisionToken) projectsignals.SavedExplorationRevisionSignal {
	return projectsignals.SavedExplorationRevisionSignal{RevisionID: token.RevisionID.String(), Number: int64(token.Number), ContentHash: token.ContentHash}
}

func (h *BrowserHandler) SavedExplorationReopen(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if h == nil || h.SavedExplorations == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.NotFound(w, r)
		return
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	explorationID := saved.ExplorationID(strings.TrimSpace(chi.URLParam(r, "exploration")))
	if err := explorationID.Validate(); err != nil {
		stdhttp.NotFound(w, r)
		return
	}
	opened, err := h.SavedExplorations.Reopen(r.Context(), saved.ReopenRequest{ProjectID: projectID, ID: explorationID, ActorID: principal.ID})
	if err != nil {
		writeSavedExplorationReadError(w, r, err)
		return
	}
	if err := exploration.ValidateShape(&opened.Spec); err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnprocessableEntity), stdhttp.StatusUnprocessableEntity)
		return
	}
	mode := "explore"
	command := projectsignals.DataExplorerCommand{Mode: &mode, Explore: &projectsignals.DataExploreCommand{Spec: opened.Spec, RequestSeq: 1, ResetVersion: 1}, Limit: dataExplorerDefaultLimit, Count: dataExplorerDefaultLimit}
	// Reopen normally preserves the explorer's immediate result for active
	// copies, but it must not run strict durable-state validation: an active
	// saved revision may contain a field removed from the current model. The
	// non-strict execution path leaves that repair state as a signal error (and
	// performs no governed query) while still returning the detached payload;
	// compatible active copies retain their normal result visualization.
	executeQuery := opened.Lifecycle.Status == saved.StatusActive
	page, explorer, ok := h.dataExplorerSignalsForCommandWithOptions(w, r, command, executeQuery, false, false)
	if !ok {
		return
	}
	state := h.savedExplorationStateForBrowser(r, opened.Lifecycle.ID.String(), savedExplorationIncludeArchived(r)).State
	state.Current = savedExplorationCurrentSignal(opened.Lifecycle, true, (*canonicalExplorationSpec)(&opened.Spec))
	state.List.SelectedID = projectsignals.Optional(opened.Lifecycle.ID.String())
	if state.Save.State != "error" {
		state.Save = projectsignals.SavedExplorationSaveStateSignal{State: "saved"}
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{
		"page": page, "dataExplorer": explorer, "dataExplorerCommand": explorer.Command,
		"savedExplorations": state,
	})
}

type savedExplorationBrowserCommand struct {
	Saved projectsignals.SavedExplorationStateSignal `json:"savedExplorations"`
}

func (h *BrowserHandler) SavedExplorationCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var payload savedExplorationBrowserCommand
	if err := pagestream.ReadSignals(r, &payload); err != nil {
		stdhttp.Error(w, "saved exploration command payload is required", stdhttp.StatusBadRequest)
		return
	}
	command := payload.Saved.Command
	operation := h.savedExplorationOperation(command.Action)
	if operation == "" || uicommand.VerifyClaim(uicommand.OperationClaims(r), operation) != nil {
		h.savedExplorationCommandPatch(w, r, command, "The saved exploration command is invalid.", payload.Saved.List.IncludeArchived)
		return
	}
	if command.Action == "reopen" {
		h.savedExplorationCommandPatch(w, r, command, "Reopen is a read-only operation.", payload.Saved.List.IncludeArchived)
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" || h.SavedExplorations == nil {
		h.savedExplorationCommandPatch(w, r, command, "Saved exploration operation is unavailable.", payload.Saved.List.IncludeArchived)
		return
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		h.savedExplorationCommandPatch(w, r, command, "Saved exploration operation is unavailable.", payload.Saved.List.IncludeArchived)
		return
	}
	target, err := h.savedCommandTarget(command)
	if err != nil {
		h.savedExplorationCommandPatch(w, r, command, "The saved exploration target is invalid.", payload.Saved.List.IncludeArchived)
		return
	}
	key, requestID, correlationID, err := savedBrowserRequestIdentity(r)
	if err != nil {
		h.savedExplorationCommandPatch(w, r, command, "The saved exploration command is invalid.", payload.Saved.List.IncludeArchived)
		return
	}
	revision, err := savedRevisionTokenForCommand(command, command.Action == "update" || command.Action == "archive" || command.Action == "duplicate")
	if err != nil {
		h.savedExplorationCommandPatch(w, r, command, "The saved exploration revision is stale. Reload and try again.", payload.Saved.List.IncludeArchived)
		return
	}
	if command.Action == "update" || command.Action == "archive" || command.Action == "duplicate" {
		if err := savedIfMatchMatchesRevision(r, revision); err != nil {
			h.savedExplorationCommandPatch(w, r, command, "The saved exploration revision is stale. Reload and try again.", payload.Saved.List.IncludeArchived)
			return
		}
	}
	var result saved.MutationResult
	started, err := h.beginSavedExplorationInvocation(r, command.Action, projectID.String(), target.String(), key, requestID, correlationID, revision, &result.ConcurrencyRevision)
	if err != nil {
		h.savedExplorationCommandPatch(w, r, command, "The saved exploration command is invalid.", payload.Saved.List.IncludeArchived)
		return
	}
	invocation := SavedExplorationCommandInvocation{Action: command.Action, Project: projectID.String(), Resource: target.String(), IdempotencyKey: key, RequestID: requestID, CorrelationID: correlationID, Revision: revision, ConcurrencyRevision: &result.ConcurrencyRevision}
	if h.ExecuteSavedExplorationCommand == nil {
		h.savedExplorationCommandPatch(w, r, command, "Saved exploration command execution is unavailable.", payload.Saved.List.IncludeArchived)
		return
	}
	executed := false
	err = h.ExecuteSavedExplorationCommand(started.Context(), invocation, func(ctx context.Context) error {
		executed = true
		switch command.Action {
		case "create":
			var mutationErr error
			result, mutationErr = h.savedCreate(ctx, projectID, principal.ID, command, key, requestID, correlationID)
			return mutationErr
		case "update":
			var mutationErr error
			result, mutationErr = h.savedUpdate(ctx, projectID, principal.ID, target, command, revision, key, requestID, correlationID)
			return mutationErr
		case "duplicate":
			var mutationErr error
			result, mutationErr = h.savedDuplicate(ctx, projectID, principal.ID, target, command, revision, key, requestID, correlationID)
			return mutationErr
		case "archive":
			var mutationErr error
			result, mutationErr = h.savedArchive(ctx, projectID, principal.ID, target, revision, key, requestID, correlationID)
			return mutationErr
		default:
			return saved.ErrInvalid
		}
	})
	if err == nil && !executed {
		err = errors.New("saved exploration command executor did not run the transaction")
	}
	if err != nil {
		h.savedExplorationCommandPatch(w, r, command, publicSavedExplorationCommandError(err), payload.Saved.List.IncludeArchived)
		return
	}
	if err := result.Validate(); err != nil {
		h.savedExplorationCommandPatch(w, r, command, "The saved exploration command returned an invalid result.", payload.Saved.List.IncludeArchived)
		return
	}
	// Archive does not append a revision, but its lifecycle remains the exact
	// metadata returned by the CAS mutation. Include the archived item in this
	// explicit response so the browser can offer Reopen; normal explorer
	// bootstraps remain active-only.
	includeArchived := command.Action == "archive"
	state := h.savedExplorationStateForBrowser(r, result.Lifecycle.ID.String(), includeArchived).State
	if command.Action == "archive" {
		// Do not copy a browser command spec into durable state. An archive
		// response has no revision payload; the next explicit Reopen reads the
		// authored payload through the service and marks it read-only.
		state.Current = savedExplorationCurrentSignal(result.Lifecycle, false, nil)
	} else if result.Revision != nil {
		if spec, specErr := result.Revision.Payload.Spec(); specErr == nil {
			state.Current = savedExplorationCurrentSignal(result.Lifecycle, true, (*canonicalExplorationSpec)(&spec))
		}
	}
	message := "Saved exploration saved."
	if command.Action == "archive" {
		message = "Saved exploration archived."
	}
	if state.Save.State != "error" {
		state.Save = projectsignals.SavedExplorationSaveStateSignal{State: "saved", Message: &message}
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"savedExplorations": state})
}

// authorizeSavedExplorationMutationReplay reconstructs the actor/project/
// target-bound mutation and asks the application service to resolve the
// durable ledger entry. A captured response is replayable only when the
// current principal still passes the service's current authorization checks;
// a missing or non-replayed result fails closed.
func (h *BrowserHandler) authorizeSavedExplorationMutationReplay(r *stdhttp.Request) bool {
	if h == nil || r == nil || h.SavedExplorations == nil {
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var payload savedExplorationBrowserCommand
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	command := payload.Saved.Command
	operation := h.savedExplorationOperation(command.Action)
	if operation == "" || uicommand.VerifyClaim(uicommand.OperationClaims(r), operation) != nil || command.Action == "reopen" {
		return false
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return false
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		return false
	}
	target, err := h.savedCommandTarget(command)
	if err != nil {
		return false
	}
	key, _, _, err := savedBrowserRequestIdentity(r)
	if err != nil {
		return false
	}
	revision, err := savedRevisionTokenForCommand(command, command.Action == "update" || command.Action == "archive" || command.Action == "duplicate")
	if err != nil {
		return false
	}
	if command.Action == "update" || command.Action == "archive" || command.Action == "duplicate" {
		if err := savedIfMatchMatchesRevision(r, revision); err != nil {
			return false
		}
	}
	replayRequest, err := h.savedMutationReplayAuthorizationRequest(projectID, principal.ID, target, command, revision, key)
	if err != nil {
		return false
	}
	authorized, err := h.SavedExplorations.AuthorizeMutationReplay(r.Context(), replayRequest)
	return err == nil && authorized
}

func (h *BrowserHandler) savedMutationReplayAuthorizationRequest(projectID projectgraph.ResourceID, actor string, target saved.ExplorationID, command projectsignals.SavedExplorationCommandSignal, revision saved.RevisionToken, key string) (savedapplication.MutationReplayAuthorizationRequest, error) {
	request := savedapplication.MutationReplayAuthorizationRequest{ProjectID: projectID, ActorID: actor, IdempotencyKey: key}
	switch command.Action {
	case "create":
		spec, payload, err := savedCommandSpec(command)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		id, err := savedapplication.StableExplorationID("exploration-", projectID.String(), actor, key, "create")
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		title := savedCommandString(command.Title, "Untitled exploration")
		create := saved.CreateRequest{ProjectID: projectID, ID: saved.ExplorationID(id), ActorID: actor, Title: title, Slug: savedBrowserCommandSlug(command.Slug, title, id), Visibility: savedCommandVisibility(command.Visibility), Payload: payload, Spec: spec}
		fingerprint, err := savedapplication.FingerprintCreate(create)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		request.Action = saved.MutationActionCreate
		request.TargetID = create.ID
		request.Fingerprint = fingerprint
	case "update":
		spec, payload, err := savedCommandSpec(command)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		title := savedCommandString(command.Title, "Untitled exploration")
		update := saved.UpdateVersionRequest{ProjectID: projectID, ID: target, ActorID: actor, ExpectedRevision: revision, Title: title, Slug: savedBrowserCommandSlug(command.Slug, title, ""), Visibility: savedCommandVisibility(command.Visibility), Payload: payload, Spec: spec}
		fingerprint, err := savedapplication.FingerprintUpdate(update)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		request.Action = saved.MutationActionUpdate
		request.TargetID = target
		request.Fingerprint = fingerprint
	case "duplicate":
		duplicate := saved.DuplicateRequest{ProjectID: projectID, SourceID: target, ExpectedSourceRevision: revision, ActorID: actor, Title: savedCommandString(command.Title, "Copy of "+target.String()), Visibility: savedCommandVisibility(command.Visibility)}
		id, err := savedapplication.StableExplorationID("exploration-", projectID.String(), actor, key, "duplicate:"+target.String())
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		duplicate.Slug = savedBrowserCommandSlug(command.Slug, duplicate.Title, id)
		duplicate.ID = saved.ExplorationID(id)
		fingerprint, err := savedapplication.FingerprintDuplicate(duplicate)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		request.Action = saved.MutationActionDuplicate
		request.TargetID = target
		request.Fingerprint = fingerprint
	case "archive":
		archive := saved.ArchiveRequest{ProjectID: projectID, ID: target, ActorID: actor, ExpectedRevision: revision}
		fingerprint, err := savedapplication.FingerprintArchive(archive)
		if err != nil {
			return savedapplication.MutationReplayAuthorizationRequest{}, err
		}
		request.Action = saved.MutationActionArchive
		request.TargetID = target
		request.Fingerprint = fingerprint
	default:
		return savedapplication.MutationReplayAuthorizationRequest{}, saved.ErrInvalid
	}
	return request, nil
}

func (h *BrowserHandler) savedCreate(ctx context.Context, projectID projectgraph.ResourceID, actor string, command projectsignals.SavedExplorationCommandSignal, key, requestID, correlationID string) (saved.MutationResult, error) {
	spec, payload, err := savedCommandSpec(command)
	if err != nil {
		return saved.MutationResult{}, err
	}
	title := savedCommandString(command.Title, "Untitled exploration")
	visibility := savedCommandVisibility(command.Visibility)
	id, err := savedapplication.StableExplorationID("exploration-", projectID.String(), actor, key, "create")
	if err != nil {
		return saved.MutationResult{}, err
	}
	slug := savedBrowserCommandSlug(command.Slug, title, id)
	request := saved.CreateRequest{ProjectID: projectID, ID: saved.ExplorationID(id), ActorID: actor, Title: title, Slug: slug, Visibility: visibility, Payload: payload, Spec: spec}
	fingerprint, err := savedapplication.FingerprintCreate(request)
	if err != nil {
		return saved.MutationResult{}, err
	}
	request.Evidence, err = saved.NewMutationEvidence(actor, saved.MutationActionCreate, key, fingerprint, requestID, correlationID, time.Now().UTC())
	if err != nil {
		return saved.MutationResult{}, err
	}
	return h.SavedExplorations.Create(ctx, request)
}

func (h *BrowserHandler) savedUpdate(ctx context.Context, projectID projectgraph.ResourceID, actor string, target saved.ExplorationID, command projectsignals.SavedExplorationCommandSignal, revision saved.RevisionToken, key, requestID, correlationID string) (saved.MutationResult, error) {
	spec, payload, err := savedCommandSpec(command)
	if err != nil {
		return saved.MutationResult{}, err
	}
	title := savedCommandString(command.Title, "Untitled exploration")
	request := saved.UpdateVersionRequest{ProjectID: projectID, ID: target, ActorID: actor, ExpectedRevision: revision, Title: title, Slug: savedBrowserCommandSlug(command.Slug, title, ""), Visibility: savedCommandVisibility(command.Visibility), Payload: payload, Spec: spec}
	fingerprint, err := savedapplication.FingerprintUpdate(request)
	if err != nil {
		return saved.MutationResult{}, err
	}
	request.Evidence, err = saved.NewMutationEvidence(actor, saved.MutationActionUpdate, key, fingerprint, requestID, correlationID, time.Now().UTC())
	if err != nil {
		return saved.MutationResult{}, err
	}
	return h.SavedExplorations.UpdateVersion(ctx, request)
}

func (h *BrowserHandler) savedDuplicate(ctx context.Context, projectID projectgraph.ResourceID, actor string, source saved.ExplorationID, command projectsignals.SavedExplorationCommandSignal, revision saved.RevisionToken, key, requestID, correlationID string) (saved.MutationResult, error) {
	title := savedCommandString(command.Title, "Copy of "+source.String())
	request := saved.DuplicateRequest{ProjectID: projectID, SourceID: source, ExpectedSourceRevision: revision, ActorID: actor, Title: title, Visibility: savedCommandVisibility(command.Visibility)}
	id, err := savedapplication.StableExplorationID("exploration-", projectID.String(), actor, key, "duplicate:"+source.String())
	if err != nil {
		return saved.MutationResult{}, err
	}
	request.Slug = savedBrowserCommandSlug(command.Slug, title, id)
	request.ID = saved.ExplorationID(id)
	fingerprint, err := savedapplication.FingerprintDuplicate(request)
	if err != nil {
		return saved.MutationResult{}, err
	}
	request.Evidence, err = saved.NewMutationEvidence(actor, saved.MutationActionDuplicate, key, fingerprint, requestID, correlationID, time.Now().UTC())
	if err != nil {
		return saved.MutationResult{}, err
	}
	return h.SavedExplorations.Duplicate(ctx, request)
}

func (h *BrowserHandler) savedArchive(ctx context.Context, projectID projectgraph.ResourceID, actor string, target saved.ExplorationID, revision saved.RevisionToken, key, requestID, correlationID string) (saved.MutationResult, error) {
	request := saved.ArchiveRequest{ProjectID: projectID, ID: target, ActorID: actor, ExpectedRevision: revision}
	fingerprint, err := savedapplication.FingerprintArchive(request)
	if err != nil {
		return saved.MutationResult{}, err
	}
	request.Evidence, err = saved.NewMutationEvidence(actor, saved.MutationActionArchive, key, fingerprint, requestID, correlationID, time.Now().UTC())
	if err != nil {
		return saved.MutationResult{}, err
	}
	return h.SavedExplorations.Archive(ctx, request)
}

func savedCommandSpec(command projectsignals.SavedExplorationCommandSignal) (exploration.ExplorationSpec, saved.ExplorationSpecPayload, error) {
	if command.Spec == nil {
		return exploration.ExplorationSpec{}, saved.ExplorationSpecPayload{}, fmt.Errorf("%w: authored exploration spec is required", saved.ErrInvalid)
	}
	if err := exploration.ValidateShape(command.Spec); err != nil {
		return exploration.ExplorationSpec{}, saved.ExplorationSpecPayload{}, err
	}
	payload, err := saved.NewExplorationSpecPayload(*command.Spec)
	if err != nil {
		return exploration.ExplorationSpec{}, saved.ExplorationSpecPayload{}, err
	}
	return *command.Spec, payload, nil
}

func (h *BrowserHandler) savedCommandTarget(command projectsignals.SavedExplorationCommandSignal) (saved.ExplorationID, error) {
	if command.Action == "create" {
		return "", nil
	}
	wireID := strings.TrimSpace(projectsignals.ValueOrZero(command.ExplorationID))
	if command.Action == "duplicate" {
		wireID = strings.TrimSpace(projectsignals.ValueOrZero(command.SourceExplorationID))
	}
	if wireID == "" {
		return "", fmt.Errorf("%w: command target is required", saved.ErrInvalid)
	}
	target := saved.ExplorationID(wireID)
	return target, target.Validate()
}

func savedRevisionTokenForCommand(command projectsignals.SavedExplorationCommandSignal, required bool) (saved.RevisionToken, error) {
	if !required {
		return saved.RevisionToken{}, nil
	}
	revision := command.ExpectedRevision
	if command.Action == "duplicate" {
		revision = command.ExpectedSourceRevision
	}
	if revision == nil || revision.RevisionID == "" || revision.Number <= 0 || strings.TrimSpace(revision.ContentHash) == "" {
		return saved.RevisionToken{}, saved.ErrInvalidRevision
	}
	token := saved.RevisionToken{RevisionID: saved.RevisionID(revision.RevisionID), Number: uint64(revision.Number), ContentHash: revision.ContentHash}
	return token, token.ValidateComplete()
}

// savedIfMatchMatchesRevision verifies the browser's concurrency header
// against the canonical signal token. The browser currently sends
// JSON.stringify(revision), so the unquoted JSON object is the primary wire
// format. A JSON string containing that object is accepted for clients that
// safely quote a header value; no ETag or wildcard syntax is accepted.
func savedIfMatchMatchesRevision(r *stdhttp.Request, expected saved.RevisionToken) error {
	if r == nil {
		return fmt.Errorf("%w: If-Match is required", saved.ErrInvalidRevision)
	}
	values := r.Header.Values("If-Match")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return fmt.Errorf("%w: If-Match must contain one complete revision token", saved.ErrInvalidRevision)
	}
	actual, err := parseSavedIfMatchRevision(values[0])
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("%w: If-Match does not match the signal revision", saved.ErrStaleRevision)
	}
	return nil
}

func parseSavedIfMatchRevision(value string) (saved.RevisionToken, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return saved.RevisionToken{}, fmt.Errorf("%w: If-Match is required", saved.ErrInvalidRevision)
	}
	if strings.HasPrefix(value, "\"") {
		var encoded string
		if err := decodeSavedJSONValue(value, &encoded); err != nil {
			return saved.RevisionToken{}, fmt.Errorf("%w: invalid If-Match revision: %v", saved.ErrInvalidRevision, err)
		}
		value = strings.TrimSpace(encoded)
	}
	var token saved.RevisionToken
	if err := decodeSavedJSONValue(value, &token); err != nil {
		return saved.RevisionToken{}, fmt.Errorf("%w: invalid If-Match revision: %v", saved.ErrInvalidRevision, err)
	}
	if err := token.ValidateComplete(); err != nil {
		return saved.RevisionToken{}, err
	}
	return token, nil
}

func decodeSavedJSONValue(value string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func savedBrowserRequestIdentity(r *stdhttp.Request) (string, string, string, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		return "", "", "", errors.New("request identity is required")
	}
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	return "ui:" + requestID, requestID, correlationID, nil
}

func (h *BrowserHandler) beginSavedExplorationInvocation(r *stdhttp.Request, action, project, resource, key, requestID, correlationID string, revision saved.RevisionToken, concurrencyRevision *saved.RevisionToken) (*stdhttp.Request, error) {
	if h == nil || h.BeginSavedExplorationCommand == nil {
		return r, errors.New("saved exploration command invocation is unavailable")
	}
	ctx, err := h.BeginSavedExplorationCommand(r.Context(), SavedExplorationCommandInvocation{Action: action, Project: project, Resource: resource, IdempotencyKey: key, RequestID: requestID, CorrelationID: correlationID, Revision: revision, ConcurrencyRevision: concurrencyRevision})
	if err != nil {
		return r, err
	}
	return r.WithContext(ctx), nil
}

func (h *BrowserHandler) savedExplorationOperation(action string) string {
	switch action {
	case "create":
		return h.SavedExplorationCommands.Create.OperationID()
	case "update":
		return h.SavedExplorationCommands.Update.OperationID()
	case "duplicate":
		return h.SavedExplorationCommands.Duplicate.OperationID()
	case "archive":
		return h.SavedExplorationCommands.Archive.OperationID()
	default:
		return ""
	}
}

func (h *BrowserHandler) savedExplorationCommandPatch(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.SavedExplorationCommandSignal, message string, includeArchived ...bool) {
	selected := strings.TrimSpace(projectsignals.ValueOrZero(command.ExplorationID))
	if command.Action == "duplicate" {
		selected = strings.TrimSpace(projectsignals.ValueOrZero(command.SourceExplorationID))
	}
	include := savedExplorationIncludeArchived(r)
	if len(includeArchived) > 0 && includeArchived[0] {
		include = true
	}
	state := h.savedExplorationStateForBrowser(r, selected, include).State
	state.Command = command
	state.Save = projectsignals.SavedExplorationSaveStateSignal{State: "error", Message: &message}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"savedExplorations": state})
}

func publicSavedExplorationCommandError(err error) string {
	switch {
	case errors.Is(err, access.ErrForbidden), errors.Is(err, saved.ErrUnauthorized), errors.Is(err, saved.ErrNotFound):
		return "Saved exploration operation is forbidden."
	case errors.Is(err, saved.ErrStaleRevision), errors.Is(err, saved.ErrConflict), errors.Is(err, saved.ErrArchived):
		return "The saved exploration changed. Reload and try again."
	case errors.Is(err, saved.ErrUnavailable):
		return "Saved exploration operation is temporarily unavailable."
	default:
		return "The saved exploration command is invalid."
	}
}

func writeSavedExplorationReadError(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	switch {
	case errors.Is(err, access.ErrForbidden), errors.Is(err, saved.ErrUnauthorized), errors.Is(err, saved.ErrNotFound):
		stdhttp.NotFound(w, r)
	case errors.Is(err, saved.ErrInvalid), errors.Is(err, saved.ErrInvalidIdentifier):
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnprocessableEntity), stdhttp.StatusUnprocessableEntity)
	case errors.Is(err, saved.ErrUnavailable):
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
	default:
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusInternalServerError), stdhttp.StatusInternalServerError)
	}
}

func savedCommandString(value *string, fallback string) string {
	if value != nil && strings.TrimSpace(*value) != "" {
		return strings.TrimSpace(*value)
	}
	return fallback
}

func savedCommandVisibility(value *string) saved.Visibility {
	if value != nil && strings.TrimSpace(*value) != "" {
		return saved.Visibility(strings.TrimSpace(*value))
	}
	return saved.VisibilityPrivate
}

// savedBrowserCommandSlug preserves an authored slug exactly. A nil slug is
// the only case that permits browser fallback generation; this distinction is
// important because the saved domain must reject explicit empty or malformed
// slugs instead of normalizing them into a different value.
func savedBrowserCommandSlug(value *string, title, id string) string {
	if value != nil {
		return *value
	}
	if id == "" {
		return savedBrowserSlug(title)
	}
	return savedBrowserUniqueSlug(title, id)
}

// savedBrowserSlug generates a fallback within the saved-domain grammar even
// when the source identity has punctuation such as the colon used by
// generated IDs.
func savedBrowserSlug(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	value = strings.Trim(builder.String(), "-")
	if value == "" {
		return "exploration"
	}
	if len(value) > saved.MaxSlugLength {
		value = strings.TrimRight(value[:saved.MaxSlugLength], "-")
	}
	return value
}

func savedBrowserUniqueSlug(value, id string) string {
	base := savedBrowserSlug(value)
	suffix := strings.TrimPrefix(strings.TrimSpace(id), "exploration-")
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	if suffix == "" {
		return base
	}
	maxBase := saved.MaxSlugLength - len(suffix) - 1
	if maxBase < 1 {
		return suffix[:saved.MaxSlugLength]
	}
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	if base == "" {
		return suffix
	}
	return base + "-" + suffix
}
