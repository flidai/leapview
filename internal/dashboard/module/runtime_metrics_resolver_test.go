package module

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestRuntimeMetricsResolverPublishedSuccessUsesOneLease(t *testing.T) {
	compiled := moduleCompiledRevision(t, "workspace", "published", "state-1")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})

	resolved, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve("published")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source.Kind != dashboardresolver.SourceWorkspace || resolved.Source.AuthoredRevision.ID != "revision-1" {
		t.Fatalf("source = %#v", resolved.Source)
	}
	if provider.acquires != 1 || provider.lease == nil || provider.lease.releases != 1 {
		t.Fatalf("lease counts acquire=%d release=%d", provider.acquires, provider.lease.releases)
	}
}

func TestRuntimeMetricsResolverStalePublishedDoesNotFallbackToProject(t *testing.T) {
	compiled := moduleCompiledRevision(t, "workspace", "same", "old-state")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})

	_, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve("same")
	if !errors.Is(err, dashboardresolver.ErrStaleSemanticState) {
		t.Fatalf("error = %v, want ErrStaleSemanticState", err)
	}
	if provider.lease == nil || provider.lease.releases != 1 {
		t.Fatalf("lease releases = %d, want 1", provider.lease.releases)
	}
}

func TestRuntimeMetricsResolverSameIDCollisionIsAmbiguous(t *testing.T) {
	compiled := moduleCompiledRevision(t, "workspace", "same", "state-1")
	provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{
		Provider: provider, WorkspaceID: "workspace", PublishedCompilationReader: moduleCompilationReader{compiled: compiled},
	})
	_, err := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver().Resolve("same")
	if !errors.Is(err, dashboardresolver.ErrAmbiguous) {
		t.Fatalf("error = %v, want ErrAmbiguous", err)
	}
}

func TestRuntimeMetricsResolverPinsRuntimeProviderAcrossResolve(t *testing.T) {
	runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
	provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
	metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, WorkspaceID: "workspace"})
	resolver := metrics.(interface {
		Resolver() dashboardresolver.Resolver
	}).Resolver()
	resolved, err := resolver.Resolve("project")
	if err != nil {
		t.Fatal(err)
	}
	if provider.acquires != 1 || provider.lease.releases != 1 || runtime.resolveCalls != 1 || runtime.modelCalls != 0 {
		t.Fatalf("provider/runtime calls acquire=%d release=%d resolve=%d model=%d", provider.acquires, provider.lease.releases, runtime.resolveCalls, runtime.modelCalls)
	}
	if resolved.Source.ServingStateID != "state-1" {
		t.Fatalf("source = %#v", resolved.Source)
	}
}

type resolverTestProvider struct {
	runtime  runtimehost.Runtime
	stateID  string
	acquires int
	lease    *resolverTestLease
}

func (p *resolverTestProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	p.acquires++
	p.lease = &resolverTestLease{runtime: p.runtime, stateID: servingstate.ID(p.stateID)}
	return p.lease, nil
}

type resolverTestLease struct {
	runtime  runtimehost.Runtime
	stateID  servingstate.ID
	releases int
}

func (l *resolverTestLease) Runtime() runtimehost.Runtime    { return l.runtime }
func (l *resolverTestLease) ServingStateID() servingstate.ID { return l.stateID }
func (l *resolverTestLease) DuckLakeSnapshotID() int64       { return 0 }
func (l *resolverTestLease) Release()                        { l.releases++ }

type resolverTestRuntime struct {
	model        *semanticmodel.Model
	resolveCalls int
	modelCalls   int
}

func (r *resolverTestRuntime) Close() error { return nil }
func (r *resolverTestRuntime) Resolve(id string) (dashboardresolver.Resolved, error) {
	r.resolveCalls++
	if id != "project" && id != "same" {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return dashboardresolver.Resolved{Definition: dashboarddefinition.Definition{ID: id, SemanticModel: "sales_model"}, Model: r.model}, nil
}
func (r *resolverTestRuntime) SemanticModel(string) (*semanticmodel.Model, bool) {
	r.modelCalls++
	return r.model, r.model != nil
}
func (r *resolverTestRuntime) DefaultFilters(string) dashboard.Filters { return dashboard.Filters{} }

type moduleCompilationReader struct{ compiled authoring.CompiledRevision }

func (r moduleCompilationReader) GetPublishedCompilation(context.Context, string, authoring.DashboardID) (authoring.CompiledRevision, error) {
	if r.compiled.Definition.ID == "" {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	return r.compiled, nil
}

func moduleCompiledRevision(t *testing.T, workspace, dashboardID, stateID string) authoring.CompiledRevision {
	t.Helper()
	definition, err := dashboarddefinition.New(dashboardID, "Sales", "", "sales_model", []dashboard.Page{{ID: "overview", Title: "Overview"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := authoring.NewCompiledRevision(workspace, authoring.DashboardID(dashboardID), authoring.RevisionToken{RevisionID: "revision-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("b", 64)}, definition, stateID, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
