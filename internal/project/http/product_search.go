package http

import (
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"strconv"
	"strings"

	webpage "github.com/flidai/leapview/internal/platform/web/page"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectui "github.com/flidai/leapview/internal/project/ui"
	"github.com/flidai/leapview/pkg/pagestream"
)

func (h *BrowserHandler) SearchUpdates(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.Error(w, "authentication is required", stdhttp.StatusUnauthorized)
		return
	}
	layout := webpage.Resolve(h.layout(r), webpage.Context{Active: "search", PageTitle: "Search"})
	h.ClientIDs.PatchAndWait(w, r, pagestream.SignalPatch(webpage.WithSignal(layout, map[string]any{})))
}

// ProductSearch serves the same governed catalog to the command palette and
// the shareable search page. Browser navigation asks for HTML; existing JSON
// consumers retain their asset-only default scope.
func (h *BrowserHandler) ProductSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.Error(w, "authentication is required", stdhttp.StatusUnauthorized)
		return
	}
	credential := h.currentCredential(r)
	projectID := projectgraph.ResourceID("")
	if !principal.DevBypass || typedBrowserCredential(credential) {
		var err error
		projectID, err = h.boundProject(r.Context())
		if err != nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Accept")
	html := strings.Contains(r.Header.Get("Accept"), "text/html")
	values := r.URL.Query()
	request := projectcatalog.SearchRequest{
		PrincipalID: principal.ID, DevAuthBypass: principal.DevBypass,
		Query: strings.TrimSpace(values.Get("q")), Domain: strings.TrimSpace(values.Get("domain")),
		Cursor: values.Get("cursor"), Limit: 24,
	}
	if !html {
		request.Kinds = append([]projectgraph.Kind(nil), productSearchKinds...)
	}
	if kinds := values["kind"]; len(kinds) > 0 {
		request.Kinds = nil
		for _, value := range kinds {
			kind, err := projectgraph.ParseKind(value)
			if err != nil {
				stdhttp.Error(w, "invalid search kind", stdhttp.StatusBadRequest)
				return
			}
			request.Kinds = append(request.Kinds, kind)
		}
	}
	if value := values.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > projectcatalog.MaxLimit {
			stdhttp.Error(w, "invalid search limit", stdhttp.StatusBadRequest)
			return
		}
		request.Limit = limit
	}
	var page projectcatalog.Page
	var searchError string
	if request.Query != "" || !html {
		if h.SearchCatalog == nil {
			stdhttp.Error(w, "search is temporarily unavailable", stdhttp.StatusServiceUnavailable)
			return
		}
		var err error
		page, err = searchCatalogAuthorized(r.Context(), h.SearchCatalog, request, credential, projectID)
		if err != nil {
			status := stdhttp.StatusServiceUnavailable
			if errors.Is(err, projectcatalog.ErrInvalidRequest) || errors.Is(err, projectcatalog.ErrInvalidCursor) {
				status = stdhttp.StatusBadRequest
			}
			if errors.Is(err, projectcatalog.ErrSnapshotChanged) {
				status = stdhttp.StatusConflict
			}
			if !html {
				stdhttp.Error(w, stdhttp.StatusText(status), status)
				return
			}
			// Keep the requested filters visible when an older assistant link
			// refers to a catalog generation that has since been replaced.
			searchError = "Search is temporarily unavailable. Try again."
			if status == stdhttp.StatusBadRequest {
				searchError = "This search request is no longer valid. Adjust the filters or return to the first page."
			}
			if status == stdhttp.StatusConflict {
				searchError = "The project changed since this search. Return to the first page to view current results."
			}
		}
	}
	if html {
		items := make([]projectui.SearchResult, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, projectui.SearchResult{
				ID: item.Ref.ID.String(), Kind: string(item.Ref.Kind),
				Title:       browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()),
				Description: item.Description, Domain: item.Domain, Href: productSearchHref(item),
			})
		}
		kinds := make([]string, 0, len(request.Kinds))
		for _, kind := range request.Kinds {
			kinds = append(kinds, string(kind))
		}
		writeDocument(w, projectui.SearchPage(projectui.SearchPageOptions{
			Query: request.Query, Domain: request.Domain, Kinds: kinds, Limit: request.Limit,
			Cursor: request.Cursor, NextCursor: page.NextCursor, Results: items, Error: searchError,
		}, h.csrf(r), h.layout(r)))
		return
	}
	items := make([]productSearchResult, 0, len(page.Items))
	for _, item := range page.Items {
		if result, ok := productSearchResultFor(item); ok {
			items = append(items, result)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Items      []productSearchResult `json:"items"`
		NextCursor string                `json:"nextCursor,omitempty"`
	}{Items: items, NextCursor: page.NextCursor})
}
