package filesystem

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/platform/filesystem"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	ocidigest "github.com/opencontainers/go-digest"
)

const MaxUploadBytes = 128 << 20

type ArtifactStore struct {
	dir string
}

func NewArtifactStore(dir string) *ArtifactStore {
	return &ArtifactStore{dir: dir}
}

func (s *ArtifactStore) UploadPath(servingStateID servingstate.ID) string {
	if err := validateArtifactPathComponent(string(servingStateID), "serving state id"); err != nil {
		return filepath.Join(s.dir, ".invalid.upload.tar.gz")
	}
	return filepath.Join(s.dir, string(servingStateID)+".upload.tar.gz")
}

func (s *ArtifactStore) SaveUpload(_ context.Context, servingStateID servingstate.ID, source io.Reader) (int64, error) {
	if err := validateArtifactPathComponent(string(servingStateID), "serving state id"); err != nil {
		return 0, err
	}
	if err := securefs.EnsurePrivateDir(s.dir); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(s.dir, string(servingStateID)+".upload-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	size, copyErr := io.Copy(tmp, source)
	closeErr := tmp.Close()
	if copyErr != nil {
		return size, copyErr
	}
	if closeErr != nil {
		return size, fmt.Errorf("closing uploaded artifact: %w", closeErr)
	}
	if err := os.Rename(tmpPath, s.UploadPath(servingStateID)); err != nil {
		return size, err
	}
	cleanup = false
	return size, nil
}

func (s *ArtifactStore) PromoteUploaded(_ context.Context, servingStateID servingstate.ID, digest, manifestJSON string) (servingstate.Artifact, error) {
	if err := validateArtifactPathComponent(string(servingStateID), "serving state id"); err != nil {
		return servingstate.Artifact{}, err
	}
	if err := validateArtifactPathComponent(digest, "artifact digest"); err != nil {
		return servingstate.Artifact{}, err
	}
	if err := securefs.EnsurePrivateDir(s.dir); err != nil {
		return servingstate.Artifact{}, err
	}
	uploadPath := s.UploadPath(servingStateID)
	finalPath := filepath.Join(s.dir, digest+".tar.gz")
	if err := copyFile(uploadPath, finalPath); err != nil {
		return servingstate.Artifact{}, err
	}
	return servingstate.Artifact{
		ID:             "artifact_" + string(servingStateID),
		ServingStateID: servingStateID,
		Digest:         digest,
		Format:         projectbundle.BundleFormat,
		Path:           finalPath,
		ManifestJSON:   manifestJSON,
		SizeBytes:      fileSize(finalPath),
	}, nil
}

func (s *ArtifactStore) DiscardUploaded(_ context.Context, servingStateID servingstate.ID) error {
	if err := validateArtifactPathComponent(string(servingStateID), "serving state id"); err != nil {
		return err
	}
	if err := os.Remove(s.UploadPath(servingStateID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func validateArtifactPathComponent(value, label string) error {
	if value != strings.TrimSpace(value) || value == "" || value == "." || value == ".." || filepath.IsAbs(value) || filepath.Base(value) != value {
		return fmt.Errorf("%s must be a safe path component", label)
	}
	return nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func copyFile(source, target string) error {
	if same, err := sameFileContent(source, target); err == nil && same {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil {
		return fmt.Errorf("artifact target %s already exists with different content", target)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(target), ".artifact-promote-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := out.Name()
	cleanup := true
	defer func() {
		_ = out.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := out.Chmod(securefs.PrivateFileMode); err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", target, err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func sameFileContent(left, right string) (bool, error) {
	leftDigest, err := fileDigest(left)
	if err != nil {
		return false, err
	}
	rightDigest, err := fileDigest(right)
	if err != nil {
		return false, err
	}
	return leftDigest == rightDigest, nil
}

func fileDigest(path string) (ocidigest.Digest, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return ocidigest.FromReader(file)
}
