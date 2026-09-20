package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/accesslifecycle"
	"github.com/google/uuid"
)

// ListOwnedObjects reports active, non-tombstoned conversations. Archived
// conversations and deleted transcripts are historical evidence retained by
// the agent authority and do not represent live ownership.
func (r *Repository) ListOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	principalID, err := principalIDValue(principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if r == nil || r.db == nil {
		return access.OwnershipReport{}, fmt.Errorf("agent ownership authority is unavailable")
	}
	rows, err := agentdb.New(r.db).ListOwnedLiveAgentConversations(ctx, principalID)
	if err != nil {
		return access.OwnershipReport{}, fmt.Errorf("list principal-owned agent conversations: %w", err)
	}
	objects := make([]access.OwnedObject, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.PrincipalID) != principalID || strings.TrimSpace(row.ID) == "" {
			return access.OwnershipReport{}, fmt.Errorf("agent ownership authority returned an inconsistent owner")
		}
		objects = append(objects, access.OwnedObject{
			Kind: "agent_conversation", ID: row.ID, Name: row.Title,
			OwnerPrincipalID: principalID, Lifecycle: row.Status,
			Transferable: true, TombstoneOnOffboard: true,
		})
	}
	return access.OwnershipReport{PrincipalID: principalID, Objects: objects}, nil
}

func (r *Repository) WithOwnershipDB(db access.OwnershipDBTX) access.OwnershipAuthority {
	return &Repository{db: db}
}

// WithOwnershipMutationDB binds the administrator ownership lifecycle to the
// caller-owned transaction. The returned adapter never commits or rolls back
// that transaction.
func (r *Repository) WithOwnershipMutationDB(db access.OwnershipDBTX) access.OwnershipMutator {
	return &Repository{db: db}
}

// TransferOwnedObjects moves all active, non-tombstoned conversations to the
// target principal while retaining transcript, run, message, and event rows.
// Conversations with an active run are deliberately left untouched; the
// caller's post-condition check then returns a conflict for a safe retry.
func (r *Repository) TransferOwnedObjects(ctx context.Context, principalID, targetPrincipalID string) (access.OwnershipReport, error) {
	principalID, err := principalIDValue(principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	targetPrincipalID, err = principalIDValue(targetPrincipalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if principalID == targetPrincipalID {
		return access.OwnershipReport{}, fmt.Errorf("principal and target principal ids must differ")
	}
	return r.withOwnershipTx(ctx, func(tx Tx, q *agentdb.Queries) (access.OwnershipReport, error) {
		if err := lockAccessPrincipalIfCanonical(ctx, tx, principalID); err != nil {
			return access.OwnershipReport{}, err
		}
		if err := lockAccessPrincipalIfCanonical(ctx, tx, targetPrincipalID); err != nil {
			return access.OwnershipReport{}, err
		}
		rows, err := q.TransferOwnedAgentConversations(ctx, agentdb.TransferOwnedAgentConversationsParams{PrincipalID: principalID, TargetPrincipalID: targetPrincipalID})
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("transfer principal-owned agent conversations: %w", err)
		}
		objects := make([]access.OwnedObject, 0, len(rows))
		for _, row := range rows {
			objects = append(objects, access.OwnedObject{Kind: "agent_conversation", ID: row.ID, Name: row.Title, OwnerPrincipalID: principalID, Lifecycle: row.Status, Transferable: true, TombstoneOnOffboard: true})
		}
		return access.OwnershipReport{PrincipalID: principalID, Objects: objects}, nil
	})
}

// TombstoneOwnedObjects marks active conversations deleted in retained
// metadata and archives them. Child transcript/run/event evidence remains
// queryable to retention and audit tooling, while normal chat lists hide the
// tombstoned rows. Active runs are left untouched and cause the caller's
// post-condition check to fail closed.
func (r *Repository) TombstoneOwnedObjects(ctx context.Context, principalID string) (access.OwnershipReport, error) {
	principalID, err := principalIDValue(principalID)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	return r.withOwnershipTx(ctx, func(tx Tx, q *agentdb.Queries) (access.OwnershipReport, error) {
		if err := lockAccessPrincipalIfCanonical(ctx, tx, principalID); err != nil {
			return access.OwnershipReport{}, err
		}
		rows, err := q.TombstoneOwnedAgentConversations(ctx, principalID)
		if err != nil {
			return access.OwnershipReport{}, fmt.Errorf("tombstone principal-owned agent conversations: %w", err)
		}
		objects := make([]access.OwnedObject, 0, len(rows))
		for _, row := range rows {
			objects = append(objects, access.OwnedObject{Kind: "agent_conversation", ID: row.ID, Name: row.Title, OwnerPrincipalID: principalID, Lifecycle: "deleted", Transferable: true, TombstoneOnOffboard: true})
		}
		return access.OwnershipReport{PrincipalID: principalID, Objects: objects}, nil
	})
}

func (r *Repository) withOwnershipTx(ctx context.Context, fn func(Tx, *agentdb.Queries) (access.OwnershipReport, error)) (access.OwnershipReport, error) {
	if r == nil || r.db == nil {
		return access.OwnershipReport{}, fmt.Errorf("agent ownership authority is unavailable")
	}
	if tx, ok := r.db.(Tx); ok {
		return fn(tx, agentdb.New(tx))
	}
	b, ok := r.db.(beginner)
	if !ok {
		return access.OwnershipReport{}, fmt.Errorf("agent PostgreSQL handle must support transactions")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return access.OwnershipReport{}, err
	}
	defer tx.Rollback(ctx)
	report, err := fn(tx, agentdb.New(tx))
	if err != nil {
		return access.OwnershipReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return access.OwnershipReport{}, err
	}
	return report, nil
}

func principalIDValue(value string) (string, error) {
	return principalID(value)
}

// The standalone agent persistence contract historically allowed opaque
// principal labels in tests and in isolated deployments. Only canonical
// access UUIDs have a row in access.principal and therefore participate in
// access lifecycle serialization.
func lockAccessPrincipalIfCanonical(ctx context.Context, tx DBTX, principalID string) error {
	if _, err := uuid.Parse(strings.TrimSpace(principalID)); err != nil {
		return nil
	}
	return accesslifecycle.LockOwnedPrincipal(ctx, tx, principalID)
}

var _ access.OwnershipAuthority = (*Repository)(nil)
var _ access.TransactionalOwnershipAuthority = (*Repository)(nil)
var _ access.OwnershipMutator = (*Repository)(nil)
var _ access.TransactionalOwnershipMutator = (*Repository)(nil)
