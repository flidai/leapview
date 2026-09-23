package access

// RoleBindingAdministrationState is the transport-neutral read model for the
// target-owned project policy. It deliberately returns captured bindings and
// the versioned preset catalog separately: the UI may explain a role, but it
// must never re-expand or reinterpret persisted authority.
type RoleBindingAdministrationState struct {
	Scope               AuthorizationPolicyScope
	Revision            int64
	Digest              string
	RoleBindings        []RoleBinding
	RolePresets         []PermissionRolePreset
	ActiveBindingIDs    []string
	ActiveSnapshotReady bool
}

// RoleBindingAdministrationCommand is the narrow role mutation accepted from
// product administration. Project and actor identity are server-bound.
type RoleBindingAdministrationCommand struct {
	Action           string
	BindingID        string
	Subject          SubjectRef
	Role             PermissionRole
	ExpectedRevision int64
	IdempotencyKey   string
}
