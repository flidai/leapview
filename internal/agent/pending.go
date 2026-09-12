package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	PendingConversationArchive = "archive"
	PendingConversationDelete  = "delete"
	PendingConversationWindow  = 10 * time.Second
)

var (
	ErrPendingConversationUnsupported = errors.New("pending conversation actions are unavailable")
	ErrPendingConversationExpired     = errors.New("conversation undo window has expired")
	ErrPendingConversationNotDue      = errors.New("conversation undo window has not expired")
	ErrPendingConversationCanceled    = errors.New("conversation action was canceled")
)

// pendingConversationLifecycle keeps the durable metadata and the wake-up
// timer together. The timer is only an optimization: Start rehydrates every
// persisted pending action, so a process restart cannot strand an action.
type pendingConversationLifecycle struct {
	repo PendingConversationRepository

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
	timers  map[string]*time.Timer
}

func newPendingConversationLifecycle(repo PendingConversationRepository) *pendingConversationLifecycle {
	return &pendingConversationLifecycle{repo: repo, timers: make(map[string]*time.Timer)}
}

func pendingConversationKey(action PendingConversationAction) string {
	return action.PrincipalID + "\x00" + action.ConversationID + "\x00" + action.RequestID
}

func (l *pendingConversationLifecycle) start(ctx context.Context) error {
	if l == nil || l.repo == nil {
		return ErrPendingConversationUnsupported
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return nil
	}
	l.ctx, l.cancel = context.WithCancel(ctx)
	l.started = true
	l.mu.Unlock()
	pending, err := l.repo.ListPendingConversationActions(ctx)
	if err != nil {
		l.stop()
		return err
	}
	for _, action := range pending {
		l.schedule(action)
	}
	go l.scanLoop()
	return nil
}

// scanLoop lets a second healthy instance take over a persisted action when
// the instance that first observed it stops before its timer fires. The
// repository mutation is request-scoped and locked, so duplicate observations
// converge to one archive/delete.
func (l *pendingConversationLifecycle) scanLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		l.mu.Lock()
		ctx := l.ctx
		l.mu.Unlock()
		if ctx == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pending, err := l.repo.ListPendingConversationActions(ctx)
			if err != nil {
				continue
			}
			for _, action := range pending {
				l.schedule(action)
			}
		}
	}
}

func (l *pendingConversationLifecycle) stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
	}
	for key, timer := range l.timers {
		timer.Stop()
		delete(l.timers, key)
	}
	l.started = false
}

func (l *pendingConversationLifecycle) schedule(action PendingConversationAction) {
	if l == nil || l.repo == nil || strings.TrimSpace(action.RequestID) == "" {
		return
	}
	key := pendingConversationKey(action)
	l.mu.Lock()
	if old := l.timers[key]; old != nil {
		old.Stop()
	}
	ctx := l.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	delay := time.Until(action.Deadline)
	if delay < 0 {
		delay = 0
	}
	l.timers[key] = time.AfterFunc(delay, func() {
		l.finalize(ctx, key, action)
	})
	l.mu.Unlock()
}

func (l *pendingConversationLifecycle) finalize(ctx context.Context, key string, action PendingConversationAction) {
	err := l.repo.FinalizePendingConversationAction(ctx, action)
	if errors.Is(err, ErrPendingConversationNotDue) {
		l.schedule(action)
		return
	}
	if err != nil && !errors.Is(err, ErrPendingConversationCanceled) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrPendingConversationExpired) {
		// A transient database or active-run failure must not turn a durable
		// pending action into a permanent no-op. Retry while retaining the exact
		// principal, conversation, and request identity.
		l.mu.Lock()
		if l.started && l.ctx != nil && l.ctx.Err() == nil {
			l.timers[key] = time.AfterFunc(time.Second, func() { l.finalize(l.ctx, key, action) })
		}
		l.mu.Unlock()
		return
	}
	l.mu.Lock()
	delete(l.timers, key)
	l.mu.Unlock()
}

func (l *pendingConversationLifecycle) begin(ctx context.Context, action PendingConversationAction) (PendingConversationAction, error) {
	if l == nil || l.repo == nil {
		return PendingConversationAction{}, ErrPendingConversationUnsupported
	}
	conversation, err := l.repo.BeginPendingConversationAction(ctx, action)
	if err != nil {
		return PendingConversationAction{}, err
	}
	if state, parseErr := ParseConversationMetadata(conversation.MetadataJSON); parseErr == nil && state.PendingAction != "" {
		action.Action = state.PendingAction
		action.RequestID = state.PendingRequest
		if deadline, deadlineErr := time.Parse(time.RFC3339Nano, state.PendingUntil); deadlineErr == nil {
			action.Deadline = deadline
		}
	}
	l.schedule(action)
	return action, nil
}

func (l *pendingConversationLifecycle) cancelAction(ctx context.Context, principalID, conversationID, requestID string) error {
	if l == nil || l.repo == nil {
		return ErrPendingConversationUnsupported
	}
	if err := l.repo.CancelPendingConversationAction(ctx, principalID, conversationID, requestID); err != nil {
		return err
	}
	l.mu.Lock()
	key := strings.TrimSpace(principalID) + "\x00" + strings.TrimSpace(conversationID) + "\x00" + strings.TrimSpace(requestID)
	if timer := l.timers[key]; timer != nil {
		timer.Stop()
		delete(l.timers, key)
	}
	l.mu.Unlock()
	return nil
}

func ValidatePendingConversationAction(action PendingConversationAction) error {
	if strings.TrimSpace(action.PrincipalID) == "" || strings.TrimSpace(action.ConversationID) == "" || strings.TrimSpace(action.RequestID) == "" {
		return fmt.Errorf("pending conversation action requires principal, conversation, and request IDs")
	}
	if action.Action != PendingConversationArchive && action.Action != PendingConversationDelete {
		return fmt.Errorf("unsupported pending conversation action %q", action.Action)
	}
	if action.Deadline.IsZero() {
		return fmt.Errorf("pending conversation deadline is required")
	}
	return nil
}
