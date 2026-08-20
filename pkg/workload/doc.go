// Package workload provides application-neutral process-local workload
// admission contracts.
//
// A host supplies the ordered class identifiers and a policy for every class;
// this package does not define classes, defaults, authentication, or
// authorization.  The package owns the data types and lifecycle boundary used
// by an admission controller.  Scheduling and resource accounting can be
// implemented behind this boundary without coupling callers to a product
// configuration package.
package workload
