package materialize

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

// semanticAuditBarrierRecorder pauses the first durable write. The query has
// already evaluated its request-bound decision at that point, but lifecycle
// and authority checks still run before any result can be disclosed.
type semanticAuditBarrierRecorder struct {
	entered chan struct{}
	release chan struct{}

	enteredOnce sync.Once
	releaseOnce sync.Once

	mu     sync.Mutex
	events []access.CanonicalAuditEvent
}

func newSemanticAuditBarrierRecorder() *semanticAuditBarrierRecorder {
	return &semanticAuditBarrierRecorder{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (r *semanticAuditBarrierRecorder) RecordCanonicalAuditEvent(ctx context.Context, event access.CanonicalAuditEvent) error {
	r.enteredOnce.Do(func() {
		close(r.entered)
	})
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *semanticAuditBarrierRecorder) Release() {
	r.releaseOnce.Do(func() {
		close(r.release)
	})
}

func (r *semanticAuditBarrierRecorder) Events() []access.CanonicalAuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]access.CanonicalAuditEvent(nil), r.events...)
}

func TestProtectedSemanticAuditBindsDecisionAcrossAuthorityAndLifecycleRaces(t *testing.T) {
	tests := []struct {
		name             string
		mutateResolution func(*access.SemanticAttributeResolution)
		mutateLifecycle  func(*resultidentity.SemanticLifecycle)
	}{
		{name: "control update", mutateResolution: func(resolution *access.SemanticAttributeResolution) {
			resolution.ControlState.Revision++
			resolution.ControlState.Digest = materializeTestDigest('e')
		}},
		{name: "registry update", mutateResolution: func(resolution *access.SemanticAttributeResolution) {
			resolution.Registry.State.Revision++
			resolution.Registry.State.Digest = materializeTestDigest('d')
		}},
		{name: "tombstone", mutateLifecycle: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.Sequence++
			lifecycle.ActiveBundleID = "bundle:tombstone"
		}},
		{name: "restore", mutateLifecycle: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.Sequence++
			lifecycle.ActiveBundleID = "bundle:restore"
		}},
		{name: "rollback", mutateLifecycle: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.Sequence++
			lifecycle.ActiveBundleID = "bundle:rollback"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, lifecycle, database := newSemanticCacheTestRuntime(t, "")
			authority, ok := runtime.semanticAccessAuthority.(*semanticConsumerAuthority)
			if !ok {
				t.Fatal("test runtime authority has unexpected type")
			}
			authority.mu.Lock()
			original := authority.resolutions[0]
			authority.mu.Unlock()
			modelDigest, err := semanticquery.SemanticModelDigest(runtime.model)
			if err != nil {
				t.Fatal(err)
			}

			barrier := newSemanticAuditBarrierRecorder()
			runtime.semanticAudit.Recorder = barrier
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct {
				result dataquery.Result
				err    error
			}, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				result, err := runtime.ExecuteDataQuery(ctx, semanticCacheTestRequest())
				done <- struct {
					result dataquery.Result
					err    error
				}{result: result, err: err}
			}()
			defer func() {
				barrier.Release()
				cancel()
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Errorf("protected lifecycle race goroutine did not exit during cleanup")
				}
			}()

			select {
			case <-barrier.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("protected decision did not reach the audit persistence barrier")
			}

			if test.mutateResolution != nil {
				changed := original
				test.mutateResolution(&changed)
				authority.mu.Lock()
				authority.resolutions = []access.SemanticAttributeResolution{changed}
				authority.calls = 0
				authority.mu.Unlock()
			}
			if test.mutateLifecycle != nil {
				changed := semanticCacheTestBinding()
				test.mutateLifecycle(&changed)
				lifecycle.Set(changed)
			}
			barrier.Release()

			var outcome struct {
				result dataquery.Result
				err    error
			}
			select {
			case outcome = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("protected lifecycle race did not finish")
			}
			if ctx.Err() != nil || errors.Is(outcome.err, context.Canceled) || errors.Is(outcome.err, context.DeadlineExceeded) {
				t.Fatalf("protected lifecycle race completed only through context cancellation: ctx=%v err=%v", ctx.Err(), outcome.err)
			}
			if outcome.err == nil {
				t.Fatal("authority/lifecycle mutation unexpectedly disclosed a result")
			}
			if len(outcome.result.Rows) != 0 {
				t.Fatal("authority/lifecycle mutation returned rows")
			}
			if got := database.queries.Load(); got != 0 {
				t.Fatalf("authority/lifecycle mutation physical executions = %d, want 0", got)
			}
			if got := runtime.queryCache.scope.Stats().Entries; got != 0 {
				t.Fatalf("authority/lifecycle mutation cache entries = %d, want 0", got)
			}

			events := barrier.Events()
			if len(events) == 0 {
				t.Fatal("protected decision was not retained after persistence barrier")
			}
			for _, event := range events {
				if event.Identity.ProjectID != "project:test" || event.Identity.Environment != "test" || event.Identity.GenerationID != "serving:test" || event.PrincipalID != "principal-1" || event.RequestID != "request-1" || event.Resource.ID() != "sales" || event.Capability != access.CapabilityResourceUse {
					t.Fatalf("audit identity was not bound to the request: %#v", event)
				}
				evidence, err := access.DecodeSemanticDecisionEvidence(event.MetadataJSON)
				if err != nil {
					t.Fatal(err)
				}
				if evidence.InstanceID != "instance:test" || evidence.ActorPrincipalID != "principal-1" || evidence.SemanticModelDigest != modelDigest {
					t.Fatalf("audit evidence identity changed during lifecycle race: %#v", evidence)
				}
				if evidence.Registry.Revision != original.Registry.State.Revision || evidence.Registry.Digest != original.Registry.State.Digest || evidence.Control.Revision != original.ControlState.Revision || evidence.Control.Digest != original.ControlState.Digest {
					t.Fatalf("audit evidence used post-admission authority: %#v", evidence)
				}
			}
		})
	}
}

func TestProtectedSemanticAuditCacheReplayRetainsBoundDecision(t *testing.T) {
	runtime, _, database := newSemanticCacheTestRuntime(t, "")
	recorder := runtime.semanticAudit.Recorder.(*semanticAuditTestRecorder)
	request := semanticCacheTestRequest()
	if result, err := runtime.ExecuteDataQuery(t.Context(), request); err != nil || result.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("protected cache miss = %#v, %v", result, err)
	}
	missEvents := recorder.Events()
	if len(missEvents) == 0 {
		t.Fatal("protected cache miss wrote no audit event")
	}
	if result, err := runtime.ExecuteDataQuery(t.Context(), request); err != nil || result.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("protected cache hit = %#v, %v", result, err)
	}
	hitEvents := recorder.Events()
	if len(hitEvents) <= len(missEvents) {
		t.Fatalf("protected cache hit did not append audit evidence: miss=%d hit=%d", len(missEvents), len(hitEvents))
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("protected cache replay physical executions = %d, want 1", got)
	}
	for _, event := range hitEvents {
		if event.Identity.ProjectID != "project:test" || event.Identity.GenerationID != "serving:test" || event.PrincipalID != "principal-1" || event.RequestID != "request-1" || event.Resource.ID() != "sales" {
			t.Fatalf("cache replay changed audit identity: %#v", event)
		}
		if _, err := access.DecodeSemanticDecisionEvidence(event.MetadataJSON); err != nil {
			t.Fatalf("cache replay retained invalid evidence: %v", err)
		}
	}
}

var _ access.CanonicalAuditRecorder = (*semanticAuditBarrierRecorder)(nil)
