// Package catalog defines the neutral read model shared by dashboard, agent,
// admin, and UI transports.
package catalog

import dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
import projectgraph "github.com/flidai/leapview/internal/project/graph"

type Catalog struct {
	Project    Project     `json:"project"`
	Models     []Model     `json:"models"`
	Dashboards []Dashboard `json:"dashboards"`
}

type Project struct {
	ID          projectgraph.ResourceID `json:"id"`
	Title       string                  `json:"title"`
	Description string                  `json:"description"`
}

type Model struct {
	ID          projectgraph.ResourceID `json:"id"`
	Title       string                  `json:"title"`
	Description string                  `json:"description"`
}

type Dashboard struct {
	ID            projectgraph.ResourceID   `json:"id"`
	Title         string                    `json:"title"`
	Description   string                    `json:"description"`
	SemanticModel projectgraph.ResourceID   `json:"semanticModel"`
	Tags          []string                  `json:"tags"`
	PageCount     int                       `json:"pageCount"`
	Appearance    dashboardappearance.Value `json:"appearance"`
}
