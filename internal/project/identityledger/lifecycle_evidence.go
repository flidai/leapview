package identityledger

// LifecycleEvidence is the coherent lifecycle and publication observation for
// one instance-qualified authored identity. Sequence is the append-only
// resource_identity_history sequence and therefore changes for every active,
// tombstone, restore, or rollback transition.
//
// Publication is immutable exact-replay evidence for the requested contract
// version. It is deliberately carried alongside the current identity rather
// than becoming another cache identity or digest authority.
type LifecycleEvidence struct {
	Identity    Identity
	Sequence    int64
	Publication ContractPublication
}
