package catalogseal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalObjectStore is a target-owned create-only catalog store. Metadata is
// verified against streamed bytes on create and recomputed on read, avoiding
// mutable manifests or sidecar files in the physical object namespace.
type LocalObjectStore struct{ Root string }

func NewLocalObjectStore(root string) (*LocalObjectStore, error) {
	root, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || root == "" {
		return nil, fmt.Errorf("local catalog root is invalid")
	}
	if info, statErr := os.Stat(root); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, err
		}
	} else if !info.IsDir() {
		return nil, fmt.Errorf("local catalog root is not a directory")
	}
	return &LocalObjectStore{Root: filepath.Clean(root)}, nil
}

func (s *LocalObjectStore) path(key string) (string, error) {
	if s == nil || s.Root == "" {
		return "", ErrObjectUpload
	}
	raw := strings.TrimSpace(key)
	if raw == "" || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", ErrObjectUpload
	}
	for _, part := range strings.Split(strings.ReplaceAll(raw, "\\", "/"), "/") {
		if part == ".." {
			return "", ErrObjectUpload
		}
	}
	key = filepath.Clean(filepath.FromSlash(raw))
	if key == "." || filepath.IsAbs(key) || key == ".." || strings.HasPrefix(key, ".."+string(filepath.Separator)) {
		return "", ErrObjectUpload
	}
	full := filepath.Join(s.Root, key)
	if full != s.Root && !strings.HasPrefix(full, s.Root+string(filepath.Separator)) {
		return "", ErrObjectUpload
	}
	return full, nil
}

func (s *LocalObjectStore) Create(_ context.Context, key string, body io.Reader, metadata ObjectMetadata) error {
	if body == nil || metadata == nil {
		return ErrObjectUpload
	}
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ErrObjectUpload
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".catalog-upload-")
	if err != nil {
		return ErrObjectUpload
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), body)
	if err != nil {
		_ = tmp.Close()
		return ErrObjectUpload
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if metadata[MetadataDigest] != digest || metadata[MetadataSize] != fmt.Sprintf("%d", n) {
		_ = tmp.Close()
		return ErrObjectCorrupt
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return ErrObjectUpload
	}
	if err := tmp.Close(); err != nil {
		return ErrObjectUpload
	}
	// Link is atomic and fails with EEXIST without replacing the prior object.
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrObjectExists
		}
		return ErrObjectUpload
	}
	return nil
}

func (s *LocalObjectStore) Open(_ context.Context, key string) (Object, error) {
	path, err := s.path(key)
	if err != nil {
		return Object{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Object{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return Object{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return Object{}, ErrObjectCorrupt
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return Object{}, ErrObjectCorrupt
	}
	metadata := ObjectMetadata{MetadataDigest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), MetadataSize: fmt.Sprintf("%d", info.Size())}
	return Object{Body: file, Size: info.Size(), Metadata: metadata}, nil
}
