package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

// ErrTargetAlreadyPublished signals that ordinary grant authority should
// handle the request because this target already has an active publication.
// It aliases the native fence sentinel so errors.Is works across the adapter.
var ErrTargetAlreadyPublished = nativepostgres.ErrAlreadyActive

// nativeTargetReader is the small read-only portion of the native delivery
// repository needed by the application readiness and serving checks. Keeping
// this interface local makes the adapter straightforward to test without
// replacing the concrete PostgreSQL authority in production composition.
type nativeTargetReader interface {
	Target(context.Context, string) (nativepostgres.DeliveryTarget, error)
}

type nativeUnpublishedTargetRunner interface {
	WithUnpublishedTarget(context.Context, string, string, string, func(context.Context) error) error
}

// TargetReader adapts the native PostgreSQL target fence to application
// delivery-target reads and first-publication checks. It does not cache or
// derive target state; each operation uses the supplied control-plane authority.
type TargetReader struct {
	repository nativeTargetReader
}

var _ interface {
	DeliveryTargetRevision(context.Context, string) (deployment.DeliveryTarget, error)
} = (*TargetReader)(nil)
var _ deployment.DeliveryTargetResolver = (*TargetReader)(nil)

// NewTargetReader returns a target reader backed by the native PostgreSQL
// deployment repository.
func NewTargetReader(repository *nativepostgres.Repository) *TargetReader {
	return &TargetReader{repository: repository}
}

// DeliveryTargetRevision reads and maps the complete native target fence. In
// particular, the active generation and publication pointers are copied from
// PostgreSQL rather than reconstructed from the requested target ID.
func (r *TargetReader) DeliveryTargetRevision(ctx context.Context, targetID string) (deployment.DeliveryTarget, error) {
	target, err := r.readTarget(ctx, targetID)
	if err != nil {
		return deployment.DeliveryTarget{}, err
	}
	return target, nil
}

// ResolveDeliveryTarget adapts the native target fence to Deployment's
// lifecycle resolver. It is intentionally a fresh control-plane read on every
// call; no target identity is reconstructed from process configuration.
func (r *TargetReader) ResolveDeliveryTarget(ctx context.Context, targetID string) (deployment.DeliveryTarget, error) {
	return r.DeliveryTargetRevision(ctx, targetID)
}

// WithUnpublishedTarget holds the native target fence while a caller grants
// first-publication authority in its own access transaction. Native
// ErrAlreadyActive is preserved so callers can continue through their regular
// grant authority when publication has already activated the target.
func (r *TargetReader) WithUnpublishedTarget(ctx context.Context, targetID, projectID, environment string, callback func(context.Context) error) error {
	if r == nil || r.repository == nil || callback == nil {
		return nativepostgres.ErrInvalid
	}
	runner, ok := r.repository.(nativeUnpublishedTargetRunner)
	if !ok {
		return fmt.Errorf("%w: deployment PostgreSQL unpublished target fence is not configured", nativepostgres.ErrInvalid)
	}
	var callbackErr error
	err := runner.WithUnpublishedTarget(ctx, targetID, projectID, environment, func(callbackCtx context.Context) error {
		callbackErr = callback(callbackCtx)
		return callbackErr
	})
	if callbackErr != nil {
		return callbackErr
	}
	return mapTargetReaderError(err)
}

func (r *TargetReader) readTarget(ctx context.Context, targetID string) (deployment.DeliveryTarget, error) {
	if r == nil || r.repository == nil {
		return deployment.DeliveryTarget{}, fmt.Errorf("%w: deployment PostgreSQL target reader is not configured", nativepostgres.ErrInvalid)
	}
	target, err := r.repository.Target(ctx, targetID)
	if err != nil {
		return deployment.DeliveryTarget{}, mapTargetReaderError(fmt.Errorf("load deployment target %q: %w", targetID, err))
	}
	if target.TargetID != targetID || strings.TrimSpace(target.ProjectID) == "" || target.ProjectID != strings.TrimSpace(target.ProjectID) || strings.TrimSpace(target.Environment) == "" || target.Environment != strings.TrimSpace(target.Environment) || target.TargetRevision <= 0 {
		return deployment.DeliveryTarget{}, fmt.Errorf("%w: stored delivery target identity is invalid", deployment.ErrDeliveryConflict)
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

func mapTargetReaderError(err error) error {
	if errors.Is(err, nativepostgres.ErrNotFound) {
		return fmt.Errorf("%w: delivery target or generation is absent", deployment.ErrNotFound)
	}
	if errors.Is(err, nativepostgres.ErrConflict) {
		return fmt.Errorf("%w: delivery target or generation identity differs", deployment.ErrDeliveryConflict)
	}
	return err
}

// newTargetReader allows package tests to exercise mapping and forwarding
// without opening a PostgreSQL connection. Production callers should use
// NewTargetReader so the concrete native authority remains explicit.
func newTargetReader(repository nativeTargetReader) *TargetReader {
	return &TargetReader{repository: repository}
}
