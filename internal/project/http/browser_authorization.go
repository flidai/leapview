package http

import (
	"context"
	"fmt"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	dashboardauthoringcatalog "github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// searchCatalogAuthorized fills a product-search page from authorized results,
// rather than filtering only the first underlying page. This prevents a typed
// credential's denied matches from crowding later authorized results out of the
// response while preserving the catalog's snapshot-bound opaque cursor checks.
func searchCatalogAuthorized(ctx context.Context, catalog ProductSearchCatalog, request projectcatalog.SearchRequest, credential *access.APICredential, projectID projectgraph.ResourceID) (projectcatalog.Page, error) {
	if catalog == nil {
		return projectcatalog.Page{}, projectcatalog.ErrUnavailable
	}
	limit := request.Limit
	items := make([]projectcatalog.Result, 0, limit)
	seenCursors := map[string]struct{}{}
	for pages := 0; ; pages++ {
		if pages >= 10000 {
			return projectcatalog.Page{}, fmt.Errorf("catalog search pagination exceeded safety bound")
		}
		page, err := catalog.Search(ctx, request)
		if err != nil {
			return projectcatalog.Page{}, err
		}
		page = filterCatalogPageForCredential(page, credential, projectID)
		remaining := limit - len(items)
		if remaining > len(page.Items) {
			remaining = len(page.Items)
		}
		items = append(items, page.Items[:remaining]...)
		if len(items) >= limit || page.NextCursor == "" {
			return projectcatalog.Page{Items: items}, nil
		}
		if _, seen := seenCursors[page.NextCursor]; seen {
			return projectcatalog.Page{}, fmt.Errorf("catalog search pagination cursor repeated")
		}
		seenCursors[page.NextCursor] = struct{}{}
		request.Cursor = page.NextCursor
	}
}

func (h *BrowserHandler) currentCredential(r *stdhttp.Request) *access.APICredential {
	if h == nil || h.CurrentCredential == nil || r == nil {
		return nil
	}
	credential, ok := h.CurrentCredential(r)
	if !ok {
		return nil
	}
	return &credential
}

func typedBrowserCredential(credential *access.APICredential) bool {
	return credential != nil && strings.TrimSpace(credential.Token.ID) != "" &&
		(credential.Token.PermissionProfile != "" || credential.Token.Permissions != nil)
}

func catalogReadAction(kind projectgraph.Kind) (access.Action, bool) {
	switch kind {
	case projectgraph.KindDashboard:
		return access.ActionDashboardRead, true
	case projectgraph.KindSemanticModel:
		return access.ActionSemanticRead, true
	case projectgraph.KindModel:
		return access.ActionModelRead, true
	case projectgraph.KindSource:
		return access.ActionSourceRead, true
	case projectgraph.KindPipeline:
		return access.ActionPipelineRead, true
	case projectgraph.KindConnection:
		return access.ActionConnectionRead, true
	default:
		// ProjectNamespace and any future graph kinds have no implicit typed
		// browser-read action. A typed credential must name a supported action.
		return "", false
	}
}

func typedCatalogRefDecision(credential *access.APICredential, projectID projectgraph.ResourceID, ref projectcatalog.Ref) (typed, allowed bool) {
	if !typedBrowserCredential(credential) {
		return false, true
	}
	action, ok := catalogReadAction(ref.Kind)
	if !ok {
		return true, false
	}
	resource, err := access.NewResourceRef(ref.ID, ref.Kind)
	if err != nil || projectID.Validate() != nil {
		return true, false
	}
	pair, err := access.NewExactPermissionPair(action, projectID, resource)
	if err != nil {
		return true, false
	}
	return true, access.PermissionSetAllows(credential.Token.Permissions, pair)
}

func filterCatalogPageForCredential(page projectcatalog.Page, credential *access.APICredential, projectID projectgraph.ResourceID) projectcatalog.Page {
	if !typedBrowserCredential(credential) {
		return page
	}
	filtered := make([]projectcatalog.Result, 0, len(page.Items))
	for _, item := range page.Items {
		if _, allowed := typedCatalogRefDecision(credential, projectID, item.Ref); allowed {
			filtered = append(filtered, item)
		}
	}
	page.Items = filtered
	return page
}

func filterDashboardCatalogForCredential(items []dashboardauthoringcatalog.Dashboard, credential *access.APICredential, projectID projectgraph.ResourceID) []dashboardauthoringcatalog.Dashboard {
	if !typedBrowserCredential(credential) {
		return items
	}
	filtered := make([]dashboardauthoringcatalog.Dashboard, 0, len(items))
	for _, item := range items {
		ref := projectcatalog.Ref{ID: item.ID, Kind: projectgraph.KindDashboard}
		if _, allowed := typedCatalogRefDecision(credential, projectID, ref); allowed {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func listCatalogAll(ctx context.Context, catalog CatalogAuthorizer, principalID string, devAuthBypass bool, kinds []projectgraph.Kind, credential *access.APICredential, projectID projectgraph.ResourceID) (projectcatalog.Page, error) {
	if catalog == nil {
		return projectcatalog.Page{}, projectcatalog.ErrUnavailable
	}
	items := make([]projectcatalog.Result, 0)
	cursor := ""
	seenCursors := map[string]struct{}{}
	for pages := 0; ; pages++ {
		if pages >= 10000 {
			return projectcatalog.Page{}, fmt.Errorf("catalog pagination exceeded safety bound")
		}
		page, err := catalog.List(ctx, projectcatalog.ListRequest{PrincipalID: principalID, DevAuthBypass: devAuthBypass, Kinds: kinds, Limit: projectcatalog.MaxLimit, Cursor: cursor})
		if err != nil {
			return projectcatalog.Page{}, err
		}
		page = filterCatalogPageForCredential(page, credential, projectID)
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			return projectcatalog.Page{Items: items}, nil
		}
		if _, seen := seenCursors[page.NextCursor]; seen {
			return projectcatalog.Page{}, fmt.Errorf("catalog pagination cursor repeated")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}
