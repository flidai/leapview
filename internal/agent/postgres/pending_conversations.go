package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/agent"
	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) BeginPendingConversationAction(ctx context.Context, pending agent.PendingConversationAction) (agent.Conversation, error) {
	if err := agent.ValidatePendingConversationAction(pending); err != nil {
		return agent.Conversation{}, err
	}
	principal, err := principalID(pending.PrincipalID)
	if err != nil {
		return agent.Conversation{}, mapDBError(err)
	}
	var out agent.Conversation
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		if err := q.AcquireAgentConversationMutationLock(ctx, agentdb.AcquireAgentConversationMutationLockParams{ConversationID: pending.ConversationID, PrincipalID: principal}); err != nil {
			return err
		}
		row, err := q.GetAgentConversation(ctx, agentdb.GetAgentConversationParams{ID: pending.ConversationID, PrincipalID: principal})
		if err != nil {
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
		state.PendingAction, state.PendingRequest = pending.Action, pending.RequestID
		state.PendingUntil = pending.Deadline.UTC().Format(time.RFC3339Nano)
		metadata, err := agent.UpdateConversationMetadata(current.MetadataJSON, state)
		if err != nil {
			return err
		}
		affected, err := q.UpdatePendingConversationMetadata(ctx, agentdb.UpdatePendingConversationMetadataParams{MetadataJson: []byte(metadata), ConversationID: pending.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		if affected != 1 {
			return agent.ErrNotFound
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			if err := r.recordAudit(ctx, tx, &intent, pending.ConversationID, pending.ConversationID, nil); err != nil {
				return err
			}
		}
		row, err = q.GetAgentConversation(ctx, agentdb.GetAgentConversationParams{ID: pending.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		out = mapConversation(row)
		return nil
	})
	return out, mapDBError(err)
}

func (r *Repository) CancelPendingConversationAction(ctx context.Context, principal, conversationID, requestID string) error {
	principal, err := principalID(principal)
	if err != nil {
		return mapDBError(err)
	}
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(requestID) == "" {
		return errors.New("conversation and request IDs are required")
	}
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		if err := q.AcquireAgentConversationMutationLock(ctx, agentdb.AcquireAgentConversationMutationLockParams{ConversationID: conversationID, PrincipalID: principal}); err != nil {
			return err
		}
		row, err := q.GetAgentConversation(ctx, agentdb.GetAgentConversationParams{ID: conversationID, PrincipalID: principal})
		if errors.Is(err, pgx.ErrNoRows) {
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
		affected, err := q.UpdatePendingConversationMetadata(ctx, agentdb.UpdatePendingConversationMetadataParams{MetadataJson: []byte(metadata), ConversationID: conversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		if affected != 1 {
			return agent.ErrNotFound
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			return r.recordAudit(ctx, tx, &intent, conversationID, conversationID, nil)
		}
		return nil
	})
	return mapDBError(err)
}

func (r *Repository) FinalizePendingConversationAction(ctx context.Context, pending agent.PendingConversationAction) error {
	if err := agent.ValidatePendingConversationAction(pending); err != nil {
		return err
	}
	principal, err := principalID(pending.PrincipalID)
	if err != nil {
		return mapDBError(err)
	}
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		if err := q.AcquireAgentConversationMutationLock(ctx, agentdb.AcquireAgentConversationMutationLockParams{ConversationID: pending.ConversationID, PrincipalID: principal}); err != nil {
			return err
		}
		row, err := q.GetAgentConversation(ctx, agentdb.GetAgentConversationParams{ID: pending.ConversationID, PrincipalID: principal})
		if errors.Is(err, pgx.ErrNoRows) {
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
			busy, err := q.AgentConversationHasActiveRun(ctx, pending.ConversationID)
			if err != nil {
				return err
			}
			if busy {
				return agent.ErrConversationBusy
			}
			if _, err := q.DeleteAgentConversation(ctx, agentdb.DeleteAgentConversationParams{ID: pending.ConversationID, PrincipalID: principal}); err != nil {
				return err
			}
			domain, err := r.recordDomain(ctx, tx, principal, "agent_conversation", pending.ConversationID, "agent.conversation.deleted", []byte(`{"status":"deleted"}`))
			if err != nil {
				return err
			}
			if intent, ok := agent.AuditIntentFromContext(ctx); ok {
				return r.recordAudit(ctx, tx, &intent, pending.ConversationID, pending.ConversationID, domain)
			}
			return nil
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
			affected, err := q.UpdatePendingConversationMetadata(ctx, agentdb.UpdatePendingConversationMetadataParams{MetadataJson: []byte(metadata), ConversationID: pending.ConversationID, PrincipalID: principal})
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
		affected, err := q.UpdatePendingConversationMetadata(ctx, agentdb.UpdatePendingConversationMetadataParams{MetadataJson: []byte(metadata), ConversationID: pending.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		if affected != 1 {
			return agent.ErrNotFound
		}
		if _, err := q.ArchiveAgentConversation(ctx, agentdb.ArchiveAgentConversationParams{ID: pending.ConversationID, PrincipalID: principal}); err != nil {
			return err
		}
		domain, err := r.recordDomain(ctx, tx, principal, "agent_conversation", pending.ConversationID, "agent.conversation.archived", []byte(`{"status":"archived"}`))
		if err != nil {
			return err
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			return r.recordAudit(ctx, tx, &intent, pending.ConversationID, pending.ConversationID, domain)
		}
		return nil
	})
	return mapDBError(err)
}

func (r *Repository) ListPendingConversationActions(ctx context.Context) ([]agent.PendingConversationAction, error) {
	rows, err := agentdb.New(r.db).ListPendingConversationActions(ctx)
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
