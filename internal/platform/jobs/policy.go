// Package jobs contains LeapView's durable-job policy and adapters.
package jobs

const (
	// SystemPrincipalID is used for internal work that has no end-user actor.
	SystemPrincipalID = "system:durable-jobs"

	// Workload classes admitted by the LeapView durable-job worker.
	WorkloadClassBackground = "background"
	WorkloadClassControl    = "control"

)
