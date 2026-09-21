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

	"github.com/flidai/leapview/internal/refresh/recovery"
)

const maxEvidenceBytes = 2 << 20

type FileEvidenceStore struct{ Root string }

func (store FileEvidenceStore) root() (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(store.Root))
	if err != nil || root == "." {
		return "", fmt.Errorf("%w: evidence root is required", ErrInvalid)
	}
	return root, nil
}

func (store FileEvidenceStore) path(digest string) (string, error) {
	root, err := store.root()
	if err != nil {
		return "", err
	}
	if len(digest) != 64 {
		return "", fmt.Errorf("%w: evidence digest is invalid", ErrInvalid)
	}
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return "", fmt.Errorf("%w: evidence digest is invalid", ErrInvalid)
	}
	return filepath.Join(root, digest+".json"), nil
}

func (store FileEvidenceStore) Load(_ context.Context, reference recovery.EvidenceReference) (Report, error) {
	canonical, err := recovery.CanonicalEvidenceReferences([]recovery.EvidenceReference{reference})
	if err != nil || len(canonical) != 1 || canonical[0].Kind != "provider-restore" {
		return Report{}, fmt.Errorf("%w: provider restore evidence reference is invalid", ErrInconsistent)
	}
	path, err := store.path(reference.SHA256)
	if err != nil {
		return Report{}, err
	}
	parsed, err := url.Parse(reference.URI)
	if err != nil || parsed.Scheme != "file" || parsed.Host != "" || filepath.Clean(parsed.Path) != path {
		return Report{}, fmt.Errorf("%w: provider restore evidence path does not match its digest", ErrInconsistent)
	}
	file, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxEvidenceBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return Report{}, errors.Join(readErr, closeErr)
	}
	if len(raw) > maxEvidenceBytes {
		return Report{}, fmt.Errorf("provider restore evidence exceeds %d bytes", maxEvidenceBytes)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != reference.SHA256 {
		return Report{}, fmt.Errorf("%w: provider restore evidence digest mismatch", ErrInconsistent)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("decode provider restore evidence: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Report{}, fmt.Errorf("provider restore evidence contains trailing data")
	}
	if report.SchemaVersion != ReportSchemaVersion || report.Kind != ReportKind || report.OccurrenceID == "" {
		return Report{}, fmt.Errorf("%w: provider restore evidence identity mismatch", ErrInconsistent)
	}
	return report, nil
}

func (store FileEvidenceStore) Save(_ context.Context, report Report) (recovery.EvidenceReference, error) {
	if report.SchemaVersion != ReportSchemaVersion || report.Kind != ReportKind || report.OccurrenceID == "" {
		return recovery.EvidenceReference{}, ErrInvalid
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return recovery.EvidenceReference{}, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxEvidenceBytes {
		return recovery.EvidenceReference{}, fmt.Errorf("provider restore evidence exceeds %d bytes", maxEvidenceBytes)
	}
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	path, err := store.path(digest)
	if err != nil {
		return recovery.EvidenceReference{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return recovery.EvidenceReference{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".provider-restore-*.tmp")
	if err != nil {
		return recovery.EvidenceReference{}, err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return recovery.EvidenceReference{}, err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return recovery.EvidenceReference{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return recovery.EvidenceReference{}, err
	}
	if err := temporary.Close(); err != nil {
		return recovery.EvidenceReference{}, err
	}
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Equal(existing, encoded) {
			return recovery.EvidenceReference{}, fmt.Errorf("%w: content-addressed provider evidence was modified", ErrInconsistent)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return recovery.EvidenceReference{}, readErr
	} else if err := os.Rename(temporaryPath, path); err != nil {
		return recovery.EvidenceReference{}, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return recovery.EvidenceReference{}, err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return recovery.EvidenceReference{}, errors.Join(syncErr, closeErr)
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	return recovery.EvidenceReference{Kind: "provider-restore", URI: uri, SHA256: digest}, nil
}
