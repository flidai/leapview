package providerrestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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
	PostgresRootCA  string `json:"postgresRootCa"`
	ObjectRootCA    string `json:"objectRootCa"`
}

type FileSecretBundleStore struct {
	Root string
}

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
	referenceURI := (&url.URL{Scheme: "leapview-secret", Host: "host-provisioned", Path: "/recovery/" + digest}).String()
	return SecretBundleReference{
		Provider: "host-provisioned-root-file", URI: referenceURI, SHA256: digest, Version: "1",
		Keys: []string{"object.credentials", "object.root-ca", "postgres.control.url", "postgres.ducklake.url", "postgres.root-ca"},
	}, nil
}

func (store FileSecretBundleStore) SourcePath(reference SecretBundleReference) (string, error) {
	if err := validateSecretBundleReference(reference); err != nil {
		return "", err
	}
	root, err := store.root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, reference.SHA256+".json"), nil
}

func (store FileSecretBundleStore) Load(_ context.Context, reference SecretBundleReference) (CredentialBundle, error) {
	if err := validateSecretBundleReference(reference); err != nil {
		return CredentialBundle{}, err
	}
	root, err := store.root()
	if err != nil {
		return CredentialBundle{}, err
	}
	file, err := os.Open(filepath.Join(root, reference.SHA256+".json"))
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
	if bundle.SchemaVersion != 2 || strings.TrimSpace(bundle.ObjectAccessKey) == "" || strings.TrimSpace(bundle.ObjectSecretKey) == "" || strings.TrimSpace(bundle.ObjectRegion) == "" {
		return fmt.Errorf("%w: credential bundle is incomplete", ErrInvalid)
	}
	for _, value := range []string{bundle.ControlURL, bundle.DuckLakeURL} {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "postgres" || parsed.User == nil || parsed.Hostname() == "" || strings.TrimPrefix(parsed.Path, "/") == "" || parsed.Query().Get("sslmode") != "verify-full" || !replacementReachableHostname(parsed.Hostname()) {
			return fmt.Errorf("%w: credential bundle database URL is invalid", ErrInvalid)
		}
	}
	endpoint, err := url.Parse(bundle.ObjectEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Hostname() == "" || !replacementReachableHostname(endpoint.Hostname()) {
		return fmt.Errorf("%w: credential bundle object endpoint is invalid", ErrInvalid)
	}
	for _, raw := range []string{bundle.PostgresRootCA, bundle.ObjectRootCA} {
		block, _ := pem.Decode([]byte(raw))
		if block == nil || block.Type != "CERTIFICATE" {
			return fmt.Errorf("%w: credential bundle TLS root is invalid", ErrInvalid)
		}
		certificate, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil || !certificate.IsCA {
			return fmt.Errorf("%w: credential bundle TLS root is invalid", ErrInvalid)
		}
	}
	return nil
}

// ValidateCredentialBundleForHandoff binds the private provider credentials to
// the credential-free endpoints in the immutable recovery handoff. Callers must
// perform this check before contacting any restored provider.
func ValidateCredentialBundleForHandoff(handoff ReplacementHandoff, bundle CredentialBundle) error {
	if err := validateCredentialBundle(bundle); err != nil {
		return err
	}
	credentials := map[string]string{
		"control": bundle.ControlURL, "ducklake": bundle.DuckLakeURL,
	}
	for _, endpoint := range handoff.Providers {
		switch endpoint.Role {
		case "control", "ducklake":
			parsed, err := url.Parse(credentials[endpoint.Role])
			if err != nil {
				return fmt.Errorf("%w: private database credential is malformed", ErrInconsistent)
			}
			database := strings.TrimPrefix(parsed.Path, "/")
			parsed.User, parsed.Path, parsed.RawPath = nil, "", ""
			if parsed.String() != endpoint.Endpoint || database != endpoint.Database || endpoint.TLSRootCASecretKey != "postgres.root-ca" {
				return fmt.Errorf("%w: private database credential does not match the handoff", ErrInconsistent)
			}
		case "objects":
			if bundle.ObjectEndpoint != endpoint.Endpoint || bundle.ObjectRegion != endpoint.Region || endpoint.TLSRootCASecretKey != "object.root-ca" {
				return fmt.Errorf("%w: private object credential does not match the handoff", ErrInconsistent)
			}
		}
	}
	return nil
}
