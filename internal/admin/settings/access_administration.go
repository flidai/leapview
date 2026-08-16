package settings

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
)

type AccessAdministrationSignal struct {
	Principals          []AccessPrincipalSignal      `json:"principals"`
	Groups              []AccessGroupSignal          `json:"groups"`
	Sessions            []AccessSessionSignal        `json:"sessions"`
	RoleAssignments     []AccessRoleAssignmentSignal `json:"roleAssignments"`
	Activity            []AccessActivitySignal       `json:"activity"`
	SelectedPrincipalID string                       `json:"selectedPrincipalId,omitempty"`
	SelectedGroupID     string                       `json:"selectedGroupId,omitempty"`
	TemporaryPassword   string                       `json:"temporaryPassword,omitempty"`
	RedirectTo          string                       `json:"redirectTo,omitempty"`
	Message             string                       `json:"message,omitempty"`
	Error               string                       `json:"error,omitempty"`
	Loading             bool                         `json:"loading"`
}

type AccessPrincipalCapabilitiesSignal struct {
	CanUpdateProfile  bool `json:"canUpdateProfile"`
	CanResetPassword  bool `json:"canResetPassword"`
	CanBlock          bool `json:"canBlock"`
	CanUnblock        bool `json:"canUnblock"`
	CanDelete         bool `json:"canDelete"`
	CanManageSessions bool `json:"canManageSessions"`
}

type AccessPrincipalSignal struct {
	ID               string                            `json:"id"`
	Kind             string                            `json:"kind"`
	Email            string                            `json:"email"`
	DisplayName      string                            `json:"displayName"`
	IdentitySource   string                            `json:"identitySource"`
	IdentityProvider string                            `json:"identityProvider,omitempty"`
	HasLocalPassword bool                              `json:"hasLocalPassword"`
	DisabledAt       string                            `json:"disabledAt,omitempty"`
	BlockedAt        string                            `json:"blockedAt,omitempty"`
	CreatedAt        string                            `json:"createdAt,omitempty"`
	UpdatedAt        string                            `json:"updatedAt,omitempty"`
	LastSeenAt       string                            `json:"lastSeenAt,omitempty"`
	Revision         string                            `json:"revision,omitempty"`
	Groups           []AccessGroupReferenceSignal      `json:"groups"`
	Capabilities     AccessPrincipalCapabilitiesSignal `json:"capabilities"`
}

type AccessGroupCapabilitiesSignal struct {
	CanUpdate        bool `json:"canUpdate"`
	CanDelete        bool `json:"canDelete"`
	CanManageMembers bool `json:"canManageMembers"`
}

type AccessGroupSignal struct {
	ID           string                           `json:"id"`
	WorkspaceID  string                           `json:"workspaceId,omitempty"`
	Name         string                           `json:"name"`
	Provider     string                           `json:"provider"`
	ExternalID   string                           `json:"externalId,omitempty"`
	CreatedAt    string                           `json:"createdAt,omitempty"`
	Revision     string                           `json:"revision,omitempty"`
	Members      []AccessPrincipalReferenceSignal `json:"members"`
	Capabilities AccessGroupCapabilitiesSignal    `json:"capabilities"`
}

type AccessGroupReferenceSignal struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type AccessPrincipalReferenceSignal struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

type AccessSessionSignal struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	CreatedAt  string `json:"createdAt,omitempty"`
	LastSeenAt string `json:"lastSeenAt,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
}

type AccessRoleAssignmentSignal struct {
	WorkspaceID string `json:"workspaceId"`
	Role        string `json:"role"`
	SourceType  string `json:"sourceType"`
	SourceID    string `json:"sourceId"`
	SourceName  string `json:"sourceName"`
}

type AccessActivitySignal struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	ActorID   string `json:"actorId,omitempty"`
	ActorName string `json:"actorName,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type AccessAdministrationCommand struct {
	Action       string   `json:"action"`
	PrincipalID  string   `json:"principalId,omitempty"`
	PrincipalIDs []string `json:"principalIds,omitempty"`
	GroupID      string   `json:"groupId,omitempty"`
	SessionID    string   `json:"sessionId,omitempty"`
	WorkspaceID  string   `json:"workspaceId,omitempty"`
	Email        string   `json:"email,omitempty"`
	DisplayName  string   `json:"displayName,omitempty"`
	Revision     string   `json:"revision,omitempty"`
}

type AccessAdministrationResult struct {
	SelectedPrincipalID string
	SelectedGroupID     string
	TemporaryPassword   string
	Message             string
	Deleted             bool
}

func NormalizeAccessAdministrationCommand(command AccessAdministrationCommand) AccessAdministrationCommand {
	command.Action = strings.TrimSpace(command.Action)
	command.PrincipalID = strings.TrimSpace(command.PrincipalID)
	command.PrincipalIDs = normalizeAccessAdministrationIDs(command.PrincipalIDs)
	command.GroupID = strings.TrimSpace(command.GroupID)
	command.SessionID = strings.TrimSpace(command.SessionID)
	command.WorkspaceID = strings.TrimSpace(command.WorkspaceID)
	command.Email = access.NormalizeEmail(command.Email)
	command.DisplayName = strings.TrimSpace(command.DisplayName)
	command.Revision = strings.TrimSpace(command.Revision)
	return command
}

func normalizeAccessAdministrationIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func LoadAccessAdministration(ctx context.Context, repository access.Repository, actorID, selectedPrincipalID, selectedGroupID string) (AccessAdministrationSignal, error) {
	state := AccessAdministrationSignal{
		Principals: []AccessPrincipalSignal{}, Groups: []AccessGroupSignal{}, Sessions: []AccessSessionSignal{},
		RoleAssignments: []AccessRoleAssignmentSignal{}, Activity: []AccessActivitySignal{},
		SelectedPrincipalID: strings.TrimSpace(selectedPrincipalID), SelectedGroupID: strings.TrimSpace(selectedGroupID),
	}
	if repository == nil {
		state.Error = "Access administration is unavailable."
		return state, nil
	}
	principals, err := repository.ListPrincipals(ctx, access.PrincipalFilter{})
	if err != nil {
		return state, err
	}
	groups, err := repository.ListAllGroups(ctx)
	if err != nil {
		return state, err
	}
	groupMembers := make(map[string][]access.GroupMember, len(groups))
	principalGroups := make(map[string][]AccessGroupReferenceSignal, len(principals))
	for _, group := range groups {
		members, memberErr := repository.ListGroupMembersByGroup(ctx, group.ID)
		if memberErr != nil {
			return state, memberErr
		}
		groupMembers[group.ID] = members
		for _, member := range members {
			principalGroups[member.PrincipalID] = append(principalGroups[member.PrincipalID], AccessGroupReferenceSignal{ID: group.ID, Name: group.Name, Provider: group.Provider})
		}
	}
	managementReader, _ := repository.(access.PrincipalIdentityManagementRepository)
	principalNames := make(map[string]string, len(principals))
	for _, principal := range principals {
		principalNames[principal.ID] = firstAccessValue(principal.DisplayName, principal.Email, principal.ID)
		management := access.PrincipalIdentityManagement{Source: access.IdentityManagementSystem}
		if managementReader != nil {
			management, err = managementReader.PrincipalIdentityManagement(ctx, principal.ID)
			if err != nil {
				return state, err
			}
		}
		revision, _ := access.PrincipalRevision(principal)
		isUser := principal.Kind == access.PrincipalKindUser
		isSelf := principal.ID == strings.TrimSpace(actorID)
		local := management.Source == access.IdentityManagementLocal
		row := AccessPrincipalSignal{
			ID: principal.ID, Kind: string(principal.Kind), Email: principal.Email, DisplayName: principal.DisplayName,
			IdentitySource: string(management.Source), IdentityProvider: management.Provider, HasLocalPassword: management.HasLocalPassword,
			DisabledAt: principal.DisabledAt, BlockedAt: principal.BlockedAt, CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt,
			Revision: revision, Groups: principalGroups[principal.ID],
			Capabilities: AccessPrincipalCapabilitiesSignal{
				CanUpdateProfile: isUser && local, CanResetPassword: isUser && management.HasLocalPassword,
				CanBlock: isUser && !isSelf && principal.BlockedAt == "" && principal.DisabledAt == "", CanUnblock: isUser && principal.BlockedAt != "" && principal.DisabledAt == "",
				CanDelete: isUser && !isSelf && local, CanManageSessions: isUser,
			},
		}
		if row.Groups == nil {
			row.Groups = []AccessGroupReferenceSignal{}
		}
		state.Principals = append(state.Principals, row)
	}
	for _, group := range groups {
		members := make([]AccessPrincipalReferenceSignal, 0, len(groupMembers[group.ID]))
		for _, member := range groupMembers[group.ID] {
			members = append(members, AccessPrincipalReferenceSignal{ID: member.PrincipalID, Email: member.Email, DisplayName: member.DisplayName})
		}
		revision, _ := access.GroupRevision(group)
		local := accessGroupIsLocal(group)
		state.Groups = append(state.Groups, AccessGroupSignal{
			ID: group.ID, WorkspaceID: group.WorkspaceID, Name: group.Name, Provider: group.Provider, ExternalID: group.ExternalID,
			CreatedAt: group.CreatedAt, Revision: revision, Members: members,
			Capabilities: AccessGroupCapabilitiesSignal{CanUpdate: local, CanDelete: local, CanManageMembers: local},
		})
	}
	sort.SliceStable(state.Principals, func(i, j int) bool {
		return accessPrincipalSortKey(state.Principals[i]) < accessPrincipalSortKey(state.Principals[j])
	})
	sort.SliceStable(state.Groups, func(i, j int) bool {
		return strings.ToLower(state.Groups[i].Name) < strings.ToLower(state.Groups[j].Name)
	})
	if state.SelectedPrincipalID != "" {
		selected, found := accessPrincipalByID(state.Principals, state.SelectedPrincipalID)
		if !found {
			return state, fmt.Errorf("principal not found")
		}
		sessions, sessionErr := repository.ListSessions(ctx, state.SelectedPrincipalID)
		if sessionErr != nil {
			return state, sessionErr
		}
		for _, session := range sessions {
			selected.LastSeenAt = latestAccessTimestamp(selected.LastSeenAt, firstAccessValue(session.LastSeenAt, session.CreatedAt))
			if session.RevokedAt == "" {
				state.Sessions = append(state.Sessions, AccessSessionSignal{ID: session.ID, Kind: string(session.Kind), CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, ExpiresAt: session.ExpiresAt})
			}
		}
		for index := range state.Principals {
			if state.Principals[index].ID == selected.ID {
				state.Principals[index].LastSeenAt = selected.LastSeenAt
				break
			}
		}
		bindings, bindingErr := repository.ListAllRoleBindings(ctx)
		if bindingErr != nil {
			return state, bindingErr
		}
		selectedGroups := make(map[string]AccessGroupReferenceSignal, len(selected.Groups))
		for _, group := range selected.Groups {
			selectedGroups[group.ID] = group
		}
		for _, binding := range bindings {
			sourceType, sourceID, sourceName := "", "", ""
			if binding.SubjectType == access.SubjectPrincipal && binding.SubjectID == selected.ID {
				sourceType, sourceID, sourceName = "direct", selected.ID, firstAccessValue(selected.DisplayName, selected.Email, selected.ID)
			} else if group, ok := selectedGroups[binding.SubjectID]; binding.SubjectType == access.SubjectGroup && ok {
				sourceType, sourceID, sourceName = "group", group.ID, firstAccessValue(group.Name, group.ID)
			}
			if sourceType != "" {
				state.RoleAssignments = append(state.RoleAssignments, AccessRoleAssignmentSignal{WorkspaceID: binding.WorkspaceID, Role: binding.Role, SourceType: sourceType, SourceID: sourceID, SourceName: sourceName})
			}
		}
		sort.SliceStable(state.RoleAssignments, func(i, j int) bool {
			left, right := state.RoleAssignments[i], state.RoleAssignments[j]
			if left.WorkspaceID != right.WorkspaceID {
				return left.WorkspaceID < right.WorkspaceID
			}
			if left.Role != right.Role {
				return left.Role < right.Role
			}
			return left.SourceType < right.SourceType
		})
		events, activityErr := repository.ListAuditEvents(ctx, access.AuditEventFilter{TargetType: "principal", TargetID: selected.ID, Limit: 10})
		if activityErr != nil {
			return state, activityErr
		}
		for _, event := range events {
			state.Activity = append(state.Activity, AccessActivitySignal{ID: event.ID, Action: event.Action, ActorID: event.PrincipalID,
				ActorName: firstAccessValue(principalNames[event.PrincipalID], event.PrincipalID, "System"), Status: event.Status, CreatedAt: event.CreatedAt})
		}
	}
	if state.SelectedGroupID != "" {
		if _, found := accessGroupSignalByID(state.Groups, state.SelectedGroupID); !found {
			return state, fmt.Errorf("group not found")
		}
	}
	return state, nil
}

func ApplyAccessAdministrationCommand(ctx context.Context, repository access.Repository, actorID string, command AccessAdministrationCommand) (AccessAdministrationResult, error) {
	if repository == nil {
		return AccessAdministrationResult{}, errors.New("access administration is unavailable")
	}
	command = NormalizeAccessAdministrationCommand(command)
	result := AccessAdministrationResult{SelectedPrincipalID: command.PrincipalID, SelectedGroupID: command.GroupID}
	mutation := func(tx access.Repository) (access.AuditEventInput, error) {
		event := access.AuditEventInput{PrincipalID: strings.TrimSpace(actorID), Privilege: access.PrivilegeManageGrants, Status: "success", MetadataJSON: `{}`}
		var mutationErr error
		switch command.Action {
		case "create_principal":
			if command.Email == "" || command.DisplayName == "" {
				return event, errors.New("email and display name are required")
			}
			created, err := tx.CreateLocalUser(ctx, access.LocalUserInput{Email: command.Email, DisplayName: command.DisplayName, MustChange: true})
			mutationErr = err
			result.SelectedPrincipalID, result.TemporaryPassword = created.Principal.ID, created.Password
			event.Action, event.TargetType, event.TargetID = "principal.local_user.created", "principal", created.Principal.ID
			result.Message = "Local user created. Copy the temporary password now."
		case "update_principal":
			current, management, err := accessAdministrationPrincipal(ctx, tx, command.PrincipalID)
			if err != nil {
				return event, err
			}
			if current.Kind != access.PrincipalKindUser || management.Source != access.IdentityManagementLocal {
				return event, errors.New("this principal's profile is managed externally")
			}
			if command.DisplayName == "" {
				return event, errors.New("display name is required")
			}
			if err := checkPrincipalRevision(current, command.Revision); err != nil {
				return event, err
			}
			_, mutationErr = tx.UpsertPrincipal(ctx, access.PrincipalInput{ID: current.ID, Kind: current.Kind, Email: current.Email, DisplayName: command.DisplayName})
			event.Action, event.TargetType, event.TargetID = "principal.updated", "principal", current.ID
			result.Message = "Principal updated."
		case "delete_principal":
			current, management, err := accessAdministrationPrincipal(ctx, tx, command.PrincipalID)
			if err != nil {
				return event, err
			}
			if current.ID == strings.TrimSpace(actorID) {
				return event, errors.New("you cannot delete your own account")
			}
			if current.Kind != access.PrincipalKindUser || management.Source != access.IdentityManagementLocal {
				return event, errors.New("this principal is managed externally")
			}
			deleter, ok := tx.(interface {
				DeletePrincipal(context.Context, string) error
			})
			if !ok {
				return event, errors.New("principal deletion is unavailable")
			}
			mutationErr = deleter.DeletePrincipal(ctx, current.ID)
			event.Action, event.TargetType, event.TargetID = "principal.deleted", "principal", current.ID
			result.Deleted, result.SelectedPrincipalID, result.Message = true, "", "Principal deleted."
		case "reset_password":
			_, management, err := accessAdministrationPrincipal(ctx, tx, command.PrincipalID)
			if err != nil {
				return event, err
			}
			if !management.HasLocalPassword {
				return event, errors.New("this principal does not have a local password")
			}
			reset, err := tx.ResetLocalPassword(ctx, command.PrincipalID)
			mutationErr, result.TemporaryPassword = err, reset.Password
			event.Action, event.TargetType, event.TargetID = "principal.local_password.reset", "principal", command.PrincipalID
			result.Message = "Password reset. Copy the temporary password now."
		case "block_principal", "unblock_principal":
			current, _, err := accessAdministrationPrincipal(ctx, tx, command.PrincipalID)
			if err != nil {
				return event, err
			}
			if current.Kind != access.PrincipalKindUser {
				return event, errors.New("only users can be blocked")
			}
			if command.Action == "block_principal" && current.ID == strings.TrimSpace(actorID) {
				return event, errors.New("you cannot block your own account")
			}
			writer, ok := tx.(interface {
				DisablePrincipal(context.Context, string) (access.Principal, error)
				EnablePrincipal(context.Context, string) (access.Principal, error)
			})
			if !ok {
				return event, errors.New("principal status changes are unavailable")
			}
			if command.Action == "block_principal" {
				_, mutationErr = writer.DisablePrincipal(ctx, current.ID)
				event.Action, result.Message = "principal.blocked", "Principal blocked and active sessions revoked."
			} else {
				_, mutationErr = writer.EnablePrincipal(ctx, current.ID)
				event.Action, result.Message = "principal.unblocked", "Principal unblocked."
			}
			event.TargetType, event.TargetID = "principal", current.ID
		case "revoke_session":
			if command.PrincipalID == "" || command.SessionID == "" {
				return event, errors.New("principal and session are required")
			}
			mutationErr = tx.RevokeSessionForPrincipal(ctx, command.PrincipalID, command.SessionID)
			event.Action, event.TargetType, event.TargetID = "principal.session.revoked", "session", command.SessionID
			result.Message = "Session revoked."
		case "revoke_all_sessions":
			if command.PrincipalID == "" {
				return event, errors.New("principal is required")
			}
			sessions, err := tx.ListSessions(ctx, command.PrincipalID)
			if err != nil {
				return event, err
			}
			for _, session := range sessions {
				if session.RevokedAt == "" {
					if err := tx.RevokeSessionForPrincipal(ctx, command.PrincipalID, session.ID); err != nil {
						return event, err
					}
				}
			}
			event.Action, event.TargetType, event.TargetID = "principal.sessions.revoked", "principal", command.PrincipalID
			result.Message = "All active sessions revoked."
		case "create_group":
			workspaceID := command.WorkspaceID
			if workspaceID == "" || command.DisplayName == "" {
				return event, errors.New("workspace and group name are required")
			}
			group, err := tx.UpsertGroup(ctx, access.GroupInput{WorkspaceID: workspaceID, Name: command.DisplayName})
			mutationErr, result.SelectedGroupID = err, group.ID
			event.Action, event.WorkspaceID, event.TargetType, event.TargetID = "group.created", workspaceID, "group", group.ID
			result.Message = "Group created."
		case "update_group", "delete_group", "add_group_member", "remove_group_member":
			group, err := accessAdministrationGroup(ctx, tx, command.GroupID)
			if err != nil {
				return event, err
			}
			if !accessGroupIsLocal(group) {
				return event, fmt.Errorf("group is managed by %s", firstAccessValue(group.Provider, "its identity provider"))
			}
			event.WorkspaceID = group.WorkspaceID
			switch command.Action {
			case "update_group":
				if command.DisplayName == "" {
					return event, errors.New("group name is required")
				}
				if err := checkGroupRevision(group, command.Revision); err != nil {
					return event, err
				}
				_, mutationErr = tx.UpsertGroup(ctx, access.GroupInput{ID: group.ID, WorkspaceID: group.WorkspaceID, Provider: group.Provider, ExternalID: group.ExternalID, Name: command.DisplayName})
				event.Action, event.TargetType, event.TargetID = "group.updated", "group", group.ID
				result.Message = "Group updated."
			case "delete_group":
				mutationErr = tx.DeleteGroup(ctx, group.WorkspaceID, group.ID)
				event.Action, event.TargetType, event.TargetID = "group.deleted", "group", group.ID
				result.Deleted, result.SelectedGroupID, result.Message = true, "", "Group deleted."
			case "add_group_member":
				principalIDs := command.PrincipalIDs
				if len(principalIDs) == 0 && command.PrincipalID != "" {
					principalIDs = []string{command.PrincipalID}
				}
				if len(principalIDs) == 0 {
					return event, errors.New("principal is required")
				}
				for _, principalID := range principalIDs {
					if err := tx.AddGroupMember(ctx, group.WorkspaceID, group.ID, principalID); err != nil {
						return event, err
					}
				}
				if len(principalIDs) == 1 {
					event.Action, event.TargetType, event.TargetID = "group.member_added", "group_member", group.ID+":"+principalIDs[0]
					result.Message = "Member added."
				} else {
					event.Action, event.TargetType, event.TargetID = "group.members_added", "group", group.ID
					result.Message = fmt.Sprintf("%d members added.", len(principalIDs))
				}
			case "remove_group_member":
				if command.PrincipalID == "" {
					return event, errors.New("principal is required")
				}
				mutationErr = tx.RemoveGroupMember(ctx, group.WorkspaceID, group.ID, command.PrincipalID)
				event.Action, event.TargetType, event.TargetID = "group.member_removed", "group_member", group.ID+":"+command.PrincipalID
				result.Message = "Member removed."
			}
		default:
			return event, errors.New("unknown access administration action")
		}
		return event, mutationErr
	}
	if transactional, ok := repository.(access.AuditedMutationRepository); ok {
		if err := transactional.RunAuditedMutation(ctx, mutation); err != nil {
			return AccessAdministrationResult{}, err
		}
		return result, nil
	}
	event, err := mutation(repository)
	if err != nil {
		return AccessAdministrationResult{}, err
	}
	if err := access.PersistAuditEvent(ctx, repository, event); err != nil {
		return AccessAdministrationResult{}, err
	}
	return result, nil
}

func accessAdministrationPrincipal(ctx context.Context, repository access.Repository, id string) (access.Principal, access.PrincipalIdentityManagement, error) {
	if strings.TrimSpace(id) == "" {
		return access.Principal{}, access.PrincipalIdentityManagement{}, errors.New("principal is required")
	}
	principal, err := repository.PrincipalByID(ctx, id)
	if err != nil {
		return access.Principal{}, access.PrincipalIdentityManagement{}, err
	}
	management := access.PrincipalIdentityManagement{Source: access.IdentityManagementSystem}
	if reader, ok := repository.(access.PrincipalIdentityManagementRepository); ok {
		management, err = reader.PrincipalIdentityManagement(ctx, id)
	}
	return principal, management, err
}

func accessAdministrationGroup(ctx context.Context, repository access.Repository, id string) (access.Group, error) {
	if strings.TrimSpace(id) == "" {
		return access.Group{}, errors.New("group is required")
	}
	groups, err := repository.ListAllGroups(ctx)
	if err != nil {
		return access.Group{}, err
	}
	for _, group := range groups {
		if group.ID == id {
			return group, nil
		}
	}
	return access.Group{}, errors.New("group not found")
}

func checkPrincipalRevision(principal access.Principal, presented string) error {
	current, err := access.PrincipalRevision(principal)
	if err != nil {
		return err
	}
	if presented == "" || presented != current {
		return errors.New("principal changed; refresh and try again")
	}
	return nil
}

func checkGroupRevision(group access.Group, presented string) error {
	current, err := access.GroupRevision(group)
	if err != nil {
		return err
	}
	if presented == "" || presented != current {
		return errors.New("group changed; refresh and try again")
	}
	return nil
}

func accessGroupIsLocal(group access.Group) bool {
	return strings.EqualFold(strings.TrimSpace(group.Provider), "local") && strings.TrimSpace(group.WorkspaceID) != ""
}

func accessPrincipalSortKey(principal AccessPrincipalSignal) string {
	return strings.ToLower(firstAccessValue(principal.DisplayName, principal.Email, principal.ID))
}

func accessPrincipalByID(principals []AccessPrincipalSignal, id string) (AccessPrincipalSignal, bool) {
	for _, principal := range principals {
		if principal.ID == id {
			return principal, true
		}
	}
	return AccessPrincipalSignal{}, false
}

func accessGroupSignalByID(groups []AccessGroupSignal, id string) (AccessGroupSignal, bool) {
	for _, group := range groups {
		if group.ID == id {
			return group, true
		}
	}
	return AccessGroupSignal{}, false
}

func firstAccessValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func latestAccessTimestamp(current, candidate string) string {
	current, candidate = strings.TrimSpace(current), strings.TrimSpace(candidate)
	if current == "" {
		return candidate
	}
	if candidate == "" {
		return current
	}
	parse := func(value string) (time.Time, bool) {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, true
			}
		}
		return time.Time{}, false
	}
	currentTime, currentOK := parse(current)
	candidateTime, candidateOK := parse(candidate)
	if currentOK && candidateOK && candidateTime.After(currentTime) {
		return candidate
	}
	if !currentOK && !candidateOK && candidate > current {
		return candidate
	}
	return current
}
