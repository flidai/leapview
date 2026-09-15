package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/agent"
	platformdb "github.com/flidai/leapview/internal/agent/internal/db"
)

func normalizeConversationIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("conversation id is required")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (r *Repository) BulkArchiveConversations(ctx context.Context, principalID string, conversationIDs []string) ([]agent.Conversation, error) {
	return r.bulkConversationMutation(ctx, principalID, conversationIDs, false)
}

func (r *Repository) BulkDeleteConversations(ctx context.Context, principalID string, conversationIDs []string) ([]agent.Conversation, error) {
	return r.bulkConversationMutation(ctx, principalID, conversationIDs, true)
}

func (r *Repository) bulkConversationMutation(ctx context.Context, principalID string, conversationIDs []string, deleting bool) ([]agent.Conversation, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return nil, err
	}
	conversationIDs, err = normalizeConversationIDs(conversationIDs)
	if err != nil {
		return nil, err
	}
	if len(conversationIDs) == 0 {
		return []agent.Conversation{}, nil
	}
	out := make([]agent.Conversation, 0, len(conversationIDs))
	err = r.withConversationMutationTx(ctx, func(tx *sql.Tx, q *platformdb.Queries) error {
		for _, conversationID := range conversationIDs {
			if err := r.lockConversationForMutation(ctx, q, principalID, conversationID); err != nil {
				return err
			}
			current, err := q.GetAgentConversation(ctx, platformdb.GetAgentConversationParams{ID: conversationID, PrincipalID: principalID})
			if errors.Is(err, sql.ErrNoRows) {
				return agent.ErrNotFound
			}
			if err != nil {
				return err
			}
			if deleting {
				if err := r.rejectActiveConversationRun(ctx, q, conversationID); err != nil {
					return err
				}
			}
			var row platformdb.AgentConversation
			if deleting {
				row, err = q.DeleteAgentConversation(ctx, platformdb.DeleteAgentConversationParams{ID: conversationID, PrincipalID: principalID})
			} else if current.Status == agent.ConversationStatusActive {
				row, err = q.ArchiveAgentConversation(ctx, platformdb.ArchiveAgentConversationParams{ID: conversationID, PrincipalID: principalID})
			} else {
				out = append(out, mapConversation(current))
				continue
			}
			if errors.Is(err, sql.ErrNoRows) {
				return agent.ErrNotFound
			}
			if err != nil {
				return err
			}
			changed := mapConversation(row)
			if deleting {
				changed.Status = agent.ConversationStatusDeleted
				changed.DeletedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			out = append(out, changed)
			if err := r.recordConversationAudit(ctx, tx, conversationID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
