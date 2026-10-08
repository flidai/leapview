package tools

import "github.com/flidai/leapview/internal/dashboard/authoring/catalog"

// Keep discovery small and tabular. Detailed evidence belongs in get_dashboard;
// repeating publication metadata for every item can exhaust the tool budget.
type dashboardCatalogItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	SemanticModel string `json:"semanticModel"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	Visibility    string `json:"visibility"`
	DraftID       string `json:"draftId"`
	PageCount     int    `json:"pageCount"`
}

type dashboardCatalogListResult struct {
	Items         []dashboardCatalogItem `json:"items"`
	Count         int                    `json:"count"`
	InstanceCount int                    `json:"instanceCount"`
	ProjectCount  int                    `json:"projectCount"`
}

func summarizeDashboardCatalog(value catalog.ListResult) dashboardCatalogListResult {
	result := dashboardCatalogListResult{Items: make([]dashboardCatalogItem, 0, len(value.Items)), Count: value.Count, InstanceCount: value.InstanceCount, ProjectCount: value.ProjectCount}
	for _, item := range value.Items {
		result.Items = append(result.Items, dashboardCatalogItem{
			ID: item.ID.String(), Title: item.Title, SemanticModel: item.SemanticModel.String(),
			Source: string(item.Source), Status: string(item.Status), Visibility: string(item.Visibility),
			DraftID: string(item.DraftID), PageCount: item.PageCount,
		})
	}
	return result
}
