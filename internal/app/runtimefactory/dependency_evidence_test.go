package runtimefactory

import (
	"context"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/runtimehost"
)

type activationEvidenceStub struct {
	want     projectgraph.ServingIdentity
	evidence ActivationEvidence
	called   bool
}

func (s *activationEvidenceStub) ResultIdentityEvidence(_ context.Context, identity projectgraph.ServingIdentity) (ActivationEvidence, error) {
	s.called = true
	if identity != s.want {
		return ActivationEvidence{}, projectgraph.ErrInvalidServingIdentity
	}
	return s.evidence, nil
}

func TestDependencyEvidenceForProductionRuntimeIsReusable(t *testing.T) {
	graphValue, manifest := dependencyEvidenceProjectFixture(t)
	artifact, err := projectartifact.NewProject(graphValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:demo", "production", "generation-42")
	if err != nil {
		t.Fatal(err)
	}
	source := &activationEvidenceStub{want: identity, evidence: ActivationEvidence{
		RuntimeVersion:     "leapview-runtime:v1",
		BindingFingerprint: dependencyEvidenceTestDigest('a'),
		BindingKinds:       map[string]string{"connection:warehouse": "managed"},
		Capabilities:       []runtimehost.RuntimeCapabilityEvidence{dependencyEvidenceTestCapability('1')},
	}}
	evidenceByModel, err := dependencyEvidenceForRuntime(
		context.Background(), identity,
		projectbundle.CompiledProjectArtifact{ProjectID: "project:demo", Graph: graphValue, Manifest: manifest},
		artifact,
		runtimehost.ManagedDataResolution{Revisions: map[string]string{"connection:warehouse": dependencyEvidenceTestDigest('b')}},
		nil,
		source,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !source.called {
		t.Fatal("production activation evidence source was not consulted")
	}
	evidence := evidenceByModel["semantic:sales"]
	if !evidence.Available() {
		t.Fatal("production runtime dependency evidence is unavailable")
	}
	dependency, err := evidence.Dependency(resultidentity.PlanInput{
		Datasets: []string{"orders"}, PlannerDigest: dependencyEvidenceTestDigest('c'),
		SettingsDigest: dependencyEvidenceTestDigest('d'),
		ResultFormat:   resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1},
	})
	if err != nil {
		t.Fatalf("production evidence is not reusable: %v", err)
	}
	if dependency.Digest() == "" {
		t.Fatal("production evidence produced an empty dependency digest")
	}
}

func TestDependencyEvidenceFailsClosedPerExternalBundleBranch(t *testing.T) {
	graphValue, manifest := dependencyEvidenceProjectWithExternalDataset(t, false)
	artifact, err := projectartifact.NewProject(graphValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	evidenceByModel, err := buildDependencyEvidence(
		projectbundle.CompiledProjectArtifact{ProjectID: "project:demo", Graph: graphValue, Manifest: manifest},
		artifact,
		map[string]string{
			"connection:warehouse": dependencyEvidenceTestDigest('b'),
			// Even a digest-shaped value must not become evidence for a
			// connector whose identity capability is unavailable.
			"connection:external": dependencyEvidenceTestDigest('f'),
		},
		dependencyEvidenceActivation(map[string]string{
			"connection:warehouse": "managed", "connection:external": "http",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence := evidenceByModel["semantic:sales"]
	if !evidence.Available() {
		t.Fatal("managed-only branch lost reusable dependency evidence")
	}
	planInput := resultidentity.PlanInput{
		PlannerDigest: dependencyEvidenceTestDigest('c'), SettingsDigest: dependencyEvidenceTestDigest('d'),
		ResultFormat: resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1},
	}
	planInput.Datasets = []string{"orders"}
	if _, err := evidence.Dependency(planInput); err != nil {
		t.Fatalf("managed bundle branch dependency: %v", err)
	}
	planInput.Datasets = []string{"events"}
	if _, err := evidence.Dependency(planInput); err == nil {
		t.Fatal("external bundle branch received fallback dependency identity")
	}
	planInput.Datasets = []string{"orders", "events"}
	if _, err := evidence.Dependency(planInput); err == nil {
		t.Fatal("mixed managed/external plan received partial dependency identity")
	}
}

func TestDependencyEvidenceMixedSourceLineageIsUnavailable(t *testing.T) {
	graphValue, manifest := dependencyEvidenceProjectWithExternalDataset(t, true)
	artifact, err := projectartifact.NewProject(graphValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	evidenceByModel, err := buildDependencyEvidence(
		projectbundle.CompiledProjectArtifact{ProjectID: "project:demo", Graph: graphValue, Manifest: manifest},
		artifact,
		map[string]string{
			"connection:warehouse": dependencyEvidenceTestDigest('b'),
			"connection:external":  dependencyEvidenceTestDigest('f'),
		},
		dependencyEvidenceActivation(map[string]string{
			"connection:warehouse": "managed", "connection:external": "http",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidenceByModel["semantic:sales"].Available() {
		t.Fatal("model with mixed managed/external lineage remained reusable")
	}
}

func TestDependencyEvidenceSQLLineageFailsClosedWithoutPersistedEvidence(t *testing.T) {
	plan := resultidentity.PlanInput{
		Datasets: []string{"orders"}, PlannerDigest: dependencyEvidenceTestDigest('c'),
		SettingsDigest: dependencyEvidenceTestDigest('d'),
		ResultFormat:   resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1},
	}

	build := func(t *testing.T, validated bool) resultidentity.Evidence {
		t.Helper()
		graphValue, manifest := dependencyEvidenceProjectFixture(t)
		physical := manifest.Models["model:orders"]
		physical.Execution = semanticmodel.ExecutionDefinition{SQL: "SELECT * FROM orders"}
		physical.SourceDependencies = nil
		physical.SQLAnalysisEvidence = nil
		if validated {
			physical.SourceDependencies = []string{"source:orders"}
			physical.SQLAnalysisEvidence = &semanticmodel.SQLAnalysisEvidence{
				Validated: true, SourceRefs: []string{"orders"},
			}
		}
		manifest.Models["model:orders"] = physical
		semanticTable := manifest.SemanticModels["semantic:sales"].Tables["orders"]
		semanticTable.Execution = semanticmodel.ExecutionDefinition{SQL: "SELECT * FROM orders"}
		semanticTable.SourceDependencies = nil
		semanticTable.SQLAnalysisEvidence = nil
		if validated {
			semanticTable.SourceDependencies = []string{"orders"}
			semanticTable.SQLAnalysisEvidence = &semanticmodel.SQLAnalysisEvidence{
				Validated: true, SourceRefs: []string{"orders"},
			}
		}
		manifest.SemanticModels["semantic:sales"].Tables["orders"] = semanticTable

		artifact, err := projectartifact.NewProject(graphValue, manifest)
		if err != nil {
			t.Fatal(err)
		}
		evidenceByModel, err := buildDependencyEvidence(
			projectbundle.CompiledProjectArtifact{ProjectID: "project:demo", Graph: graphValue, Manifest: manifest},
			artifact,
			map[string]string{"connection:warehouse": dependencyEvidenceTestDigest('b')},
			dependencyEvidenceActivation(map[string]string{"connection:warehouse": "managed"}),
		)
		if err != nil {
			t.Fatal(err)
		}
		return evidenceByModel["semantic:sales"]
	}

	missing := build(t, false)
	if missing.Available() {
		t.Fatal("SQL model with missing persisted lineage remained reusable")
	}
	dependency, err := missing.Dependency(plan)
	if err == nil || dependency.Digest() != "" {
		t.Fatalf("missing SQL lineage dependency = %#v, %v; want no digest and fail-closed error", dependency, err)
	}

	validated := build(t, true)
	if !validated.Available() {
		t.Fatal("SQL model with validated managed lineage lost reusable evidence")
	}
	dependency, err = validated.Dependency(plan)
	if err != nil || dependency.Digest() == "" {
		t.Fatalf("validated SQL lineage dependency = %#v, %v; want reusable digest", dependency, err)
	}
}

func dependencyEvidenceProjectFixture(t *testing.T) (projectgraph.ProjectGraph, projectmanifest.Project) {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
	}, []projectgraph.Edge{
		{From: "source:orders", To: "connection:warehouse"},
		{From: "model:orders", To: "source:orders"},
		{From: "semantic:sales", To: "model:orders"},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := projectmanifest.Project{
		ID: "project:demo", Name: "demo",
		Connections: map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{
			"source:orders": {Connection: "connection:warehouse"},
		},
		Models: map[string]semanticmodel.Table{
			"model:orders": {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}, SourceDependencies: []string{"source:orders"}},
		},
		SemanticModels: map[string]*semanticmodel.Model{
			"semantic:sales": {
				Name: "sales",
				Sources: map[string]semanticmodel.Source{
					"orders": {},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{
					"orders": {Model: "orders_model"},
				},
				Tables: map[string]semanticmodel.Table{
					"orders": {ModelName: "orders_model", Execution: semanticmodel.ExecutionDefinition{Source: "orders"}},
				},
			},
		},
		NameIndex: projectmanifest.NameIndex{
			Connections:    map[string]string{"warehouse": "connection:warehouse"},
			Sources:        map[string]string{"orders": "source:orders"},
			Models:         map[string]string{"orders_model": "model:orders"},
			SemanticModels: map[string]string{"sales": "semantic:sales"},
		},
	}
	return graphValue, manifest
}

func dependencyEvidenceProjectWithExternalDataset(t *testing.T, mixedLineage bool) (projectgraph.ProjectGraph, projectmanifest.Project) {
	t.Helper()
	baseGraph, manifest := dependencyEvidenceProjectFixture(t)
	resources := append(baseGraph.Resources(),
		projectgraph.Resource{ID: "connection:external", Kind: projectgraph.KindConnection, Name: "external"},
		projectgraph.Resource{ID: "source:events", Kind: projectgraph.KindSource, Name: "events"},
	)
	edges := append(baseGraph.Edges(), projectgraph.Edge{From: "source:events", To: "connection:external"})
	manifest.Connections["connection:external"] = semanticmodel.Connection{Kind: "http"}
	pathLocation := &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{
			Type: "path", Path: "https://example.test/events.csv", Format: "csv",
		},
		Format: "csv",
	}}
	manifest.Sources["source:events"] = semanticmodel.Source{
		Connection: "connection:external", Path: "https://example.test/events.csv", Format: "csv",
		PathLocation: pathLocation, EffectivePathLocation: pathLocation,
	}
	manifest.NameIndex.Connections["external"] = "connection:external"
	manifest.NameIndex.Sources["events"] = "source:events"

	if mixedLineage {
		table := manifest.Models["model:orders"]
		table.Execution = semanticmodel.ExecutionDefinition{SQL: "SELECT * FROM orders JOIN events USING (id)"}
		table.SourceDependencies = []string{"source:orders", "source:events"}
		table.SQLAnalysisEvidence = &semanticmodel.SQLAnalysisEvidence{
			Validated: true, SourceRefs: []string{"source:orders", "source:events"},
		}
		manifest.Models["model:orders"] = table
		edges = append(edges, projectgraph.Edge{From: "model:orders", To: "source:events"})
	} else {
		resources = append(resources, projectgraph.Resource{ID: "model:events", Kind: projectgraph.KindModel, Name: "events_model"})
		edges = append(edges,
			projectgraph.Edge{From: "model:events", To: "source:events"},
			projectgraph.Edge{From: "semantic:sales", To: "model:events"},
		)
		manifest.Models["model:events"] = semanticmodel.Table{
			Execution: semanticmodel.ExecutionDefinition{Source: "source:events"}, SourceDependencies: []string{"source:events"},
		}
		manifest.NameIndex.Models["events_model"] = "model:events"
		model := manifest.SemanticModels["semantic:sales"]
		model.Sources["events"] = semanticmodel.Source{}
		model.Datasets["events"] = semanticmodel.SemanticDatasetSpec{Model: "events_model"}
		model.Tables["events"] = semanticmodel.Table{
			ModelName: "events_model", Execution: semanticmodel.ExecutionDefinition{Source: "events"},
		}
	}
	graphValue, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	return graphValue, manifest
}

func dependencyEvidenceActivation(bindingKinds map[string]string) ActivationEvidence {
	return ActivationEvidence{
		RuntimeVersion: "runtime:v1", BindingFingerprint: dependencyEvidenceTestDigest('a'),
		BindingKinds: bindingKinds,
		Capabilities: []runtimehost.RuntimeCapabilityEvidence{dependencyEvidenceTestCapability('1')},
	}
}

func TestCapabilityDigestUsesExactCanonicalEvidence(t *testing.T) {
	kinds := map[string]string{"connection:warehouse": "managed"}
	first := dependencyEvidenceTestCapability('1')
	same := first
	changed := first
	changed.Digest = dependencyEvidenceTestDigest('2')

	firstDigest, err := capabilityDigest(kinds, []runtimehost.RuntimeCapabilityEvidence{first})
	if err != nil {
		t.Fatal(err)
	}
	sameDigest, err := capabilityDigest(kinds, []runtimehost.RuntimeCapabilityEvidence{same})
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := capabilityDigest(kinds, []runtimehost.RuntimeCapabilityEvidence{changed})
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != sameDigest {
		t.Fatal("identical capability evidence produced different digests")
	}
	if firstDigest == changedDigest {
		t.Fatal("extension artifact digest did not rotate capability identity")
	}
	if _, err := capabilityDigest(kinds, nil); err == nil {
		t.Fatal("missing capability evidence produced a digest")
	}
}

func TestCandidateAndProductionCapabilityEvidenceDeriveCompatibleDependencies(t *testing.T) {
	graphValue, manifest := dependencyEvidenceProjectFixture(t)
	artifact, err := projectartifact.NewProject(graphValue, manifest)
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
	compiled := projectbundle.CompiledProjectArtifact{ProjectID: "project:demo", Graph: graphValue, Manifest: manifest}
	managed := runtimehost.ManagedDataResolution{Revisions: map[string]string{"connection:warehouse": dependencyEvidenceTestDigest('b')}}
	production, err := dependencyEvidenceForRuntime(
		t.Context(), identity, compiled, artifact, managed, nil,
		&activationEvidenceStub{want: identity, evidence: activation},
	)
	if err != nil {
		t.Fatal(err)
	}
	candidateContext := &runtimehost.CandidateRuntimeContext{
		RuntimeVersion: activation.RuntimeVersion, BindingFingerprint: activation.BindingFingerprint,
		BindingKinds: cloneStringMap(activation.BindingKinds), Capabilities: cloneRuntimeCapabilities(activation.Capabilities),
	}
	candidate, err := dependencyEvidenceForRuntime(t.Context(), identity, compiled, artifact, managed, candidateContext, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := resultidentity.PlanInput{
		Datasets: []string{"orders"}, PlannerDigest: dependencyEvidenceTestDigest('c'),
		SettingsDigest: dependencyEvidenceTestDigest('d'),
		ResultFormat:   resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1},
	}
	productionDependency, err := production["semantic:sales"].Dependency(plan)
	if err != nil {
		t.Fatal(err)
	}
	candidateDependency, err := candidate["semantic:sales"].Dependency(plan)
	if err != nil {
		t.Fatal(err)
	}
	if productionDependency.Digest() != candidateDependency.Digest() {
		t.Fatal("candidate and production capability evidence produced incompatible dependencies")
	}

	changed := *candidateContext
	changed.Capabilities = cloneRuntimeCapabilities(candidateContext.Capabilities)
	changed.Capabilities[0].Digest = dependencyEvidenceTestDigest('2')
	changedEvidence, err := dependencyEvidenceForRuntime(t.Context(), identity, compiled, artifact, managed, &changed, nil)
	if err != nil {
		t.Fatal(err)
	}
	changedDependency, err := changedEvidence["semantic:sales"].Dependency(plan)
	if err != nil {
		t.Fatal(err)
	}
	if changedDependency.Digest() == candidateDependency.Digest() {
		t.Fatal("candidate extension digest mismatch did not rotate dependency")
	}

	missing := *candidateContext
	missing.Capabilities = nil
	if _, err := dependencyEvidenceForRuntime(t.Context(), identity, compiled, artifact, managed, &missing, nil); err == nil {
		t.Fatal("missing candidate capability evidence did not fail closed")
	}
}

func TestDependencyEvidenceRelationProjectionIgnoresPresentationAndRotatesExecution(t *testing.T) {
	graphValue, baseManifest := dependencyEvidenceProjectFixture(t)
	baseTable := baseManifest.Models["model:orders"]
	baseTable.Columns = map[string]semanticmodel.ModelColumn{"order_id": {Name: "order_id", Datatype: semanticmodel.DataTypeString}}
	baseManifest.Models["model:orders"] = baseTable

	dependencyFor := func(manifest projectmanifest.Project, revision byte) resultidentity.Dependency {
		t.Helper()
		artifact, err := projectartifact.NewProject(graphValue, manifest)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := buildDependencyEvidence(
			projectbundle.CompiledProjectArtifact{ProjectID: "project:demo", Graph: graphValue, Manifest: manifest},
			artifact,
			map[string]string{"connection:warehouse": dependencyEvidenceTestDigest(revision)},
			ActivationEvidence{
				RuntimeVersion: "runtime:v1", BindingFingerprint: dependencyEvidenceTestDigest('a'),
				BindingKinds: map[string]string{"connection:warehouse": "managed"},
				Capabilities: []runtimehost.RuntimeCapabilityEvidence{dependencyEvidenceTestCapability('1')},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		dependency, err := evidence["semantic:sales"].Dependency(resultidentity.PlanInput{
			Datasets: []string{"orders"}, PlannerDigest: dependencyEvidenceTestDigest('c'),
			SettingsDigest: dependencyEvidenceTestDigest('d'),
			ResultFormat:   resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		return dependency
	}

	base := dependencyFor(baseManifest, 'b')
	presentation := cloneDependencyEvidenceManifest(baseManifest)
	connection := presentation.Connections["connection:warehouse"]
	connection.Description = "Display help"
	presentation.Connections["connection:warehouse"] = connection
	source := presentation.Sources["source:orders"]
	source.Description = "Display help"
	presentation.Sources["source:orders"] = source
	table := presentation.Models["model:orders"]
	table.Description = "Display help"
	column := table.Columns["order_id"]
	column.Description = "Display help"
	table.Columns["order_id"] = column
	presentation.Models["model:orders"] = table
	if got := dependencyFor(presentation, 'b'); got.Digest() != base.Digest() {
		t.Fatal("presentation-only relation metadata rotated dependency identity")
	}

	execution := cloneDependencyEvidenceManifest(baseManifest)
	table = execution.Models["model:orders"]
	column = table.Columns["order_id"]
	column.Datatype = semanticmodel.DataTypeInteger
	table.Columns["order_id"] = column
	execution.Models["model:orders"] = table
	if got := dependencyFor(execution, 'b'); got.Digest() == base.Digest() {
		t.Fatal("execution-affecting relation metadata did not rotate dependency identity")
	}
	if got := dependencyFor(baseManifest, 'f'); got.Digest() == base.Digest() {
		t.Fatal("managed content revision did not rotate dependency identity")
	}
}

func cloneDependencyEvidenceManifest(value projectmanifest.Project) projectmanifest.Project {
	clone := value
	clone.Connections = make(map[string]semanticmodel.Connection, len(value.Connections))
	for id, connection := range value.Connections {
		clone.Connections[id] = connection
	}
	clone.Sources = make(map[string]semanticmodel.Source, len(value.Sources))
	for id, source := range value.Sources {
		source.Fields = cloneSourceFields(source.Fields)
		source.Schema.Columns = append([]semanticmodel.ColumnSchema(nil), source.Schema.Columns...)
		clone.Sources[id] = source
	}
	clone.Models = make(map[string]semanticmodel.Table, len(value.Models))
	for id, table := range value.Models {
		table.Columns = cloneModelColumns(table.Columns)
		table.Schema.Columns = append([]semanticmodel.ColumnSchema(nil), table.Schema.Columns...)
		clone.Models[id] = table
	}
	return clone
}

func cloneSourceFields(values map[string]semanticmodel.SourceField) map[string]semanticmodel.SourceField {
	clone := make(map[string]semanticmodel.SourceField, len(values))
	for name, value := range values {
		clone[name] = value
	}
	return clone
}

func cloneModelColumns(values map[string]semanticmodel.ModelColumn) map[string]semanticmodel.ModelColumn {
	clone := make(map[string]semanticmodel.ModelColumn, len(values))
	for name, value := range values {
		clone[name] = value
	}
	return clone
}

func dependencyEvidenceTestDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}

func dependencyEvidenceTestCapability(value byte) runtimehost.RuntimeCapabilityEvidence {
	return runtimehost.RuntimeCapabilityEvidence{
		Name: "ducklake", Identity: dependencyEvidenceTestDigest(value), Digest: dependencyEvidenceTestDigest(value),
		DuckDBVersion: "duckdb:v1", ExtensionVersion: "extension:v1",
		GOOS: "linux", GOARCH: "amd64", Platform: "linux-amd64", SupportProfile: "stable",
	}
}
