package module

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// AdminReader is the read-only global identity directory surface. Project
// authorization and role listings are intentionally not part of this reader.
type AdminReader struct {
	repository access.Repository
}

func (m *Module) AdminReader() *AdminReader {
	if m == nil {
		return nil
	}
	repository := m.repositoryValue()
	if repository == nil {
		return nil
	}
	return &AdminReader{repository: repository}
}

func (r *AdminReader) ListPrincipals(ctx context.Context, filter access.PrincipalFilter) ([]access.Principal, error) {
	if reader, ok := r.repository.(interface {
		ListPrincipalsWithActivity(context.Context, access.PrincipalFilter) ([]access.Principal, error)
	}); ok {
		return reader.ListPrincipalsWithActivity(ctx, filter)
	}
	return r.repository.ListPrincipals(ctx, filter)
}

func (r *AdminReader) ListAllGroups(ctx context.Context) ([]access.Group, error) {
	return r.repository.ListAllGroups(ctx)
}

func (r *AdminReader) ListGroupMembersByGroup(ctx context.Context, groupID string) ([]access.GroupMember, error) {
	return r.repository.ListGroupMembersByGroup(ctx, groupID)
}
