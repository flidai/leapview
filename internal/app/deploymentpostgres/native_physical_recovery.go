package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
)

// NativePhysicalMarkerResolver is the application recovery boundary for
// exact DuckLake build markers. It intentionally does not embed the physical
// build environment or materializer: recovery may inspect a marker but must
// never rerun Materialize.
type NativePhysicalMarkerResolver interface {
	ResolveCommittedMarker(context.Context, catalogartifact.CommitMarker) (ducklake.PhysicalMarkerResolution, error)
	Close() error
}

// NativePhysicalMarkerResolverFactory opens read-only physical recovery
// environments. Implementations must ensure every resolver call gets a fresh
// DuckLake session rather than reusing connection-local commit state.
type NativePhysicalMarkerResolverFactory interface {
	OpenReadOnly(context.Context) (NativePhysicalMarkerResolver, error)
}

// NativePhysicalMarkerResolverFactoryFunc adapts a constructor for tests and
// embedders without exposing a DuckDB or PostgreSQL handle.
type NativePhysicalMarkerResolverFactoryFunc func(context.Context) (NativePhysicalMarkerResolver, error)

var _ NativePhysicalMarkerResolverFactory = NativePhysicalMarkerResolverFactoryFunc(nil)

func (f NativePhysicalMarkerResolverFactoryFunc) OpenReadOnly(ctx context.Context) (NativePhysicalMarkerResolver, error) {
	if f == nil {
		return nil, errors.New("native physical marker resolver factory is not configured")
	}
	return f(ctx)
}

// DuckLakePhysicalMarkerResolverFactory is the production app adapter. It
// keeps DuckLake's config and credential bootstrap at composition while
// exporting only the recovery resolver capability to orchestration.
// ResolverFactory is optional and exists for package-level composition tests;
// production callers should provide Config and use the DuckLake adapter.
type DuckLakePhysicalMarkerResolverFactory struct {
	Config          ducklake.Config
	ResolverFactory ducklake.PhysicalMarkerResolverFactory
}

var _ NativePhysicalMarkerResolverFactory = DuckLakePhysicalMarkerResolverFactory{}

// OpenReadOnly creates one application-owned read-only resolver. Config is
// copied by the DuckLake factory, which forces marker-reconciliation attach
// mode and removes any caller commit marker or catalog-file path.
func (f DuckLakePhysicalMarkerResolverFactory) OpenReadOnly(ctx context.Context) (NativePhysicalMarkerResolver, error) {
	resolverFactory := f.ResolverFactory
	if resolverFactory == nil {
		resolverFactory = ducklake.DuckLakePhysicalMarkerResolverFactory{Config: f.Config}
	}
	resolver, err := resolverFactory.OpenReadOnly(ctx)
	if err != nil {
		return nil, err
	}
	if resolver == nil {
		return nil, fmt.Errorf("native physical marker resolver factory returned nil resolver")
	}
	return &duckLakeNativePhysicalMarkerResolver{resolver: resolver}, nil
}

type duckLakeNativePhysicalMarkerResolver struct {
	resolver ducklake.PhysicalMarkerResolver
}

var _ NativePhysicalMarkerResolver = (*duckLakeNativePhysicalMarkerResolver)(nil)

func (r *duckLakeNativePhysicalMarkerResolver) ResolveCommittedMarker(ctx context.Context, marker catalogartifact.CommitMarker) (ducklake.PhysicalMarkerResolution, error) {
	if r == nil || r.resolver == nil {
		return ducklake.PhysicalMarkerResolution{}, errors.New("native physical marker resolver is not initialized")
	}
	return r.resolver.ResolveCommittedMarker(ctx, marker)
}

func (r *duckLakeNativePhysicalMarkerResolver) Close() error {
	if r == nil || r.resolver == nil {
		return nil
	}
	return r.resolver.Close()
}
