// Package navigation defines the workspace-owned read projection used to
// render application navigation. Dashboard runtime catalogs are adapted into
// this projection at the workspace module boundary.
package navigation

import dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"

type Catalog struct {
	Workspace  Workspace   `json:"workspace"`
	Models     []Model     `json:"models"`
	Dashboards []Dashboard `json:"dashboards"`
}

type Workspace struct {
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
