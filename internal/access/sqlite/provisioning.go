package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
)

func (r *Repository) BootstrapAdmin(ctx context.Context, workspaceID, email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	principal, err := r.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          access.PrincipalIDForEmail(email),
		Email:       email,
		DisplayName: email,
	})
	if err != nil {
		return err
	}
	role, err := r.q.GetRoleByName(ctx, access.RoleOwner)
	if err != nil {
		return err
	}
	bindingID := stableAccessID("rolebinding", workspaceID, principal.ID+"|"+access.RoleOwner)
	if err := r.deleteRoleBindingGrants(ctx, bindingID); err != nil {
		return err
	}
	if err := r.q.InsertRoleBinding(ctx, platformdb.InsertRoleBindingParams{
		ID:          bindingID,
		WorkspaceID: workspaceID,
		RoleID:      role.ID,
		PrincipalID: sql.NullString{String: principal.ID, Valid: principal.ID != ""},
	}); err != nil {
		return err
	}
	return r.syncRoleBindingGrants(ctx, bindingID, workspaceID, access.RoleOwner, access.SubjectPrincipal, principal.ID)
}

func (r *Repository) ResolveExternalPrincipal(ctx context.Context, input access.ExternalIdentityInput) (access.Principal, error) {
	access.ClearAuthorizationCache(ctx)
	input.Email = access.NormalizeEmail(input.Email)
	if input.Provider == "" || input.Subject == "" {
		return access.Principal{}, fmt.Errorf("external identity requires provider and subject")
	}
	identity, err := r.q.GetExternalIdentity(ctx, platformdb.GetExternalIdentityParams{
		Provider: input.Provider,
		TenantID: input.TenantID,
		Subject:  input.Subject,
	})
	if err == nil {
		principal, err := r.q.GetPrincipal(ctx, identity.PrincipalID)
		if err != nil {
			return access.Principal{}, err
		}
		if (principal.DisabledAt.Valid && principal.DisabledAt.String != "") || (principal.BlockedAt.Valid && principal.BlockedAt.String != "") {
			return access.Principal{}, sql.ErrNoRows
		}
		return r.UpsertPrincipal(ctx, access.PrincipalInput{
			ID:          identity.PrincipalID,
			Email:       input.Email,
			DisplayName: input.DisplayName,
		})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return access.Principal{}, err
	}

	var principal access.Principal
	if input.Email != "" {
		row, err := r.q.GetPrincipalByEmail(ctx, input.Email)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return access.Principal{}, err
		}
		if err == nil {
			principal = mapPrincipal(row)
			if principal.AccessDisabled() {
				return access.Principal{}, sql.ErrNoRows
			}
		}
	}
	if principal.ID == "" {
		principal, err = r.UpsertPrincipal(ctx, access.PrincipalInput{
			ID:          "external_" + stableID(input.Provider+"|"+input.TenantID+"|"+input.Subject),
			Email:       input.Email,
			DisplayName: input.DisplayName,
		})
		if err != nil {
			return access.Principal{}, err
		}
	} else {
		principal, err = r.UpsertPrincipal(ctx, access.PrincipalInput{
			ID:          principal.ID,
			Email:       input.Email,
			DisplayName: input.DisplayName,
		})
		if err != nil {
			return access.Principal{}, err
		}
	}

	if err := r.q.UpsertExternalIdentity(ctx, platformdb.UpsertExternalIdentityParams{
		ID:          "identity_" + stableID(input.Provider+"|"+input.TenantID+"|"+input.Subject),
		PrincipalID: principal.ID,
		Provider:    input.Provider,
		TenantID:    input.TenantID,
		Subject:     input.Subject,
		Email:       input.Email,
	}); err != nil {
		return access.Principal{}, err
	}
	return principal, nil
}

func (r *Repository) UpsertSCIMUser(ctx context.Context, input access.SCIMUserInput) (access.SCIMUser, error) {
	access.ClearAuthorizationCache(ctx)
	id := strings.TrimSpace(input.ID)
	existingSubject := ""
	if id == "" {
		id = "scim_user_" + stableID(firstNonEmpty(input.ExternalID, input.UserName, input.Email))
	} else if identity, err := r.q.GetExternalIdentityByPrincipalProvider(ctx, platformdb.GetExternalIdentityByPrincipalProviderParams{
		PrincipalID: id,
		Provider:    "scim",
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if _, principalErr := r.q.GetPrincipal(ctx, id); principalErr == nil {
				return access.SCIMUser{}, sql.ErrNoRows
			} else if principalErr != nil && !errors.Is(principalErr, sql.ErrNoRows) {
				return access.SCIMUser{}, principalErr
			}
		} else {
			return access.SCIMUser{}, err
		}
	} else {
		existingSubject = identity.Subject
	}
	subject := strings.TrimSpace(firstNonEmpty(input.ExternalID, existingSubject, input.ID, input.UserName, input.Email))
	if subject == "" {
		return access.SCIMUser{}, fmt.Errorf("scim user requires id, external id, userName, or email")
	}
	if id == "" {
		id = "scim_user_" + stableID(subject)
	}
	email := access.NormalizeEmail(firstNonEmpty(input.Email, input.UserName))
	displayName := strings.TrimSpace(firstNonEmpty(input.DisplayName, email, input.UserName, id))
	principal, err := r.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          id,
		Kind:        access.PrincipalKindUser,
		Email:       email,
		DisplayName: displayName,
	})
	if err != nil {
		return access.SCIMUser{}, err
	}
	if err := r.q.UpsertExternalIdentity(ctx, platformdb.UpsertExternalIdentityParams{
		ID:          "identity_" + stableID("scim||"+subject),
		PrincipalID: principal.ID,
		Provider:    "scim",
		TenantID:    "",
		Subject:     subject,
		Email:       email,
	}); err != nil {
		return access.SCIMUser{}, err
	}
	if !input.Active {
		return r.DisableSCIMUser(ctx, principal.ID)
	}
	if err := r.q.EnableProvisionedPrincipal(ctx, principal.ID); err != nil {
		return access.SCIMUser{}, err
	}
	row, err := r.q.GetPrincipal(ctx, principal.ID)
	if err != nil {
		return access.SCIMUser{}, err
	}
	return access.SCIMUser{Principal: mapPrincipal(row), ExternalID: subject}, nil
}

func (r *Repository) ListSCIMUsers(ctx context.Context, filter access.SCIMUserFilter) ([]access.SCIMUser, error) {
	if strings.TrimSpace(filter.ID) != "" {
		row, err := r.q.GetPrincipal(ctx, strings.TrimSpace(filter.ID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return []access.SCIMUser{}, nil
			}
			return nil, err
		}
		identity, err := r.q.GetExternalIdentityByPrincipalProvider(ctx, platformdb.GetExternalIdentityByPrincipalProviderParams{
			PrincipalID: row.ID,
			Provider:    "scim",
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return []access.SCIMUser{}, nil
			}
			return nil, err
		}
		return []access.SCIMUser{{Principal: mapPrincipal(row), ExternalID: identity.Subject}}, nil
	}
	subject := strings.TrimSpace(firstNonEmpty(filter.ID, filter.ExternalID))
	rows, err := r.q.ListSCIMPrincipals(ctx, platformdb.ListSCIMPrincipalsParams{
		Subject:  subject,
		UserName: strings.TrimSpace(filter.UserName),
	})
	if err != nil {
		return nil, err
	}
	users := make([]access.SCIMUser, 0, len(rows))
	for _, row := range rows {
		identity, err := r.q.GetExternalIdentityByPrincipalProvider(ctx, platformdb.GetExternalIdentityByPrincipalProviderParams{
			PrincipalID: row.ID,
			Provider:    "scim",
		})
		if err != nil {
			return nil, err
		}
		users = append(users, access.SCIMUser{Principal: mapPrincipal(row), ExternalID: identity.Subject})
	}
	return users, nil
}

func (r *Repository) DisableSCIMUser(ctx context.Context, principalID string) (access.SCIMUser, error) {
	access.ClearAuthorizationCache(ctx)
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return access.SCIMUser{}, fmt.Errorf("principal id is required")
	}
	identity, err := r.q.GetExternalIdentityByPrincipalProvider(ctx, platformdb.GetExternalIdentityByPrincipalProviderParams{
		PrincipalID: principalID,
		Provider:    "scim",
	})
	if err != nil {
		return access.SCIMUser{}, err
	}
	if _, err := r.DisableProvisionedPrincipal(ctx, principalID); err != nil {
		return access.SCIMUser{}, err
	}
	if err := r.q.DeleteSCIMGroupMembersByPrincipal(ctx, principalID); err != nil {
		return access.SCIMUser{}, err
	}
	row, err := r.q.GetPrincipal(ctx, principalID)
	if err != nil {
		return access.SCIMUser{}, err
	}
	return access.SCIMUser{Principal: mapPrincipal(row), ExternalID: identity.Subject}, nil
}

func (r *Repository) UpsertGroup(ctx context.Context, input access.GroupInput) (access.Group, error) {
	access.ClearAuthorizationCache(ctx)
	if strings.TrimSpace(input.WorkspaceID) == "" {
		return access.Group{}, fmt.Errorf("workspace id is required")
	}
	if err := r.validateWorkspace(ctx, input.WorkspaceID); err != nil {
		return access.Group{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return access.Group{}, fmt.Errorf("group name is required")
	}
	if strings.TrimSpace(input.ID) == "" {
		id, err := newID("group")
		if err != nil {
			return access.Group{}, err
		}
		input.ID = id
	}
	if strings.TrimSpace(input.Provider) == "" && strings.TrimSpace(input.ExternalID) == "" {
		input.Provider = "local"
		input.ExternalID = input.ID
	}
	if err := r.q.UpsertGroup(ctx, platformdb.UpsertGroupParams{
		ID:          input.ID,
		WorkspaceID: input.WorkspaceID,
		Provider:    input.Provider,
		ExternalID:  input.ExternalID,
		Name:        input.Name,
	}); err != nil {
		return access.Group{}, err
	}
	row, err := r.q.GetGroupByProviderExternalID(ctx, platformdb.GetGroupByProviderExternalIDParams{
		WorkspaceID: input.WorkspaceID,
		Provider:    input.Provider,
		ExternalID:  input.ExternalID,
	})
	if err != nil {
		return access.Group{}, err
	}
	return mapGroup(row), nil
}

func (r *Repository) ListGroups(ctx context.Context, workspaceID string) ([]access.Group, error) {
	rows, err := r.q.ListGroupsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	groups := make([]access.Group, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, mapGroup(row))
	}
	return groups, nil
}

func (r *Repository) SearchGroups(ctx context.Context, workspaceID, query string, limit int) ([]access.Group, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []access.Group{}, nil
	}
	if limit <= 0 {
		limit = 8
	}
	rows, err := r.q.SearchGroups(ctx, platformdb.SearchGroupsParams{
		WorkspaceID: workspaceID,
		Search:      query,
		ResultLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	groups := make([]access.Group, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, mapGroup(row))
	}
	return groups, nil
}

func (r *Repository) ListAllGroups(ctx context.Context) ([]access.Group, error) {
	rows, err := r.q.ListAllGroups(ctx)
	if err != nil {
		return nil, err
	}
	groups := make([]access.Group, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, mapGroup(row))
	}
	return groups, nil
}

func (r *Repository) DeleteGroup(ctx context.Context, workspaceID, groupID string) error {
	access.ClearAuthorizationCache(ctx)
	if strings.TrimSpace(groupID) == "" {
		return fmt.Errorf("group id is required")
	}
	if err := r.validateWorkspaceGroup(ctx, workspaceID, groupID); err != nil {
		return err
	}
	return r.q.DeleteGroup(ctx, platformdb.DeleteGroupParams{
		WorkspaceID: workspaceID,
		ID:          groupID,
	})
}

func (r *Repository) AddGroupMember(ctx context.Context, workspaceID, groupID, principalID string) error {
	access.ClearAuthorizationCache(ctx)
	workspaceID = strings.TrimSpace(workspaceID)
	groupID = strings.TrimSpace(groupID)
	principalID = strings.TrimSpace(principalID)
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(principalID) == "" {
		return fmt.Errorf("group id and principal id are required")
	}
	if err := r.validateWorkspaceGroup(ctx, workspaceID, groupID); err != nil {
		return err
	}
	if _, err := r.q.GetPrincipal(ctx, principalID); err != nil {
		return err
	}
	return r.q.InsertGroupMember(ctx, platformdb.InsertGroupMemberParams{
		WorkspaceID: workspaceID,
		GroupID:     groupID,
		PrincipalID: principalID,
	})
}

func (r *Repository) RemoveGroupMember(ctx context.Context, workspaceID, groupID, principalID string) error {
	access.ClearAuthorizationCache(ctx)
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(principalID) == "" {
		return fmt.Errorf("group id and principal id are required")
	}
	if err := r.validateWorkspaceGroup(ctx, workspaceID, groupID); err != nil {
		return err
	}
	if _, err := r.q.GetPrincipal(ctx, principalID); err != nil {
		return err
	}
	return r.q.DeleteGroupMember(ctx, platformdb.DeleteGroupMemberParams{
		WorkspaceID: workspaceID,
		GroupID:     groupID,
		PrincipalID: principalID,
	})
}

func (r *Repository) ListGroupMembers(ctx context.Context, workspaceID, groupID string) ([]access.GroupMember, error) {
	if err := r.validateWorkspaceGroup(ctx, workspaceID, groupID); err != nil {
		return nil, err
	}
	rows, err := r.q.ListGroupMembers(ctx, platformdb.ListGroupMembersParams{
		WorkspaceID: workspaceID,
		GroupID:     groupID,
	})
	if err != nil {
		return nil, err
	}
	members := make([]access.GroupMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, access.GroupMember{
			GroupID:     row.GroupID,
			WorkspaceID: row.WorkspaceID,
			PrincipalID: row.PrincipalID,
			Kind:        access.PrincipalKind(row.Kind),
			Email:       row.Email,
			DisplayName: row.DisplayName,
			CreatedAt:   row.CreatedAt,
		})
	}
	return members, nil
}

func (r *Repository) ListGroupMembersByGroup(ctx context.Context, groupID string) ([]access.GroupMember, error) {
	rows, err := r.q.ListGroupMembersByGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	members := make([]access.GroupMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, access.GroupMember{
			GroupID: row.GroupID, WorkspaceID: row.WorkspaceID, PrincipalID: row.PrincipalID,
			Kind: access.PrincipalKind(row.Kind), Email: row.Email, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt,
		})
	}
	return members, nil
}

func (r *Repository) UpsertSCIMGroup(ctx context.Context, input access.SCIMGroupInput) (access.Group, error) {
	access.ClearAuthorizationCache(ctx)
	externalID := strings.TrimSpace(firstNonEmpty(input.ExternalID, input.ID, input.Name))
	if externalID == "" {
		return access.Group{}, fmt.Errorf("scim group requires external id, id, or display name")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = externalID
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = "scim_group_" + stableID(externalID)
	}
	if input.MemberIDs != nil {
		for _, principalID := range input.MemberIDs {
			principalID = strings.TrimSpace(principalID)
			if principalID == "" {
				continue
			}
			if _, err := r.q.GetExternalIdentityByPrincipalProvider(ctx, platformdb.GetExternalIdentityByPrincipalProviderParams{PrincipalID: principalID, Provider: "scim"}); err != nil {
				return access.Group{}, err
			}
		}
	}
	if err := r.q.UpsertGroup(ctx, platformdb.UpsertGroupParams{
		ID:          id,
		WorkspaceID: "",
		Provider:    "scim",
		ExternalID:  externalID,
		Name:        name,
	}); err != nil {
		return access.Group{}, err
	}
	if input.MemberIDs != nil {
		if err := r.q.DeleteSCIMGroupMembers(ctx, id); err != nil {
			return access.Group{}, err
		}
		for _, principalID := range input.MemberIDs {
			principalID = strings.TrimSpace(principalID)
			if principalID == "" {
				continue
			}
			if err := r.q.InsertGroupMember(ctx, platformdb.InsertGroupMemberParams{
				WorkspaceID: "",
				GroupID:     id,
				PrincipalID: principalID,
			}); err != nil {
				return access.Group{}, err
			}
		}
	}
	row, err := r.q.GetSCIMGroup(ctx, id)
	if err != nil {
		return access.Group{}, err
	}
	return mapGroup(row), nil
}

func (r *Repository) ListSCIMGroups(ctx context.Context, filter access.SCIMGroupFilter) ([]access.Group, error) {
	if strings.TrimSpace(filter.ID) != "" {
		row, err := r.q.GetSCIMGroup(ctx, strings.TrimSpace(filter.ID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return []access.Group{}, nil
			}
			return nil, err
		}
		return []access.Group{mapGroup(row)}, nil
	}
	rows, err := r.q.ListSCIMGroups(ctx, platformdb.ListSCIMGroupsParams{
		ExternalID:  strings.TrimSpace(filter.ExternalID),
		DisplayName: strings.TrimSpace(filter.DisplayName),
	})
	if err != nil {
		return nil, err
	}
	groups := make([]access.Group, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, mapGroup(row))
	}
	return groups, nil
}

func (r *Repository) DeleteSCIMGroup(ctx context.Context, groupID string) error {
	access.ClearAuthorizationCache(ctx)
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return fmt.Errorf("group id is required")
	}
	if _, err := r.q.GetSCIMGroup(ctx, groupID); err != nil {
		return err
	}
	if err := r.q.DeleteSCIMGroupMembers(ctx, groupID); err != nil {
		return err
	}
	return r.q.DeleteSCIMGroup(ctx, groupID)
}

func (r *Repository) AddSCIMGroupMember(ctx context.Context, groupID, principalID string) error {
	access.ClearAuthorizationCache(ctx)
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(principalID) == "" {
		return fmt.Errorf("group id and principal id are required")
	}
	if _, err := r.q.GetSCIMGroup(ctx, groupID); err != nil {
		return err
	}
	if _, err := r.q.GetExternalIdentityByPrincipalProvider(ctx, platformdb.GetExternalIdentityByPrincipalProviderParams{PrincipalID: principalID, Provider: "scim"}); err != nil {
		return err
	}
	return r.q.InsertGroupMember(ctx, platformdb.InsertGroupMemberParams{
		WorkspaceID: "",
		GroupID:     groupID,
		PrincipalID: principalID,
	})
}

func (r *Repository) RemoveSCIMGroupMember(ctx context.Context, groupID, principalID string) error {
	access.ClearAuthorizationCache(ctx)
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(principalID) == "" {
		return fmt.Errorf("group id and principal id are required")
	}
	if _, err := r.q.GetSCIMGroup(ctx, groupID); err != nil {
		return err
	}
	if _, err := r.q.GetExternalIdentityByPrincipalProvider(ctx, platformdb.GetExternalIdentityByPrincipalProviderParams{PrincipalID: principalID, Provider: "scim"}); err != nil {
		return err
	}
	return r.q.DeleteGroupMember(ctx, platformdb.DeleteGroupMemberParams{
		WorkspaceID: "",
		GroupID:     groupID,
		PrincipalID: principalID,
	})
}

func (r *Repository) ListSCIMGroupMembers(ctx context.Context, groupID string) ([]access.GroupMember, error) {
	if _, err := r.q.GetSCIMGroup(ctx, strings.TrimSpace(groupID)); err != nil {
		return nil, err
	}
	rows, err := r.q.ListSCIMGroupMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	members := make([]access.GroupMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, access.GroupMember{
			GroupID:     row.GroupID,
			WorkspaceID: row.WorkspaceID,
			PrincipalID: row.PrincipalID,
			Kind:        access.PrincipalKind(row.Kind),
			Email:       row.Email,
			DisplayName: row.DisplayName,
			CreatedAt:   row.CreatedAt,
		})
	}
	return members, nil
}

func (r *Repository) validateWorkspace(ctx context.Context, workspaceID string) error {
	var exists int
	return r.db.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ? LIMIT 1`, strings.TrimSpace(workspaceID)).Scan(&exists)
}

func (r *Repository) validateWorkspaceGroup(ctx context.Context, workspaceID, groupID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	groupID = strings.TrimSpace(groupID)
	var groupWorkspace, provider string
	if err := r.db.QueryRowContext(ctx, `SELECT workspace_id, provider FROM groups WHERE id = ?`, groupID).Scan(&groupWorkspace, &provider); err != nil {
		return err
	}
	if groupWorkspace != workspaceID || provider == "scim" || workspaceID == "" {
		return sql.ErrNoRows
	}
	return r.validateWorkspace(ctx, workspaceID)
}
