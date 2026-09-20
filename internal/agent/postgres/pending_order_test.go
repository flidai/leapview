package postgres

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/agent"
)

func TestPendingUndoPreservesHistoryOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		action   string
		pinned   bool
		archived bool
	}{
		{name: "delete", action: agent.PendingConversationDelete},
		{name: "archive", action: agent.PendingConversationArchive},
		{name: "pinned delete", action: agent.PendingConversationDelete, pinned: true},
		{name: "archived delete", action: agent.PendingConversationDelete, archived: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pool, repo := agentPostgresTestRepo(t, "undo_order")
			owner := "owner"
			for i := range 3 {
				conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner, Title: fmt.Sprintf("Chat %d", i)})
				if err != nil {
					t.Fatal(err)
				}
				if tc.pinned {
					if _, err := repo.SetConversationPinned(ctx, owner, conversation.ID, true); err != nil {
						t.Fatal(err)
					}
				}
				if tc.archived {
					if _, err := repo.ArchiveConversation(ctx, owner, conversation.ID); err != nil {
						t.Fatal(err)
					}
				}
				// Seed distinct past activity times without sleeps or clock-resolution ties.
				stamp := fmt.Sprintf("2025-01-0%d 12:00:00", i+1)
				if _, err := pool.Exec(ctx, `UPDATE agent.conversations SET updated_at = $1::text::timestamptz WHERE id = $2`, stamp, conversation.ID); err != nil {
					t.Fatal(err)
				}
			}
			list := repo.ListConversations
			if tc.archived {
				list = repo.ListArchivedConversations
			}
			before, err := list(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != 3 {
				t.Fatalf("history length = %d, want 3", len(before))
			}
			middle := before[1]
			pending := agent.PendingConversationAction{PrincipalID: owner, ConversationID: middle.ID, Action: tc.action, RequestID: "undo-order", Deadline: time.Now().UTC().Add(time.Minute)}
			if _, err := repo.BeginPendingConversationAction(ctx, pending); err != nil {
				t.Fatal(err)
			}
			hidden, err := list(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			if len(hidden) != 2 || hidden[0].ID != before[0].ID || hidden[1].ID != before[2].ID {
				t.Fatalf("pending action did not hide only the middle chat: %#v", hidden)
			}
			for range 2 {
				if err := repo.CancelPendingConversationAction(ctx, owner, middle.ID, pending.RequestID); err != nil {
					t.Fatal(err)
				}
				restored, err := list(ctx, owner)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.EqualFunc(before, restored, func(a, b agent.Conversation) bool {
					return a.ID == b.ID && a.UpdatedAt == b.UpdatedAt && a.Pinned == b.Pinned && a.Status == b.Status
				}) {
					t.Fatalf("Undo changed history order or activity: before=%#v after=%#v", before, restored)
				}
			}
			if tc.archived {
				return
			}
			// A real transcript update during the Undo window must still advance recency.
			pending.RequestID = "undo-with-activity"
			if _, err := repo.BeginPendingConversationAction(ctx, pending); err != nil {
				t.Fatal(err)
			}
			updated, err := repo.UpdateConversationTranscript(ctx, owner, middle.ID, `[{"role":"user","content":"New activity"}]`, middle.TranscriptRevision)
			if err != nil {
				t.Fatal(err)
			}
			if updated.UpdatedAt == middle.UpdatedAt {
				t.Fatal("new activity did not update recency")
			}
			if err := repo.CancelPendingConversationAction(ctx, owner, middle.ID, pending.RequestID); err != nil {
				t.Fatal(err)
			}
			afterActivity, err := list(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			if len(afterActivity) != 3 || afterActivity[0].ID != middle.ID || afterActivity[0].UpdatedAt != updated.UpdatedAt {
				t.Fatalf("Undo lost genuine activity recency: %#v", afterActivity)
			}
		})
	}
}

func TestPinAndUnpinPreserveHistoryOrder(t *testing.T) {
	ctx := t.Context()
	pool, repo := agentPostgresTestRepo(t, "pin_order")
	const owner = "owner"
	for i := range 3 {
		conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner, Title: fmt.Sprintf("Chat %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		stamp := fmt.Sprintf("2025-01-0%d 12:00:00", i+1)
		if _, err := pool.Exec(ctx, `UPDATE agent.conversations SET updated_at = $1::text::timestamptz WHERE id = $2`, stamp, conversation.ID); err != nil {
			t.Fatal(err)
		}
	}
	before, err := repo.ListConversations(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	target := before[2]
	if _, err := repo.SetConversationPinned(ctx, owner, target.ID, true); err != nil {
		t.Fatal(err)
	}
	pinned, err := repo.ListConversations(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if pinned[0].ID != target.ID || pinned[0].UpdatedAt != target.UpdatedAt {
		t.Fatalf("pin changed activity recency: before=%#v after=%#v", target, pinned[0])
	}
	if _, err := repo.SetConversationPinned(ctx, owner, target.ID, false); err != nil {
		t.Fatal(err)
	}
	after, err := repo.ListConversations(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(before, after, func(a, b agent.Conversation) bool {
		return a.ID == b.ID && a.UpdatedAt == b.UpdatedAt && a.Pinned == b.Pinned
	}) {
		t.Fatalf("unpin did not restore history order: before=%#v after=%#v", before, after)
	}
}
