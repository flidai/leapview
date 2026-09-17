package permissions

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/flidai/leapview/pkg/strictjson"
)

var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// PairSet names a set of paired permissions without introducing another
// runtime representation. The alias keeps []Pair and PairSet interchangeable
// for adapters.
type PairSet = []Pair

// ValidateShape checks target structure without consulting an action catalog.
// It is useful at generic authority and persistence boundaries; action scope
// and allowed-kind checks require CompiledCatalog.ValidatePair.
func (target Target) ValidateShape() error {
	switch target.Scope {
	case ScopeInstance:
		if !validTargetIdentity(target.InstanceID) || target.ProjectID != "" || target.ResourceKind != "" || target.ResourceID != "" || target.IncludeFuture {
			return fmt.Errorf("%w: invalid instance target", ErrInvalidPair)
		}
	case ScopeProject:
		if !validResourceID(target.ProjectID) || target.InstanceID != "" || target.ResourceID != "" {
			return fmt.Errorf("%w: invalid Project target", ErrInvalidPair)
		}
		if target.ResourceKind != "" && !target.ResourceKind.Valid() {
			return fmt.Errorf("%w: invalid resource kind %q", ErrInvalidPair, target.ResourceKind)
		}
		if target.IncludeFuture != (target.ResourceKind != "") {
			return fmt.Errorf("%w: future Project target requires exactly one kind and includeFuture", ErrInvalidPair)
		}
	case ScopeResource:
		if !validResourceID(target.ProjectID) || !target.ResourceKind.Valid() || !validResourceID(target.ResourceID) || target.InstanceID != "" || target.IncludeFuture {
			return fmt.Errorf("%w: invalid exact resource target", ErrInvalidPair)
		}
	default:
		return fmt.Errorf("%w: target scope %q is unsupported", ErrInvalidPair, target.Scope)
	}
	return nil
}

// ValidateShape checks a pair's generic wire shape. It does not require that
// the action exists in any particular catalog.
func (pair Pair) ValidateShape() error {
	if !validProfile(pair.Profile) {
		return fmt.Errorf("%w: profile is required and must be canonical", ErrInvalidPair)
	}
	if !pair.Action.Valid() {
		return fmt.Errorf("%w: action %q is not canonical", ErrInvalidPair, pair.Action)
	}
	return pair.Target.ValidateShape()
}

// ValidatePairSetShape validates pair shape and duplicate identity. Nil is
// omitted authority and rejected; a non-nil empty set is explicit deny-all.
func ValidatePairSetShape(pairs PairSet) error {
	if pairs == nil {
		return ErrPermissionPairsRequired
	}
	seen := make(map[string]struct{}, len(pairs))
	for index, pair := range pairs {
		if err := pair.ValidateShape(); err != nil {
			return fmt.Errorf("permission %d: %w", index, err)
		}
		key := pairKey(pair)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate permission %q", ErrInvalidPair, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// NewExactPair constructs an exact pair with an explicitly supplied profile.
// It is shape-only: callers should use a compiled catalog to validate action
// scope and resource kind before persisting or evaluating the result.
func NewExactPair(profile string, action Action, projectID string, kind Kind, resourceID string) (Pair, error) {
	pair := Pair{Action: action, Profile: profile, Target: Target{
		Scope: ScopeResource, ProjectID: projectID, ResourceKind: kind, ResourceID: resourceID,
	}}
	if err := pair.ValidateShape(); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

// NewProjectPair constructs a Project-scoped pair with an explicit profile.
func NewProjectPair(profile string, action Action, projectID string) (Pair, error) {
	pair := Pair{Action: action, Profile: profile, Target: Target{Scope: ScopeProject, ProjectID: projectID}}
	if err := pair.ValidateShape(); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

// NewFuturePair constructs an explicit typed future-resource selector.
func NewFuturePair(profile string, action Action, projectID string, kind Kind) (Pair, error) {
	pair := Pair{Action: action, Profile: profile, Target: Target{
		Scope: ScopeProject, ProjectID: projectID, ResourceKind: kind, IncludeFuture: true,
	}}
	if err := pair.ValidateShape(); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

// NewInstancePair constructs an instance-audience pair with an explicit
// profile.
func NewInstancePair(profile string, action Action, instanceID string) (Pair, error) {
	pair := Pair{Action: action, Profile: profile, Target: Target{Scope: ScopeInstance, InstanceID: instanceID}}
	if err := pair.ValidateShape(); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

// NewExactPair constructs an exact pair pinned to catalog.Profile().
func (catalog *CompiledCatalog) NewExactPair(action Action, projectID string, kind Kind, resourceID string) (Pair, error) {
	pair, err := NewExactPair(catalogProfile(catalog), action, projectID, kind, resourceID)
	if err != nil {
		return Pair{}, err
	}
	if err := catalog.ValidatePair(pair); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

// NewProjectPair constructs a Project-scoped pair pinned to the catalog.
func (catalog *CompiledCatalog) NewProjectPair(action Action, projectID string) (Pair, error) {
	pair, err := NewProjectPair(catalogProfile(catalog), action, projectID)
	if err != nil {
		return Pair{}, err
	}
	if err := catalog.ValidatePair(pair); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

// NewFuturePair constructs a future-resource pair pinned to the catalog.
func (catalog *CompiledCatalog) NewFuturePair(action Action, projectID string, kind Kind) (Pair, error) {
	pair, err := NewFuturePair(catalogProfile(catalog), action, projectID, kind)
	if err != nil {
		return Pair{}, err
	}
	if err := catalog.ValidatePair(pair); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

// NewInstancePair constructs an instance pair pinned to the catalog.
func (catalog *CompiledCatalog) NewInstancePair(action Action, instanceID string) (Pair, error) {
	pair, err := NewInstancePair(catalogProfile(catalog), action, instanceID)
	if err != nil {
		return Pair{}, err
	}
	if err := catalog.ValidatePair(pair); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

func catalogProfile(catalog *CompiledCatalog) string {
	if catalog == nil {
		return ""
	}
	return catalog.profile
}

// ValidatePair validates a pair's shape, profile and action/target semantics
// against this catalog.
func (catalog *CompiledCatalog) ValidatePair(pair Pair) error {
	if catalog == nil {
		return fmt.Errorf("%w: catalog is nil", ErrInvalidCatalog)
	}
	if err := pair.ValidateShape(); err != nil {
		return err
	}
	if pair.Profile != catalog.profile {
		return fmt.Errorf("%w: unsupported profile %q", ErrInvalidPair, pair.Profile)
	}
	definition, ok := catalog.Definition(pair.Action)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownAction, pair.Action)
	}
	target := pair.Target
	switch target.Scope {
	case ScopeInstance:
		if definition.Scope != ScopeInstance {
			return fmt.Errorf("%w: action %q has invalid instance target", ErrInvalidPair, pair.Action)
		}
	case ScopeResource:
		if definition.Scope != ScopeResource || !kindAllowed(definition.ResourceKinds, target.ResourceKind) {
			return fmt.Errorf("%w: action %q has invalid exact resource target", ErrInvalidPair, pair.Action)
		}
	case ScopeProject:
		if definition.Scope == ScopeProject {
			if target.ResourceKind != "" || target.IncludeFuture {
				return fmt.Errorf("%w: Project-scoped action %q cannot carry a resource wildcard", ErrInvalidPair, pair.Action)
			}
		} else if definition.Scope == ScopeResource {
			if !target.IncludeFuture || !kindAllowed(definition.ResourceKinds, target.ResourceKind) {
				return fmt.Errorf("%w: resource action %q requires an explicit typed future-resource selector", ErrInvalidPair, pair.Action)
			}
		} else {
			return fmt.Errorf("%w: instance action %q cannot target a Project", ErrInvalidPair, pair.Action)
		}
	}
	return nil
}

// ValidatePairs validates all pairs against this catalog.
func (catalog *CompiledCatalog) ValidatePairs(pairs PairSet) error {
	if catalog == nil {
		return fmt.Errorf("%w: catalog is nil", ErrInvalidCatalog)
	}
	if pairs == nil {
		return ErrPermissionPairsRequired
	}
	seen := make(map[string]struct{}, len(pairs))
	for index, pair := range pairs {
		if err := catalog.ValidatePair(pair); err != nil {
			return fmt.Errorf("permission %d: %w", index, err)
		}
		key := pairKey(pair)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate permission %q", ErrInvalidPair, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validTargetIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 255 && !strings.ContainsAny(value, "\x00\r\n\t")
}

func validResourceID(value string) bool {
	return validTargetIdentity(value) && resourceIDPattern.MatchString(value)
}

func pairKey(pair Pair) string {
	target := pair.Target
	return strings.Join([]string{
		pair.Profile, string(pair.Action), string(target.Scope), target.InstanceID,
		target.ProjectID, string(target.ResourceKind), target.ResourceID, fmt.Sprintf("%t", target.IncludeFuture),
	}, "\x00")
}

// Key returns a stable diagnostic/set key. It is not a persisted identifier.
func (pair Pair) Key() string { return pairKey(pair) }

func kindAllowed(kinds []Kind, candidate Kind) bool {
	for _, kind := range kinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

// Allows reports whether granted permits requested. Exact pairs only match
// the same exact pair. A future selector matches an exact resource of the
// declared kind in the declared Project and never another future selector.
func (catalog *CompiledCatalog) Allows(granted, requested Pair) bool {
	if catalog == nil || catalog.ValidatePair(granted) != nil || catalog.ValidatePair(requested) != nil || granted.Profile != requested.Profile || granted.Action != requested.Action {
		return false
	}
	if granted.Target.Scope == ScopeProject && granted.Target.IncludeFuture {
		return requested.Target.Scope == ScopeResource && granted.Target.ProjectID == requested.Target.ProjectID && granted.Target.ResourceKind == requested.Target.ResourceKind
	}
	return pairKey(granted) == pairKey(requested)
}

// Intersect retains effective pairs covered by a credential ceiling, in
// effective order. Pairing is preserved and no Cartesian product is formed.
func (catalog *CompiledCatalog) Intersect(ceiling, effective PairSet) PairSet {
	if catalog == nil || ceiling == nil || len(effective) == 0 {
		return PairSet{}
	}
	result := make(PairSet, 0, len(effective))
	for _, candidate := range effective {
		for _, grant := range ceiling {
			if catalog.Allows(grant, candidate) {
				result = append(result, candidate)
				break
			}
		}
	}
	return result
}

// Required returns requested followed by its recursive prerequisite closure,
// all bound to the same target. Prerequisites are checks, not implied grants.
func (catalog *CompiledCatalog) Required(requested Pair) (PairSet, error) {
	if err := catalog.ValidatePair(requested); err != nil {
		return nil, err
	}
	result := make(PairSet, 0, 2)
	seen := make(map[Action]struct{})
	var appendRequired func(Pair) error
	appendRequired = func(pair Pair) error {
		if _, exists := seen[pair.Action]; exists {
			return nil
		}
		seen[pair.Action] = struct{}{}
		result = append(result, pair)
		definition, ok := catalog.Definition(pair.Action)
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownAction, pair.Action)
		}
		for _, action := range definition.Prerequisites {
			prerequisite := pair
			prerequisite.Action = action
			if err := catalog.ValidatePair(prerequisite); err != nil {
				return fmt.Errorf("%w: prerequisite %q cannot use target for %q: %v", ErrInvalidPair, action, pair.Action, err)
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

// Contains reports whether granted covers every requested pair and its
// prerequisite closure. A nil set is omitted authority; explicit empty is a
// valid set with no permissions.
func (catalog *CompiledCatalog) Contains(granted, requested PairSet) bool {
	if catalog == nil || granted == nil || requested == nil {
		return false
	}
	if catalog.ValidatePairs(granted) != nil || catalog.ValidatePairs(requested) != nil {
		return false
	}
	for _, pair := range requested {
		required, err := catalog.Required(pair)
		if err != nil {
			return false
		}
		for _, requirement := range required {
			allowed := false
			for _, grant := range granted {
				if catalog.Allows(grant, requirement) {
					allowed = true
					break
				}
			}
			if !allowed {
				return false
			}
		}
	}
	return true
}

// Encode emits the canonical strict JSON array for pairs.
func (catalog *CompiledCatalog) Encode(pairs PairSet) ([]byte, error) {
	if err := catalog.ValidatePairs(pairs); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(pairs)
	if err != nil {
		return nil, fmt.Errorf("encode permission pairs: %w", err)
	}
	return encoded, nil
}

// Decode accepts exactly one strict, bounded JSON array and validates its
// profile, actions, targets, duplicates and catalog semantics.
func (catalog *CompiledCatalog) Decode(encoded []byte) (PairSet, error) {
	var pairs PairSet
	if err := strictjson.Decode(encoded, &pairs); err != nil {
		return nil, fmt.Errorf("decode permission pairs: %w", err)
	}
	if err := catalog.ValidatePairs(pairs); err != nil {
		return nil, err
	}
	return pairs, nil
}
