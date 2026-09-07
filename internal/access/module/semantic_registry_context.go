package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

// ReadSemanticRegistryTx exposes the Access transaction-bound publication
// reader without exposing the PostgreSQL adapter to consuming capabilities.
// The caller retains ownership of tx.
func (m *Module) ReadSemanticRegistryTx(ctx context.Context, tx pgx.Tx, instanceID string) (access.SemanticRegistryContext, error) {
	if m == nil || m.instanceID == "" || m.instanceID != instanceID {
		return access.SemanticRegistryContext{}, fmt.Errorf("%w: semantic registry module instance mismatch", access.ErrControlIdentityConflict)
	}
	return accesspostgres.ReadSemanticRegistryTx(ctx, tx, instanceID)
}

// ReadSemanticRegistry is a composition-only definition read. It never admits
// a principal or enables semantic execution.
func (m *Module) ReadSemanticRegistry(ctx context.Context, instanceID string) (access.SemanticRegistryContext, error) {
	if m == nil || m.repository == nil || m.instanceID == "" || m.instanceID != instanceID {
		return access.SemanticRegistryContext{}, fmt.Errorf("%w: semantic registry module instance mismatch", access.ErrControlIdentityConflict)
	}
	repository, err := m.repository()
	if err != nil {
		return access.SemanticRegistryContext{}, err
	}
	reader, ok := repository.(access.SemanticRegistryReader)
	if !ok {
		return access.SemanticRegistryContext{}, fmt.Errorf("semantic registry reader is unavailable")
	}
	value, err := reader.ReadSemanticRegistry(ctx, instanceID)
	if err != nil {
		return access.SemanticRegistryContext{}, err
	}
	if err := value.Validate(instanceID, projectgraph.ResourceID(value.Control.ProjectID)); err != nil {
		return access.SemanticRegistryContext{}, err
	}
	return value, nil
}
