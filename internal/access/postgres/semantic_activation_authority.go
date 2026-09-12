package postgres

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
)

// SemanticAttributeActivationAuthorityTx locks and returns the exact registry
// and control snapshots used by final semantic activation admission. Both
// singleton rows remain locked until the caller commits or rolls back tx, so a
// control-plane mutation cannot cross the activation decision boundary.
func (r *Repository) SemanticAttributeActivationAuthorityTx(ctx context.Context, tx Tx) (access.SemanticAttributeRegistrySnapshot, access.SemanticAttributeControlSnapshot, error) {
	if r == nil || !r.Configured() || tx == nil {
		return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, fmt.Errorf("semantic activation authority transaction is unavailable")
	}
	queries := accessdb.New(tx)
	registryRow, err := queries.LockSemanticAttributeRegistry(ctx)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, fmt.Errorf("lock semantic attribute registry for activation: %w", err)
	}
	definitionRows, err := queries.ListSemanticAttributeDefinitions(ctx)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, fmt.Errorf("list semantic attribute definitions for activation: %w", err)
	}
	definitions := make([]access.SemanticAttributeDefinition, len(definitionRows))
	for index, row := range definitionRows {
		definitions[index] = semanticAttributeDefinitionFromList(row)
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State: access.SemanticAttributeRegistryState{
			Profile: registryRow.Profile, Revision: registryRow.RegistryRevision,
			Digest: registryRow.RegistryDigest, UpdatedAt: registryRow.UpdatedAt,
		},
		Definitions: definitions,
	}
	if err := access.ValidateSemanticAttributeRegistrySnapshot(registry); err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
	}

	controlRow, err := lockSemanticAttributeControlState(ctx, tx)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
	}
	assignments, mappings, err := allControlRows(ctx, tx)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
	}
	control := access.SemanticAttributeControlSnapshot{
		State: access.SemanticAttributeControlState{
			Profile: controlRow.Profile, Revision: controlRow.Revision,
			Digest: controlRow.Digest, UpdatedAt: controlRow.UpdatedAt,
		},
		Assignments: assignments,
		Mappings:    mappings,
	}
	if err := access.ValidateSemanticAttributeControlSnapshot(control); err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, access.SemanticAttributeControlSnapshot{}, err
	}
	return registry, control, nil
}
