package http

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/admin/ui"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
)

const (
	adminQueryHistoryDefaultLimit = 50
	adminQueryHistoryMaxLimit     = 100
)

type queryHistoryCommandSignals struct {
	AdminQueryHistory        uisignals.AdminQueryHistorySignal  `json:"adminQueryHistory"`
	AdminQueryDetail         uisignals.AdminQueryDetailSignal   `json:"adminQueryDetail"`
	AdminQueryHistoryCommand uisignals.AdminQueryHistoryCommand `json:"adminQueryHistoryCommand"`
}

func (h Handler) queryHistoryUpdates(w http.ResponseWriter, r *http.Request) {
	clientID, ok := webtransport.RequireClientID(w, r)
	if !ok {
		return
	}
	if h.Broker == nil {
		http.Error(w, "admin query-history broker is not configured", http.StatusInternalServerError)
		return
	}
	streamID := queryHistoryStreamID(clientID)
	updates := pagestream.NewSignalStream(w, r)
	data, err := h.adminDataForUpdates(r, "queries")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := updates.Patch(ui.AdminBootstrapSignals("queries", data, h.layout(r))); err != nil {
		return
	}
	_ = updates.Forward(r.Context(), h.Broker, streamID)
}

func (h Handler) queryHistoryCommand(w http.ResponseWriter, r *http.Request) {
	clientID, ok := webtransport.RequireClientID(w, r)
	if !ok {
		return
	}
	signals := queryHistoryCommandSignals{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	command := normalizeQueryHistoryCommand(signals.AdminQueryHistoryCommand)
	repo, err := h.readModel().queryAuditReader()
	if err != nil || repo == nil {
		errorText := queryHistoryErrorText(err)
		if errorText == "" {
			errorText = "Query audit repository is not configured."
		}
		if command.Action == "select_detail" {
			detail := signals.AdminQueryDetail
			detail.EventID = command.EventID
			detail.Loading = false
			detail.Error = uisignals.Optional(errorText)
			h.publishQueryDetailPatch(clientID, detail)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		history := signals.AdminQueryHistory
		history.Loading = false
		history.Error = errorText
		h.publishQueryHistoryPatch(clientID, history)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch command.Action {
	case "select_detail":
		event, err := repo.GetQueryEvent(r.Context(), uisignals.ValueOrZero(command.EventID))
		if err != nil {
			detail := signals.AdminQueryDetail
			detail.EventID = command.EventID
			detail.Loading = false
			detail.Error = uisignals.Optional(err.Error())
			h.publishQueryDetailPatch(clientID, detail)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.publishQueryDetailPatch(clientID, ui.AdminQueryDetailSignalFromEvent(queryEventFromAudit(event)))
		w.WriteHeader(http.StatusNoContent)
		return
	case "close_detail":
		h.publishQueryDetailPatch(clientID, uisignals.AdminQueryDetailSignal{})
		w.WriteHeader(http.StatusNoContent)
		return
	case "filter_search":
		history := signals.AdminQueryHistory
		history.Loading = false
		history.Error = ""
		filterMenu := uisignals.ValueOrZero(command.FilterMenu)
		history.FilterMenus = uisignals.OptionalSlice(h.readModel().queryHistoryFilterMenus(r, repo, command.Filters, uisignals.ValueOrZero(filterMenu.MenuID), uisignals.ValueOrZero(filterMenu.Search)))
		h.publishQueryHistoryPatch(clientID, history)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if command.Action == "filter_toggle" || command.Action == "filter_clear" {
		command.Filters = applyFilterMenuCommand(command.Filters, uisignals.ValueOrZero(command.FilterMenu))
		command.PageToken = nil
	}
	events, nextCursor, hasMore, err := queryHistoryPage(r, repo, command.Filters, uisignals.ValueOrZero(command.PageToken), int(uisignals.ValueOrZero(command.Limit)))
	history := signals.AdminQueryHistory
	incomingCount := len(history.Table.Rows)
	if command.Action == "load_more" {
		nextTable := ui.AdminQueryHistorySignalFromData(ui.AdminQueryHistoryData{Events: events}).Table
		history.Table.Rows = append(history.Table.Rows, nextTable.Rows...)
	} else {
		history.Table = ui.AdminQueryHistorySignalFromData(ui.AdminQueryHistoryData{Events: events}).Table
		incomingCount = 0
	}
	history.FilterMenus = uisignals.OptionalSlice(h.readModel().queryHistoryFilterMenus(r, repo, command.Filters, "", ""))
	history.Filters = command.Filters
	history.NextCursor = nextCursor
	history.HasMore = hasMore
	history.LoadedCountLabel = queryHistoryLoadedCountLabel(incomingCount + len(events))
	history.Loading = false
	history.Error = ""
	history.Limit = int64(normalizeQueryHistoryLimit(int(uisignals.ValueOrZero(command.Limit))))
	if err != nil {
		history.Loading = false
		history.Error = err.Error()
	}
	h.publishQueryHistoryPatch(clientID, history)
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) publishQueryHistoryPatch(clientID string, history uisignals.AdminQueryHistorySignal) {
	if h.Broker == nil {
		return
	}
	h.Broker.Publish(queryHistoryStreamID(clientID), map[string]any{
		"adminQueryHistory": history,
		"adminQueryHistoryCommand": uisignals.AdminQueryHistoryCommand{
			Action:    "load_more",
			Filters:   history.Filters,
			PageToken: uisignals.Optional(history.NextCursor),
			Limit:     uisignals.Pointer(history.Limit),
		},
	})
}

func (h Handler) publishQueryDetailPatch(clientID string, detail uisignals.AdminQueryDetailSignal) {
	if h.Broker == nil {
		return
	}
	h.Broker.Publish(queryHistoryStreamID(clientID), map[string]any{"adminQueryDetail": detail})
}

func normalizeQueryHistoryCommand(command uisignals.AdminQueryHistoryCommand) uisignals.AdminQueryHistoryCommand {
	action := strings.TrimSpace(command.Action)
	switch action {
	case "load_more", "select_detail", "close_detail", "filter_search", "filter_toggle", "filter_clear":
	default:
		action = "reset"
		command.PageToken = nil
	}
	command.Action = action
	command.Limit = uisignals.Pointer(int64(normalizeQueryHistoryLimit(int(uisignals.ValueOrZero(command.Limit)))))
	command.PageToken = uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.PageToken)))
	command.EventID = uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.EventID)))
	command.Filters = normalizeQueryHistoryFilters(command.Filters)
	filterMenu := normalizeFilterMenuCommand(uisignals.ValueOrZero(command.FilterMenu))
	command.FilterMenu = &filterMenu
	return command
}

func normalizeQueryHistoryFilters(filters uisignals.AdminQueryHistoryFilters) uisignals.AdminQueryHistoryFilters {
	return uisignals.AdminQueryHistoryFilters{
		Projects:   uisignals.OptionalSlice(cleanStringSlice(uisignals.ValueOrZero(filters.Projects))),
		Principals: uisignals.OptionalSlice(cleanStringSlice(uisignals.ValueOrZero(filters.Principals))),
		Surfaces:   uisignals.OptionalSlice(cleanStringSlice(uisignals.ValueOrZero(filters.Surfaces))),
		Kinds:      uisignals.OptionalSlice(cleanStringSlice(uisignals.ValueOrZero(filters.Kinds))),
		Statuses:   uisignals.OptionalSlice(cleanStringSlice(uisignals.ValueOrZero(filters.Statuses))),
		Target:     uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(filters.Target))),
		Search:     uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(filters.Search))),
		From:       uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(filters.From))),
		To:         uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(filters.To))),
	}
}

func normalizeFilterMenuCommand(command uisignals.FilterMenuCommand) uisignals.FilterMenuCommand {
	return uisignals.FilterMenuCommand{
		MenuID:   uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.MenuID))),
		Action:   uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.Action))),
		Search:   uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.Search))),
		Value:    uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.Value))),
		Selected: uisignals.OptionalSlice(cleanStringSlice(uisignals.ValueOrZero(command.Selected))),
	}
}

func applyFilterMenuCommand(filters uisignals.AdminQueryHistoryFilters, command uisignals.FilterMenuCommand) uisignals.AdminQueryHistoryFilters {
	if uisignals.ValueOrZero(command.Action) == "clear" {
		command.Selected = nil
	}
	selected := cleanStringSlice(uisignals.ValueOrZero(command.Selected))
	if uisignals.ValueOrZero(command.Action) == "toggle" {
		selected = toggleStringSelection(selected, uisignals.ValueOrZero(command.Value))
	}
	switch uisignals.ValueOrZero(command.MenuID) {
	case "project":
		filters.Projects = uisignals.OptionalSlice(selected)
	case "principal":
		filters.Principals = uisignals.OptionalSlice(selected)
	case "surface":
		filters.Surfaces = uisignals.OptionalSlice(selected)
	case "kind":
		filters.Kinds = uisignals.OptionalSlice(selected)
	case "status":
		filters.Statuses = uisignals.OptionalSlice(selected)
	}
	return normalizeQueryHistoryFilters(filters)
}

func toggleStringSelection(selected []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return selected
	}
	out := make([]string, 0, len(selected)+1)
	removed := false
	for _, item := range selected {
		if item == value {
			removed = true
			continue
		}
		out = append(out, item)
	}
	if !removed {
		out = append(out, value)
	}
	return out
}

func cleanStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func queryHistoryLoadedCountLabel(count int) string {
	if count == 1 {
		return "1 query loaded"
	}
	return strconv.Itoa(count) + " queries loaded"
}

func (m ReadModel) queryHistoryFilterMenus(r *http.Request, repo queryaudit.Reader, filters uisignals.AdminQueryHistoryFilters, searchMenuID, search string) []uisignals.FilterMenuSignal {
	menus := []struct {
		id          string
		label       string
		placeholder string
		empty       string
		selected    []string
		values      map[string]int
		labels      map[string]string
		icon        string
	}{
		{id: "project", label: "Project", placeholder: "Search projects", empty: "No projects found.", selected: uisignals.ValueOrZero(filters.Projects), values: map[string]int{}, labels: map[string]string{}, icon: "project"},
		{id: "principal", label: "User", placeholder: "Search users", empty: "No users found.", selected: uisignals.ValueOrZero(filters.Principals), values: map[string]int{}, labels: map[string]string{}, icon: "user"},
		{id: "surface", label: "Source type", placeholder: "Search source types", empty: "No source types found.", selected: uisignals.ValueOrZero(filters.Surfaces), values: map[string]int{}, labels: map[string]string{}, icon: "source"},
		{id: "kind", label: "Kind", placeholder: "Search kinds", empty: "No kinds found.", selected: uisignals.ValueOrZero(filters.Kinds), values: map[string]int{}, labels: map[string]string{}, icon: "kind"},
		{id: "status", label: "Status", placeholder: "Search statuses", empty: "No statuses found.", selected: uisignals.ValueOrZero(filters.Statuses), values: map[string]int{}, labels: map[string]string{}, icon: "status"},
	}
	out := make([]uisignals.FilterMenuSignal, 0, len(menus))
	for _, menu := range menus {
		menuSearch := ""
		loading := false
		if menu.id == searchMenuID {
			menuSearch = strings.TrimSpace(search)
		}
		options, err := repo.ListQueryEventFilterOptions(r.Context(), menu.id, menuSearch, 100)
		if err != nil {
			return queryHistoryFilterMenusWithError(filters, err.Error())
		}
		for _, option := range options {
			menu.values[option.Value] = option.Count
		}
		if menu.id == "principal" {
			menu.labels = m.PrincipalLabels(r, mapKeys(menu.values))
		}
		out = append(out, queryHistoryFilterMenu(menu.id, menu.label, menu.placeholder, menu.empty, menu.icon, menu.values, menu.labels, menu.selected, menuSearch, loading, ""))
	}
	return out
}

func queryHistoryFilterMenusWithError(filters uisignals.AdminQueryHistoryFilters, message string) []uisignals.FilterMenuSignal {
	return []uisignals.FilterMenuSignal{
		queryHistoryFilterMenu("project", "Project", "Search projects", "No projects found.", "project", nil, nil, uisignals.ValueOrZero(filters.Projects), "", false, message),
		queryHistoryFilterMenu("principal", "User", "Search users", "No users found.", "user", nil, nil, uisignals.ValueOrZero(filters.Principals), "", false, message),
		queryHistoryFilterMenu("surface", "Source type", "Search source types", "No source types found.", "source", nil, nil, uisignals.ValueOrZero(filters.Surfaces), "", false, message),
		queryHistoryFilterMenu("kind", "Kind", "Search kinds", "No kinds found.", "kind", nil, nil, uisignals.ValueOrZero(filters.Kinds), "", false, message),
		queryHistoryFilterMenu("status", "Status", "Search statuses", "No statuses found.", "status", nil, nil, uisignals.ValueOrZero(filters.Statuses), "", false, message),
	}
}

func queryHistoryFilterMenu(id, label, placeholder, emptyLabel, icon string, values map[string]int, labels map[string]string, selected []string, search string, loading bool, errorText string) uisignals.FilterMenuSignal {
	selected = cleanStringSlice(selected)
	options := queryHistoryFilterOptions(values, labels, selected, search, icon)
	return uisignals.FilterMenuSignal{
		ID:           id,
		Label:        label,
		SummaryLabel: uisignals.Optional(queryHistoryFilterSummary(label, selected, labels)),
		Mode:         uisignals.Pointer("multi"),
		Search:       uisignals.Optional(search),
		Selected:     uisignals.OptionalSlice(selected),
		Options:      uisignals.OptionalSlice(options),
		Loading:      loading,
		Error:        uisignals.Optional(errorText),
		Placeholder:  uisignals.Optional(placeholder),
		EmptyLabel:   uisignals.Optional(emptyLabel),
	}
}

func queryHistoryFilterOptions(values map[string]int, labels map[string]string, selected []string, search, icon string) []uisignals.FilterMenuOptionSignal {
	search = strings.ToLower(strings.TrimSpace(search))
	selectedSet := stringSet(selected)
	options := make([]uisignals.FilterMenuOptionSignal, 0, len(values)+len(selected))
	seen := map[string]struct{}{}
	for value, count := range values {
		label := queryHistoryOptionLabel(value, labels)
		if search != "" && !strings.Contains(strings.ToLower(label+" "+value), search) {
			continue
		}
		seen[value] = struct{}{}
		options = append(options, uisignals.FilterMenuOptionSignal{
			Value:      value,
			Label:      label,
			Icon:       uisignals.Optional(icon),
			CountLabel: uisignals.Optional(strconv.Itoa(count)),
			Selected:   selectedSet[value],
		})
	}
	for _, value := range selected {
		if _, ok := seen[value]; ok {
			continue
		}
		label := queryHistoryOptionLabel(value, labels)
		if search != "" && !strings.Contains(strings.ToLower(label+" "+value), search) {
			continue
		}
		options = append(options, uisignals.FilterMenuOptionSignal{
			Value:    value,
			Label:    label,
			Icon:     uisignals.Optional(icon),
			Selected: true,
		})
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Selected != options[j].Selected {
			return options[i].Selected
		}
		return strings.ToLower(options[i].Label) < strings.ToLower(options[j].Label)
	})
	return options
}

func queryHistoryOptionLabel(value string, labels map[string]string) string {
	if labels != nil {
		if label := strings.TrimSpace(labels[value]); label != "" {
			return label
		}
	}
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func queryHistoryFilterSummary(label string, selected []string, labels map[string]string) string {
	selected = cleanStringSlice(selected)
	switch len(selected) {
	case 0:
		return label
	case 1:
		return queryHistoryOptionLabel(selected[0], labels)
	default:
		return strconv.Itoa(len(selected)) + " selected"
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func mapKeys(values map[string]int) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func queryHistoryStreamID(clientID string) string {
	if strings.TrimSpace(clientID) == "" {
		clientID = "default"
	}
	return "admin-queries:" + clientID
}

func queryHistoryPage(r *http.Request, repo queryaudit.Reader, filters uisignals.AdminQueryHistoryFilters, pageToken string, limit int) ([]ui.AdminQueryEvent, string, bool, error) {
	limit = normalizeQueryHistoryLimit(limit)
	rows, err := repo.ListQueryEvents(r.Context(), queryaudit.Filter{
		ProjectIDs:   projectIDs(uisignals.ValueOrZero(filters.Projects)),
		PrincipalIDs: cleanStringSlice(uisignals.ValueOrZero(filters.Principals)),
		Surfaces:     cleanStringSlice(uisignals.ValueOrZero(filters.Surfaces)),
		QueryKinds:   cleanStringSlice(uisignals.ValueOrZero(filters.Kinds)),
		Target:       strings.TrimSpace(uisignals.ValueOrZero(filters.Target)),
		Statuses:     cleanStringSlice(uisignals.ValueOrZero(filters.Statuses)),
		Search:       strings.TrimSpace(uisignals.ValueOrZero(filters.Search)),
		From:         strings.TrimSpace(uisignals.ValueOrZero(filters.From)),
		To:           strings.TrimSpace(uisignals.ValueOrZero(filters.To)),
		PageToken:    strings.TrimSpace(pageToken),
		Limit:        limit + 1,
	})
	if err != nil {
		return nil, "", false, err
	}
	nextCursor := ""
	hasMore := len(rows) > limit
	if hasMore {
		last := rows[limit-1]
		nextCursor = encodeCursor(last.CreatedAt, last.ID)
		rows = rows[:limit]
	}
	out := make([]ui.AdminQueryEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, queryEventFromAudit(row))
	}
	return out, nextCursor, hasMore, nil
}

func projectIDs(values []string) []projectgraph.ResourceID {
	cleaned := cleanStringSlice(values)
	if len(cleaned) == 0 {
		return nil
	}
	out := make([]projectgraph.ResourceID, 0, len(cleaned))
	for _, value := range cleaned {
		out = append(out, projectgraph.ResourceID(value))
	}
	return out
}

func queryEventFromAudit(row queryaudit.Event) ui.AdminQueryEvent {
	return ui.AdminQueryEvent{
		ID:               row.ID,
		ProjectID:        row.ProjectID.String(),
		PrincipalID:      row.PrincipalID,
		Surface:          row.Surface,
		Operation:        row.Operation,
		QueryKind:        row.QueryKind,
		ModelID:          row.ModelID,
		Target:           row.Target,
		ObjectType:       row.ObjectType,
		ObjectID:         row.ObjectID,
		RequestID:        row.RequestID,
		CorrelationID:    row.CorrelationID,
		Status:           row.Status,
		DurationMS:       row.DurationMS,
		PlanningMS:       row.PlanningMS,
		ConnectionWaitMS: row.ConnectionWaitMS,
		DatabaseMS:       row.DatabaseMS,
		RowsReturned:     row.RowsReturned,
		Error:            row.Error,
		SQL:              row.SQL,
		PlanText:         row.PlanText,
		QueryJSON:        row.QueryJSON,
		CreatedAt:        row.CreatedAt,
	}
}

func queryHistoryErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func normalizeQueryHistoryLimit(limit int) int {
	if limit <= 0 {
		return adminQueryHistoryDefaultLimit
	}
	if limit > adminQueryHistoryMaxLimit {
		return adminQueryHistoryMaxLimit
	}
	return limit
}

func encodeCursor(createdAt, id string) string {
	if strings.TrimSpace(createdAt) == "" || strings.TrimSpace(id) == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + id))
}
