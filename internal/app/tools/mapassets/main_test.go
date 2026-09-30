package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPMTilesCommandPinsArchiveEncodingToolchain(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "go1.27.1")
	t.Setenv("GODEBUG", "asynctimerchan=1")
	t.Setenv("LEAPVIEW_MAP_TEST_ENV", "preserved")
	command := pmtilesCommand(context.Background(), "extract", "input", "output", "--maxzoom=6")
	want := []string{"go", "run", "github.com/protomaps/go-pmtiles@v1.31.1", "extract", "input", "output", "--maxzoom=6"}
	if !slices.Equal(command.Args, want) {
		t.Fatalf("command arguments = %q, want %q", command.Args, want)
	}
	environment := make(map[string]string)
	for _, entry := range command.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		environment[key] = value
	}
	if environment["GOTOOLCHAIN"] != "go1.26.8" {
		t.Fatalf("archive encoder toolchain = %q, want go1.26.8", environment["GOTOOLCHAIN"])
	}
	if environment["GODEBUG"] != "asynctimerchan=1,http2client=0" {
		t.Fatalf("archive transport settings = %q, want inherited settings and HTTP/1.1", environment["GODEBUG"])
	}
	if environment["LEAPVIEW_MAP_TEST_ENV"] != "preserved" {
		t.Fatal("archive command discarded the inherited environment")
	}
}

func TestPMTilesTransientExtractionClassificationIsExact(t *testing.T) {
	for _, failure := range []string{"HTTP error: 429", "HTTP error: 500", "HTTP error: 502", "HTTP error: 503", "HTTP error: 504", "unexpected EOF", "EOF"} {
		if !isPMTilesTransientExtraction("2026/09/30 14:24:48 main.go:185: Failed to extract, " + failure + "\nexit status 1\n") {
			t.Errorf("transient extraction failure %q was not retryable", failure)
		}
	}
	for _, message := range []string{
		"Failed to extract, HTTP error: 404",
		"Failed to extract, HTTP error: 401",
		"Failed to extract, HTTP error: 5000",
		"Failed to extract, write output: no space left on device",
		"Failed to merge, unexpected EOF",
		"unexpected archive digest",
		"HTTP 429 returned by an unrelated message",
		"unexpected EOF",
	} {
		if isPMTilesTransientExtraction(message) {
			t.Errorf("classified permanent or unrelated failure %q as retryable", message)
		}
	}
}

func TestRunPMTilesRetriesStdoutFailureAndRemovesPartialOutput(t *testing.T) {
	for _, mode := range []string{"recover", "recover-eof"} {
		t.Run(mode, func(t *testing.T) {
			output, attempts := fakePMTiles(t, mode)
			if err := runPMTiles(context.Background(), "extract", planetURL, output); err != nil {
				t.Fatal(err)
			}
			if got := readPMTilesAttempts(t, attempts); got != "xx" {
				t.Fatalf("attempts = %q, want two invocations", got)
			}
			contents, err := os.ReadFile(output)
			if err != nil || string(contents) != "complete" {
				t.Fatalf("extracted output = %q, error %v", contents, err)
			}
		})
	}
}

func TestRunPMTilesBoundsRepeatedTransportFailures(t *testing.T) {
	output, attempts := fakePMTiles(t, "exhaust")
	if err := runPMTiles(context.Background(), "extract", planetURL, output); err == nil {
		t.Fatal("repeated transport failures accepted")
	}
	if got := readPMTilesAttempts(t, attempts); got != "xxx" {
		t.Fatalf("attempts = %q, want three invocations", got)
	}
}

func TestRunPMTilesDoesNotRetryPermanentFailures(t *testing.T) {
	output, attempts := fakePMTiles(t, "permanent")
	if err := runPMTiles(context.Background(), "extract", planetURL, output); err == nil {
		t.Fatal("permanent extraction failure accepted")
	}
	if got := readPMTilesAttempts(t, attempts); got != "x" {
		t.Fatalf("attempts = %q, want one invocation", got)
	}
}

func TestRunPMTilesCancellationInterruptsBackoff(t *testing.T) {
	output, attempts := fakePMTiles(t, "exhaust")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runPMTiles(ctx, "extract", planetURL, output); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v, want context deadline", err)
	}
	if got := readPMTilesAttempts(t, attempts); got != "x" {
		t.Fatalf("attempts = %q, want one invocation before cancellation", got)
	}
}

func fakePMTiles(t *testing.T, mode string) (string, string) {
	t.Helper()
	root := t.TempDir()
	attempts := filepath.Join(root, "attempts")
	// The pinned CLI logs its extraction failure on stdout, while go run adds
	// its exit status on stderr. A retry must discard the interrupted archive.
	script := `#!/bin/sh
output="$5"
if [ -e "$output" ]; then
  echo 'stale partial archive was not removed' >&2
  exit 2
fi
printf x >> "$PMTILES_TEST_ATTEMPTS"
if [ "${PMTILES_TEST_MODE#recover}" != "$PMTILES_TEST_MODE" ] && [ "$(cat "$PMTILES_TEST_ATTEMPTS")" = xx ]; then
  printf complete > "$output"
  exit 0
fi
printf partial > "$output"
if [ "$PMTILES_TEST_MODE" = permanent ]; then
  echo '2026/09/30 14:24:48 main.go:185: Failed to extract, HTTP error: 404'
elif [ "$PMTILES_TEST_MODE" = recover-eof ]; then
  echo '2026/09/30 14:24:48 main.go:185: Failed to extract, unexpected EOF'
else
  echo '2026/09/30 14:24:48 main.go:185: Failed to extract, HTTP error: 500'
fi
echo 'exit status 1' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(root, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PMTILES_TEST_ATTEMPTS", attempts)
	t.Setenv("PMTILES_TEST_MODE", mode)
	return filepath.Join(root, "output.pmtiles"), attempts
}

func readPMTilesAttempts(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestVerifyFileFailsClosedOnDigestMismatch(t *testing.T) {
	name := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(name, []byte("map"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("map")))
	if err := verifyFile(name, digest); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(name, archiveDigest); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}

func TestGlyphPackageCoversBasemapScriptsObservedAtRuntime(t *testing.T) {
	want := []string{"0-255", "256-511", "512-767", "768-1023", "1024-1279", "1280-1535", "1536-1791", "3840-4095", "4096-4351", "11520-11775", "65024-65279"}
	present := make(map[string]bool, len(glyphRanges))
	for _, value := range glyphRanges {
		present[value] = true
	}
	for _, value := range want {
		if !present[value] {
			t.Errorf("glyph package missing Unicode range %s", value)
		}
	}
}

func TestInstallTargetsContentAddressedPackagePaths(t *testing.T) {
	root := t.TempDir()
	archive, err := assetTarget(root, "/map-assets/leapview-streets/archives/"+archiveDigest+"/basemap.pmtiles")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "leapview-streets", "archives", archiveDigest, "basemap.pmtiles")
	if archive != want {
		t.Fatalf("asset target = %q, want %q", archive, want)
	}
	if _, err := assetTarget(root, "/map-assets/leapview-streets/../../secret"); err == nil {
		t.Fatal("assetTarget accepted traversal")
	}
}

func TestInstallSeedArchiveRejectsUnverifiedInput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "basemap.pmtiles")
	if err := os.WriteFile(source, []byte("not the pinned archive"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installSeedArchive(source, root); err == nil {
		t.Fatal("installSeedArchive() accepted an unverified archive")
	}
	asset, err := assetTarget(root, "/map-assets/leapview-streets/archives/"+archiveDigest+"/basemap.pmtiles")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(asset); !os.IsNotExist(err) {
		t.Fatalf("seed target exists after failed verification: %v", err)
	}
}

func TestRegionalExtractionProfileExtendsTheGlobalArchive(t *testing.T) {
	if regionalBounds != "-82,-56,-30,14" || regionalMinimumZoom != "7" || regionalMaximumZoom != "10" {
		t.Fatalf("regional profile = bounds %q zoom %s..%s", regionalBounds, regionalMinimumZoom, regionalMaximumZoom)
	}
	if archiveDownloadThreads != "2" {
		t.Fatalf("archive download threads = %q, want conservative range concurrency", archiveDownloadThreads)
	}
	if archiveDigest == globalArchiveDigest {
		t.Fatal("regional detail was not merged into a distinct immutable archive")
	}
}
