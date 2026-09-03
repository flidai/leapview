package localruntimefactory

import (
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/candidatecatalog"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
)

// SQLiteWriterLeaseVerifier adapts the durable local pool fence to
// candidatecatalog without exposing the deployment repository to analytics.
func SQLiteWriterLeaseVerifier(repository interface {
	IsCurrentWriterFence(context.Context, deploymentsqlite.WriterFence, time.Time) (bool, error)
}) candidatecatalog.LeaseVerifier {
	return func(ctx context.Context, lease candidatecatalog.WriterLease) error {
		if repository == nil {
			return fmt.Errorf("writer lease repository is unavailable")
		}
		ok, err := repository.IsCurrentWriterFence(ctx, deploymentsqlite.WriterFence{ID: lease.ID, AttemptID: lease.AttemptID, PhysicalPoolID: lease.PhysicalPoolID, Epoch: lease.Epoch, HolderID: lease.HolderID}, time.Now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("writer lease is not current")
		}
		return nil
	}
}
