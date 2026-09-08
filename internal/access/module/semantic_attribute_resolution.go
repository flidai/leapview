package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// ResolveSemanticAttributes resolves only the authenticated principal's
// durable direct/group attributes. Authentication is taken from the existing
// principal context; no caller-provided subject or raw provider claims are
// accepted by this capability.
func (m *Module) ResolveSemanticAttributes(ctx context.Context) (access.SemanticAttributeResolution, error) {
	if ctx == nil {
		return access.SemanticAttributeResolution{}, ErrUnauthorized
	}
	principal, ok := PrincipalFromContext(ctx)
	if !ok || strings.TrimSpace(principal.ID) == "" || principal.ID != strings.TrimSpace(principal.ID) {
		return access.SemanticAttributeResolution{}, ErrUnauthorized
	}
	if principal.DevBypass {
		return access.SemanticAttributeResolution{}, ErrForbidden
	}
	// Publication and group identities are not authenticated principals for
	// semantic attribute resolution. Service principals remain valid subjects
	// for API and workload consumers.
	if principal.Kind != access.PrincipalKindUser && principal.Kind != access.PrincipalKindServicePrincipal {
		return access.SemanticAttributeResolution{}, ErrForbidden
	}

	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, principal.ID)
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
	for _, resolvedSubject := range resolution.Subjects {
		if resolvedSubject.Kind == access.SubjectKindPrincipal && resolvedSubject != subject {
			return access.SemanticAttributeResolution{}, fmt.Errorf("semantic attribute resolution principal closure mismatch: expected %s, got %s", subject.ID, resolvedSubject.ID)
		}
	}
	return resolution, nil
}
