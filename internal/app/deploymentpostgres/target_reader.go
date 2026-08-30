package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// nativeTargetReader is the small read-only portion of the native delivery
// repository needed by the application readiness and serving checks. Keeping
// this interface local makes the adapter straightforward to test without
// replacing the concrete PostgreSQL authority in production composition.
type nativeTargetReader interface {
	Target(context.Context, string) (nativepostgres.DeliveryTarget, error)
}

type nativeActiveGenerationReader interface {
	ActiveGeneration(context.Context, string) (nativepostgres.DeliveryGeneration, error)
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
var _ deployment.DeliveryTargetResolver = (*TargetReader)(nil)
var _ interface {
	ActiveDeliveryGenerationForTarget(context.Context, string, string, string) (deployment.DeliveryGeneration, error)
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

// ActiveDeliveryGenerationForTarget resolves the exact generation selected by
// the target's durable pointer and maps only the evidence available in the
// native delivery authority. Scope and pointer identities are checked before
// returning a neutral deployment generation; a missing pointer is not treated
// as an implicit/latest generation.
func (r *TargetReader) ActiveDeliveryGenerationForTarget(ctx context.Context, targetID, projectID, environment string) (deployment.DeliveryGeneration, error) {
	if r == nil || r.repository == nil {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: deployment PostgreSQL target reader is not configured", nativepostgres.ErrInvalid)
	}
	if err := validateScopeInput(targetID, projectID, environment); err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	project, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: target project identity is invalid: %v", deployment.ErrDeliveryConflict, err)
	}
	target, err := r.readTarget(ctx, targetID)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if target.TargetID != targetID || target.ProjectID != projectID || target.Environment != environment {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: delivery target scope differs from requested scope", deployment.ErrDeliveryConflict)
	}
	if strings.TrimSpace(target.ActiveGenerationID) == "" {
		return deployment.DeliveryGeneration{}, mapTargetReaderError(nativepostgres.ErrNotFound)
	}
	activeReader, ok := r.repository.(nativeActiveGenerationReader)
	if !ok {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: deployment PostgreSQL active-generation reader is not configured", nativepostgres.ErrInvalid)
	}
	native, err := activeReader.ActiveGeneration(ctx, targetID)
	if err != nil {
		return deployment.DeliveryGeneration{}, mapTargetReaderError(err)
	}
	if native.GenerationID != target.ActiveGenerationID || native.TargetID != targetID {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: active generation pointer identity differs", deployment.ErrDeliveryConflict)
	}
	return deployment.DeliveryGeneration{
		ID:                    native.GenerationID,
		CandidateID:           native.CandidateID,
		PlanID:                native.PlanID,
		PlanDigest:            native.PlanDigest,
		TargetID:              target.TargetID,
		ProjectID:             project,
		Environment:           target.Environment,
		ServingArtifactDigest: native.ServingArtifactDigest,
		// Native generation admission binds the serving-state identity to the
		// generation UUID. It is not a caller-derived alias.
		ServingStateID: native.GenerationID,
		Status:         deployment.DeliveryGenerationActive,
		CreatedAt:      native.CreatedAt,
	}, nil
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

func validateScopeInput(targetID, projectID, environment string) error {
	for name, value := range map[string]string{"target id": targetID, "project id": projectID, "environment": environment} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: %s must be canonical", deployment.ErrDeliveryInvalid, name)
		}
	}
	return nil
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
