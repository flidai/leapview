package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/flidai/leapview/internal/deployment/extensionsupply"
)

func TestPrepareInstallsOnceIntoExactRuntimeIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "extensions")
	installCalls := 0
	install := func(_ context.Context, receivedRoot, platform, name string) error {
		installCalls++
		path := filepath.Join(receivedRoot, "v1.5.4", platform, name+".duckdb_extension")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("signed-fixture"), 0o600)
	}
	first, err := prepare(context.Background(), root, "v1.5.4", "linux_amd64_gcc4", "ducklake", install)
	if err != nil {
		t.Fatalf("prepare fixture: %v", err)
	}
	second, err := prepare(context.Background(), root, "v1.5.4", "linux_amd64_gcc4", "ducklake", install)
	if err != nil {
		t.Fatalf("reuse fixture: %v", err)
	}
	if installCalls != 1 || first != second {
		t.Fatalf("install calls = %d, paths = %q and %q", installCalls, first, second)
	}
}

func TestLocateArtifactRejectsSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "extensions")
	path := filepath.Join(root, "v1.5.4", "linux_amd64_gcc4", "ducklake.duckdb_extension")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "ducklake.duckdb_extension")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := locateArtifact(root, "v1.5.4", "linux_amd64_gcc4", "ducklake"); err == nil {
		t.Fatal("expected symlink artifact rejection")
	}
}

func TestInstallExtensionRejectsUnapprovedName(t *testing.T) {
	name := "../../httpfs; DROP TABLE secrets"
	if _, err := prepare(context.Background(), t.TempDir(), "v1.5.4", "linux_amd64", name, nil); err == nil {
		t.Fatal("expected unapproved artifact lookup rejection")
	}
	if err := installExtension(context.Background(), t.TempDir(), "linux_amd64", name); err == nil {
		t.Fatal("expected unapproved extension rejection")
	}
}

func TestWriteDevelopmentSupplyReferencesOnlyReviewedFixtureArtifacts(t *testing.T) {
	root := t.TempDir()
	fixtures := make([]preparedFixture, 0, len(fixtureExtensions))
	for _, name := range fixtureExtensions {
		path := filepath.Join(root, name+".duckdb_extension")
		contents := []byte("signed-" + name)
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		fixtures = append(fixtures, preparedFixture{
			Name: name, Path: path, Digest: hex.EncodeToString(digest[:]), ExtensionVersion: "fixture-v1",
		})
	}

	manifestPath := filepath.Join(root, "supply", "extension-supply.json")
	if err := writeDevelopmentSupply(manifestPath, "v1.5.4", "linux_amd64_gcc4", fixtures); err != nil {
		t.Fatalf("write development supply: %v", err)
	}
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest extensionsupply.Manifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SupportProfile != developmentSupportProfile || manifest.GOOS != runtime.GOOS || manifest.GOARCH != runtime.GOARCH {
		t.Fatalf("manifest target/profile = %#v", manifest)
	}
	if len(manifest.Artifacts) != len(fixtureExtensions) || len(manifest.Origins) != len(fixtureExtensions) {
		t.Fatalf("manifest has %d artifacts and %d origins", len(manifest.Artifacts), len(manifest.Origins))
	}
	for index, artifact := range manifest.Artifacts {
		if artifact.Identity.Name != fixtureExtensions[index] || manifest.Origins[index].Path != fixtures[index].Path || artifact.Origins[0] != manifest.Origins[index].ID {
			t.Fatalf("artifact/origin %d = %#v / %#v", index, artifact, manifest.Origins[index])
		}
	}
	sidecar, err := os.ReadFile(manifestPath + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if string(sidecar) != hex.EncodeToString(digest[:])+"\n" {
		t.Fatalf("manifest digest sidecar = %q", sidecar)
	}
}

func TestWriteDevelopmentSupplyRejectsIncompleteFixtureSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ducklake.duckdb_extension")
	if err := os.WriteFile(path, []byte("signed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDevelopmentSupply(filepath.Join(t.TempDir(), "extension-supply.json"), "v1.5.4", "linux_amd64_gcc4", []preparedFixture{{
		Name: "ducklake", Path: path, Digest: hex.EncodeToString(make([]byte, sha256.Size)), ExtensionVersion: "fixture-v1",
	}}); err == nil {
		t.Fatal("expected incomplete development fixture set rejection")
	}
}
