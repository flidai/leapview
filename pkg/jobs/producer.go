package jobs

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrStoreRequired = errors.New("async job store is required")

// Enqueuer is the durable subset needed by a job producer.
type Enqueuer interface {
	Enqueue(context.Context, EnqueueInput) (Job, error)
}

// Canceller is the durable subset needed by a queued-job cancellation API.
type Canceller interface {
	Cancel(context.Context, string) error
}

// JSONEnqueueInput is EnqueueInput with a payload that is encoded as JSON by
// EnqueueJSON.
type JSONEnqueueInput struct {
	ID                   string
	Kind                 string
	WorkloadClass        string
	PrincipalID          string
	GroupIDs             []string
	ResourceKind         string
	ResourceID           string
	EstimatedMemoryBytes int64
	Payload              any
}

// EnqueueJSON marshals input.Payload and enqueues the resulting durable job.
func EnqueueJSON(ctx context.Context, queue Enqueuer, input JSONEnqueueInput) error {
	if queue == nil {
		return ErrStoreRequired
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return err
	}
	_, err = queue.Enqueue(ctx, EnqueueInput{
		ID: input.ID, Kind: input.Kind, WorkloadClass: input.WorkloadClass,
		PrincipalID: input.PrincipalID, GroupIDs: input.GroupIDs,
		ResourceKind: input.ResourceKind, ResourceID: input.ResourceID,
		EstimatedMemoryBytes: input.EstimatedMemoryBytes, Payload: payload,
	})
	return err
}

// AppendJSONEvent marshals data and appends it to the durable event stream.
func AppendJSONEvent(ctx context.Context, store EventAppender, resourceKind, resourceID, eventType string, data any) error {
	if store == nil {
		return ErrStoreRequired
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = store.AppendEvent(ctx, resourceKind, resourceID, eventType, encoded)
	return err
}

// CancelQueued requests cancellation of a queued job. A claim race is
// reported as (false, nil), since the worker that won the race owns the next
// state transition.
func CancelQueued(ctx context.Context, queue Canceller, id string) (bool, error) {
	if queue == nil {
		return false, ErrStoreRequired
	}
	err := queue.Cancel(ctx, id)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrConflict) {
		return false, nil
	}
	return false, err
}
