package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/agent"
	platformdb "github.com/flidai/leapview/internal/agent/internal/db"
)

func (r *Repository) BeginPendingConversationAction(ctx context.Context, pending agent.PendingConversationAction) (agent.Conversation, error) {
	if err := agent.ValidatePendingConversationAction(pending); err != nil {
		return agent.Conversation{}, err
	}
	principalID, err := agentPrincipalID(pending.PrincipalID)
	if err != nil {
		return agent.Conversation{}, err
	}
	var out agent.Conversation
	err = r.withConversationMutationTx(ctx, func(tx *sql.Tx, q *platformdb.Queries) error {
		if err := r.lockConversationForMutation(ctx, q, principalID, pending.ConversationID); err != nil {
			return err
		}
		row, err := q.GetAgentConversation(ctx, platformdb.GetAgentConversationParams{ID: pending.ConversationID, PrincipalID: principalID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return agent.ErrNotFound
			}
			return err
		}
		current := mapConversation(row)
		state, err := agent.ParseConversationMetadata(current.MetadataJSON)
		if err != nil {
			return err
		}
		if state.DeletedAt != "" {
			return agent.ErrNotFound
		}
		if state.PendingAction != "" {
			if state.PendingRequest == pending.RequestID && state.PendingAction == pending.Action {
				out = current
				return nil
			}
			return agent.ErrConversationBusy
		}
		if pending.Action == agent.PendingConversationArchive && current.Status != agent.ConversationStatusActive {
			return agent.ErrConversationArchived
		}
		state.PendingAction = pending.Action
		state.PendingRequest = pending.RequestID
		state.PendingUntil = pending.Deadline.UTC().Format(time.RFC3339Nano)
		metadata, err := agent.UpdateConversationMetadata(current.MetadataJSON, state)
		if err != nil {
			return err
		}
		affected, err := q.UpdatePendingConversationMetadata(ctx, platformdb.UpdatePendingConversationMetadataParams{MetadataJson: metadata, ConversationID: pending.ConversationID, PrincipalID: principalID})
		if err != nil {
			return err
		}
		if affected != 1 {
			return agent.ErrNotFound
		}
		if err := r.recordConversationAudit(ctx, tx, pending.ConversationID); err != nil {
			return err
		}
		row, err = q.GetAgentConversation(ctx, platformdb.GetAgentConversationParams{ID: pending.ConversationID, PrincipalID: principalID})
		if err != nil {
			return err
		}
		out = mapConversation(row)
		return nil
	})
	return out, err
}

func (r *Repository) CancelPendingConversationAction(ctx context.Context, principalID, conversationID, requestID string) error {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(requestID) == "" {
		return fmt.Errorf("conversation and request IDs are required")
	}
	return r.withConversationMutationTx(ctx, func(tx *sql.Tx, q *platformdb.Queries) error {
		if err := r.lockConversationForMutation(ctx, q, principalID, conversationID); err != nil {
			return err
		}
		row, err := q.GetAgentConversation(ctx, platformdb.GetAgentConversationParams{ID: conversationID, PrincipalID: principalID})
		if errors.Is(err, sql.ErrNoRows) {
			return agent.ErrNotFound
		}
		if err != nil {
			return err
		}
		current := mapConversation(row)
		state, err := agent.ParseConversationMetadata(current.MetadataJSON)
		if err != nil {
			return err
		}
		if state.PendingAction == "" {
			return agent.ErrPendingConversationCanceled
		}
		if state.PendingRequest != requestID {
			return agent.ErrPendingConversationCanceled
		}
		deadline, err := time.Parse(time.RFC3339Nano, state.PendingUntil)
		if err != nil || !time.Now().UTC().Before(deadline) {
			return agent.ErrPendingConversationExpired
		}
		state.PendingAction, state.PendingRequest, state.PendingUntil = "", "", ""
		metadata, err := agent.UpdateConversationMetadata(current.MetadataJSON, state)
		if err != nil {
			return err
		}
		affected, err := q.UpdatePendingConversationMetadata(ctx, platformdb.UpdatePendingConversationMetadataParams{MetadataJson: metadata, ConversationID: conversationID, PrincipalID: principalID})
		if err != nil {
			return err
		}
		if affected != 1 {
			return agent.ErrNotFound
		}
		return r.recordConversationAudit(ctx, tx, conversationID)
	})
}

func (r *Repository) FinalizePendingConversationAction(ctx context.Context, pending agent.PendingConversationAction) error {
	if err := agent.ValidatePendingConversationAction(pending); err != nil {
		return err
	}
	principalID, err := agentPrincipalID(pending.PrincipalID)
	if err != nil {
		return err
	}
	return r.withConversationMutationTx(ctx, func(tx *sql.Tx, q *platformdb.Queries) error {
		if err := r.lockConversationForMutation(ctx, q, principalID, pending.ConversationID); err != nil {
			return err
		}
		row, err := q.GetAgentConversation(ctx, platformdb.GetAgentConversationParams{ID: pending.ConversationID, PrincipalID: principalID})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		current := mapConversation(row)
		state, err := agent.ParseConversationMetadata(current.MetadataJSON)
		if err != nil {
			return err
		}
		if state.PendingAction == "" {
			return nil
		}
		if state.PendingRequest != pending.RequestID || state.PendingAction != pending.Action {
			return agent.ErrPendingConversationCanceled
		}
		deadline, err := time.Parse(time.RFC3339Nano, state.PendingUntil)
		if err != nil {
			return err
		}
		if time.Now().UTC().Before(deadline) {
			return agent.ErrPendingConversationNotDue
		}
		if pending.Action == agent.PendingConversationDelete {
			if err := r.rejectActiveConversationRun(ctx, q, pending.ConversationID); err != nil {
				return err
			}
			if _, err := q.DeleteAgentConversation(ctx, platformdb.DeleteAgentConversationParams{ID: pending.ConversationID, PrincipalID: principalID}); errors.Is(err, sql.ErrNoRows) {
				return agent.ErrNotFound
			} else if err != nil {
				return err
			}
			return r.recordConversationAudit(ctx, tx, pending.ConversationID)
		}
		if current.Status != agent.ConversationStatusActive {
			if current.Status != agent.ConversationStatusArchived {
				return agent.ErrConversationArchived
			}
			state.PendingAction, state.PendingRequest, state.PendingUntil = "", "", ""
			metadata, err := agent.UpdateConversationMetadata(current.MetadataJSON, state)
			if err != nil {
				return err
			}
			affected, err := q.UpdatePendingConversationMetadata(ctx, platformdb.UpdatePendingConversationMetadataParams{MetadataJson: metadata, ConversationID: pending.ConversationID, PrincipalID: principalID})
			if err != nil {
				return err
			}
			if affected != 1 {
				return agent.ErrNotFound
			}
			return nil
		}
		state.PendingAction, state.PendingRequest, state.PendingUntil = "", "", ""
		metadata, err := agent.UpdateConversationMetadata(current.MetadataJSON, state)
		if err != nil {
			return err
		}
		affected, err := q.UpdatePendingConversationMetadata(ctx, platformdb.UpdatePendingConversationMetadataParams{MetadataJson: metadata, ConversationID: pending.ConversationID, PrincipalID: principalID})
		if err != nil {
			return err
		}
		if affected != 1 {
			return agent.ErrNotFound
		}
		if _, err := q.ArchiveAgentConversation(ctx, platformdb.ArchiveAgentConversationParams{ID: pending.ConversationID, PrincipalID: principalID}); errors.Is(err, sql.ErrNoRows) {
			return agent.ErrNotFound
		} else if err != nil {
			return err
		}
		return r.recordConversationAudit(ctx, tx, pending.ConversationID)
	})
}

func (r *Repository) ListPendingConversationActions(ctx context.Context) ([]agent.PendingConversationAction, error) {
	rows, err := r.q.ListPendingConversationActions(ctx)
	if err != nil {
		return nil, err
	}
	var out []agent.PendingConversationAction
	for _, row := range rows {
		deadline, err := time.Parse(time.RFC3339Nano, row.PendingUntil)
		if err != nil {
			continue
		}
		out = append(out, agent.PendingConversationAction{PrincipalID: row.PrincipalID, ConversationID: row.ID, Action: row.PendingAction, RequestID: row.PendingRequestID, Deadline: deadline})
	}
	return out, nil
}
