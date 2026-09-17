package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
)

var (
	ErrInvalidPermissionPair            = errors.New("invalid permission action/resource pair")
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
	if pair.Profile != PermissionCatalogProfile {
		return fmt.Errorf("%w: unsupported profile %q", ErrInvalidPermissionPair, pair.Profile)
	}
	definition, ok := Permission(pair.Action)
	if !ok {
		return fmt.Errorf("%w: %w %q", ErrInvalidPermissionPair, ErrUnknownPermissionAction, pair.Action)
	}
	target := pair.Target
	switch target.Scope {
	case PermissionScopeInstance:
		if definition.Scope != PermissionScopeInstance || !validTargetIdentity(target.InstanceID) || target.ProjectID != "" || target.ResourceKind != "" || target.ResourceID != "" || target.IncludeFuture {
			return fmt.Errorf("%w: action %q has invalid instance target", ErrInvalidPermissionPair, pair.Action)
		}
	case PermissionScopeResource:
		if definition.Scope != PermissionScopeResource || target.InstanceID != "" || target.IncludeFuture || target.ProjectID.Validate() != nil || target.ResourceID.Validate() != nil || !kindAllowed(definition.ResourceKinds, target.ResourceKind) {
			return fmt.Errorf("%w: action %q has invalid exact resource target", ErrInvalidPermissionPair, pair.Action)
		}
	case PermissionScopeProject:
		if target.InstanceID != "" || target.ResourceID != "" || target.ProjectID.Validate() != nil {
			return fmt.Errorf("%w: action %q has invalid Project target", ErrInvalidPermissionPair, pair.Action)
		}
		if definition.Scope == PermissionScopeProject {
			if target.ResourceKind != "" || target.IncludeFuture {
				return fmt.Errorf("%w: Project-scoped action %q cannot carry a resource wildcard", ErrInvalidPermissionPair, pair.Action)
			}
		} else if definition.Scope == PermissionScopeResource {
			if !target.IncludeFuture || !kindAllowed(definition.ResourceKinds, target.ResourceKind) {
				return fmt.Errorf("%w: resource action %q requires an explicit typed future-resource selector", ErrInvalidPermissionPair, pair.Action)
			}
		} else {
			return fmt.Errorf("%w: instance action %q cannot target a Project", ErrInvalidPermissionPair, pair.Action)
		}
	default:
		return fmt.Errorf("%w: action %q has target scope %q", ErrInvalidPermissionPair, pair.Action, target.Scope)
	}
	return nil
}

func validTargetIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 255 && !strings.ContainsAny(value, "\x00\r\n\t")
}

func kindAllowed(kinds []projectgraph.Kind, candidate projectgraph.Kind) bool {
	for _, kind := range kinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

// ValidatePermissionPairs validates structural integrity and rejects duplicate
// action/target pairs. A nil slice is omitted authority and is rejected for
// new credentials; an explicit empty slice is a valid identity-only token.
func ValidatePermissionPairs(pairs []PermissionPair) error {
	if pairs == nil {
		return ErrTokenPermissionsNeeded
	}
	seen := make(map[string]struct{}, len(pairs))
	for index, pair := range pairs {
		if err := pair.Validate(); err != nil {
			return fmt.Errorf("permission %d: %w", index, err)
		}
		key := permissionPairKey(pair)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate permission %q", ErrInvalidPermissionPair, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func EncodePermissionPairs(pairs []PermissionPair) ([]byte, error) {
	if err := ValidatePermissionPairs(pairs); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(pairs)
	if err != nil {
		return nil, fmt.Errorf("encode permission pairs: %w", err)
	}
	return encoded, nil
}

func DecodePermissionPairs(encoded []byte) ([]PermissionPair, error) {
	var pairs []PermissionPair
	if err := strictjson.Decode(encoded, &pairs); err != nil {
		return nil, fmt.Errorf("decode permission pairs: %w", err)
	}
	if err := ValidatePermissionPairs(pairs); err != nil {
		return nil, err
	}
	return pairs, nil
}

func permissionPairKey(pair PermissionPair) string {
	target := pair.Target
	return strings.Join([]string{
		pair.Profile, string(pair.Action), string(target.Scope), target.InstanceID,
		target.ProjectID.String(), string(target.ResourceKind), target.ResourceID.String(), fmt.Sprintf("%t", target.IncludeFuture),
	}, "\x00")
}

// PermissionPairAllows reports whether one persisted credential pair permits
// the requested exact action/target pair. Exact permissions never cross-expand.
// A typed future-resource selector matches only its declared Project and kind.
func PermissionPairAllows(granted, requested PermissionPair) bool {
	if granted.Validate() != nil || requested.Validate() != nil || granted.Profile != requested.Profile || granted.Action != requested.Action {
		return false
	}
	if granted.Target.Scope == PermissionScopeProject && granted.Target.IncludeFuture {
		return requested.Target.Scope == PermissionScopeResource &&
			granted.Target.ProjectID == requested.Target.ProjectID &&
			granted.Target.ResourceKind == requested.Target.ResourceKind
	}
	return permissionPairKey(granted) == permissionPairKey(requested)
}

// IntersectPermissionPairs applies a credential ceiling to effective
// principal authority while preserving requested pair identity and order.
func IntersectPermissionPairs(credential, effective []PermissionPair) []PermissionPair {
	if credential == nil || len(effective) == 0 {
		return []PermissionPair{}
	}
	result := make([]PermissionPair, 0, len(effective))
	for _, candidate := range effective {
		for _, ceiling := range credential {
			if PermissionPairAllows(ceiling, candidate) {
				result = append(result, candidate)
				break
			}
		}
	}
	return result
}

// RequiredPermissionPairs returns the requested pair followed by every
// declared prerequisite bound to the same exact target. It does not grant the
// prerequisite: callers must independently prove each returned pair against
// both principal and credential authority. Catalog validation guarantees the
// prerequisite graph is acyclic.
func RequiredPermissionPairs(requested PermissionPair) ([]PermissionPair, error) {
	if err := requested.Validate(); err != nil {
		return nil, err
	}
	result := make([]PermissionPair, 0, 2)
	seen := make(map[Action]struct{})
	var appendRequired func(PermissionPair) error
	appendRequired = func(pair PermissionPair) error {
		if _, exists := seen[pair.Action]; exists {
			return nil
		}
		seen[pair.Action] = struct{}{}
		result = append(result, pair)
		definition, _ := Permission(pair.Action)
		for _, action := range definition.Prerequisites {
			prerequisite := pair
			prerequisite.Action = action
			if err := prerequisite.Validate(); err != nil {
				return fmt.Errorf("%w: prerequisite %q cannot use target for %q: %v", ErrInvalidPermissionPair, action, pair.Action, err)
			}
			if err := appendRequired(prerequisite); err != nil {
				return err
			}
		}
		return nil
	}
	if err := appendRequired(requested); err != nil {
		return nil, err
	}
	return result, nil
}

// PermissionSetAllows requires the principal or credential set to cover the
// requested pair and every catalog prerequisite. It never inserts authority.
func PermissionSetAllows(granted []PermissionPair, requested PermissionPair) bool {
	required, err := RequiredPermissionPairs(requested)
	if err != nil {
		return false
	}
	for _, requirement := range required {
		allowed := false
		for _, permission := range granted {
			if PermissionPairAllows(permission, requirement) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
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
