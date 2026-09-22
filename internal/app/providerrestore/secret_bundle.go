package providerrestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type CredentialBundle struct {
	SchemaVersion   int    `json:"schemaVersion"`
	ControlURL      string `json:"controlUrl"`
	DuckLakeURL     string `json:"duckLakeUrl"`
	ObjectEndpoint  string `json:"objectEndpoint"`
	ObjectRegion    string `json:"objectRegion"`
	ObjectAccessKey string `json:"objectAccessKey"`
	ObjectSecretKey string `json:"objectSecretKey"`
}

type FileSecretBundleStore struct{ Root string }

func (store FileSecretBundleStore) Save(_ context.Context, bundle CredentialBundle) (SecretBundleReference, error) {
	if err := validateCredentialBundle(bundle); err != nil {
		return SecretBundleReference{}, err
	}
	root, err := store.root()
	if err != nil {
		return SecretBundleReference{}, err
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return SecretBundleReference{}, err
	}
	encoded = append(encoded, '\n')
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(root, 0o700); err != nil {
		return SecretBundleReference{}, err
	}
	path := filepath.Join(root, digest+".json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return SecretBundleReference{}, err
	}
	return SecretBundleReference{
		Provider: "root-readable-file", URI: (&url.URL{Scheme: "file", Path: path}).String(), SHA256: digest, Version: "1",
		Keys: []string{"postgres.control.url", "postgres.ducklake.url", "object.credentials"},
	}, nil
}

func (store FileSecretBundleStore) Load(_ context.Context, reference SecretBundleReference) (CredentialBundle, error) {
	if err := validateSecretBundleReference(reference); err != nil {
		return CredentialBundle{}, err
	}
	root, err := store.root()
	if err != nil {
		return CredentialBundle{}, err
	}
	parsed, err := url.Parse(reference.URI)
	if err != nil || filepath.Dir(filepath.Clean(parsed.Path)) != root || filepath.Base(parsed.Path) != reference.SHA256+".json" {
		return CredentialBundle{}, fmt.Errorf("%w: secret bundle is outside its private store", ErrInconsistent)
	}
	file, err := os.Open(parsed.Path)
	if err != nil {
		return CredentialBundle{}, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxEvidenceBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return CredentialBundle{}, errors.Join(readErr, closeErr)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != reference.SHA256 {
		return CredentialBundle{}, fmt.Errorf("%w: secret bundle digest mismatch", ErrInconsistent)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle CredentialBundle
	if err := decoder.Decode(&bundle); err != nil {
		return CredentialBundle{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CredentialBundle{}, fmt.Errorf("secret bundle contains trailing data")
	}
	if err := validateCredentialBundle(bundle); err != nil {
		return CredentialBundle{}, err
	}
	return bundle, nil
}

func (store FileSecretBundleStore) root() (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(store.Root))
	if err != nil || root == "." {
		return "", fmt.Errorf("%w: private secret bundle root is required", ErrInvalid)
	}
	return root, nil
}

func validateCredentialBundle(bundle CredentialBundle) error {
	if bundle.SchemaVersion != 1 || strings.TrimSpace(bundle.ObjectAccessKey) == "" || strings.TrimSpace(bundle.ObjectSecretKey) == "" || strings.TrimSpace(bundle.ObjectRegion) == "" {
		return fmt.Errorf("%w: credential bundle is incomplete", ErrInvalid)
	}
	for _, value := range []string{bundle.ControlURL, bundle.DuckLakeURL} {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "postgres" || parsed.User == nil || parsed.Hostname() == "" || strings.TrimPrefix(parsed.Path, "/") == "" {
			return fmt.Errorf("%w: credential bundle database URL is invalid", ErrInvalid)
		}
	}
	endpoint, err := url.Parse(bundle.ObjectEndpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Hostname() == "" {
		return fmt.Errorf("%w: credential bundle object endpoint is invalid", ErrInvalid)
	}
	return nil
}
