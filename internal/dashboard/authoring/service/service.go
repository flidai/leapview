// Package service contains the application boundary for dashboard authoring.
// It deliberately depends on the authoring contracts and repository ports,
// but does not know about HTTP, Datastar, grants, Git, or serving-state
// publication.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/project/graph"
)

// AuthorizationRequest is the single authorization boundary for every
// authoring mutation. The owner and semantic-model identities are included so
// an adapter can make one scoped decision without looking up more state.
type AuthorizationRequest struct {
	ActorID          string
	ProjectID        graph.ResourceID
	DashboardID      authoring.DashboardID
	OwnerPrincipalID string
	SemanticModel    graph.ResourceID
	Action           authoring.AuthorizationAction
}

type Authorizer interface {
	Authorize(context.Context, AuthorizationRequest) error
}

// Compilation is the exact compiler-facing result persisted with a published
// authored revision. The service does not activate a serving generation.
type Compilation struct {
	Definition       dashboarddefinition.Definition
	SemanticIdentity graph.ServingIdentity
}

type Compiler interface {
	Compile(context.Context, graph.ResourceID, graph.ResourceID, authoring.Dashboard) (Compilation, error)
}

// Options wires the service's required ports. IDs and time are supplied by
// callers so domain operations are deterministic and testable.
type Options struct {
	Repository authoring.Repository
	Authorizer Authorizer
	Compiler   Compiler
	Now        func() time.Time

	NewDashboardID func() (authoring.DashboardID, error)
	NewDraftID     func() (authoring.DraftID, error)
	NewRevisionID  func() (authoring.RevisionID, error)
}

type Service struct {
	repository     authoring.Repository
	authorizer     Authorizer
	compiler       Compiler
	now            func() time.Time
	newDashboardID func() (authoring.DashboardID, error)
	newDraftID     func() (authoring.DraftID, error)
	newRevisionID  func() (authoring.RevisionID, error)
}

func NewService(options Options) (*Service, error) {
	if options.Repository == nil || options.Authorizer == nil || options.Compiler == nil {
		return nil, fmt.Errorf("dashboard authoring repository, authorizer, and compiler are required")
	}
	if options.Now == nil || options.NewDashboardID == nil || options.NewDraftID == nil || options.NewRevisionID == nil {
		return nil, fmt.Errorf("dashboard authoring clock and ID generators are required")
	}
	return &Service{
		repository: options.Repository, authorizer: options.Authorizer, compiler: options.Compiler,
		now: options.Now, newDashboardID: options.NewDashboardID,
		newDraftID: options.NewDraftID, newRevisionID: options.NewRevisionID,
	}, nil
}

// Result is the stable service response. Revision identifies the mutation's
// immutable result while Lifecycle is the repository-authoritative current
// pointer (which may have advanced after an idempotent replay).
type Result struct {
	Revision  authoring.RevisionToken      `json:"revision"`
	Lifecycle authoring.DashboardLifecycle `json:"lifecycle"`
}

// CreateRequest creates a named project draft. DashboardID is optional; an
// omitted ID is allocated by the injected generator.
type CreateRequest struct {
	ProjectID        graph.ResourceID
	ActorID          string
	OwnerPrincipalID string
	DashboardID      authoring.DashboardID
	Title            string
	Slug             string
	SemanticModel    graph.ResourceID
	Visibility       authoring.Visibility
	Origin           authoring.Origin
	Source           *authoring.SourceMetadata
	ConversationID   string
	ToolCallID       string
	// IdempotencyKey is a caller-supplied retry identity. It is deliberately
	// separate from ToolCallID, which is retained solely as provenance.
	IdempotencyKey       string
	BaseSemanticIdentity graph.ServingIdentity
}

// ForkRequest copies the exact published authored revision of a dashboard into
// a new private draft in the project identified by ProjectID.
type ForkRequest struct {
	ProjectID         graph.ResourceID
	SourceDashboardID authoring.DashboardID
	ActorID           string
	OwnerPrincipalID  string
	Title             string
	Slug              string
	Origin            authoring.Origin
	Source            *authoring.SourceMetadata
	ConversationID    string
	ToolCallID        string
	// IdempotencyKey is a caller-supplied retry identity, separate from the
	// provenance ToolCallID.
	IdempotencyKey       string
	BaseSemanticIdentity graph.ServingIdentity
}

// CreateFromDocumentRequest creates a new private draft from a complete
// authored document supplied by an application source adapter. The document
// is cloned and assigned fresh dashboard/revision/draft identities before it
// crosses the repository boundary. This keeps project-artifact forks atomic:
// callers never need to reconstruct a document through a sequence of edit
// commands (which could lose fields or leave a partial draft behind).
//
// Source is descriptive provenance only. In particular, an adapter must not
// manufacture a RevisionToken for a project artifact that does not have one.
type CreateFromDocumentRequest struct {
	ProjectID            graph.ResourceID
	ActorID              string
	OwnerPrincipalID     string
	Document             authoring.Dashboard
	Title                string
	Slug                 string
	Origin               authoring.Origin
	Source               *authoring.SourceMetadata
	ForkedFrom           *authoring.ForkEvidence
	ConversationID       string
	ToolCallID           string
	IdempotencyKey       string
	OperationSeed        *ForkOperationSeed
	BaseSemanticIdentity graph.ServingIdentity
}

// ForkOperationSeed binds a complete-document project fork to its immutable
// caller-supplied source reference. It lets an adapter replay before loading a
// mutable serving artifact.
type ForkOperationSeed struct {
	SourceKind        string
	SourceProjectID   graph.ResourceID
	SourceDashboardID authoring.DashboardID
	TargetProjectID   graph.ResourceID
	OwnerPrincipalID  string
	Title             string
	Slug              string
}

// ForkIdentityRequest is the source-reference and caller-override portion of
// a fork. It is used for a pre-load replay check by source adapters; Source,
// when present, is caller provenance rather than loaded source evidence.
type ForkIdentityRequest struct {
	TargetProjectID   graph.ResourceID
	SourceKind        string
	SourceProjectID   graph.ResourceID
	SourceDashboardID authoring.DashboardID
	ActorID           string
	OwnerPrincipalID  string
	Title             string
	Slug              string
	Origin            authoring.Origin
	Source            *authoring.SourceMetadata
	ConversationID    string
	ToolCallID        string
	IdempotencyKey    string
}

// CreateFromDocument creates one private draft from an authored document.
// It performs the same edit authorization and transactional repository create
// as Create, while preserving every authored field in the supplied document.
// No compiler, publication, deployment, or data/model mutation is involved.
func (s *Service) CreateFromDocument(ctx context.Context, input CreateFromDocumentRequest) (Result, error) {
	return s.createDraft(ctx, createDraftInput{
		ProjectID: input.ProjectID, ActorID: input.ActorID, OwnerPrincipalID: input.OwnerPrincipalID,
		Document: input.Document, Title: input.Title, Slug: input.Slug, Visibility: authoring.VisibilityPrivate,
		Origin: input.Origin, Source: input.Source, ForkedFrom: input.ForkedFrom,
		ConversationID: input.ConversationID, ToolCallID: input.ToolCallID, IdempotencyKey: input.IdempotencyKey,
		OperationSeed: input.OperationSeed, BaseSemanticIdentity: input.BaseSemanticIdentity, OperationKind: "fork",
	})
}

// Fork copies a published authored revision into a new private draft. It
// never compiles, publishes, deploys, or mutates the source lifecycle.
func (s *Service) Fork(ctx context.Context, input ForkRequest) (Result, error) {
	projectID := input.ProjectID
	actorID := strings.TrimSpace(input.ActorID)
	if err := projectID.Validate(); err != nil || actorID == "" {
		return Result{}, fmt.Errorf("project and actor are required")
	}
	sourceProjectID := projectID
	sourceID := input.SourceDashboardID
	if err := sourceID.Validate(); err != nil {
		return Result{}, err
	}
	operation, err := s.forkOperation(projectID, actorID, sourceID, input)
	if err != nil {
		return Result{}, err
	}
	if operation.Enabled() {
		if replay, found, err := s.authorizedCreateReplay(ctx, actorID, operation); err != nil {
			return Result{}, err
		} else if found {
			return replay, nil
		}
	}

	// Load and authorize the source before reading its immutable revision. This
	// is both the authorization boundary and the required VIEW-before-EDIT
	// ordering for a fork.
	source, err := s.repository.Get(ctx, sourceProjectID, sourceID)
	if err != nil {
		return Result{}, err
	}
	if source.ProjectID != sourceProjectID || source.ID != sourceID {
		return Result{}, fmt.Errorf("%w: source lifecycle identity does not match request", authoring.ErrInvalidAuthoring)
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{ActorID: actorID, ProjectID: sourceProjectID, DashboardID: source.ID, OwnerPrincipalID: source.OwnerPrincipalID, SemanticModel: source.SemanticModel, Action: authoring.AuthorizationActionView}); err != nil {
		return Result{}, err
	}
	if source.Status == authoring.LifecycleStatusArchived {
		return Result{}, fmt.Errorf("%w: archived dashboard cannot be forked", authoring.ErrConflict)
	}
	if source.Status != authoring.LifecycleStatusPublished || source.Published == nil {
		return Result{}, fmt.Errorf("%w: source dashboard has no published revision", authoring.ErrInvalidAuthoring)
	}
	if err := source.Validate(); err != nil {
		return Result{}, err
	}

	publishedToken := source.Published.Revision
	sourceRevision, err := s.repository.GetRevision(ctx, sourceProjectID, source.ID, publishedToken.RevisionID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) || errors.Is(err, authoring.ErrSourceUnavailable) {
			return Result{}, errors.Join(authoring.ErrSourceUnavailable, err)
		}
		return Result{}, err
	}
	if err := sourceRevision.Validate(); err != nil {
		return Result{}, err
	}
	if sourceRevision.DashboardID != source.ID || !sameToken(sourceRevision.Token(), publishedToken) {
		return Result{}, fmt.Errorf("%w: published revision pointer does not match retained source revision", authoring.ErrStaleRevision)
	}
	if sourceRevision.Document.SemanticModel != source.SemanticModel {
		return Result{}, fmt.Errorf("%w: source revision semantic model does not match lifecycle", authoring.ErrInvalidAuthoring)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = source.Title
	}
	slug := strings.TrimSpace(input.Slug)
	if slug == "" {
		slug = slugForTitle(title)
	}
	if slug == "" {
		return Result{}, fmt.Errorf("dashboard slug is required when title has no slug-compatible characters")
	}
	ownerID := strings.TrimSpace(input.OwnerPrincipalID)
	if ownerID == "" {
		ownerID = actorID
	}
	origin := input.Origin
	if origin == "" {
		origin = authoring.OriginUI
	}
	forkedFrom := &authoring.ForkEvidence{Kind: authoring.ForkSourceInstance, Instance: &authoring.InstanceForkEvidence{SourceProjectID: sourceProjectID, SourceDashboardID: source.ID, SourceRevision: publishedToken}}
	provenance := authoring.Provenance{Origin: origin, ActorID: actorID, ConversationID: strings.TrimSpace(input.ConversationID), ToolCallID: strings.TrimSpace(input.ToolCallID), BaseSemanticIdentity: input.BaseSemanticIdentity, Source: input.Source, ForkedFrom: forkedFrom}
	if err := provenance.Validate(); err != nil {
		return Result{}, err
	}
	targetID, err := s.newDashboardID()
	if err != nil {
		return Result{}, fmt.Errorf("allocate dashboard id: %w", err)
	}
	if err := targetID.Validate(); err != nil {
		return Result{}, err
	}
	if targetID == source.ID {
		return Result{}, fmt.Errorf("%w: source and target dashboard must differ", authoring.ErrInvalidAuthoring)
	}
	draftID, err := s.newDraftID()
	if err != nil {
		return Result{}, fmt.Errorf("allocate draft id: %w", err)
	}
	revisionID, err := s.newRevisionID()
	if err != nil {
		return Result{}, fmt.Errorf("allocate revision id: %w", err)
	}
	now, err := s.utcNow()
	if err != nil {
		return Result{}, err
	}

	document, err := sourceRevision.Document.Clone()
	if err != nil {
		return Result{}, err
	}
	document.ID = targetID
	document.Title = title
	// SemanticModel is intentionally left untouched: a dashboard fork never
	// forks the governed semantic model or its underlying data/schema.
	revision, err := authoring.NewRevision(revisionID, targetID, 1, now, document, provenance)
	if err != nil {
		return Result{}, err
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: projectID, ID: targetID, OwnerPrincipalID: ownerID, Slug: slug,
		Title: title, SemanticModel: source.SemanticModel, Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: draftID, DashboardID: targetID, Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		return Result{}, err
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{
		ActorID: actorID, ProjectID: projectID, DashboardID: targetID,
		OwnerPrincipalID: ownerID, SemanticModel: source.SemanticModel, Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return Result{}, err
	}
	created, err := s.repository.Create(ctx, authoring.CreateInput{ProjectID: projectID, Lifecycle: lifecycle, Revision: revision, Operation: operation})
	if err != nil {
		return Result{}, err
	}
	if replay, found, err := s.lookupCreateReplay(ctx, operation); err != nil {
		return Result{}, err
	} else if found {
		return replay, nil
	}
	return Result{Revision: revision.Token(), Lifecycle: created}, nil
}

// LookupForkReplay checks a source-reference fork key before an adapter loads
// mutable source content. Authorization is performed against the retained
// target before any fingerprint-reuse error is returned.
func (s *Service) LookupForkReplay(ctx context.Context, input ForkIdentityRequest) (Result, bool, error) {
	operation, err := s.forkIdentityOperation(input)
	if err != nil {
		return Result{}, false, err
	}
	if !operation.Enabled() {
		return Result{}, false, nil
	}
	return s.authorizedCreateReplay(ctx, strings.TrimSpace(input.ActorID), operation)
}

func (s *Service) Create(ctx context.Context, input CreateRequest) (Result, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return Result{}, fmt.Errorf("dashboard title is required")
	}
	semanticModel := input.SemanticModel
	if semanticModel == "" {
		return Result{}, fmt.Errorf("semantic model is required")
	}
	visibility := input.Visibility
	if visibility == "" {
		visibility = authoring.VisibilityPrivate
	}
	if err := visibility.Validate(); err != nil {
		return Result{}, err
	}
	defaultPage := dashboardmodel.Page{ID: "overview", Title: "Overview", Canvas: dashboardmodel.PageCanvas{Width: 1366, Height: 940}, Grid: dashboardmodel.PageGrid{Columns: 12, RowHeight: 48, Gap: 16}}.WithDefaults()
	return s.createDraft(ctx, createDraftInput{
		ProjectID: input.ProjectID, ActorID: input.ActorID, OwnerPrincipalID: input.OwnerPrincipalID,
		DashboardID:          input.DashboardID,
		RequestedDashboardID: input.DashboardID,
		Document:             authoring.Dashboard{ID: input.DashboardID, Title: title, SemanticModel: semanticModel, Visuals: map[string]authoring.AuthoringVisualization{}, Pages: []dashboardmodel.Page{defaultPage}},
		Title:                title, Slug: input.Slug, Visibility: visibility, Origin: input.Origin, Source: input.Source,
		ConversationID: input.ConversationID, ToolCallID: input.ToolCallID, BaseSemanticIdentity: input.BaseSemanticIdentity,
		IdempotencyKey: input.IdempotencyKey, OperationKind: "create",
	})
}

type createDraftInput struct {
	ProjectID            graph.ResourceID
	ActorID              string
	OwnerPrincipalID     string
	DashboardID          authoring.DashboardID
	RequestedDashboardID authoring.DashboardID
	Document             authoring.Dashboard
	Title                string
	Slug                 string
	Visibility           authoring.Visibility
	Origin               authoring.Origin
	Source               *authoring.SourceMetadata
	ForkedFrom           *authoring.ForkEvidence
	ConversationID       string
	ToolCallID           string
	IdempotencyKey       string
	OperationSeed        *ForkOperationSeed
	OperationKind        string
	BaseSemanticIdentity graph.ServingIdentity
}

// createDraft is the single transactional private-draft construction path
// shared by ordinary creation and external authored-source forks. Keeping ID
// allocation, provenance validation, edit authorization, and repository
// insertion together prevents the two entry points from drifting.
func (s *Service) createDraft(ctx context.Context, input createDraftInput) (Result, error) {
	projectID := input.ProjectID
	actorID := strings.TrimSpace(input.ActorID)
	ownerID := strings.TrimSpace(input.OwnerPrincipalID)
	if err := projectID.Validate(); err != nil || actorID == "" {
		return Result{}, fmt.Errorf("project and actor are required")
	}
	if ownerID == "" {
		ownerID = actorID
	}
	semanticModel := input.Document.SemanticModel
	if semanticModel == "" {
		return Result{}, fmt.Errorf("semantic model is required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = strings.TrimSpace(input.Document.Title)
	}
	if title == "" {
		return Result{}, fmt.Errorf("dashboard title is required")
	}
	slug := strings.TrimSpace(input.Slug)
	if slug == "" {
		slug = slugForTitle(title)
	}
	if slug == "" {
		return Result{}, fmt.Errorf("dashboard slug is required when title has no slug-compatible characters")
	}
	visibility := input.Visibility
	if visibility == "" {
		visibility = authoring.VisibilityPrivate
	}
	if err := visibility.Validate(); err != nil {
		return Result{}, err
	}
	origin := input.Origin
	if origin == "" {
		origin = authoring.OriginUI
	}
	provenance := authoring.Provenance{
		Origin: origin, ActorID: actorID,
		ConversationID: strings.TrimSpace(input.ConversationID), ToolCallID: strings.TrimSpace(input.ToolCallID),
		BaseSemanticIdentity: input.BaseSemanticIdentity, Source: input.Source, ForkedFrom: input.ForkedFrom,
	}
	if err := provenance.Validate(); err != nil {
		return Result{}, err
	}
	// Normalize a detached payload before allocating any lifecycle identity.
	// A temporary document ID is only for structural validation and is removed
	// from the operation fingerprint by createOperation.
	payloadDocument, err := input.Document.Clone()
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(payloadDocument.ID.String()) == "" {
		payloadDocument.ID = graph.ResourceID("pending-dashboard")
	}
	payloadDocument.Title = title
	if err := payloadDocument.ValidateDraftStructure(); err != nil {
		return Result{}, err
	}
	normalized := inputWithNormalizedCreateFields(input, projectID, actorID, ownerID, input.DashboardID, title, slug, visibility, origin, payloadDocument)
	normalized.ForkedFrom = provenance.ForkedFrom
	normalized.Source = provenance.Source
	normalized.OperationKind = input.OperationKind
	operation, err := s.createOperation(normalized)
	if err != nil {
		return Result{}, err
	}
	if operation.Enabled() {
		if replay, found, err := s.authorizedCreateReplay(ctx, actorID, operation); err != nil {
			return Result{}, err
		} else if found {
			return replay, nil
		}
	}
	targetID := input.DashboardID
	if targetID == "" {
		targetID, err = s.newDashboardID()
		if err != nil {
			return Result{}, fmt.Errorf("allocate dashboard id: %w", err)
		}
	}
	if err := targetID.Validate(); err != nil {
		return Result{}, err
	}
	draftID, err := s.newDraftID()
	if err != nil {
		return Result{}, fmt.Errorf("allocate draft id: %w", err)
	}
	revisionID, err := s.newRevisionID()
	if err != nil {
		return Result{}, fmt.Errorf("allocate revision id: %w", err)
	}
	now, err := s.utcNow()
	if err != nil {
		return Result{}, err
	}
	document, err := input.Document.Clone()
	if err != nil {
		return Result{}, err
	}
	document.ID = targetID
	document.Title = title
	revision, err := authoring.NewRevision(revisionID, targetID, 1, now, document, provenance)
	if err != nil {
		return Result{}, err
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: projectID, ID: targetID, OwnerPrincipalID: ownerID, Slug: slug,
		Title: title, SemanticModel: semanticModel, Visibility: visibility,
		Draft: &authoring.Draft{ID: draftID, DashboardID: targetID, Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		return Result{}, err
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{
		ActorID: actorID, ProjectID: projectID, DashboardID: targetID,
		OwnerPrincipalID: ownerID, SemanticModel: semanticModel, Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return Result{}, err
	}
	created, err := s.repository.Create(ctx, authoring.CreateInput{ProjectID: projectID, Lifecycle: lifecycle, Revision: revision, Operation: operation})
	if err != nil {
		return Result{}, err
	}
	if replay, found, err := s.lookupCreateReplay(ctx, operation); err != nil {
		return Result{}, err
	} else if found {
		return replay, nil
	}
	return Result{Revision: revision.Token(), Lifecycle: created}, nil
}

func (s *Service) lookupCreateReplay(ctx context.Context, operation authoring.CreateOperation) (Result, bool, error) {
	if !operation.Enabled() {
		return Result{}, false, nil
	}
	repository, ok := s.repository.(authoring.CreateOperationRepository)
	if !ok {
		return Result{}, false, fmt.Errorf("dashboard authoring repository does not support create idempotency")
	}
	stored, found, err := repository.LookupCreateOperation(ctx, operation)
	if err != nil || !found {
		return Result{}, found, err
	}
	lifecycle, err := s.repository.Get(ctx, operation.ProjectID, stored.DashboardID)
	if err != nil {
		return Result{}, false, err
	}
	return Result{Revision: stored.Revision, Lifecycle: lifecycle}, true, nil
}

// authorizedCreateReplay deliberately authorizes the retained target before
// comparing request fingerprints. This prevents a caller without EDIT access
// from learning whether an idempotency key exists or was reused.
func (s *Service) authorizedCreateReplay(ctx context.Context, actorID string, operation authoring.CreateOperation) (Result, bool, error) {
	if !operation.Enabled() {
		return Result{}, false, nil
	}
	repository, ok := s.repository.(authoring.CreateOperationRepository)
	if !ok {
		return Result{}, false, fmt.Errorf("dashboard authoring repository does not support create idempotency")
	}
	stored, found, err := repository.LookupCreateOperation(ctx, operation)
	if err != nil || !found {
		return Result{}, found, err
	}
	lifecycle, err := s.repository.Get(ctx, operation.ProjectID, stored.DashboardID)
	if err != nil {
		return Result{}, false, err
	}
	if err := s.authorizeReplay(ctx, actorID, lifecycle); err != nil {
		return Result{}, false, err
	}
	if stored.Fingerprint != operation.Fingerprint {
		return Result{}, false, authoring.ErrCommandReuse
	}
	return Result{Revision: stored.Revision, Lifecycle: lifecycle}, true, nil
}

func (s *Service) authorizeReplay(ctx context.Context, actorID string, lifecycle authoring.DashboardLifecycle) error {
	return s.authorizer.Authorize(ctx, AuthorizationRequest{ActorID: actorID, ProjectID: lifecycle.ProjectID, DashboardID: lifecycle.ID, OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel, Action: authoring.AuthorizationActionEdit})
}

func inputWithNormalizedCreateFields(input createDraftInput, projectID graph.ResourceID, actorID, ownerID string, dashboardID authoring.DashboardID, title, slug string, visibility authoring.Visibility, origin authoring.Origin, document authoring.Dashboard) createDraftInput {
	input.ProjectID, input.ActorID, input.OwnerPrincipalID = projectID, actorID, ownerID
	input.DashboardID, input.Title, input.Slug, input.Visibility, input.Origin = dashboardID, title, slug, visibility, origin
	input.Document = document
	return input
}

func (s *Service) createOperation(input createDraftInput) (authoring.CreateOperation, error) {
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return authoring.CreateOperation{}, nil
	}
	if input.OperationSeed != nil {
		seed := *input.OperationSeed
		return s.forkIdentityOperation(ForkIdentityRequest{TargetProjectID: seed.TargetProjectID, SourceKind: seed.SourceKind, SourceProjectID: seed.SourceProjectID, SourceDashboardID: seed.SourceDashboardID, ActorID: input.ActorID, OwnerPrincipalID: seed.OwnerPrincipalID, Title: seed.Title, Slug: seed.Slug, Origin: input.Origin, ConversationID: input.ConversationID, ToolCallID: input.ToolCallID, IdempotencyKey: key})
	}
	kind := strings.TrimSpace(input.OperationKind)
	if kind == "" {
		kind = "create"
	}
	requestedID := ""
	if input.RequestedDashboardID != "" {
		requestedID = input.RequestedDashboardID.String()
	}
	document := input.Document
	// Generated target IDs are not request payload. Source/fork evidence still
	// binds the exact source identity where applicable.
	document.ID = ""
	payload := struct {
		Kind                 string                    `json:"kind"`
		DashboardID          string                    `json:"dashboardId,omitempty"`
		OwnerPrincipalID     string                    `json:"ownerPrincipalId"`
		Title                string                    `json:"title"`
		Slug                 string                    `json:"slug"`
		SemanticModel        graph.ResourceID          `json:"semanticModel"`
		Visibility           authoring.Visibility      `json:"visibility"`
		Origin               authoring.Origin          `json:"origin"`
		Source               *authoring.SourceMetadata `json:"source,omitempty"`
		ForkedFrom           *authoring.ForkEvidence   `json:"forkedFrom,omitempty"`
		ConversationID       string                    `json:"conversationId,omitempty"`
		ToolCallID           string                    `json:"toolCallId,omitempty"`
		BaseSemanticIdentity graph.ServingIdentity     `json:"baseSemanticIdentity,omitempty"`
		Document             authoring.Dashboard       `json:"document"`
	}{Kind: kind, DashboardID: requestedID, OwnerPrincipalID: strings.TrimSpace(input.OwnerPrincipalID), Title: strings.TrimSpace(input.Title), Slug: strings.TrimSpace(input.Slug), SemanticModel: input.Document.SemanticModel, Visibility: input.Visibility, Origin: input.Origin, Source: input.Source, ForkedFrom: input.ForkedFrom, ConversationID: strings.TrimSpace(input.ConversationID), ToolCallID: strings.TrimSpace(input.ToolCallID), BaseSemanticIdentity: input.BaseSemanticIdentity, Document: document}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return authoring.CreateOperation{}, fmt.Errorf("encode create operation payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	operation := authoring.CreateOperation{ProjectID: input.ProjectID, ActorID: strings.TrimSpace(input.ActorID), Kind: kind, IdempotencyKey: key, ConversationID: strings.TrimSpace(input.ConversationID), ToolCallID: strings.TrimSpace(input.ToolCallID), Fingerprint: "sha256:" + hex.EncodeToString(digest[:])}
	if err := operation.Validate(); err != nil {
		return authoring.CreateOperation{}, err
	}
	return operation, nil
}

func (s *Service) forkOperation(projectID graph.ResourceID, actorID string, sourceID authoring.DashboardID, input ForkRequest) (authoring.CreateOperation, error) {
	return s.forkIdentityOperation(ForkIdentityRequest{TargetProjectID: projectID, SourceKind: "instance", SourceProjectID: projectID, SourceDashboardID: sourceID, ActorID: actorID, OwnerPrincipalID: input.OwnerPrincipalID, Title: input.Title, Slug: input.Slug, Origin: input.Origin, Source: input.Source, ConversationID: input.ConversationID, ToolCallID: input.ToolCallID, IdempotencyKey: input.IdempotencyKey})
}

func (s *Service) forkIdentityOperation(input ForkIdentityRequest) (authoring.CreateOperation, error) {
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return authoring.CreateOperation{}, nil
	}
	origin := input.Origin
	if origin == "" {
		origin = authoring.OriginUI
	}
	payload := struct {
		Kind              string                    `json:"kind"`
		SourceKind        string                    `json:"sourceKind"`
		SourceProjectID   graph.ResourceID          `json:"sourceProjectId"`
		SourceDashboardID authoring.DashboardID     `json:"sourceDashboardId"`
		TargetProjectID   graph.ResourceID          `json:"targetProjectId"`
		OwnerPrincipalID  string                    `json:"ownerPrincipalId,omitempty"`
		Title             string                    `json:"title,omitempty"`
		Slug              string                    `json:"slug,omitempty"`
		Origin            authoring.Origin          `json:"origin"`
		Source            *authoring.SourceMetadata `json:"source,omitempty"`
		ConversationID    string                    `json:"conversationId,omitempty"`
		ToolCallID        string                    `json:"toolCallId,omitempty"`
	}{Kind: "fork", SourceKind: strings.TrimSpace(input.SourceKind), SourceProjectID: input.SourceProjectID, SourceDashboardID: input.SourceDashboardID, TargetProjectID: input.TargetProjectID, OwnerPrincipalID: strings.TrimSpace(input.OwnerPrincipalID), Title: strings.TrimSpace(input.Title), Slug: strings.TrimSpace(input.Slug), Origin: origin, Source: input.Source, ConversationID: strings.TrimSpace(input.ConversationID), ToolCallID: strings.TrimSpace(input.ToolCallID)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return authoring.CreateOperation{}, fmt.Errorf("encode fork operation payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	operation := authoring.CreateOperation{ProjectID: input.TargetProjectID, ActorID: strings.TrimSpace(input.ActorID), Kind: "fork", IdempotencyKey: key, ConversationID: strings.TrimSpace(input.ConversationID), ToolCallID: strings.TrimSpace(input.ToolCallID), Fingerprint: "sha256:" + hex.EncodeToString(digest[:])}
	if err := operation.Validate(); err != nil {
		return authoring.CreateOperation{}, err
	}
	return operation, nil
}

// Execute is the one mutation path for the typed command union. Authorization
// and command-result lookup happen before stale reducer evaluation so retries
// remain successful even if a later edit has advanced the draft pointer.
func (s *Service) Execute(ctx context.Context, projectID graph.ResourceID, command authoring.Command) (Result, error) {
	return s.execute(ctx, projectID, command, nil)
}

// ExecuteValidated is the narrow extension used by application-level builder
// intents. The validator runs only after authorization and durable command
// idempotency lookup, so a replay returns its original result without reading
// a stale draft or acquiring a runtime lease again.
func (s *Service) ExecuteValidated(ctx context.Context, projectID graph.ResourceID, command authoring.Command, validator func(context.Context, authoring.DashboardLifecycle) error) (Result, error) {
	return s.execute(ctx, projectID, command, validator)
}

func (s *Service) execute(ctx context.Context, projectID graph.ResourceID, command authoring.Command, validator func(context.Context, authoring.DashboardLifecycle) error) (Result, error) {
	if err := projectID.Validate(); err != nil {
		return Result{}, fmt.Errorf("project id is required")
	}
	if err := command.Validate(); err != nil {
		return Result{}, err
	}
	action, err := command.RequiredAction()
	if err != nil {
		return Result{}, err
	}
	lifecycle, err := s.repository.Get(ctx, projectID, command.DashboardID)
	if err != nil {
		return Result{}, err
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{ActorID: command.Provenance.ActorID, ProjectID: projectID, DashboardID: lifecycle.ID, OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel, Action: action}); err != nil {
		return Result{}, err
	}
	fingerprint, err := command.Fingerprint()
	if err != nil {
		return Result{}, err
	}
	evidenceAt, err := s.utcNow()
	if err != nil {
		return Result{}, err
	}
	evidence := authoring.CommandEvidence{ID: command.ID, Fingerprint: fingerprint, Action: action, Provenance: command.Provenance, OccurredAt: evidenceAt}
	if err := evidence.Validate(); err != nil {
		return Result{}, err
	}
	replayed, found, err := s.repository.LookupCommandResult(ctx, projectID, command.DashboardID, evidence)
	if err != nil {
		return Result{}, err
	}
	if found {
		if replayed.Revision.IsZero() {
			replayed.Revision = currentToken(lifecycle)
		}
		return Result{Revision: replayed.Revision, Lifecycle: lifecycle}, nil
	}
	if validator != nil {
		if err := validator(ctx, lifecycle); err != nil {
			return Result{}, err
		}
	}

	switch {
	case command.Publish != nil:
		return s.publish(ctx, projectID, command, lifecycle, evidence)
	case command.Archive != nil:
		return s.archive(ctx, projectID, command, lifecycle, evidence)
	default:
		return s.edit(ctx, projectID, command, lifecycle, evidence)
	}
}

func (s *Service) edit(ctx context.Context, projectID graph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, evidence authoring.CommandEvidence) (Result, error) {
	if lifecycle.Draft == nil {
		return Result{}, fmt.Errorf("%w: dashboard has no draft", authoring.ErrConflict)
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return Result{}, fmt.Errorf("%w: archived dashboard cannot receive draft revisions", authoring.ErrConflict)
	}
	if !sameToken(lifecycle.Draft.Revision, command.ExpectedRevision) {
		return Result{}, fmt.Errorf("%w: expected revision does not match current draft", authoring.ErrStaleRevision)
	}
	current, err := s.repository.GetRevision(ctx, projectID, lifecycle.ID, lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		return Result{}, err
	}
	nextID, err := s.newRevisionID()
	if err != nil {
		return Result{}, fmt.Errorf("allocate revision id: %w", err)
	}
	nextLifecycle, revision, err := authoring.ApplyEdit(lifecycle, current, command, nextID, current.Number+1, evidence.OccurredAt)
	if err != nil {
		return Result{}, err
	}
	appended, err := s.repository.AppendDraft(ctx, authoring.AppendDraftInput{ProjectID: projectID, DashboardID: command.DashboardID, ExpectedDraftRevision: command.ExpectedRevision, Revision: revision, Next: nextLifecycle, Evidence: evidence})
	if err != nil {
		return Result{}, err
	}
	currentLifecycle, err := s.repository.Get(ctx, projectID, command.DashboardID)
	if err != nil {
		return Result{}, err
	}
	return Result{Revision: appended.Token(), Lifecycle: currentLifecycle}, nil
}

func (s *Service) publish(ctx context.Context, projectID graph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, evidence authoring.CommandEvidence) (Result, error) {
	if lifecycle.Draft == nil {
		return Result{}, fmt.Errorf("%w: dashboard has no draft", authoring.ErrConflict)
	}
	if command.DraftID != lifecycle.Draft.ID {
		return Result{}, fmt.Errorf("%w: command belongs to draft %q", authoring.ErrInvalidAuthoring, command.DraftID)
	}
	if !sameToken(lifecycle.Draft.Revision, command.ExpectedRevision) {
		return Result{}, fmt.Errorf("%w: expected revision does not match current draft", authoring.ErrStaleRevision)
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return Result{}, fmt.Errorf("%w: archived dashboard cannot be published", authoring.ErrConflict)
	}
	current, err := s.repository.GetRevision(ctx, projectID, lifecycle.ID, lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		return Result{}, err
	}
	compilation, err := s.compiler.Compile(ctx, projectID, lifecycle.SemanticModel, current.Document)
	if err != nil {
		return Result{}, err
	}
	if compilation.Definition.ID != current.Document.ID.String() || compilation.Definition.Title != current.Document.Title {
		return Result{}, fmt.Errorf("%w: compiler definition identity does not match authored dashboard", authoring.ErrInvalidAuthoring)
	}
	if compilation.Definition.SemanticModel != lifecycle.SemanticModel.String() || compilation.Definition.SemanticModel != current.Document.SemanticModel.String() {
		return Result{}, fmt.Errorf("%w: compiler semantic model does not match authored lifecycle", authoring.ErrInvalidAuthoring)
	}
	if err := compilation.SemanticIdentity.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: compiler semantic serving identity is required: %v", authoring.ErrInvalidAuthoring, err)
	}
	compiled, err := authoring.NewCompiledRevision(projectID, command.DashboardID, current.Token(), compilation.Definition, compilation.SemanticIdentity, evidence.OccurredAt)
	if err != nil {
		return Result{}, err
	}
	published, err := s.repository.Publish(ctx, authoring.PublishInput{ProjectID: projectID, DashboardID: command.DashboardID, ExpectedDraftRevision: command.ExpectedRevision, Published: authoring.Published{Revision: current.Token(), Compilation: compiled.Token(), PublishedAt: evidence.OccurredAt, Provenance: command.Provenance}, Compilation: compiled, Evidence: evidence})
	if err != nil {
		return Result{}, err
	}
	return Result{Revision: current.Token(), Lifecycle: published}, nil
}

func (s *Service) archive(ctx context.Context, projectID graph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, evidence authoring.CommandEvidence) (Result, error) {
	token := currentToken(lifecycle)
	if token.IsZero() || !sameToken(token, command.ExpectedRevision) {
		return Result{}, fmt.Errorf("%w: expected revision does not match current lifecycle", authoring.ErrStaleRevision)
	}
	archived, err := s.repository.Archive(ctx, authoring.ArchiveInput{ProjectID: projectID, DashboardID: command.DashboardID, ExpectedCurrentRevision: command.ExpectedRevision, Evidence: evidence})
	if err != nil {
		return Result{}, err
	}
	return Result{Revision: token, Lifecycle: archived}, nil
}

func (s *Service) utcNow() (time.Time, error) {
	now := s.now()
	if now.IsZero() || now.Location() != time.UTC {
		return time.Time{}, fmt.Errorf("dashboard authoring clock must return a non-zero UTC timestamp")
	}
	return now, nil
}

func currentToken(lifecycle authoring.DashboardLifecycle) authoring.RevisionToken {
	if lifecycle.Draft != nil {
		return lifecycle.Draft.Revision
	}
	if lifecycle.Published != nil {
		return lifecycle.Published.Revision
	}
	return authoring.RevisionToken{}
}

func sameToken(left, right authoring.RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}

func slugForTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	var b strings.Builder
	dash := false
	for _, r := range title {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
