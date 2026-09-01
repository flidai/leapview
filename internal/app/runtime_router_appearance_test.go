package app

import (
	"context"
	"testing"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

type dashboardAppearanceProjectReaderStub struct {
	project projectmanifest.Project
}

func (s dashboardAppearanceProjectReaderStub) ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	return s.project, nil, nil
}

type dashboardAppearanceReaderStub struct {
	records map[projectgraph.ResourceID]dashboardappearance.Record
}

func (s dashboardAppearanceReaderStub) ListProject(context.Context, projectgraph.ResourceID) (map[projectgraph.ResourceID]dashboardappearance.Record, error) {
	return s.records, nil
}

func TestDashboardAppearanceResolverUsesAuthoredAppearanceAndPersistedOverrides(t *testing.T) {
	dashboardID := projectgraph.ResourceID("dashboard:showcase")
	authoredIcon := "gallery-vertical-end"
	authoredColor := dashboarddocument.DashboardAppearanceColorBlue
	reader := dashboardAppearanceProjectReaderStub{project: projectmanifest.Project{
		DashboardSources: map[string]projectmanifest.DashboardSource{
			dashboardID.String(): {Document: dashboarddocument.DashboardDocument{Spec: dashboarddocument.DashboardSpec{
				Appearance: &dashboarddocument.DashboardAppearance{Icon: &authoredIcon, Color: &authoredColor},
			}}},
		},
	}}

	authored, err := dashboardAppearanceResolver(reader, dashboardAppearanceReaderStub{})(t.Context(), "project:test", dashboardID)
	if err != nil {
		t.Fatal(err)
	}
	if authored.Icon != authoredIcon || authored.Color != "blue" {
		t.Fatalf("authored appearance = %#v", authored)
	}

	override := dashboardappearance.Value{Icon: "house", Color: "orange"}
	persisted, err := dashboardAppearanceResolver(reader, dashboardAppearanceReaderStub{records: map[projectgraph.ResourceID]dashboardappearance.Record{
		dashboardID: {Value: override},
	}})(t.Context(), "project:test", dashboardID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != override {
		t.Fatalf("persisted appearance = %#v, want %#v", persisted, override)
	}
}
