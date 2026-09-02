package devloop

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/stretchr/testify/require"
)

func BenchmarkFilesystemBuilderCoherentSnapshot(b *testing.B) {
	for _, edit := range []struct {
		name  string
		files int
	}{
		{name: "no_edit", files: 0},
		{name: "single_dashboard_edit", files: 1},
		{name: "multi_dashboard_edit", files: 3},
	} {
		b.Run(edit.name, func(b *testing.B) {
			b.StopTimer()
			projectPath, editable := copyBenchmarkProject(b)
			if edit.files > len(editable) {
				b.Fatalf("benchmark fixture has %d editable dashboards, want %d", len(editable), edit.files)
			}
			builder := FilesystemBuilder{ProjectPath: projectPath}
			baseline, err := builder.Build(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ReportMetric(float64(len(baseline.Artifacts)), "resources")
			durations := make([]time.Duration, 0, b.N)
			previous := baseline
			observed := baseline
			for iteration := 0; iteration < b.N; iteration++ {
				b.StopTimer()
				for index := 0; index < edit.files; index++ {
					editable[index].apply(b, iteration%2 == 0)
				}
				b.StartTimer()
				started := time.Now()
				snapshot, buildErr := builder.Build(context.Background())
				durations = append(durations, time.Since(started))
				b.StopTimer()
				if buildErr != nil {
					b.Fatal(buildErr)
				}
				if edit.files == 0 && snapshot.Digest != previous.Digest {
					b.Fatalf("no-op snapshot digest changed: %s -> %s", previous.Digest, snapshot.Digest)
				}
				if edit.files > 0 && snapshot.Digest == previous.Digest {
					b.Fatal("semantic dashboard edit did not change coherent snapshot")
				}
				if edit.files > 0 && iteration == 0 {
					observed = snapshot
				}
				previous = snapshot
				b.StartTimer()
			}
			b.StopTimer()
			b.ReportMetric(float64(changedSnapshotArtifacts(baseline, observed)), "affected-artifacts")
			reportDevloopLatencyPercentiles(b, durations)
		})
	}
}

type devloopBenchmarkEdit struct {
	path      string
	baseline  []byte
	alternate []byte
}

func (edit devloopBenchmarkEdit) apply(tb testing.TB, alternate bool) {
	tb.Helper()
	content := edit.baseline
	if alternate {
		content = edit.alternate
	}
	if err := os.WriteFile(edit.path, content, 0o600); err != nil {
		tb.Fatal(err)
	}
}

func copyBenchmarkProject(b *testing.B) (string, []devloopBenchmarkEdit) {
	b.Helper()
	original, err := filepath.Abs(filepath.Join("..", "..", "..", "dashboards"))
	if err != nil {
		b.Fatal(err)
	}
	paths, err := projectcompiler.SourceRootFiles(original)
	if err != nil {
		b.Fatal(err)
	}
	originalRoot := original
	targetRoot := b.TempDir()
	editable := make([]devloopBenchmarkEdit, 0, 3)
	for _, source := range paths {
		relative, err := filepath.Rel(originalRoot, source)
		if err != nil {
			b.Fatal(err)
		}
		target := filepath.Join(targetRoot, relative)
		body, err := os.ReadFile(source)
		if err != nil {
			b.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(target, body, 0o600); err != nil {
			b.Fatal(err)
		}
		if strings.Contains(filepath.ToSlash(relative), "/dashboards/") {
			alternate, ok := benchmarkDashboardTitleVariant(body)
			if ok {
				editable = append(editable, devloopBenchmarkEdit{path: target, baseline: body, alternate: alternate})
			}
		}
	}
	return targetRoot, editable
}

func benchmarkDashboardTitleVariant(body []byte) ([]byte, bool) {
	lines := strings.Split(string(body), "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "  title: ") {
			lines[index] = line + " benchmark"
			return []byte(strings.Join(lines, "\n")), true
		}
	}
	return nil, false
}

func changedSnapshotArtifacts(before, after Snapshot) int {
	beforeDigests := make(map[string]string, len(before.Artifacts))
	for _, artifact := range before.Artifacts {
		beforeDigests[artifact.Path] = artifact.Digest
	}
	changed := 0
	seen := make(map[string]struct{}, len(before.Artifacts)+len(after.Artifacts))
	for _, artifact := range after.Artifacts {
		seen[artifact.Path] = struct{}{}
		if digest, ok := beforeDigests[artifact.Path]; !ok || digest != artifact.Digest {
			changed++
		}
	}
	for _, artifact := range before.Artifacts {
		if _, ok := seen[artifact.Path]; !ok {
			changed++
		}
	}
	return changed
}

func reportDevloopLatencyPercentiles(b *testing.B, durations []time.Duration) {
	if len(durations) == 0 {
		return
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	b.ReportMetric(float64(durations[(len(durations)*50-1)/100])/float64(time.Millisecond), "p50-ms")
	b.ReportMetric(float64(durations[(len(durations)*95-1)/100])/float64(time.Millisecond), "p95-ms")
}

func TestFilesystemBuilderProducesDeterministicCanonicalSourceRootArtifacts(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards")
	builder := FilesystemBuilder{ProjectPath: projectPath}

	first, err := builder.Build(t.Context())
	require.NoError(t, err)
	second, err := builder.Build(t.Context())
	require.NoError(t, err)
	if first.ProjectID != "project:source-root" ||
		first.ProjectFile != sourceRootEntrypoint ||
		first.Digest != second.Digest {
		t.Fatalf(
			"candidate identities = (%q, %q) and (%q, %q)",
			first.ProjectID, first.Digest, second.ProjectID, second.Digest,
		)
	}
	if len(first.Artifacts) < 2 {
		t.Fatalf("content artifacts = %d, want reachable project sources", len(first.Artifacts))
	}
	for index, artifact := range first.Artifacts {
		if err := digest.ValidateSHA256Identity(artifact.Digest); err != nil {
			t.Fatalf("artifact %q digest: %v", artifact.Path, err)
		}
		if len(artifact.Content) == 0 ||
			artifact.Path != second.Artifacts[index].Path ||
			artifact.Digest != second.Artifacts[index].Digest {
			t.Fatalf("non-deterministic artifact at %d: %#v / %#v", index, artifact, second.Artifacts[index])
		}
	}
}

func TestFilesystemBuilderProducesDeterministicSourceRootArtifacts(t *testing.T) {
	root := t.TempDir()
	connectionPath := filepath.Join(root, "connections", "warehouse.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(connectionPath), 0o755))
	require.NoError(t, os.WriteFile(connectionPath, []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`), 0o600))
	builder := FilesystemBuilder{ProjectPath: root}

	first, err := builder.Build(t.Context())
	require.NoError(t, err)
	second, err := builder.Build(t.Context())
	require.NoError(t, err)
	if first.ProjectID != "project:source-root" || first.ProjectFile != sourceRootEntrypoint || first.Digest != second.Digest {
		t.Fatalf("source-root identities = (%q, %q, %q) / (%q, %q, %q)", first.ProjectID, first.ProjectFile, first.Digest, second.ProjectID, second.ProjectFile, second.Digest)
	}
	if len(first.Artifacts) != 2 {
		t.Fatalf("source-root artifacts = %#v, want authored resource and protocol marker", first.Artifacts)
	}
	markerFound := false
	for _, artifact := range first.Artifacts {
		if artifact.Path == sourceRootEntrypoint {
			markerFound = string(artifact.Content) == sourceRootMarker
		}
	}
	if !markerFound {
		t.Fatalf("source-root marker missing or invalid: %#v", first.Artifacts)
	}
}

func TestFilesystemBuilderRejectsLegacyProjectManifest(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "leapview.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte("apiVersion: leapview.dev/v1\nkind: Project\n"), 0o600))
	_, err := (FilesystemBuilder{ProjectPath: manifest}).Build(t.Context())
	require.ErrorContains(t, err, "Project authoring was removed")
}

func TestCompileRetainedAuthoringInputRequiresPinnedSourceRootMarker(t *testing.T) {
	root := t.TempDir()
	connectionPath := filepath.Join(root, "connections", "warehouse.yaml")
	markerPath := filepath.Join(root, filepath.FromSlash(sourceRootEntrypoint))
	require.NoError(t, os.MkdirAll(filepath.Dir(connectionPath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(markerPath), 0o755))
	require.NoError(t, os.WriteFile(connectionPath, []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`), 0o600))
	require.NoError(t, os.WriteFile(markerPath, []byte("leapview.source-root/v999\n"), 0o600))
	if _, err := compileRetainedAuthoringInput(root, sourceRootEntrypoint); err == nil || !strings.Contains(err.Error(), "unsupported source-root marker") {
		t.Fatalf("invalid source-root marker error = %v", err)
	}
	require.NoError(t, os.WriteFile(markerPath, []byte(sourceRootMarker), 0o600))
	compiled, err := compileRetainedAuthoringInput(root, sourceRootEntrypoint)
	require.NoError(t, err)
	if compiled.ProjectID() != "project:source-root" {
		t.Fatalf("compiled source-root id = %q", compiled.ProjectID())
	}
}

func TestCandidateSetDigestIncludesProjectEntrypoint(t *testing.T) {
	artifacts := []Artifact{
		contentArtifact("leapview.yaml", []byte("one")),
		contentArtifact("alternate.yaml", []byte("two")),
	}
	first := candidateSetDigest("project", "leapview.yaml", artifacts)
	second := candidateSetDigest("project", "alternate.yaml", artifacts)
	if first == second {
		t.Fatalf("candidate set digest ignored project entrypoint: %q", first)
	}
}

func TestNormalizeSnapshotRejectsContentAndCandidateSetDigestMismatch(t *testing.T) {
	valid := testSnapshot("valid")

	badContent := cloneSnapshot(valid)
	badContent.Artifacts[0].Content = []byte("tampered")
	if _, err := normalizeSnapshot(badContent); err == nil {
		t.Fatal("normalize snapshot accepted content that does not match its digest")
	}

	badSet := cloneSnapshot(valid)
	badSet.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := normalizeSnapshot(badSet); err == nil {
		t.Fatal("normalize snapshot accepted a candidate-set digest that does not match its artifacts")
	}
}

func TestNormalizeSnapshotRejectsUnsafeArtifactPaths(t *testing.T) {
	for _, path := range []string{"../secrets.env", "/etc/passwd", `C:\secrets.env`, "models/../leapview.yaml"} {
		snapshot := testSnapshot("valid")
		snapshot.Artifacts[0].Path = path
		snapshot.Digest = candidateSetDigest(snapshot.ProjectID, snapshot.ProjectFile, snapshot.Artifacts)
		if _, err := normalizeSnapshot(snapshot); err == nil {
			t.Errorf("normalize snapshot accepted unsafe artifact path %q", path)
		}
	}
}

func TestCandidateSetDigestIsIndependentOfArtifactOrder(t *testing.T) {
	artifacts := []Artifact{
		{Path: "sales.yaml", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Path: "operations.yaml", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	first := candidateSetDigest("project", "leapview.yaml", artifacts)
	artifacts[0], artifacts[1] = artifacts[1], artifacts[0]
	if second := candidateSetDigest("project", "leapview.yaml", artifacts); second != first {
		t.Fatalf("candidate set digests differ: %q / %q", first, second)
	}
}
