package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/admin/personalsettings"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	adminview "github.com/flidai/leapview/internal/admin/view"
	"github.com/flidai/leapview/internal/dashboard"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	g "maragu.dev/gomponents"
)

type AdminData struct {
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
	AgentConfigCommand    uicommand.Binding
	PublicationCommands   map[string]uicommand.Binding
	ProductCommands       map[string]uicommand.Binding
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
	ProjectID, Name, Dashboard, DefaultPage, Status  string
	Origins                                          []string
	History                                          []string
	Generation, PublicURL, EmbedURL, IFrameSnippet   string
	ConfiguredAt, SuspendedAt, DisabledAt, RotatedAt string
}

type AdminAgentData struct {
	Enabled      bool
	Model        string
	SystemPrompt string
	Revision     string
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
	ID                string
	Kind              string
	Email             string
	DisplayName       string
	ProfilePictureURL string
	DisabledAt        string
	CreatedAt         string
	UpdatedAt         string
	LastSeenAt        string
	DirectRoles       []string
	Groups            []AdminGroupRef
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
	ProjectID        string
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
type AdminStorageTable = adminview.AdminStorageTable
type AdminStorageFile = adminview.AdminStorageFile
type AdminStorageSignal = uisignals.AdminStorageSignal
type AdminStorageSummary = uisignals.AdminStorageSummary
type AdminStorageTableSignal = uisignals.AdminStorageTableSignal
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
	if active == "storage-detail" && len(data.Storage.Tables) == 1 {
		table := data.Storage.Tables[0]
		adminUpdatesURL = updatesURL(uisignals.RouteAdmin, "section", active, "schema", table.Schema, "table", table.Name)
	}
	adminAttrs := []g.Node{
		g.Attr("slot", "page"),
		g.Attr("section", active),
	}
	if active == "profile" || active == "security" || active == "api-tokens" {
		adminAttrs = append(adminAttrs, personalsettings.CommandAttributes("/admin/personal-settings/command?section="+url.QueryEscape(active))...)
	}
	if active == "general" || active == "authentication" || active == "system" {
		productCommands := map[string]uicommand.Binding{
			"save_display_name": data.ProductCommands["update_identity"],
			"remove_logo":       data.ProductCommands["delete_logo"],
			"reset_identity":    data.ProductCommands["reset_identity"],
		}
		productMutation := uiactions.CommandPostSwitchWithRevision(
			"evt.detail.action", productCommands, "/admin/product-settings/command?section="+url.QueryEscape(active),
			`'"product-' + $productSettings.general.revision + '"'`, "productSettingsCommand",
		)
		productRefresh := uiactions.QueryPost("/admin/product-settings/command?section="+url.QueryEscape(active), "productSettingsCommand")
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-product-settings-command", "$productSettingsCommand = evt.detail; evt.detail.action == 'refresh' ? ("+productRefresh+") : ("+productMutation+")"),
		)
	}
	if active == "service-accounts" {
		serviceAccountCommands := map[string]uicommand.Binding{
			"create":        accessgen.GenUIActionCreateServicePrincipal(),
			"delete":        accessgen.GenUIActionDeleteServicePrincipal(),
			"create_secret": accessgen.GenUIActionCreateServicePrincipalSecret(),
			"revoke_secret": accessgen.GenUIActionRevokeServicePrincipalSecret(),
		}
		serviceAccountMutation := uiactions.CommandPostSwitch("evt.detail.action", serviceAccountCommands, "/admin/service-accounts/command", "adminServiceAccountCommand")
		serviceAccountSelect := uiactions.QueryPost("/admin/service-accounts/command", "adminServiceAccountCommand")
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-service-account-command", "$adminServiceAccountCommand = evt.detail; evt.detail.action == 'select' ? ("+serviceAccountSelect+") : ("+serviceAccountMutation+")"),
		)
	}
	if active == "principals" || active == "groups" || active == "principal-detail" || active == "group-detail" {
		accessCommands := map[string]uicommand.Binding{
			"create_principal":    accessgen.GenUIActionCreatePrincipal(),
			"update_principal":    accessgen.GenUIActionUpdatePrincipal(),
			"delete_principal":    accessgen.GenUIActionDeletePrincipal(),
			"block_principal":     accessgen.GenUIActionDisablePrincipal(),
			"unblock_principal":   accessgen.GenUIActionEnablePrincipal(),
			"reset_password":      accessgen.GenUIActionResetPrincipalPassword(),
			"revoke_session":      accessgen.GenUIActionRevokePrincipalSession(),
			"revoke_all_sessions": accessgen.GenUIActionRevokePrincipalSession(),
			"create_group":        accessgen.GenUIActionCreateGroup(),
			"update_group":        accessgen.GenUIActionUpdateGroup(),
			"delete_group":        accessgen.GenUIActionDeleteGroup(),
			"add_group_member":    accessgen.GenUIActionAddGroupMember(),
			"remove_group_member": accessgen.GenUIActionRemoveGroupMember(),
		}
		commandQuery := url.Values{"section": []string{active}}
		if data.SelectedPrincipal != nil {
			commandQuery.Set("principal", data.SelectedPrincipal.ID)
		}
		if data.SelectedGroup != nil {
			commandQuery.Set("group", data.SelectedGroup.ID)
		}
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-access-admin-command", "$adminAccessCommand = evt.detail; $adminAccess.loading = true; $adminAccess.error = ''; "+uiactions.CommandPostSwitch("evt.detail.action", accessCommands, "/admin/access/command?"+commandQuery.Encode(), "adminAccessCommand")),
		)
	}
	if active == "audit" {
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-audit-log-command", "$adminAuditLogCommand = evt.detail; "+uiactions.QueryPost("/admin/audit/command", "adminAuditLogCommand", "adminAuditLog")),
		)
	}
	if active == "agent" {
		adminAttrs = append(adminAttrs,
			g.Attr("data-on:lv-agent-system-prompt-save", "$adminAgentCommand = evt.detail; "+uiactions.CommandPatch(data.AgentConfigCommand, "/admin/agent/config", data.Agent.Revision)),
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
			g.Attr("data-on:lv-entity-list-query__debounce.200ms", "$entityListQuery = evt.detail.query; $entityListFilter = evt.detail.filter; "+uiactions.Get("/admin/"+active+"/search", "entityListQuery", "entityListFilter")),
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
		items := []uisignals.AdminDirectoryListItemSignal{}
		if page.DirectoryList != nil {
			items = page.DirectoryList.Items
		}
		return map[string]any{"page": map[string]any{
			"directoryList": map[string]any{"items": items},
		}}
	case "groups":
		return map[string]any{"page": map[string]any{"sections": uisignals.ValueOrZero(page.Sections)}}
	default:
		return nil
	}
}

func adminLayoutContext(active string) webpage.Context {
	pageID := active
	if active == "storage-detail" {
		pageID = "storage"
	}
	return webpage.Context{
		Active: "admin", SectionTitle: "Project", PageTitle: "Published assets",
		PageID: pageID, Compact: true,
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
		page.HeaderTitle = "Principals"
		page.HeaderDetail = "Manage users and their account status."
		page.DirectoryList = uisignals.Pointer(adminDirectoryList(data.Principals, data.ListFilter))
	case "profile":
		page.HeaderTitle = "Profile"
		page.HeaderDetail = "Manage your photo and display name."
	case "security":
		page.HeaderTitle = "Security & sessions"
		page.HeaderDetail = "Change your password and review signed-in devices."
	case "api-tokens":
		page.HeaderTitle = "API tokens"
		page.HeaderDetail = "Manage personal API and CLI credentials."
	case "general":
		page.HeaderTitle = "General"
		page.HeaderDetail = "Configure product identity and view instance details."
	case "principal-detail":
		page.HeaderTitle = "Principals"
		page.HeaderDetail = "Manage principal identity, access status, and sessions."
		if data.SelectedPrincipal == nil {
			page.Empty = uisignals.Pointer("Principal not found.")
			return page
		}
		principal := *data.SelectedPrincipal
		name := adminDisplayLabel(principal.DisplayName, principal.Email, principal.ID)
		page.HeaderTitle = "Principals / " + name
		page.HeaderDetail = "Manage identity and access with controls appropriate to its source."
	case "groups":
		page.HeaderTitle = "Groups"
		page.HeaderDetail = "Organize users and assign access collectively."
		page.ListFilterOptions = uisignals.OptionalSlice(adminGroupProviders(data.Groups))
		page.Sections = uisignals.OptionalSlice([]uisignals.AdminContentSectionSignal{{Title: "Groups", Table: uisignals.Pointer(adminGroupsGrid(filterAdminGroups(data.Groups, data.ListQuery, data.ListFilter)))}})
	case "service-accounts":
		page.HeaderTitle = "Service accounts"
		page.HeaderDetail = "Manage machine identities and credentials."
	case "authentication":
		page.HeaderTitle = "Authentication"
		page.HeaderDetail = "Review login and provisioning configuration."
	case "group-detail":
		page.HeaderTitle = "Groups"
		page.HeaderDetail = "Manage group identity and membership."
		if data.SelectedGroup == nil {
			page.Empty = uisignals.Pointer("Group not found.")
			return page
		}
		group := *data.SelectedGroup
		name := adminDisplayLabel(group.Name, group.ExternalID, group.ID)
		page.HeaderTitle = "Groups / " + name
		page.HeaderDetail = "Local groups are editable; synchronized groups remain read-only."
	case "agent":
		page.HeaderTitle = "Agent"
		page.HeaderDetail = "Review agent availability and configuration."
		page.Agent = uisignals.Pointer(adminAgentSignal(data.Agent))
		page.Metrics = uisignals.OptionalSlice([]uisignals.AdminMetricSignal{
			{Label: "Status", Value: configuredLabel(data.Agent.Enabled)},
			{Label: "Model", Value: data.Agent.Model},
			{Label: "Tools", Value: fmt.Sprint(len(data.Agent.Tools))},
		})
	case "storage":
		page.HeaderTitle = "Storage"
		page.HeaderDetail = "Browse tables and views across schemas."
		page.Storage = uisignals.Pointer(AdminStorageSignalFromData(data.Storage))
		page.Metrics = uisignals.OptionalSlice([]uisignals.AdminMetricSignal{{
			Label: "Total data size", Value: data.Storage.TotalDataSizeLabel,
			Detail: uisignals.Optional(fmt.Sprintf("%d active files", data.Storage.DataFileCount)),
		}})
	case "storage-detail":
		page.HeaderTitle = "Storage"
		page.HeaderDetail = "Physical storage and active data files."
		if len(data.Storage.Tables) != 1 {
			page.Empty = uisignals.Pointer("Storage table not found.")
			return page
		}
		table := data.Storage.Tables[0]
		page.HeaderTitle = table.Name
		page.Metrics = uisignals.OptionalSlice([]uisignals.AdminMetricSignal{
			{Label: "Data size", Value: table.SizeLabel},
			{Label: "Active files", Value: fmt.Sprint(table.FileCount)},
			{Label: "Stored rows", Value: table.RowCountLabel},
			{Label: "Begin snapshot", Value: fmt.Sprint(table.BeginSnapshot)},
		})
		page.Sections = uisignals.OptionalSlice([]uisignals.AdminContentSectionSignal{
			{Title: "Storage", Facts: uisignals.Pointer([]uisignals.DefinitionFactSignal{
				{Label: "Schema", Value: table.Schema},
				{Label: "Object type", Value: table.Type},
				{Label: "DuckLake path", Value: table.DuckLakePath},
				{Label: "Table UUID", Value: table.TableUUID},
			})},
			{Title: "Active files", Table: uisignals.Pointer(adminStorageFilesGrid(table.Files))},
		})
	case "queries":
		page.HeaderTitle = "Query history"
		page.HeaderDetail = "Inspect query activity, performance, and failures."
	case "audit":
		page.HeaderTitle = "Audit log"
		page.HeaderDetail = "Review security and administrative changes."
	case "system":
		page.HeaderTitle = "System"
		page.HeaderDetail = "View health, version, limits, and runtime configuration."
	case "publications":
		page.HeaderTitle = "Publications"
		page.HeaderDetail = "Control publicly shared dashboards."
		page.Publications = uisignals.OptionalSlice(adminPublicationSignals(data.Publications))
		if len(data.Publications) == 0 {
			page.Empty = uisignals.Pointer("No dashboard publications have been configured.")
		}
	default:
		page.HeaderTitle = "Profile"
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
		ProjectID:        uisignals.Optional(event.ProjectID),
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
			ProjectID:        event.ProjectID,
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
			ProjectID: row.ProjectID, Name: row.Name, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage,
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
	items := make([]uisignals.AdminDirectoryListItemSignal, 0, len(principals))
	for _, principal := range principals {
		if kind := strings.TrimSpace(principal.Kind); kind != "" && kind != "user" {
			continue
		}
		status := "active"
		if strings.TrimSpace(principal.DisabledAt) != "" {
			status = "inactive"
		}
		if !adminDirectoryFilterMatches(filter, status) {
			continue
		}
		items = append(items, uisignals.AdminDirectoryListItemSignal{
			ID: principal.ID, AvatarURL: uisignals.Optional(principal.ProfilePictureURL),
			Name:     adminDisplayLabel(principal.DisplayName, principal.Email, principal.ID),
			Username: adminPrincipalUsername(principal), Email: principal.Email,
			Href: adminPrincipalHref(principal.ID), Status: status,
			GroupCount: int64(len(principal.Groups)), JoinedAt: principal.CreatedAt, LastSeenAt: principal.LastSeenAt,
		})
	}
	return uisignals.AdminDirectoryListSignal{
		SearchPlaceholder: "Search by name or email",
		FilterLabel:       "Filter members",
		Items:             items,
	}
}

func adminDirectoryFilterMatches(filter, status string) bool {
	switch strings.ToLower(strings.TrimSpace(filter)) {
	case "active":
		return status == "active"
	case "inactive":
		return status == "inactive"
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

func adminStorageFilesGrid(files []AdminStorageFile) adminRecordTable {
	rows := make([]map[string]any, 0, len(files))
	for _, file := range files {
		rows = append(rows, map[string]any{
			"path":     file.Path,
			"format":   strings.ToUpper(file.Format),
			"rows":     map[string]any{"label": file.RecordCountLabel, "value": file.RecordCount},
			"size":     map[string]any{"label": file.SizeLabel, "value": file.SizeBytes},
			"snapshot": file.BeginSnapshot,
		})
	}
	return adminRecordTable{
		Columns: []adminRecordTableColumn{
			{ID: "path", Header: "File path", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("320px")},
			{ID: "format", Header: "Format", Width: uisignals.Pointer("90px")},
			{ID: "rows", Header: "Rows", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("110px")},
			{ID: "size", Header: "Data size", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("110px")},
			{ID: "snapshot", Header: "Begin snapshot", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("130px")},
		},
		Rows:     rows,
		Empty:    "No active data files were found for this table.",
		MinWidth: uisignals.Pointer("760px"),
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
	if event.ProjectID == "" {
		return "-"
	}
	return event.ProjectID
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
	case "api-tokens":
		return "API tokens"
	case "security":
		return "Security & sessions"
	case "general":
		return "General"
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
	case "service-accounts":
		return "Service accounts"
	case "authentication":
		return "Authentication"
	case "agent":
		return "Agent"
	case "storage":
		return "Storage"
	case "storage-detail":
		return "Storage table"
	case "queries":
		return "Query history"
	case "audit":
		return "Audit log"
	case "system":
		return "System"
	case "publications":
		return "Publications"
	default:
		return "Profile"
	}
}

func normalizeAdminSection(active string) string {
	switch strings.TrimSpace(active) {
	case "profile", "security", "api-tokens", "general", "principals", "principal-detail", "groups", "group-detail", "service-accounts", "authentication", "agent", "storage", "storage-detail", "queries", "audit", "system", "publications":
		return strings.TrimSpace(active)
	default:
		return "profile"
	}
}

func AdminStorageSignalFromData(data AdminStorageData) AdminStorageSignal {
	tables := make([]AdminStorageTableSignal, 0, len(data.Tables))
	for _, table := range data.Tables {
		tables = append(tables, AdminStorageTableSignalFromTable(table))
	}
	return AdminStorageSignal{
		Summary: AdminStorageSummary{
			TotalDataSizeLabel: data.TotalDataSizeLabel,
			TableCount:         int64(data.TableCount),
			DataFileCount:      int64(data.DataFileCount),
		},
		Status: data.Status,
		Tables: tables,
	}
}

func AdminStorageTableSignalFromTable(table AdminStorageTable) AdminStorageTableSignal {
	return AdminStorageTableSignal{
		Key:           AdminStorageTableKey(table.Schema, table.Name),
		Schema:        table.Schema,
		Name:          table.Name,
		Type:          table.Type,
		BeginSnapshot: table.BeginSnapshot,
		RowCount:      table.RowCount,
		RowCountLabel: table.RowCountLabel,
		ColumnCount:   int64(table.ColumnCount),
		FileCount:     int64(table.FileCount),
		SizeBytes:     table.SizeBytes,
		SizeLabel:     table.SizeLabel,
	}
}

func AdminStorageTableKey(schemaName, tableName string) string {
	return schemaName + "\x00" + tableName
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
