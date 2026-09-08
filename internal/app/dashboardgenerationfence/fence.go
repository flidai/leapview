// Package dashboardgenerationfence provides the transaction-scoped active
// generation check shared by dashboard authoring and publication authorities.
package dashboardgenerationfence

import (
	"context"
	"errors"
	"fmt"
	"strings"

	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

// Fence binds generation validation to one process-bound delivery target. The
// repository is the exact native deployment authority supplied by composition;
// this adapter never opens a second connection or transaction.
type Fence struct {
	delivery *deploymentpostgres.Repository
	targetID string
}

func New(delivery *deploymentpostgres.Repository, targetID string) (*Fence, error) {
	if delivery == nil || !delivery.Configured() || !delivery.TransactionCapable() {
		return nil, errors.New("dashboard generation fence requires a configured PostgreSQL deployment repository")
	}
	if strings.TrimSpace(targetID) == "" || targetID != strings.TrimSpace(targetID) {
		return nil, errors.New("dashboard generation fence target id is required and must be canonical")
	}
	return &Fence{delivery: delivery, targetID: targetID}, nil
}

// Matches proves this fence is bound to the exact deployment authority and
// process-bound target identity supplied by application composition.
func (f *Fence) Matches(delivery *deploymentpostgres.Repository, targetID string) bool {
	return f != nil && f.delivery != nil && f.delivery == delivery && f.targetID == targetID
}

// ValidateActiveGeneration checks the target pointer and immutable generation
// evidence inside the caller-owned transaction. Any project, environment,
// target, or generation mismatch fails closed before a source mutation writes.
func (f *Fence) ValidateActiveGeneration(ctx context.Context, tx pgx.Tx, identity projectgraph.ServingIdentity) error {
	if f == nil || f.delivery == nil {
		return errors.New("dashboard generation fence is not configured")
	}
	if tx == nil {
		return errors.New("dashboard generation fence transaction is required")
	}
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("dashboard generation fence identity: %w", err)
	}
	// Share-lock the target row. Canonical activation takes the same row lock
	// before advancing the pointer, so a cutover cannot overtake this check
	// before the source transaction commits.
	target, err := f.delivery.TargetForShareTx(ctx, tx, f.targetID)
	if err != nil {
		return fmt.Errorf("dashboard generation fence target: %w", err)
	}
	if target.TargetID != f.targetID || target.ProjectID != identity.ProjectID.String() || target.Environment != identity.Environment || target.ActiveGenerationID != identity.GenerationID {
		return fmt.Errorf("%w: target %q does not point to serving generation %q", deploymentpostgres.ErrCASConflict, f.targetID, identity.GenerationID)
	}
	generation, err := f.delivery.GenerationTx(ctx, tx, identity.GenerationID)
	if err != nil {
		return fmt.Errorf("dashboard generation fence generation: %w", err)
	}
	if generation.GenerationID != identity.GenerationID || generation.TargetID != f.targetID {
		return fmt.Errorf("%w: generation %q is not bound to target %q", deploymentpostgres.ErrCASConflict, identity.GenerationID, f.targetID)
	}
	return nil
}
