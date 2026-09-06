package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

type semanticAttributeRegistryReader interface {
	SemanticAttributeRegistry(context.Context) (access.SemanticAttributeRegistrySnapshot, error)
}

// ResolveSemanticAttributes resolves the authenticated principal's durable
// direct/group attributes. Authentication comes only from the existing
// principal context; development bypass principals are never accepted as
// semantic attribute subjects.
func (m *Module) ResolveSemanticAttributes(ctx context.Context) (access.SemanticAttributeResolution, error) {
	if ctx == nil {
		return access.SemanticAttributeResolution{}, ErrUnauthorized
	}
	principal, ok := PrincipalFromContext(ctx)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return access.SemanticAttributeResolution{}, ErrUnauthorized
	}
	if principal.DevBypass {
		return access.SemanticAttributeResolution{}, ErrForbidden
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, strings.TrimSpace(principal.ID))
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	repository := m.repositoryValue()
	if repository == nil {
		return access.SemanticAttributeResolution{}, errors.New("access repository is unavailable")
	}
	reader, ok := repository.(access.SemanticAttributeResolutionReader)
	if !ok {
		return access.SemanticAttributeResolution{}, fmt.Errorf("access repository does not support semantic attribute resolution")
	}
	resolution, err := reader.ResolveSemanticAttributes(ctx, subject)
	if err != nil {
		return access.SemanticAttributeResolution{}, err
	}
	if resolution.Subject != subject {
		return access.SemanticAttributeResolution{}, fmt.Errorf("semantic attribute resolution subject mismatch: expected %s, got %s", subject.ID, resolution.Subject.ID)
	}
	return resolution, nil
}

// SemanticAttributeRegistry returns the repository's complete registry
// snapshot for activation. It is intentionally a getter on the module and
// does not expose the repository or its mutation interfaces.
func (m *Module) SemanticAttributeRegistry(ctx context.Context) (access.SemanticAttributeRegistrySnapshot, error) {
	repository := m.repositoryValue()
	if repository == nil {
		return access.SemanticAttributeRegistrySnapshot{}, errors.New("access repository is unavailable")
	}
	reader, ok := repository.(semanticAttributeRegistryReader)
	if !ok {
		return access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("access repository does not support semantic attribute registry reads")
	}
	return reader.SemanticAttributeRegistry(ctx)
}
