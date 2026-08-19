// Package arrowresult provides immutable, reference-counted ownership for
// Apache Arrow record batches. Builders establish independent ownership of
// borrowed input, while leases pin result buffers for safe concurrent readers.
//
// The package deliberately excludes application row-decoding and transport
// policy. Consumers inspect borrowed batches through Lease.VisitRecords.
package arrowresult
