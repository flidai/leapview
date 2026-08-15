// Package service contains the application boundary for dashboard authoring.
// It deliberately depends on the authoring contracts and repository ports,
// but does not know about HTTP, Datastar, grants, Git, or serving-state
// publication.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
)

// AuthorizationRequest is the single authorization boundary for every
// authoring mutation. The owner and semantic-model identities are included so
// an adapter can make one scoped decision without looking up more state.
type AuthorizationRequest struct {
	ActorID          string
	WorkspaceID      string
	DashboardID      authoring.DashboardID
	OwnerPrincipalID string
	SemanticModel    string
	Action           authoring.AuthorizationAction
}

type Authorizer interface {
	Authorize(context.Context, AuthorizationRequest) error
}

// Compilation is the exact compiler-facing result persisted with a published
// authored revision. The service does not activate a serving generation.
type Compilation struct {
	Definition             dashboarddefinition.Definition
	SemanticServingStateID string
}

type Compiler interface {
	Compile(context.Context, string, string, authoring.Dashboard) (Compilation, error)
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
	Revision  authoring.RevisionToken
	Lifecycle authoring.DashboardLifecycle
}

// CreateRequest creates a named workspace draft. DashboardID is optional; an
// omitted ID is allocated by the injected generator.
type CreateRequest struct {
	WorkspaceID                string
	ActorID                    string
	OwnerPrincipalID           string
	DashboardID                authoring.DashboardID
	Title                      string
	Slug                       string
	SemanticModel              string
	Visibility                 authoring.Visibility
	Origin                     authoring.Origin
	Source                     *authoring.SourceMetadata
	ConversationID             string
	ToolCallID                 string
	BaseSemanticServingStateID string
}

// ForkRequest copies the exact published authored revision of a dashboard into
// a new private draft in the workspace identified by WorkspaceID.
type ForkRequest struct {
	WorkspaceID                string
	SourceDashboardID          authoring.DashboardID
	ActorID                    string
	OwnerPrincipalID           string
	Title                      string
	Slug                       string
	Origin                     authoring.Origin
	Source                     *authoring.SourceMetadata
	ConversationID             string
	ToolCallID                 string
	BaseSemanticServingStateID string
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
	WorkspaceID                string
	ActorID                    string
	OwnerPrincipalID           string
	Document                   authoring.Dashboard
	Title                      string
	Slug                       string
	Origin                     authoring.Origin
	Source                     *authoring.SourceMetadata
	ForkedFrom                 *authoring.ForkEvidence
	ConversationID             string
	ToolCallID                 string
	BaseSemanticServingStateID string
}

// CreateFromDocument creates one private draft from an authored document.
// It performs the same edit authorization and transactional repository create
// as Create, while preserving every authored field in the supplied document.
// No compiler, publication, deployment, or data/model mutation is involved.
func (s *Service) CreateFromDocument(ctx context.Context, input CreateFromDocumentRequest) (Result, error) {
	return s.createDraft(ctx, createDraftInput{
		WorkspaceID: input.WorkspaceID, ActorID: input.ActorID, OwnerPrincipalID: input.OwnerPrincipalID,
		Document: input.Document, Title: input.Title, Slug: input.Slug, Visibility: authoring.VisibilityPrivate,
		Origin: input.Origin, Source: input.Source, ForkedFrom: input.ForkedFrom,
		ConversationID: input.ConversationID, ToolCallID: input.ToolCallID,
		BaseSemanticServingStateID: input.BaseSemanticServingStateID,
	})
}

// Fork copies a published authored revision into a new private draft. It
// never compiles, publishes, deploys, or mutates the source lifecycle.
func (s *Service) Fork(ctx context.Context, input ForkRequest) (Result, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	actorID := strings.TrimSpace(input.ActorID)
	if workspaceID == "" || actorID == "" {
		return Result{}, fmt.Errorf("workspace and actor are required")
	}
	sourceWorkspaceID := workspaceID
	sourceID := input.SourceDashboardID
	if err := sourceID.Validate(); err != nil {
		return Result{}, err
	}

	// Load and authorize the source before reading its immutable revision. This
	// is both the authorization boundary and the required VIEW-before-EDIT
	// ordering for a fork.
	source, err := s.repository.Get(ctx, sourceWorkspaceID, sourceID)
	if err != nil {
		return Result{}, err
	}
	if source.WorkspaceID != sourceWorkspaceID || source.ID != sourceID {
		return Result{}, fmt.Errorf("%w: source lifecycle identity does not match request", authoring.ErrInvalidAuthoring)
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{
		ActorID: actorID, WorkspaceID: sourceWorkspaceID, DashboardID: source.ID,
		OwnerPrincipalID: source.OwnerPrincipalID, SemanticModel: source.SemanticModel,
		Action: authoring.AuthorizationActionView,
	}); err != nil {
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
	sourceRevision, err := s.repository.GetRevision(ctx, sourceWorkspaceID, source.ID, publishedToken.RevisionID)
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
	provenance := authoring.Provenance{
		Origin: origin, ActorID: actorID,
		ConversationID: strings.TrimSpace(input.ConversationID), ToolCallID: strings.TrimSpace(input.ToolCallID),
		BaseSemanticServingStateID: strings.TrimSpace(input.BaseSemanticServingStateID), Source: input.Source,
		ForkedFrom: &authoring.ForkEvidence{Kind: authoring.ForkSourceWorkspace, Workspace: &authoring.WorkspaceForkEvidence{SourceWorkspaceID: sourceWorkspaceID, SourceDashboardID: source.ID, SourceRevision: publishedToken}},
	}
	if err := provenance.Validate(); err != nil {
		return Result{}, err
	}

	document, err := sourceRevision.Document.Clone()
	if err != nil {
		return Result{}, err
	}
	document.ID = targetID.String()
	document.Title = title
	// SemanticModel is intentionally left untouched: a dashboard fork never
	// forks the governed semantic model or its underlying data/schema.
	revision, err := authoring.NewRevision(revisionID, targetID, 1, now, document, provenance)
	if err != nil {
		return Result{}, err
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		WorkspaceID: workspaceID, ID: targetID, OwnerPrincipalID: ownerID, Slug: slug,
		Title: title, SemanticModel: source.SemanticModel, Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: draftID, DashboardID: targetID, Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		return Result{}, err
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{
		ActorID: actorID, WorkspaceID: workspaceID, DashboardID: targetID,
		OwnerPrincipalID: ownerID, SemanticModel: source.SemanticModel, Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return Result{}, err
	}
	created, err := s.repository.Create(ctx, authoring.CreateInput{WorkspaceID: workspaceID, Lifecycle: lifecycle, Revision: revision})
	if err != nil {
		return Result{}, err
	}
	return Result{Revision: revision.Token(), Lifecycle: created}, nil
}

func (s *Service) Create(ctx context.Context, input CreateRequest) (Result, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return Result{}, fmt.Errorf("dashboard title is required")
	}
	semanticModel := strings.TrimSpace(input.SemanticModel)
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
		WorkspaceID: input.WorkspaceID, ActorID: input.ActorID, OwnerPrincipalID: input.OwnerPrincipalID,
		DashboardID: input.DashboardID,
		Document:    authoring.Dashboard{ID: input.DashboardID.String(), Title: title, SemanticModel: semanticModel, Visuals: map[string]authoring.AuthoringVisualization{}, Pages: []dashboardmodel.Page{defaultPage}},
		Title:       title, Slug: input.Slug, Visibility: visibility, Origin: input.Origin, Source: input.Source,
		ConversationID: input.ConversationID, ToolCallID: input.ToolCallID, BaseSemanticServingStateID: input.BaseSemanticServingStateID,
	})
}

type createDraftInput struct {
	WorkspaceID                string
	ActorID                    string
	OwnerPrincipalID           string
	DashboardID                authoring.DashboardID
	Document                   authoring.Dashboard
	Title                      string
	Slug                       string
	Visibility                 authoring.Visibility
	Origin                     authoring.Origin
	Source                     *authoring.SourceMetadata
	ForkedFrom                 *authoring.ForkEvidence
	ConversationID             string
	ToolCallID                 string
	BaseSemanticServingStateID string
}

// createDraft is the single transactional private-draft construction path
// shared by ordinary creation and external authored-source forks. Keeping ID
// allocation, provenance validation, edit authorization, and repository
// insertion together prevents the two entry points from drifting.
func (s *Service) createDraft(ctx context.Context, input createDraftInput) (Result, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	actorID := strings.TrimSpace(input.ActorID)
	ownerID := strings.TrimSpace(input.OwnerPrincipalID)
	if workspaceID == "" || actorID == "" {
		return Result{}, fmt.Errorf("workspace and actor are required")
	}
	if ownerID == "" {
		ownerID = actorID
	}
	semanticModel := strings.TrimSpace(input.Document.SemanticModel)
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
	targetID := input.DashboardID
	var err error
	if targetID == "" {
		targetID, err = s.newDashboardID()
		if err != nil {
			return Result{}, fmt.Errorf("allocate dashboard id: %w", err)
		}
	}
	if err := targetID.Validate(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(input.Document.ID) == "" {
		input.Document.ID = targetID.String()
	}
	if err := input.Document.ValidateDraftStructure(); err != nil {
		return Result{}, err
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
	origin := input.Origin
	if origin == "" {
		origin = authoring.OriginUI
	}
	provenance := authoring.Provenance{
		Origin: origin, ActorID: actorID,
		ConversationID: strings.TrimSpace(input.ConversationID), ToolCallID: strings.TrimSpace(input.ToolCallID),
		BaseSemanticServingStateID: strings.TrimSpace(input.BaseSemanticServingStateID), Source: input.Source, ForkedFrom: input.ForkedFrom,
	}
	if err := provenance.Validate(); err != nil {
		return Result{}, err
	}
	document, err := input.Document.Clone()
	if err != nil {
		return Result{}, err
	}
	document.ID = targetID.String()
	document.Title = title
	revision, err := authoring.NewRevision(revisionID, targetID, 1, now, document, provenance)
	if err != nil {
		return Result{}, err
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		WorkspaceID: workspaceID, ID: targetID, OwnerPrincipalID: ownerID, Slug: slug,
		Title: title, SemanticModel: semanticModel, Visibility: visibility,
		Draft: &authoring.Draft{ID: draftID, DashboardID: targetID, Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		return Result{}, err
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{
		ActorID: actorID, WorkspaceID: workspaceID, DashboardID: targetID,
		OwnerPrincipalID: ownerID, SemanticModel: semanticModel, Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return Result{}, err
	}
	created, err := s.repository.Create(ctx, authoring.CreateInput{WorkspaceID: workspaceID, Lifecycle: lifecycle, Revision: revision})
	if err != nil {
		return Result{}, err
	}
	return Result{Revision: revision.Token(), Lifecycle: created}, nil
}

// Execute is the one mutation path for the typed command union. Authorization
// and command-result lookup happen before stale reducer evaluation so retries
// remain successful even if a later edit has advanced the draft pointer.
func (s *Service) Execute(ctx context.Context, workspaceID string, command authoring.Command) (Result, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return Result{}, fmt.Errorf("workspace id is required")
	}
	if err := command.Validate(); err != nil {
		return Result{}, err
	}
	action, err := command.RequiredAction()
	if err != nil {
		return Result{}, err
	}
	lifecycle, err := s.repository.Get(ctx, workspaceID, command.DashboardID)
	if err != nil {
		return Result{}, err
	}
	if err := s.authorizer.Authorize(ctx, AuthorizationRequest{ActorID: command.Provenance.ActorID, WorkspaceID: workspaceID, DashboardID: lifecycle.ID, OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel, Action: action}); err != nil {
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
	replayed, found, err := s.repository.LookupCommandResult(ctx, workspaceID, command.DashboardID, evidence)
	if err != nil {
		return Result{}, err
	}
	if found {
		if replayed.Revision.IsZero() {
			replayed.Revision = currentToken(lifecycle)
		}
		return Result{Revision: replayed.Revision, Lifecycle: lifecycle}, nil
	}

	switch {
	case command.Publish != nil:
		return s.publish(ctx, workspaceID, command, lifecycle, evidence)
	case command.Archive != nil:
		return s.archive(ctx, workspaceID, command, lifecycle, evidence)
	default:
		return s.edit(ctx, workspaceID, command, lifecycle, evidence)
	}
}

func (s *Service) edit(ctx context.Context, workspaceID string, command authoring.Command, lifecycle authoring.DashboardLifecycle, evidence authoring.CommandEvidence) (Result, error) {
	if lifecycle.Draft == nil {
		return Result{}, fmt.Errorf("%w: dashboard has no draft", authoring.ErrConflict)
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return Result{}, fmt.Errorf("%w: archived dashboard cannot receive draft revisions", authoring.ErrConflict)
	}
	if !sameToken(lifecycle.Draft.Revision, command.ExpectedRevision) {
		return Result{}, fmt.Errorf("%w: expected revision does not match current draft", authoring.ErrStaleRevision)
	}
	current, err := s.repository.GetRevision(ctx, workspaceID, lifecycle.ID, lifecycle.Draft.Revision.RevisionID)
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
	appended, err := s.repository.AppendDraft(ctx, authoring.AppendDraftInput{WorkspaceID: workspaceID, DashboardID: command.DashboardID, ExpectedDraftRevision: command.ExpectedRevision, Revision: revision, Next: nextLifecycle, Evidence: evidence})
	if err != nil {
		return Result{}, err
	}
	currentLifecycle, err := s.repository.Get(ctx, workspaceID, command.DashboardID)
	if err != nil {
		return Result{}, err
	}
	return Result{Revision: appended.Token(), Lifecycle: currentLifecycle}, nil
}

func (s *Service) publish(ctx context.Context, workspaceID string, command authoring.Command, lifecycle authoring.DashboardLifecycle, evidence authoring.CommandEvidence) (Result, error) {
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
	current, err := s.repository.GetRevision(ctx, workspaceID, lifecycle.ID, lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		return Result{}, err
	}
	compilation, err := s.compiler.Compile(ctx, workspaceID, lifecycle.SemanticModel, current.Document)
	if err != nil {
		return Result{}, err
	}
	if compilation.Definition.ID != current.Document.ID || compilation.Definition.Title != current.Document.Title {
		return Result{}, fmt.Errorf("%w: compiler definition identity does not match authored dashboard", authoring.ErrInvalidAuthoring)
	}
	if compilation.Definition.SemanticModel != lifecycle.SemanticModel || compilation.Definition.SemanticModel != current.Document.SemanticModel {
		return Result{}, fmt.Errorf("%w: compiler semantic model does not match authored lifecycle", authoring.ErrInvalidAuthoring)
	}
	if strings.TrimSpace(compilation.SemanticServingStateID) == "" {
		return Result{}, fmt.Errorf("%w: compiler semantic serving state id is required", authoring.ErrInvalidAuthoring)
	}
	compiled, err := authoring.NewCompiledRevision(workspaceID, command.DashboardID, current.Token(), compilation.Definition, compilation.SemanticServingStateID, evidence.OccurredAt)
	if err != nil {
		return Result{}, err
	}
	published, err := s.repository.Publish(ctx, authoring.PublishInput{WorkspaceID: workspaceID, DashboardID: command.DashboardID, ExpectedDraftRevision: command.ExpectedRevision, Published: authoring.Published{Revision: current.Token(), Compilation: compiled.Token(), PublishedAt: evidence.OccurredAt, Provenance: command.Provenance}, Compilation: compiled, Evidence: evidence})
	if err != nil {
		return Result{}, err
	}
	return Result{Revision: current.Token(), Lifecycle: published}, nil
}

func (s *Service) archive(ctx context.Context, workspaceID string, command authoring.Command, lifecycle authoring.DashboardLifecycle, evidence authoring.CommandEvidence) (Result, error) {
	token := currentToken(lifecycle)
	if token.IsZero() || !sameToken(token, command.ExpectedRevision) {
		return Result{}, fmt.Errorf("%w: expected revision does not match current lifecycle", authoring.ErrStaleRevision)
	}
	archived, err := s.repository.Archive(ctx, authoring.ArchiveInput{WorkspaceID: workspaceID, DashboardID: command.DashboardID, ExpectedCurrentRevision: command.ExpectedRevision, Evidence: evidence})
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
