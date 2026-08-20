package app

import (
	"context"
	"testing"

	publicationsqlite "github.com/flidai/leapview/internal/dashboard/publication/sqlite"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

type dashboardPublicationServingStateFixture struct {
	state servingstate.State
}

func (f dashboardPublicationServingStateFixture) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	if id != f.state.ID {
		return servingstate.State{}, servingstate.ErrNotFound
	}
	return f.state, nil
}

func TestReconcileActivatedDashboardPublicationsProjectsCompiledDefinitions(t *testing.T) {
	store := testStore(t)
	state := servingstate.State{
		ID:                        "generation_publication",
		ProjectID:                 projectgraph.ResourceID("project:publication"),
		Environment:               servingstate.Environment("dev"),
		DashboardPublicationsJSON: `{"website":{"name":"website","dashboard":"dashboard:test.sales","defaultPage":"overview","allowedOrigins":["https://example.test"],"dependencyAssetIds":["dashboard:test.sales"],"configurationDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`,
	}
	if _, err := store.SQLDB().Exec(`INSERT INTO serving_states (id, project_id, environment, status) VALUES (?, ?, ?, ?)`, state.ID, state.ProjectID, state.Environment, "active"); err != nil {
		t.Fatalf("seed serving state: %v", err)
	}
	activated := deployment.Deployment{
		ServingIdentity: projectgraph.ServingIdentity{
			ProjectID: projectgraph.ResourceID("project:publication"), Environment: "dev", GenerationID: string(state.ID),
		},
		ActivationPrincipal: "principal:publisher",
	}
	states := dashboardPublicationServingStateFixture{state: state}
	if err := reconcileActivatedDashboardPublications(t.Context(), store.SQLDB(), states, activated); err != nil {
		t.Fatalf("reconcile activated dashboard publications: %v", err)
	}

	publication, err := publicationsqlite.NewRepository(store.SQLDB()).Get(t.Context(), projectgraph.ResourceID("project:publication"), "website")
	if err != nil {
		t.Fatalf("load reconciled publication: %v", err)
	}
	if !publication.Configured || publication.ServingStateID != string(state.ID) || publication.Dashboard != "dashboard:test.sales" || publication.DefaultPage != "overview" {
		t.Fatalf("reconciled publication = %+v", publication)
	}
	if len(publication.AllowedOrigins) != 1 || publication.AllowedOrigins[0] != "https://example.test" {
		t.Fatalf("reconciled publication origins = %#v", publication.AllowedOrigins)
	}

	state.DashboardPublicationsJSON = `{}`
	states.state = state
	activated.ActivationPrincipal = "principal:publisher-2"
	if err := reconcileActivatedDashboardPublications(t.Context(), store.SQLDB(), states, activated); err != nil {
		t.Fatalf("disable removed dashboard publication: %v", err)
	}
	publication, err = publicationsqlite.NewRepository(store.SQLDB()).Get(t.Context(), projectgraph.ResourceID("project:publication"), "website")
	if err != nil {
		t.Fatalf("load disabled publication: %v", err)
	}
	if publication.Configured || publication.Status() != "unconfigured" {
		t.Fatalf("removed publication remained configured: %+v", publication)
	}
}
