package hostinstall

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/platform/ociref"
)

type payloadFile struct {
	Source string
	Target func(Paths) string
	Mode   os.FileMode
}

// legacyPayloadFiles is the immutable deployment payload contract.
var legacyPayloadFiles = []payloadFile{
	{Source: "leapviewctl", Target: func(paths Paths) string { return filepath.Join(paths.Root, "leapviewctl") }, Mode: 0o700},
	{Source: "compose.yaml", Target: func(paths Paths) string { return filepath.Join(paths.Root, "compose.yaml") }, Mode: 0o600},
	{Source: "compose.https.yaml", Target: func(paths Paths) string { return filepath.Join(paths.Root, "compose.https.yaml") }, Mode: 0o600},
	{Source: "Caddyfile", Target: func(paths Paths) string { return filepath.Join(paths.Root, "Caddyfile") }, Mode: 0o600},
	{Source: "deployment.env.example", Target: func(paths Paths) string { return filepath.Join(paths.Root, "deployment.env.example") }, Mode: 0o600},
	{Source: "leapviewctl-wrapper", Target: func(paths Paths) string { return filepath.Join(paths.SystemBin, "leapviewctl") }, Mode: 0o700},
}

var requiredPayloadFiles = append([]payloadFile{}, legacyPayloadFiles...)

func payloadFiles(payload map[string][]byte) ([]payloadFile, error) {
	for _, file := range legacyPayloadFiles {
		if len(payload[file.Source]) == 0 {
			return nil, fmt.Errorf("deployment payload %s is missing or empty", file.Source)
		}
	}
	return requiredPayloadFiles, nil
}

func stageGeneration(paths Paths, image string, payload map[string][]byte) (string, error) {
	parsed, err := ociref.ParseImmutable(image)
	if err != nil {
		return "", err
	}
	files, err := payloadFiles(payload)
	if err != nil {
		return "", err
	}
	releases := filepath.Join(paths.Root, "releases")
	if err := os.MkdirAll(releases, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(releases, 0o700); err != nil {
		return "", err
	}
	generation := parsed.Generation
	destination := filepath.Join(releases, generation)
	if _, err := os.Lstat(destination); err == nil {
		if err := validateGeneration(destination, payload); err == nil {
			return generation, nil
		}
		return "", fmt.Errorf("existing deployment generation %s does not match the admitted payload", destination)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	temporary, err := os.MkdirTemp(releases, ".generation-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return "", err
	}
	for _, file := range files {
		if err := writeAtomic(filepath.Join(temporary, file.Source), payload[file.Source], file.Mode); err != nil {
			return "", fmt.Errorf("stage %s: %w", file.Source, err)
		}
	}
	if err := syncPath(temporary); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", err
	}
	if err := syncPath(releases); err != nil {
		return "", err
	}
	return generation, nil
}

func generationMatchesImage(generation string, reference ociref.Immutable) bool {
	return generation == reference.Generation
}

func validateGeneration(directory string, payload map[string][]byte) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("generation %s must be a directory", directory)
	}
	files, err := payloadFiles(payload)
	if err != nil {
		return err
	}
	for _, file := range files {
		path := filepath.Join(directory, file.Source)
		fileInfo, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("generation file %s must be regular", path)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(contents, payload[file.Source]) {
			return fmt.Errorf("generation %s differs from immutable image payload", directory)
		}
		if fileInfo.Mode().Perm() != file.Mode {
			return fmt.Errorf("generation file %s has mode %o, want %o", path, fileInfo.Mode().Perm(), file.Mode)
		}
	}
	return nil
}

func ensurePayloadLinks(paths Paths) error {
	return ensurePayloadLinksFor(paths, requiredPayloadFiles)
}

func ensurePayloadLinksFor(paths Paths, files []payloadFile) error {
	for _, file := range files {
		target := file.Target(paths)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source := filepath.Join(paths.Root, "current", file.Source)
		relative, err := filepath.Rel(filepath.Dir(target), source)
		if err != nil {
			return err
		}
		info, err := os.Lstat(target)
		if err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("target %s is not a symbolic link", target)
			}
			actual, err := os.Readlink(target)
			if err != nil {
				return err
			}
			if actual != relative {
				return fmt.Errorf("target %s has unexpected symbolic link %q", target, actual)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.Symlink(relative, target); err != nil {
			return err
		}
		if err := syncPath(filepath.Dir(target)); err != nil {
			return err
		}
	}
	return nil
}

func activateGeneration(paths Paths, generation string) error {
	if generation == "" || strings.ContainsAny(generation, `/\\`) {
		return fmt.Errorf("invalid deployment generation %q", generation)
	}
	destination := filepath.Join(paths.Root, "releases", generation)
	info, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("deployment generation %s must be a directory", destination)
	}
	current := filepath.Join(paths.Root, "current")
	if currentInfo, err := os.Lstat(current); err == nil {
		if currentInfo.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("active deployment generation %s is not a symbolic link", current)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary := filepath.Join(paths.Root, ".current-"+generation)
	_ = os.Remove(temporary)
	if err := os.Symlink(filepath.Join("releases", generation), temporary); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, current); err != nil {
		return err
	}
	return syncPath(paths.Root)
}

func activeGeneration(paths Paths) (string, error) {
	target, err := os.Readlink(filepath.Join(paths.Root, "current"))
	if err != nil {
		return "", err
	}
	directory, generation := filepath.Split(filepath.Clean(target))
	if filepath.Clean(directory) != "releases" || generation == "" || strings.ContainsAny(generation, `/\\`) {
		return "", fmt.Errorf("active deployment generation has unexpected target %q", target)
	}
	return generation, nil
}

func syncPath(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readPayload(directory string) (map[string][]byte, error) {
	contents := make(map[string][]byte, len(requiredPayloadFiles))
	for _, file := range legacyPayloadFiles {
		if err := readPayloadFile(directory, file, contents); err != nil {
			return nil, err
		}
	}
	return contents, nil
}

func readPayloadFile(directory string, file payloadFile, contents map[string][]byte) error {
	path := filepath.Join(directory, file.Source)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return readPayloadFileWithInfo(path, file, info, contents)
}

func readPayloadFileWithInfo(path string, file payloadFile, info os.FileInfo, contents map[string][]byte) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file", file.Source)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(value) == 0 {
		return fmt.Errorf("%s is empty", file.Source)
	}
	contents[file.Source] = value
	return nil
}

func installInitialFile(path string, contents []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target %s is a symbolic link", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("target %s is not a regular file", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return writeAtomic(path, contents, mode)
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
