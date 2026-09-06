package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
)

func sourceBundleFixture(t *testing.T) projectartifact.SourceBundle {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse", Provenance: projectgraph.Provenance{Path: "connections/warehouse.yaml"}},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders", Provenance: projectgraph.Provenance{Path: "sources/orders.yaml"}},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model", Provenance: projectgraph.Provenance{Path: "models/orders.yaml"}},
	}, []projectgraph.Edge{{From: "source:orders", To: "connection:warehouse"}, {From: "model:orders", To: "source:orders"}})
	if err != nil {
		t.Fatal(err)
	}
	project, err := projectartifact.NewSourceBundle(graphValue, manifest.ResourceManifest{
		Connections: map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed"}},
		Sources:     map[string]semanticmodel.Source{"source:orders": {Connection: "connection:warehouse"}},
		Models: map[string]semanticmodel.Table{
			"model:orders": {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}, SourceDependencies: []string{"source:orders"}},
		},
		ResourceFiles: map[string]string{
			"connection:warehouse": "connections/warehouse.yaml",
			"source:orders":        "sources/orders.yaml",
			"model:orders":         "models/orders.yaml",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func bundlePlan(project projectartifact.SourceBundle) projectcompiler.BundlePlan {
	return projectcompiler.BundlePlan{Connections: []string{"connection:warehouse"}, Sources: []string{"source:orders"}, Models: []string{"model:orders"}}
}

func TestPackCompiledSourceBundleUsesPortableDigestsAndDeterministicBytes(t *testing.T) {
	project := sourceBundleFixture(t)
	var first, second bytes.Buffer
	manifestA, digestA, err := PackCompiledSourceBundle(project, bundlePlan(project), &first)
	if err != nil {
		t.Fatal(err)
	}
	manifestB, digestB, err := PackCompiledSourceBundle(project, bundlePlan(project), &second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) || digestA != digestB || !reflect.DeepEqual(manifestA, manifestB) {
		t.Fatal("identical source bundles are not deterministic")
	}
	if manifestA.CompiledPath != CompiledSourceBundleFile || manifestA.BundleDigest != project.Digest() || manifestA.GraphDigest != project.Graph().Digest() {
		t.Fatalf("portable manifest = %#v", manifestA)
	}
	encoded := string(first.Bytes())
	for _, forbidden := range []string{"projectId", "projectDigest", "catalogPath", "leapview.yaml"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("bundle retained forbidden legacy field/path %q", forbidden)
		}
	}
	path := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(path, first.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	validation, err := ValidateArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(validation.RootDir)
	if validation.Digest != digestA || validation.BundleDigest != project.Digest() || validation.Graph.Digest() != project.Graph().Digest() {
		t.Fatalf("validated bundle = (%q, %q, %q)", validation.Digest, validation.BundleDigest, validation.Graph.Digest())
	}
}

func TestPackSourceBundleCollectsOnlySourceRootFiles(t *testing.T) {
	project := sourceBundleFixture(t)
	sourceRoot := t.TempDir()
	files := map[string][]byte{
		"connections/warehouse.yaml": []byte("connection"),
		"sources/orders.yaml":        []byte("source"),
		"models/orders.yaml":         []byte("model"),
	}
	for path, content := range files {
		fullPath := filepath.Join(sourceRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	manifestValue, _, err := PackSourceBundle(sourceRoot, PackSourceBundleOptions{SourceBundle: project, Plan: bundlePlan(project)}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifestValue.Files) != len(files) {
		t.Fatalf("source manifest files = %#v, want %d", manifestValue.Files, len(files))
	}
	entries := tarEntries(t, output.Bytes())
	for path := range files {
		if _, ok := entries[path]; !ok {
			t.Fatalf("source-root file %q missing from bundle entries %v", path, entries)
		}
	}
	if _, ok := entries["leapview.yaml"]; ok {
		t.Fatal("bundle unexpectedly included legacy project root")
	}
}

func TestPackSourceBundleIncludesDashboardFragments(t *testing.T) {
	base := sourceBundleFixture(t)
	resources := append(base.Graph().Resources(),
		projectgraph.Resource{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales", Provenance: projectgraph.Provenance{Path: "semantic-models/sales.yaml"}},
		projectgraph.Resource{ID: "dashboard:sales", Kind: projectgraph.KindDashboard, Name: "sales-dashboard", Provenance: projectgraph.Provenance{Path: "dashboards/sales.yaml"}},
	)
	edges := append(base.Graph().Edges(),
		projectgraph.Edge{From: "semantic:sales", To: "model:orders"},
		projectgraph.Edge{From: "dashboard:sales", To: "semantic:sales"},
	)
	graphValue, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	projectManifest := base.Manifest()
	projectManifest.SemanticModels = map[string]*semanticmodel.Model{"semantic:sales": {Name: "sales"}}
	projectManifest.DashboardDefinitions = map[string]dashboarddefinition.Definition{
		"dashboard:sales": {ID: "dashboard:sales", SemanticModel: "semantic:sales"},
	}
	projectManifest.DashboardSources = map[string]manifest.DashboardSource{
		"dashboard:sales": {Document: document.DashboardDocument{
			APIVersion: "leapview.dev/v1", Kind: document.DashboardResourceKindDashboard,
			Metadata: document.DashboardMetadata{ID: "dashboard:sales", Name: "sales-dashboard"},
			Spec:     document.DashboardSpec{SemanticModel: "semantic:sales"},
		}, Path: "dashboards/sales.yaml"},
	}
	projectManifest.ResourceFiles["semantic:sales"] = "semantic-models/sales.yaml"
	projectManifest.ResourceFiles["dashboard:sales"] = "dashboards/sales.yaml"
	project, err := projectartifact.NewSourceBundle(graphValue, projectManifest)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	files := map[string]string{
		"connections/warehouse.yaml": "connection",
		"sources/orders.yaml":        "source",
		"models/orders.yaml":         "model",
		"semantic-models/sales.yaml": "semantic model",
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales}
spec:
  semanticModel: sales
  filters: []
  includes: {visuals: [fragments/visuals.yaml]}
  visuals: {}
  pages: []
`,
		"dashboards/fragments/visuals.yaml": `visuals:
  revenue:
    type: bar
    query: {type: aggregate, dimensions: [], metrics: [revenue]}
    presentation: {type: cartesian}
`,
	}
	for name, content := range files {
		path := filepath.Join(sourceRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan := bundlePlan(project)
	plan.SemanticModels = []string{"semantic:sales"}
	plan.Dashboards = []string{"dashboard:sales"}
	var output bytes.Buffer
	manifestValue, _, err := PackSourceBundle(sourceRoot, PackSourceBundleOptions{SourceBundle: project, Plan: plan}, &output)
	if err != nil {
		t.Fatal(err)
	}
	entries := tarEntries(t, output.Bytes())
	if _, ok := entries["dashboards/fragments/visuals.yaml"]; !ok {
		t.Fatalf("dashboard fragment missing from source bundle: manifest=%#v entries=%v", manifestValue.Files, entries)
	}
}

func TestPackSourceBundleRejectsUnexpectedOrMissingSourceRootFiles(t *testing.T) {
	project := sourceBundleFixture(t)
	sourceRoot := t.TempDir()
	files := map[string][]byte{
		"connections/warehouse.yaml": []byte("connection"),
		"sources/orders.yaml":        []byte("source"),
		"models/orders.yaml":         []byte("model"),
	}
	for path, content := range files {
		fullPath := filepath.Join(sourceRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	withExtra := make(map[string][]byte, len(files)+1)
	for path, content := range files {
		withExtra[path] = content
	}
	withExtra["leapview.yaml"] = []byte("legacy project")
	var output bytes.Buffer
	if _, _, err := PackSourceBundle(sourceRoot, PackSourceBundleOptions{SourceBundle: project, Plan: bundlePlan(project), SourceFiles: withExtra}, &output); err == nil {
		t.Fatal("PackSourceBundle accepted an unreferenced source file")
	}
	missing := map[string][]byte{
		"connections/warehouse.yaml": files["connections/warehouse.yaml"],
		"sources/orders.yaml":        files["sources/orders.yaml"],
	}
	if _, _, err := PackSourceBundle(sourceRoot, PackSourceBundleOptions{SourceBundle: project, Plan: bundlePlan(project), SourceFiles: missing}, &output); err == nil {
		t.Fatal("PackSourceBundle accepted a missing source file")
	}
}

func TestBundleRejectsTamperedCompiledSourceBundleAndLegacyManifest(t *testing.T) {
	project := sourceBundleFixture(t)
	compiled, err := compiledSourceBundle(project, bundlePlan(project))
	if err != nil {
		t.Fatal(err)
	}
	compiled.BundleDigest = "sha256:" + strings.Repeat("f", 64)
	if err := ValidateCompiledSourceBundleArtifact(compiled); err == nil || !strings.Contains(err.Error(), "bundle digest") {
		t.Fatalf("tampered bundle digest error = %v", err)
	}
	compiled, err = compiledSourceBundle(project, bundlePlan(project))
	if err != nil {
		t.Fatal(err)
	}
	compiled.GraphDigest = "sha256:" + strings.Repeat("f", 64)
	if err := ValidateCompiledSourceBundleArtifact(compiled); err == nil || !strings.Contains(err.Error(), "graph digest") {
		t.Fatalf("tampered graph digest error = %v", err)
	}

	var output bytes.Buffer
	if _, _, err := PackCompiledSourceBundle(project, bundlePlan(project), &output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := ExtractArtifact(path, root); err != nil {
		t.Fatal(err)
	}
	compiledPath := filepath.Join(root, CompiledSourceBundleFile)
	compiledBytes, err := os.ReadFile(compiledPath)
	if err != nil {
		t.Fatal(err)
	}
	compiledBytes = bytes.Replace(compiledBytes, []byte(`"graphDigest"`), []byte(`"tamperedDigest"`), 1)
	if err := os.WriteFile(compiledPath, compiledBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCompiledSourceBundleArtifact(root); err == nil {
		t.Fatal("loader accepted tampered compiled payload")
	}
	legacyManifest := `{"version":1,"projectId":"project:demo","projectDigest":"sha256:bad","graphDigest":"sha256:bad","catalogPath":"leapview.yaml","compiledPath":"compiled/project.json","compiledSha256":"sha256:bad","files":[]}`
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(legacyManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(root); err == nil {
		t.Fatal("readManifest accepted legacy identity/catalog fields")
	}
}

func TestExtractArtifactRejectsTraversalAndNonRegularEntries(t *testing.T) {
	for _, test := range []struct {
		name   string
		header *tar.Header
	}{
		{name: "traversal", header: &tar.Header{Name: "../escape", Mode: 0o644, Size: 1}},
		{name: "symlink", header: &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(test.header); err != nil {
				t.Fatal(err)
			}
			if test.header.Size > 0 {
				if _, err := tw.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(t.TempDir(), "bundle.tar.gz")
			if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := ExtractArtifact(archivePath, t.TempDir()); err == nil {
				t.Fatal("ExtractArtifact accepted unsafe archive entry")
			}
		})
	}
}

func tarEntries(t *testing.T, data []byte) map[string]struct{} {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := map[string]struct{}{}
	for {
		header, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return entries
			}
			t.Fatal(err)
		}
		entries[header.Name] = struct{}{}
	}
}
