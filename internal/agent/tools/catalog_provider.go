package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentcontracts "github.com/flidai/leapview/internal/agent/contracts"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

const (
	CatalogSearchToolName = "catalog_search"
	CatalogListToolName   = "catalog_list"
	CatalogGetToolName    = "catalog_get"

	DefaultCatalogSearchLimit = 10
	MaxCatalogSearchLimit     = 25
	DefaultCatalogListLimit   = 25
	MaxCatalogListLimit       = 50
)

type CatalogType = agentcontracts.CatalogType

type CatalogRef = agentcontracts.CatalogRef

type CatalogItem struct {
	Ref         CatalogRef `json:"ref"`
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName,omitempty"`
	Description string     `json:"description,omitempty"`
	Domain      string     `json:"domain,omitempty"`
	Owner       string     `json:"owner,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
}

type CatalogSearchRequest struct {
	Query  string        `json:"query"`
	Kinds  []CatalogType `json:"kinds,omitempty"`
	Domain string        `json:"domain,omitempty"`
	Cursor string        `json:"cursor,omitempty"`
	Limit  int           `json:"limit,omitempty"`
}

type CatalogListRequest struct {
	Parent     *CatalogRef   `json:"parent,omitempty"`
	ChildKinds []CatalogType `json:"childKinds,omitempty"`
	Domain     string        `json:"domain,omitempty"`
	Cursor     string        `json:"cursor,omitempty"`
	Limit      int           `json:"limit,omitempty"`
}

type CatalogGetRequest struct {
	Ref CatalogRef `json:"ref"`
}

type CatalogPage struct {
	Items      []CatalogItem `json:"items"`
	Count      int           `json:"count"`
	HasMore    bool          `json:"hasMore"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type CatalogGetResult struct {
	Item    CatalogItem    `json:"item"`
	Details map[string]any `json:"details"`
}

// ResourceResolver resolves an exact graph resource through the authorized
// active-generation catalog before an authoring or query tool executes.
type ResourceResolver func(context.Context, Scope, projectgraph.ResourceID, projectgraph.Kind, access.Capability) (projectgraph.ResourceID, error)

// Catalog is the sole model-facing catalog port. Implementations must resolve
// against the immutable active serving-generation snapshot and an
// authorization subject set for Scope.PrincipalID.
type Catalog interface {
	Search(context.Context, Scope, CatalogSearchRequest) (CatalogPage, error)
	List(context.Context, Scope, CatalogListRequest) (CatalogPage, error)
	Get(context.Context, Scope, CatalogGetRequest) (CatalogGetResult, error)
}

type CatalogError struct {
	Code    string
	Message string
}

func (e *CatalogError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// SemanticModelResolver supplies the active, authorization-checked semantic
// model snapshot for catalog_get. The catalog graph intentionally carries
// only resource metadata, so this optional projection gives the model enough
// information to explain metric definitions without exporting dashboard YAML
// or searching generic documentation.
type SemanticModelResolver func(context.Context, Scope, CatalogRef) (*semanticmodel.Model, bool)

type CatalogProvider struct {
	Catalog       Catalog
	SemanticModel SemanticModelResolver
}

func (p CatalogProvider) Definitions(scope Scope) []agentcore.ToolDefinition {
	return []agentcore.ToolDefinition{
		{
			Name: CatalogSearchToolName, Description: "Search authorized project resources by stable ID, name, description, or domain metadata. Use an exact ref from a unique result; do not repeat broad searches when hasMore is false.",
			InputSchema: json.RawMessage(agentcontracts.CatalogSearchInputSchemaJSON), OutputSchema: json.RawMessage(agentcontracts.CatalogPageSchemaJSON),
			Effect: "read", Tags: []string{"catalog", "search"},
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				var request CatalogSearchRequest
				if err := decodeCatalogArguments(call.Arguments, &request); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				request.Query = strings.TrimSpace(request.Query)
				if request.Query == "" {
					return ToolError("invalid_arguments", "query is required"), nil
				}
				if request.Limit == 0 {
					request.Limit = DefaultCatalogSearchLimit
				}
				if err := validateCatalogLimit(request.Limit, MaxCatalogSearchLimit); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if err := validateCatalogKinds(request.Kinds); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if p.Catalog == nil {
					return ToolError("catalog_unavailable", "catalog service is not configured"), nil
				}
				result, err := p.Catalog.Search(ctx, scope, request)
				if err != nil {
					return catalogToolError("catalog_search_failed", err), nil
				}
				return agentcore.ToolResult{Content: catalogPageResult(result)}, nil
			}),
		},
		{
			Name: CatalogListToolName, Description: "Browse one authorized project-resource hierarchy level when a parent ref is known. Returned refs are exact stable IDs; a page with hasMore false is complete.",
			InputSchema: json.RawMessage(agentcontracts.CatalogListInputSchemaJSON), OutputSchema: json.RawMessage(agentcontracts.CatalogPageSchemaJSON),
			Effect: "read", Tags: []string{"catalog", "browse"},
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				var request CatalogListRequest
				if err := decodeCatalogArguments(call.Arguments, &request); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if request.Limit == 0 {
					request.Limit = DefaultCatalogListLimit
				}
				if err := validateCatalogLimit(request.Limit, MaxCatalogListLimit); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				request.ChildKinds = normalizedCatalogKinds(request.ChildKinds)
				if request.Parent != nil {
					if err := validateCatalogRef(*request.Parent); err != nil {
						return ToolError("invalid_arguments", err.Error()), nil
					}
				}
				if err := validateCatalogKinds(request.ChildKinds); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if p.Catalog == nil {
					return ToolError("catalog_unavailable", "catalog service is not configured"), nil
				}
				result, err := p.Catalog.List(ctx, scope, request)
				if err != nil {
					return catalogToolError("catalog_list_failed", err), nil
				}
				return agentcore.ToolResult{Content: catalogPageResult(result)}, nil
			}),
		},
		{
			Name: CatalogGetToolName, Description: "Resolve one exact authorized project resource ID and return compact metadata. For a semantic_model ref, details.metadata.definition contains the bounded active definition (datasets, dimensions, and metrics); use it to explain metric formulas before exporting dashboards or searching documentation.",
			InputSchema: json.RawMessage(agentcontracts.CatalogGetInputSchemaJSON), OutputSchema: json.RawMessage(agentcontracts.CatalogGetResultSchemaJSON),
			Effect: "read", Tags: []string{"catalog", "describe"},
			Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
				var request CatalogGetRequest
				if err := decodeCatalogArguments(call.Arguments, &request); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if err := validateCatalogRef(request.Ref); err != nil {
					return ToolError("invalid_arguments", err.Error()), nil
				}
				if p.Catalog == nil {
					return ToolError("catalog_unavailable", "catalog service is not configured"), nil
				}
				result, err := p.Catalog.Get(ctx, scope, request)
				if err != nil {
					return catalogToolError("catalog_get_failed", err), nil
				}
				if request.Ref.Kind == CatalogType(agentcontracts.CatalogTypeSemanticModel) && p.SemanticModel != nil {
					if model, ok := p.SemanticModel(ctx, scope, result.Item.Ref); ok && model != nil {
						if result.Details == nil {
							result.Details = map[string]any{}
						}
						metadata, _ := result.Details["metadata"].(map[string]any)
						if metadata == nil {
							metadata = map[string]any{}
							result.Details["metadata"] = metadata
						}
						metadata["definition"] = semanticModelDefinition(model)
					}
				}
				return agentcore.ToolResult{Content: result}, nil
			}),
		},
	}
}

func catalogPageResult(page CatalogPage) CatalogPage {
	if page.Items == nil {
		page.Items = []CatalogItem{}
	}
	page.Count = len(page.Items)
	page.HasMore = strings.TrimSpace(page.NextCursor) != ""
	return page
}

func semanticModelDefinition(model *semanticmodel.Model) map[string]any {
	definition := map[string]any{
		"name":        model.Name,
		"title":       model.Title,
		"description": model.Description,
	}
	if len(model.Datasets) > 0 {
		datasets := make(map[string]any, len(model.Datasets))
		for name, dataset := range model.Datasets {
			if protected, ok := model.AccessPolicy.Datasets[name]; ok && len(protected.RequiredAccessGrants) > 0 {
				continue
			}
			datasets[name] = compactSemanticDataset(dataset)
		}
		if len(datasets) > 0 {
			definition["datasets"] = datasets
		}
	}
	if len(model.Dimensions) > 0 {
		dimensions := make(map[string]any, len(model.Dimensions))
		for name, dimension := range model.Dimensions {
			if len(model.AccessPolicy.Dimensions[name]) > 0 {
				continue
			}
			protectedDataset := false
			for dataset := range dimension.Bindings {
				if policy, ok := model.AccessPolicy.Datasets[dataset]; ok && len(policy.RequiredAccessGrants) > 0 {
					protectedDataset = true
					break
				}
			}
			if protectedDataset {
				continue
			}
			dimensions[name] = compactSemanticDimension(dimension)
		}
		if len(dimensions) > 0 {
			definition["dimensions"] = dimensions
		}
	}
	if len(model.Metrics) > 0 {
		metrics := make(map[string]any, len(model.Metrics))
		protectedMetrics := map[string]struct{}{}
		for name, metric := range model.Metrics {
			if metric.Hidden || len(model.AccessPolicy.Metrics[name]) > 0 {
				protectedMetrics[name] = struct{}{}
				continue
			}
			if datasetPolicy, ok := model.AccessPolicy.Datasets[metric.Dataset]; ok && len(datasetPolicy.RequiredAccessGrants) > 0 {
				protectedMetrics[name] = struct{}{}
			}
		}
		for name, metric := range model.Metrics {
			if _, protected := protectedMetrics[name]; protected {
				continue
			}
			if metricReferencesProtected(metric, protectedMetrics) {
				continue
			}
			metrics[name] = compactSemanticMetric(metric)
		}
		if len(metrics) > 0 {
			definition["metrics"] = metrics
		}
	}
	return definition
}

func metricReferencesProtected(metric semanticmodel.Metric, protected map[string]struct{}) bool {
	for _, name := range []string{metric.Numerator, metric.Denominator} {
		if _, ok := protected[name]; name != "" && ok {
			return true
		}
	}
	for _, token := range strings.FieldsFunc(metric.Expression, func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		if _, ok := protected[token]; ok {
			return true
		}
	}
	return false
}

func compactSemanticDataset(dataset semanticmodel.SemanticDatasetSpec) map[string]any {
	return compactStrings(map[string]string{
		"model":                dataset.Model,
		"defaultTimeDimension": dataset.DefaultTimeDimension,
		"displayName":          dataset.DisplayName,
		"description":          dataset.Description,
	})
}

func compactSemanticDimension(dimension semanticmodel.SemanticDimension) map[string]any {
	result := compactStrings(map[string]string{
		"label":       dimension.Label,
		"description": dimension.Description,
		"type":        dimension.Type,
		"datatype":    string(dimension.Datatype),
		"nativeGrain": dimension.NativeGrain,
		"timezone":    dimension.Timezone,
		"calendar":    dimension.Calendar,
		"weekStart":   dimension.WeekStart,
	})
	if len(dimension.Grains) > 0 {
		result["grains"] = append([]string(nil), dimension.Grains...)
	}
	return result
}

func compactSemanticMetric(metric semanticmodel.Metric) map[string]any {
	result := compactStrings(map[string]string{
		"type":          metric.Type,
		"dataset":       metric.Dataset,
		"aggregation":   metric.Aggregation,
		"where":         strings.Join(metric.Where, ","),
		"empty":         metric.Empty,
		"timeDimension": metric.TimeDimension,
		"expression":    metric.Expression,
		"numerator":     metric.Numerator,
		"denominator":   metric.Denominator,
		"label":         metric.Label,
		"description":   metric.Description,
		"unit":          metric.Unit,
		"format":        metric.Format,
	})
	if metric.Input != nil && strings.TrimSpace(metric.Input.Field) != "" {
		result["input"] = map[string]any{"field": metric.Input.Field}
	}
	if len(metric.Where) > 0 {
		result["where"] = append([]string(nil), metric.Where...)
	}
	return result
}

func compactStrings(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		if strings.TrimSpace(value) != "" {
			result[key] = value
		}
	}
	return result
}

func decodeCatalogArguments(arguments json.RawMessage, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("arguments must contain one JSON object")
		}
		return err
	}
	return nil
}

func validateCatalogLimit(limit, maximum int) error {
	if limit < 1 || limit > maximum {
		return fmt.Errorf("limit must be between 1 and %d", maximum)
	}
	return nil
}

func validateCatalogKinds(kinds []CatalogType) error {
	for _, kind := range kinds {
		if _, err := projectgraph.ParseKind(string(kind)); err != nil {
			return fmt.Errorf("unsupported catalog kind %q", kind)
		}
	}
	return nil
}

func validateCatalogRef(ref CatalogRef) error {
	if _, err := projectgraph.NewResourceID(strings.TrimSpace(ref.ID)); err != nil {
		return fmt.Errorf("ref.id is invalid: %v", err)
	}
	return validateCatalogKinds([]CatalogType{ref.Kind})
}

func normalizedCatalogKinds(kinds []CatalogType) []CatalogType {
	seen := map[CatalogType]struct{}{}
	out := make([]CatalogType, 0, len(kinds))
	for _, kind := range kinds {
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	return out
}

func catalogToolError(fallback string, err error) agentcore.ToolResult {
	var catalogErr *CatalogError
	if errors.As(err, &catalogErr) && strings.TrimSpace(catalogErr.Code) != "" {
		return ToolError(catalogErr.Code, catalogErr.Message)
	}
	return ToolError(fallback, err.Error())
}
