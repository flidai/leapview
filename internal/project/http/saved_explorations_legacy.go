package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"strings"

	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/google/uuid"
)

const maxSavedExplorationRequestBytes = 128 << 10

type savedExplorationResponse struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Href      string `json:"href"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ListSavedExplorations returns only items owned by the authenticated actor
// in the currently bound project and environment.
func (h *BrowserHandler) ListSavedExplorations(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	scope, status := h.savedExplorationScope(r)
	if status != 0 {
		writeSavedExplorationError(w, status)
		return
	}
	items, err := h.LegacySavedExplorations.ListSavedExplorations(r.Context(), scope)
	if err != nil {
		writeSavedExplorationError(w, stdhttp.StatusServiceUnavailable)
		return
	}
	response := struct {
		Items []savedExplorationResponse `json:"items"`
	}{Items: make([]savedExplorationResponse, 0, len(items))}
	for _, item := range items {
		response.Items = append(response.Items, savedExplorationResponse{
			ID: item.ID, Title: item.Title, Href: savedExplorationHref(item.ID), UpdatedAt: item.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

// CreateSavedExploration accepts a same-origin Data Explorer URL, normalizes
// it into a governed query command, and persists only that command and title.
func (h *BrowserHandler) CreateSavedExploration(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindSemanticModel}) {
		return
	}
	scope, status := h.savedExplorationScope(r)
	if status != 0 {
		writeSavedExplorationError(w, status)
		return
	}
	var input struct {
		Title       string `json:"title"`
		ExplorerURL string `json:"explorerUrl"`
	}
	body := stdhttp.MaxBytesReader(w, r.Body, maxSavedExplorationRequestBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeSavedExplorationError(w, stdhttp.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeSavedExplorationError(w, stdhttp.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(input.Title)
	if title == "" || len(title) > 255 {
		writeSavedExplorationError(w, stdhttp.StatusBadRequest)
		return
	}
	command, err := savedExplorationCommandFromURL(input.ExplorerURL)
	if err != nil {
		writeSavedExplorationError(w, stdhttp.StatusBadRequest)
		return
	}
	command.Mode = projectsignals.Pointer("explore")
	command.Limit = dataExplorerDefaultLimit
	command.Count = dataExplorerDefaultLimit
	_, explorer, ok := h.dataExplorerSignalsForRestoredCommand(w, r, command, false)
	if !ok {
		return
	}
	canonicalJSON, err := json.Marshal(explorer.Explore.Command)
	if err != nil {
		writeSavedExplorationError(w, stdhttp.StatusInternalServerError)
		return
	}
	id, err := uuid.NewV7()
	if err != nil {
		writeSavedExplorationError(w, stdhttp.StatusServiceUnavailable)
		return
	}
	item, err := h.LegacySavedExplorations.CreateSavedExploration(r.Context(), scope, id.String(), title, string(canonicalJSON))
	if err != nil {
		if errors.Is(err, projectview.ErrSavedExplorationInvalid) {
			writeSavedExplorationError(w, stdhttp.StatusBadRequest)
			return
		}
		writeSavedExplorationError(w, stdhttp.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(stdhttp.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		Item savedExplorationResponse `json:"item"`
	}{Item: savedExplorationResponse{
		ID: item.ID, Title: item.Title, Href: savedExplorationHref(item.ID), CreatedAt: item.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}})
}

func (h *BrowserHandler) savedExplorationScope(r *stdhttp.Request) (projectview.SavedExplorationScope, int) {
	if h == nil || h.LegacySavedExplorations == nil {
		return projectview.SavedExplorationScope{}, stdhttp.StatusServiceUnavailable
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return projectview.SavedExplorationScope{}, stdhttp.StatusUnauthorized
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil || strings.TrimSpace(h.Environment) == "" {
		return projectview.SavedExplorationScope{}, stdhttp.StatusServiceUnavailable
	}
	return projectview.SavedExplorationScope{ProjectID: projectID, Environment: strings.TrimSpace(h.Environment), PrincipalID: strings.TrimSpace(principal.ID)}, 0
}

func (h *BrowserHandler) loadSavedExploration(w stdhttp.ResponseWriter, r *stdhttp.Request, id string) (projectsignals.DataExplorerCommand, bool) {
	scope, status := h.savedExplorationScope(r)
	if status != 0 {
		writeSavedExplorationError(w, status)
		return projectsignals.DataExplorerCommand{}, false
	}
	if uuid.Validate(id) != nil {
		stdhttp.NotFound(w, r)
		return projectsignals.DataExplorerCommand{}, false
	}
	item, err := h.LegacySavedExplorations.GetSavedExploration(r.Context(), scope, id)
	if errors.Is(err, projectview.ErrSavedExplorationNotFound) {
		stdhttp.NotFound(w, r)
		return projectsignals.DataExplorerCommand{}, false
	}
	if err != nil {
		writeSavedExplorationError(w, stdhttp.StatusServiceUnavailable)
		return projectsignals.DataExplorerCommand{}, false
	}
	var query projectsignals.DataExploreCommand
	decoder := json.NewDecoder(bytes.NewBufferString(item.CommandJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&query); err != nil {
		writeSavedExplorationError(w, stdhttp.StatusServiceUnavailable)
		return projectsignals.DataExplorerCommand{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || query.Dimensions == nil || query.Metrics == nil || query.Filters == nil || query.Sort == nil {
		writeSavedExplorationError(w, stdhttp.StatusServiceUnavailable)
		return projectsignals.DataExplorerCommand{}, false
	}
	return projectsignals.DataExplorerCommand{Mode: projectsignals.Pointer("explore"), Limit: dataExplorerDefaultLimit, Count: dataExplorerDefaultLimit, Block: projectsignals.Pointer("all"), Explore: &query}, true
}

func savedExplorationCommandFromURL(value string) (projectsignals.DataExplorerCommand, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Path != "/explore" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return projectsignals.DataExplorerCommand{}, fmt.Errorf("explorerUrl must be a same-origin /explore URL")
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return projectsignals.DataExplorerCommand{}, fmt.Errorf("explorerUrl query is invalid")
	}
	allowed := map[string]bool{"v": true, "mode": true, "semanticModel": true, "dataset": true, "dimension": true, "metric": true, "filter": true, "sort": true, "time": true, "limit": true}
	for key := range values {
		if !allowed[key] {
			return projectsignals.DataExplorerCommand{}, fmt.Errorf("unsupported explorerUrl parameter %q", key)
		}
	}
	for _, key := range []string{"v", "mode", "semanticModel", "dataset", "time", "limit"} {
		if len(values[key]) > 1 {
			return projectsignals.DataExplorerCommand{}, fmt.Errorf("explorerUrl parameter %q may be supplied once", key)
		}
	}
	if len(values["mode"]) != 1 || strings.TrimSpace(values.Get("mode")) != "explore" {
		return projectsignals.DataExplorerCommand{}, fmt.Errorf("explorerUrl must select explore mode")
	}
	query, err := dataExploreCommandFromQuery(values)
	if err != nil {
		return projectsignals.DataExplorerCommand{}, err
	}
	return projectsignals.DataExplorerCommand{Mode: projectsignals.Pointer("explore"), Explore: &query}, nil
}

func savedExplorationHref(id string) string {
	return "/explore?saved=" + url.QueryEscape(id)
}

func writeSavedExplorationError(w stdhttp.ResponseWriter, status int) {
	stdhttp.Error(w, stdhttp.StatusText(status), status)
}

// withSavedExplorationURL restores the stored semantic command rather than
// trusting query fields supplied alongside the saved identifier.
func (h *BrowserHandler) withSavedExplorationURL(w stdhttp.ResponseWriter, r *stdhttp.Request, executeQuery bool) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeSavedExplorationError(w, stdhttp.StatusBadRequest)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	ids := values["saved"]
	if len(ids) != 1 || len(values) != 1 {
		writeSavedExplorationError(w, stdhttp.StatusBadRequest)
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	command, ok := h.loadSavedExploration(w, r, ids[0])
	if !ok {
		return projectsignals.DataExplorerPageSignal{}, projectsignals.DataExplorerSignal{}, false
	}
	return h.dataExplorerSignalsForRestoredCommand(w, r, command, executeQuery)
}
