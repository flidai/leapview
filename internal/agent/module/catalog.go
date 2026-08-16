package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// CatalogService is the only agent catalog implementation. The project
// catalog owns graph traversal, lease acquisition, and exact generation-bound
// authorization; this adapter only translates the model-facing DTOs.
type CatalogService struct {
	project *projectcatalog.Service
}

type CatalogConfig struct {
	ProjectCatalog *projectcatalog.Service
}

func BuildCatalog(config CatalogConfig) agenttools.Catalog {
	return CatalogService{project: config.ProjectCatalog}
}

func (c CatalogService) Search(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogSearchRequest) (agenttools.CatalogPage, error) {
	if c.project == nil {
		return agenttools.CatalogPage{}, &agenttools.CatalogError{Code: "catalog_unavailable", Message: "project catalog is not configured"}
	}
	kinds, err := catalogKinds(request.Kinds)
	if err != nil {
		return agenttools.CatalogPage{}, err
	}
	page, err := c.project.Search(ctx, projectcatalog.SearchRequest{PrincipalID: scope.PrincipalID, Query: request.Query, Kinds: kinds, Domain: request.Domain, Cursor: request.Cursor, Limit: request.Limit})
	if err != nil {
		return agenttools.CatalogPage{}, catalogError(err)
	}
	return projectPage(page), nil
}

func (c CatalogService) List(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogListRequest) (agenttools.CatalogPage, error) {
	if c.project == nil {
		return agenttools.CatalogPage{}, &agenttools.CatalogError{Code: "catalog_unavailable", Message: "project catalog is not configured"}
	}
	kinds, err := catalogKinds(request.ChildKinds)
	if err != nil {
		return agenttools.CatalogPage{}, err
	}
	var parent *projectcatalog.Ref
	if request.Parent != nil {
		ref, err := catalogRef(request.Parent)
		if err != nil {
			return agenttools.CatalogPage{}, err
		}
		parent = &ref
	}
	page, err := c.project.List(ctx, projectcatalog.ListRequest{PrincipalID: scope.PrincipalID, Parent: parent, Kinds: kinds, Domain: request.Domain, Cursor: request.Cursor, Limit: request.Limit})
	if err != nil {
		return agenttools.CatalogPage{}, catalogError(err)
	}
	return projectPage(page), nil
}

func (c CatalogService) Get(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogGetRequest) (agenttools.CatalogGetResult, error) {
	if c.project == nil {
		return agenttools.CatalogGetResult{}, &agenttools.CatalogError{Code: "catalog_unavailable", Message: "project catalog is not configured"}
	}
	ref, err := catalogRef(&request.Ref)
	if err != nil {
		return agenttools.CatalogGetResult{}, err
	}
	capability := access.CapabilityResourceRead
	if ref.Kind == projectgraph.KindProject {
		capability = access.CapabilityProjectAdmin
	}
	result, err := c.project.Resolve(ctx, scope.PrincipalID, ref, capability)
	if err != nil {
		return agenttools.CatalogGetResult{}, catalogError(err)
	}
	return agenttools.CatalogGetResult{Item: projectItem(result), Details: map[string]any{
		"kind": string(result.Ref.Kind),
		"metadata": map[string]any{
			"id": result.Ref.ID.String(), "domain": result.Domain, "owner": result.Owner,
		},
	}}, nil
}

func catalogRef(ref *agentcontracts.CatalogRef) (projectcatalog.Ref, error) {
	if ref == nil {
		return projectcatalog.Ref{}, &agenttools.CatalogError{Code: "invalid_arguments", Message: "catalog ref is required"}
	}
	id, err := projectgraph.NewResourceID(strings.TrimSpace(ref.ID))
	if err != nil {
		return projectcatalog.Ref{}, &agenttools.CatalogError{Code: "invalid_arguments", Message: fmt.Sprintf("invalid catalog ref id: %v", err)}
	}
	kind, err := projectgraph.ParseKind(string(ref.Kind))
	if err != nil {
		return projectcatalog.Ref{}, &agenttools.CatalogError{Code: "invalid_arguments", Message: err.Error()}
	}
	return projectcatalog.Ref{ID: id, Kind: kind}, nil
}

func catalogKinds(values []agenttools.CatalogType) ([]projectgraph.Kind, error) {
	out := make([]projectgraph.Kind, 0, len(values))
	for _, value := range values {
		kind, err := projectgraph.ParseKind(string(value))
		if err != nil {
			return nil, &agenttools.CatalogError{Code: "invalid_arguments", Message: err.Error()}
		}
		out = append(out, kind)
	}
	return out, nil
}

func projectPage(page projectcatalog.Page) agenttools.CatalogPage {
	items := make([]agenttools.CatalogItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, projectItem(item))
	}
	return agenttools.CatalogPage{Items: items, Count: len(items), HasMore: page.NextCursor != "", NextCursor: page.NextCursor}
}

func projectItem(item projectcatalog.Result) agenttools.CatalogItem {
	return agenttools.CatalogItem{Ref: agenttools.CatalogRef{ID: item.Ref.ID.String(), Kind: agenttools.CatalogType(item.Ref.Kind)}, Name: item.Name, DisplayName: item.DisplayName, Description: item.Description, Domain: item.Domain, Owner: item.Owner, Tags: append([]string(nil), item.Tags...)}
}

func catalogError(err error) error {
	if err == nil {
		return nil
	}
	code := "catalog_failed"
	switch {
	case errors.Is(err, projectcatalog.ErrNotFound), errors.Is(err, projectcatalog.ErrUnauthorized):
		code = "catalog_not_found"
	case errors.Is(err, projectcatalog.ErrInvalidRequest), errors.Is(err, projectcatalog.ErrInvalidCursor):
		code = "invalid_arguments"
	case errors.Is(err, projectcatalog.ErrUnavailable):
		code = "catalog_unavailable"
	}
	return &agenttools.CatalogError{Code: code, Message: fmt.Sprintf("%s: %s", code, strings.TrimSpace(err.Error()))}
}
