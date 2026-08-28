package app

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/extension"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
)

func TestActiveResultIdentityEvidenceUsesVerifiedReleaseInputs(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project:test", "production", "generation:active")
	if err != nil {
		t.Fatal(err)
	}
	bindings := []release.BindingEvidence{{
		BindingID: "binding:warehouse", ConnectionID: "connection:warehouse",
		ConnectorKind: "postgres", Revision: 4, ValidatedVersion: "postgres:v2",
		EndpointConfigHash: "sha256:endpoint",
	}}
	source := activeConnectionEvidenceSource{
		targetID: "instance:test", environment: "production",
		releases: sourceSchemaProvenanceStub{provenance: releasemodule.Provenance{Plan: release.GenerationPlanProvenance{
			Identity: identity, TargetID: "instance:test", RuntimeVersion: "runtime:v3",
			Bindings:            bindings,
			AuthoredConnections: []release.AuthoredConnectionEvidence{{ConnectionID: "connection:local", ConnectorKind: "sqlite"}},
			ManagedDataPins:     []release.ManagedDataPin{{ConnectionID: "connection:managed", RevisionID: "revision:one"}},
			Extensions:          []extension.Evidence{activeResultIdentityCapability('a')},
		}}},
	}
	evidence, err := source.ResultIdentityEvidence(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.RuntimeVersion != "runtime:v3" || evidence.BindingFingerprint != release.BindingFingerprint(bindings) {
		t.Fatalf("activation evidence = %#v", evidence)
	}
	wantKinds := map[string]string{
		"connection:warehouse": "postgres",
		"connection:local":     "sqlite",
		"connection:managed":   "managed",
	}
	for connection, want := range wantKinds {
		if evidence.BindingKinds[connection] != want {
			t.Fatalf("binding kind %q = %q, want %q", connection, evidence.BindingKinds[connection], want)
		}
	}
	if len(evidence.Capabilities) != 1 || evidence.Capabilities[0].Digest != activeResultIdentityDigest('a') {
		t.Fatalf("runtime capabilities = %#v", evidence.Capabilities)
	}
}

func activeResultIdentityCapability(value byte) extension.Evidence {
	return extension.Evidence{
		Name: "ducklake", Identity: activeResultIdentityDigest(value), Digest: activeResultIdentityDigest(value),
		DuckDBVersion: "duckdb:v1", ExtensionVersion: "extension:v1",
		GOOS: "linux", GOARCH: "amd64", Platform: "linux-amd64", SupportProfile: "stable",
	}
}

func activeResultIdentityDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}

func TestActiveResultIdentityEvidenceRejectsDifferentGeneration(t *testing.T) {
	requested, err := projectgraph.NewServingIdentity("project:test", "production", "generation:active")
	if err != nil {
		t.Fatal(err)
	}
	other, err := projectgraph.NewServingIdentity("project:test", "production", "generation:other")
	if err != nil {
		t.Fatal(err)
	}
	source := activeConnectionEvidenceSource{
		targetID: "instance:test", environment: "production",
		releases: sourceSchemaProvenanceStub{provenance: releasemodule.Provenance{Plan: release.GenerationPlanProvenance{
			Identity: other, TargetID: "instance:test",
		}}},
	}
	if _, err := source.ResultIdentityEvidence(t.Context(), requested); err == nil {
		t.Fatal("generation-mismatched activation evidence was accepted")
	}
}
