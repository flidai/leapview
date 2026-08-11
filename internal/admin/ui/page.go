package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	adminview "github.com/flidai/leapview/internal/admin/view"
	"github.com/flidai/leapview/internal/dashboard"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	workspaceview "github.com/flidai/leapview/internal/workspace"
	g "maragu.dev/gomponents"
)

type AdminData struct {
	Workspace             workspaceview.WorkspaceView
	CSRFToken             string
	AuthConfigured        bool
	AccessConfigured      bool
	AccessStatusLabel     string
	PrincipalCount        int
	GroupCount            int
	BindingCount          int
	RoleCount             int
	Principals            []AdminPrincipal
	SelectedPrincipal     *AdminPrincipal
	Groups                []AdminGroup
	SelectedGroup         *AdminGroup
	Agent                 AdminAgentData
	Storage               AdminStorageData
	QueryHistory          AdminQueryHistoryData
	Publications          []AdminPublication
	CanManagePublications bool
	PublicationCommands   map[string]uicommand.Binding
	Profile               AdminProfile
	ListFilter            string
	ListQuery             string
}

type AdminProfile struct {
	ID                string
	Email             string
	DisplayName       string
	Title             string
	Username          string
	ProfilePictureURL string
}

type AdminPublication struct {
	WorkspaceID, Name, Dashboard, DefaultPage, Status string
	Origins                                           []string
	History                                           []string
	Generation, PublicURL, EmbedURL, IFrameSnippet    string
	ConfiguredAt, SuspendedAt, DisabledAt, RotatedAt  string
}

type AdminAgentData struct {
	Enabled      bool
	Model        string
	SystemPrompt string
	CanWrite     bool
	CSRFToken    string
	UpdatePath   string
	Tools        []AdminAgentTool
}

type AdminAgentTool struct {
	Name         string
	Description  string
	Effect       string
	Defaults     map[string]any
	InputSchema  map[string]any
	OutputSchema map[string]any
}

type AdminPrincipal struct {
	ID          string
	Kind        string
	Email       string
	DisplayName string
	DisabledAt  string
	CreatedAt   string
	UpdatedAt   string
	DirectRoles []string
	Groups      []AdminGroupRef
}

type AdminGroupRef struct {
	ID         string
	Name       string
	ExternalID string
}

type AdminGroup struct {
	ID         string
	Name       string
	Provider   string
	ExternalID string
	CreatedAt  string
	Roles      []string
	Members    []AdminPrincipalRef
}

type AdminQueryEvent struct {
	ID               string
	WorkspaceID      string
	PrincipalID      string
	Surface          string
	Operation        string
	QueryKind        string
	ModelID          string
	Target           string
	ObjectType       string
	ObjectID         string
	RequestID        string
	CorrelationID    string
	Status           string
	DurationMS       int64
	PlanningMS       int64
	ConnectionWaitMS int64
	DatabaseMS       int64
	RowsReturned     int
	Error            string
	SQL              string
	PlanText         string
	QueryJSON        string
	CreatedAt        string
}

type AdminQueryHistoryData struct {
	Events      []AdminQueryEvent
	FilterMenus []uisignals.FilterMenuSignal
	Filters     uisignals.AdminQueryHistoryFilters
	NextCursor  string
	HasMore     bool
	Limit       int
	Error       string
}

type AdminPrincipalRef struct {
	ID          string
	Email       string
	DisplayName string
}

type AdminStorageData = adminview.AdminStorageData
type AdminStorageDatabase = adminview.AdminStorageDatabase
type AdminStorageTable = adminview.AdminStorageTable
type AdminStorageColumn = adminview.AdminStorageColumn
type AdminStorageFile = adminview.AdminStorageFile
type AdminStorageTableHistory = adminview.AdminStorageTableHistory
type AdminStorageSnapshot = adminview.AdminStorageSnapshot
type AdminStorageServingState = adminview.AdminStorageServingState
type AdminStorageSignal = uisignals.AdminStorageSignal
type AdminStorageSummary = uisignals.AdminStorageSummary
type AdminStorageTableSignal = uisignals.AdminStorageTableSignal
type AdminStorageColumnSignal = uisignals.AdminStorageColumnSignal
type AdminStorageFileSignal = uisignals.AdminStorageFileSignal
type AdminStorageTableHistorySignal = uisignals.AdminStorageTableHistorySignal
type AdminStorageSnapshotSignal = uisignals.AdminStorageSnapshotSignal
type AdminStorageServingStateSignal = uisignals.AdminStorageServingStateSignal
type AdminStorageCommand = uisignals.AdminStorageCommand
type adminRecordTable = uisignals.RecordTableSignal
type adminRecordTableColumn = uisignals.RecordTableColumnSignal

func AdminPage(active string, data AdminData, providers ...webpage.Provider) g.Node {
	active = normalizeAdminSection(active)
	title := adminPageTitle(active)
	layout := webpage.Resolve(firstProvider(providers), adminLayoutContext(active))
	adminUpdatesURL := updatesURL(uisignals.RouteAdmin, "section", active)
	if active == "principals" || active == "groups" {
		adminUpdatesURL = updatesURL(uisignals.RouteAdmin, "section", active, "q", data.ListQuery, "filter", data.ListFilter)
	}
	if active == "principal-detail" && data.SelectedPrincipal != nil {
		adminUpdatesURL = updatesURL(uisignals.RouteAdmin, "section", active, "principal", data.SelectedPrincipal.ID)
	}
	if active == "group-detail" && data.SelectedGroup != nil {
		adminUpdatesURL = updatesURL(uisignals.RouteAdmin, "section", active, "group", data.SelectedGroup.ID)
	}
	adminAttrs := []g.Node{
		g.Attr("slot", "page"),
		g.Attr("section", active),
	}
	if active == "storage" {
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-storage-table-select", "$adminStorageCommand = evt.detail; "+uiactions.EventPost("/admin/storage/select-table")),
		)
	}
	if active == "agent" {
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-agent-system-prompt-save", "$adminAgentCommand = evt.detail; "+uiactions.UncontractedMutationPatch("/admin/agent/config")),
		)
	}
	if active == "queries" {
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-query-history-command", "$adminQueryHistoryCommand = evt.detail; evt.detail.action == 'select_detail' ? ($adminQueryDetail = {eventId: evt.detail.eventId, loading: true, error: ''}) : evt.detail.action == 'close_detail' ? ($adminQueryDetail = {eventId: '', loading: false, error: ''}) : ($adminQueryHistory.loading = true, $adminQueryHistory.error = ''); "+uiactions.QueryPost("/admin/queries/command")),
		)
	}
	if active == "publications" {
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-publication-command", "$adminPublicationCommand = evt.detail; "+uiactions.CommandPostSwitch("evt.detail.action", data.PublicationCommands, "/admin/publications/command", "adminPublicationCommand")),
		)
	}
	if active == "principals" || active == "groups" {
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-entity-list-query__debounce.200ms", "$entityListQuery = evt.detail.query; $entityListFilter = evt.detail.filter; "+uiactions.QueryPost("/admin/"+active+"/search", "entityListQuery", "entityListFilter")),
		)
	}
	return webpage.Render(layout, webpage.Spec{
		Title: "Admin - " + title, CSRFToken: data.CSRFToken,
		Stylesheets: []string{"/static/admin-page.css"}, Scripts: []string{"/static/admin-page.js"},
		UpdatesURL: adminUpdatesURL,
		Content:    g.El("lv-admin-page", adminAttrs...),
	})
}

func AdminBootstrapSignals(active string, data AdminData, providers ...webpage.Provider) map[string]any {
	active = normalizeAdminSection(active)
	page := adminPageSignal(active, data)
	signals := map[string]any{
		"page":    page,
		"runtime": uisignals.RouteRuntimeSignal{Kind: uisignals.RouteAdmin},
		"status":  dashboard.Status{},
	}
	if active == "agent" {
		signals["adminAgentCommand"] = map[string]string{"systemPrompt": data.Agent.SystemPrompt}
	}
	if active == "storage" {
		signals["adminStorage"] = page.Storage
		signals["adminStorageCommand"] = AdminStorageCommand{}
	}
	if active == "queries" {
		queryHistory := AdminQueryHistorySignalFromData(data.QueryHistory)
		signals["adminQueryHistory"] = queryHistory
		signals["adminQueryDetail"] = uisignals.AdminQueryDetailSignal{}
		signals["adminQueryHistoryCommand"] = uisignals.AdminQueryHistoryCommand{Action: "load_more", Filters: queryHistory.Filters, PageToken: uisignals.Optional(queryHistory.NextCursor), Limit: uisignals.Pointer(queryHistory.Limit)}
	}
	if active == "publications" {
		signals["adminPublicationCommand"] = uisignals.AdminPublicationCommand{}
	}
	layout := webpage.Resolve(firstProvider(providers), adminLayoutContext(active))
	return webpage.WithSignal(layout, signals)
}

func AdminListResultsPatch(active string, data AdminData) map[string]any {
	page := adminPageSignal(active, data)
	switch normalizeAdminSection(active) {
	case "principals":
		groups := []uisignals.AdminDirectoryListGroupSignal{}
		if page.DirectoryList != nil {
			groups = page.DirectoryList.Groups
		}
		return map[string]any{"page": map[string]any{
			"directoryList": map[string]any{"groups": groups},
		}}
	case "groups":
		return map[string]any{"page": map[string]any{"sections": uisignals.ValueOrZero(page.Sections)}}
	default:
		return nil
	}
}

func adminLayoutContext(active string) webpage.Context {
	return webpage.Context{
		Active: "admin", SectionTitle: "Workspace", PageTitle: "Published assets",
		PageID: active, Compact: true,
	}
}

func firstProvider(providers []webpage.Provider) webpage.Provider {
	if len(providers) == 0 {
		return nil
	}
	return providers[0]
}

func updatesURL(route uisignals.RouteKind, pairs ...string) string {
	values := url.Values{}
	values.Set("route", string(route))
	for index := 0; index+1 < len(pairs); index += 2 {
		if strings.TrimSpace(pairs[index+1]) != "" {
			values.Set(pairs[index], pairs[index+1])
		}
	}
	return "/updates?" + values.Encode()
}

func adminPageSignal(active string, data AdminData) uisignals.AdminPageSignal {
	active = normalizeAdminSection(active)
	page := uisignals.AdminPageSignal{
		Kind:       uisignals.RouteAdmin,
		Title:      adminPageTitle(active),
		Active:     active,
		ListFilter: uisignals.Optional(data.ListFilter),
		ListQuery:  uisignals.Optional(data.ListQuery),
	}
	switch active {
	case "principals":
		page.HeaderTitle = "Members"
		page.DirectoryList = uisignals.Pointer(adminDirectoryList(data.Principals, data.ListFilter))
	case "profile":
		page.HeaderTitle = "Profile"
		page.Profile = uisignals.Pointer(adminProfileSignal(data.Profile))
	case "principal-detail":
		page.HeaderTitle = "Principals"
		page.HeaderDetail = "Read-only principal access."
		if data.SelectedPrincipal == nil {
			page.Empty = uisignals.Pointer("Principal not found.")
			return page
		}
		principal := *data.SelectedPrincipal
		name := adminDisplayLabel(principal.DisplayName, principal.Email, principal.ID)
		page.HeaderTitle = "Principals / " + name
		page.HeaderDetail = "Read-only principal identity and group memberships."
		page.Metrics = uisignals.OptionalSlice([]uisignals.AdminMetricSignal{
			{Label: "Email", Value: principal.Email},
			{Label: "Principal ID", Value: principal.ID},
			{Label: "Direct roles", Value: strings.Join(principal.DirectRoles, ", ")},
			{Label: "Group count", Value: fmt.Sprint(len(principal.Groups))},
			{Label: "Created", Value: principal.CreatedAt},
			{Label: "Updated", Value: principal.UpdatedAt},
		})
		page.Sections = uisignals.OptionalSlice([]uisignals.AdminContentSectionSignal{{Title: "Groups", Table: uisignals.Pointer(adminPrincipalGroupsGrid(principal, data.Groups))}})
	case "groups":
		page.HeaderTitle = "Groups"
		page.HeaderDetail = "Workspace groups and their read-only membership summaries."
		page.ListFilterOptions = uisignals.OptionalSlice(adminGroupProviders(data.Groups))
		page.Sections = uisignals.OptionalSlice([]uisignals.AdminContentSectionSignal{{Title: "Groups", Table: uisignals.Pointer(adminGroupsGrid(filterAdminGroups(data.Groups, data.ListQuery, data.ListFilter)))}})
	case "group-detail":
		page.HeaderTitle = "Groups"
		page.HeaderDetail = "Read-only group membership."
		if data.SelectedGroup == nil {
			page.Empty = uisignals.Pointer("Group not found.")
			return page
		}
		group := *data.SelectedGroup
		name := adminDisplayLabel(group.Name, group.ExternalID, group.ID)
		page.HeaderTitle = "Groups / " + name
		page.HeaderDetail = "Read-only group membership and role assignments."
		page.Metrics = uisignals.OptionalSlice([]uisignals.AdminMetricSignal{
			{Label: "Provider", Value: group.Provider},
			{Label: "External ID", Value: group.ExternalID},
			{Label: "Group ID", Value: group.ID},
			{Label: "Roles", Value: strings.Join(group.Roles, ", ")},
			{Label: "Member count", Value: fmt.Sprint(len(group.Members))},
		})
		page.Sections = uisignals.OptionalSlice([]uisignals.AdminContentSectionSignal{{Title: "Members", Table: uisignals.Pointer(adminGroupMembersGrid(group, data.Principals))}})
	case "agent":
		page.HeaderTitle = "Agent"
		page.HeaderDetail = "Platform agent prompt and read-only tool inventory."
		page.Agent = uisignals.Pointer(adminAgentSignal(data.Agent))
		page.Metrics = uisignals.OptionalSlice([]uisignals.AdminMetricSignal{
			{Label: "Status", Value: configuredLabel(data.Agent.Enabled)},
			{Label: "Model", Value: data.Agent.Model},
			{Label: "Tools", Value: fmt.Sprint(len(data.Agent.Tools))},
		})
	case "storage":
		page.HeaderTitle = "Storage"
		page.HeaderDetail = "Read-only DuckLake catalog and table metadata."
		page.Storage = uisignals.Pointer(AdminStorageSignalFromData(data.Storage, AdminStorageCommand{}))
		if data.Storage.Status != "" {
			page.Empty = uisignals.Pointer(data.Storage.Status)
		}
		page.Metrics = uisignals.OptionalSlice([]uisignals.AdminMetricSignal{
			{Label: "Catalog path", Value: data.Storage.CatalogPath},
			{Label: "Data path", Value: data.Storage.DataPath},
			{Label: "Snapshots", Value: fmt.Sprint(data.Storage.SnapshotCount)},
			{Label: "Tables", Value: fmt.Sprint(data.Storage.TableCount)},
		})
	case "queries":
		page.HeaderTitle = "Query History"
		page.HeaderDetail = "Product query audit across dashboards, API, agents, and Data Explorer."
	case "publications":
		page.HeaderTitle = "Publications"
		page.HeaderDetail = "Public dashboard URLs, embedding policy, and immediate lifecycle controls. Configuration remains YAML-only."
		page.Publications = uisignals.OptionalSlice(adminPublicationSignals(data.Publications))
		if len(data.Publications) == 0 {
			page.Empty = uisignals.Pointer("No dashboard publications have been configured.")
		}
	default:
		page.HeaderTitle = "Profile"
		page.Profile = uisignals.Pointer(adminProfileSignal(data.Profile))
	}
	return page
}

func AdminQueryHistorySignalFromData(data AdminQueryHistoryData) uisignals.AdminQueryHistorySignal {
	limit := data.Limit
	if limit <= 0 {
		limit = 50
	}
	return uisignals.AdminQueryHistorySignal{
		Table:            adminQueryEventsGrid(data.Events),
		FilterMenus:      uisignals.OptionalSlice(data.FilterMenus),
		Filters:          data.Filters,
		NextCursor:       data.NextCursor,
		LoadedCountLabel: queryHistoryCountLabel(len(data.Events)),
		HasMore:          data.HasMore,
		Loading:          false,
		Error:            data.Error,
		Limit:            int64(limit),
	}
}

func AdminQueryDetailSignalFromEvent(event AdminQueryEvent) uisignals.AdminQueryDetailSignal {
	return uisignals.AdminQueryDetailSignal{
		EventID:          uisignals.Optional(event.ID),
		Loading:          false,
		Error:            uisignals.Optional(event.Error),
		Status:           uisignals.Optional(event.Status),
		StatusLabel:      uisignals.Optional(queryEventStatusLabel(event.Status)),
		WorkspaceID:      uisignals.Optional(event.WorkspaceID),
		PrincipalID:      uisignals.Optional(event.PrincipalID),
		Surface:          uisignals.Optional(event.Surface),
		Operation:        uisignals.Optional(event.Operation),
		QueryKind:        uisignals.Optional(event.QueryKind),
		ModelID:          uisignals.Optional(event.ModelID),
		Target:           uisignals.Optional(event.Target),
		ObjectType:       uisignals.Optional(event.ObjectType),
		ObjectID:         uisignals.Optional(event.ObjectID),
		RequestID:        uisignals.Optional(event.RequestID),
		CorrelationID:    uisignals.Optional(event.CorrelationID),
		DurationMS:       event.DurationMS,
		PlanningMS:       event.PlanningMS,
		ConnectionWaitMS: event.ConnectionWaitMS,
		DatabaseMS:       event.DatabaseMS,
		RowsReturned:     int64(event.RowsReturned),
		QueryError:       uisignals.Optional(event.Error),
		SQL:              uisignals.Optional(event.SQL),
		PlanText:         uisignals.Optional(event.PlanText),
		QueryJSON:        uisignals.Optional(event.QueryJSON),
		CreatedAt:        uisignals.Optional(event.CreatedAt),
	}
}

func queryEventStatusLabel(status string) string {
	switch status {
	case "success":
		return "Success"
	case "canceled":
		return "Canceled"
	case "timeout":
		return "Timeout"
	case "validation_failed":
		return "Validation failed"
	case "":
		return "Unknown"
	default:
		return status
	}
}

func queryHistoryCountLabel(count int) string {
	if count == 1 {
		return "1 query loaded"
	}
	return fmt.Sprintf("%d queries loaded", count)
}

func adminQueryEventSignals(events []AdminQueryEvent) []uisignals.AdminQueryEventSignal {
	out := make([]uisignals.AdminQueryEventSignal, 0, len(events))
	for _, event := range events {
		out = append(out, uisignals.AdminQueryEventSignal{
			ID:               event.ID,
			WorkspaceID:      event.WorkspaceID,
			PrincipalID:      event.PrincipalID,
			Surface:          event.Surface,
			Operation:        event.Operation,
			QueryKind:        event.QueryKind,
			ModelID:          event.ModelID,
			Target:           event.Target,
			ObjectType:       event.ObjectType,
			ObjectID:         event.ObjectID,
			RequestID:        event.RequestID,
			CorrelationID:    event.CorrelationID,
			Status:           event.Status,
			DurationMS:       event.DurationMS,
			PlanningMS:       event.PlanningMS,
			ConnectionWaitMS: event.ConnectionWaitMS,
			DatabaseMS:       event.DatabaseMS,
			RowsReturned:     int64(event.RowsReturned),
			Error:            event.Error,
			SQL:              event.SQL,
			PlanText:         event.PlanText,
			QueryJSON:        event.QueryJSON,
			CreatedAt:        event.CreatedAt,
		})
	}
	return out
}

func adminPublicationSignals(rows []AdminPublication) []uisignals.AdminPublicationSignal {
	out := make([]uisignals.AdminPublicationSignal, 0, len(rows))
	for _, row := range rows {
		out = append(out, uisignals.AdminPublicationSignal{
			WorkspaceID: row.WorkspaceID, Name: row.Name, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage,
			Status: row.Status, Origins: row.Origins, Generation: uisignals.Optional(row.Generation), PublicURL: row.PublicURL,
			EmbedURL: row.EmbedURL, IframeSnippet: row.IFrameSnippet, ConfiguredAt: uisignals.Optional(row.ConfiguredAt),
			SuspendedAt: uisignals.Optional(row.SuspendedAt), DisabledAt: uisignals.Optional(row.DisabledAt), RotatedAt: uisignals.Optional(row.RotatedAt),
			History: append([]string(nil), row.History...),
		})
	}
	return out
}

func adminAgentSignal(data AdminAgentData) uisignals.AdminAgentSignal {
	tools := make([]uisignals.AdminAgentToolSignal, 0, len(data.Tools))
	for _, tool := range data.Tools {
		tools = append(tools, uisignals.AdminAgentToolSignal{
			Name:         tool.Name,
			Description:  tool.Description,
			Effect:       tool.Effect,
			Defaults:     tool.Defaults,
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
		})
	}
	return uisignals.AdminAgentSignal{
		Enabled:      data.Enabled,
		Model:        uisignals.Optional(data.Model),
		SystemPrompt: data.SystemPrompt,
		CanWrite:     data.CanWrite,
		UpdatePath:   data.UpdatePath,
		Tools:        tools,
	}
}

func adminProfileSignal(data AdminProfile) uisignals.AdminProfileSignal {
	return uisignals.AdminProfileSignal{
		ID:                data.ID,
		Email:             data.Email,
		DisplayName:       data.DisplayName,
		Title:             data.Title,
		Username:          data.Username,
		ProfilePictureURL: uisignals.Optional(data.ProfilePictureURL),
	}
}

func adminPrincipalsGrid(principals []AdminPrincipal) adminRecordTable {
	rows := make([]map[string]any, 0, len(principals))
	for _, principal := range principals {
		rows = append(rows, map[string]any{
			"name":        adminDisplayLabel(principal.DisplayName, principal.Email, principal.ID),
			"name_href":   adminPrincipalHref(principal.ID),
			"email":       principal.Email,
			"id":          principal.ID,
			"roles":       principal.DirectRoles,
			"group_count": len(principal.Groups),
			"updated_at":  principal.UpdatedAt,
		})
	}
	return adminRecordTable{
		Columns: []adminRecordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("name_href"), Width: uisignals.Pointer("150px")},
			{ID: "email", Header: "Email", Width: uisignals.Pointer("190px")},
			{ID: "roles", Header: "Direct roles", Kind: uisignals.Pointer("tags"), Width: uisignals.Pointer("135px")},
			{ID: "group_count", Header: "Group count", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("120px")},
			{ID: "id", Header: "Principal ID", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("190px")},
			{ID: "updated_at", Header: "Updated", Width: uisignals.Pointer("150px")},
		},
		Rows:     rows,
		Empty:    "No principals found.",
		MinWidth: uisignals.Pointer("935px"),
	}
}

func adminDirectoryList(principals []AdminPrincipal, filter string) uisignals.AdminDirectoryListSignal {
	groupOrder := []struct {
		id    string
		label string
	}{
		{id: "active", label: "Active"},
		{id: "application", label: "Application"},
		{id: "inactive", label: "Inactive"},
	}
	itemsByGroup := make(map[string][]uisignals.AdminDirectoryListItemSignal, len(groupOrder))
	for _, principal := range principals {
		groupID := "active"
		kind := "person"
		status := "active"
		role := firstRoleLabel(principal.DirectRoles)
		if strings.TrimSpace(principal.DisabledAt) != "" {
			groupID = "inactive"
			status = "inactive"
			role = "Suspended"
		} else if principal.Kind == "service_principal" || principal.Kind == "dashboard_publication" {
			groupID = "application"
			kind = "application"
			role = "Application"
		}
		if role == "" {
			role = "Member"
		}
		if !adminDirectoryFilterMatches(filter, groupID, kind) {
			continue
		}
		itemsByGroup[groupID] = append(itemsByGroup[groupID], uisignals.AdminDirectoryListItemSignal{
			ID:         principal.ID,
			Name:       adminDisplayLabel(principal.DisplayName, principal.Email, principal.ID),
			Username:   adminPrincipalUsername(principal),
			Email:      principal.Email,
			Href:       adminPrincipalHref(principal.ID),
			Kind:       kind,
			Status:     status,
			Role:       role,
			GroupCount: int64(len(principal.Groups)),
			JoinedAt:   principal.CreatedAt,
		})
	}

	groups := make([]uisignals.AdminDirectoryListGroupSignal, 0, len(groupOrder))
	for _, group := range groupOrder {
		items := itemsByGroup[group.id]
		if len(items) == 0 {
			continue
		}
		groups = append(groups, uisignals.AdminDirectoryListGroupSignal{ID: group.id, Label: group.label, Items: items})
	}
	return uisignals.AdminDirectoryListSignal{
		SearchPlaceholder: "Search by name or email",
		FilterLabel:       "Filter members",
		Groups:            groups,
	}
}

func adminDirectoryFilterMatches(filter, groupID, kind string) bool {
	switch strings.ToLower(strings.TrimSpace(filter)) {
	case "people":
		return groupID == "active" && kind == "person"
	case "applications":
		return groupID == "application"
	case "inactive":
		return groupID == "inactive"
	default:
		return true
	}
}

func filterAdminGroups(groups []AdminGroup, query, filter string) []AdminGroup {
	query = strings.ToLower(strings.TrimSpace(query))
	filter = strings.ToLower(strings.TrimSpace(filter))
	filtered := make([]AdminGroup, 0, len(groups))
	for _, group := range groups {
		if filter != "" && filter != "all" && strings.ToLower(strings.TrimSpace(group.Provider)) != filter {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{group.Name, group.ID, group.Provider, group.ExternalID, strings.Join(group.Roles, " ")}, " "))
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		filtered = append(filtered, group)
	}
	return filtered
}

func adminGroupProviders(groups []AdminGroup) []string {
	seen := make(map[string]struct{}, len(groups))
	providers := make([]string, 0, len(groups))
	for _, group := range groups {
		provider := strings.ToLower(strings.TrimSpace(group.Provider))
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

func firstRoleLabel(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	role := strings.TrimSpace(roles[0])
	if role == "" {
		return ""
	}
	return strings.ToUpper(role[:1]) + role[1:]
}

func adminPrincipalUsername(principal AdminPrincipal) string {
	if email := strings.TrimSpace(principal.Email); email != "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			return email[:at]
		}
	}
	return strings.TrimSpace(principal.ID)
}

func adminPrincipalGroupsGrid(principal AdminPrincipal, groups []AdminGroup) adminRecordTable {
	groupsByID := make(map[string]AdminGroup, len(groups))
	for _, group := range groups {
		groupsByID[group.ID] = group
	}
	rows := make([]map[string]any, 0, len(principal.Groups))
	for _, ref := range principal.Groups {
		group := groupsByID[ref.ID]
		rows = append(rows, map[string]any{
			"name":         adminDisplayLabel(group.Name, ref.Name, group.ExternalID, ref.ExternalID, ref.ID),
			"name_href":    adminGroupHref(ref.ID),
			"provider":     group.Provider,
			"external_id":  adminDisplayLabel(group.ExternalID, ref.ExternalID),
			"roles":        group.Roles,
			"member_count": len(group.Members),
		})
	}
	return adminRecordTable{
		Columns: []adminRecordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("name_href"), Width: uisignals.Pointer("180px")},
			{ID: "provider", Header: "Provider", Width: uisignals.Pointer("120px")},
			{ID: "external_id", Header: "External ID", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("180px")},
			{ID: "roles", Header: "Roles", Kind: uisignals.Pointer("tags"), Width: uisignals.Pointer("160px")},
			{ID: "member_count", Header: "Member count", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("130px")},
		},
		Rows:     rows,
		Empty:    "No groups found.",
		MinWidth: uisignals.Pointer("800px"),
	}
}

func adminGroupsGrid(groups []AdminGroup) adminRecordTable {
	rows := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		rows = append(rows, map[string]any{
			"name":         adminDisplayLabel(group.Name, group.ExternalID, group.ID),
			"name_href":    adminGroupHref(group.ID),
			"provider":     group.Provider,
			"external_id":  group.ExternalID,
			"id":           group.ID,
			"roles":        group.Roles,
			"member_count": len(group.Members),
		})
	}
	return adminRecordTable{
		Columns: []adminRecordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("name_href"), Width: uisignals.Pointer("180px")},
			{ID: "provider", Header: "Provider", Width: uisignals.Pointer("120px")},
			{ID: "external_id", Header: "External ID", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("180px")},
			{ID: "roles", Header: "Roles", Kind: uisignals.Pointer("tags"), Width: uisignals.Pointer("180px")},
			{ID: "member_count", Header: "Member count", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("130px")},
			{ID: "id", Header: "Group ID", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
		},
		Rows:     rows,
		Empty:    "No groups found.",
		MinWidth: uisignals.Pointer("1010px"),
	}
}

func adminGroupMembersGrid(group AdminGroup, principals []AdminPrincipal) adminRecordTable {
	principalsByID := make(map[string]AdminPrincipal, len(principals))
	for _, principal := range principals {
		principalsByID[principal.ID] = principal
	}
	rows := make([]map[string]any, 0, len(group.Members))
	for _, member := range group.Members {
		principal := principalsByID[member.ID]
		rows = append(rows, map[string]any{
			"name":         adminDisplayLabel(member.DisplayName, principal.DisplayName, member.Email, principal.Email, member.ID),
			"email":        adminDisplayLabel(member.Email, principal.Email),
			"id":           member.ID,
			"direct_roles": principal.DirectRoles,
			"updated_at":   principal.UpdatedAt,
		})
	}
	return adminRecordTable{
		Columns: []adminRecordTableColumn{
			{ID: "name", Header: "Name", Width: uisignals.Pointer("150px")},
			{ID: "email", Header: "Email", Width: uisignals.Pointer("190px")},
			{ID: "id", Header: "Principal ID", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("180px")},
			{ID: "direct_roles", Header: "Direct roles", Kind: uisignals.Pointer("tags"), Width: uisignals.Pointer("130px")},
			{ID: "updated_at", Header: "Updated", Width: uisignals.Pointer("150px")},
		},
		Rows:     rows,
		Empty:    "No members found.",
		MinWidth: uisignals.Pointer("840px"),
	}
}

func adminQueryEventsGrid(events []AdminQueryEvent) adminRecordTable {
	rows := make([]map[string]any, 0, len(events))
	for _, event := range events {
		rows = append(rows, map[string]any{
			"id": event.ID,
			"query": map[string]any{
				"label":           queryEventStatement(event),
				"statusLabel":     event.Status,
				"tone":            queryEventStatusTone(event.Status),
				"icon":            queryEventStatusIcon(event.Status),
				"expandedContent": queryEventExpandedContent(event),
			},
			"started_at":     event.CreatedAt,
			"duration_ms":    map[string]any{"label": fmt.Sprintf("%d ms", event.DurationMS), "value": event.DurationMS},
			"source":         event.Surface,
			"runtime":        queryEventRuntimeLabel(event),
			"principal_id":   event.PrincipalID,
			"rows_returned":  event.RowsReturned,
			"operation":      event.Operation,
			"kind":           event.QueryKind,
			"model":          event.ModelID,
			"target":         event.Target,
			"object":         queryEventObjectLabel(event),
			"request_id":     event.RequestID,
			"correlation_id": event.CorrelationID,
			"error":          event.Error,
		})
	}
	falseValue := false
	return adminRecordTable{
		Columns: []adminRecordTableColumn{
			{ID: "query", Header: "Query", Kind: uisignals.Pointer("query"), Width: uisignals.Pointer("560px"), Toggleable: &falseValue},
			{ID: "started_at", Header: "Started", Width: uisignals.Pointer("150px")},
			{ID: "duration_ms", Header: "Duration", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("105px")},
			{ID: "source", Header: "Source type", Width: uisignals.Pointer("120px")},
			{ID: "runtime", Header: "Runtime", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("130px")},
			{ID: "principal_id", Header: "User", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "rows_returned", Header: "Rows", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("90px")},
			{ID: "operation", Header: "Operation", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("145px")},
			{ID: "kind", Header: "Kind", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("170px")},
			{ID: "model", Header: "Model", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("130px")},
			{ID: "target", Header: "Target", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "object", Header: "Object", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
			{ID: "request_id", Header: "Request ID", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("170px")},
			{ID: "correlation_id", Header: "Correlation ID", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("170px")},
			{ID: "error", Header: "Error", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
		},
		Rows:      rows,
		Empty:     "No query events match these filters.",
		MinWidth:  uisignals.Pointer("1305px"),
		Density:   uisignals.Pointer("tight"),
		RowAction: uisignals.Pointer("detail"),
		ColumnSelector: &uisignals.RecordTableColumnSelector{
			Enabled:        true,
			Label:          uisignals.Pointer("Columns"),
			DefaultColumns: uisignals.Pointer([]string{"started_at", "duration_ms", "source", "runtime", "principal_id", "rows_returned"}),
		},
	}
}

func queryEventStatement(event AdminQueryEvent) string {
	sql := collapseWhitespace(event.SQL)
	if sql != "" {
		return sql
	}
	parts := []string{event.Operation, event.QueryKind, strings.Join(nonEmptyStrings(event.ModelID, event.Target), ".")}
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		if label := collapseWhitespace(part); label != "" {
			labels = append(labels, label)
		}
	}
	if len(labels) > 0 {
		return strings.Join(labels, " · ")
	}
	return event.ID
}

func queryEventExpandedContent(event AdminQueryEvent) string {
	if event.SQL != "" {
		return event.SQL
	}
	return queryEventStatement(event)
}

func queryEventObjectLabel(event AdminQueryEvent) string {
	object := strings.Join(nonEmptyStrings(event.ObjectType, event.ObjectID), ":")
	if object != "" {
		return object
	}
	object = strings.Join(nonEmptyStrings(event.ModelID, event.Target), ":")
	if object != "" {
		return object
	}
	return "-"
}

func queryEventRuntimeLabel(event AdminQueryEvent) string {
	if event.WorkspaceID == "" {
		return "-"
	}
	return event.WorkspaceID
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func queryEventStatusTone(status string) string {
	switch status {
	case "success":
		return "success"
	case "canceled":
		return "muted"
	case "timeout":
		return "attention"
	default:
		return "danger"
	}
}

func queryEventStatusIcon(status string) string {
	switch status {
	case "success":
		return "check"
	case "canceled", "timeout":
		return "clock"
	default:
		return "x"
	}
}

func adminQueryMetrics(events []AdminQueryEvent) []uisignals.AdminMetricSignal {
	failures := 0
	totalDuration := int64(0)
	for _, event := range events {
		if event.Status != "success" {
			failures++
		}
		totalDuration += event.DurationMS
	}
	avg := int64(0)
	if len(events) > 0 {
		avg = totalDuration / int64(len(events))
	}
	return []uisignals.AdminMetricSignal{
		{Label: "Recent events", Value: fmt.Sprint(len(events))},
		{Label: "Failures", Value: fmt.Sprint(failures)},
		{Label: "Average duration", Value: fmt.Sprintf("%d ms", avg)},
	}
}

func adminGroupHref(groupID string) string {
	return "/admin/groups/" + url.PathEscape(groupID)
}

func adminPrincipalHref(principalID string) string {
	return "/admin/principals/" + url.PathEscape(principalID)
}

func adminPageTitle(active string) string {
	switch active {
	case "principals":
		return "Principals"
	case "profile":
		return "Profile"
	case "principal-detail":
		return "Principal"
	case "groups":
		return "Groups"
	case "group-detail":
		return "Group"
	case "agent":
		return "Agent"
	case "storage":
		return "Storage"
	case "queries":
		return "Query History"
	case "publications":
		return "Publications"
	default:
		return "Profile"
	}
}

func normalizeAdminSection(active string) string {
	switch strings.TrimSpace(active) {
	case "profile", "principals", "principal-detail", "groups", "group-detail", "agent", "storage", "queries", "publications":
		return strings.TrimSpace(active)
	default:
		return "profile"
	}
}

func AdminStorageSignalFromData(data AdminStorageData, command AdminStorageCommand) AdminStorageSignal {
	tables := make([]AdminStorageTableSignal, 0, len(data.Tables))
	var selected *AdminStorageTableSignal
	for _, table := range data.Tables {
		signalTable := AdminStorageTableSignalFromTable(table)
		tables = append(tables, signalTable)
		if selected == nil && adminStorageCommandMatches(command, table) {
			copy := signalTable
			selected = &copy
		}
	}
	if selected == nil && len(tables) > 0 {
		copy := tables[0]
		selected = &copy
	}
	selectedKey := ""
	if selected != nil {
		selectedKey = selected.Key
	}
	return AdminStorageSignal{
		Summary: AdminStorageSummary{
			CatalogPath:        data.CatalogPath,
			DataPath:           data.DataPath,
			CatalogSizeLabel:   data.CatalogSizeLabel,
			DataSizeLabel:      data.DataSizeLabel,
			TotalSizeLabel:     data.TotalSizeLabel,
			TotalDataSizeLabel: data.TotalDataSizeLabel,
			DatabaseCount:      int64(data.DatabaseCount),
			TableCount:         int64(data.TableCount),
			SnapshotCount:      int64(data.SnapshotCount),
			DataFileCount:      int64(data.DataFileCount),
		},
		Status:        data.Status,
		Warnings:      data.Warnings,
		Tables:        tables,
		Snapshots:     adminStorageSnapshotSignals(data.Snapshots),
		ServingStates: adminStorageServingStateSignals(data.ServingStates),
		SelectedKey:   selectedKey,
		SelectedTable: selected,
	}
}

func AdminStorageTableSignalFromTable(table AdminStorageTable) AdminStorageTableSignal {
	columns := make([]AdminStorageColumnSignal, 0, len(table.Columns))
	for _, column := range table.Columns {
		columns = append(columns, AdminStorageColumnSignal{
			ID:                  column.ID,
			Name:                column.Name,
			Type:                column.Type,
			Ordinal:             int64(column.Ordinal),
			Nullable:            column.Nullable,
			Default:             column.Default,
			InitialDefault:      column.InitialDefault,
			DefaultValueType:    column.DefaultValueType,
			DefaultValueDialect: column.DefaultValueDialect,
			BeginSnapshot:       column.BeginSnapshot,
			ContainsNull:        column.ContainsNull,
			ContainsNaN:         column.ContainsNaN,
			MinValue:            column.MinValue,
			MaxValue:            column.MaxValue,
			ExtraStats:          column.ExtraStats,
		})
	}
	files := make([]AdminStorageFileSignal, 0, len(table.Files))
	for _, file := range table.Files {
		files = append(files, AdminStorageFileSignal{
			ID:               file.ID,
			Path:             file.Path,
			Format:           file.Format,
			RecordCount:      file.RecordCount,
			RecordCountLabel: file.RecordCountLabel,
			SizeBytes:        file.SizeBytes,
			SizeLabel:        file.SizeLabel,
			BeginSnapshot:    file.BeginSnapshot,
			EndSnapshot:      file.EndSnapshot,
		})
	}
	history := make([]AdminStorageTableHistorySignal, 0, len(table.History))
	for _, event := range table.History {
		history = append(history, AdminStorageTableHistorySignal{
			SnapshotID:    event.SnapshotID,
			Time:          event.Time,
			SchemaVersion: event.SchemaVersion,
			Source:        event.Source,
			Changes:       event.Changes,
			Author:        event.Author,
			Message:       event.Message,
			ExtraInfo:     event.ExtraInfo,
		})
	}
	return AdminStorageTableSignal{
		Key:           AdminStorageTableKey(table.DatabaseID, table.Schema, table.Name),
		DatabaseID:    table.DatabaseID,
		DatabaseName:  table.DatabaseName,
		DatabasePath:  table.DatabasePath,
		ModelID:       table.ModelID,
		ModelName:     table.ModelName,
		Schema:        table.Schema,
		Name:          table.Name,
		Type:          table.Type,
		TableID:       table.TableID,
		TableUUID:     table.TableUUID,
		DuckLakePath:  table.DuckLakePath,
		BeginSnapshot: table.BeginSnapshot,
		EndSnapshot:   table.EndSnapshot,
		RowCount:      table.RowCount,
		RowCountLabel: table.RowCountLabel,
		ColumnCount:   int64(table.ColumnCount),
		FileCount:     int64(table.FileCount),
		SizeBytes:     table.SizeBytes,
		SizeLabel:     table.SizeLabel,
		Columns:       uisignals.OptionalSlice(columns),
		Files:         uisignals.OptionalSlice(files),
		History:       uisignals.OptionalSlice(history),
		ServingStates: uisignals.OptionalSlice(adminStorageServingStateSignals(table.ServingStates)),
	}
}

func adminStorageSnapshotSignals(snapshots []AdminStorageSnapshot) []AdminStorageSnapshotSignal {
	out := make([]AdminStorageSnapshotSignal, 0, len(snapshots))
	for _, snapshot := range snapshots {
		out = append(out, AdminStorageSnapshotSignal{
			ID:                snapshot.ID,
			Time:              snapshot.Time,
			SchemaVersion:     snapshot.SchemaVersion,
			Author:            snapshot.Author,
			Message:           snapshot.Message,
			Changes:           snapshot.Changes,
			ExtraInfo:         snapshot.ExtraInfo,
			Protected:         snapshot.Protected,
			ServingStateCount: int64(snapshot.ServingStateCount),
		})
	}
	return out
}

func adminStorageServingStateSignals(servingStates []AdminStorageServingState) []AdminStorageServingStateSignal {
	out := make([]AdminStorageServingStateSignal, 0, len(servingStates))
	for _, servingState := range servingStates {
		out = append(out, AdminStorageServingStateSignal{
			WorkspaceID:    servingState.WorkspaceID,
			Environment:    servingState.Environment,
			ServingStateID: servingState.ServingStateID,
			Status:         servingState.Status,
			SnapshotID:     servingState.SnapshotID,
			Digest:         servingState.Digest,
			Active:         servingState.Active,
			ActivatedAt:    servingState.ActivatedAt,
		})
	}
	return out
}

func AdminStorageTableKey(databaseID, schemaName, tableName string) string {
	return databaseID + "\x00" + schemaName + "\x00" + tableName
}

func adminStorageCommandMatches(command AdminStorageCommand, table AdminStorageTable) bool {
	return command.DatabaseID == table.DatabaseID && command.Schema == table.Schema && command.Table == table.Name
}

func configuredLabel(configured bool) string {
	if configured {
		return "Configured"
	}
	return "Not configured"
}

func adminDisplayLabel(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "-"
}
