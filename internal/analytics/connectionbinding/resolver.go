package connectionbinding

import (
	"context"
	"fmt"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"strings"
)

type ResolverKind string

const (
	ResolverInfisical   ResolverKind = "infisical"
	ResolverEnvironment ResolverKind = "environment"
)

type TargetClass string

const (
	TargetProduction  TargetClass = "production"
	TargetDevelopment TargetClass = "development"
)

type ResolverSelection struct {
	TargetID    TargetID
	ProjectID   projectgraph.ResourceID
	Environment string
	TargetClass TargetClass
	Kind        ResolverKind
}

type ResolverSelectionInput ResolverSelection

type CredentialResolver interface {
	Resolve(context.Context, CredentialReference) (CredentialSnapshot, error)
}

// VersionedCredentialResolver resolves the exact immutable provider version
// recorded in release evidence. Implementations must fail closed when the
// requested version cannot be proved to match the returned snapshot.
type VersionedCredentialResolver interface {
	ResolveVersion(context.Context, CredentialReference, string) (CredentialSnapshot, error)
}

type ResolverSet struct {
	Infisical   CredentialResolver
	Environment CredentialResolver
}

func SelectResolver(selection ResolverSelection, resolvers ResolverSet) (CredentialResolver, error) {
	validated, err := NewResolverSelection(ResolverSelectionInput(selection))
	if err != nil {
		return nil, err
	}
	switch validated.Kind {
	case ResolverInfisical:
		if resolvers.Infisical == nil {
			return nil, ErrProviderUnavailable
		}
		return resolvers.Infisical, nil
	case ResolverEnvironment:
		if resolvers.Environment == nil {
			return nil, ErrProviderUnavailable
		}
		return resolvers.Environment, nil
	default:
		return nil, fmt.Errorf("%w: exactly one authoritative resolver must be selected", ErrInvalidBinding)
	}
}

type Repository interface {
	Create(context.Context, TargetBinding) error
	Binding(context.Context, BindingScope, TargetID, projectgraph.ResourceID) (TargetBinding, error)
	Save(context.Context, TargetBinding, int64) (TargetBinding, error)
}

func NewResolverSelection(input ResolverSelectionInput) (ResolverSelection, error) {
	if _, err := ParseTargetID(input.TargetID.String()); err != nil {
		return ResolverSelection{}, err
	}
	if input.ProjectID.String() != strings.TrimSpace(input.ProjectID.String()) {
		return ResolverSelection{}, fmt.Errorf("%w: resolver project identity must be canonical", ErrInvalidBinding)
	}
	input.ProjectID = projectgraph.ResourceID(input.ProjectID.String())
	input.Environment = strings.TrimSpace(input.Environment)
	if !input.ProjectID.Valid() || !identifierPattern.MatchString(input.Environment) {
		return ResolverSelection{}, fmt.Errorf("%w: resolver target and environment are required", ErrInvalidBinding)
	}
	if input.TargetClass != TargetProduction && input.TargetClass != TargetDevelopment {
		return ResolverSelection{}, fmt.Errorf("%w: resolver target class must be explicit", ErrInvalidBinding)
	}
	switch input.Kind {
	case ResolverInfisical:
	case ResolverEnvironment:
		if input.TargetClass == TargetProduction {
			return ResolverSelection{}, fmt.Errorf("%w: environment resolver cannot be selected for a production target", ErrInvalidBinding)
		}
	default:
		return ResolverSelection{}, fmt.Errorf("%w: exactly one authoritative resolver must be selected", ErrInvalidBinding)
	}
	return ResolverSelection(input), nil
}
