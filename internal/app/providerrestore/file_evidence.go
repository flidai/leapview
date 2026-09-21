package providerrestore

import (
	"bufio"
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

func (store FileEvidenceStore) path(occurrenceID string) (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(store.Root))
	if err != nil || root == "." || strings.TrimSpace(occurrenceID) == "" {
		return "", fmt.Errorf("%w: evidence root and occurrence are required", ErrInvalid)
	}
	sum := sha256.Sum256([]byte(occurrenceID))
	return filepath.Join(root, "provider-restore-"+hex.EncodeToString(sum[:16])+".json"), nil
}

func (store FileEvidenceStore) Load(_ context.Context, occurrenceID string) (Report, bool, error) {
	path, err := store.path(occurrenceID)
	if err != nil {
		return Report{}, false, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Report{}, false, nil
	}
	if err != nil {
		return Report{}, false, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxEvidenceBytes+1))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, false, fmt.Errorf("decode provider restore evidence: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Report{}, false, fmt.Errorf("provider restore evidence contains trailing data")
	}
	if report.SchemaVersion != ReportSchemaVersion || report.Kind != ReportKind || report.OccurrenceID != occurrenceID {
		return Report{}, false, fmt.Errorf("%w: provider restore evidence identity mismatch", ErrInconsistent)
	}
	return report, true, nil
}

func (store FileEvidenceStore) Save(_ context.Context, report Report) (recovery.EvidenceReference, error) {
	path, err := store.path(report.OccurrenceID)
	if err != nil {
		return recovery.EvidenceReference{}, err
	}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return recovery.EvidenceReference{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".provider-restore-*.tmp")
	if err != nil {
		return recovery.EvidenceReference{}, err
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	defer cleanup()
	writer := bufio.NewWriter(temporary)
	if _, err := writer.Write(encoded); err != nil {
		_ = temporary.Close()
		return recovery.EvidenceReference{}, err
	}
	if err := writer.Flush(); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
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
	sum := sha256.Sum256(encoded)
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	return recovery.EvidenceReference{Kind: "provider-restore", URI: uri, SHA256: hex.EncodeToString(sum[:])}, nil
}
