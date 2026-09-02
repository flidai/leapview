package runtimefactory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestPrepareDashboardRejectsMissingOrInvalidActiveRuntimeEvidence(t *testing.T) {
	artifact, identity, managed := runtimeDependencyEvidenceArtifact(t)
	tests := []struct {
		name   string
		source ActivationEvidenceSource
	}{
		{name: "missing source"},
		{name: "invalid evidence", source: &activationEvidenceStub{want: identity, evidence: ActivationEvidence{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builderCalled := false
			builder := func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment) (*dashboardruntime.Service, error) {
				builderCalled = true
				return &dashboardruntime.Service{}, nil
			}
			factory := servingStateRuntimeFactory{
				runtimeDir:         t.TempDir(),
				activationEvidence: test.source,
			}
			input := runtimehost.RuntimeInput{
				State:       servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment), Digest: artifact.Digest},
				Artifact:    artifact,
				ManagedData: managed,
			}
			_, err := factory.prepareDashboard(t.Context(), input, builder, &ducklake.Environment{}, "")
			if err == nil || !strings.Contains(err.Error(), "resolve runtime dependency evidence") {
				t.Fatalf("prepareDashboard() error = %v, want dependency-evidence failure", err)
			}
			if builderCalled {
				t.Fatal("dashboard builder was called after active evidence failure")
			}
		})
	}
}

func TestPrepareDashboardUsesValidatedCandidateRuntimeEvidenceWithoutActiveSource(t *testing.T) {
	artifact, identity, managed := runtimeDependencyEvidenceArtifact(t)
	builderCalled := false
	var gotEvidence bool
	builder := func(_ context.Context, input dashboardruntimefactory.Input, _ *ducklake.Environment) (*dashboardruntime.Service, error) {
		builderCalled = true
		evidence, ok := input.DependencyEvidence["semantic:sales"]
		gotEvidence = ok && evidence.Available()
		return &dashboardruntime.Service{}, nil
	}
	factory := servingStateRuntimeFactory{runtimeDir: t.TempDir()}
	input := runtimehost.RuntimeInput{
		State:       servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment), Digest: artifact.Digest},
		Artifact:    artifact,
		ManagedData: managed,
		Candidate: &runtimehost.CandidateRuntimeContext{
			CandidateID:        "candidate-1",
			RuntimeVersion:     "runtime:v1",
			BindingFingerprint: dependencyEvidenceTestDigest('a'),
			BindingKinds:       map[string]string{"connection:warehouse": "managed"},
			Capabilities:       []runtimehost.RuntimeCapabilityEvidence{dependencyEvidenceTestCapability('1')},
		},
	}
	if _, err := factory.prepareDashboard(t.Context(), input, builder, &ducklake.Environment{}, ""); err != nil {
		t.Fatalf("prepareDashboard() error = %v, want candidate evidence to remain valid", err)
	}
	if !builderCalled || !gotEvidence {
		t.Fatalf("builderCalled=%v, gotEvidence=%v; want candidate dependency evidence", builderCalled, gotEvidence)
	}
}

func runtimeDependencyEvidenceArtifact(t *testing.T) (servingstate.Artifact, projectgraph.ServingIdentity, runtimehost.ManagedDataResolution) {
	t.Helper()
	graphValue, manifest := dependencyEvidenceProjectFixture(t)
	project, err := projectartifact.NewProject(graphValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	plan := projectbundle.Plan{
		Project: "project:demo", Connections: []string{"connection:warehouse"},
		Sources: []string{"source:orders"}, Models: []string{"model:orders"},
		SemanticModels: []string{"semantic:sales"},
	}
	_, digest, err := projectbundle.PackCompiledProject(project, plan, &content)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "artifact.tar.gz")
	if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:demo", "production", "generation-42")
	if err != nil {
		t.Fatal(err)
	}
	return servingstate.Artifact{ID: "artifact-1", ServingStateID: servingstate.ID(identity.GenerationID), Digest: digest, Path: path}, identity,
		runtimehost.ManagedDataResolution{Revisions: map[string]string{"connection:warehouse": dependencyEvidenceTestDigest('b')}}
}
