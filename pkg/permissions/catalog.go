// Package permissions contains transport-neutral, versioned resource
// authorization mechanics. Product packages own the action vocabulary and
// inject a profile and definitions into a CompiledCatalog.
package permissions

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrInvalidCatalog          = errors.New("invalid permission catalog")
	ErrUnknownAction           = errors.New("unknown permission action")
	ErrInvalidPair             = errors.New("invalid permission action/resource pair")
	ErrPermissionPairsRequired = errors.New("permission pairs are required")
)

// Action is an opaque typed operation identity. Its canonical syntax is a
// lower-case, dot-separated name; its meaning comes only from a catalog.
type Action string

// Valid reports whether action has canonical wire syntax.
func (action Action) Valid() bool { return validActionName(action) }

// Scope identifies the audience of a pair.
type Scope string

const (
	ScopeInstance Scope = "instance"
	ScopeProject  Scope = "project"
	ScopeResource Scope = "resource"
)

// Kind is an opaque logical securable-resource kind. Product catalogs decide
// which kinds they support; this package validates only canonical syntax.
type Kind string

// Valid reports whether kind has canonical wire syntax.
func (kind Kind) Valid() bool {
	value := string(kind)
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n\t") {
		return false
	}
	first := value[0]
	if !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	for _, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

// Target is the audience and resource half of a typed permission pair.
// String IDs keep identity ownership in the caller's domain while retaining
// the exact v1 wire field names.
type Target struct {
	Scope         Scope  `json:"scope"`
	InstanceID    string `json:"instanceId,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	ResourceKind  Kind   `json:"resourceKind,omitempty"`
	ResourceID    string `json:"resourceId,omitempty"`
	IncludeFuture bool   `json:"includeFuture,omitempty"`
}

// Pair preserves the action/target relationship through consent, storage and
// evaluation. Splitting these fields into independent lists creates a
// Cartesian product of authority.
type Pair struct {
	Action  Action `json:"action"`
	Target  Target `json:"target"`
	Profile string `json:"profile"`
}

// Definition describes the structural part of one action. Presentation and
// delegation policy remain owned by the product catalog and need not be
// duplicated in this mechanics package.
type Definition struct {
	Action        Action   `json:"action"`
	Scope         Scope    `json:"scope"`
	ResourceKinds []Kind   `json:"resourceKinds,omitempty"`
	CheckKinds    []Kind   `json:"checkKinds,omitempty"`
	Prerequisites []Action `json:"prerequisites,omitempty"`
}

// CompiledCatalog is immutable after construction. It owns defensive copies
// of definitions and a private action lookup table.
type CompiledCatalog struct {
	profile string
	defs    []Definition
	byName  map[Action]Definition
}

func cloneDefinition(definition Definition) Definition {
	definition.ResourceKinds = append([]Kind(nil), definition.ResourceKinds...)
	definition.CheckKinds = append([]Kind(nil), definition.CheckKinds...)
	definition.Prerequisites = append([]Action(nil), definition.Prerequisites...)
	return definition
}

// CompileCatalog validates and compiles an explicit profile and definition
// set. Neither input is retained by reference.
func CompileCatalog(profile string, definitions []Definition) (*CompiledCatalog, error) {
	if !validProfile(profile) {
		return nil, fmt.Errorf("%w: profile is required and must be canonical", ErrInvalidCatalog)
	}
	if err := ValidateCatalog(definitions); err != nil {
		return nil, err
	}
	catalog := &CompiledCatalog{
		profile: profile,
		defs:    make([]Definition, len(definitions)),
		byName:  make(map[Action]Definition, len(definitions)),
	}
	for index, definition := range definitions {
		definition = cloneDefinition(definition)
		catalog.defs[index] = definition
		catalog.byName[definition.Action] = definition
	}
	return catalog, nil
}

// Profile returns the profile pinned by this catalog.
func (catalog *CompiledCatalog) Profile() string {
	if catalog == nil {
		return ""
	}
	return catalog.profile
}

// Validate re-checks the immutable catalog contract. It is primarily useful
// for defensive assertions at package boundaries and tests.
func (catalog *CompiledCatalog) Validate() error {
	if catalog == nil {
		return fmt.Errorf("%w: catalog is nil", ErrInvalidCatalog)
	}
	if !validProfile(catalog.profile) {
		return fmt.Errorf("%w: profile is required and must be canonical", ErrInvalidCatalog)
	}
	return ValidateCatalog(catalog.defs)
}

// Definitions returns a defensive copy in supplied order.
func (catalog *CompiledCatalog) Definitions() []Definition {
	if catalog == nil {
		return nil
	}
	result := make([]Definition, len(catalog.defs))
	for index, definition := range catalog.defs {
		result[index] = cloneDefinition(definition)
	}
	return result
}

// Definition looks up an action and returns a defensive copy.
func (catalog *CompiledCatalog) Definition(action Action) (Definition, bool) {
	if catalog == nil {
		return Definition{}, false
	}
	definition, ok := catalog.byName[action]
	if !ok {
		return Definition{}, false
	}
	return cloneDefinition(definition), true
}

// ValidateActionForKind checks that a catalog action is evaluated on one of
// its declared check kinds. Instance actions intentionally have no graph kind
// and therefore cannot be checked through this method.
func (catalog *CompiledCatalog) ValidateActionForKind(action Action, kind Kind) error {
	if catalog == nil {
		return fmt.Errorf("%w: catalog is nil", ErrInvalidCatalog)
	}
	definition, ok := catalog.Definition(action)
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownAction, action)
	}
	if definition.Scope == ScopeInstance {
		return fmt.Errorf("%w: instance action %q has no graph resource kind", ErrInvalidCatalog, action)
	}
	for _, allowed := range definition.CheckKinds {
		if allowed == kind {
			return nil
		}
	}
	return fmt.Errorf("%w: action %q is not valid for check kind %q", ErrInvalidCatalog, action, kind)
}

// ValidateCatalog checks action syntax, duplicate actions, scope/kind shape,
// prerequisite references, duplicate prerequisites, and cycles. A
// prerequisite must be satisfiable using the parent's target.
func ValidateCatalog(definitions []Definition) error {
	if len(definitions) == 0 {
		return fmt.Errorf("%w: catalog is empty", ErrInvalidCatalog)
	}
	byAction := make(map[Action]Definition, len(definitions))
	for index, definition := range definitions {
		if !definition.Action.Valid() {
			return fmt.Errorf("%w: definition %d has invalid action %q", ErrInvalidCatalog, index, definition.Action)
		}
		if _, exists := byAction[definition.Action]; exists {
			return fmt.Errorf("%w: duplicate action %q", ErrInvalidCatalog, definition.Action)
		}
		switch definition.Scope {
		case ScopeInstance:
			if len(definition.ResourceKinds) != 0 || len(definition.CheckKinds) != 0 {
				return fmt.Errorf("%w: instance action %q has graph kinds", ErrInvalidCatalog, definition.Action)
			}
		case ScopeProject, ScopeResource:
			if len(definition.ResourceKinds) == 0 || len(definition.CheckKinds) == 0 {
				return fmt.Errorf("%w: action %q lacks resource/check kinds", ErrInvalidCatalog, definition.Action)
			}
		default:
			return fmt.Errorf("%w: action %q has scope %q", ErrInvalidCatalog, definition.Action, definition.Scope)
		}
		if err := validateKinds(definition.Action, "resource", definition.ResourceKinds); err != nil {
			return err
		}
		if err := validateKinds(definition.Action, "check", definition.CheckKinds); err != nil {
			return err
		}
		byAction[definition.Action] = definition
	}
	for _, definition := range definitions {
		seen := make(map[Action]struct{}, len(definition.Prerequisites))
		for _, prerequisite := range definition.Prerequisites {
			if !prerequisite.Valid() {
				return fmt.Errorf("%w: action %q has invalid prerequisite %q", ErrInvalidCatalog, definition.Action, prerequisite)
			}
			if prerequisite == definition.Action {
				return fmt.Errorf("%w: action %q requires itself", ErrInvalidCatalog, definition.Action)
			}
			if _, duplicate := seen[prerequisite]; duplicate {
				return fmt.Errorf("%w: action %q repeats prerequisite %q", ErrInvalidCatalog, definition.Action, prerequisite)
			}
			seen[prerequisite] = struct{}{}
			dependency, exists := byAction[prerequisite]
			if !exists {
				return fmt.Errorf("%w: action %q has unknown prerequisite %q", ErrInvalidCatalog, definition.Action, prerequisite)
			}
			if !prerequisiteCanShareTarget(definition, dependency) {
				return fmt.Errorf("%w: action %q has unsatisfied prerequisite %q", ErrInvalidCatalog, definition.Action, prerequisite)
			}
		}
	}
	if cycle := catalogCycle(byAction); len(cycle) > 0 {
		return fmt.Errorf("%w: prerequisite cycle %s", ErrInvalidCatalog, strings.Join(cycle, " -> "))
	}
	return nil
}

func prerequisiteCanShareTarget(parent, dependency Definition) bool {
	if parent.Scope != dependency.Scope {
		return false
	}
	// Project-scoped actions all evaluate against the same Project target;
	// ResourceKinds describe the resource affected by creation/actions rather
	// than a field carried by that target.
	if parent.Scope == ScopeInstance || parent.Scope == ScopeProject {
		return true
	}
	for _, parentKind := range parent.ResourceKinds {
		for _, dependencyKind := range dependency.ResourceKinds {
			if parentKind == dependencyKind {
				return true
			}
		}
	}
	return false
}

func validActionName(action Action) bool {
	value := string(action)
	if value == "" || value != strings.ToLower(value) || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || !strings.Contains(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validProfile(profile string) bool {
	return profile != "" && profile == strings.TrimSpace(profile) && len(profile) <= 255 && !strings.ContainsAny(profile, "\x00\r\n\t")
}

func validateKinds(action Action, label string, kinds []Kind) error {
	seen := make(map[Kind]struct{}, len(kinds))
	for _, kind := range kinds {
		if !kind.Valid() {
			return fmt.Errorf("%w: action %q has invalid %s kind %q", ErrInvalidCatalog, action, label, kind)
		}
		if _, duplicate := seen[kind]; duplicate {
			return fmt.Errorf("%w: action %q repeats %s kind %q", ErrInvalidCatalog, action, label, kind)
		}
		seen[kind] = struct{}{}
	}
	return nil
}

func catalogCycle(definitions map[Action]Definition) []string {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[Action]int, len(definitions))
	stack := make([]Action, 0, len(definitions))
	actions := make([]string, 0, len(definitions))
	for action := range definitions {
		actions = append(actions, string(action))
	}
	sort.Strings(actions)
	var visit func(Action) []string
	visit = func(action Action) []string {
		if state[action] == visiting {
			start := 0
			for index, candidate := range stack {
				if candidate == action {
					start = index
					break
				}
			}
			cycle := make([]string, 0, len(stack)-start+1)
			for _, candidate := range stack[start:] {
				cycle = append(cycle, string(candidate))
			}
			return append(cycle, string(action))
		}
		if state[action] == visited {
			return nil
		}
		state[action] = visiting
		stack = append(stack, action)
		for _, prerequisite := range definitions[action].Prerequisites {
			if cycle := visit(prerequisite); len(cycle) > 0 {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		state[action] = visited
		return nil
	}
	for _, raw := range actions {
		if cycle := visit(Action(raw)); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}
