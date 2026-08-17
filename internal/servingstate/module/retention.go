package module

import (
	"context"
	"fmt"
	"time"

	storagemaintenance "github.com/flidai/leapview/internal/servingstate/retention"
	"github.com/flidai/leapview/internal/workload"
)

type RetentionRepository interface {
	ReconcileRetention(context.Context, string, time.Time) error
	ReferencedDuckLakeSnapshots(context.Context, string) ([]int64, error)
}

type RetentionConfig struct {
	States             RetentionRepository
	Snapshots          storagemaintenance.SnapshotMaintenance
	Admission          workload.Admitter
	Environment        string
	CatalogPath        string
	DataPath           string
	ProtectedSnapshots func() []int64
}

type Retention struct {
	config RetentionConfig
}

func NewRetention(config RetentionConfig) *Retention {
	return &Retention{config: config}
}

func (r *Retention) Run(ctx context.Context, dryRun bool) error {
	return r.run(ctx, dryRun, nil)
}

func (r *Retention) RunWithProtected(ctx context.Context, dryRun bool, additional []int64) error {
	return r.run(ctx, dryRun, additional)
}

func (r *Retention) run(ctx context.Context, dryRun bool, additional []int64) error {
	if r == nil || r.config.States == nil {
		return nil
	}
	if _, _, admitted := workload.Current(ctx); !admitted && r.config.Admission != nil {
		lease, err := r.config.Admission.Acquire(ctx, workload.Request{
			Class: workload.Maintenance, PrincipalID: "system:storage-retention", Operation: "storage.retention", EstimatedMemoryBytes: 128 << 20,
		})
		if err != nil {
			return nil
		}
		defer lease.Release()
		ctx = lease.Context()
	}
	if r.config.CatalogPath == "" || r.config.DataPath == "" {
		return nil
	}
	var protected []int64
	if r.config.ProtectedSnapshots != nil {
		protected = r.config.ProtectedSnapshots()
	}
	protected = append(protected, additional...)
	_, err := storagemaintenance.Run(ctx, r.config.States, storagemaintenance.Options{
		Snapshots: r.config.Snapshots, Environment: r.config.Environment,
		CatalogPath: r.config.CatalogPath, DataPath: r.config.DataPath,
		AdditionalProtectedSnapshots: protected, DryRun: dryRun,
	})
	if err != nil {
		return fmt.Errorf("storage retention: %w", err)
	}
	return nil
}
