package projectsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
)

func TestCompilerCompilesLogicalFilesAndReturnsCanonicalArtifact(t *testing.T) {
	files := compilerTestFiles()
	input := compilerTestInput(files, "project:test")
	output, err := (Compiler{}).Compile(context.Background(), input)
	if err != nil {
		t.Fatalf("Compiler.Compile: %v", err)
	}
	artifact, err := projectartifact.Decode(output.ProjectArtifact)
	if err != nil {
		t.Fatalf("artifact.Decode: %v", err)
	}
	if output.ProjectDigest != artifact.Digest() {
		t.Fatalf("project digest = %q, artifact = %q", output.ProjectDigest, artifact.Digest())
	}
	if output.CompilerVersion != projectartifact.CompilerVersion || output.SchemaVersion != projectartifact.Version {
		t.Fatalf("compiler contract = %q/%d", output.CompilerVersion, output.SchemaVersion)
	}
	if !bytes.Equal(output.ProjectArtifact, artifact.Canonical()) {
		t.Fatal("output artifact is not canonical")
	}
	var manifest map[string]any
	if err := json.Unmarshal(output.Manifest, &manifest); err != nil {
		t.Fatalf("output manifest JSON: %v", err)
	}
	if got, _ := manifest["id"].(string); got != "project:test" {
		t.Fatalf("manifest project id = %q", got)
	}
	if strings.Contains(string(output.ProjectArtifact), "/tmp/") {
		t.Fatal("artifact contains host path material")
	}
}

func TestCompilerRejectsContextIdentityAndSourceIntegrityFailures(t *testing.T) {
	files := compilerTestFiles()
	input := compilerTestInput(files, "project:test")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Compiler{}).Compile(canceled, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled compile error = %v", err)
	}
	input.ProjectID = "project:other"
	input.SourceDigest = compilerSourceDigest(input)
	if _, err := (Compiler{}).Compile(context.Background(), input); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong project id error = %v", err)
	}
	input = compilerTestInput(files, "project:test")
	input.Files[0].Digest = sha256Identity([]byte("tampered"))
	if _, err := (Compiler{}).Compile(context.Background(), input); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("bad source digest error = %v", err)
	}
	input = compilerTestInput(files, "project:test")
	input.Files[0].StorageSecurityDomain = "other"
	if _, err := (Compiler{}).Compile(context.Background(), input); err == nil || !strings.Contains(err.Error(), "security domain") {
		t.Fatalf("bad source domain error = %v", err)
	}
	input = compilerTestInput(files, "project:test")
	input.StorageSecurityDomain = ""
	for i := range input.Files {
		input.Files[i].StorageSecurityDomain = ""
	}
	if _, err := (Compiler{}).Compile(context.Background(), input); err == nil || !strings.Contains(err.Error(), "storage security domain") {
		t.Fatalf("empty storage domain error = %v", err)
	}
	input = compilerTestInput(files, "project:test")
	input.SourceDigest = sha256Identity([]byte("unbound-source"))
	if _, err := (Compiler{}).Compile(context.Background(), input); err == nil || !strings.Contains(err.Error(), "canonical source digest") {
		t.Fatalf("noncanonical source digest error = %v", err)
	}
}

func compilerSourceDigest(input CompileInput) string {
	entries := make([]projectpostgres.SourceSnapshotEntryInput, 0, len(input.Files))
	for _, file := range input.Files {
		entries = append(entries, projectpostgres.SourceSnapshotEntryInput{Path: file.Path, Digest: file.Digest, SizeBytes: int64(len(file.Bytes))})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return projectpostgres.CanonicalSourceDigest(input.ProjectID, input.ProjectFile, entries)
}

func compilerTestInput(files map[string][]byte, projectID string) CompileInput {
	entries := make([]SourceFile, 0, len(files))
	for path, body := range files {
		entries = append(entries, SourceFile{Path: path, Bytes: body, Digest: sha256Identity(body), StorageSecurityDomain: "runtime"})
	}
	sourceEntries := make([]projectpostgres.SourceSnapshotEntryInput, 0, len(entries))
	for _, file := range entries {
		sourceEntries = append(sourceEntries, projectpostgres.SourceSnapshotEntryInput{Path: file.Path, Digest: file.Digest, SizeBytes: int64(len(file.Bytes))})
	}
	sort.Slice(sourceEntries, func(i, j int) bool { return sourceEntries[i].Path < sourceEntries[j].Path })
	return CompileInput{ProjectID: projectID, StorageSecurityDomain: "runtime", ProjectFile: "leapview.yaml", SourceDigest: projectpostgres.CanonicalSourceDigest(projectID, "leapview.yaml", sourceEntries), Files: entries}
}

func compilerTestFiles() map[string][]byte {
	files := map[string]string{
		"leapview.yaml": `apiVersion: leapview.dev/v1
kind: Project
metadata: {id: project:test, name: test}
spec:
  connections: {include: [connections/*.yaml]}
  sources: {include: [sources/*.yaml]}
  models: {include: [models/*.yaml]}
  semanticModels: {include: [semantic-models/*.yaml]}
  pipelines: {include: [pipelines/*.yaml]}
  dashboards: {include: [dashboards/*.yaml]}
  access: {include: []}
  publications: {include: []}
`,
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {definition: {type: direct, source: orders}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {datasets: {orders: {model: orders_model}}, metrics: {order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.id}, empty: zero}}}
`,
		"pipelines/sales-refresh.yaml": `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:sales-refresh, name: sales-refresh}
spec: {selection: {semanticModel: sales}}
`,
		"dashboards/fragments/shared/visuals.yaml": `visuals:
  order_count:
    type: kpi
    query: {type: aggregate, dimensions: [], metrics: [order_count]}
    presentation: {type: kpi}
`,
		"dashboards/fragments/pages.yaml": `pages:
  - id: overview
    title: Overview
    components: []
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard}
spec:
  semanticModel: sales
  filters: []
  includes:
    visuals: [fragments/shared/visuals.yaml]
    pages: [fragments/pages.yaml]
  visuals: {}
  pages: []
`,
	}
	out := make(map[string][]byte, len(files))
	for path, body := range files {
		out[path] = []byte(body)
	}
	return out
}
