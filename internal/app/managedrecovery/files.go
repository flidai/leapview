// Package managedrecovery composes the managed local-storage restore profile
// over the existing RecoverySet and provider-recovery authorities.
package managedrecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
)

const maxManifestFiles = 1_000_000

type FileContent struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// FileManifest is a retained content proof, distinct from a native snapshot's
// root-path identity and closure path set. Capture requires the caller to hold
// the existing workload/host writer fence for the selected backup frontier.
type FileManifest struct {
	SchemaVersion int           `json:"schemaVersion"`
	Files         []FileContent `json:"files"`
}

func (manifest FileManifest) canonical() ([]byte, error) {
	if manifest.SchemaVersion != 1 || len(manifest.Files) > maxManifestFiles {
		return nil, errors.New("invalid managed content manifest")
	}
	manifest.Files = append([]FileContent{}, manifest.Files...)
	slices.SortFunc(manifest.Files, func(a, b FileContent) int { return slices.Compare([]byte(a.Path), []byte(b.Path)) })
	for index, file := range manifest.Files {
		decoded, err := hex.DecodeString(file.SHA256)
		if !fs.ValidPath(file.Path) || file.Path == "." || path.Clean(file.Path) != file.Path || file.Size < 0 || err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != file.SHA256 || (index > 0 && manifest.Files[index-1].Path == file.Path) {
			return nil, errors.New("invalid managed file content identity")
		}
	}
	return json.Marshal(manifest)
}

func (manifest FileManifest) Digest() (string, error) {
	encoded, err := manifest.canonical()
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CaptureFiles hashes every regular file without accepting symlinks, devices,
// sockets or pipes. The bounded root handle prevents a raced link escaping the
// selected root. Changed file identity/size/timestamp invalidates capture.
func CaptureFiles(directory string) (FileManifest, error) {
	manifest := FileManifest{SchemaVersion: 1, Files: []FileContent{}}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" {
		return manifest, errors.New("canonical absolute managed content root required")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return manifest, errors.New("managed content root must be a real directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	err = filepath.WalkDir(directory, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("managed content contains a non-regular file")
		}
		name, err := filepath.Rel(directory, full)
		if err != nil {
			return err
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		before, err := file.Stat()
		if err != nil || !before.Mode().IsRegular() {
			file.Close()
			return errors.New("managed content file identity changed")
		}
		hash := sha256.New()
		size, readErr := io.Copy(hash, file)
		after, statErr := file.Stat()
		closeErr := file.Close()
		if err := errors.Join(readErr, statErr, closeErr); err != nil {
			return err
		}
		pathInfo, err := root.Lstat(name)
		if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(before, pathInfo) || size != before.Size() || after.Size() != size || !before.ModTime().Equal(after.ModTime()) {
			return errors.New("managed content changed during capture")
		}
		if len(manifest.Files) == maxManifestFiles {
			return errors.New("managed content manifest exceeds file bound")
		}
		manifest.Files = append(manifest.Files, FileContent{Path: filepath.ToSlash(name), Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))})
		return nil
	})
	if err != nil {
		return FileManifest{}, err
	}
	return manifest, nil
}

func (manifest FileManifest) Verify(directory string) error {
	want, err := manifest.Digest()
	if err != nil {
		return err
	}
	observed, err := CaptureFiles(directory)
	if err != nil {
		return err
	}
	got, err := observed.Digest()
	if err != nil || got != want {
		return fmt.Errorf("managed restored file content differs from retained frontier")
	}
	return nil
}
