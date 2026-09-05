package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
)

func bundleProject(t *testing.T) projectartifact.Project {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:demo", Kind: projectgraph.KindProject, Name: "demo", Provenance: projectgraph.Provenance{Path: "leapview.yaml"}},
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse", Provenance: projectgraph.Provenance{Path: "connections/warehouse.yaml"}},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders", Provenance: projectgraph.Provenance{Path: "sources/orders.yaml"}},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model", Provenance: projectgraph.Provenance{Path: "models/orders.yaml"}},
	}, []projectgraph.Edge{{From: "source:orders", To: "connection:warehouse"}, {From: "model:orders", To: "source:orders"}})
	if err != nil {
		t.Fatal(err)
	}
	project, err := projectartifact.NewProject(graphValue, manifest.Project{
		ID:   "project:demo",
		Name: "demo",
		Connections: map[string]semanticmodel.Connection{
			"connection:warehouse": {Kind: "managed"},
		},
		Sources: map[string]semanticmodel.Source{
			"source:orders": {Connection: "connection:warehouse"},
		},
		Models: map[string]semanticmodel.Table{
			"model:orders": {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}},
		},
		ResourceFiles: map[string]string{"project:demo": "leapview.yaml", "connection:warehouse": "connections/warehouse.yaml", "source:orders": "sources/orders.yaml", "model:orders": "models/orders.yaml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func bundlePlan(project projectartifact.Project) Plan {
	return Plan{Project: project.ProjectID().String(), Connections: []string{"connection:warehouse"}, Sources: []string{"source:orders"}, Models: []string{"model:orders"}}
}

func TestPackCompiledProjectUsesSingleDeterministicCompiledPath(t *testing.T) {
	project := bundleProject(t)
	var first, second bytes.Buffer
	manifestA, digestA, err := PackCompiledProject(project, bundlePlan(project), &first)
	if err != nil {
		t.Fatal(err)
	}
	manifestB, digestB, err := PackCompiledProject(project, bundlePlan(project), &second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) || digestA != digestB {
		t.Fatal("identical project bundles are not deterministic")
	}
	if !strings.HasPrefix(digestA, "sha256:") {
		t.Fatalf("packed bundle digest = %q, want canonical sha256 identity", digestA)
	}
	if manifestA.CompiledPath != CompiledProjectFile || manifestB.CompiledPath != CompiledProjectFile {
		t.Fatalf("compiled paths = %q, %q, want %q", manifestA.CompiledPath, manifestB.CompiledPath, CompiledProjectFile)
	}
	if strings.Contains(string(first.Bytes()), "workspaceId") || strings.Contains(string(first.Bytes()), "environment") {
		t.Fatal("bundle retained serving/workspace selectors")
	}
	path := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(path, first.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	validation, err := ValidateArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if validation.Digest != digestA {
		t.Fatalf("validated bundle digest = %q, packed = %q", validation.Digest, digestA)
	}
	if validation.ProjectID != project.ProjectID().String() || validation.ProjectDigest != project.Digest() {
		t.Fatalf("validation identity = (%q, %q), want (%q, %q)", validation.ProjectID, validation.ProjectDigest, project.ProjectID(), project.Digest())
	}
	if !strings.HasPrefix(validation.Digest, "sha256:") {
		t.Fatalf("bundle digest = %q, want canonical sha256 identity", validation.Digest)
	}
	readerValidation, compiled, err := ValidateArtifactBytes(first.Bytes())
	if err != nil {
		t.Fatalf("ValidateArtifactBytes() error = %v", err)
	}
	if readerValidation.RootDir != "" {
		t.Fatalf("reader validation root = %q, want empty", readerValidation.RootDir)
	}
	if readerValidation.Digest != validation.Digest || readerValidation.ManifestJSON != validation.ManifestJSON || readerValidation.ProjectID != validation.ProjectID || readerValidation.ProjectDigest != validation.ProjectDigest || compiled.ProjectID.String() != validation.ProjectID {
		t.Fatalf("reader validation = %#v, compiled = %#v; path validation = %#v", readerValidation, compiled, validation)
	}
	readerValidation2, compiled2, err := ValidateArtifactReader(bytes.NewReader(first.Bytes()), int64(first.Len()))
	if err != nil {
		t.Fatalf("ValidateArtifactReader() error = %v", err)
	}
	if readerValidation2.Digest != readerValidation.Digest || compiled2.ProjectDigest != compiled.ProjectDigest {
		t.Fatal("reader and bytes validation differ")
	}
}

func TestCompiledProjectRoundTripPreservesRuntimeProjection(t *testing.T) {
	base := bundleProject(t)
	manifest := base.Manifest()
	header := true
	location := &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: "orders.csv", Format: "csv"},
		Format:                 "csv",
		Options:                &projectcontracts.CSVReaderOptions{Header: &header},
	}}
	source := manifest.Sources["source:orders"]
	source.Path = "orders.csv"
	source.LocationType = "path"
	source.PathLocation = location
	source.EffectivePathLocation = location
	manifest.Sources["source:orders"] = source
	project, err := projectartifact.NewProject(base.Graph(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, _, err := PackCompiledProject(project, bundlePlan(project), &output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	validation, err := ValidateArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(validation.RootDir)
	if got := validation.Manifest.Sources["source:orders"]; got.PathLocation == nil || got.EffectivePathLocation == nil {
		t.Fatalf("compiled manifest lost typed path runtime projection: %#v", got)
	}
	if got := validation.Manifest.Models["model:orders"].Execution.Source; got != "source:orders" {
		t.Fatalf("compiled manifest lost model execution projection: %q", got)
	}
	compiled, _, err := LoadCompiledProjectArtifact(validation.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Runtime.Sources["source:orders"].PathLocation == nil || compiled.Runtime.Models["model:orders"].Source != "source:orders" {
		t.Fatalf("compiled runtime projection is incomplete: %#v", compiled.Runtime)
	}
}

func TestCompiledProjectRejectsMissingRuntimeProjectionAndTargetState(t *testing.T) {
	project := bundleProject(t)
	var output bytes.Buffer
	if _, _, err := PackCompiledProject(project, bundlePlan(project), &output); err != nil {
		t.Fatal(err)
	}
	versionOnePath := filepath.Join(t.TempDir(), "version-one.tar.gz")
	if err := os.WriteFile(versionOnePath, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	mutateBundle(t, versionOnePath, func(compiled *CompiledProjectArtifact, _ *Manifest) {
		compiled.Version = 1
	})
	root := t.TempDir()
	if err := ExtractArtifact(versionOnePath, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCompiledProjectArtifact(root); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("version one error = %v", err)
	}

	missingRuntimePath := filepath.Join(t.TempDir(), "missing-runtime.tar.gz")
	if err := os.WriteFile(missingRuntimePath, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	mutateBundle(t, missingRuntimePath, func(compiled *CompiledProjectArtifact, _ *Manifest) {
		compiled.Runtime = projectartifact.RuntimeProjection{}
	})
	root = t.TempDir()
	if err := ExtractArtifact(missingRuntimePath, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCompiledProjectArtifact(root); err == nil || !strings.Contains(err.Error(), "runtime projection") {
		t.Fatalf("missing runtime projection error = %v", err)
	}

	targetStatePath := filepath.Join(t.TempDir(), "target-state.tar.gz")
	if err := os.WriteFile(targetStatePath, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	mutateBundle(t, targetStatePath, func(compiled *CompiledProjectArtifact, _ *Manifest) {
		connection := compiled.Manifest.Connections["connection:warehouse"]
		connection.Path = "/target/path"
		compiled.Manifest.Connections["connection:warehouse"] = connection
	})
	root = t.TempDir()
	if err := ExtractArtifact(targetStatePath, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCompiledProjectArtifact(root); err == nil || !strings.Contains(err.Error(), "target-owned state") {
		t.Fatalf("target state error = %v", err)
	}
}

func TestPackProjectPreservesAuthoredSourcesDeterministically(t *testing.T) {
	project := bundleProject(t)
	root := t.TempDir()
	projectPath := filepath.Join(root, ProjectFile)
	if err := os.WriteFile(projectPath, []byte("apiVersion: leapview.dev/v1\nkind: Project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "connections"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sources", "orders.yaml"), []byte("apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders}\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "connections", "warehouse.yaml"), []byte("apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\nspec: {type: managed}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "orders.yaml"), []byte("kind: Model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	manifestValue, _, err := PackProject(projectPath, PackProjectOptions{Project: project, Plan: bundlePlan(project)}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if manifestValue.CatalogPath != ProjectFile || len(manifestValue.Files) != 4 {
		t.Fatalf("manifest = %#v", manifestValue)
	}
	path := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	validation, err := ValidateArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(validation.RootDir, ProjectFile)); err != nil {
		t.Fatal(err)
	}
}

func TestPackProjectIncludesOnlyManifestAndGraphProvenanceFiles(t *testing.T) {
	project := bundleProject(t)
	root := t.TempDir()
	projectPath := filepath.Join(root, ProjectFile)
	files := map[string]string{
		ProjectFile:                  "apiVersion: leapview.dev/v1\nkind: Project\n",
		"connections/warehouse.yaml": "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\nspec: {type: managed}\n",
		"sources/orders.yaml":        "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders}\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n",
		"models/orders.yaml":         "kind: Model\n",
		"unrelated/other.yaml":       "must not be bundled\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	manifestValue, _, err := PackProject(projectPath, PackProjectOptions{Project: project, Plan: bundlePlan(project)}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifestValue.Files) != 4 {
		t.Fatalf("manifest files = %#v, want project + three provenance files", manifestValue.Files)
	}
	for _, file := range manifestValue.Files {
		if file.Path == "unrelated/other.yaml" {
			t.Fatal("bundle recursively included unrelated YAML")
		}
	}
}

func TestPackProjectRequiresExactSuppliedSourceSet(t *testing.T) {
	project := bundleProject(t)
	root := t.TempDir()
	projectPath := filepath.Join(root, ProjectFile)
	expected := map[string][]byte{
		ProjectFile:                  []byte("project\n"),
		"connections/warehouse.yaml": []byte("connection\n"),
		"sources/orders.yaml":        []byte("source\n"),
		"models/orders.yaml":         []byte("model\n"),
	}
	if _, _, err := PackProject(projectPath, PackProjectOptions{Project: project, Plan: bundlePlan(project), SourceFiles: expected}, &bytes.Buffer{}); err != nil {
		t.Fatalf("PackProject() exact source set error = %v", err)
	}
	for name, files := range map[string]map[string][]byte{
		"missing": {
			ProjectFile:           expected[ProjectFile],
			"sources/orders.yaml": expected["sources/orders.yaml"],
		},
		"extra": {
			ProjectFile:                  expected[ProjectFile],
			"connections/warehouse.yaml": expected["connections/warehouse.yaml"],
			"sources/orders.yaml":        expected["sources/orders.yaml"],
			"models/orders.yaml":         expected["models/orders.yaml"],
			"unrelated.yaml":             []byte("extra\n"),
		},
	} {
		if _, _, err := PackProject(projectPath, PackProjectOptions{Project: project, Plan: bundlePlan(project), SourceFiles: files}, &bytes.Buffer{}); err == nil {
			t.Fatalf("PackProject() %s source set error = nil", name)
		}
	}
}

func TestPackProjectRejectsProjectOutsideSourceRoot(t *testing.T) {
	project := bundleProject(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), ProjectFile)
	if _, _, err := PackProject(outside, PackProjectOptions{Project: project, Plan: bundlePlan(project), SourceRoot: root, SourceFiles: map[string][]byte{
		ProjectFile:           []byte("project\n"),
		"sources/orders.yaml": []byte("source\n"),
		"models/orders.yaml":  []byte("model\n"),
	}}, &bytes.Buffer{}); err == nil {
		t.Fatal("PackProject() outside source root error = nil")
	}
}

func TestPackCompiledProjectRequiresExactPlanResourceLists(t *testing.T) {
	project := bundleProject(t)
	plan := bundlePlan(project)
	plan.Models = nil
	if _, _, err := PackCompiledProject(project, plan, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "models") {
		t.Fatalf("PackCompiledProject() plan error = %v", err)
	}
}

func TestLoadCompiledProjectRejectsUnknownVersionAndIdentity(t *testing.T) {
	project := bundleProject(t)
	var output bytes.Buffer
	if _, _, err := PackCompiledProject(project, bundlePlan(project), &output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	mutateBundle(t, path, func(compiled *CompiledProjectArtifact, _ *Manifest) {
		compiled.Version = compiledProjectVersion + 1
	})
	root := t.TempDir()
	if err := ExtractArtifact(path, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCompiledProjectArtifact(root); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("version error = %v", err)
	}

	var output2 bytes.Buffer
	if _, _, err := PackCompiledProject(project, bundlePlan(project), &output2); err != nil {
		t.Fatal(err)
	}
	path2 := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(path2, output2.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	mutateBundle(t, path2, func(compiled *CompiledProjectArtifact, _ *Manifest) {
		compiled.ProjectID = "project:other"
	})
	root2 := t.TempDir()
	if err := ExtractArtifact(path2, root2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCompiledProjectArtifact(root2); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("identity error = %v", err)
	}
}

func TestLoadCompiledProjectRejectsTamperedManifestDigestEvenWhenRehashed(t *testing.T) {
	project := bundleProject(t)
	var output bytes.Buffer
	if _, _, err := PackCompiledProject(project, bundlePlan(project), &output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	mutateBundle(t, path, func(compiled *CompiledProjectArtifact, manifestValue *Manifest) {
		compiled.Manifest.Name = "tampered"
		compiled.ProjectDigest = "sha256:" + strings.Repeat("f", 64)
		manifestValue.ProjectDigest = compiled.ProjectDigest
	})
	root := t.TempDir()
	if err := ExtractArtifact(path, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCompiledProjectArtifact(root); err == nil || !strings.Contains(err.Error(), "reconstructed manifest digest") {
		t.Fatalf("tampered manifest error = %v", err)
	}
}

func mutateBundle(t *testing.T, path string, mutate func(*CompiledProjectArtifact, *Manifest)) {
	t.Helper()
	root := t.TempDir()
	if err := ExtractArtifact(path, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, CompiledProjectFile))
	if err != nil {
		t.Fatal(err)
	}
	var compiled CompiledProjectArtifact
	if err := json.Unmarshal(data, &compiled); err != nil {
		t.Fatal(err)
	}
	manifestValue, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&compiled, &manifestValue)
	data, err = json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, CompiledProjectFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestValue.CompiledSHA256 = digestBytes(data)
	manifestData, err := json.MarshalIndent(manifestValue, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeExtractedRoot(root, file); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractArtifactRejectsSymlinkEscape(t *testing.T) {
	artifactPath := testTar(t, map[string]string{"link/escape.txt": "owned"})
	dest := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ExtractArtifact(artifactPath, dest); err == nil {
		t.Fatal("ExtractArtifact() error = nil, want symlink escape rejection")
	}
}

func TestExtractArtifactRejectsDuplicateEntries(t *testing.T) {
	artifactPath := testTarEntries(t, [][2]string{{"manifest.json", "first"}, {"manifest.json", "second"}})
	if err := ExtractArtifact(artifactPath, t.TempDir()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("ExtractArtifact() duplicate error = %v", err)
	}
}

func TestValidateArtifactBytesRejectsTrailingAndUnsafeEntries(t *testing.T) {
	project := bundleProject(t)
	var output bytes.Buffer
	if _, _, err := PackCompiledProject(project, bundlePlan(project), &output); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"trailing compressed bytes": func(data []byte) []byte { return append(append([]byte(nil), data...), []byte("trailing")...) },
		"second gzip member": func(data []byte) []byte {
			var member bytes.Buffer
			writer := gzip.NewWriter(&member)
			if _, err := writer.Write([]byte("second")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			return append(append([]byte(nil), data...), member.Bytes()...)
		},
		"truncated gzip": func(data []byte) []byte { return data[:len(data)-1] },
		"trailing tar bytes": func(data []byte) []byte {
			reader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			uncompressed, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			_ = reader.Close()
			uncompressed = append(uncompressed, []byte("trailing")...)
			var encoded bytes.Buffer
			writer := gzip.NewWriter(&encoded)
			if _, err := writer.Write(uncompressed); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			return encoded.Bytes()
		},
	} {
		if _, _, err := ValidateArtifactBytes(mutate(output.Bytes())); err == nil {
			t.Fatalf("ValidateArtifactBytes() %s error = nil", name)
		}
	}
	for name, entries := range map[string][][2]string{
		"duplicate":           {{"manifest.json", "one"}, {"manifest.json", "two"}},
		"traversal":           {{"../manifest.json", "bad"}},
		"backslash traversal": {{`..\manifest.json`, "bad"}},
	} {
		path := testTarEntries(t, entries)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ValidateArtifactBytes(data); err == nil {
			t.Fatalf("ValidateArtifactBytes() %s error = nil", name)
		}
	}
}

func TestValidateArtifactReaderEnforcesExpectedSizeAndLimits(t *testing.T) {
	if _, _, err := ValidateArtifactReader(bytes.NewReader([]byte("not an archive")), 1); err == nil {
		t.Fatal("ValidateArtifactReader() malformed archive error = nil")
	}
	if _, _, err := ValidateArtifactReader(bytes.NewReader([]byte("not an archive")), 0); err == nil {
		t.Fatal("ValidateArtifactReader() expected size error = nil")
	}
	oversized := bytes.Repeat([]byte{'x'}, int(MaxBundleBytes)+1)
	if _, _, err := ValidateArtifactBytes(oversized); err == nil {
		t.Fatal("ValidateArtifactBytes() oversized archive error = nil")
	}
}

func testTar(t *testing.T, files map[string]string) string {
	t.Helper()
	entries := make([][2]string, 0, len(files))
	for name, body := range files {
		entries = append(entries, [2]string{name, body})
	}
	return testTarEntries(t, entries)
}

func testTarEntries(t *testing.T, entries [][2]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gz)
	for _, entry := range entries {
		name, body := entry[0], entry[1]
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
