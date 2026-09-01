package mapasset

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveReturnsImmutableSameOriginManifest(t *testing.T) {
	asset, err := Resolve("streets")
	if err != nil {
		t.Fatal(err)
	}
	if asset.ID != "leapview-streets" || asset.StyleURL[0] != '/' || asset.ArchiveURL[0] != '/' || len(asset.ArchiveDigest) != 71 {
		t.Fatalf("asset = %#v", asset)
	}
	styleDigest := strings.TrimPrefix(asset.StyleDigest, "sha256:")
	archiveDigest := strings.TrimPrefix(asset.ArchiveDigest, "sha256:")
	if !strings.Contains(asset.StyleURL, "/styles/"+styleDigest+"/") {
		t.Fatalf("style URL is not content addressed: %q", asset.StyleURL)
	}
	if !strings.Contains(asset.ArchiveURL, "/archives/"+archiveDigest+"/") {
		t.Fatalf("archive URL is not content addressed: %q", asset.ArchiveURL)
	}
	if !strings.Contains(asset.GlyphsURL, "/assets/"+BasemapAssetsRevision+"/") || !strings.Contains(asset.SpriteURL, "/assets/"+BasemapAssetsRevision+"/") {
		t.Fatalf("supporting asset URLs are not revision addressed: %#v", asset)
	}
	if _, err := Resolve("remote"); err == nil {
		t.Fatal("unknown map style asset accepted")
	}
}

func TestStyleDigestMatchesAuthoredAsset(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "static", "map-assets", "leapview-streets", "style.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(contents)); got != StyleSHA256 {
		t.Fatalf("authored style digest = %s, want %s", got, StyleSHA256)
	}
}

func TestStreetsBasemapIncludesRegionalBusinessDetail(t *testing.T) {
	asset, err := Resolve("streets")
	if err != nil {
		t.Fatal(err)
	}
	if asset.MinimumZoom != 0 || asset.MaximumZoom < 10 {
		t.Fatalf("streets zoom range = %v..%v, want global context through regional zoom 10", asset.MinimumZoom, asset.MaximumZoom)
	}
	if len(asset.Bounds) != 4 || asset.Bounds[0] > -82 || asset.Bounds[1] > -56 || asset.Bounds[2] < -30 || asset.Bounds[3] < 14 {
		t.Fatalf("streets bounds = %v, want complete South America regional coverage", asset.Bounds)
	}
}

func TestVerifyInstalledFailsClosedForMissingOrChangedAssets(t *testing.T) {
	root := t.TempDir()
	files := ExpectedFiles()
	if len(files) < 3 {
		t.Fatalf("expected complete basemap package, got %d files", len(files))
	}
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("wrong"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := VerifyInstalled(root); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("VerifyInstalled() error = %v, want digest mismatch", err)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(files[0].Path))); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstalled(root); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("VerifyInstalled() error = %v, want missing asset", err)
	}
}

func TestVerifierCachesUnchangedFilesAndDetectsRuntimeCorruption(t *testing.T) {
	root := t.TempDir()
	contents := []byte("verified map package")
	digest := fmt.Sprintf("%x", sha256.Sum256(contents))
	file := File{Path: "leapview-streets/test/asset.bin", Digest: digest}
	name := filepath.Join(root, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	verifier := newVerifier(root, []File{file})
	if err := verifier.Verify(context.Background()); err != nil {
		t.Fatalf("initial Verify() error = %v", err)
	}
	if got := verifier.hashedFiles(); got != 1 {
		t.Fatalf("initial hashed files = %d, want 1", got)
	}
	if err := verifier.Verify(context.Background()); err != nil {
		t.Fatalf("cached Verify() error = %v", err)
	}
	if got := verifier.hashedFiles(); got != 1 {
		t.Fatalf("unchanged verification rehashed %d files, want 1 total", got)
	}

	corrupt := []byte("corrupted map asset!")
	if len(corrupt) != len(contents) {
		t.Fatalf("test corruption length = %d, want %d", len(corrupt), len(contents))
	}
	if err := os.WriteFile(name, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(name, future, future); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("corrupt Verify() error = %v, want digest mismatch", err)
	}

	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing Verify() error = %v, want missing asset", err)
	}
}

func TestContentAddressedURLPathRejectsLegacyAndForgedPaths(t *testing.T) {
	asset, err := Resolve("streets")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{asset.StyleURL, asset.ArchiveURL, strings.ReplaceAll(asset.GlyphsURL, "{fontstack}", "Noto%20Sans%20Regular"), asset.SpriteURL + ".png"} {
		path = strings.ReplaceAll(path, "{range}", "0-255")
		if !IsContentAddressedURLPath(path) {
			t.Errorf("trusted path rejected: %q", path)
		}
	}
	for _, path := range []string{"/map-assets/leapview-streets/style.json", "/map-assets/leapview-streets/basemap.pmtiles", "/map-assets/other/" + strings.Repeat("a", 64) + "/style.json", "/map-assets/leapview-streets/../../secret"} {
		if IsContentAddressedURLPath(path) {
			t.Errorf("untrusted path accepted: %q", path)
		}
	}
}
