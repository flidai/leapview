package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
)

// SemanticAttributeOwnershipAuthority is the access-owned adapter for
// principal-owned semantic-attribute definitions. Ownership transfer is a
// semantic-attribute lifecycle mutation: it advances each definition version
// and refreshes the registry digest in the caller-owned transaction.
type SemanticAttributeOwnershipAuthority struct{ db DBTX }

// NewSemanticAttributeOwnershipAuthority constructs the access-owned adapter.
func NewSemanticAttributeOwnershipAuthority(db DBTX) *SemanticAttributeOwnershipAuthority {
	return &SemanticAttributeOwnershipAuthority{db: db}
}

func (a *SemanticAttributeOwnershipAuthority) ListOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	if a == nil || a.db == nil {
		return access.OwnershipReport{}, errors.New("access semantic-attribute ownership authority is unavailable")
	}
	id, err := uuidID("principal id", principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	ownerID, err := pgUUID(id)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	rows, err := accessdb.New(a.db).ListPrincipalOwnedSemanticAttributes(ctx, ownerID)
	if err != nil {
		return access.OwnershipReport{}, fmt.Errorf("list principal-owned semantic attributes: %w", err)
	}
	objects := make([]access.OwnedObject, 0, len(rows))
	for _, row := range rows {
		if row.OwnerKind != "principal" || row.OwnerID != id {
			return access.OwnershipReport{}, errors.New("semantic-attribute ownership authority returned an inconsistent owner")
		}
		lifecycle := "active"
		if !row.Enabled || strings.TrimSpace(row.DisabledAt) != "" {
			lifecycle = "disabled"
		}
		objects = append(objects, access.OwnedObject{
			Kind: "semantic_attribute", ID: row.DefinitionID, Name: row.Name,
			OwnerPrincipalID: id, Lifecycle: lifecycle,
			// The definition owner can be changed by the semantic-attribute
			// metadata lifecycle; offboarding uses the transaction-bound
			// transfer adapter below.
			Transferable: true, TombstoneOnOffboard: false,
		})
	}
	return access.OwnershipReport{PrincipalID: id, Objects: objects}, nil
}

// TransferOwnedObjects moves every principal-owned definition to an enabled
// target principal. A retry is a no-op because the source-owner predicate no
// longer matches. Registry locking and the digest update are kept in the same
// transaction as the owner changes so an active serving reader cannot observe
// a mixed definition registry.
func (a *SemanticAttributeOwnershipAuthority) TransferOwnedObjects(ctx context.Context, principalID, targetPrincipalID string) (access.OwnershipReport, error) {
	ownerID, err := uuidID("principal id", principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	targetID, err := uuidID("target principal id", targetPrincipalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if ownerID == targetID {
		return access.OwnershipReport{}, errors.New("principal and target principal ids must differ")
	}
	if a == nil || a.db == nil {
		return access.OwnershipReport{}, errors.New("access semantic-attribute ownership authority is unavailable")
	}
	ownerUUID, err := pgUUID(ownerID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	targetUUID, err := pgUUID(targetID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	return a.withOwnershipTx(ctx, func(db DBTX) (access.OwnershipReport, error) {
		queries := accessdb.New(db)
		// Keep direct semantic-attribute transfers on the same lifecycle
		// authority boundary as repository-level offboarding. This adapter is
		// also used directly by focused control-plane callers.
		if err := queries.LockPlatformRoleAuthority(ctx); err != nil {
			return access.OwnershipReport{}, fmt.Errorf("lock principal ownership authority: %w", err)
		}
		exists, err := queries.SemanticAttributeTransferPrincipalExists(ctx, targetUUID)
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("validate semantic-attribute transfer target: %w", err)
		}
		if !exists {
			return access.OwnershipReport{}, fmt.Errorf("semantic-attribute transfer target %q is not enabled", targetID)
		}
		locked, err := queries.LockSemanticAttributeRegistry(ctx)
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("lock semantic attribute registry: %w", err)
		}
		rows, err := queries.TransferOwnedSemanticAttributes(ctx, accessdb.TransferOwnedSemanticAttributesParams{TargetOwnerID: targetUUID, OwnerID: ownerUUID})
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("transfer principal-owned semantic attributes: %w", err)
		}
		if len(rows) == 0 {
			return access.OwnershipReport{PrincipalID: ownerID, Objects: []access.OwnedObject{}}, nil
		}
		if _, err := refreshSemanticAttributeRegistry(ctx, queries, locked.RegistryRevision+1); err != nil {
			return access.OwnershipReport{}, err
		}
		objects := make([]access.OwnedObject, 0, len(rows))
		for _, row := range rows {
			lifecycle := "active"
			if !row.Enabled || strings.TrimSpace(row.DisabledAt) != "" {
				lifecycle = "disabled"
			}
			objects = append(objects, access.OwnedObject{
				Kind: "semantic_attribute", ID: row.DefinitionID, Name: row.Name,
				OwnerPrincipalID: ownerID, Lifecycle: lifecycle,
				Transferable: true, TombstoneOnOffboard: false,
			})
		}
		return access.OwnershipReport{PrincipalID: ownerID, Objects: objects}, nil
	})
}

// TombstoneOwnedObjects is intentionally unsupported. Semantic-attribute
// definitions remain part of the live registry, so their safe offboarding
// action is transfer rather than silently disabling or deleting the definition.
func (a *SemanticAttributeOwnershipAuthority) TombstoneOwnedObjects(context.Context, string) (access.OwnershipReport, error) {
	return access.OwnershipReport{}, errors.New("semantic-attribute definitions must be transferred before offboarding")
}

func (a *SemanticAttributeOwnershipAuthority) withOwnershipTx(ctx context.Context, fn func(DBTX) (access.OwnershipReport, error)) (access.OwnershipReport, error) {
	if tx, ok := a.db.(Tx); ok {
		return fn(tx)
	}
	b, ok := a.db.(beginner)
	if !ok {
		return access.OwnershipReport{}, errors.New("access semantic-attribute ownership authority requires a transaction-capable PostgreSQL handle")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	defer tx.Rollback(ctx)
	report, err := fn(tx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return access.OwnershipReport{}, err
	}
	return report, nil
}

func (a *SemanticAttributeOwnershipAuthority) WithOwnershipDB(db access.OwnershipDBTX) access.OwnershipAuthority {
	return NewSemanticAttributeOwnershipAuthority(db)
}

func (a *SemanticAttributeOwnershipAuthority) WithOwnershipMutationDB(db access.OwnershipDBTX) access.OwnershipMutator {
	return NewSemanticAttributeOwnershipAuthority(db)
}

var _ access.OwnershipAuthority = (*SemanticAttributeOwnershipAuthority)(nil)
var _ access.TransactionalOwnershipAuthority = (*SemanticAttributeOwnershipAuthority)(nil)
var _ access.OwnershipMutator = (*SemanticAttributeOwnershipAuthority)(nil)
var _ access.TransactionalOwnershipMutator = (*SemanticAttributeOwnershipAuthority)(nil)
