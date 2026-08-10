package http

import (
	"database/sql"
	nethttp "net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/admin/ui"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/go-chi/chi/v5"
)

type QueryAuditReaderProvider func() (queryaudit.Reader, error)

type Handler struct {
	ReadModel           ReadModel
	Layout              func(*nethttp.Request) webpage.Provider
	EnsureClientID      func(nethttp.ResponseWriter, *nethttp.Request)
	Broker              *pagestream.Broker
	PublicationMutation func(*nethttp.Request, uisignals.AdminPublicationCommand) error
}

type storageCommandSignals struct {
	AdminStorageCommand ui.AdminStorageCommand `json:"adminStorageCommand"`
}

type publicationCommandSignals struct {
	AdminPublicationCommand uisignals.AdminPublicationCommand `json:"adminPublicationCommand"`
}

type entityListSignals struct {
	Query  *string `json:"entityListQuery"`
	Filter *string `json:"entityListFilter"`
}

func (h Handler) AdminRoot(w nethttp.ResponseWriter, r *nethttp.Request) {
	nethttp.Redirect(w, r, "/admin/profile", nethttp.StatusSeeOther)
}

func (h Handler) Profile(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderPage(w, r, "profile")
}

func (h Handler) Principals(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderPage(w, r, "principals")
}

func (h Handler) PrincipalDetail(w nethttp.ResponseWriter, r *nethttp.Request) {
	data, err := h.adminData(r)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	principalID := chi.URLParam(r, "principal")
	for i := range data.Principals {
		if data.Principals[i].ID == principalID {
			data.SelectedPrincipal = &data.Principals[i]
			h.writePage(w, r, "principal-detail", data)
			return
		}
	}
	nethttp.NotFound(w, r)
}

func (h Handler) Groups(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderPage(w, r, "groups")
}

func (h Handler) PrincipalsSearch(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.listSearch(w, r, "principals")
}

func (h Handler) GroupsSearch(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.listSearch(w, r, "groups")
}

func (h Handler) listSearch(w nethttp.ResponseWriter, r *nethttp.Request, active string) {
	var signals entityListSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	query, filter := "", ""
	if signals.Query != nil {
		query = strings.TrimSpace(*signals.Query)
	}
	if signals.Filter != nil {
		filter = strings.TrimSpace(*signals.Filter)
	}
	values := r.URL.Query()
	if query != "" {
		values.Set("q", query)
	}
	if filter != "" && filter != "all" {
		values.Set("filter", filter)
	}
	r.URL.RawQuery = values.Encode()
	data, err := h.adminDataForUpdates(r, active)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	data.ListQuery = query
	data.ListFilter = filter
	_ = pagestream.PatchResponse(w, r, ui.AdminListResultsPatch(active, data))
}

func (h Handler) GroupDetail(w nethttp.ResponseWriter, r *nethttp.Request) {
	data, err := h.adminData(r)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	groupID := chi.URLParam(r, "group")
	for i := range data.Groups {
		if data.Groups[i].ID == groupID {
			data.SelectedGroup = &data.Groups[i]
			h.writePage(w, r, "group-detail", data)
			return
		}
	}
	nethttp.NotFound(w, r)
}

func (h Handler) Agent(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderPage(w, r, "agent")
}

func (h Handler) Storage(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.ensureClientID(w, r)
	h.renderPage(w, r, "storage")
}

func (h Handler) Queries(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.ensureClientID(w, r)
	h.renderPage(w, r, "queries")
}

func (h Handler) Publications(w nethttp.ResponseWriter, r *nethttp.Request) {
	data, err := h.readModel().PublicationData(r)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	h.writePage(w, r, "publications", data)
}

func (h Handler) PublicationCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals publicationCommandSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if h.PublicationMutation == nil {
		nethttp.Error(w, "publication management is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	if err := h.PublicationMutation(r, signals.AdminPublicationCommand); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusConflict)
		return
	}
	_ = pagestream.Redirect(w, r, "/admin/publications")
}

func (h Handler) BootstrapUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	active := strings.TrimSpace(r.URL.Query().Get("section"))
	if active == "" || active == "general" {
		active = "profile"
	}
	var listState entityListSignals
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	if active == "principals" || active == "groups" {
		if err := pagestream.ReadSignals(r, &listState); err != nil {
			nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
			return
		}
		if listState.Query != nil {
			query = strings.TrimSpace(*listState.Query)
		}
		if listState.Filter != nil {
			filter = strings.TrimSpace(*listState.Filter)
		}
		values := r.URL.Query()
		if query == "" {
			values.Del("q")
		} else {
			values.Set("q", query)
		}
		if filter == "" || filter == "all" {
			values.Del("filter")
		} else {
			values.Set("filter", filter)
		}
		r.URL.RawQuery = values.Encode()
	}
	var data ui.AdminData
	var err error
	if active == "publications" {
		data, err = h.readModel().PublicationData(r)
	} else {
		data, err = h.adminDataForUpdates(r, active)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			nethttp.NotFound(w, r)
			return
		}
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	data.ListQuery = query
	data.ListFilter = filter
	h.patchAndWait(w, r, ui.AdminBootstrapSignals(active, data, h.layout(r)))
}

func (h Handler) QueryUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.queryHistoryUpdates(w, r)
}

func (h Handler) QueryCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.queryHistoryCommand(w, r)
}

func (h Handler) StorageSignalUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	clientID := pagestream.EnsureClientID(w, r)
	if h.Broker == nil {
		nethttp.Error(w, "admin storage broker is not configured", nethttp.StatusInternalServerError)
		return
	}
	streamID := adminStorageStreamID(clientID)
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(h.Broker.TraceStore(), streamID, "admin.storage.bootstrap"))
	data, err := h.adminDataForUpdates(r, "storage")
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	if err := updates.Patch(ui.AdminBootstrapSignals("storage", data, h.layout(r))); err != nil {
		return
	}
	_ = updates.Forward(r.Context(), h.Broker, streamID)
}

func (h Handler) StorageTableSelect(w nethttp.ResponseWriter, r *nethttp.Request) {
	clientID := pagestream.EnsureClientID(w, r)
	signals := storageCommandSignals{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	command := signals.AdminStorageCommand
	table, err := h.readModel().StorageService.SelectTable(r.Context(), command.DatabaseID, command.Schema, command.Table)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	selectedTable := ui.AdminStorageTableSignalFromTable(*table)
	if h.Broker == nil {
		nethttp.Error(w, "admin storage broker is not configured", nethttp.StatusInternalServerError)
		return
	}
	h.Broker.Publish(adminStorageStreamID(clientID), map[string]any{
		"adminStorage": map[string]any{
			"selectedKey":   selectedTable.Key,
			"selectedTable": &selectedTable,
		},
	})
	w.WriteHeader(nethttp.StatusNoContent)
}

func (h Handler) renderPage(w nethttp.ResponseWriter, r *nethttp.Request, active string) {
	data, err := h.adminDataForUpdates(r, active)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	h.writePage(w, r, active, data)
}

func (h Handler) writePage(w nethttp.ResponseWriter, r *nethttp.Request, active string, data ui.AdminData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.AdminPage(active, data, h.layout(r)).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) adminData(r *nethttp.Request) (ui.AdminData, error) {
	return h.readModel().Data(r)
}

func (h Handler) adminDataForUpdates(r *nethttp.Request, active string) (ui.AdminData, error) {
	switch active {
	case "principals":
		return h.readModel().PrincipalsListData(r)
	case "groups":
		return h.readModel().GroupsListData(r)
	}
	data, err := h.adminData(r)
	if err != nil {
		return data, err
	}
	switch active {
	case "principal-detail":
		principalID := strings.TrimSpace(r.URL.Query().Get("principal"))
		for i := range data.Principals {
			if data.Principals[i].ID == principalID {
				data.SelectedPrincipal = &data.Principals[i]
				return data, nil
			}
		}
		return data, sql.ErrNoRows
	case "group-detail":
		groupID := strings.TrimSpace(r.URL.Query().Get("group"))
		for i := range data.Groups {
			if data.Groups[i].ID == groupID {
				data.SelectedGroup = &data.Groups[i]
				return data, nil
			}
		}
		return data, sql.ErrNoRows
	default:
		return data, nil
	}
}

func (h Handler) patchAndWait(w nethttp.ResponseWriter, r *nethttp.Request, patch pagestream.SignalPatch) {
	clientID := pagestream.EnsureClientID(w, r)
	var trace *pagestream.TraceStore
	if h.Broker != nil {
		trace = h.Broker.TraceStore()
	}
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, "admin:"+clientID, "admin.bootstrap"))
	if err := updates.Patch(patch); err != nil {
		return
	}
	updates.Wait(r.Context())
}

func (h Handler) layout(r *nethttp.Request) webpage.Provider {
	if h.Layout == nil {
		return nil
	}
	return h.Layout(r)
}

func (h Handler) ensureClientID(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.EnsureClientID != nil {
		h.EnsureClientID(w, r)
		return
	}
	_ = pagestream.EnsureClientID(w, r)
}

func (h Handler) readModel() ReadModel {
	return h.ReadModel
}

func adminStorageStreamID(clientID string) string {
	if strings.TrimSpace(clientID) == "" {
		clientID = "default"
	}
	return "admin-storage:" + clientID
}
