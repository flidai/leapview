package access

import (
	"errors"
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/permissions"
)

var (
	ErrInvalidPermissionPair            = permissions.ErrInvalidPair
	ErrTokenPermissionsNeeded           = errors.New("API token permissions are required")
	ErrTokenPermissionAttenuationNeeded = errors.New("typed API token credential scope is required for permission issuance")
	ErrTokenPermissionNotAllowed        = errors.New("requested API token permission exceeds credential authority")
)

// PermissionTarget is the audience and resource half of one permission pair.
//
// Exact resource targets carry ProjectID, ResourceKind, and ResourceID. Project
// targets carry ProjectID only for Project-scoped actions. A typed Project-wide
// resource selector carries ProjectID, ResourceKind, and IncludeFuture=true;
// selecting existing resources without future inclusion is persisted as exact
// pairs so later creation cannot silently widen the credential. Instance
// targets carry InstanceID only.
type PermissionTarget struct {
	Scope         PermissionScope         `json:"scope"`
	InstanceID    string                  `json:"instanceId,omitempty"`
	ProjectID     projectgraph.ResourceID `json:"projectId,omitempty"`
	ResourceKind  projectgraph.Kind       `json:"resourceKind,omitempty"`
	ResourceID    projectgraph.ResourceID `json:"resourceId,omitempty"`
	IncludeFuture bool                    `json:"includeFuture,omitempty"`
}

// PermissionPair preserves the action/target relationship through consent,
// persistence, and evaluation. Callers must never split a set of pairs into
// independent action and resource arrays, which would create a Cartesian
// product of authority.
type PermissionPair struct {
	Action  Action           `json:"action"`
	Target  PermissionTarget `json:"target"`
	Profile string           `json:"profile"`
}

// NewExactPermissionPair constructs an exact-resource pair.
func NewExactPermissionPair(action Action, projectID projectgraph.ResourceID, resource ResourceRef) (PermissionPair, error) {
	pair := PermissionPair{
		Action: action, Profile: PermissionCatalogProfile,
		Target: PermissionTarget{Scope: PermissionScopeResource, ProjectID: projectID, ResourceKind: resource.Kind(), ResourceID: resource.ID()},
	}
	if err := pair.Validate(); err != nil {
		return PermissionPair{}, err
	}
	return pair, nil
}

// NewProjectPermissionPair constructs a Project-scoped operation pair. It is
// used for administration, delivery, and typed creation actions, not as an
// implicit all-resources selector.
func NewProjectPermissionPair(action Action, projectID projectgraph.ResourceID) (PermissionPair, error) {
	pair := PermissionPair{
		Action: action, Profile: PermissionCatalogProfile,
		Target: PermissionTarget{Scope: PermissionScopeProject, ProjectID: projectID},
	}
	if err := pair.Validate(); err != nil {
		return PermissionPair{}, err
	}
	return pair, nil
}

// NewFutureProjectPermissionPair constructs the only wildcard resource form.
// IncludeFuture is intentionally explicit; current-only selections must be
// expanded to exact pairs when the credential is issued.
func NewFutureProjectPermissionPair(action Action, projectID projectgraph.ResourceID, kind projectgraph.Kind) (PermissionPair, error) {
	pair := PermissionPair{
		Action: action, Profile: PermissionCatalogProfile,
		Target: PermissionTarget{Scope: PermissionScopeProject, ProjectID: projectID, ResourceKind: kind, IncludeFuture: true},
	}
	if err := pair.Validate(); err != nil {
		return PermissionPair{}, err
	}
	return pair, nil
}

// NewInstancePermissionPair constructs an instance-audience pair. Possessing
// this pair attenuates an existing durable platform role; it never grants one.
func NewInstancePermissionPair(action Action, instanceID string) (PermissionPair, error) {
	pair := PermissionPair{
		Action: action, Profile: PermissionCatalogProfile,
		Target: PermissionTarget{Scope: PermissionScopeInstance, InstanceID: instanceID},
	}
	if err := pair.Validate(); err != nil {
		return PermissionPair{}, err
	}
	return pair, nil
}

func (pair PermissionPair) Validate() error {
	return permissionMechanicsCatalog.ValidatePair(contractPermissionPair(pair))
}

func contractPermissionPair(pair PermissionPair) permissions.Pair {
	return permissions.Pair{
		Action:  pair.Action,
		Profile: pair.Profile,
		Target: permissions.Target{
			Scope:         pair.Target.Scope,
			InstanceID:    pair.Target.InstanceID,
			ProjectID:     pair.Target.ProjectID.String(),
			ResourceKind:  permissions.Kind(pair.Target.ResourceKind),
			ResourceID:    pair.Target.ResourceID.String(),
			IncludeFuture: pair.Target.IncludeFuture,
		},
	}
}

func accessPermissionPair(pair permissions.Pair) PermissionPair {
	return PermissionPair{
		Action:  pair.Action,
		Profile: pair.Profile,
		Target: PermissionTarget{
			Scope:         pair.Target.Scope,
			InstanceID:    pair.Target.InstanceID,
			ProjectID:     projectgraph.ResourceID(pair.Target.ProjectID),
			ResourceKind:  projectgraph.Kind(pair.Target.ResourceKind),
			ResourceID:    projectgraph.ResourceID(pair.Target.ResourceID),
			IncludeFuture: pair.Target.IncludeFuture,
		},
	}
}

// ToContractPermissionPair converts the graph-bound access representation to
// the public transport-neutral contract and revalidates product semantics.
func ToContractPermissionPair(pair PermissionPair) (permissions.Pair, error) {
	if err := pair.Validate(); err != nil {
		return permissions.Pair{}, err
	}
	return contractPermissionPair(pair), nil
}

// FromContractPermissionPair converts a public pair into graph-bound product
// types and rejects unknown actions, kinds, profiles, or malformed identities.
func FromContractPermissionPair(pair permissions.Pair) (PermissionPair, error) {
	if err := permissionMechanicsCatalog.ValidatePair(pair); err != nil {
		return PermissionPair{}, err
	}
	converted := accessPermissionPair(pair)
	if converted.Target.ProjectID != "" {
		if err := converted.Target.ProjectID.Validate(); err != nil {
			return PermissionPair{}, fmt.Errorf("%w: invalid project id: %v", ErrInvalidPermissionPair, err)
		}
	}
	if converted.Target.ResourceID != "" {
		if err := converted.Target.ResourceID.Validate(); err != nil {
			return PermissionPair{}, fmt.Errorf("%w: invalid resource id: %v", ErrInvalidPermissionPair, err)
		}
	}
	if converted.Target.ResourceKind != "" && !converted.Target.ResourceKind.Valid() {
		return PermissionPair{}, fmt.Errorf("%w: invalid resource kind %q", ErrInvalidPermissionPair, converted.Target.ResourceKind)
	}
	return converted, nil
}

// ValidatePermissionPairs validates structural integrity and rejects duplicate
// action/target pairs. A nil slice is omitted authority and is rejected for
// new credentials; an explicit empty slice is a valid identity-only token.
func ValidatePermissionPairs(pairs []PermissionPair) error {
	if pairs == nil {
		return ErrTokenPermissionsNeeded
	}
	converted := make(permissions.PairSet, len(pairs))
	for index, pair := range pairs {
		converted[index] = contractPermissionPair(pair)
	}
	return permissionMechanicsCatalog.ValidatePairs(converted)
}

func EncodePermissionPairs(pairs []PermissionPair) ([]byte, error) {
	if pairs == nil {
		return nil, ErrTokenPermissionsNeeded
	}
	converted := make(permissions.PairSet, len(pairs))
	for index, pair := range pairs {
		converted[index] = contractPermissionPair(pair)
	}
	return permissionMechanicsCatalog.Encode(converted)
}

func DecodePermissionPairs(encoded []byte) ([]PermissionPair, error) {
	decoded, err := permissionMechanicsCatalog.Decode(encoded)
	if err != nil {
		if errors.Is(err, permissions.ErrPermissionPairsRequired) {
			return nil, ErrTokenPermissionsNeeded
		}
		return nil, err
	}
	pairs := make([]PermissionPair, len(decoded))
	for index, pair := range decoded {
		pairs[index] = accessPermissionPair(pair)
	}
	return pairs, nil
}

func permissionPairKey(pair PermissionPair) string {
	return contractPermissionPair(pair).Key()
}

// PermissionPairAllows reports whether one persisted credential pair permits
// the requested exact action/target pair. Exact permissions never cross-expand.
// A typed future-resource selector matches only its declared Project and kind.
func PermissionPairAllows(granted, requested PermissionPair) bool {
	return permissionMechanicsCatalog.Allows(contractPermissionPair(granted), contractPermissionPair(requested))
}

// IntersectPermissionPairs applies a credential ceiling to effective
// principal authority while preserving requested pair identity and order.
func IntersectPermissionPairs(credential, effective []PermissionPair) []PermissionPair {
	credentialPairs := make(permissions.PairSet, len(credential))
	for index, pair := range credential {
		credentialPairs[index] = contractPermissionPair(pair)
	}
	effectivePairs := make(permissions.PairSet, len(effective))
	for index, pair := range effective {
		effectivePairs[index] = contractPermissionPair(pair)
	}
	intersection := permissionMechanicsCatalog.Intersect(credentialPairs, effectivePairs)
	result := make([]PermissionPair, len(intersection))
	for index, pair := range intersection {
		result[index] = accessPermissionPair(pair)
	}
	return result
}

// RequiredPermissionPairs returns the requested pair followed by every
// declared prerequisite bound to the same exact target. It does not grant the
// prerequisite: callers must independently prove each returned pair against
// both principal and credential authority. Catalog validation guarantees the
// prerequisite graph is acyclic.
func RequiredPermissionPairs(requested PermissionPair) ([]PermissionPair, error) {
	required, err := permissionMechanicsCatalog.Required(contractPermissionPair(requested))
	if err != nil {
		return nil, err
	}
	result := make([]PermissionPair, len(required))
	for index, pair := range required {
		result[index] = accessPermissionPair(pair)
	}
	return result, nil
}

// PermissionSetAllows requires the principal or credential set to cover the
// requested pair and every catalog prerequisite. It never inserts authority.
func PermissionSetAllows(granted []PermissionPair, requested PermissionPair) bool {
	converted := make(permissions.PairSet, len(granted))
	for index, pair := range granted {
		converted[index] = contractPermissionPair(pair)
	}
	return permissionMechanicsCatalog.Contains(converted, permissions.PairSet{contractPermissionPair(requested)})
}

// ValidatePermissionPairsAgainstAuthority verifies that every requested pair
// is contained in an independently resolved durable authority set. The
// authority set is presentation-independent: callers must resolve it from the
// authenticated principal or credential at the mutation boundary, never from
// browser-supplied picker options. An explicit empty request remains valid.
func ValidatePermissionPairsAgainstAuthority(authority, requested []PermissionPair) error {
	if err := ValidatePermissionPairs(requested); err != nil {
		return err
	}
	if authority == nil {
		authority = []PermissionPair{}
	}
	if err := ValidatePermissionPairs(authority); err != nil {
		return fmt.Errorf("durable permission authority: %w", err)
	}
	for index, pair := range requested {
		// Prerequisites are independent authority checks. In particular,
		// semantic.query is issuable only when the durable authority set also
		// contains semantic.consume for the same exact model.
		if !PermissionSetAllows(authority, pair) {
			return fmt.Errorf("%w: permission %d (%s)", ErrTokenPermissionNotAllowed, index, pair.Action)
		}
	}
	return nil
}

// ValidateTokenPermissionAttenuation verifies that a typed API token may
// issue the requested typed permission set. Both the caller's credential and
// the request are pinned to the catalog profile, and every requested pair is
// checked with PermissionSetAllows so prerequisites and exact action/target
// pairings remain intact. An old or omitted caller scope is ambiguous and is
// never treated as a wildcard.
func ValidateTokenPermissionAttenuation(token APIToken, requested []PermissionPair) error {
	if token.PermissionProfile != PermissionCatalogProfile || token.Permissions == nil {
		return ErrTokenPermissionAttenuationNeeded
	}
	if err := ValidatePermissionPairs(token.Permissions); err != nil {
		return fmt.Errorf("caller credential permissions: %w", err)
	}
	if err := ValidatePermissionPairs(requested); err != nil {
		return err
	}
	for index, pair := range requested {
		if !PermissionSetAllows(token.Permissions, pair) {
			return fmt.Errorf("%w: permission %d (%s)", ErrTokenPermissionNotAllowed, index, pair.Action)
		}
	}
	return nil
}
