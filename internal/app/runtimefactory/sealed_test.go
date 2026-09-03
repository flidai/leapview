package runtimefactory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/resulttier"
	"github.com/flidai/leapview/internal/analytics/sealedcatalog"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestSealedFactoryRejectsLegacyPrepare(t *testing.T) {
	factory := NewSealedFactory(FactoryConfig{}, nil, nil, nil, nil, nil)
	if _, err := factory.Prepare(context.Background(), runtimehost.RuntimeInput{}); err == nil {
		t.Fatal("sealed factory accepted legacy Prepare")
	}
}

func TestSealedPreparedRuntimePreservesRuntimeCapabilitySurface(t *testing.T) {
	prepared := runtimehost.PreparedRuntime(&sealedPreparedRuntime{
		dashboardRuntimeWithGraph: &dashboardRuntimeWithGraph{},
	})
	if _, ok := prepared.(interface {
		Resolver() dashboardresolver.Resolver
		SemanticModel(string) (*semanticmodel.Model, bool)
		SemanticModelByID(projectgraph.ResourceID) (*semanticmodel.Model, bool)
	}); !ok {
		t.Fatal("sealed prepared runtime lost resolver and semantic-model capabilities")
	}
	if _, ok := prepared.(interface {
		ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error)
		QuerySemantic(context.Context, string, reportdef.AggregateQuery) (reportdef.QueryRows, error)
		PreviewSemantic(context.Context, string, reportdef.RowQuery) (reportdef.QueryRows, error)
	}); !ok {
		t.Fatal("sealed prepared runtime lost semantic query capabilities")
	}
}

func TestSealedFactoryFailsClosedWhenDeliveryRootMissing(t *testing.T) {
	want := errors.New("delivery root missing")
	factory := NewSealedFactory(FactoryConfig{}, func(context.Context, runtimehost.RuntimeInput) (SealedServingRoot, error) {
		return SealedServingRoot{}, want
	}, structObjectStore{}, structLeases{}, func(context.Context, sealedcatalog.Artifact, catalogartifact.LeaseInput) error { return nil }, func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment, resulttier.Tier) (*dashboardruntime.Service, error) {
		return nil, errors.New("unexpected dashboard access")
	})
	_, err := factory.(interface {
		PrepareSealed(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error)
	}).PrepareSealed(context.Background(), runtimehost.RuntimeInput{})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want root error", err)
	}
}

func TestSealedFactoryRejectsPersistedArtifactMismatch(t *testing.T) {
	factory := NewSealedFactory(FactoryConfig{}, func(context.Context, runtimehost.RuntimeInput) (SealedServingRoot, error) {
		return SealedServingRoot{ServingStateID: "state-1", ServingArtifactID: "artifact-other", ServingArtifactDigest: "sha256:" + strings.Repeat("a", 64)}, nil
	}, structObjectStore{}, structLeases{}, func(context.Context, sealedcatalog.Artifact, catalogartifact.LeaseInput) error { return nil }, func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment, resulttier.Tier) (*dashboardruntime.Service, error) {
		return nil, errors.New("unexpected dashboard access")
	})
	input := runtimehost.RuntimeInput{State: servingstate.State{ID: "state-1", ProjectID: projectgraph.ResourceID("project-1")}, Artifact: servingstate.Artifact{ID: "artifact-expected", ServingStateID: "state-1", Digest: "sha256:" + strings.Repeat("a", 64)}}
	sealedFactory := factory.(interface {
		PrepareSealed(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error)
	})
	if _, err := sealedFactory.PrepareSealed(context.Background(), input); !errors.Is(err, ErrSealedRootMismatch) {
		t.Fatalf("mismatch error=%v", err)
	}
}

// These tiny stubs keep the test before object access and avoid requiring a
// DuckDB catalog fixture for the fail-closed control-plane assertion.
type structObjectStore struct{}

func (structObjectStore) Open(context.Context, string) (sealedcatalog.Object, error) {
	return sealedcatalog.Object{}, errors.New("unexpected object access")
}

type structLeases struct{}

func (structLeases) AcquireQueryLease(context.Context, catalogartifact.LeaseInput) (catalogartifact.QueryLease, error) {
	return catalogartifact.QueryLease{}, errors.New("unexpected lease access")
}
func (structLeases) ReleaseQueryLease(context.Context, string) error { return nil }
