package jobs

import (
	"context"
	"errors"
	"testing"
)

type producerTestStore struct {
	enqueued EnqueueInput
	event    Event
	cancel   error
}

func (s *producerTestStore) Enqueue(_ context.Context, input EnqueueInput) (Job, error) {
	s.enqueued = input
	return Job{ID: input.ID}, nil
}

func (s *producerTestStore) AppendEvent(_ context.Context, kind, id, eventType string, data []byte) (Event, error) {
	s.event = Event{ResourceKind: kind, ResourceID: id, EventType: eventType, Data: data}
	return s.event, nil
}

func (s *producerTestStore) Cancel(context.Context, string) error { return s.cancel }

func TestEnqueueJSONPreservesDeclaredWorkloadAndEncodesPayload(t *testing.T) {
	store := &producerTestStore{}
	err := EnqueueJSON(t.Context(), store, JSONEnqueueInput{
		ID: "agent:run-1", Kind: "agent.run", WorkloadClass: "background",
		PrincipalID: "principal-1", GroupIDs: []string{"team-a"}, ResourceKind: "agent_run", ResourceID: "run-1", EstimatedMemoryBytes: 1,
		Payload: struct {
			Run string `json:"run"`
		}{Run: "run-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(store.enqueued.Payload); got != `{"run":"run-1"}` {
		t.Fatalf("payload = %s", got)
	}
	if store.enqueued.WorkloadClass != "background" || store.enqueued.PrincipalID != "principal-1" || store.enqueued.EstimatedMemoryBytes != 1 {
		t.Fatalf("workload = %#v", store.enqueued)
	}
}

func TestAppendJSONEventEncodesData(t *testing.T) {
	store := &producerTestStore{}
	if err := AppendJSONEvent(t.Context(), store, "upload", "upload-1", "upload.created", map[string]string{"status": "created"}); err != nil {
		t.Fatal(err)
	}
	if got := string(store.event.Data); got != `{"status":"created"}` {
		t.Fatalf("event data = %s", got)
	}
}

func TestCancelQueuedReportsClaimConflictWithoutError(t *testing.T) {
	store := &producerTestStore{cancel: ErrConflict}
	cancelled, err := CancelQueued(t.Context(), store, "job-1")
	if err != nil || cancelled {
		t.Fatalf("cancelled = %v, err = %v", cancelled, err)
	}

	store.cancel = errors.New("storage unavailable")
	if _, err := CancelQueued(t.Context(), store, "job-1"); err == nil {
		t.Fatal("expected storage error")
	}
}

func TestProducerHelpersRejectMissingStores(t *testing.T) {
	if err := EnqueueJSON(t.Context(), nil, JSONEnqueueInput{}); err == nil {
		t.Fatal("expected missing queue error")
	}
	if err := AppendJSONEvent(t.Context(), nil, "", "", "", nil); err == nil {
		t.Fatal("expected missing event store error")
	}
	if _, err := CancelQueued(t.Context(), nil, "job-1"); err == nil {
		t.Fatal("expected missing queue error")
	}
}
