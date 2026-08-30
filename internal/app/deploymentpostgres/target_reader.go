package deploymentpostgres

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

// nativeTargetReader is the small read-only portion of the native delivery
// repository needed by the application readiness and serving checks. Keeping
// this interface local makes the adapter straightforward to test without
// replacing the concrete PostgreSQL authority in production composition.
type nativeTargetReader interface {
	Target(context.Context, string) (nativepostgres.DeliveryTarget, error)
}

// TargetReader adapts the native PostgreSQL target fence to the application
// delivery-target reader contract. It does not cache or derive target state;
// every call reads the current control-plane row from the supplied authority.
type TargetReader struct {
	repository nativeTargetReader
}

var _ interface {
	DeliveryTargetRevision(context.Context, string) (deployment.DeliveryTarget, error)
} = (*TargetReader)(nil)

// NewTargetReader returns a target reader backed by the native PostgreSQL
// deployment repository.
func NewTargetReader(repository *nativepostgres.Repository) *TargetReader {
	return &TargetReader{repository: repository}
}

// DeliveryTargetRevision reads and maps the complete native target fence. In
// particular, the active generation and publication pointers are copied from
// PostgreSQL rather than reconstructed from the requested target ID.
func (r *TargetReader) DeliveryTargetRevision(ctx context.Context, targetID string) (deployment.DeliveryTarget, error) {
	if r == nil || r.repository == nil {
		return deployment.DeliveryTarget{}, fmt.Errorf("%w: deployment PostgreSQL target reader is not configured", nativepostgres.ErrInvalid)
	}
	target, err := r.repository.Target(ctx, targetID)
	if err != nil {
		return deployment.DeliveryTarget{}, fmt.Errorf("load deployment target %q: %w", targetID, err)
	}
	return deployment.DeliveryTarget{
		TargetID:            target.TargetID,
		ProjectID:           target.ProjectID,
		Environment:         target.Environment,
		TargetRevision:      target.TargetRevision,
		ActiveGenerationID:  target.ActiveGenerationID,
		ActivePublicationID: target.ActivePublicationID,
	}, nil
}

// newTargetReader allows package tests to exercise mapping and forwarding
// without opening a PostgreSQL connection. Production callers should use
// NewTargetReader so the concrete native authority remains explicit.
func newTargetReader(repository nativeTargetReader) *TargetReader {
	return &TargetReader{repository: repository}
}
