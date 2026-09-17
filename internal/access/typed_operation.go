package access

import (
	"errors"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// TypedOperationResolver names a product-owned resolver for an operation's
// exact authorization target. The resolver name is transport-neutral metadata
// from APIGen; domain adapters resolve the actual ResourceRef values.
type TypedOperationResolver string

const (
	TypedOperationResolverDashboard     TypedOperationResolver = "dashboard"
	TypedOperationResolverSemanticModel TypedOperationResolver = "semantic-model"
	TypedOperationResolverConnection    TypedOperationResolver = "connection"
	TypedOperationResolverSource        TypedOperationResolver = "source"
	TypedOperationResolverModel         TypedOperationResolver = "model"
	TypedOperationResolverProject       TypedOperationResolver = "project"
	TypedOperationResolverPipeline      TypedOperationResolver = "pipeline"
	TypedOperationResolverDelivery      TypedOperationResolver = "delivery"
	TypedOperationResolverInstance      TypedOperationResolver = "instance"
)

var (
	ErrTypedOperationActionRequired   = errors.New("typed operation action is required")
	ErrTypedOperationResolverRequired = errors.New("typed operation resolver is required")
	ErrUnknownTypedOperationResolver  = errors.New("unknown typed operation resolver")
	ErrTypedOperationTargetRequired   = errors.New("typed operation target is required")
)

// TypedOperationRequirement is the validated action/resolver contract shared
// by API, browser, and durable job adapters. Action meanings remain owned by
// the product permission catalog; resolver names identify the domain target
// family and never infer a target on their own.
type TypedOperationRequirement struct {
	Action   Action
	Resolver TypedOperationResolver
}

// TypedOperationRequirementService validates generated action/resolver
// metadata and resolves exact permission pairs after a domain adapter has
// resolved its concrete ResourceRefs. Keeping this tiny service in the
// product access package lets API and refresh adapters use the same contract.
type TypedOperationRequirementService struct{}

// NewTypedOperationRequirementService returns the shared product typed
// operation requirement service.
func NewTypedOperationRequirementService() TypedOperationRequirementService {
	return TypedOperationRequirementService{}
}

// New validates one generated action/resolver pair against the product
// catalog and resolver registry. Unknown actions and resolver names fail
// closed; callers must not silently fall back to a legacy capability.
func (TypedOperationRequirementService) New(action Action, resolver string) (TypedOperationRequirement, error) {
	if strings.TrimSpace(string(action)) == "" {
		return TypedOperationRequirement{}, ErrTypedOperationActionRequired
	}
	if _, ok := Permission(action); !ok {
		return TypedOperationRequirement{}, fmt.Errorf("%w: %q", ErrUnknownPermissionAction, action)
	}
	resolverValue := strings.TrimSpace(resolver)
	if resolverValue == "" {
		return TypedOperationRequirement{}, ErrTypedOperationResolverRequired
	}
	if resolverValue != resolver {
		return TypedOperationRequirement{}, fmt.Errorf("%w: resolver %q is not canonical", ErrUnknownTypedOperationResolver, resolver)
	}
	resolverKind, ok := typedOperationResolverKinds[TypedOperationResolver(resolverValue)]
	if !ok && TypedOperationResolver(resolverValue) != TypedOperationResolverInstance {
		return TypedOperationRequirement{}, fmt.Errorf("%w: %q", ErrUnknownTypedOperationResolver, resolver)
	}
	if TypedOperationResolver(resolverValue) == TypedOperationResolverInstance {
		definition, _ := Permission(action)
		if definition.Scope != PermissionScopeInstance {
			return TypedOperationRequirement{}, fmt.Errorf("typed operation action %q with resolver %q: %w", action, resolver, ErrInvalidPermissionCatalog)
		}
	} else if err := ValidateActionForKind(action, resolverKind); err != nil {
		return TypedOperationRequirement{}, fmt.Errorf("typed operation action %q with resolver %q: %w", action, resolver, err)
	}
	return TypedOperationRequirement{Action: action, Resolver: TypedOperationResolver(resolverValue)}, nil
}

// Requirement is a convenience alias for New at the service boundary.
func (service TypedOperationRequirementService) Requirement(action Action, resolver string) (TypedOperationRequirement, error) {
	return service.New(action, resolver)
}

// ResolvePairs validates every domain-resolved target against this operation
// requirement and returns the exact requested pairs plus any catalog-defined
// prerequisite pairs. Dependencies are bound to the same exact target and
// are never inferred as grants.
func (requirement TypedOperationRequirement) ResolvePairs(projectID projectgraph.ResourceID, resources ...ResourceRef) ([]PermissionPair, error) {
	if err := projectID.Validate(); err != nil {
		return nil, err
	}
	definition, ok := Permission(requirement.Action)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPermissionAction, requirement.Action)
	}
	if definition.Scope == PermissionScopeInstance {
		return nil, fmt.Errorf("typed instance operation %q requires an instance identity", requirement.Action)
	}
	// Project-scoped actions are checked against the serving Project namespace,
	// even when their resource family is a child object (for example
	// connection.create). Never encode these as resource-scoped pairs: doing
	// so would make the pair structurally invalid and could accidentally widen
	// an attenuation to a child object.
	if definition.Scope == PermissionScopeProject {
		if len(resources) == 0 {
			resources = []ResourceRef{projectResourceRef(projectID)}
		}
		for _, resource := range resources {
			if err := requirement.ValidateResource(resource); err != nil {
				return nil, err
			}
			if resource.Kind() != projectgraph.KindProjectNamespace || resource.ID() != projectID {
				return nil, fmt.Errorf("typed project operation target %q is not the requested project %q", resource.ID(), projectID)
			}
		}
		pair, err := NewProjectPermissionPair(requirement.Action, projectID)
		if err != nil {
			return nil, err
		}
		return RequiredPermissionPairs(pair)
	}
	if len(resources) == 0 {
		return nil, ErrTypedOperationTargetRequired
	}
	result := make([]PermissionPair, 0, len(resources))
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		if err := requirement.ValidateResource(resource); err != nil {
			return nil, err
		}
		pair, err := NewExactPermissionPair(requirement.Action, projectID, resource)
		if err != nil {
			return nil, err
		}
		required, err := RequiredPermissionPairs(pair)
		if err != nil {
			return nil, err
		}
		for _, dependency := range required {
			key := permissionPairKey(dependency)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, dependency)
		}
	}
	return result, nil
}

// ResolveInstancePairs creates the instance-audience pair for platform
// administration operations. Instance actions intentionally do not accept a
// graph ResourceRef or a project ID; callers must supply the target instance
// identity resolved by the application boundary.
func (requirement TypedOperationRequirement) ResolveInstancePairs(instanceID string) ([]PermissionPair, error) {
	if requirement.Resolver != TypedOperationResolverInstance {
		return nil, fmt.Errorf("typed operation resolver %q is not instance", requirement.Resolver)
	}
	definition, ok := Permission(requirement.Action)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPermissionAction, requirement.Action)
	}
	if definition.Scope != PermissionScopeInstance {
		return nil, fmt.Errorf("typed operation action %q is not instance-scoped", requirement.Action)
	}
	pair, err := NewInstancePermissionPair(requirement.Action, strings.TrimSpace(instanceID))
	if err != nil {
		return nil, err
	}
	return RequiredPermissionPairs(pair)
}

func projectResourceRef(projectID projectgraph.ResourceID) ResourceRef {
	return ResourceRef{id: projectID, kind: projectgraph.KindProjectNamespace}
}

// PermissionPair resolves exactly one domain target for this requirement.
func (requirement TypedOperationRequirement) PermissionPair(projectID projectgraph.ResourceID, resource ResourceRef) (PermissionPair, error) {
	pairs, err := requirement.ResolvePairs(projectID, resource)
	if err != nil {
		return PermissionPair{}, err
	}
	return pairs[0], nil
}

// ValidateResource proves that the domain adapter returned the resource kind
// named by the operation resolver and that the catalog action can be checked
// against that exact kind.
func (requirement TypedOperationRequirement) ValidateResource(resource ResourceRef) error {
	if err := resource.Validate(); err != nil {
		return err
	}
	wantKind, ok := typedOperationResolverKinds[requirement.Resolver]
	if !ok {
		if requirement.Resolver == TypedOperationResolverInstance {
			return fmt.Errorf("typed instance resolver does not accept graph resources")
		}
		return fmt.Errorf("%w: %q", ErrUnknownTypedOperationResolver, requirement.Resolver)
	}
	if resource.Kind() != wantKind {
		return fmt.Errorf("typed operation resolver %q returned resource kind %q, want %q", requirement.Resolver, resource.Kind(), wantKind)
	}
	return ValidateActionForKind(requirement.Action, resource.Kind())
}

var typedOperationResolverKinds = map[TypedOperationResolver]projectgraph.Kind{
	TypedOperationResolverDashboard:     projectgraph.KindDashboard,
	TypedOperationResolverSemanticModel: projectgraph.KindSemanticModel,
	TypedOperationResolverConnection:    projectgraph.KindConnection,
	TypedOperationResolverSource:        projectgraph.KindSource,
	TypedOperationResolverModel:         projectgraph.KindModel,
	TypedOperationResolverProject:       projectgraph.KindProjectNamespace,
	TypedOperationResolverPipeline:      projectgraph.KindPipeline,
	// Delivery is a project-scoped control-plane family. Its actions are
	// validated against the Project namespace, not an invented graph kind.
	TypedOperationResolverDelivery: projectgraph.KindProjectNamespace,
}
