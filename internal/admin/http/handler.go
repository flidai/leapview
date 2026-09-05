package http

import (
	"context"
	"database/sql"
	"errors"
	nethttp "net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/admin/personalsettings"
	"github.com/flidai/leapview/internal/admin/productsettings"
	adminsettings "github.com/flidai/leapview/internal/admin/settings"
	"github.com/flidai/leapview/internal/admin/ui"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/dashboard/publication"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
)

type QueryAuditReaderProvider func() (queryaudit.Reader, error)

type Handler struct {
	ReadModel           ReadModel
	Layout              func(*nethttp.Request) webpage.Provider
	EnsureClientID      func(nethttp.ResponseWriter, *nethttp.Request) bool
	Broker              *pagestream.Broker
	PublicationMutation func(*nethttp.Request, uisignals.AdminPublicationCommand) error
	PersonalSettings    *personalsettings.Handler
	ProductSettings     *productsettings.Handler
	SettingsRepository  interface {
		access.Repository
		adminsettings.ServiceAccountReader
	}
	AuthorizationProjection adminsettings.AuthorizationProjectionReader
	CurrentCredential       func(*nethttp.Request) (access.APICredential, bool)
}

type publicationCommandSignals struct {
	AdminPublicationCommand uisignals.AdminPublicationCommand `json:"adminPublicationCommand"`
}

type serviceAccountCommandSignals struct {
	Command adminsettings.ServiceAccountCommand `json:"adminServiceAccountCommand"`
}

type auditLogCommandSignals struct {
	Command adminsettings.AuditLogCommand `json:"adminAuditLogCommand"`
	Current adminsettings.AuditLogSignal  `json:"adminAuditLog"`
}

type entityListSignals struct {
	Query  *string `json:"entityListQuery"`
	Filter *string `json:"entityListFilter"`
}

func (h Handler) AdminRoot(w nethttp.ResponseWriter, r *nethttp.Request) {
	nethttp.Redirect(w, r, "/admin/profile", nethttp.StatusSeeOther)
}

func (h Handler) Profile(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	h.renderPage(w, r, "profile")
}

func (h Handler) Security(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	h.renderPage(w, r, "security")
}
func (h Handler) APITokens(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	h.renderPage(w, r, "api-tokens")
}
func (h Handler) General(w nethttp.ResponseWriter, r *nethttp.Request) { h.renderPage(w, r, "general") }
func (h Handler) ServiceAccounts(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderPage(w, r, "service-accounts")
}
func (h Handler) Authentication(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderPage(w, r, "authentication")
}
func (h Handler) Audit(w nethttp.ResponseWriter, r *nethttp.Request)  { h.renderPage(w, r, "audit") }
func (h Handler) System(w nethttp.ResponseWriter, r *nethttp.Request) { h.renderPage(w, r, "system") }

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
	h.renderPage(w, r, "storage")
}

func (h Handler) StorageTable(w nethttp.ResponseWriter, r *nethttp.Request) {
	data, err := h.readModel().StorageTableData(r, chi.URLParam(r, "schema"), chi.URLParam(r, "table"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			nethttp.NotFound(w, r)
			return
		}
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	h.writePage(w, r, "storage-detail", data)
}

func (h Handler) Queries(w nethttp.ResponseWriter, r *nethttp.Request) {
	if !h.ensureClientID(w, r) {
		return
	}
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
		status := publicationMutationStatus(err)
		nethttp.Error(w, nethttp.StatusText(status), status)
		return
	}
	_ = pagestream.Redirect(w, r, "/admin/publications")
}

func publicationMutationStatus(err error) int {
	if kind, ok := apigenfailure.KindOf(err); ok {
		switch kind {
		case "precondition":
			return nethttp.StatusPreconditionFailed
		case "not_found":
			return nethttp.StatusNotFound
		case "conflict":
			return nethttp.StatusConflict
		case "audit_unavailable", "authorization_unavailable", "service_unavailable", "unavailable":
			return nethttp.StatusServiceUnavailable
		}
	}
	if errors.Is(err, publication.ErrNotFound) {
		return nethttp.StatusNotFound
	}
	if errors.Is(err, publication.ErrConflict) {
		return nethttp.StatusConflict
	}
	return nethttp.StatusServiceUnavailable
}

func (h Handler) PersonalSettingsCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	if h.PersonalSettings == nil {
		nethttp.Error(w, "personal settings are unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	h.PersonalSettings.Command(w, r)
}

func (h Handler) ProductSettingsCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.ProductSettings == nil {
		nethttp.Error(w, "product settings are unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	h.ProductSettings.Command(w, r)
}

func (h Handler) ServiceAccountCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.SettingsRepository == nil {
		nethttp.Error(w, "service accounts are unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	var request serviceAccountCommandSignals
	if err := pagestream.ReadSignals(r, &request); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Command.Action) == "select" {
		state, err := adminsettings.LoadServiceAccounts(r.Context(), h.SettingsRepository, request.Command.AccountID)
		if err != nil {
			h.patchServiceAccountError(w, r, request.Command.AccountID, err)
			return
		}
		_ = pagestream.PatchResponse(w, r, map[string]any{"adminServiceAccounts": state})
		return
	}
	started, err := beginServiceAccountInvocation(r, request.Command)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	r = started
	actorID := ""
	if h.ReadModel.CurrentPrincipal != nil {
		if principal, ok := h.ReadModel.CurrentPrincipal(r); ok {
			actorID = principal.ID
		}
	}
	secret, err := adminsettings.ApplyServiceAccountCommandAudited(r.Context(), h.SettingsRepository, actorID, request.Command)
	if err != nil {
		h.patchServiceAccountError(w, r, request.Command.AccountID, err)
		return
	}
	state, err := adminsettings.LoadServiceAccounts(r.Context(), h.SettingsRepository, request.Command.AccountID)
	if err != nil {
		h.patchServiceAccountError(w, r, request.Command.AccountID, err)
		return
	}
	state.CreatedSecret = secret
	_ = pagestream.PatchResponse(w, r, map[string]any{"adminServiceAccounts": state})
}

func (h Handler) patchServiceAccountError(w nethttp.ResponseWriter, r *nethttp.Request, selectedID string, commandErr error) {
	state, loadErr := adminsettings.LoadServiceAccounts(r.Context(), h.SettingsRepository, strings.TrimSpace(selectedID))
	if loadErr != nil {
		state = adminsettings.ServiceAccountsSignal{Items: []adminsettings.ServiceAccountSignal{}, Secrets: []adminsettings.ServiceAccountSecretSignal{}, SelectedID: strings.TrimSpace(selectedID)}
	}
	state.Error = commandErr.Error()
	state.Loading = false
	_ = pagestream.PatchResponse(w, r, map[string]any{"adminServiceAccounts": state})
}

func beginServiceAccountInvocation(r *nethttp.Request, command adminsettings.ServiceAccountCommand) (*nethttp.Request, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	idempotencyKey := "ui:" + requestID
	begin := func(binding uicommand.Binding, start func() (context.Context, error)) (*nethttp.Request, error) {
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()); err != nil {
			return r, err
		}
		ctx, err := start()
		if err != nil {
			return r, err
		}
		return r.WithContext(ctx), nil
	}
	switch strings.TrimSpace(command.Action) {
	case "create":
		return begin(accessgen.GenUIActionCreateServicePrincipal(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenCreateServicePrincipalCommand(r.Context(), accessgen.GenCreateServicePrincipalCommandInvocation{
				Surface: apigencommand.SurfaceUI, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case "delete":
		return begin(accessgen.GenUIActionDeleteServicePrincipal(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenDeleteServicePrincipalCommand(r.Context(), accessgen.GenDeleteServicePrincipalCommandInvocation{
				Surface: apigencommand.SurfaceUI, ServicePrincipal: strings.TrimSpace(command.AccountID), RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case "create_secret":
		return begin(accessgen.GenUIActionCreateServicePrincipalSecret(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenCreateServicePrincipalSecretCommand(r.Context(), accessgen.GenCreateServicePrincipalSecretCommandInvocation{
				Surface: apigencommand.SurfaceUI, ServicePrincipal: strings.TrimSpace(command.AccountID), IdempotencyKey: idempotencyKey,
				RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	case "revoke_secret":
		return begin(accessgen.GenUIActionRevokeServicePrincipalSecret(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenRevokeServicePrincipalSecretCommand(r.Context(), accessgen.GenRevokeServicePrincipalSecretCommandInvocation{
				Surface: apigencommand.SurfaceUI, ServicePrincipal: strings.TrimSpace(command.AccountID), RequestID: requestID, CorrelationID: correlationID,
			})
			return ctx, err
		})
	default:
		return r, errors.New("unknown service account command")
	}
}

func (h Handler) AuditLogCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.SettingsRepository == nil {
		nethttp.Error(w, "audit log is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	var request auditLogCommandSignals
	if err := pagestream.ReadSignals(r, &request); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	command := adminsettings.NormalizeAuditLogCommand(request.Command)
	state, err := adminsettings.LoadAuditLog(r.Context(), h.SettingsRepository, command.Filters, command.PageToken, command.Limit)
	if err != nil {
		current := request.Current
		current.Filters = command.Filters
		h.patchAuditLogError(w, r, current, err)
		return
	}
	if command.Action == "load_more" {
		state.Items = append(append([]adminsettings.AuditEventSignal{}, request.Current.Items...), state.Items...)
		state.LoadedCount = len(state.Items)
	}
	_ = pagestream.PatchResponse(w, r, map[string]any{"adminAuditLog": state})
}

func (h Handler) patchAuditLogError(w nethttp.ResponseWriter, r *nethttp.Request, state adminsettings.AuditLogSignal, commandErr error) {
	state.Error = commandErr.Error()
	state.Loading = false
	_ = pagestream.PatchResponse(w, r, map[string]any{"adminAuditLog": state})
}

func (h Handler) BootstrapUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	active := strings.TrimSpace(r.URL.Query().Get("section"))
	if active == "" {
		active = "profile"
	}
	if (active == "profile" || active == "security" || active == "api-tokens") && h.rejectAuthoringCredential(w, r) {
		return
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
	signals := ui.AdminBootstrapSignals(active, data, h.layout(r))
	if err := h.addSettingsSignals(r, active, signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	h.patchAndWait(w, r, signals)
}

func (h Handler) rejectAuthoringCredential(w nethttp.ResponseWriter, r *nethttp.Request) bool {
	if h.CurrentCredential == nil {
		return false
	}
	credential, ok := h.CurrentCredential(r)
	if !ok || credential.Authoring == nil {
		return false
	}
	nethttp.Error(w, "personal settings require a browser session or personal API token", nethttp.StatusForbidden)
	return true
}

func (h Handler) addSettingsSignals(r *nethttp.Request, active string, signals map[string]any) error {
	switch active {
	case "profile", "security", "api-tokens":
		if h.PersonalSettings == nil {
			return nil
		}
		state, err := h.PersonalSettings.State(r)
		if err != nil {
			return err
		}
		for key, value := range personalsettings.BootstrapSignals(state) {
			signals[key] = value
		}
	case "general", "authentication", "system":
		if h.ProductSettings == nil {
			return nil
		}
		state, err := h.ProductSettings.Bootstrap(r, active)
		if err != nil {
			return err
		}
		signals["productSettings"] = productsettings.Payload(state)
		signals["productSettingsCommand"] = map[string]any{}
	case "service-accounts":
		if h.SettingsRepository == nil {
			return nil
		}
		state, err := adminsettings.LoadServiceAccounts(r.Context(), h.SettingsRepository, "")
		if err != nil {
			return err
		}
		signals["adminServiceAccounts"] = state
		signals["adminServiceAccountCommand"] = adminsettings.ServiceAccountCommand{}
	case "principals", "groups", "principal-detail", "group-detail":
		if h.SettingsRepository == nil {
			return nil
		}
		actorID := ""
		if h.ReadModel.CurrentPrincipal != nil {
			if principal, ok := h.ReadModel.CurrentPrincipal(r); ok {
				actorID = principal.ID
			}
		}
		selectedPrincipalID := strings.TrimSpace(r.URL.Query().Get("principal"))
		selectedGroupID := strings.TrimSpace(r.URL.Query().Get("group"))
		state, err := h.loadAccessAdministration(r.Context(), actorID, selectedPrincipalID, selectedGroupID)
		if err != nil {
			return err
		}
		signals["adminAccess"] = state
		signals["adminAccessCommand"] = adminsettings.AccessAdministrationCommand{}
	case "audit":
		if h.SettingsRepository == nil {
			return nil
		}
		state, err := adminsettings.LoadAuditLog(r.Context(), h.SettingsRepository, adminsettings.AuditLogFilters{}, "", 50)
		if err != nil {
			return err
		}
		signals["adminAuditLog"] = state
		signals["adminAuditLogCommand"] = adminsettings.AuditLogCommand{Action: "reset", Limit: 50}
	}
	return nil
}

func (h Handler) QueryUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.queryHistoryUpdates(w, r)
}

func (h Handler) QueryCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.queryHistoryCommand(w, r)
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
	case "storage":
		return h.readModel().StorageData(r), nil
	case "storage-detail":
		return h.readModel().StorageTableData(r, r.URL.Query().Get("schema"), r.URL.Query().Get("table"))
	case "profile", "security", "api-tokens", "general", "service-accounts", "authentication", "audit", "system":
		return h.readModel().SettingsData(r)
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
	if _, ok := webtransport.RequireClientID(w, r); !ok {
		return
	}
	updates := pagestream.NewSignalStream(w, r)
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

func (h Handler) ensureClientID(w nethttp.ResponseWriter, r *nethttp.Request) bool {
	if h.EnsureClientID != nil {
		return h.EnsureClientID(w, r)
	}
	_, ok := webtransport.RequireClientID(w, r)
	return ok
}

func (h Handler) readModel() ReadModel {
	return h.ReadModel
}
