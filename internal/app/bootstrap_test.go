package app

import (
	"context"
	"errors"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type bootstrapStateStoreFake struct {
	refreshmodule.ServingStateRepository
	scopes []servingstate.ActiveScope
	err    error
}

func (s bootstrapStateStoreFake) ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error) {
	return s.scopes, s.err
}

type bootstrapClaimStoreFake struct {
	deployment.ProjectClaimRepository
	claim deployment.ProjectClaim
	err   error
}

func (c bootstrapClaimStoreFake) GetProjectClaim(context.Context) (deployment.ProjectClaim, error) {
	return c.claim, c.err
}

func bootstrapProject(t *testing.T, value string) projectgraph.ResourceID {
	t.Helper()
	id, err := projectgraph.NewResourceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHasActiveBootstrapServingStateUsesFreshScopes(t *testing.T) {
	project := bootstrapProject(t, "project_demo")
	for _, test := range []struct {
		name    string
		store   bootstrapStateStoreFake
		want    bool
		wantErr bool
	}{
		{name: "empty fresh store", store: bootstrapStateStoreFake{}, want: false},
		{name: "active scope", store: bootstrapStateStoreFake{scopes: []servingstate.ActiveScope{{ProjectID: project, Environment: "prod"}}}, want: true},
		{name: "state error", store: bootstrapStateStoreFake{err: errors.New("state unavailable")}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := hasActiveBootstrapServingState(context.Background(), nil, test.store, "prod")
			if (err != nil) != test.wantErr {
				t.Fatalf("hasActiveBootstrapServingState() error = %v, want error: %t", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("hasActiveBootstrapServingState() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestBootstrapAPIGenDecision(t *testing.T) {
	project := bootstrapProject(t, "project_demo")
	foreign := bootstrapProject(t, "project_foreign")
	environment := servingstate.Environment("prod")
	emptyState := bootstrapStateStoreFake{}
	tests := []struct {
		name      string
		operation string
		claims    bootstrapClaimStoreFake
		states    bootstrapStateStoreFake
		project   projectgraph.ResourceID
		want      accessmodule.APIGenBootstrapDecision
		wantErr   bool
	}{
		{name: "no claim start allowed", operation: "startProjectCandidate", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "no claim plan allowed", operation: "planProjectCandidateSynchronization", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "no claim unrelated denied", operation: "getProjectCandidate", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "exact claim candidate allowed", operation: "getProjectCandidate", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "foreign claim denied", operation: "getProjectCandidate", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: foreign, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "active is unhandled", operation: "startProjectCandidate", claims: bootstrapClaimStoreFake{err: errors.New("claim should not be read")}, states: bootstrapStateStoreFake{scopes: []servingstate.ActiveScope{{ProjectID: project, Environment: environment}}}, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: false}},
		{name: "state error", operation: "startProjectCandidate", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: bootstrapStateStoreFake{err: errors.New("state unavailable")}, project: project, wantErr: true},
		{name: "claim error", operation: "startProjectCandidate", claims: bootstrapClaimStoreFake{err: errors.New("claim unavailable")}, states: emptyState, project: project, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := bootstrapAPIGenDecision(context.Background(), nil, test.states, test.claims, string(environment), test.operation, test.project)
			if (err != nil) != test.wantErr {
				t.Fatalf("bootstrapAPIGenDecision() error = %v, want error: %t", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("bootstrapAPIGenDecision() = %#v, want %#v", got, test.want)
			}
		})
	}
}
