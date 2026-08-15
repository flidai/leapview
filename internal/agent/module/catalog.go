package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/workspace"
	productsearch "github.com/flidai/leapview/internal/workspace/search"
)

const catalogListCursorPrefix = "cl1"

var catalogSearchTypes = []productsearch.Type{
	productsearch.TypeWorkspace,
	productsearch.TypeDashboard,
	productsearch.TypePage,
	productsearch.TypeVisual,
	productsearch.TypeFilter,
	productsearch.TypeSemanticModel,
	productsearch.TypeSemanticTable,
	productsearch.TypeField,
	productsearch.TypeMeasure,
}

type CatalogService struct {
	search              catalogSearchService
	environment         string
	workspaces          workspace.ReadModel
	rootMetrics         queryruntime.Metrics
	metricsForWorkspace func(string) (queryruntime.Metrics, bool)
	authorizeAnyObject  func(context.Context, string, access.Privilege, []access.ObjectRef) (bool, error)
	recordAudit         func(context.Context, access.AuditEventInput) error
	skipAuthorization   bool
	signCursor          func(string, []byte) string
	verifyCursor        func(string, string) ([]byte, error)
}

type CatalogConfig struct {
	Search              SearchPort
	Environment         string
	Workspaces          workspace.ReadModel
	RootMetrics         queryruntime.Metrics
	MetricsForWorkspace func(string) (queryruntime.Metrics, bool)
	AuthorizeAnyObject  func(context.Context, string, access.Privilege, []access.ObjectRef) (bool, error)
	RecordAudit         func(context.Context, access.AuditEventInput) error
	SkipAuthorization   bool
	SignCursor          func(string, []byte) string
	VerifyCursor        func(string, string) ([]byte, error)
}

func resolveDashboard(metrics queryruntime.Metrics, dashboardID string) (dashboardresolver.Resolved, bool) {
	if metrics == nil || metrics.Resolver() == nil {
		return dashboardresolver.Resolved{}, false
	}
	resolved, err := metrics.Resolver().Resolve(dashboardID)
	return resolved, err == nil
}

func BuildCatalog(config CatalogConfig) agenttools.Catalog {
	return CatalogService{
		search: config.Search, environment: config.Environment,
		workspaces: config.Workspaces, rootMetrics: config.RootMetrics,
		metricsForWorkspace: config.MetricsForWorkspace,
		authorizeAnyObject:  config.AuthorizeAnyObject,
		recordAudit:         config.RecordAudit,
		skipAuthorization:   config.SkipAuthorization,
		signCursor:          config.SignCursor,
		verifyCursor:        config.VerifyCursor,
	}
}

type catalogSearchService interface {
	Search(context.Context, productsearch.Subject, productsearch.Query) (productsearch.Page, error)
	ResolveSearchReferences(context.Context, productsearch.Subject, string, []productsearch.Reference) ([]productsearch.Result, error)
}

type activeWorkspaceRepository interface {
	ListWithActiveMetadata(context.Context, string) ([]workspace.Summary, error)
	ByIDWithActiveMetadata(context.Context, workspace.WorkspaceID, string) (workspace.Summary, error)
}

func (c CatalogService) workspaceMetrics(workspaceID string) (queryruntime.Metrics, bool) {
	if c.metricsForWorkspace != nil {
		return c.metricsForWorkspace(workspaceID)
	}
	if c.rootMetrics == nil || c.rootMetrics.Catalog().Workspace.ID != workspaceID {
		return nil, false
	}
	return c.rootMetrics, true
}

func (c CatalogService) recordCatalogAudit(ctx context.Context, scope agenttools.Scope, workspaceID, status string, cause error) {
	if c.recordAudit == nil {
		return
	}
	metadata := dataquery.MetadataFromContext(ctx)
	payload := map[string]any{}
	if cause != nil {
		payload["error"] = cause.Error()
	}
	encoded, _ := json.Marshal(payload)
	_ = c.recordAudit(ctx, access.AuditEventInput{
		WorkspaceID: workspaceID, PrincipalID: scope.PrincipalID,
		Action: "agent_tool.called", TargetType: "agent_tool", TargetID: agenttools.CatalogGetToolName,
		Privilege: access.PrivilegeViewItem, Status: status,
		RequestID: metadata.RequestID, CorrelationID: metadata.CorrelationID, MetadataJSON: string(encoded),
	})
}

func containsSearchPrivilege(privileges []string, wanted access.Privilege) bool {
	for _, privilege := range privileges {
		if strings.EqualFold(strings.TrimSpace(privilege), string(wanted)) {
			return true
		}
	}
	return false
}

func catalogFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (c CatalogService) Search(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogSearchRequest) (agenttools.CatalogPage, error) {
	if c.search == nil {
		return agenttools.CatalogPage{}, errors.New("catalog search is not configured")
	}
	types := make([]productsearch.Type, 0, len(request.Types))
	for _, typ := range request.Types {
		types = append(types, productsearch.Type(typ))
	}
	query := productsearch.Query{
		Text:         request.Query,
		Environment:  c.environment,
		Workspaces:   append([]string(nil), request.WorkspaceIDs...),
		Types:        types,
		AllowedTypes: append([]productsearch.Type(nil), catalogSearchTypes...),
		Limit:        request.Limit,
		Cursor:       request.Cursor,
	}
	if request.Context != nil {
		query.Context.DashboardID = request.Context.DashboardID
		query.Context.PageID = request.Context.PageID
	}
	if len(request.WorkspaceIDs) == 1 {
		query.Context.WorkspaceID = request.WorkspaceIDs[0]
	} else if scope.WorkspaceID != "" {
		query.Context.WorkspaceID = scope.WorkspaceID
	}
	page, err := c.search.Search(ctx, catalogSearchSubject(scope), query)
	if err != nil {
		return agenttools.CatalogPage{}, catalogSearchError(err)
	}
	return agenttools.CatalogPage{Items: catalogItems(page.Items), NextCursor: page.NextCursor}, nil
}

func (c CatalogService) List(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogListRequest) (agenttools.CatalogPage, error) {
	items, err := c.listItems(ctx, scope, request)
	if err != nil {
		return agenttools.CatalogPage{}, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		if left != right {
			return left < right
		}
		if items[i].Ref.Type != items[j].Ref.Type {
			return items[i].Ref.Type < items[j].Ref.Type
		}
		return items[i].Ref.ID < items[j].Ref.ID
	})
	snapshot := catalogItemsSnapshot(items)
	offset, err := c.decodeCatalogListCursor(request.Cursor, scope, request, snapshot)
	if err != nil {
		return agenttools.CatalogPage{}, err
	}
	if offset > len(items) {
		return agenttools.CatalogPage{}, &agenttools.CatalogError{Code: "invalid_arguments", Message: "catalog cursor offset is invalid"}
	}
	end := offset + request.Limit
	if end > len(items) {
		end = len(items)
	}
	page := agenttools.CatalogPage{Items: append([]agenttools.CatalogItem(nil), items[offset:end]...)}
	if end < len(items) {
		if c.signCursor == nil {
			return agenttools.CatalogPage{}, &agenttools.CatalogError{Code: "invalid_arguments", Message: "catalog cursor codec is unavailable"}
		}
		page.NextCursor = c.encodeCatalogListCursor(scope, request, snapshot, end)
	}
	return page, nil
}

func (c CatalogService) Get(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogGetRequest) (agenttools.CatalogGetResult, error) {
	result, err := c.get(ctx, scope, request)
	status := "success"
	if err != nil {
		status = "error"
		var catalogErr *agenttools.CatalogError
		if errors.As(err, &catalogErr) && catalogErr.Code == "catalog_not_found" {
			status = "denied"
		}
	}
	if !scope.DevAuthBypass {
		c.recordCatalogAudit(ctx, scope, request.Ref.WorkspaceID, status, err)
	}
	return result, err
}

func (c CatalogService) get(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogGetRequest) (agenttools.CatalogGetResult, error) {
	if request.Ref.Type == agenttools.CatalogTypeWorkspace {
		item, summary, ok, err := c.workspaceItem(ctx, scope, request.Ref.WorkspaceID)
		if err != nil {
			return agenttools.CatalogGetResult{}, err
		}
		if !ok {
			return agenttools.CatalogGetResult{}, catalogNotFound()
		}
		return agenttools.CatalogGetResult{
			Item: item,
			Details: map[string]any{
				"type":                 string(agenttools.CatalogTypeWorkspace),
				"activeServingStateId": string(summary.ActiveServingStateID),
			},
		}, nil
	}
	result, ok, err := c.resolveOne(ctx, scope, request.Ref)
	if err != nil {
		return agenttools.CatalogGetResult{}, err
	}
	if !ok {
		return agenttools.CatalogGetResult{}, catalogNotFound()
	}
	item := catalogItem(result)
	location, err := catalogGetLocation(request, item.Locations)
	if err != nil {
		return agenttools.CatalogGetResult{}, err
	}
	details, err := c.details(ctx, scope, request.Ref, location)
	if err != nil {
		return agenttools.CatalogGetResult{}, err
	}
	return agenttools.CatalogGetResult{Item: item, Details: details}, nil
}

func (c CatalogService) listItems(ctx context.Context, scope agenttools.Scope, request agenttools.CatalogListRequest) ([]agenttools.CatalogItem, error) {
	if request.Parent == nil {
		return c.listWorkspaceItems(ctx, scope)
	}
	parent := *request.Parent
	if parent.Type == agenttools.CatalogTypeWorkspace {
		if _, _, ok, err := c.workspaceItem(ctx, scope, parent.WorkspaceID); err != nil {
			return nil, err
		} else if !ok {
			return nil, catalogNotFound()
		}
	} else if _, ok, err := c.resolveOne(ctx, scope, parent); err != nil {
		return nil, err
	} else if !ok {
		return nil, catalogNotFound()
	}
	references, err := c.childReferences(parent, request.ChildTypes)
	if err != nil {
		return nil, err
	}
	if len(references) == 0 {
		return []agenttools.CatalogItem{}, nil
	}
	if c.search == nil {
		return nil, errors.New("catalog search is not configured")
	}
	results, err := c.search.ResolveSearchReferences(ctx, catalogSearchSubject(scope), c.environment, references)
	if err != nil {
		return nil, err
	}
	return catalogItems(results), nil
}

func (c CatalogService) listWorkspaceItems(ctx context.Context, scope agenttools.Scope) ([]agenttools.CatalogItem, error) {
	var summaries []workspace.Summary
	var err error
	if c.workspaces != nil {
		if active, ok := c.workspaces.(activeWorkspaceRepository); ok {
			summaries, err = active.ListWithActiveMetadata(ctx, c.environment)
		} else {
			summaries, err = c.workspaces.List(ctx)
		}
		if err != nil {
			return nil, err
		}
	} else if c.rootMetrics != nil {
		catalog := c.rootMetrics.Catalog()
		summaries = []workspace.Summary{{
			ID: workspace.WorkspaceID(catalog.Workspace.ID), Title: catalog.Workspace.Title, Description: catalog.Workspace.Description,
		}}
	}
	items := make([]agenttools.CatalogItem, 0, len(summaries))
	for _, summary := range summaries {
		allowed, err := c.canViewWorkspace(ctx, scope, string(summary.ID))
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		items = append(items, catalogWorkspaceItem(summary))
	}
	return items, nil
}

func (c CatalogService) workspaceItem(ctx context.Context, scope agenttools.Scope, workspaceID string) (agenttools.CatalogItem, workspace.Summary, bool, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	allowed, err := c.canViewWorkspace(ctx, scope, workspaceID)
	if err != nil || !allowed {
		return agenttools.CatalogItem{}, workspace.Summary{}, false, err
	}
	if c.workspaces != nil {
		var summary workspace.Summary
		var err error
		if active, ok := c.workspaces.(activeWorkspaceRepository); ok {
			summary, err = active.ByIDWithActiveMetadata(ctx, workspace.WorkspaceID(workspaceID), c.environment)
		} else {
			summary, err = c.workspaces.ByID(ctx, workspace.WorkspaceID(workspaceID))
		}
		if err != nil {
			if errors.Is(err, workspace.ErrNotFound) {
				return agenttools.CatalogItem{}, workspace.Summary{}, false, nil
			}
			return agenttools.CatalogItem{}, workspace.Summary{}, false, err
		}
		return catalogWorkspaceItem(summary), summary, true, nil
	}
	metrics, ok := c.workspaceMetrics(workspaceID)
	if !ok || metrics == nil {
		return agenttools.CatalogItem{}, workspace.Summary{}, false, nil
	}
	value := metrics.Catalog().Workspace
	summary := workspace.Summary{ID: workspace.WorkspaceID(value.ID), Title: value.Title, Description: value.Description}
	return catalogWorkspaceItem(summary), summary, true, nil
}

func (c CatalogService) canViewWorkspace(ctx context.Context, scope agenttools.Scope, workspaceID string) (bool, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(scope.PrincipalID) == "" {
		return false, nil
	}
	if scope.Credential.WorkspaceID != "" && scope.Credential.WorkspaceID != workspaceID {
		return false, nil
	}
	if scope.Credential.Restricted && !containsSearchPrivilege(scope.Credential.Privileges, access.PrivilegeViewItem) {
		return false, nil
	}
	if scope.DevAuthBypass || c.skipAuthorization {
		return true, nil
	}
	if c.authorizeAnyObject == nil {
		return false, nil
	}
	return c.authorizeAnyObject(ctx, scope.PrincipalID, access.PrivilegeViewItem, []access.ObjectRef{access.WorkspaceObject(workspaceID)})
}

func (c CatalogService) resolveOne(ctx context.Context, scope agenttools.Scope, ref agenttools.CatalogRef) (productsearch.Result, bool, error) {
	if c.search == nil {
		return productsearch.Result{}, false, errors.New("catalog search is not configured")
	}
	results, err := c.search.ResolveSearchReferences(ctx, catalogSearchSubject(scope), c.environment, []productsearch.Reference{catalogSearchReference(ref)})
	if err != nil {
		return productsearch.Result{}, false, err
	}
	if len(results) != 1 {
		return productsearch.Result{}, false, nil
	}
	return results[0], true, nil
}

func (c CatalogService) childReferences(parent agenttools.CatalogRef, requested []agenttools.CatalogType) ([]productsearch.Reference, error) {
	metrics, ok := c.workspaceMetrics(parent.WorkspaceID)
	if !ok || metrics == nil {
		return []productsearch.Reference{}, nil
	}
	children := catalogRequestedChildren(parent.Type, requested)
	allows := func(typ agenttools.CatalogType) bool {
		for _, child := range children {
			if child == typ {
				return true
			}
		}
		return false
	}
	references := make([]productsearch.Reference, 0)
	switch parent.Type {
	case agenttools.CatalogTypeWorkspace:
		catalog := metrics.Catalog()
		if allows(agenttools.CatalogTypeDashboard) {
			for _, value := range catalog.Dashboards {
				references = append(references, productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypeDashboard, ID: value.ID})
			}
		}
		if allows(agenttools.CatalogTypeSemanticModel) {
			for _, value := range catalog.Models {
				references = append(references, productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypeSemanticModel, ID: value.ID})
			}
		}
	case agenttools.CatalogTypeDashboard:
		if allows(agenttools.CatalogTypePage) {
			for _, page := range metrics.Pages(parent.ID) {
				references = append(references, productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypePage, ID: parent.ID + "." + page.ID})
			}
		}
	case agenttools.CatalogTypePage:
		dashboardID, pageID, ok := catalogPageIDs(parent.ID)
		if !ok {
			return nil, catalogNotFound()
		}
		resolved, ok := resolveDashboard(metrics, dashboardID)
		if !ok {
			return nil, catalogNotFound()
		}
		report := resolved.Definition
		page, ok := catalogPage(metrics.Pages(dashboardID), pageID)
		if !ok {
			return nil, catalogNotFound()
		}
		seen := map[productsearch.Reference]struct{}{}
		for _, component := range page.Visuals {
			if allows(agenttools.CatalogTypeVisual) {
				id := component.Visual
				if id != "" {
					reference := productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypeVisual, ID: report.ID + "." + id}
					if _, duplicate := seen[reference]; !duplicate {
						seen[reference] = struct{}{}
						references = append(references, reference)
					}
				}
			}
			if binding, ok := catalogComponentFilterBinding(report, page, component); allows(agenttools.CatalogTypeFilter) && ok {
				reference := productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypeFilter, ID: report.ID + "." + binding.Filter}
				if _, duplicate := seen[reference]; !duplicate {
					seen[reference] = struct{}{}
					references = append(references, reference)
				}
			}
		}
	case agenttools.CatalogTypeSemanticModel:
		model, ok := metrics.SemanticModel(parent.ID)
		if !ok || model == nil {
			return nil, catalogNotFound()
		}
		if allows(agenttools.CatalogTypeSemanticTable) {
			for _, id := range sortedCatalogKeys(model.Tables) {
				references = append(references, productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypeSemanticTable, ID: parent.ID + "." + id})
			}
		}
		if allows(agenttools.CatalogTypeField) {
			for _, id := range sortedCatalogKeys(model.Dimensions) {
				references = append(references, productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypeField, ID: parent.ID + "." + id})
			}
		}
		if allows(agenttools.CatalogTypeMeasure) {
			for _, id := range append(sortedCatalogKeys(model.Measures), sortedCatalogKeys(model.Metrics)...) {
				references = append(references, productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypeMeasure, ID: parent.ID + "." + id})
			}
		}
	case agenttools.CatalogTypeSemanticTable:
		modelID, tableID, ok := catalogSemanticTableIDs(parent.ID)
		if !ok {
			return nil, catalogNotFound()
		}
		model, ok := metrics.SemanticModel(modelID)
		if !ok || model == nil {
			return nil, catalogNotFound()
		}
		table, ok := model.Tables[tableID]
		if !ok {
			return nil, catalogNotFound()
		}
		if allows(agenttools.CatalogTypeField) {
			for _, id := range sortedCatalogKeys(table.Dimensions) {
				references = append(references, productsearch.Reference{WorkspaceID: parent.WorkspaceID, Type: productsearch.TypeField, ID: modelID + "." + tableID + "." + id})
			}
		}
	default:
		return nil, &agenttools.CatalogError{Code: "invalid_arguments", Message: fmt.Sprintf("catalog type %q cannot have children", parent.Type)}
	}
	return references, nil
}

func (c CatalogService) details(ctx context.Context, scope agenttools.Scope, ref agenttools.CatalogRef, location agenttools.CatalogLocation) (map[string]any, error) {
	metrics, ok := c.workspaceMetrics(ref.WorkspaceID)
	if !ok || metrics == nil {
		return nil, catalogNotFound()
	}
	switch ref.Type {
	case agenttools.CatalogTypeDashboard:
		resolved, ok := resolveDashboard(metrics, ref.ID)
		if !ok {
			return nil, catalogNotFound()
		}
		report := resolved.Definition
		return map[string]any{
			"type":             string(ref.Type),
			"semanticModelRef": catalogRefValue(ref.WorkspaceID, agenttools.CatalogTypeSemanticModel, report.SemanticModel),
			"pageCount":        len(metrics.Pages(ref.ID)),
			"visualCount":      len(report.Visualizations),
			"filterCount":      len(report.FilterDefinitions),
		}, nil
	case agenttools.CatalogTypePage:
		dashboardID, pageID, ok := catalogPageIDs(ref.ID)
		if !ok {
			return nil, catalogNotFound()
		}
		resolved, ok := resolveDashboard(metrics, dashboardID)
		if !ok {
			return nil, catalogNotFound()
		}
		report := resolved.Definition
		page, ok := catalogPage(metrics.Pages(dashboardID), pageID)
		if !ok {
			return nil, catalogNotFound()
		}
		components := make([]map[string]any, 0, len(page.Visuals))
		for _, component := range page.Visuals {
			components = append(components, catalogComponent(component, report, page))
		}
		return map[string]any{"type": string(ref.Type), "components": components}, nil
	case agenttools.CatalogTypeVisual:
		return catalogVisualDetails(metrics, ref, location)
	case agenttools.CatalogTypeFilter:
		return catalogFilterDetails(metrics, ref, location)
	case agenttools.CatalogTypeSemanticModel:
		return c.semanticModelDetails(ctx, scope, metrics, ref)
	case agenttools.CatalogTypeSemanticTable:
		return catalogSemanticTableDetails(metrics, ref)
	case agenttools.CatalogTypeField:
		return catalogFieldDetails(metrics, ref)
	case agenttools.CatalogTypeMeasure:
		return catalogMeasureDetails(metrics, ref)
	default:
		return nil, catalogNotFound()
	}
}

func catalogSearchSubject(scope agenttools.Scope) productsearch.Subject {
	subject := productsearch.Subject{
		ID:                   scope.PrincipalID,
		DevBypass:            scope.DevAuthBypass,
		CredentialRestricted: scope.Credential.Restricted,
		Privileges:           append([]string(nil), scope.Credential.Privileges...),
	}
	if workspaceID := strings.TrimSpace(scope.Credential.WorkspaceID); workspaceID != "" {
		subject.Restricted = true
		subject.WorkspaceIDs = []string{workspaceID}
	}
	return subject
}

func catalogSearchReference(ref agenttools.CatalogRef) productsearch.Reference {
	return productsearch.Reference{WorkspaceID: ref.WorkspaceID, Type: productsearch.Type(ref.Type), ID: ref.ID}
}

func catalogItems(results []productsearch.Result) []agenttools.CatalogItem {
	items := make([]agenttools.CatalogItem, 0, len(results))
	for _, result := range results {
		items = append(items, catalogItem(result))
	}
	return items
}

func catalogItem(result productsearch.Result) agenttools.CatalogItem {
	ref := agenttools.CatalogRef{WorkspaceID: result.Reference.WorkspaceID, Type: agenttools.CatalogType(result.Reference.Type), ID: result.Reference.ID}
	hierarchy := make([]agenttools.CatalogHierarchyItem, 0, len(result.Hierarchy))
	for _, ancestor := range result.Hierarchy {
		hierarchy = append(hierarchy, agenttools.CatalogHierarchyItem{
			Ref:  agenttools.CatalogRef{WorkspaceID: result.Reference.WorkspaceID, Type: agenttools.CatalogType(ancestor.Type), ID: ancestor.ID},
			Name: ancestor.Name,
		})
	}
	locations := make([]agenttools.CatalogLocation, 0, len(result.Locations))
	for _, location := range result.Locations {
		if location.DashboardID == "" || location.PageID == "" {
			continue
		}
		locations = append(locations, agenttools.CatalogLocation{
			DashboardID: location.DashboardID, DashboardName: location.DashboardName,
			PageID: location.PageID, PageName: location.PageName, Href: location.Href,
		})
	}
	return agenttools.CatalogItem{
		Ref: ref, Name: result.Name, Description: result.Description,
		Workspace: agenttools.CatalogWorkspace{
			Ref:  agenttools.CatalogRef{WorkspaceID: result.Workspace.ID, Type: agenttools.CatalogTypeWorkspace, ID: result.Workspace.ID},
			Name: result.Workspace.Name,
		},
		Hierarchy: hierarchy, Locations: locations, Href: result.Href, Capabilities: catalogCapabilities(ref.Type),
	}
}

func catalogWorkspaceItem(summary workspace.Summary) agenttools.CatalogItem {
	id := string(summary.ID)
	name := catalogFirstNonEmpty(summary.Title, id)
	ref := agenttools.CatalogRef{WorkspaceID: id, Type: agenttools.CatalogTypeWorkspace, ID: id}
	return agenttools.CatalogItem{
		Ref: ref, Name: name, Description: summary.Description,
		Workspace: agenttools.CatalogWorkspace{Ref: ref, Name: name},
		Hierarchy: []agenttools.CatalogHierarchyItem{},
		Href:      "/workspaces/" + url.PathEscape(id),
		Capabilities: []string{
			agenttools.CatalogGetToolName,
			agenttools.CatalogListToolName,
		},
	}
}

func catalogCapabilities(typ agenttools.CatalogType) []string {
	switch typ {
	case agenttools.CatalogTypeWorkspace, agenttools.CatalogTypeDashboard, agenttools.CatalogTypePage, agenttools.CatalogTypeSemanticTable:
		return []string{agenttools.CatalogGetToolName, agenttools.CatalogListToolName}
	case agenttools.CatalogTypeSemanticModel:
		return []string{agenttools.CatalogGetToolName, agenttools.CatalogListToolName, "query_semantic_model", agenttools.QueryVisualToolName}
	case agenttools.CatalogTypeVisual:
		return []string{agenttools.CatalogGetToolName, "query_dashboard_visual"}
	case agenttools.CatalogTypeField, agenttools.CatalogTypeMeasure:
		return []string{agenttools.CatalogGetToolName, "query_semantic_model", agenttools.QueryVisualToolName}
	default:
		return []string{agenttools.CatalogGetToolName}
	}
}

func catalogRequestedChildren(parent agenttools.CatalogType, requested []agenttools.CatalogType) []agenttools.CatalogType {
	if len(requested) > 0 {
		return requested
	}
	switch parent {
	case agenttools.CatalogTypeWorkspace:
		return []agenttools.CatalogType{agenttools.CatalogTypeDashboard, agenttools.CatalogTypeSemanticModel}
	case agenttools.CatalogTypeDashboard:
		return []agenttools.CatalogType{agenttools.CatalogTypePage}
	case agenttools.CatalogTypePage:
		return []agenttools.CatalogType{agenttools.CatalogTypeVisual, agenttools.CatalogTypeFilter}
	case agenttools.CatalogTypeSemanticModel:
		return []agenttools.CatalogType{agenttools.CatalogTypeSemanticTable, agenttools.CatalogTypeField, agenttools.CatalogTypeMeasure}
	case agenttools.CatalogTypeSemanticTable:
		return []agenttools.CatalogType{agenttools.CatalogTypeField}
	default:
		return nil
	}
}

func catalogGetLocation(request agenttools.CatalogGetRequest, locations []agenttools.CatalogLocation) (agenttools.CatalogLocation, error) {
	if request.Ref.Type != agenttools.CatalogTypeVisual && request.Ref.Type != agenttools.CatalogTypeFilter {
		return agenttools.CatalogLocation{}, nil
	}
	if request.Location == nil {
		if len(locations) == 1 {
			return locations[0], nil
		}
		if len(locations) > 1 {
			return agenttools.CatalogLocation{}, &agenttools.CatalogError{
				Code: "catalog_location_required", Message: "this resource is shared; pass one of its dashboard/page locations",
			}
		}
		return agenttools.CatalogLocation{}, catalogNotFound()
	}
	for _, location := range locations {
		if location.DashboardID == request.Location.DashboardID && location.PageID == request.Location.PageID {
			return location, nil
		}
	}
	return agenttools.CatalogLocation{}, catalogNotFound()
}

func catalogSearchError(err error) error {
	switch {
	case errors.Is(err, productsearch.ErrInvalidCursor):
		return &agenttools.CatalogError{Code: "invalid_arguments", Message: err.Error()}
	case errors.Is(err, productsearch.ErrSnapshotChanged):
		return &agenttools.CatalogError{Code: "catalog_snapshot_changed", Message: err.Error()}
	default:
		return err
	}
}

func catalogNotFound() error {
	return &agenttools.CatalogError{Code: "catalog_not_found", Message: "catalog resource was not found"}
}

type catalogListCursor struct {
	Scope      string                   `json:"scope"`
	Parent     *agenttools.CatalogRef   `json:"parent,omitempty"`
	ChildTypes []agenttools.CatalogType `json:"childTypes,omitempty"`
	Snapshot   string                   `json:"snapshot"`
	Offset     int                      `json:"offset"`
}

func (c CatalogService) encodeCatalogListCursor(scope agenttools.Scope, request agenttools.CatalogListRequest, snapshot string, offset int) string {
	payload, _ := json.Marshal(catalogListCursor{
		Scope: catalogScopeDigest(scope), Parent: request.Parent,
		ChildTypes: append([]agenttools.CatalogType(nil), request.ChildTypes...),
		Snapshot:   snapshot, Offset: offset,
	})
	return c.signCursor(catalogListCursorPrefix, payload)
}

func (c CatalogService) decodeCatalogListCursor(value string, scope agenttools.Scope, request agenttools.CatalogListRequest, snapshot string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	if c.verifyCursor == nil {
		return 0, &agenttools.CatalogError{Code: "invalid_arguments", Message: "catalog cursor codec is unavailable"}
	}
	payload, err := c.verifyCursor(catalogListCursorPrefix, value)
	if err != nil {
		return 0, &agenttools.CatalogError{Code: "invalid_arguments", Message: "catalog cursor is invalid"}
	}
	var cursor catalogListCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Offset < 0 {
		return 0, &agenttools.CatalogError{Code: "invalid_arguments", Message: "catalog cursor is invalid"}
	}
	if cursor.Scope != catalogScopeDigest(scope) || !catalogRefsEqual(cursor.Parent, request.Parent) || !catalogTypesEqual(cursor.ChildTypes, request.ChildTypes) {
		return 0, &agenttools.CatalogError{Code: "invalid_arguments", Message: "catalog cursor does not match this request"}
	}
	if cursor.Snapshot != snapshot {
		return 0, &agenttools.CatalogError{Code: "catalog_snapshot_changed", Message: "catalog changed while browsing; restart from the first page"}
	}
	return cursor.Offset, nil
}

func catalogScopeDigest(scope agenttools.Scope) string {
	values := append([]string{scope.PrincipalID, scope.WorkspaceID, scope.Credential.WorkspaceID, fmt.Sprint(scope.Credential.Restricted)}, scope.Credential.Privileges...)
	sort.Strings(values[4:])
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func catalogItemsSnapshot(items []agenttools.CatalogItem) string {
	encoded, _ := json.Marshal(items)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func catalogRefsEqual(left, right *agenttools.CatalogRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func catalogTypesEqual(left, right []agenttools.CatalogType) bool {
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

func catalogPageIDs(id string) (string, string, bool) {
	index := strings.LastIndex(id, ".")
	if index <= 0 || index == len(id)-1 {
		return "", "", false
	}
	return id[:index], id[index+1:], true
}

func catalogSemanticTableIDs(id string) (string, string, bool) {
	return catalogPageIDs(id)
}

func catalogPage(pages []dashboard.Page, id string) (dashboard.Page, bool) {
	for _, page := range pages {
		if page.ID == id {
			return page, true
		}
	}
	return dashboard.Page{}, false
}

func catalogComponent(component dashboard.PageVisual, report dashboarddefinition.Definition, page dashboard.Page) map[string]any {
	kind, ref := component.Kind, ""
	switch {
	case component.Visual != "":
		kind, ref = "visual", component.Visual
	case component.Binding.ID != "":
		if binding, ok := catalogComponentFilterBinding(report, page, component); ok {
			kind, ref = "filter", binding.Filter
		}
	}
	title := component.Title
	if title == "" {
		if value, ok := report.Visualizations[ref]; ok {
			title = dashboarddefinition.SpecTitle(value.Spec)
		} else if value, ok := report.FilterDefinitions[ref]; ok {
			title = value.Label
		}
	}
	out := map[string]any{
		"id": component.ID, "kind": kind, "ref": ref, "title": title,
		"description": component.Description, "placement": catalogPlacement(component.Placement),
	}
	if kind == "visual" {
		out["visualId"] = ref
	}
	if kind == "filter" {
		out["filterId"] = ref
	}
	return out
}

func catalogVisualDetails(metrics queryruntime.Metrics, ref agenttools.CatalogRef, location agenttools.CatalogLocation) (map[string]any, error) {
	dashboardID, visualID, ok := catalogPageIDs(ref.ID)
	if !ok || (location.DashboardID != "" && location.DashboardID != dashboardID) {
		return nil, catalogNotFound()
	}
	resolved, ok := resolveDashboard(metrics, dashboardID)
	if !ok {
		return nil, catalogNotFound()
	}
	report := resolved.Definition
	page, ok := catalogPage(metrics.Pages(dashboardID), location.PageID)
	if !ok {
		return nil, catalogNotFound()
	}
	component, ok := catalogComponentForRef(page, visualID)
	if !ok {
		return nil, catalogNotFound()
	}
	if visual, exists := report.Visualizations[visualID]; exists {
		columns := make([]map[string]any, 0)
		for _, column := range dashboarddefinition.TableColumns(visual.Spec) {
			columns = append(columns, map[string]any{
				"key": column.Key, "label": column.Label, "role": column.Role, "format": column.Format,
			})
		}
		return map[string]any{
			"type": string(ref.Type), "visualType": catalogVisualizationType(visual),
			"shape": string(visual.Query.ResultShape), "renderer": visual.RendererID,
			"query": catalogJSONMap(visual.Query), "columns": columns,
			"placement": catalogComponentPlacement(component),
		}, nil
	}
	return nil, catalogNotFound()
}

func catalogVisualizationType(visual visualizationdefinition.Definition) string {
	spec := catalogJSONMap(visual.Spec)
	if mark, _ := spec["mark"].(string); mark != "" {
		return mark
	}
	kind, _ := spec["kind"].(string)
	return kind
}

func catalogFilterDetails(metrics queryruntime.Metrics, ref agenttools.CatalogRef, location agenttools.CatalogLocation) (map[string]any, error) {
	dashboardID, filterID, ok := catalogPageIDs(ref.ID)
	if !ok || (location.DashboardID != "" && location.DashboardID != dashboardID) {
		return nil, catalogNotFound()
	}
	resolved, ok := resolveDashboard(metrics, dashboardID)
	if !ok {
		return nil, catalogNotFound()
	}
	report := resolved.Definition
	filter, exists := report.FilterDefinitions[filterID]
	if !exists {
		return nil, catalogNotFound()
	}
	page, ok := catalogPage(metrics.Pages(dashboardID), location.PageID)
	if !ok {
		return nil, catalogNotFound()
	}
	component, binding, ok := catalogFilterComponent(report, page, filterID)
	if !ok {
		return nil, catalogNotFound()
	}
	return map[string]any{
		"type": string(ref.Type), "field": filter.Field,
		"configuration": map[string]any{
			"definition":   catalogJSONMap(filter),
			"binding":      catalogJSONMap(binding),
			"presentation": catalogJSONMap(component.Presentation),
		},
		"placement": catalogComponentPlacement(component),
	}, nil
}

func (c CatalogService) semanticModelDetails(ctx context.Context, scope agenttools.Scope, metrics queryruntime.Metrics, ref agenttools.CatalogRef) (map[string]any, error) {
	model, ok := metrics.SemanticModel(ref.ID)
	if !ok || model == nil {
		return nil, catalogNotFound()
	}
	dashboards := catalogDashboardsForModel(metrics, ref.ID)
	references := make([]productsearch.Reference, 0, len(dashboards))
	for _, dashboardUsage := range dashboards {
		references = append(references, productsearch.Reference{
			WorkspaceID: ref.WorkspaceID,
			Type:        productsearch.TypeDashboard,
			ID:          dashboardUsage,
		})
	}
	authorized, err := c.search.ResolveSearchReferences(ctx, catalogSearchSubject(scope), c.environment, references)
	if err != nil {
		return nil, err
	}
	usage := make([]agenttools.CatalogRef, 0, len(authorized))
	for _, dashboardUsage := range authorized {
		usage = append(usage, catalogRefValue(ref.WorkspaceID, agenttools.CatalogTypeDashboard, dashboardUsage.Reference.ID))
	}
	fieldCount := 0
	for _, table := range model.Tables {
		fieldCount += len(table.Dimensions)
	}
	return map[string]any{
		"type":                    string(ref.Type),
		"semanticTableCount":      len(model.Tables),
		"fieldCount":              fieldCount,
		"conformedDimensionCount": len(model.Dimensions),
		"atomicMeasureCount":      len(model.Measures),
		"metricCount":             len(model.Metrics),
		"factCount":               len(model.FactNames()),
		"relationshipCount":       len(model.Relationships),
		"relationships":           catalogRelationshipDetails(ref.WorkspaceID, ref.ID, model.Relationships),
		"dashboardCount":          len(usage),
		"dashboardUsage":          usage,
	}, nil
}

func catalogSemanticTableDetails(metrics queryruntime.Metrics, ref agenttools.CatalogRef) (map[string]any, error) {
	modelID, tableID, ok := catalogSemanticTableIDs(ref.ID)
	if !ok {
		return nil, catalogNotFound()
	}
	model, ok := metrics.SemanticModel(modelID)
	if !ok || model == nil {
		return nil, catalogNotFound()
	}
	table, ok := model.Tables[tableID]
	if !ok {
		return nil, catalogNotFound()
	}
	sources := append([]string{}, table.Sources...)
	if table.Source != "" && len(sources) == 0 {
		sources = []string{table.Source}
	}
	sort.Strings(sources)
	measureCount := 0
	for _, measure := range model.Measures {
		if measure.Fact == tableID {
			measureCount++
		}
	}
	keys := []string{}
	if table.PrimaryKey != "" {
		keys = append(keys, table.PrimaryKey)
	}
	return map[string]any{
		"type": string(ref.Type), "source": table.Source, "sources": sources, "grain": table.Grain,
		"primaryKey": table.PrimaryKey, "keys": keys, "roles": catalogSemanticTableRoles(model, tableID),
		"fieldCount": len(table.Dimensions), "measureCount": measureCount,
	}, nil
}

func catalogFieldDetails(metrics queryruntime.Metrics, ref agenttools.CatalogRef) (map[string]any, error) {
	parts := strings.Split(ref.ID, ".")
	if len(parts) < 2 {
		return nil, catalogNotFound()
	}
	model, ok := metrics.SemanticModel(parts[0])
	if !ok || model == nil {
		return nil, catalogNotFound()
	}
	if len(parts) == 2 {
		field, ok := model.Dimensions[parts[1]]
		if !ok {
			return nil, catalogNotFound()
		}
		bindings := make([]map[string]any, 0, len(field.Bindings))
		for _, tableID := range sortedCatalogKeys(field.Bindings) {
			binding := field.Bindings[tableID]
			bindings = append(bindings, map[string]any{
				"semanticTableRef": catalogRefValue(ref.WorkspaceID, agenttools.CatalogTypeSemanticTable, parts[0]+"."+tableID),
				"fieldRef":         catalogRefValue(ref.WorkspaceID, agenttools.CatalogTypeField, parts[0]+"."+binding.Field),
				"relationshipPath": append([]string{}, binding.Path...),
			})
		}
		return map[string]any{
			"type": string(ref.Type), "kind": "dimension", "label": field.Label, "dataType": field.Type,
			"timeGrains": append([]string{}, field.Grains...), "bindings": bindings,
		}, nil
	}
	table, ok := model.Tables[parts[len(parts)-2]]
	if !ok {
		return nil, catalogNotFound()
	}
	field, ok := table.Dimensions[parts[len(parts)-1]]
	if !ok {
		return nil, catalogNotFound()
	}
	details := map[string]any{
		"type": string(ref.Type), "kind": "dimension", "table": parts[len(parts)-2],
		"label": field.Label, "dataType": field.Type, "grain": table.Grain,
		"expression": catalogFirstNonEmpty(field.Expression, field.Expr),
		"primaryKey": parts[len(parts)-1] == table.PrimaryKey,
	}
	if column, ok := table.Columns[parts[len(parts)-1]]; ok && column.SourceField != "" {
		details["sourceField"] = column.SourceField
	}
	for _, column := range table.Schema.Columns {
		if column.Name == parts[len(parts)-1] && column.Nullable != nil {
			details["nullable"] = *column.Nullable
			break
		}
	}
	return details, nil
}

func catalogMeasureDetails(metrics queryruntime.Metrics, ref agenttools.CatalogRef) (map[string]any, error) {
	modelID, measureID, ok := catalogPageIDs(ref.ID)
	if !ok {
		return nil, catalogNotFound()
	}
	model, ok := metrics.SemanticModel(modelID)
	if !ok || model == nil {
		return nil, catalogNotFound()
	}
	if measure, ok := model.Measures[measureID]; ok {
		dependencies := catalogMeasureFieldRefs(ref.WorkspaceID, modelID, measure)
		input := map[string]any{}
		if measure.Input.Field != "" {
			input["fieldRef"] = catalogRefValue(ref.WorkspaceID, agenttools.CatalogTypeField, modelID+"."+measure.Input.Field)
		}
		if measure.Input.Expression != "" {
			input["expression"] = measure.Input.Expression
		}
		filters := make([]map[string]any, 0, len(measure.Filters))
		for _, filter := range measure.Filters {
			filters = append(filters, map[string]any{
				"fieldRef": catalogRefValue(ref.WorkspaceID, agenttools.CatalogTypeField, modelID+"."+filter.Field),
				"operator": filter.Operator,
				"values":   append([]any{}, filter.Values...),
			})
		}
		return map[string]any{
			"type": string(ref.Type), "kind": "measure", "table": measure.Fact,
			"factRef": catalogRefValue(ref.WorkspaceID, agenttools.CatalogTypeSemanticTable, modelID+"."+measure.Fact),
			"label":   measure.Label, "aggregation": measure.Aggregation, "input": input, "filters": filters,
			"empty": measure.Empty, "dependencyRefs": dependencies,
			"unit": measure.Unit, "format": measure.Format, "hidden": measure.Hidden,
		}, nil
	}
	if metric, ok := model.Metrics[measureID]; ok {
		dependencies := []agenttools.CatalogRef{}
		if expression, err := semanticmodel.ParseExpression(metric.Expression); err == nil {
			for _, dependency := range expression.References() {
				dependencies = append(dependencies, catalogRefValue(ref.WorkspaceID, agenttools.CatalogTypeMeasure, modelID+"."+dependency))
			}
		}
		dependencies = uniqueCatalogRefs(dependencies)
		return map[string]any{
			"type": string(ref.Type), "kind": "metric", "label": metric.Label,
			"expression": metric.Expression, "dependencyRefs": dependencies,
			"unit": metric.Unit, "format": metric.Format, "hidden": metric.Hidden,
		}, nil
	}
	return nil, catalogNotFound()
}

func catalogRelationshipDetails(workspaceID, modelID string, relationships []semanticmodel.Relationship) []map[string]any {
	out := make([]map[string]any, 0, len(relationships))
	for _, relationship := range relationships {
		out = append(out, map[string]any{
			"id":           relationship.ID,
			"description":  relationship.Description,
			"fromFieldRef": catalogRefValue(workspaceID, agenttools.CatalogTypeField, modelID+"."+relationship.From),
			"toFieldRef":   catalogRefValue(workspaceID, agenttools.CatalogTypeField, modelID+"."+relationship.To),
			"cardinality":  relationship.Cardinality,
			"active":       true,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["id"].(string) < out[j]["id"].(string)
	})
	return out
}

func catalogSemanticTableRoles(model *semanticmodel.Model, tableID string) []string {
	roles := []string{}
	for _, measure := range model.Measures {
		if measure.Fact == tableID {
			roles = append(roles, "fact")
			break
		}
	}
	for _, relationship := range model.Relationships {
		if table, _, ok := strings.Cut(relationship.To, "."); ok && table == tableID {
			roles = append(roles, "dimension")
			break
		}
	}
	return roles
}

func catalogDashboardsForModel(metrics queryruntime.Metrics, modelID string) []string {
	dashboardIDs := make([]string, 0)
	for _, summary := range metrics.Catalog().Dashboards {
		resolved, ok := resolveDashboard(metrics, summary.ID)
		if !ok || (resolved.Definition.SemanticModel != modelID && (resolved.Model == nil || resolved.Model.Name != modelID)) {
			continue
		}
		dashboardIDs = append(dashboardIDs, resolved.Definition.ID)
	}
	sort.Strings(dashboardIDs)
	return dashboardIDs
}

func catalogMeasureFieldRefs(workspaceID, modelID string, measure semanticmodel.MetricMeasure) []agenttools.CatalogRef {
	values := []string{}
	if measure.Input.Field != "" {
		values = append(values, measure.Input.Field)
	}
	if measure.Input.Expression != "" {
		if expression, err := semanticmodel.ParseExpression(measure.Input.Expression); err == nil {
			values = append(values, expression.References()...)
		}
	}
	for _, filter := range measure.Filters {
		values = append(values, filter.Field)
	}
	refs := make([]agenttools.CatalogRef, 0, len(values))
	for _, value := range values {
		refs = append(refs, catalogRefValue(workspaceID, agenttools.CatalogTypeField, modelID+"."+value))
	}
	return uniqueCatalogRefs(refs)
}

func uniqueCatalogRefs(values []agenttools.CatalogRef) []agenttools.CatalogRef {
	seen := map[agenttools.CatalogRef]struct{}{}
	out := make([]agenttools.CatalogRef, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func catalogComponentForRef(page dashboard.Page, id string) (dashboard.PageVisual, bool) {
	for _, component := range page.Visuals {
		if component.Visual == id {
			return component, true
		}
	}
	return dashboard.PageVisual{}, false
}

func catalogFilterComponent(report dashboarddefinition.Definition, page dashboard.Page, filterID string) (dashboard.PageVisual, dashboardfilter.Binding, bool) {
	for _, component := range page.Visuals {
		binding, ok := catalogComponentFilterBinding(report, page, component)
		if ok && binding.Filter == filterID {
			return component, binding, true
		}
	}
	return dashboard.PageVisual{}, dashboardfilter.Binding{}, false
}

func catalogComponentFilterBinding(report dashboarddefinition.Definition, page dashboard.Page, component dashboard.PageVisual) (dashboardfilter.Binding, bool) {
	switch component.Binding.Scope {
	case dashboardfilter.ScopeReport:
		binding, ok := report.FilterBindings[component.Binding.ID]
		return binding, ok
	case dashboardfilter.ScopePage:
		binding, ok := page.FilterBindings[component.Binding.ID]
		return binding, ok
	default:
		return dashboardfilter.Binding{}, false
	}
}

func catalogPlacement(value dashboard.PagePlacement) map[string]any {
	return map[string]any{"col": value.Col, "row": value.Row, "colSpan": value.ColSpan, "rowSpan": value.RowSpan}
}

func catalogComponentPlacement(component dashboard.PageVisual) map[string]any {
	if !component.Placement.IsZero() {
		return catalogPlacement(component.Placement)
	}
	return map[string]any{"x": component.X, "y": component.Y, "width": component.Width, "height": component.Height}
}

func catalogJSONMap(value any) map[string]any {
	encoded, _ := json.Marshal(value)
	out := map[string]any{}
	_ = json.Unmarshal(encoded, &out)
	return out
}

func catalogRefValue(workspaceID string, typ agenttools.CatalogType, id string) agenttools.CatalogRef {
	return agenttools.CatalogRef{WorkspaceID: workspaceID, Type: typ, ID: id}
}

func sortedCatalogKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
