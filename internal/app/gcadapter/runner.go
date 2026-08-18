package gcadapter

import (
	"context"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/gc"
)

// Runner is the production composition entrypoint for one admitted physical
// pool. It exposes only global mark-and-sweep; callers cannot route through
// serving-state retention or DuckLake native cleanup.
// Runner owns the only production repair entrypoint. Repair is composed from
// the collector's durable root reader and read-only inspector, so callers
// cannot mutate a catalog root without passing through the exact SQLite,
// artifact-byte/digest, and DuckLake-closure checks.
type Runner struct {
	Collector  *gc.Collector
	RepairTool *RepairTool
}

func NewRunner(collector *gc.Collector) (*Runner, error) {
	if collector == nil {
		return nil, gc.ErrInvalidConfig
	}
	repair, err := NewRepairTool(collector.Control, collector.Inspector)
	if err != nil {
		return nil, err
	}
	return &Runner{Collector: collector, RepairTool: repair}, nil
}

func NewProductionRunner(control gc.ControlPlane, store gc.PoolStore, inspector gc.Inspector, config gc.Config) (*Runner, error) {
	if config.PhysicalPoolID == "" {
		return nil, gc.ErrInvalidConfig
	}
	collector, err := gc.New(control, store, inspector, config)
	if err != nil {
		return nil, err
	}
	return NewRunner(collector)
}

func (r *Runner) Run(ctx context.Context) (gc.Result, error) {
	if r == nil || r.Collector == nil {
		return gc.Result{}, gc.ErrInvalidConfig
	}
	return r.Collector.Run(ctx)
}

// RepairAtRevision is the fenced production repair entrypoint. The callback
// receives the exact durable root revision observed before physical
// verification and must pass it to a repository-level compare-and-swap
// mutation.
func (r *Runner) RepairAtRevision(ctx context.Context, root deployment.DeliveryRoot, mutate func(context.Context, deployment.DeliveryRoot, int64) error) error {
	if r == nil || r.RepairTool == nil {
		return ErrRepairUnavailable
	}
	return r.RepairTool.VerifyAndMutateAtRevision(ctx, root, mutate)
}
