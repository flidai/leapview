package http

import (
	"context"
	"net/http"
	"sort"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/admin/storage"
	"github.com/flidai/leapview/internal/admin/ui"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/agent/api"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

type Principal struct {
	ID          string
	Email       string
	DisplayName string
	DevBypass   bool
}

type AgentDetailsProvider func(context.Context) (api.AdminAgentResponse, error)
type CSRFTokenProvider func(*http.Request) string
type CurrentPrincipalProvider func(*http.Request) (Principal, bool)
type PublicationProvider func(*http.Request) ([]ui.AdminPublication, bool, error)

type AccessReader interface {
	ListPrincipals(context.Context, access.PrincipalFilter) ([]access.Principal, error)
	ListAllGroups(context.Context) ([]access.Group, error)
	ListGroupMembersByGroup(context.Context, string) ([]access.GroupMember, error)
	ListRoles(context.Context) ([]access.Role, error)
	ListAllRoleBindings(context.Context) ([]access.RoleBinding, error)
	Authorize(context.Context, string, access.Privilege, access.ObjectRef) (access.AuthorizationDecision, error)
}

type AvatarReader interface {
	Current(context.Context, string) (avatar.Metadata, error)
}

type ReadModel struct {
	Access              AccessReader
	Avatars             AvatarReader
	AgentDetails        AgentDetailsProvider
	StorageService      storage.Service
	QueryAuditReader    QueryAuditReaderProvider
	CSRFToken           CSRFTokenProvider
	CurrentPrincipal    CurrentPrincipalProvider
	Publications        PublicationProvider
	AgentConfigCommand  uicommand.Binding
	PublicationCommands map[string]uicommand.Binding
	ProductCommands     map[string]uicommand.Binding
	AuthConfigured      bool
	AccessConfigured    bool
}

func (m ReadModel) SettingsData(r *http.Request) (ui.AdminData, error) {
	return m.baseData(r), nil
}

func (m ReadModel) Data(r *http.Request) (ui.AdminData, error) {
	data := m.baseData(r)
	var err error
	data.Agent, err = m.agentData(r)
	if err != nil {
		return data, err
	}
	if m.Publications != nil {
		data.Publications, data.CanManagePublications, err = m.Publications(r)
		if err != nil {
			return data, err
		}
	}
	if err := m.populateAccessDirectory(r, &data, true, true); err != nil {
		return data, err
	}
	data.QueryHistory = m.QueryHistoryData(r, uisignals.AdminQueryHistoryFilters{}, "", 50)
	return data, nil
}

func (m ReadModel) StorageData(r *http.Request) ui.AdminData {
	data := m.baseData(r)
	data.Storage = m.StorageService.Data(r.Context())
	return data
}

func (m ReadModel) StorageTableData(r *http.Request, schema, tableName string) (ui.AdminData, error) {
	data := m.baseData(r)
	table, err := m.StorageService.Table(r.Context(), strings.TrimSpace(schema), strings.TrimSpace(tableName))
	if err != nil {
		return data, err
	}
	data.Storage.Tables = []ui.AdminStorageTable{*table}
	return data, nil
}

func (m ReadModel) PrincipalsListData(r *http.Request) (ui.AdminData, error) {
	data := m.baseData(r)
	err := m.populateAccessDirectory(r, &data, true, false)
	return data, err
}

func (m ReadModel) GroupsListData(r *http.Request) (ui.AdminData, error) {
	data := m.baseData(r)
	err := m.populateAccessDirectory(r, &data, false, true)
	return data, err
}

func (m ReadModel) baseData(r *http.Request) ui.AdminData {
	data := ui.AdminData{
		ListFilter:          strings.TrimSpace(r.URL.Query().Get("filter")),
		ListQuery:           strings.TrimSpace(r.URL.Query().Get("q")),
		CSRFToken:           m.csrfToken(r),
		AuthConfigured:      m.AuthConfigured,
		AccessConfigured:    m.AccessConfigured,
		AccessStatusLabel:   "Configured",
		AgentConfigCommand:  m.AgentConfigCommand,
		PublicationCommands: m.PublicationCommands,
		ProductCommands:     m.ProductCommands,
	}
	return data
}

func (m ReadModel) populateAccessDirectory(r *http.Request, data *ui.AdminData, includePrincipals, includeGroups bool) error {
	repo := m.Access
	if repo == nil {
		data.AccessConfigured = false
		data.AccessStatusLabel = "Access store is not configured"
		data.RoleCount = len(defaultRoleViews())
		return nil
	}
	var principals []ui.AdminPrincipal
	var err error
	if includePrincipals {
		principals, err = m.principalsData(r, repo)
		if err != nil {
			return err
		}
	}
	groups, err := repo.ListAllGroups(r.Context())
	if err != nil {
		return err
	}
	bindings, roles, err := m.roleBindingsAndRoles(r, repo)
	if err != nil {
		return err
	}
	membersByGroup := map[string][]ui.AdminPrincipalRef{}
	groupsByID := map[string]access.Group{}
	for _, group := range groups {
		groupsByID[group.ID] = group
		members := groupMembersData(r, repo, group.ID)
		for _, member := range members {
			membersByGroup[group.ID] = append(membersByGroup[group.ID], ui.AdminPrincipalRef{
				ID:          member.ID,
				Email:       member.Email,
				DisplayName: member.DisplayName,
			})
		}
	}
	data.RoleCount = len(roles)
	data.BindingCount = len(bindings)
	if includePrincipals {
		data.Principals = buildAdminPrincipals(principals, bindings, groupsByID, membersByGroup)
		data.PrincipalCount = len(data.Principals)
	}
	if includeGroups {
		data.Groups = buildAdminGroups(groups, bindings, membersByGroup)
		data.GroupCount = len(data.Groups)
	}
	return nil
}

func (m ReadModel) PublicationData(r *http.Request) (ui.AdminData, error) {
	data := ui.AdminData{CSRFToken: m.csrfToken(r), CanManagePublications: true, PublicationCommands: m.PublicationCommands}
	if m.Publications == nil {
		return data, nil
	}
	rows, allowed, err := m.Publications(r)
	data.Publications = rows
	data.CanManagePublications = allowed
	return data, err
}

func (m ReadModel) agentData(r *http.Request) (ui.AdminAgentData, error) {
	details, err := m.agentDetails(r.Context())
	if err != nil {
		return ui.AdminAgentData{}, err
	}
	data := ui.AdminAgentData{
		Enabled:      details.Enabled,
		Model:        details.Model,
		SystemPrompt: details.SystemPrompt,
		CSRFToken:    m.csrfToken(r),
		UpdatePath:   "/admin/agent/config",
		CanWrite:     true,
	}
	data.Revision, err = apigencommand.RevisionToken(details)
	if err != nil {
		return ui.AdminAgentData{}, err
	}
	for _, tool := range details.Tools {
		data.Tools = append(data.Tools, ui.AdminAgentTool{
			Name:         tool.Name,
			Description:  tool.Description,
			Effect:       tool.Effect,
			Defaults:     tool.Defaults,
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
		})
	}
	if !m.AuthConfigured {
		return data, nil
	}
	principal, ok := m.currentPrincipal(r)
	if !ok || principal.DevBypass {
		return data, nil
	}
	repo := m.Access
	if repo == nil {
		return data, nil
	}
	decision, err := repo.Authorize(r.Context(), principal.ID, access.PrivilegeManagePlatform, access.PlatformObject())
	if err != nil {
		return data, err
	}
	data.CanWrite = decision.Allowed
	return data, nil
}

func (m ReadModel) principalsData(r *http.Request, repo AccessReader) ([]ui.AdminPrincipal, error) {
	rows, err := repo.ListPrincipals(r.Context(), access.PrincipalFilter{
		Email: r.URL.Query().Get("email"),
		Query: r.URL.Query().Get("q"),
	})
	if err != nil {
		return nil, err
	}
	principals := make([]ui.AdminPrincipal, 0, len(rows))
	for _, row := range rows {
		principals = append(principals, ui.AdminPrincipal{
			ID:                row.ID,
			Kind:              string(row.Kind),
			Email:             row.Email,
			DisplayName:       row.DisplayName,
			ProfilePictureURL: m.avatarURL(r.Context(), row.ID),
			DisabledAt:        row.DisabledAt,
			CreatedAt:         row.CreatedAt,
			UpdatedAt:         row.UpdatedAt,
			LastSeenAt:        row.LastSeenAt,
		})
	}
	sort.SliceStable(principals, func(i, j int) bool {
		return adminPrincipalSortKey(principals[i]) < adminPrincipalSortKey(principals[j])
	})
	return principals, nil
}

func (m ReadModel) avatarURL(ctx context.Context, principalID string) string {
	if m.Avatars == nil {
		return ""
	}
	metadata, err := m.Avatars.Current(ctx, principalID)
	if err != nil {
		return ""
	}
	return avatar.URLForPrincipal(principalID, metadata)
}

func groupMembersData(r *http.Request, repo AccessReader, groupID string) []ui.AdminPrincipalRef {
	rows, err := repo.ListGroupMembersByGroup(r.Context(), groupID)
	if err != nil {
		return nil
	}
	members := make([]ui.AdminPrincipalRef, 0, len(rows))
	for _, row := range rows {
		members = append(members, ui.AdminPrincipalRef{
			ID:          row.PrincipalID,
			Email:       row.Email,
			DisplayName: row.DisplayName,
		})
	}
	return members
}

type roleView struct {
	Name       string
	Privileges []string
}

type adminRoleBindingView struct {
	ID          string
	SubjectType string
	SubjectID   string
	PrincipalID string
	GroupID     string
	Email       string
	DisplayName string
	GroupName   string
	Role        string
	CreatedAt   string
}

func (m ReadModel) roleBindingsAndRoles(r *http.Request, repo AccessReader) ([]adminRoleBindingView, []roleView, error) {
	if repo == nil {
		return nil, defaultRoleViews(), nil
	}
	roleRows, err := repo.ListRoles(r.Context())
	if err != nil {
		return nil, nil, err
	}
	bindingRows, err := repo.ListAllRoleBindings(r.Context())
	if err != nil {
		return nil, nil, err
	}
	bindings := make([]adminRoleBindingView, 0, len(bindingRows))
	for _, row := range bindingRows {
		bindings = append(bindings, roleBindingView(row))
	}
	return bindings, roleViews(roleRows), nil
}

func (m ReadModel) QueryHistoryData(r *http.Request, filters uisignals.AdminQueryHistoryFilters, pageToken string, limit int) ui.AdminQueryHistoryData {
	repo, err := m.queryAuditReader()
	if err != nil || repo == nil {
		return ui.AdminQueryHistoryData{Filters: filters, Limit: normalizeQueryHistoryLimit(limit), Error: queryHistoryErrorText(err)}
	}
	filters = normalizeQueryHistoryFilters(filters)
	events, nextCursor, hasMore, err := queryHistoryPage(r, repo, filters, pageToken, limit)
	if err != nil {
		return ui.AdminQueryHistoryData{Filters: filters, Limit: normalizeQueryHistoryLimit(limit), Error: err.Error()}
	}
	return ui.AdminQueryHistoryData{
		Events:      events,
		FilterMenus: m.queryHistoryFilterMenus(r, repo, filters, "", ""),
		Filters:     filters,
		NextCursor:  nextCursor,
		HasMore:     hasMore,
		Limit:       normalizeQueryHistoryLimit(limit),
	}
}

func (m ReadModel) PrincipalLabels(r *http.Request, values []string) map[string]string {
	labels := map[string]string{}
	current, hasCurrent := m.currentPrincipal(r)
	for _, value := range values {
		if value == "" {
			continue
		}
		if hasCurrent && value == current.ID {
			identity := firstNonEmpty(current.Email, current.DisplayName, current.ID)
			labels[value] = "Me (" + identity + ")"
			continue
		}
		labels[value] = value
	}
	return labels
}

func (m ReadModel) queryAuditReader() (queryaudit.Reader, error) {
	if m.QueryAuditReader == nil {
		return nil, nil
	}
	return m.QueryAuditReader()
}

func (m ReadModel) agentDetails(ctx context.Context) (api.AdminAgentResponse, error) {
	if m.AgentDetails == nil {
		return api.AdminAgentResponse{}, nil
	}
	return m.AgentDetails(ctx)
}

func (m ReadModel) currentPrincipal(r *http.Request) (Principal, bool) {
	if m.CurrentPrincipal == nil {
		return Principal{}, false
	}
	return m.CurrentPrincipal(r)
}

func (m ReadModel) csrfToken(r *http.Request) string {
	if m.CSRFToken == nil {
		return ""
	}
	return m.CSRFToken(r)
}

func buildAdminPrincipals(principals []ui.AdminPrincipal, bindings []adminRoleBindingView, groupsByID map[string]access.Group, membersByGroup map[string][]ui.AdminPrincipalRef) []ui.AdminPrincipal {
	byID := make(map[string]int, len(principals))
	out := make([]ui.AdminPrincipal, 0, len(principals))
	for _, principal := range principals {
		index := len(out)
		byID[principal.ID] = index
		out = append(out, principal)
	}
	for _, binding := range bindings {
		if binding.SubjectType == string(access.SubjectPrincipal) && binding.PrincipalID != "" {
			if index, ok := byID[binding.PrincipalID]; ok {
				out[index].DirectRoles = appendUnique(out[index].DirectRoles, binding.Role)
			}
		}
	}
	for groupID, members := range membersByGroup {
		group := groupsByID[groupID]
		for _, member := range members {
			if index, ok := byID[member.ID]; ok {
				out[index].Groups = appendAdminGroupRefUnique(out[index].Groups, ui.AdminGroupRef{
					ID:         group.ID,
					Name:       group.Name,
					ExternalID: group.ExternalID,
				})
			}
		}
	}
	for i := range out {
		sort.Strings(out[i].DirectRoles)
		sort.SliceStable(out[i].Groups, func(i, j int) bool {
			return out[i].Groups[i].Name < out[i].Groups[j].Name
		})
	}
	return out
}

func appendAdminGroupRefUnique(values []ui.AdminGroupRef, value ui.AdminGroupRef) []ui.AdminGroupRef {
	for _, existing := range values {
		if existing.ID == value.ID {
			return values
		}
	}
	return append(values, value)
}

func buildAdminGroups(groups []access.Group, bindings []adminRoleBindingView, membersByGroup map[string][]ui.AdminPrincipalRef) []ui.AdminGroup {
	out := make([]ui.AdminGroup, 0, len(groups))
	byID := make(map[string]*ui.AdminGroup, len(groups))
	for _, group := range groups {
		row := ui.AdminGroup{
			ID:         group.ID,
			Name:       group.Name,
			Provider:   group.Provider,
			ExternalID: group.ExternalID,
			CreatedAt:  group.CreatedAt,
			Members:    membersByGroup[group.ID],
		}
		sort.SliceStable(row.Members, func(i, j int) bool {
			return adminPrincipalRefSortKey(row.Members[i]) < adminPrincipalRefSortKey(row.Members[j])
		})
		byID[row.ID] = &row
		out = append(out, row)
	}
	for _, binding := range bindings {
		if binding.SubjectType == string(access.SubjectGroup) && binding.GroupID != "" {
			if group := byID[binding.GroupID]; group != nil {
				group.Roles = appendUnique(group.Roles, binding.Role)
			}
		}
	}
	for i := range out {
		if group := byID[out[i].ID]; group != nil {
			sort.Strings(group.Roles)
			out[i] = *group
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func defaultRoleViews() []roleView {
	return roleViews(access.DefaultRoles())
}

func roleViews(rows []access.Role) []roleView {
	roles := make([]roleView, 0, len(rows))
	for _, row := range rows {
		roles = append(roles, roleView{Name: row.Name, Privileges: privilegeStrings(row.Privileges)})
	}
	return roles
}

func privilegeStrings(values []access.Privilege) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

func roleBindingView(row access.RoleBinding) adminRoleBindingView {
	return adminRoleBindingView{
		ID:          row.ID,
		SubjectType: string(row.SubjectType),
		SubjectID:   row.SubjectID,
		PrincipalID: row.PrincipalID,
		GroupID:     row.GroupID,
		Email:       row.Email,
		DisplayName: firstNonEmpty(row.DisplayName, row.GroupName),
		GroupName:   row.GroupName,
		Role:        row.Role,
		CreatedAt:   row.CreatedAt,
	}
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func adminPrincipalSortKey(row ui.AdminPrincipal) string {
	return firstNonEmpty(row.Email, row.DisplayName, row.ID)
}

func adminPrincipalRefSortKey(row ui.AdminPrincipalRef) string {
	return firstNonEmpty(row.Email, row.DisplayName, row.ID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
