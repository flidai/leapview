package agent

import (
	"context"
	"errors"
	"time"
)

// ModelRequestUsage is the instance-wide count of model requests in the
// current UTC day and the next UTC midnight at which that count resets.
type ModelRequestUsage struct {
	Used     int64
	ResetsAt time.Time
}

// ErrModelRequestLimit indicates that reserving another model request would
// exceed the configured instance-wide daily limit.
var ErrModelRequestLimit = errors.New("daily Agent request limit reached; try again after midnight UTC")

// ModelRequestUsageStore persists and atomically reserves daily model request
// capacity for the Agent capability.
type ModelRequestUsageStore interface {
	ModelRequestUsage(context.Context) (ModelRequestUsage, error)
	ReserveModelRequest(context.Context, int64) (ModelRequestUsage, error)
}
