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

type bootstrapTargetReaderFake struct {
	target deployment.DeliveryTarget
	err    error
}

func (r bootstrapTargetReaderFake) DeliveryTargetRevision(context.Context, string) (deployment.DeliveryTarget, error) {
	return r.target, r.err
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
			got, err := hasActiveBootstrapServingState(context.Background(), nil, test.store, "prod", nil, "", "")
			if (err != nil) != test.wantErr {
				t.Fatalf("hasActiveBootstrapServingState() error = %v, want error: %t", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("hasActiveBootstrapServingState() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestHasActiveBootstrapServingStateUsesCanonicalTargetScope(t *testing.T) {
	project := bootstrapProject(t, "project_demo")
	target := bootstrapTargetReaderFake{target: deployment.DeliveryTarget{
		TargetID:           "target_demo",
		ProjectID:          project.String(),
		Environment:        "prod",
		ActiveGenerationID: "generation_active",
	}}
	active, err := hasActiveBootstrapServingState(context.Background(), nil, bootstrapStateStoreFake{}, "prod", target, "target_demo", project.String())
	if err != nil {
		t.Fatalf("hasActiveBootstrapServingState() error = %v", err)
	}
	if !active {
		t.Fatal("hasActiveBootstrapServingState() = false, want active canonical target")
	}
	if _, err := hasActiveBootstrapServingState(context.Background(), nil, bootstrapStateStoreFake{}, "prod", target, "target_demo", ""); err == nil {
		t.Fatal("hasActiveBootstrapServingState() accepted a stale empty project scope")
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
		{name: "no claim managed data create allowed", operation: "createManagedDataUploadSession", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "no claim managed data read allowed", operation: "getManagedDataUploadSession", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "no claim managed data revision denied", operation: "listManagedDataRevisions", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "no claim unrelated denied", operation: "getProjectCandidate", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "no claim source retention denied", operation: "retainProjectCandidateSource", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "no claim delivery plan denied", operation: "createDeliveryPlan", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "no claim delivery build denied", operation: "buildDeliveryPlan", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "no claim delivery publish denied", operation: "publishDeliveryCandidate", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "exact claim candidate allowed", operation: "getProjectCandidate", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim source retention allowed", operation: "retainProjectCandidateSource", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim delivery plan allowed", operation: "createDeliveryPlan", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim delivery build allowed", operation: "buildDeliveryPlan", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim delivery publish allowed", operation: "publishDeliveryCandidate", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim deployment status allowed while active", operation: "getDeployment", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: bootstrapStateStoreFake{scopes: []servingstate.ActiveScope{{ProjectID: project, Environment: environment}}}, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim deployment events allowed while active", operation: "listDeploymentEvents", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: bootstrapStateStoreFake{scopes: []servingstate.ActiveScope{{ProjectID: project, Environment: environment}}}, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim synchronization plan allowed while active", operation: "planProjectCandidateSynchronization", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: bootstrapStateStoreFake{scopes: []servingstate.ActiveScope{{ProjectID: project, Environment: environment}}}, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim managed data finalize allowed", operation: "finalizeManagedDataUploadSession", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}},
		{name: "exact claim managed data revision denied", operation: "listManagedDataRevisions", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: project, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "foreign claim denied", operation: "getProjectCandidate", claims: bootstrapClaimStoreFake{claim: deployment.ProjectClaim{ProjectID: foreign, Environment: environment}}, states: emptyState, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}},
		{name: "active is unhandled", operation: "startProjectCandidate", claims: bootstrapClaimStoreFake{err: errors.New("claim should not be read")}, states: bootstrapStateStoreFake{scopes: []servingstate.ActiveScope{{ProjectID: project, Environment: environment}}}, project: project, want: accessmodule.APIGenBootstrapDecision{Handled: false}},
		{name: "state error", operation: "startProjectCandidate", claims: bootstrapClaimStoreFake{err: deployment.ErrProjectClaimNotFound}, states: bootstrapStateStoreFake{err: errors.New("state unavailable")}, project: project, wantErr: true},
		{name: "claim error", operation: "startProjectCandidate", claims: bootstrapClaimStoreFake{err: errors.New("claim unavailable")}, states: emptyState, project: project, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := bootstrapAPIGenDecision(context.Background(), nil, test.states, test.claims, string(environment), test.operation, test.project, nil, "")
			if (err != nil) != test.wantErr {
				t.Fatalf("bootstrapAPIGenDecision() error = %v, want error: %t", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("bootstrapAPIGenDecision() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestBootstrapAPIGenDecisionUsesCanonicalTargetPointer(t *testing.T) {
	project := bootstrapProject(t, "project_demo")
	got, err := bootstrapAPIGenDecision(
		context.Background(), nil, bootstrapStateStoreFake{}, bootstrapClaimStoreFake{err: errors.New("claim must not be read")},
		"prod", "startProjectCandidate", project,
		bootstrapTargetReaderFake{target: deployment.DeliveryTarget{TargetID: "target_demo", ProjectID: project.String(), Environment: "prod", ActiveGenerationID: "state_active"}},
		"target_demo",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != (accessmodule.APIGenBootstrapDecision{Handled: false}) {
		t.Fatalf("bootstrap decision = %#v, want active canonical target to close bootstrap", got)
	}
}
