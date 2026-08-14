package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/workspace/ui"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
	"github.com/go-chi/chi/v5"
)

const (
	dataExplorerDefaultLimit = 100
	dataExplorerMaxLimit     = 1000
	dataExplorerRowHeight    = 32
)

const (
	DataExplorerDefaultLimit = dataExplorerDefaultLimit
	DataExplorerRowHeight    = dataExplorerRowHeight
)

var dataExplorerBlockIDs = []string{"a", "b", "c"}

type dataExplorerCommandSignals struct {
	DataExplorerCommand uisignals.DataExplorerCommand `json:"dataExplorerCommand"`
	DataExplorer        uisignals.DataExplorerSignal  `json:"dataExplorer"`
}

func (h Handler) DataExplorer(w nethttp.ResponseWriter, r *nethttp.Request) {
	page, explorer, err := h.globalDataExplorerState(r, dataExplorerCommandFromURL(r.URL.Query()))
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.DataExplorerPageWithAgent(h.catalogForWorkspacesPage(r, nil), page, explorer, h.dataExplorerAgentBootstrap(r, explorer), h.AgentCommands, h.csrfToken(r), h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) WorkspaceDataExplorerRedirect(w nethttp.ResponseWriter, r *nethttp.Request) {
	values := url.Values{}
	for key, entries := range r.URL.Query() {
		for _, entry := range entries {
			values.Add(key, entry)
		}
	}
	values.Set("workspace", h.workspaceID(chi.URLParam(r, "workspace")))
	target := "/data"
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	nethttp.Redirect(w, r, target, nethttp.StatusFound)
}

func (h Handler) DataExplorerUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	clientID := pagestream.EnsureClientID(w, r)
	streamID := dataExplorerStreamID(clientID)
	broker := h.broker()
	var trace *pagestream.TraceStore
	if broker != nil {
		trace = broker.TraceStore()
	}
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, streamID, "data-explorer.bootstrap"))
	page, explorer, err := h.globalDataExplorerState(r, dataExplorerCommandFromURL(r.URL.Query()))
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	if err := updates.Patch(ui.DataExplorerBootstrapSignalsWithAgent(h.catalogForWorkspacesPage(r, nil), page, explorer, h.dataExplorerAgentBootstrap(r, explorer), h.chromeOptions(r)...)); err != nil {
		return
	}
	if broker != nil {
		_ = updates.Forward(r.Context(), broker, streamID)
		return
	}
	updates.Wait(r.Context())
}

func (h Handler) dataExplorerAgentBootstrap(r *nethttp.Request, explorer uisignals.DataExplorerSignal) ui.DataExplorerAgentBootstrap {
	if h.AgentBootstrap == nil {
		return ui.DataExplorerAgentBootstrap{}
	}
	workspaceID := uisignals.ValueOrZero(explorer.Explore.Command.WorkspaceID)
	return h.AgentBootstrap(r, workspaceID)
}

func (h Handler) DataExplorerCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	clientID := pagestream.EnsureClientID(w, r)
	signals := dataExplorerCommandSignals{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if explorer, ok := dataExplorerResizeOnlyPatch(signals.DataExplorer, signals.DataExplorerCommand); ok {
		if broker := h.broker(); broker != nil {
			broker.Publish(dataExplorerStreamID(clientID), pagestream.SignalPatch{
				"dataExplorer":        explorer,
				"dataExplorerCommand": explorer.Command,
			})
		}
		w.WriteHeader(nethttp.StatusNoContent)
		return
	}
	_, explorer, err := h.globalDataExplorerStateWithCurrent(r, signals.DataExplorerCommand, &signals.DataExplorer)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	if dataPreviewCanceled(explorer.Preview) {
		w.WriteHeader(nethttp.StatusNoContent)
		return
	}
	if broker := h.broker(); broker != nil {
		broker.Publish(dataExplorerStreamID(clientID), pagestream.SignalPatch{
			"dataExplorer":        explorer,
			"dataExplorerCommand": explorer.Command,
			"agentContext":        ui.DataExplorerAgentContext(explorer),
		})
	}
	w.WriteHeader(nethttp.StatusNoContent)
}

func dataExplorerStreamID(clientID string) string {
	if strings.TrimSpace(clientID) == "" {
		clientID = "default"
	}
	return "data-explorer:" + clientID
}

func dataExplorerCommandFromQuery(workspaceID, object string) uisignals.DataExplorerCommand {
	return normalizeDataExplorerCommand(uisignals.DataExplorerCommand{
		Mode:        uisignals.Pointer("browse"),
		WorkspaceID: uisignals.Optional(strings.TrimSpace(workspaceID)),
		ObjectKey:   uisignals.Optional(strings.TrimSpace(object)),
		Limit:       dataExplorerDefaultLimit,
		Count:       dataExplorerDefaultLimit,
		Block:       uisignals.Pointer("all"),
	})
}

func dataExplorerCommandFromURL(values url.Values) uisignals.DataExplorerCommand {
	command := dataExplorerCommandFromQuery(values.Get("workspace"), values.Get("object"))
	if strings.EqualFold(strings.TrimSpace(values.Get("mode")), "explore") {
		command.Mode = uisignals.Pointer("explore")
		command.Explore = &uisignals.DataExploreCommand{
			WorkspaceID: uisignals.Optional(strings.TrimSpace(values.Get("workspace"))),
			ModelID:     uisignals.Optional(strings.TrimSpace(values.Get("model"))),
			DatasetID:   uisignals.Optional(strings.TrimSpace(values.Get("dataset"))),
			Dimensions:  splitDataExploreValues(values["dimension"]),
			Measures:    splitDataExploreValues(values["measure"]),
			Filters:     dataExploreFiltersFromURL(values["filter"]), Sort: dataExploreSortFromURL(values["sort"]),
			Time: dataExploreTimeFromURL(values.Get("time")), Limit: dataExploreLimitFromURL(values.Get("limit")),
		}
	}
	return normalizeDataExplorerCommand(command)
}

func dataExploreFiltersFromURL(values []string) []uisignals.DataExploreFilterSignal {
	out := []uisignals.DataExploreFilterSignal{}
	for _, value := range values {
		var filter uisignals.DataExploreFilterSignal
		if json.Unmarshal([]byte(value), &filter) == nil {
			out = append(out, filter)
		}
	}
	return out
}

func dataExploreSortFromURL(values []string) []uisignals.DataExploreSortSignal {
	out := []uisignals.DataExploreSortSignal{}
	for _, value := range values {
		var sortSpec uisignals.DataExploreSortSignal
		if json.Unmarshal([]byte(value), &sortSpec) == nil {
			out = append(out, sortSpec)
		}
	}
	return out
}

func dataExploreTimeFromURL(value string) *uisignals.DataExploreTimeSignal {
	var timeSpec uisignals.DataExploreTimeSignal
	if strings.TrimSpace(value) == "" || json.Unmarshal([]byte(value), &timeSpec) != nil {
		return nil
	}
	return &timeSpec
}

func dataExploreLimitFromURL(value string) int64 {
	limit, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || limit <= 0 {
		return dataExplorerDefaultLimit
	}
	return limit
}

func splitDataExploreValues(values []string) []string {
	out := []string{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func DataExplorerCommandFromQuery(workspaceID, object string) uisignals.DataExplorerCommand {
	return dataExplorerCommandFromQuery(workspaceID, object)
}

func normalizeDataExplorerCommand(command uisignals.DataExplorerCommand) uisignals.DataExplorerCommand {
	mode := strings.ToLower(strings.TrimSpace(uisignals.ValueOrZero(command.Mode)))
	if mode != "explore" {
		mode = "browse"
	}
	command.Mode = uisignals.Pointer(mode)
	command.WorkspaceID = uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.WorkspaceID)))
	command.ObjectKey = uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.ObjectKey)))
	if command.Limit <= 0 {
		command.Limit = dataExplorerDefaultLimit
	}
	if command.Limit > dataExplorerMaxLimit {
		command.Limit = dataExplorerMaxLimit
	}
	if command.Count <= 0 {
		command.Count = command.Limit
	}
	if command.Count > dataExplorerMaxLimit {
		command.Count = dataExplorerMaxLimit
	}
	if command.Offset < 0 {
		command.Offset = 0
	}
	if command.Start < 0 {
		command.Start = 0
	}
	if command.Start == 0 && command.Offset > 0 {
		command.Start = command.Offset
	}
	block := uisignals.ValueOrZero(command.Block)
	if block != "a" && block != "b" && block != "c" && block != "all" {
		command.Block = uisignals.Pointer("all")
	}
	direction := uisignals.ValueOrZero(command.Sort.Direction)
	if direction != "asc" && direction != "desc" {
		command.Sort.Direction = nil
	}
	if strings.TrimSpace(uisignals.ValueOrZero(command.Sort.Column)) == "" {
		command.Sort = uisignals.DataPreviewSortSignal{}
	}
	columnWidths := normalizeDataExplorerColumnWidths(uisignals.ValueOrZero(command.ColumnWidths))
	command.ColumnWidths = nil
	if len(columnWidths) > 0 {
		command.ColumnWidths = &columnWidths
	}
	if command.Explore == nil {
		command.Explore = &uisignals.DataExploreCommand{}
	}
	explore := normalizeDataExploreCommand(*command.Explore, uisignals.ValueOrZero(command.WorkspaceID))
	command.Explore = &explore
	return command
}

func normalizeDataExploreCommand(command uisignals.DataExploreCommand, fallbackWorkspaceID string) uisignals.DataExploreCommand {
	command.WorkspaceID = uisignals.Optional(firstNonEmpty(strings.TrimSpace(uisignals.ValueOrZero(command.WorkspaceID)), fallbackWorkspaceID))
	command.ModelID = uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.ModelID)))
	command.DatasetID = uisignals.Optional(strings.TrimSpace(uisignals.ValueOrZero(command.DatasetID)))
	command.Dimensions = uniqueDataExploreValues(command.Dimensions)
	command.Measures = uniqueDataExploreValues(command.Measures)
	if command.Filters == nil {
		command.Filters = []uisignals.DataExploreFilterSignal{}
	}
	if command.Sort == nil {
		command.Sort = []uisignals.DataExploreSortSignal{}
	}
	if command.Limit <= 0 {
		command.Limit = dataExplorerDefaultLimit
	}
	if command.Limit > dataExplorerMaxLimit {
		command.Limit = dataExplorerMaxLimit
	}
	return command
}

func uniqueDataExploreValues(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
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

func normalizeDataExplorerColumnWidths(widths map[string]float64) map[string]float64 {
	if len(widths) == 0 {
		return nil
	}
	out := map[string]float64{}
	for key, width := range widths {
		key = strings.TrimSpace(key)
		if key == "" || width <= 0 {
			continue
		}
		out[key] = width
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dataExplorerResizeOnlyPatch(current uisignals.DataExplorerSignal, nextCommand uisignals.DataExplorerCommand) (uisignals.DataExplorerSignal, bool) {
	if uisignals.ValueOrZero(nextCommand.Mode) == "explore" || len(uisignals.ValueOrZero(nextCommand.ColumnWidths)) == 0 || current.SelectedObject == nil {
		return uisignals.DataExplorerSignal{}, false
	}
	next := normalizeDataExplorerCommand(nextCommand)
	previous := normalizeDataExplorerCommand(current.Command)
	if !dataExplorerCommandsEqualExceptColumnWidths(previous, next) {
		return uisignals.DataExplorerSignal{}, false
	}
	current.Command = next
	current.SelectedWorkspaceID = uisignals.Optional(firstNonEmpty(uisignals.ValueOrZero(current.SelectedWorkspaceID), uisignals.ValueOrZero(next.WorkspaceID)))
	current.SelectedKey = uisignals.Optional(firstNonEmpty(uisignals.ValueOrZero(current.SelectedKey), uisignals.ValueOrZero(next.ObjectKey)))
	return current, true
}

func dataExplorerCommandsEqualExceptColumnWidths(left, right uisignals.DataExplorerCommand) bool {
	return uisignals.ValueOrZero(left.Mode) == uisignals.ValueOrZero(right.Mode) &&
		uisignals.ValueOrZero(left.WorkspaceID) == uisignals.ValueOrZero(right.WorkspaceID) &&
		uisignals.ValueOrZero(left.ObjectKey) == uisignals.ValueOrZero(right.ObjectKey) &&
		left.Offset == right.Offset &&
		left.Limit == right.Limit &&
		uisignals.ValueOrZero(left.Block) == uisignals.ValueOrZero(right.Block) &&
		left.Start == right.Start &&
		left.Count == right.Count &&
		left.RequestSeq == right.RequestSeq &&
		left.ResetVersion == right.ResetVersion &&
		uisignals.ValueOrZero(left.Sort.Column) == uisignals.ValueOrZero(right.Sort.Column) &&
		uisignals.ValueOrZero(left.Sort.Direction) == uisignals.ValueOrZero(right.Sort.Direction) &&
		stringSlicesEqual(uisignals.ValueOrZero(left.VisibleColumns), uisignals.ValueOrZero(right.VisibleColumns))
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
