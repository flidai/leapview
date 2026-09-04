package deployment

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/runtimehost"
)

type Prepared interface {
	DuckLakeSnapshotID() int64
	Close() error
}
type Runtime interface {
	Prepare(context.Context, runtimehost.ServingStateCandidate) (Prepared, error)
	Verify(context.Context, Prepared) (Verification, error)
	Activate(Prepared, func() error) error
}
type runtimeRegistry interface {
	PrepareServingStateCandidate(context.Context, runtimehost.ServingStateCandidate) (*runtimehost.Prepared, error)
	VerifyPrepared(context.Context, *runtimehost.Prepared) (runtimehost.PreparedVerification, error)
	ActivatePrepared(*runtimehost.Prepared, func() error) error
}
type registryRuntime struct{ registry runtimeRegistry }
type registryPrepared struct{ prepared *runtimehost.Prepared }

func NewRegistryRuntime(registry runtimeRegistry) (Runtime, error) {
	if registry == nil {
		return nil, fmt.Errorf("runtime registry is required")
	}
	return registryRuntime{registry: registry}, nil
}
func (r registryRuntime) Prepare(ctx context.Context, candidate runtimehost.ServingStateCandidate) (Prepared, error) {
	p, err := r.registry.PrepareServingStateCandidate(ctx, candidate)
	if err != nil {
		return nil, err
	}
	return registryPrepared{prepared: p}, nil
}
func (r registryRuntime) Verify(ctx context.Context, prepared Prepared) (Verification, error) {
	p, ok := prepared.(registryPrepared)
	if !ok || p.prepared == nil {
		return Verification{}, fmt.Errorf("prepared runtime belongs to a different coordinator")
	}
	v, err := r.registry.VerifyPrepared(ctx, p.prepared)
	if err != nil {
		return Verification{}, err
	}
	return Verification{Digest: v.Digest}, nil
}
func (r registryRuntime) Activate(prepared Prepared, activate func() error) error {
	p, ok := prepared.(registryPrepared)
	if !ok || p.prepared == nil {
		return fmt.Errorf("prepared runtime belongs to a different coordinator")
	}
	return r.registry.ActivatePrepared(p.prepared, activate)
}
func (p registryPrepared) DuckLakeSnapshotID() int64 { return p.prepared.DuckLakeSnapshotID() }
func (p registryPrepared) Close() error              { return p.prepared.Close() }
