package bundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/servingstate"
)

func TestServingArtifactLoaderLoadsNativeLocatorFromMemoryStore(t *testing.T) {
	store, artifact, want := nativeServingArtifactFixture(t)
	loader := ServingArtifactLoader{Objects: store}

	got, err := loader.LoadCompiled(context.Background(), artifact, t.TempDir())
	if err != nil {
		t.Fatalf("LoadCompiled() error = %v", err)
	}
	if got.ProjectID != want.ProjectID {
		t.Fatalf("compiled project id = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.ProjectDigest != want.ProjectDigest {
		t.Fatalf("compiled project digest = %q, want %q", got.ProjectDigest, want.ProjectDigest)
	}
}

func TestServingArtifactLoaderAcceptsReorderedManifestJSON(t *testing.T) {
	store, artifact, want := nativeServingArtifactFixture(t)
	loader := ServingArtifactLoader{Objects: store}

	var manifest map[string]json.RawMessage
	if err := json.Unmarshal([]byte(artifact.ManifestJSON), &manifest); err != nil {
		t.Fatalf("unmarshal manifest JSON: %v", err)
	}
	reordered, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal reordered manifest JSON: %v", err)
	}
	if string(reordered) == artifact.ManifestJSON {
		t.Fatal("reordered manifest JSON unexpectedly retained the original key order")
	}
	artifact.ManifestJSON = string(reordered)

	got, err := loader.LoadCompiled(context.Background(), artifact, t.TempDir())
	if err != nil {
		t.Fatalf("LoadCompiled() error = %v, want success for semantically equivalent manifest", err)
	}
	if got.ProjectID != want.ProjectID || got.ProjectDigest != want.ProjectDigest {
		t.Fatalf("compiled project identity = (%q, %q), want (%q, %q)", got.ProjectID, got.ProjectDigest, want.ProjectID, want.ProjectDigest)
	}
}

func TestServingArtifactLoaderRejectsSemanticallyTamperedManifestJSON(t *testing.T) {
	store, artifact, _ := nativeServingArtifactFixture(t)
	loader := ServingArtifactLoader{Objects: store}

	var manifest map[string]json.RawMessage
	if err := json.Unmarshal([]byte(artifact.ManifestJSON), &manifest); err != nil {
		t.Fatalf("unmarshal manifest JSON: %v", err)
	}
	manifest["projectId"] = json.RawMessage(`"tampered-project"`)
	tampered, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal tampered manifest JSON: %v", err)
	}
	artifact.ManifestJSON = string(tampered)

	_, err = loader.LoadCompiled(context.Background(), artifact, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "native serving artifact content differs from durable serving state") {
		t.Fatalf("LoadCompiled() error = %v, want semantically tampered manifest mismatch", err)
	}
}

func TestServingArtifactLoaderRequiresNativeReaderWhenPathEmpty(t *testing.T) {
	_, artifact, _ := nativeServingArtifactFixture(t)

	_, err := (ServingArtifactLoader{}).LoadCompiled(context.Background(), artifact, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "native serving artifact reader is unavailable") {
		t.Fatalf("LoadCompiled() error = %v, want missing-reader failure", err)
	}
}

func TestServingArtifactLoaderRejectsDurableObjectEvidenceMismatch(t *testing.T) {
	store, artifact, _ := nativeServingArtifactFixture(t)
	artifact.MetadataDigest = "sha256:" + strings.Repeat("1", 64)

	_, err := (ServingArtifactLoader{Objects: store}).LoadCompiled(context.Background(), artifact, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "object evidence differs from durable serving state") {
		t.Fatalf("LoadCompiled() error = %v, want durable object-evidence mismatch", err)
	}
}

func TestRefreshArtifactLoaderLoadsNativeLocatorFromMemoryStore(t *testing.T) {
	store, artifact, want := nativeServingArtifactFixture(t)
	loader := RefreshArtifactLoader{Serving: ServingArtifactLoader{Objects: store}}

	loaded, err := loader.Load(context.Background(), artifact)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Definition == nil {
		t.Fatal("Load() returned nil refresh definition")
	}
	if loaded.Graph.Digest() != want.Graph.Digest() {
		t.Fatalf("loaded graph digest = %q, want %q", loaded.Graph.Digest(), want.Graph.Digest())
	}
}

func nativeServingArtifactFixture(t *testing.T) (*platformobjectstore.MemoryStore, servingstate.Artifact, CompiledProjectArtifact) {
	t.Helper()
	project := bundleProject(t)
	var content bytes.Buffer
	manifest, digest, err := PackCompiledProject(project, bundlePlan(project), &content)
	if err != nil {
		t.Fatal(err)
	}
	_, compiled, err := ValidateArtifactBytes(content.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	const storageDomain = "runtime-test"
	emptyDigest := sha256.Sum256(nil)
	metadataDigest := "sha256:" + hex.EncodeToString(emptyDigest[:])
	// The digest of an empty canonical metadata envelope matches the native
	// object-store metadata convention.
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: storageDomain})
	if err != nil {
		t.Fatal(err)
	}
	key := "serving-artifacts/" + strings.TrimPrefix(digest, "sha256:") + ".tar.gz"
	info, err := store.PutImmutable(context.Background(), key, bytes.NewReader(content.Bytes()), platformobjectstore.ObjectMetadata{
		StorageSecurityDomain: storageDomain,
		Digest:                digest,
		SizeBytes:             int64(content.Len()),
		ContentType:           servingstate.ArtifactBundleContentType,
		MetadataDigest:        metadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := servingstate.Artifact{
		Digest:                digest,
		Format:                servingstate.ArtifactBundleFormat,
		Locator:               key,
		StorageSecurityDomain: storageDomain,
		ContentType:           servingstate.ArtifactBundleContentType,
		MetadataDigest:        metadataDigest,
		ManifestJSON:          string(manifestJSON),
		SizeBytes:             info.SizeBytes,
	}
	return store, artifact, compiled
}
