package local

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestAuthoringPackageSchemaAndNativeArchive(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("authoring archives support Linux and macOS")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("authoring archives support amd64 and arm64")
	}

	root := repositoryRoot(t)
	binary := filepath.Join(t.TempDir(), "leapview")
	requireWriteFile(t, binary, "#!/bin/sh\nexit 0\n", 0o755)
	output := t.TempDir()
	revision := strings.Repeat("a", 40)
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	command := exec.Command(filepath.Join(root, "scripts", "package-authoring-cli.sh"), binary, output, runtime.GOOS, runtime.GOARCH)
	command.Dir = root
	command.Env = append(os.Environ(),
		"BUILD_VERSION=1.2.3",
		"BUILD_REVISION="+revision,
		"BUILD_TIME=2026-09-15T12:00:00Z",
		"BUILD_RELEASE=true",
		"IMAGE_REFERENCE="+image,
		"RELEASE_TAG=v1.2.3",
	)
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("package authoring CLI: %v\n%s", err, combined)
	}
	archive := strings.TrimSpace(string(combined))
	if filepath.Dir(archive) != output {
		t.Fatalf("archive = %q, want output under %q", archive, output)
	}
	if _, err := os.Stat(archive + ".sha256"); err != nil {
		t.Fatalf("outer checksum: %v", err)
	}

	files := readTarGzip(t, archive)
	prefix := "leapview-cli-v1.2.3-" + runtime.GOOS + "-" + runtime.GOARCH + "/"
	wantFiles := []string{
		"INSTALL.md",
		"SHA256SUMS",
		"authoring-package.json",
		"authoring-package.schema.json",
		"image-reference.txt",
		"leapview",
		"local-runtime/README.md",
		"local-runtime/compose.yaml",
		"local-runtime/postgres-init.sh",
		"local-runtime/runtime-package.json",
		"local-runtime/runtime-package.schema.json",
		"release-identity.json",
	}
	for _, name := range wantFiles {
		if _, ok := files[prefix+name]; !ok {
			t.Errorf("archive missing %s", name)
		}
	}

	var manifest any
	if err := json.Unmarshal(files[prefix+"authoring-package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	schemaDocument, err := jsonschema.UnmarshalJSON(strings.NewReader(readFile(t, "authoring-package.schema.json")))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("authoring-package.json", schemaDocument); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("authoring-package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(manifest); err != nil {
		t.Fatalf("generated authoring manifest: %v", err)
	}
	invalidHost := manifest.(map[string]any)
	invalidHost["host"].(map[string]any)["supportProfile"] = "macos-15-docker-desktop"
	if runtime.GOOS == "darwin" {
		invalidHost["host"].(map[string]any)["supportProfile"] = "ubuntu-24.04-docker-engine"
	}
	if err := schema.Validate(invalidHost); err == nil {
		t.Fatal("authoring schema accepted an OS/profile mismatch")
	}

	var packageIdentity struct {
		Identity struct {
			Version  string `json:"version"`
			Revision string `json:"revision"`
		} `json:"identity"`
		ApplicationImage string `json:"applicationImage"`
	}
	if err := json.Unmarshal(files[prefix+"authoring-package.json"], &packageIdentity); err != nil {
		t.Fatal(err)
	}
	if packageIdentity.Identity.Version != "1.2.3" || packageIdentity.Identity.Revision != revision || packageIdentity.ApplicationImage != image {
		t.Fatalf("package identity = %#v", packageIdentity)
	}
	if !strings.Contains(string(files[prefix+"SHA256SUMS"]), "./local-runtime/runtime-package.json") {
		t.Fatal("internal checksums do not cover runtime manifest")
	}
}

func TestAuthoringPackagerRejectsUnsupportedTarget(t *testing.T) {
	root := repositoryRoot(t)
	binary := filepath.Join(t.TempDir(), "leapview")
	requireWriteFile(t, binary, "binary", 0o755)
	command := exec.Command(filepath.Join(root, "scripts", "package-authoring-cli.sh"), binary, t.TempDir(), "windows", "amd64")
	command.Dir = root
	command.Env = append(os.Environ(),
		"BUILD_VERSION=1.2.3",
		"BUILD_REVISION="+strings.Repeat("a", 40),
		"BUILD_TIME=2026-09-15T12:00:00Z",
		"BUILD_RELEASE=true",
		"IMAGE_REFERENCE=ghcr.io/flidai/leapview@sha256:"+strings.Repeat("b", 64),
		"RELEASE_TAG=v1.2.3",
	)
	combined, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(combined), "unsupported authoring CLI platform") {
		t.Fatalf("unsupported platform result: err=%v output=%s", err, combined)
	}
}

func TestReleasePublishesAndQualifiesEveryAuthoringPlatform(t *testing.T) {
	workflow := readFile(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	publicWorkflow := readFile(t, filepath.Join("..", "..", ".github", "workflows", "installed-candidate.yml"))
	for _, required := range []string{
		"authoring-cli:",
		"CGO_ENABLED=1 go build -trimpath -tags duckdb_arrow",
		"scripts/package-authoring-cli.sh",
		"macos-15-intel",
		"macos-15",
		"ubuntu-24.04-arm",
		"candidate/authoring/*.tar.gz",
		"subject-path: dist/leapview-cli-*.tar.gz",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	for _, required := range []string{
		"Public authoring CLI",
		"leapview-cli-${RELEASE_TAG}-${TARGET_OS}-${TARGET_ARCH}",
		"$PACKAGE_ROOT/leapview\" version --json",
		"$PACKAGE_ROOT/leapview\" dev status --format json",
	} {
		if !strings.Contains(publicWorkflow, required) {
			t.Errorf("installed-candidate workflow missing %q", required)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func requireWriteFile(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

func readTarGzip(t *testing.T, path string) map[string][]byte {
	t.Helper()
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	compressed, err := gzip.NewReader(source)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	files := map[string][]byte{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = body
	}
	if len(files) == 0 {
		t.Fatal("archive is empty")
	}
	return files
}
