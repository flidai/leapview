// Package gcstore contains target-owned physical-pool object-store adapters.
// LocalStore exposes only one declared directory and performs digest/version
// checks before every conditional delete; it has no DuckLake maintenance path.
package gcstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type LocalStore struct {
	Root string
}

func NewLocal(root string) (*LocalStore, error) {
	root, err := filepath.Abs(root)
	if err != nil || strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("physical pool root is invalid")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("physical pool root is not a directory")
	}
	return &LocalStore{Root: filepath.Clean(root)}, nil
}

func (s *LocalStore) path(key string) (string, error) {
	if s == nil || s.Root == "" {
		return "", errors.New("local pool store is not initialized")
	}
	key = filepath.Clean(filepath.FromSlash(strings.TrimSpace(key)))
	if key == "." || filepath.IsAbs(key) || key == ".." || strings.HasPrefix(key, ".."+string(filepath.Separator)) {
		return "", ErrObjectOutsidePool
	}
	path := filepath.Join(s.Root, key)
	if path != s.Root && !strings.HasPrefix(path, s.Root+string(filepath.Separator)) {
		return "", ErrObjectOutsidePool
	}
	return path, nil
}

func digestFile(path string) (string, int64, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, nil, err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, nil, err
	}
	sum := hash.Sum(nil)
	return "sha256:" + hex.EncodeToString(sum), n, info, nil
}

func version(info os.FileInfo) string {
	return fmt.Sprintf("%d:%d", info.ModTime().UTC().UnixNano(), info.Size())
}

func (s *LocalStore) Open(_ context.Context, key string) (ObjectBody, error) {
	path, err := s.path(key)
	if err != nil {
		return ObjectBody{}, err
	}
	digest, size, _, err := digestFile(path)
	if err != nil {
		return ObjectBody{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return ObjectBody{}, err
	}
	return ObjectBody{Body: file, Size: size, Metadata: map[string]string{"sha256": digest, "size": fmt.Sprintf("%d", size)}}, nil
}

func (s *LocalStore) ListPoolObjects(_ context.Context, _ string) ([]Object, error) {
	var objects []Object
	err := filepath.Walk(s.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(path) == localOwnershipMarker || filepath.Base(path) == localDeletionLease || filepath.Base(path) == localDeletionLeaseLock {
			return nil
		}
		key, err := filepath.Rel(s.Root, path)
		if err != nil {
			return err
		}
		digest, size, stat, err := digestFile(path)
		if err != nil {
			return err
		}
		objects = append(objects, Object{Key: filepath.ToSlash(key), Digest: digest, Size: size, Version: version(stat), CreatedAt: stat.ModTime().UTC(), LastModified: stat.ModTime().UTC()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return objects, nil
}

func (s *LocalStore) Stat(_ context.Context, _ string, key string) (Object, error) {
	path, err := s.path(key)
	if err != nil {
		return Object{}, err
	}
	digest, size, info, err := digestFile(path)
	if err != nil {
		return Object{}, err
	}
	return Object{Key: filepath.ToSlash(strings.TrimSpace(key)), Digest: digest, Size: size, Version: version(info), CreatedAt: info.ModTime().UTC(), LastModified: info.ModTime().UTC()}, nil
}

func (s *LocalStore) DeleteConditional(_ context.Context, req DeleteRequest) (DeleteResponse, error) {
	object, err := s.Stat(context.Background(), req.PhysicalPoolID, req.Key)
	if errors.Is(err, os.ErrNotExist) {
		return DeleteResponse{NotFound: true}, nil
	}
	if err != nil {
		return DeleteResponse{}, err
	}
	if object.Digest != req.Digest || (req.Version != "" && object.Version != req.Version) {
		return DeleteResponse{}, fmt.Errorf("conditional object identity mismatch")
	}
	path, err := s.path(req.Key)
	if err != nil {
		return DeleteResponse{}, err
	}
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return DeleteResponse{NotFound: true}, nil
	} else if err != nil {
		return DeleteResponse{}, err
	}
	return DeleteResponse{Deleted: true}, nil
}
