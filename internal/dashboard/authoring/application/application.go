// Package application is the canonical transport-facing dashboard authoring
// boundary. It composes the existing transactional authoring, governed
// catalog, source, and preview services without owning any of their domain
// state or business rules.
package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/runtimehost"
)

// Options wires the already-built authoring service and the read-side ports
// used by the composed application boundary. Runtime acquisition remains a
// callback so the transport does not depend on registry topology.
type Options struct {
	Authoring       *authoringservice.Service
	Repository      authoring.Repository
	Authorizer      authoringservice.Authorizer
	AcquireRuntime  sourceadapter.AcquireRuntime
	ExportDashboard authoring.DashboardExporter
}

// Application is the small canonical dashboard authoring application
// surface. The source adapter is built once; catalog and preview services are
// created only for the request that needs them, each with a fixed project
// provider.
type Application struct {
	authoring      *authoringservice.Service
	sources        *sourceadapter.Adapter
	repository     authoring.Repository
	authorizer     authoringservice.Authorizer
	acquireRuntime sourceadapter.AcquireRuntime
}

// New validates the composition ports and builds the source adapter once.
// The runtime callback is guarded at this boundary so every composed
// operation receives a non-empty project, runtime, and serving-state lease.
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
		Repository:      options.Repository,
		Authorizer:      options.Authorizer,
		AcquireRuntime:  acquireRuntime,
		Authoring:       options.Authoring,
		ExportDashboard: options.ExportDashboard,
	})
	if err != nil {
		return nil, err
	}
	return &Application{
		authoring:      options.Authoring,
		sources:        sources,
		repository:     options.Repository,
		authorizer:     options.Authorizer,
		acquireRuntime: acquireRuntime,
	}, nil
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
func (a *Application) Execute(ctx context.Context, project string, command authoring.Command) (authoringservice.Result, error) {
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
	if target := strings.TrimSpace(request.TargetProjectID); target != "" {
		request.TargetProjectID = target
	}
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

// Builder returns the governed dashboard-builder bootstrap for one exact
// project draft. The runtime provider is scoped to the normalized request
// project and the builder service owns the single lease for this call.
func (a *Application) Builder(ctx context.Context, request builderview.Request) (uisignals.DashboardBuilderSignal, error) {
	if err := a.validate(); err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	projectID, err := projectID(request.ProjectID)
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	service, err := builderview.NewService(builderview.Options{
		Provider:   projectProvider{projectID: projectID, acquire: a.acquireRuntime},
		Repository: a.repository,
		Authorizer: a.authorizer,
	})
	if err != nil {
		return uisignals.DashboardBuilderSignal{}, err
	}
	request.ProjectID = projectID
	return service.Build(ctx, request)
}

func (a *Application) validate() error {
	if a == nil || a.authoring == nil || a.sources == nil || a.repository == nil || a.authorizer == nil || a.acquireRuntime == nil {
		return fmt.Errorf("dashboard authoring application is not configured")
	}
	return nil
}

func (a *Application) newCatalogService(projectID string) (*catalog.Service, error) {
	return catalog.NewService(catalog.Options{
		Provider:   projectProvider{projectID: projectID, acquire: a.acquireRuntime},
		Repository: a.repository,
		Authorizer: a.authorizer,
	})
}

func projectID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("project id is required")
	}
	return value, nil
}

// guardedAcquire is shared by source, catalog, and preview paths. The
// callback owns acquisition; downstream services own release after a valid
// lease is returned. Invalid leases are released here exactly once.
func guardedAcquire(acquire sourceadapter.AcquireRuntime) sourceadapter.AcquireRuntime {
	return func(ctx context.Context, requestedProject string) (runtimehost.Lease, error) {
		project, err := projectID(requestedProject)
		if err != nil {
			return nil, err
		}
		lease, err := acquire(ctx, project)
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
		if strings.TrimSpace(string(lease.ServingStateID())) == "" {
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
	projectID string
	acquire   sourceadapter.AcquireRuntime
}

func (p projectProvider) Acquire(ctx context.Context) (runtimehost.Lease, error) {
	if strings.TrimSpace(p.projectID) == "" {
		return nil, fmt.Errorf("project id is required")
	}
	if p.acquire == nil {
		return nil, fmt.Errorf("dashboard authoring runtime provider is required")
	}
	return p.acquire(ctx, p.projectID)
}

var _ runtimehost.Provider = projectProvider{}
