package postgres

import "context"

// ActivationAdmissionPort is the mandatory composition-owned mutation fence.
// Every fresh activation calls it under the target and publication locks,
// immediately before advancing the active pointer. It must use this transaction
// for its reads and must not commit, roll back, or perform external I/O.
// Exact committed replays only return existing evidence and do not call it.
// This is distinct from the optional qualification/semantic pre-commit hook.
type ActivationAdmissionPort func(context.Context, Tx, DeliveryPublication) error

func (r *Repository) ActivationAdmissionCapable() bool {
	return r != nil && r.admission != nil
}
