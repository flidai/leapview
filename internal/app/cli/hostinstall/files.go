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

// legacyPayloadFiles is the exact deployment payload contract shipped by
// FAI-515 / PR #368. Keep this list stable: an admitted historical image must
// remain loadable so the FAI-516 controller can roll back to it.
var legacyPayloadFiles = []payloadFile{
	{Source: "leapviewctl", Target: func(paths Paths) string { return filepath.Join(paths.Root, "leapviewctl") }, Mode: 0o700},
	{Source: "release-transition-policy.json", Target: func(paths Paths) string { return filepath.Join(paths.Root, "release-transition-policy.json") }, Mode: 0o600},
	{Source: "compose.yaml", Target: func(paths Paths) string { return filepath.Join(paths.Root, "compose.yaml") }, Mode: 0o600},
	{Source: "compose.https.yaml", Target: func(paths Paths) string { return filepath.Join(paths.Root, "compose.https.yaml") }, Mode: 0o600},
	{Source: "Caddyfile", Target: func(paths Paths) string { return filepath.Join(paths.Root, "Caddyfile") }, Mode: 0o600},
	{Source: "deployment.env.example", Target: func(paths Paths) string { return filepath.Join(paths.Root, "deployment.env.example") }, Mode: 0o600},
	{Source: "leapviewctl-wrapper", Target: func(paths Paths) string { return filepath.Join(paths.SystemBin, "leapviewctl") }, Mode: 0o700},
	{Source: "leapview-backup-hook", Target: func(paths Paths) string { return filepath.Join(paths.SystemBin, "leapview-backup-hook") }, Mode: 0o700},
	{Source: "leapview-backup.service", Target: func(paths Paths) string { return filepath.Join(paths.Systemd, "leapview-backup.service") }, Mode: 0o644},
	{Source: "leapview-backup.timer", Target: func(paths Paths) string { return filepath.Join(paths.Systemd, "leapview-backup.timer") }, Mode: 0o644},
	{Source: "leapview-backup-maintenance.service", Target: func(paths Paths) string { return filepath.Join(paths.Systemd, "leapview-backup-maintenance.service") }, Mode: 0o644},
	{Source: "leapview-backup-maintenance.timer", Target: func(paths Paths) string { return filepath.Join(paths.Systemd, "leapview-backup-maintenance.timer") }, Mode: 0o644},
}

// qualificationPayloadFiles extends the historical payload with the complete
// FAI-516 runner and evidence bundle. The group is all-or-nothing so a partial
// generation can never be treated as production-qualified.
var qualificationPayloadFiles = []payloadFile{
	{Source: "leapview.env.example", Target: func(paths Paths) string { return filepath.Join(paths.Root, "leapview.env.example") }, Mode: 0o600},
	{Source: "README.md", Target: func(paths Paths) string { return filepath.Join(paths.Root, "README.md") }, Mode: 0o600},
	{Source: "QUALIFICATION.md", Target: func(paths Paths) string { return filepath.Join(paths.Root, "QUALIFICATION.md") }, Mode: 0o600},
	{Source: filepath.Join("qualification", "Dockerfile.authoring-client"), Target: func(paths Paths) string {
		return filepath.Join(paths.Root, "qualification", "Dockerfile.authoring-client")
	}, Mode: 0o600},
	{Source: filepath.Join("qualification", "authoring-worker.mjs"), Target: func(paths Paths) string { return filepath.Join(paths.Root, "qualification", "authoring-worker.mjs") }, Mode: 0o600},
	{Source: filepath.Join("qualification", "browser.mjs"), Target: func(paths Paths) string { return filepath.Join(paths.Root, "qualification", "browser.mjs") }, Mode: 0o600},
	{Source: filepath.Join("qualification", "bun.lock"), Target: func(paths Paths) string { return filepath.Join(paths.Root, "qualification", "bun.lock") }, Mode: 0o600},
	{Source: filepath.Join("qualification", "package.json"), Target: func(paths Paths) string { return filepath.Join(paths.Root, "qualification", "package.json") }, Mode: 0o600},
	{Source: filepath.Join("qualification", "performance-policy.json"), Target: func(paths Paths) string { return filepath.Join(paths.Root, "qualification", "performance-policy.json") }, Mode: 0o600},
	{Source: filepath.Join("qualification", "performance.mjs"), Target: func(paths Paths) string { return filepath.Join(paths.Root, "qualification", "performance.mjs") }, Mode: 0o600},
	{Source: "leapview-recovery-qualification.service", Target: func(paths Paths) string {
		return filepath.Join(paths.Systemd, "leapview-recovery-qualification.service")
	}, Mode: 0o644},
	{Source: "leapview-recovery-qualification.timer", Target: func(paths Paths) string { return filepath.Join(paths.Systemd, "leapview-recovery-qualification.timer") }, Mode: 0o644},
}

var requiredPayloadFiles = append(append([]payloadFile{}, legacyPayloadFiles...), qualificationPayloadFiles...)

func payloadFiles(payload map[string][]byte) ([]payloadFile, error) {
	for _, file := range legacyPayloadFiles {
		if len(payload[file.Source]) == 0 {
			return nil, fmt.Errorf("deployment payload %s is missing or empty", file.Source)
		}
	}
	present := 0
	for _, file := range qualificationPayloadFiles {
		if len(payload[file.Source]) != 0 {
			present++
		}
	}
	if present == 0 {
		return legacyPayloadFiles, nil
	}
	if present != len(qualificationPayloadFiles) {
		return nil, fmt.Errorf("deployment payload contains an incomplete recovery qualification bundle")
	}
	return requiredPayloadFiles, nil
}

func payloadHasQualification(payload map[string][]byte) bool {
	files, err := payloadFiles(payload)
	return err == nil && len(files) == len(requiredPayloadFiles)
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
		// PR #368 controllers know only the legacy payload list. When such a
		// controller admits an FAI-516 image it creates a valid legacy subset
		// under the candidate digest. Preserve that immutable subset and stage
		// the complete reviewed payload beside it under a versioned generation.
		legacySubset, legacyErr := generationIsLegacySubset(destination, payload)
		if !payloadHasQualification(payload) || legacyErr != nil || !legacySubset {
			return "", fmt.Errorf("existing deployment generation %s does not match the admitted payload", destination)
		}
		generation += "-qualification-v1"
		destination = filepath.Join(releases, generation)
		if _, err := os.Lstat(destination); err == nil {
			if err := validateGeneration(destination, payload); err != nil {
				return "", err
			}
			return generation, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
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

func generationIsLegacySubset(directory string, payload map[string][]byte) (bool, error) {
	if err := validateGeneration(directory, legacyPayload(payload)); err != nil {
		return false, err
	}
	for _, file := range qualificationPayloadFiles {
		_, err := os.Lstat(filepath.Join(directory, file.Source))
		if err == nil {
			return false, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
	}
	return true, nil
}

func legacyPayload(payload map[string][]byte) map[string][]byte {
	legacy := make(map[string][]byte, len(legacyPayloadFiles))
	for _, file := range legacyPayloadFiles {
		legacy[file.Source] = payload[file.Source]
	}
	return legacy
}

func generationMatchesImage(generation string, reference ociref.Immutable) bool {
	return generation == reference.Generation || generation == reference.Generation+"-qualification-v1"
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
	optionalPresent := false
	for _, file := range qualificationPayloadFiles {
		path := filepath.Join(directory, file.Source)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		optionalPresent = true
		if err := readPayloadFileWithInfo(path, file, info, contents); err != nil {
			return nil, err
		}
	}
	if optionalPresent {
		if _, err := payloadFiles(contents); err != nil {
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
