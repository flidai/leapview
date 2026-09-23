package providerrestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const RetainedResourceManifestSchemaVersion = 1

type RetainedResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type RetainedResourceManifest struct {
	SchemaVersion int                `json:"schemaVersion"`
	RunID         string             `json:"runId"`
	Resources     []RetainedResource `json:"resources"`
}

type RetainedResourceManifestStore struct{ Path string }

func (store RetainedResourceManifestStore) Load() (RetainedResourceManifest, bool, error) {
	path, err := store.path()
	if err != nil {
		return RetainedResourceManifest{}, false, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return RetainedResourceManifest{}, false, nil
	}
	if err != nil {
		return RetainedResourceManifest{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest RetainedResourceManifest
	if err := decoder.Decode(&manifest); err != nil {
		return RetainedResourceManifest{}, false, fmt.Errorf("decode retained-resource manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RetainedResourceManifest{}, false, fmt.Errorf("retained-resource manifest contains trailing data")
	}
	if err := validateRetainedResourceManifest(manifest); err != nil {
		return RetainedResourceManifest{}, false, err
	}
	return manifest, true, nil
}

func (store RetainedResourceManifestStore) Start(runID string) error {
	return store.save(RetainedResourceManifest{SchemaVersion: RetainedResourceManifestSchemaVersion, RunID: runID})
}

func (store RetainedResourceManifestStore) Record(runID string, resource RetainedResource) error {
	manifest, found, err := store.Load()
	if err != nil {
		return err
	}
	if !found || manifest.RunID != runID {
		return fmt.Errorf("%w: retained-resource manifest run identity mismatch", ErrInconsistent)
	}
	if strings.TrimSpace(resource.Kind) == "" || strings.TrimSpace(resource.ID) == "" || resource.Kind != strings.TrimSpace(resource.Kind) || resource.ID != strings.TrimSpace(resource.ID) {
		return fmt.Errorf("%w: retained resource identity is invalid", ErrInvalid)
	}
	if slices.Contains(manifest.Resources, resource) {
		return nil
	}
	manifest.Resources = append(manifest.Resources, resource)
	return store.save(manifest)
}

func (store RetainedResourceManifestStore) Remove() error {
	path, err := store.path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (store RetainedResourceManifestStore) save(manifest RetainedResourceManifest) error {
	if err := validateRetainedResourceManifest(manifest); err != nil {
		return err
	}
	path, err := store.path()
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".retained-resources-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
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
	return errors.Join(directory.Sync(), directory.Close())
}

func (store RetainedResourceManifestStore) path() (string, error) {
	path, err := filepath.Abs(strings.TrimSpace(store.Path))
	if err != nil || path == "." || filepath.Base(path) == "." {
		return "", fmt.Errorf("%w: retained-resource manifest path is required", ErrInvalid)
	}
	return path, nil
}

func validateRetainedResourceManifest(manifest RetainedResourceManifest) error {
	if manifest.SchemaVersion != RetainedResourceManifestSchemaVersion || strings.TrimSpace(manifest.RunID) == "" || manifest.RunID != strings.TrimSpace(manifest.RunID) {
		return fmt.Errorf("%w: retained-resource manifest identity is invalid", ErrInconsistent)
	}
	seen := map[RetainedResource]struct{}{}
	for _, resource := range manifest.Resources {
		if strings.TrimSpace(resource.Kind) == "" || strings.TrimSpace(resource.ID) == "" || resource.Kind != strings.TrimSpace(resource.Kind) || resource.ID != strings.TrimSpace(resource.ID) {
			return fmt.Errorf("%w: retained resource identity is invalid", ErrInconsistent)
		}
		if _, duplicate := seen[resource]; duplicate {
			return fmt.Errorf("%w: retained resource identity is duplicated", ErrInconsistent)
		}
		seen[resource] = struct{}{}
	}
	return nil
}
