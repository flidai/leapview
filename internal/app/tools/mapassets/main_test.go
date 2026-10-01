package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

func TestPMTilesTransientFailureClassification(t *testing.T) {
	for _, test := range []struct {
		message string
		retry   bool
	}{
		{"Failed to extract, HTTP error: 429", true},
		{"Failed to extract, HTTP error: 500", true},
		{"Failed to extract, HTTP error: 502", true},
		{"Failed to extract, HTTP error: 503", true},
		{"Failed to extract, HTTP error: 504", true},
		{"Failed to extract, unexpected EOF", true},
		{"Failed to extract, EOF", true},
		{"Failed to extract, read tcp: connection reset by peer", true},
		{"Failed to extract, read tcp: i/o timeout", true},
		{"Failed to extract, EOF while reading", false},
		{"Failed to extract, HTTP error: 401", false},
		{"Failed to extract, HTTP error: 403", false},
		{"Failed to extract, HTTP error: 403 unexpected EOF", false},
		{"Failed to extract, HTTP error: 404", false},
		{"Failed to extract, HTTP error: 5000", false},
		{"Failed to extract, write output: no space left on device", false},
		{"Failed to merge, unexpected EOF", false},
		{"unexpected archive digest", false},
		{"HTTP 429 returned by an unrelated message", false},
		{"unexpected EOF in generated source", false},
	} {
		t.Run(strings.ReplaceAll(test.message, " ", "_"), func(t *testing.T) {
			if got := isPMTilesTransientFailure(test.message); got != test.retry {
				t.Fatalf("retryable = %t, want %t for %q", got, test.retry, test.message)
			}
		})
	}
}

func TestPMTilesRetriesRequireWellFormedExtractArguments(t *testing.T) {
	for _, arguments := range [][]string{
		{"extract", planetURL},
		{"merge", "input.pmtiles", filepath.Join(t.TempDir(), "merged.pmtiles")},
	} {
		t.Run(strings.Join(arguments, "/"), func(t *testing.T) {
			attempts := 0
			failure := errors.New("transient extraction failure")
			err := runPMTilesWithRetry(context.Background(), arguments, func(context.Context, ...string) (string, error) {
				attempts++
				return "Failed to extract, unexpected EOF", failure
			})
			if !errors.Is(err, failure) || attempts != 1 {
				t.Fatalf("attempts=%d err=%v, want one attempt and original error", attempts, err)
			}
		})
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

func TestPMTilesRetriesDiscardPartialArchivesAndRecover(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "regional.pmtiles")
	arguments := []string{"extract", planetURL, target}
	attempts := 0
	err := runPMTilesWithRetry(context.Background(), arguments, func(_ context.Context, got ...string) (string, error) {
		attempts++
		if !slices.Equal(got, arguments) {
			t.Fatalf("retry changed pinned extraction arguments: %q", got)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("attempt %d retained partial output: %v", attempts, err)
		}
		if attempts < 3 {
			if err := os.WriteFile(target, []byte("partial archive"), 0o600); err != nil {
				t.Fatal(err)
			}
			message := "Failed to extract, unexpected EOF"
			if attempts == 2 {
				message = "Failed to extract, HTTP error: 500"
			}
			return message, errors.New("extraction failed")
		}
		return "", os.WriteFile(target, []byte("complete archive"), 0o600)
	})
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v, want recovery on third attempt", attempts, err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("complete archive")))
	if err := verifyFile(target, want); err != nil {
		t.Fatal(err)
	}
}

func TestPMTilesRetriesAreBounded(t *testing.T) {
	t.Parallel()
	attempts := 0
	failure := errors.New("source unavailable")
	err := runPMTilesWithRetry(context.Background(), []string{"extract", planetURL, filepath.Join(t.TempDir(), "regional.pmtiles")}, func(context.Context, ...string) (string, error) {
		attempts++
		return "Failed to extract, HTTP error: 503", failure
	})
	if !errors.Is(err, failure) || attempts != 3 {
		t.Fatalf("attempts=%d err=%v, want three attempts and original failure", attempts, err)
	}
}

func TestPMTilesPermanentFailuresAndCancellationStopRetries(t *testing.T) {
	for _, cancelDuringRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelDuringRun), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attempts := 0
			failure := errors.New("source rejected request")
			err := runPMTilesWithRetry(ctx, []string{"extract", planetURL, filepath.Join(t.TempDir(), "regional.pmtiles")}, func(context.Context, ...string) (string, error) {
				attempts++
				if cancelDuringRun {
					cancel()
					return "Failed to extract, unexpected EOF", failure
				}
				return "Failed to extract, HTTP error: 403", failure
			})
			want := failure
			if cancelDuringRun {
				want = context.Canceled
			}
			if !errors.Is(err, want) || attempts != 1 {
				t.Fatalf("attempts=%d err=%v, want one attempt and %v", attempts, err, want)
			}
		})
	}
}

func TestPMTilesCommandCapturesExtractionDiagnostics(t *testing.T) {
	if os.Getenv("LEAPVIEW_PMTILES_TEST_HELPER") == "1" {
		fmt.Fprintln(os.Stdout, "Failed to extract, unexpected EOF")
		fmt.Fprintln(os.Stderr, "exit status 1")
		os.Exit(1)
	}
	command := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestPMTilesCommandCapturesExtractionDiagnostics$")
	command.Env = append(os.Environ(), "LEAPVIEW_PMTILES_TEST_HELPER=1")
	output, err := runPMTilesCommand(command)
	if err == nil || !isPMTilesTransientFailure(output) {
		t.Fatalf("PMTiles stdout failure was not captured for retry: output=%q err=%v", output, err)
	}
	if !strings.Contains(output, "exit status 1") {
		t.Fatalf("launcher stderr missing from captured diagnostics: %q", output)
	}
}
