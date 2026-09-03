// Package application is the canonical transport-facing dashboard authoring
// boundary. It composes the existing transactional authoring, governed
// catalog, source, and preview services without owning any of their domain
// state or business rules.
package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

// Options wires the already-built authoring service and the read-side ports
// used by the composed application boundary. Runtime acquisition remains a
// callback so the transport does not depend on registry topology.
type Options struct {
	Authoring      *authoringservice.Service
	Repository     authoring.Repository
	Authorizer     authoringservice.Authorizer
	Compiler       authoringservice.Compiler
	AcquireRuntime sourceadapter.AcquireRuntime
}

// Application is the small canonical dashboard authoring application
// surface. The source adapter is built once; catalog and preview services are
// created only for the request that needs them, each with a fixed project
// provider.
type Application struct {
	authoring      *authoringservice.Service
	compiler       authoringservice.Compiler
	sources        *sourceadapter.Adapter
	repository     authoring.Repository
	authorizer     authoringservice.Authorizer
	acquireRuntime sourceadapter.AcquireRuntime
}

// New validates the composition ports and builds the source adapter once.
// The runtime callback is guarded at this boundary so every composed
// operation receives a non-empty runtime and serving-state lease.
func New(options Options) (*Application, error) {
	if options.Authoring == nil {
		return nil, fmt.Errorf("dashboard authoring service is required")
	}
	if options.Repository == nil {
		return nil, fmt.Errorf("dashboard authoring repository is required")
	}
	if options.Authorizer == nil {
		return nil, fmt.Errorf("dashboard authoring authorizer is required")
	}
	if options.AcquireRuntime == nil {
		return nil, fmt.Errorf("dashboard authoring runtime provider is required")
	}

	acquireRuntime := guardedAcquire(options.AcquireRuntime)
	sources, err := sourceadapter.New(sourceadapter.Options{
		Repository:     options.Repository,
		Authorizer:     options.Authorizer,
		AcquireRuntime: acquireRuntime,
		Authoring:      options.Authoring,
	})
	if err != nil {
		return nil, err
	}
	return &Application{
		authoring:      options.Authoring,
		compiler:       options.Compiler,
		sources:        sources,
		repository:     options.Repository,
		authorizer:     options.Authorizer,
		acquireRuntime: acquireRuntime,
	}, nil
}

// NewGenerationRevalidator binds the durable authoring repository and the
// runtime-backed compiler to the generation revalidation service. The
// resulting observer is intentionally post-activation; it records evidence
// without participating in the activation transaction.
func (a *Application) NewGenerationRevalidator(now func() time.Time) (*authoring.GenerationRevalidator, error) {
	if a == nil || a.repository == nil || a.compiler == nil {
		return nil, fmt.Errorf("dashboard generation revalidation dependencies are not configured")
	}
	store, ok := a.repository.(authoring.RevalidationStore)
	if !ok {
		return nil, fmt.Errorf("dashboard authoring repository does not support generation revalidation")
	}
	return authoring.NewGenerationRevalidator(store, revalidationCompiler{compiler: a.compiler, now: now}, now)
}

// Create creates one dashboard draft through the transactional authoring
// service.
func (a *Application) Create(ctx context.Context, request authoringservice.CreateRequest) (authoringservice.Result, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, err
	}
	projectID, err := projectID(request.ProjectID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	request.ProjectID = projectID
	return a.authoring.Create(ctx, request)
}

// Execute executes one typed authoring command through the transactional
// service.
func (a *Application) Execute(ctx context.Context, project projectgraph.ResourceID, command authoring.Command) (authoringservice.Result, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, err
	}
	projectID, err := projectID(project)
	if err != nil {
		return authoringservice.Result{}, err
	}
	return a.authoring.Execute(ctx, projectID, command)
}

// List returns the governed dashboard catalog for one project. A provider
// is fixed to the normalized request project and exists only for this call.
func (a *Application) List(ctx context.Context, request catalog.ListRequest) (catalog.ListResult, error) {
	if err := a.validate(); err != nil {
		return catalog.ListResult{}, err
	}
	projectID, err := projectID(request.ProjectID)
	if err != nil {
		return catalog.ListResult{}, err
	}
	service, err := a.newCatalogService(projectID)
	if err != nil {
		return catalog.ListResult{}, err
	}
	request.ProjectID = projectID
	return service.List(ctx, request)
}

// Get returns one governed dashboard for one project. The runtime provider
// is fixed to the requested project and cannot be reused for another one.
func (a *Application) Get(ctx context.Context, request catalog.GetRequest) (catalog.Dashboard, error) {
	if err := a.validate(); err != nil {
		return catalog.Dashboard{}, err
	}
	projectID, err := projectID(request.ProjectID)
	if err != nil {
		return catalog.Dashboard{}, err
	}
	service, err := a.newCatalogService(projectID)
	if err != nil {
		return catalog.Dashboard{}, err
	}
	request.ProjectID = projectID
	return service.Get(ctx, request)
}

// Fork copies an authored source into a private draft through the source
// adapter and existing authoring service.
func (a *Application) Fork(ctx context.Context, request sourceadapter.ForkRequest) (authoringservice.Result, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, err
	}
	project, err := projectID(request.Source.ProjectID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	request.Source.ProjectID = project
	return a.sources.Fork(ctx, request)
}

// ExportYAML exports the exact authored source as canonical project YAML.
func (a *Application) ExportYAML(ctx context.Context, request sourceadapter.ExportRequest) ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	project, err := projectID(request.Source.ProjectID)
	if err != nil {
		return nil, err
	}
	request.Source.ProjectID = project
	return a.sources.Export(ctx, request)
}

// ExportDraftYAML exports the repository-authoritative current draft source.
// The source adapter resolves the lifecycle's draft revision under the same
// project and authorization boundary as other source operations.
func (a *Application) ExportDraftYAML(ctx context.Context, request sourceadapter.ExportRequest) ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	project, err := projectID(request.Source.ProjectID)
	if err != nil {
		return nil, err
	}
	request.Source.ProjectID = project
	return a.sources.ExportDraft(ctx, request)
}

// PublishedCompilationReader exposes the read-only compilation port needed by
// dashboard runtime resolution without exposing the repository implementation
// or requiring application composition to import authoring internals.
func (a *Application) PublishedCompilationReader() dashboardresolver.PublishedCompilationReader {
	if a == nil {
		return nil
	}
	return a.repository
}

// Preview renders one exact draft revision through a request-scoped provider
// and the existing read-only preview service.
func (a *Application) Preview(ctx context.Context, request preview.PreviewRequest) (preview.Preview, error) {
	if err := a.validate(); err != nil {
		return preview.Preview{}, err
	}
	projectID, err := projectID(request.ProjectID)
	if err != nil {
		return preview.Preview{}, err
	}
	service, err := preview.NewService(preview.Options{
		Repository: a.repository,
		Authorizer: a.authorizer,
		Provider:   projectProvider{projectID: projectID, acquire: a.acquireRuntime},
	})
	if err != nil {
		return preview.Preview{}, err
	}
	request.ProjectID = projectID
	return service.Preview(ctx, request)
}

// Compile strictly compiles one exact draft revision without executing a
// dashboard page. Filter-option loading uses this path so a failing visual
// query cannot hide an otherwise valid governed filter contract.
func (a *Application) Compile(ctx context.Context, request preview.CompileRequest) (preview.Compilation, error) {
	if err := a.validate(); err != nil {
		return preview.Compilation{}, err
	}
	projectID, err := projectID(request.ProjectID)
	if err != nil {
		return preview.Compilation{}, err
	}
	service, err := preview.NewService(preview.Options{
		Repository: a.repository,
		Authorizer: a.authorizer,
		Provider:   projectProvider{projectID: projectID, acquire: a.acquireRuntime},
	})
	if err != nil {
		return preview.Compilation{}, err
	}
	request.ProjectID = projectID
	return service.Compile(ctx, request)
}

// Builder returns the governed dashboard-builder bootstrap for one exact
// project draft. The runtime provider is scoped to the normalized request
// project and the builder service owns the single lease for this call.
func (a *Application) Builder(ctx context.Context, request builderview.Request) (uisignals.DashboardBuilderSignal, error) {
	if err := a.validate(); err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	if err := request.ProjectID.Validate(); err != nil {
		return uisignals.DashboardBuilderSignal{}, fmt.Errorf("project id is invalid: %w", err)
	}
	service, err := builderview.NewService(builderview.Options{
		Provider:   projectProvider{projectID: request.ProjectID, acquire: a.acquireRuntime},
		Repository: a.repository,
		Authorizer: a.authorizer,
	})
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	return service.Build(ctx, request)
}

// AuthorizeDashboardEdit performs the repository-backed edit decision used by
// browser routes before a draft is exposed. Newly created private dashboards
// are intentionally absent from the active serving graph; the authoring
// service loads their lifecycle and applies the owner/project-role context.
func (a *Application) AuthorizeDashboardEdit(ctx context.Context, requestedProject projectgraph.ResourceID, actorID string, dashboardID authoring.DashboardID) error {
	return a.authorizeDashboardAction(ctx, requestedProject, actorID, dashboardID, authoring.AuthorizationActionEdit)
}

// AuthorizeDashboardManage performs the repository-backed manage decision
// used before lifecycle operations such as archive are exposed.
func (a *Application) AuthorizeDashboardManage(ctx context.Context, requestedProject projectgraph.ResourceID, actorID string, dashboardID authoring.DashboardID) error {
	return a.authorizeDashboardAction(ctx, requestedProject, actorID, dashboardID, authoring.AuthorizationActionArchive)
}

func (a *Application) authorizeDashboardAction(ctx context.Context, requestedProject projectgraph.ResourceID, actorID string, dashboardID authoring.DashboardID, action authoring.AuthorizationAction) error {
	if err := a.validate(); err != nil {
		return err
	}
	project, err := projectID(requestedProject)
	if err != nil {
		return err
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return fmt.Errorf("actor id is required")
	}
	if err := authoring.ValidateDashboardID(dashboardID); err != nil {
		return err
	}
	lifecycle, err := a.repository.Get(ctx, project, dashboardID)
	if err != nil {
		return err
	}
	if lifecycle.ProjectID != project || lifecycle.ID != dashboardID {
		return fmt.Errorf("dashboard lifecycle identity does not match request")
	}
	return a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actorID, ProjectID: project, DashboardID: dashboardID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
		Action: action,
	})
}

func (a *Application) validate() error {
	if a == nil || a.authoring == nil || a.sources == nil || a.repository == nil || a.authorizer == nil || a.acquireRuntime == nil {
		return fmt.Errorf("dashboard authoring application is not configured")
	}
	return nil
}

func (a *Application) newCatalogService(projectID projectgraph.ResourceID) (*catalog.Service, error) {
	return catalog.NewService(catalog.Options{
		Provider:   projectProvider{projectID: projectID, acquire: a.acquireRuntime},
		Repository: a.repository,
		Authorizer: a.authorizer,
	})
}

func projectID(value projectgraph.ResourceID) (projectgraph.ResourceID, error) {
	if err := value.Validate(); err != nil {
		return "", fmt.Errorf("project id is invalid: %w", err)
	}
	return value, nil
}

// guardedAcquire is shared by source, catalog, and preview paths. The
// callback owns acquisition; downstream services own release after a valid
// lease is returned. Invalid leases are released here exactly once.
func guardedAcquire(acquire sourceadapter.AcquireRuntime) sourceadapter.AcquireRuntime {
	return func(ctx context.Context) (runtimehost.Lease, error) {
		lease, err := acquire(ctx)
		if err != nil {
			return nil, err
		}
		if lease == nil {
			return nil, fmt.Errorf("dashboard authoring runtime lease is empty")
		}
		if lease.Runtime() == nil {
			lease.Release()
			return nil, fmt.Errorf("dashboard authoring runtime is empty")
		}
		if identity := lease.Identity(); identity.Validate() != nil || identity.GenerationID == "" {
			lease.Release()
			return nil, fmt.Errorf("dashboard authoring serving-state identity is empty")
		}
		return lease, nil
	}
}

// projectProvider closes over one normalized project. It intentionally
// implements only runtimehost.Provider, so catalog and preview cannot acquire
// a different project through a request after construction.
type projectProvider struct {
	projectID projectgraph.ResourceID
	acquire   sourceadapter.AcquireRuntime
}

func (p projectProvider) Acquire(ctx context.Context) (runtimehost.Lease, error) {
	if err := p.projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project id is invalid: %w", err)
	}
	if p.acquire == nil {
		return nil, fmt.Errorf("dashboard authoring runtime provider is required")
	}
	lease, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if lease.Identity().ProjectID != p.projectID {
		lease.Release()
		return nil, fmt.Errorf("dashboard authoring runtime project %q does not match requested project %q", lease.Identity().ProjectID, p.projectID)
	}
	return lease, nil
}

var _ runtimehost.Provider = projectProvider{}

type revalidationCompiler struct {
	compiler authoringservice.Compiler
	now      func() time.Time
}

func (c revalidationCompiler) Compile(ctx context.Context, generation authoring.RevalidationGeneration, revision authoring.Revision) (authoring.CompiledRevision, error) {
	if c.compiler == nil {
		return authoring.CompiledRevision{}, fmt.Errorf("dashboard authoring compiler is not configured")
	}
	if err := generation.Validate(); err != nil {
		return authoring.CompiledRevision{}, err
	}
	semanticModelID := projectgraph.ResourceID(revision.Document.Spec.SemanticModel)
	compiled, err := c.compiler.Compile(ctx, generation.Identity.ProjectID, semanticModelID, revision.Document)
	if err != nil {
		return authoring.CompiledRevision{}, err
	}
	if compiled.SemanticIdentity != generation.Identity {
		return authoring.CompiledRevision{}, fmt.Errorf("recompiled dashboard serving identity %#v does not match activated generation %#v", compiled.SemanticIdentity, generation.Identity)
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	return authoring.NewCompiledRevision(generation.Identity.ProjectID, authoring.DashboardID(revision.Document.Metadata.ID), revision.Token(), compiled.Definition, generation.Identity, now().UTC())
}
