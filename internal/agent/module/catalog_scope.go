package module

import (
	"context"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// credentialCatalog applies the agent credential ceiling after the principal
// catalog has resolved a page. The catalog service intentionally authorizes by
// principal; this adapter prevents a restricted agent token from inheriting
// the principal's complete search/list/get view.
type credentialCatalog struct {
	base agenttools.Catalog
}

func (c credentialCatalog) Search(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogSearchRequest) (agenttools.CatalogPage, error) {
	page, err := c.base.Search(ctx, scope, request)
	if err != nil {
		return agenttools.CatalogPage{}, err
	}
	page.Items = filterCatalogItems(scope, page.Items)
	page.Count = len(page.Items)
	return page, nil
}

func (c credentialCatalog) List(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogListRequest) (agenttools.CatalogPage, error) {
	if request.Parent != nil {
		id, err := projectgraph.NewResourceID(request.Parent.ID)
		allowed := false
		if err == nil && projectgraph.Kind(request.Parent.Kind) == projectgraph.KindProjectNamespace {
			allowed = CredentialAllowsProjectSettingsRead(moduleScopeFromTools(scope), id)
		} else if err == nil {
			allowed = CredentialAllowsResource(moduleScopeFromTools(scope), id, projectgraph.Kind(request.Parent.Kind), access.CapabilityResourceRead)
		}
		if !allowed {
			return agenttools.CatalogPage{}, &agenttools.CatalogError{Code: "catalog_not_found", Message: "resource is unknown or unauthorized"}
		}
	}
	page, err := c.base.List(ctx, scope, request)
	if err != nil {
		return agenttools.CatalogPage{}, err
	}
	page.Items = filterCatalogItems(scope, page.Items)
	page.Count = len(page.Items)
	return page, nil
}

func CredentialAllowsProjectSettingsRead(scope Scope, id projectgraph.ResourceID) bool {
	if !scope.Credential.Restricted || scope.DevAuthBypass {
		return true
	}
	if scope.Credential.PermissionProfile != access.PermissionCatalogProfile {
		return false
	}
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(scope.ProjectID))
	if err != nil || projectID != id {
		return false
	}
	pair, err := access.NewProjectPermissionPair(access.ActionProjectSettingsRead, projectID)
	if err != nil {
		return false
	}
	required, err := access.RequiredPermissionPairs(pair)
	return err == nil && permissionPairsAllowAll(scope.Credential.Permissions, required)
}

func (c credentialCatalog) Get(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogGetRequest) (agenttools.CatalogGetResult, error) {
	ref := request.Ref
	id, err := projectgraph.NewResourceID(ref.ID)
	if err != nil || !CredentialAllowsResource(moduleScopeFromTools(scope), id, projectgraph.Kind(ref.Kind), access.CapabilityResourceRead) {
		return agenttools.CatalogGetResult{}, &agenttools.CatalogError{Code: "catalog_not_found", Message: "resource is unknown or unauthorized"}
	}
	return c.base.Get(ctx, scope, request)
}

func filterCatalogItems(scope agenttools.Scope, items []agenttools.CatalogItem) []agenttools.CatalogItem {
	filtered := make([]agenttools.CatalogItem, 0, len(items))
	for _, item := range items {
		id, err := projectgraph.NewResourceID(item.Ref.ID)
		if err != nil {
			continue
		}
		if CredentialAllowsResource(moduleScopeFromTools(scope), id, projectgraph.Kind(item.Ref.Kind), access.CapabilityResourceRead) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

var _ agenttools.Catalog = credentialCatalog{}
