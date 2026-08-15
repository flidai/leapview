// Package sourceadapter is the application boundary for lossless dashboard
// source access. It intentionally knows how to obtain two kinds of authored
// source, but does not know about Git, checkouts, deployment, or data/model
// mutation.
package sourceadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/runtimehost"
)

// SourceKind is a closed discriminator. A workspace source is owned by the
// authoring repository; a project source is retained by the active runtime
// artifact. They have intentionally different provenance contracts.
type SourceKind string

const (
	SourceWorkspace SourceKind = "workspace"
	SourceProject   SourceKind = "project"
)

func (k SourceKind) Valid() bool { return k == SourceWorkspace || k == SourceProject }

// SourceRef identifies a dashboard without granting access to its content.
// WorkspaceID is the scope of the source. Fork requests may choose a
// different target workspace explicitly.
type SourceRef struct {
	Kind        SourceKind
	WorkspaceID string
	DashboardID authoring.DashboardID
}

func (r SourceRef) validate() error {
	if !r.Kind.Valid() {
		return fmt.Errorf("invalid dashboard source kind %q", r.Kind)
	}
	if strings.TrimSpace(r.WorkspaceID) == "" {
		return fmt.Errorf("dashboard source workspace id is required")
	}
	if err := r.DashboardID.Validate(); err != nil {
		return err
	}
	return nil
}

// WorkspaceProvenance records the exact published authored revision selected
// from the authoring repository. Repository/ref metadata is evidence only;
// no checkout identity is used as source authority.
type WorkspaceProvenance struct {
	WorkspaceID       string
	DashboardID       authoring.DashboardID
	PublishedRevision authoring.RevisionToken
	SourceEvidence    *authoring.SourceMetadata
}

// ProjectProvenance deliberately has no RevisionToken. A project artifact's
// retained source is identified by its immutable serving state and authored
// path, not by a fabricated authoring revision.
type ProjectProvenance struct {
	WorkspaceID    string
	DashboardID    authoring.DashboardID
	ServingStateID string
	Path           string
}

// Provenance is a discriminated union. Exactly one branch is populated for a
// loaded source, and callers can inspect Kind before reading branch details.
type Provenance struct {
	Kind      SourceKind
	Workspace *WorkspaceProvenance
	Project   *ProjectProvenance
}

// Source is a complete authored dashboard source. Document and source
// metadata are detached copies and are safe for callers to mutate.
type Source struct {
	Ref        SourceRef
	Document   authoring.Dashboard
	Metadata   projectartifact.AuthoredDashboardMetadata
	Lifecycle  *authoring.DashboardLifecycle
	Provenance Provenance
}

// PublishedRepository is intentionally narrower than authoring.Repository:
// source loading needs only lifecycle and exact retained revision reads.
type PublishedRepository interface {
	Get(context.Context, string, authoring.DashboardID) (authoring.DashboardLifecycle, error)
	GetRevision(context.Context, string, authoring.DashboardID, authoring.RevisionID) (authoring.Revision, error)
}

// ProjectRuntime is the only capability sourceadapter takes from a runtime.
// It prevents adapters from reaching into DuckDB, deployment state, or
// checkout paths. Implementations must return a fresh detached source.
type ProjectRuntime interface {
	AuthoredDashboardSource(string) (projectartifact.AuthoredDashboardSource, bool)
}

// AcquireRuntime acquires one active runtime lease for a source workspace.
// The callback keeps this package independent of registry topology while
// retaining the runtimehost lease's lifetime and generation guarantees.
type AcquireRuntime func(context.Context, string) (runtimehost.Lease, error)

// Options wires application-owned capabilities. Repository and Authorizer
// are required for all operations. AcquireRuntime is required only for
// project source operations. Authoring is the existing transactional service
// used for forks; adapters never write its repository directly.
type Options struct {
	Repository     PublishedRepository
	Authorizer     service.Authorizer
	AcquireRuntime AcquireRuntime
	Authoring      *service.Service
}

type Adapter struct {
	repository     PublishedRepository
	authorizer     service.Authorizer
	acquireRuntime AcquireRuntime
	authoring      *service.Service
}

func New(options Options) (*Adapter, error) {
	if options.Repository == nil {
		return nil, fmt.Errorf("dashboard source repository is required")
	}
	if options.Authorizer == nil {
		return nil, fmt.Errorf("dashboard source authorizer is required")
	}
	if options.Authoring == nil {
		return nil, fmt.Errorf("dashboard authoring service is required")
	}
	return &Adapter{
		repository: options.Repository, authorizer: options.Authorizer,
		acquireRuntime: options.AcquireRuntime, authoring: options.Authoring,
	}, nil
}

// Load resolves one exact authored source. Workspace loading reads the
// published lifecycle first, authorizes VIEW, and then reads exactly the
// published revision pointer; it never decompiles a compiled definition or
// follows a newer draft. Project loading authorizes before touching source
// bytes and obtains them from the same active runtime lease it returns from.
func (a *Adapter) Load(ctx context.Context, ref SourceRef, actorID string) (Source, error) {
	if a == nil {
		return Source{}, fmt.Errorf("dashboard source adapter is not configured")
	}
	if err := ref.validate(); err != nil {
		return Source{}, err
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return Source{}, fmt.Errorf("actor id is required")
	}
	switch ref.Kind {
	case SourceWorkspace:
		return a.loadWorkspace(ctx, ref, actorID)
	case SourceProject:
		return a.loadProject(ctx, ref, actorID)
	default:
		return Source{}, fmt.Errorf("invalid dashboard source kind %q", ref.Kind)
	}
}

func (a *Adapter) loadWorkspace(ctx context.Context, ref SourceRef, actorID string) (Source, error) {
	// Lifecycle metadata is itself protected source detail. Authorize on the
	// scoped dashboard object before loading it; owner/model fields are
	// deliberately unavailable until after this boundary.
	if err := a.authorizeView(ctx, actorID, ref, "", ""); err != nil {
		return Source{}, err
	}
	lifecycle, err := a.repository.Get(ctx, ref.WorkspaceID, ref.DashboardID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) || errors.Is(err, authoring.ErrSourceUnavailable) {
			return Source{}, sourceUnavailable(ref, err)
		}
		return Source{}, err
	}
	if lifecycle.WorkspaceID != ref.WorkspaceID || lifecycle.ID != ref.DashboardID {
		return Source{}, fmt.Errorf("dashboard source lifecycle identity does not match request")
	}
	if err := lifecycle.Validate(); err != nil {
		return Source{}, err
	}
	if lifecycle.Status != authoring.LifecycleStatusPublished || lifecycle.Published == nil {
		return Source{}, sourceUnavailable(ref, fmt.Errorf("dashboard is not published"))
	}
	publishedToken := lifecycle.Published.Revision
	revision, err := a.repository.GetRevision(ctx, ref.WorkspaceID, ref.DashboardID, publishedToken.RevisionID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) || errors.Is(err, authoring.ErrSourceUnavailable) {
			return Source{}, sourceUnavailable(ref, err)
		}
		return Source{}, err
	}
	if err := revision.Validate(); err != nil {
		return Source{}, err
	}
	if revision.DashboardID != ref.DashboardID || !sameToken(revision.Token(), publishedToken) {
		return Source{}, fmt.Errorf("published revision pointer does not match retained authored revision")
	}
	if strings.TrimSpace(revision.Document.SemanticModel) != strings.TrimSpace(lifecycle.SemanticModel) {
		return Source{}, fmt.Errorf("published authored revision semantic model does not match lifecycle")
	}
	document, err := revision.Document.Clone()
	if err != nil {
		return Source{}, err
	}
	metadata := projectartifact.AuthoredDashboardMetadata{
		Workspace: ref.WorkspaceID, Name: ref.DashboardID.String(), Title: lifecycle.Title,
		Owner: lifecycle.OwnerPrincipalID,
	}
	var evidence *authoring.SourceMetadata
	if revision.Provenance.Source != nil {
		copy := *revision.Provenance.Source
		if revision.Provenance.Source.Metadata != nil {
			copy.Metadata = make(map[string]string, len(revision.Provenance.Source.Metadata))
			for key, value := range revision.Provenance.Source.Metadata {
				copy.Metadata[key] = value
			}
		}
		evidence = &copy
	}
	return Source{
		Ref: ref, Document: document, Metadata: metadata,
		Lifecycle: cloneLifecycle(lifecycle),
		Provenance: Provenance{Kind: SourceWorkspace, Workspace: &WorkspaceProvenance{
			WorkspaceID: ref.WorkspaceID, DashboardID: ref.DashboardID,
			PublishedRevision: publishedToken, SourceEvidence: evidence,
		}},
	}, nil
}

func (a *Adapter) loadProject(ctx context.Context, ref SourceRef, actorID string) (Source, error) {
	// A project source has no authoring lifecycle to inspect before VIEW. The
	// dashboard object itself is not touched until this scoped decision passes.
	if err := a.authorizeView(ctx, actorID, ref, "", ""); err != nil {
		return Source{}, err
	}
	if a.acquireRuntime == nil {
		return Source{}, fmt.Errorf("project dashboard runtime provider is required")
	}
	lease, err := a.acquireRuntime(ctx, ref.WorkspaceID)
	if err != nil {
		return Source{}, err
	}
	if lease == nil {
		return Source{}, fmt.Errorf("project dashboard runtime provider returned a nil lease")
	}
	defer lease.Release()
	runtime := lease.Runtime()
	projectRuntime, ok := runtime.(ProjectRuntime)
	if !ok || projectRuntime == nil {
		return Source{}, sourceUnavailable(ref, fmt.Errorf("active runtime does not retain authored dashboard sources"))
	}
	retained, ok := projectRuntime.AuthoredDashboardSource(ref.DashboardID.String())
	if !ok {
		return Source{}, sourceUnavailable(ref, nil)
	}
	if strings.TrimSpace(retained.Metadata.Workspace) != strings.TrimSpace(ref.WorkspaceID) ||
		strings.TrimSpace(retained.Metadata.Name) != ref.DashboardID.String() || retained.Document.ID != ref.DashboardID.String() {
		return Source{}, fmt.Errorf("retained project source identity does not match request")
	}
	if err := retained.Document.ValidateDraftStructure(); err != nil {
		return Source{}, err
	}
	document, err := retained.Document.Clone()
	if err != nil {
		return Source{}, err
	}
	metadata := retained.Metadata
	metadata.Tags = append([]string(nil), retained.Metadata.Tags...)
	return Source{
		Ref: ref, Document: document, Metadata: metadata,
		Provenance: Provenance{Kind: SourceProject, Project: &ProjectProvenance{
			WorkspaceID: ref.WorkspaceID, DashboardID: ref.DashboardID,
			ServingStateID: string(lease.ServingStateID()), Path: retained.Path,
		}},
	}, nil
}

func (a *Adapter) authorizeView(ctx context.Context, actorID string, ref SourceRef, owner, semanticModel string) error {
	return a.authorizer.Authorize(ctx, service.AuthorizationRequest{
		ActorID: actorID, WorkspaceID: ref.WorkspaceID, DashboardID: ref.DashboardID,
		OwnerPrincipalID: owner, SemanticModel: semanticModel, Action: authoring.AuthorizationActionView,
	})
}

// ExportRequest identifies a source and the actor requesting its bytes.
type ExportRequest struct {
	Source  SourceRef
	ActorID string
}

// Export emits the project's canonical YAML resource from the lossless
// authored document. Compiled definitions are never accepted as an export
// source and therefore cannot silently lose authoring-only fields.
func (a *Adapter) Export(ctx context.Context, request ExportRequest) ([]byte, error) {
	source, err := a.Load(ctx, request.Source, request.ActorID)
	if err != nil {
		return nil, err
	}
	return projectcompiler.ExportDashboard(source.Document, projectcompiler.DashboardExportMetadata{
		Name: source.Metadata.Name, Workspace: source.Metadata.Workspace,
		Title: source.Metadata.Title, Description: source.Metadata.Description,
		Owner: source.Metadata.Owner, Tags: append([]string(nil), source.Metadata.Tags...),
	})
}

// ForkRequest copies a source into a new private authoring draft. Source
// workspace forks use the repository's exact published revision and retain
// ForkEvidence. Project forks use the retained runtime document and carry
// serving-state/path evidence without inventing a revision. Neither path
// compiles, publishes, deploys, or mutates semantic models/data.
type ForkRequest struct {
	Source            SourceRef
	TargetWorkspaceID string
	ActorID           string
	OwnerPrincipalID  string
	Title             string
	Slug              string
	Origin            authoring.Origin
	ConversationID    string
	ToolCallID        string
}

func (a *Adapter) Fork(ctx context.Context, request ForkRequest) (service.Result, error) {
	if a == nil || a.authoring == nil {
		return service.Result{}, fmt.Errorf("dashboard source adapter is not configured")
	}
	targetWorkspaceID := strings.TrimSpace(request.TargetWorkspaceID)
	if targetWorkspaceID == "" {
		targetWorkspaceID = strings.TrimSpace(request.Source.WorkspaceID)
	}
	if targetWorkspaceID == "" {
		return service.Result{}, fmt.Errorf("target workspace id is required")
	}
	if err := request.Source.validate(); err != nil {
		return service.Result{}, err
	}
	actorID := strings.TrimSpace(request.ActorID)
	if actorID == "" {
		return service.Result{}, fmt.Errorf("actor id is required")
	}
	switch request.Source.Kind {
	case SourceWorkspace:
		if targetWorkspaceID != request.Source.WorkspaceID {
			return service.Result{}, fmt.Errorf("workspace source forks must remain in the source workspace")
		}
		// Load performs VIEW before any source content is exposed. Service.Fork
		// then re-reads and transactionally creates the draft, preserving its
		// command/repository invariants and ForkEvidence token.
		if _, err := a.Load(ctx, request.Source, actorID); err != nil {
			return service.Result{}, err
		}
		return a.authoring.Fork(ctx, service.ForkRequest{
			WorkspaceID: request.Source.WorkspaceID, SourceDashboardID: request.Source.DashboardID,
			ActorID: actorID, OwnerPrincipalID: request.OwnerPrincipalID, Title: request.Title, Slug: request.Slug,
			Origin: request.Origin, ConversationID: request.ConversationID, ToolCallID: request.ToolCallID,
		})
	case SourceProject:
		source, err := a.Load(ctx, request.Source, actorID)
		if err != nil {
			return service.Result{}, err
		}
		sourceEvidence, forkEvidence := projectSourceEvidence(source)
		return a.authoring.CreateFromDocument(ctx, service.CreateFromDocumentRequest{
			WorkspaceID: targetWorkspaceID, ActorID: actorID, OwnerPrincipalID: request.OwnerPrincipalID,
			Document: source.Document, Title: request.Title, Slug: request.Slug, Origin: request.Origin,
			Source: sourceEvidence, ForkedFrom: forkEvidence, ConversationID: request.ConversationID,
			ToolCallID: request.ToolCallID, BaseSemanticServingStateID: source.Provenance.Project.ServingStateID,
		})
	default:
		return service.Result{}, fmt.Errorf("invalid dashboard source kind %q", request.Source.Kind)
	}
}

func projectSourceEvidence(source Source) (*authoring.SourceMetadata, *authoring.ForkEvidence) {
	project := source.Provenance.Project
	if project == nil {
		return nil, nil
	}
	evidence := &authoring.SourceMetadata{Path: project.Path}
	fork := &authoring.ForkEvidence{Kind: authoring.ForkSourceProject, Project: &authoring.ProjectForkEvidence{
		SourceWorkspaceID: project.WorkspaceID, SourceDashboardID: project.DashboardID,
		ServingStateID: project.ServingStateID, Path: project.Path,
	}}
	return evidence, fork
}

func sameToken(left, right authoring.RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}

func cloneLifecycle(lifecycle authoring.DashboardLifecycle) *authoring.DashboardLifecycle {
	copy := lifecycle
	if lifecycle.Draft != nil {
		draft := *lifecycle.Draft
		draft.Provenance = lifecycle.Draft.Provenance.Clone()
		copy.Draft = &draft
	}
	if lifecycle.Published != nil {
		published := *lifecycle.Published
		published.Provenance = lifecycle.Published.Provenance.Clone()
		copy.Published = &published
	}
	return &copy
}

var ErrSourceUnavailable = errors.New("dashboard authored source is unavailable")

type SourceUnavailableError struct {
	Kind        SourceKind
	WorkspaceID string
	DashboardID authoring.DashboardID
	Cause       error
}

func (e *SourceUnavailableError) Error() string {
	if e == nil {
		return ErrSourceUnavailable.Error()
	}
	return fmt.Sprintf("dashboard %s source %s/%s is unavailable", e.Kind, e.WorkspaceID, e.DashboardID)
}

func (e *SourceUnavailableError) Unwrap() error {
	if e == nil {
		return ErrSourceUnavailable
	}
	if e.Cause == nil {
		return errors.Join(ErrSourceUnavailable, authoring.ErrSourceUnavailable)
	}
	return errors.Join(ErrSourceUnavailable, authoring.ErrSourceUnavailable, e.Cause)
}

func sourceUnavailable(ref SourceRef, cause error) error {
	return &SourceUnavailableError{Kind: ref.Kind, WorkspaceID: ref.WorkspaceID, DashboardID: ref.DashboardID, Cause: cause}
}
