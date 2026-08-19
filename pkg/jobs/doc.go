// Package jobs provides durable, leased background work primitives.
//
// The package deliberately contains only portable models and ports. Storage
// adapters implement Repository, while applications provide workload
// admission, handlers, worker identity, and failure-payload policy.
package jobs
