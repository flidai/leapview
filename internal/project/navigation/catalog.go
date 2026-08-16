// Package navigation defines the project-owned read projection used to
// render application navigation. Dashboard runtime catalogs are adapted into
// this projection at the project module boundary.
package navigation

import dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"

type Catalog struct {
	Project    Project     `json:"project"`
	Models     []Model     `json:"models"`
	Dashboards []Dashboard `json:"dashboards"`
}

type Project struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Model struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Dashboard struct {
	ID            string                    `json:"id"`
	Title         string                    `json:"title"`
	Description   string                    `json:"description"`
	SemanticModel string                    `json:"semanticModel"`
	Tags          []string                  `json:"tags"`
	PageCount     int                       `json:"pageCount"`
	Appearance    dashboardappearance.Value `json:"appearance"`
}
