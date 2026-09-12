package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

type pendingLifecycleRepository struct {
	mu        sync.Mutex
	rows      []PendingConversationAction
	finalized chan PendingConversationAction
}

func (r *pendingLifecycleRepository) BeginPendingConversationAction(_ context.Context, action PendingConversationAction) (Conversation, error) {
	state := ConversationMetadata{PendingAction: action.Action, PendingRequest: action.RequestID, PendingUntil: action.Deadline.UTC().Format(time.RFC3339Nano)}
	metadata, err := UpdateConversationMetadata(`{}`, state)
	if err != nil {
		return Conversation{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.rows {
		if existing.RequestID == action.RequestID {
			return Conversation{MetadataJSON: metadata}, nil
		}
	}
	r.rows = append(r.rows, action)
	return Conversation{MetadataJSON: metadata}, nil
}

func (r *pendingLifecycleRepository) CancelPendingConversationAction(_ context.Context, _, _, _ string) error {
	return nil
}

func (r *pendingLifecycleRepository) FinalizePendingConversationAction(_ context.Context, action PendingConversationAction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index, existing := range r.rows {
		if existing.RequestID == action.RequestID {
			r.rows = append(r.rows[:index], r.rows[index+1:]...)
			if r.finalized != nil {
				r.finalized <- action
			}
			return nil
		}
	}
	return ErrPendingConversationCanceled
}

func (r *pendingLifecycleRepository) ListPendingConversationActions(context.Context) ([]PendingConversationAction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]PendingConversationAction(nil), r.rows...), nil
}

func TestPendingLifecycleRehydratesPersistedActionAfterStart(t *testing.T) {
	deadline := time.Now().UTC().Add(30 * time.Millisecond)
	repo := &pendingLifecycleRepository{rows: []PendingConversationAction{{PrincipalID: "owner", ConversationID: "conversation", Action: PendingConversationDelete, RequestID: "request", Deadline: deadline}}, finalized: make(chan PendingConversationAction, 1)}
	lifecycle := newPendingConversationLifecycle(repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := lifecycle.start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-repo.finalized:
		if got.RequestID != "request" || got.PrincipalID != "owner" {
			t.Fatalf("finalized action = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted pending action was not finalized")
	}
	lifecycle.stop()
}

func TestPendingLifecycleBeginKeepsPersistedDeadlineOnRetry(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Minute)
	repo := &pendingLifecycleRepository{}
	lifecycle := newPendingConversationLifecycle(repo)
	got, err := lifecycle.begin(context.Background(), PendingConversationAction{PrincipalID: "owner", ConversationID: "conversation", Action: PendingConversationArchive, RequestID: "request", Deadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deadline.Equal(deadline) {
		t.Fatalf("deadline = %s, want %s", got.Deadline, deadline)
	}
	lifecycle.stop()
}
