package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"

	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
)

// ActivationLineageVerifierAdapter verifies that the generation being
// activated has an immutable, project-scoped lineage binding. Reads are
// issued through the caller-owned deployment transaction so activation and
// lineage verification observe one control-plane snapshot.
type ActivationLineageVerifierAdapter struct {
	Lineage *lineagepostgres.Repository
}

var _ deploymentnative.ActivationLineageVerifier = (*ActivationLineageVerifierAdapter)(nil)

// NewActivationLineageVerifier constructs the application composition
// adapter over the process-owned lineage repository. It performs no I/O or
// schema work.
func NewActivationLineageVerifier(repository *lineagepostgres.Repository) (*ActivationLineageVerifierAdapter, error) {
	if repository == nil || !repository.Configured() {
		return nil, errors.New("activation lineage verifier requires a configured PostgreSQL lineage authority")
	}
	return &ActivationLineageVerifierAdapter{Lineage: repository}, nil
}

// VerifyActivationLineage resolves and integrity-checks the exact project,
// target, and generation binding required by activation. Missing, invalid,
// or tampered lineage evidence is an activation conflict rather than a
// discoverable not-found condition.
func (v *ActivationLineageVerifierAdapter) VerifyActivationLineage(ctx context.Context, tx deploymentnative.Tx, input deploymentnative.ActivationLineageInput) error {
	if v == nil || v.Lineage == nil || !v.Lineage.Configured() || tx == nil {
		return fmt.Errorf("%w: activation lineage verifier is unavailable", deploymentnative.ErrConflict)
	}
	projection, err := lineagepostgres.LoadBoundForProject(ctx, tx, input.ProjectID, input.TargetID, input.GenerationID)
	if err != nil {
		if errors.Is(err, lineagepostgres.ErrNotFound) || errors.Is(err, lineagepostgres.ErrTampered) || errors.Is(err, lineagepostgres.ErrInvalid) {
			return fmt.Errorf("%w: activation lineage evidence: %v", deploymentnative.ErrConflict, err)
		}
		return err
	}
	if projection.ProjectID != input.ProjectID {
		return fmt.Errorf("%w: activation lineage project differs", deploymentnative.ErrConflict)
	}
	return nil
}
