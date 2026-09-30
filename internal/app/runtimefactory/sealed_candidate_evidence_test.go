package runtimefactory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

type sealedCandidateEvidenceStub struct {
	wantIdentity  projectgraph.ServingIdentity
	wantCandidate string
	wantSeal      string
	wantArtifact  string
	evidence      ActivationEvidence
	err           error
	calls         int
	gotCandidate  string
	gotSeal       string
	gotArtifact   string
}

func (s *sealedCandidateEvidenceStub) SealedCandidateResultIdentityEvidence(
	_ context.Context,
	identity projectgraph.ServingIdentity,
	candidateID string,
	sealID string,
	artifactDigest string,
) (ActivationEvidence, error) {
	s.calls++
	s.gotCandidate, s.gotSeal, s.gotArtifact = candidateID, sealID, artifactDigest
	if identity != s.wantIdentity || candidateID != s.wantCandidate || sealID != s.wantSeal || artifactDigest != s.wantArtifact {
		return ActivationEvidence{}, errors.New("sealed candidate evidence input mismatch")
	}
	return s.evidence, s.err
}

func TestDependencyEvidenceForSealedActivationUsesCandidateProofWithoutActiveLookup(t *testing.T) {
	graphValue, manifest := dependencyEvidenceProjectFixture(t)
	artifact, err := projectartifact.NewSourceBundle(graphValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:demo", "production", "generation-42")
	if err != nil {
		t.Fatal(err)
	}
	activation := ActivationEvidence{
		RuntimeVersion: "runtime:v1", BindingFingerprint: dependencyEvidenceTestDigest('a'),
		BindingKinds: map[string]string{"connection:warehouse": "managed"},
		Capabilities: []runtimehost.RuntimeCapabilityEvidence{dependencyEvidenceTestCapability('1')},
	}
	sealed := &sealedCandidateEvidenceStub{
		wantIdentity: identity, wantCandidate: "candidate-42", wantSeal: "seal-42", wantArtifact: dependencyEvidenceTestDigest('b'),
		evidence: activation,
	}
	active := &activationEvidenceStub{want: identity, evidence: ActivationEvidence{}}
	input := runtimehost.RuntimeInput{
		State:                     servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment)},
		Artifact:                  servingstate.Artifact{Digest: dependencyEvidenceTestDigest('b')},
		SealedActivationCandidate: &runtimehost.CandidateRuntimeContext{CandidateID: "candidate-42"},
	}
	evidence, err := dependencyEvidenceForRuntimeInput(
		t.Context(), identity, projectbundle.CompiledSourceBundleArtifact{Graph: graphValue, Manifest: manifest}, artifact,
		runtimehost.ManagedDataResolution{Revisions: map[string]string{"connection:warehouse": dependencyEvidenceTestDigest('c')}}, input, "seal-42", active, sealed,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence["semantic:sales"].Available() {
		t.Fatal("sealed candidate proof did not produce serving dependency evidence")
	}
	if active.called {
		t.Fatal("precommit sealed activation consulted the committed active-evidence source")
	}
	if sealed.calls != 1 || sealed.gotCandidate != "candidate-42" || sealed.gotSeal != "seal-42" || sealed.gotArtifact != input.Artifact.Digest {
		t.Fatalf("sealed evidence calls/input = %d %q %q %q", sealed.calls, sealed.gotCandidate, sealed.gotSeal, sealed.gotArtifact)
	}
	if input.Candidate != nil || input.SealedActivationCandidate == nil {
		t.Fatal("sealed activation must retain its separate identity without becoming a private candidate runtime")
	}
}

func TestDependencyEvidenceForSealedActivationFailsClosed(t *testing.T) {
	graphValue, manifest := dependencyEvidenceProjectFixture(t)
	artifact, err := projectartifact.NewSourceBundle(graphValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:demo", "production", "generation-42")
	if err != nil {
		t.Fatal(err)
	}
	baseInput := runtimehost.RuntimeInput{
		State:                     servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment)},
		Artifact:                  servingstate.Artifact{Digest: dependencyEvidenceTestDigest('b')},
		SealedActivationCandidate: &runtimehost.CandidateRuntimeContext{CandidateID: "candidate-42"},
	}
	for _, test := range []struct {
		name   string
		input  runtimehost.RuntimeInput
		source SealedCandidateEvidenceSource
	}{
		{name: "missing sealed source", input: baseInput},
		{name: "failed sealed source", input: baseInput, source: &sealedCandidateEvidenceStub{wantIdentity: identity, wantCandidate: "candidate-42", wantSeal: "seal-42", wantArtifact: baseInput.Artifact.Digest, err: errors.New("candidate not committed")}},
		{name: "both candidate identities", input: withBothCandidateIdentities(baseInput), source: &sealedCandidateEvidenceStub{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			active := &activationEvidenceStub{want: identity}
			_, err := dependencyEvidenceForRuntimeInput(
				t.Context(), identity, projectbundle.CompiledSourceBundleArtifact{Graph: graphValue, Manifest: manifest}, artifact,
				runtimehost.ManagedDataResolution{}, test.input, "seal-42", active, test.source,
			)
			if err == nil {
				t.Fatal("invalid sealed activation evidence unexpectedly succeeded")
			}
			if active.called {
				t.Fatal("sealed activation failure fell back to active committed evidence")
			}
		})
	}
}

func TestDependencyEvidenceForRuntimeInputKeepsCommittedActiveSource(t *testing.T) {
	graphValue, manifest := dependencyEvidenceProjectFixture(t)
	artifact, err := projectartifact.NewSourceBundle(graphValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:demo", "production", "generation-42")
	if err != nil {
		t.Fatal(err)
	}
	activation := ActivationEvidence{
		RuntimeVersion: "runtime:v1", BindingFingerprint: dependencyEvidenceTestDigest('a'),
		BindingKinds: map[string]string{"connection:warehouse": "managed"},
		Capabilities: []runtimehost.RuntimeCapabilityEvidence{dependencyEvidenceTestCapability('1')},
	}
	active := &activationEvidenceStub{want: identity, evidence: activation}
	sealed := &sealedCandidateEvidenceStub{}
	input := runtimehost.RuntimeInput{
		State:    servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment)},
		Artifact: servingstate.Artifact{Digest: dependencyEvidenceTestDigest('b')},
	}
	if _, err := dependencyEvidenceForRuntimeInput(
		t.Context(), identity, projectbundle.CompiledSourceBundleArtifact{Graph: graphValue, Manifest: manifest}, artifact,
		runtimehost.ManagedDataResolution{Revisions: map[string]string{"connection:warehouse": dependencyEvidenceTestDigest('c')}},
		input, "", active, sealed,
	); err != nil {
		t.Fatal(err)
	}
	if !active.called || sealed.calls != 0 {
		t.Fatalf("active/sealed evidence calls = %t/%d", active.called, sealed.calls)
	}
}

func TestPrepareDashboardUsesSealedEvidenceAndKeepsProductionRuntimeIdentity(t *testing.T) {
	graphValue, manifest := dependencyEvidenceProjectFixture(t)
	artifact, err := projectartifact.NewSourceBundle(graphValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	_, artifactDigest, err := projectbundle.PackCompiledSourceBundle(artifact, projectcompiler.BundlePlan{
		Connections: []string{"connection:warehouse"}, Sources: []string{"source:orders"}, Models: []string{"model:orders"},
		SemanticModels: []string{"semantic:sales"},
	}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:demo", "production", "generation-42")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "serving.tar.gz")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	stateID := servingstate.ID(identity.GenerationID)
	input := runtimehost.RuntimeInput{
		State:    servingstate.State{ID: stateID, ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment)},
		Artifact: servingstate.Artifact{ID: "artifact-42", ServingStateID: stateID, Path: path, Digest: artifactDigest},
		ManagedData: runtimehost.ManagedDataResolution{
			RevisionID: dependencyEvidenceTestDigest('d'), Roots: map[string]string{"connection:warehouse": "/managed/warehouse"},
			Revisions: map[string]string{"connection:warehouse": dependencyEvidenceTestDigest('c')},
		},
		SealedActivationCandidate: &runtimehost.CandidateRuntimeContext{CandidateID: "candidate-42"},
	}
	sealed := &sealedCandidateEvidenceStub{
		wantIdentity: identity, wantCandidate: "candidate-42", wantSeal: "seal-42", wantArtifact: artifactDigest,
		evidence: ActivationEvidence{
			RuntimeVersion: "runtime:v1", BindingFingerprint: dependencyEvidenceTestDigest('a'),
			BindingKinds: map[string]string{"connection:warehouse": "managed"},
			Capabilities: []runtimehost.RuntimeCapabilityEvidence{dependencyEvidenceTestCapability('1')},
		},
	}
	active := &activationEvidenceStub{want: identity}
	factory := servingStateRuntimeFactory{runtimeDir: t.TempDir(), activationEvidence: active, sealedCandidateEvidence: sealed}
	var built *dashboardruntimefactory.Input
	builder := func(_ context.Context, got dashboardruntimefactory.Input, _ *ducklake.Environment) (*dashboardruntime.Service, error) {
		built = &got
		return nil, nil
	}
	if _, err := factory.prepareDashboard(t.Context(), input, builder, &ducklake.Environment{}, "relation", "target", "seal-42"); err != nil {
		t.Fatal(err)
	}
	if built == nil {
		t.Fatal("dashboard builder was not called after sealed evidence succeeded")
	}
	if built.CandidateID != "" {
		t.Fatalf("sealed activation became a private candidate runtime: candidate ID %q", built.CandidateID)
	}
	if active.called || sealed.calls != 1 {
		t.Fatalf("evidence source calls: active=%t sealed=%d", active.called, sealed.calls)
	}

	denied := servingStateRuntimeFactory{runtimeDir: t.TempDir(), activationEvidence: active}
	builderCalled := false
	denyingBuilder := func(context.Context, dashboardruntimefactory.Input, *ducklake.Environment) (*dashboardruntime.Service, error) {
		builderCalled = true
		return nil, nil
	}
	if _, err := denied.prepareDashboard(t.Context(), input, denyingBuilder, &ducklake.Environment{}, "relation", "target", "seal-42"); !errors.Is(err, errSealedCandidateEvidenceUnavailable) {
		t.Fatalf("missing sealed proof error = %v", err)
	}
	if builderCalled || active.called {
		t.Fatalf("missing sealed proof reached builder/active evidence: builder=%t active=%t", builderCalled, active.called)
	}
}

func TestServingStatePrepareRejectsSealedActivationContext(t *testing.T) {
	factory := servingStateRuntimeFactory{}
	_, err := factory.Prepare(t.Context(), runtimehost.RuntimeInput{
		SealedActivationCandidate: &runtimehost.CandidateRuntimeContext{CandidateID: "candidate-42"},
	})
	if !errors.Is(err, errSealedCandidateEvidenceUnavailable) {
		t.Fatalf("nonsealed preparation error = %v", err)
	}
}

func withBothCandidateIdentities(input runtimehost.RuntimeInput) runtimehost.RuntimeInput {
	input.Candidate = &runtimehost.CandidateRuntimeContext{CandidateID: "candidate-42"}
	return input
}
