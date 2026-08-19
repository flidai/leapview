// Package jobs contains LeapView's durable-job policy and adapters.
package jobs

import "fmt"

const (
	// SystemPrincipalID is used for internal work that has no end-user actor.
	SystemPrincipalID = "system:durable-jobs"

	// Workload classes admitted by the LeapView durable-job worker.
	WorkloadClassBackground = "background"
	WorkloadClassControl    = "control"

	// WorkerOwnerPrefix scopes lease owners to this product's workers.
	WorkerOwnerPrefix = "leapview-jobs"
)

// WorkerOwner returns a unique owner identity for one runner invocation.
func WorkerOwner(now int64) string {
	return fmt.Sprintf("%s-%d", WorkerOwnerPrefix, now)
}
