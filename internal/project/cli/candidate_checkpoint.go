package cli

import (
	"bytes"
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
	"sync"

	"github.com/flidai/leapview/internal/platform/digest"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

const candidateCheckpointDocumentVersion = 1

var ErrCandidateCheckpointNotFound = errors.New("candidate checkpoint not found")

// CandidateCheckpoint is the non-secret, immutable handoff from dev to publish.
// It binds the candidate to both the local entrypoint and the remote target.
type CandidateCheckpoint struct {
	ProjectPath       string `json:"projectPath"`
	TargetOrigin      string `json:"targetOrigin"`
	TargetID          string `json:"targetId"`
	Environment       string `json:"environment"`
	ProjectID         string `json:"projectId"`
	CandidateID       string `json:"candidateId"`
	CandidateKey      string `json:"candidateKey"`
	CandidateRevision int64  `json:"candidateRevision"`
	ArtifactDigest    string `json:"artifactDigest"`
	ProvenanceDigest  string `json:"provenanceDigest"`
}

type candidateCheckpointDocument struct {
	Version    int                            `json:"version"`
	Candidates map[string]CandidateCheckpoint `json:"candidates"`
}

// CandidateCheckpointStore atomically persists non-secret authoring state.
type CandidateCheckpointStore struct {
	path string
	mu   sync.Mutex
}

func NewCandidateCheckpointStore(path string) *CandidateCheckpointStore {
	return &CandidateCheckpointStore{path: strings.TrimSpace(path)}
}

func (store *CandidateCheckpointStore) Save(checkpoint CandidateCheckpoint) error {
	if store == nil {
		return fmt.Errorf("candidate checkpoint store is required")
	}
	normalized, err := normalizeCandidateCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	lock, err := store.acquireMutationLock()
	if err != nil {
		return err
	}
	defer lock.Release()
	document, err := store.load()
	if err != nil {
		return err
	}
	document.Candidates[candidateCheckpointKey(
		normalized.ProjectPath,
		normalized.TargetOrigin,
		normalized.CandidateKey,
	)] = normalized
	return store.save(document)
}

func (store *CandidateCheckpointStore) Load(projectPath, targetOrigin string) (CandidateCheckpoint, error) {
	return store.LoadCandidate(projectPath, targetOrigin, "default")
}

func (store *CandidateCheckpointStore) LoadCandidate(
	projectPath,
	targetOrigin,
	candidateKey string,
) (CandidateCheckpoint, error) {
	if store == nil {
		return CandidateCheckpoint{}, fmt.Errorf("candidate checkpoint store is required")
	}
	absolute, err := canonicalProjectPath(projectPath)
	if err != nil {
		return CandidateCheckpoint{}, err
	}
	origin, err := canonicalCheckpointOrigin(targetOrigin)
	if err != nil {
		return CandidateCheckpoint{}, err
	}
	candidateKey = normalizeCheckpointCandidateKey(candidateKey)
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := store.load()
	if err != nil {
		return CandidateCheckpoint{}, err
	}
	checkpoint, ok := document.Candidates[candidateCheckpointKey(
		absolute,
		origin,
		candidateKey,
	)]
	if !ok {
		return CandidateCheckpoint{}, ErrCandidateCheckpointNotFound
	}
	normalized, err := normalizeCandidateCheckpoint(checkpoint)
	if err != nil {
		return CandidateCheckpoint{}, fmt.Errorf("stored candidate checkpoint is invalid: %w", err)
	}
	if normalized.ProjectPath != absolute || normalized.TargetOrigin != origin ||
		normalized.CandidateKey != candidateKey {
		return CandidateCheckpoint{}, fmt.Errorf("stored candidate checkpoint identity does not match lookup")
	}
	return normalized, nil
}

func (store *CandidateCheckpointStore) load() (candidateCheckpointDocument, error) {
	document := candidateCheckpointDocument{
		Version:    candidateCheckpointDocumentVersion,
		Candidates: map[string]CandidateCheckpoint{},
	}
	if store.path == "" {
		return candidateCheckpointDocument{}, fmt.Errorf("candidate checkpoint path is required")
	}
	content, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return candidateCheckpointDocument{}, fmt.Errorf("read candidate checkpoints: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return candidateCheckpointDocument{}, fmt.Errorf("decode candidate checkpoints: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return candidateCheckpointDocument{}, fmt.Errorf("decode candidate checkpoints: trailing JSON content")
	}
	if document.Version != candidateCheckpointDocumentVersion {
		return candidateCheckpointDocument{}, fmt.Errorf("unsupported candidate checkpoint version %d", document.Version)
	}
	if document.Candidates == nil {
		document.Candidates = map[string]CandidateCheckpoint{}
	}
	return document, nil
}

func (store *CandidateCheckpointStore) save(document candidateCheckpointDocument) error {
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode candidate checkpoints: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(store.path, content); err != nil {
		return fmt.Errorf("write candidate checkpoints: %w", err)
	}
	return nil
}

func (store *CandidateCheckpointStore) acquireMutationLock() (*instancelock.Lock, error) {
	if store.path == "" {
		return nil, fmt.Errorf("candidate checkpoint path is required")
	}
	return instancelock.AcquireNamed(
		filepath.Dir(store.path),
		"."+filepath.Base(store.path)+".lock",
	)
}

func normalizeCandidateCheckpoint(checkpoint CandidateCheckpoint) (CandidateCheckpoint, error) {
	var err error
	checkpoint.ProjectPath, err = canonicalProjectPath(checkpoint.ProjectPath)
	if err != nil {
		return CandidateCheckpoint{}, err
	}
	checkpoint.TargetOrigin, err = canonicalCheckpointOrigin(checkpoint.TargetOrigin)
	if err != nil {
		return CandidateCheckpoint{}, err
	}
	checkpoint.TargetID = strings.TrimSpace(checkpoint.TargetID)
	checkpoint.Environment = strings.TrimSpace(checkpoint.Environment)
	checkpoint.ProjectID = strings.TrimSpace(checkpoint.ProjectID)
	checkpoint.CandidateID = strings.TrimSpace(checkpoint.CandidateID)
	checkpoint.CandidateKey = normalizeCheckpointCandidateKey(checkpoint.CandidateKey)
	checkpoint.ArtifactDigest = strings.TrimSpace(checkpoint.ArtifactDigest)
	checkpoint.ProvenanceDigest = strings.TrimSpace(checkpoint.ProvenanceDigest)
	if checkpoint.TargetID == "" || checkpoint.Environment == "" ||
		checkpoint.ProjectID == "" || checkpoint.CandidateID == "" ||
		checkpoint.CandidateRevision <= 0 {
		return CandidateCheckpoint{}, fmt.Errorf("candidate checkpoint requires target, environment, project, candidate, and revision")
	}
	if err := digest.ValidateSHA256Identity(checkpoint.ArtifactDigest); err != nil {
		return CandidateCheckpoint{}, fmt.Errorf("candidate artifact digest is invalid: %w", err)
	}
	if err := digest.ValidateSHA256Identity(checkpoint.ProvenanceDigest); err != nil {
		return CandidateCheckpoint{}, fmt.Errorf("candidate provenance digest is invalid: %w", err)
	}
	return checkpoint, nil
}

func canonicalProjectPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("project path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func canonicalCheckpointOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("candidate target origin must be an absolute URL without credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("candidate target origin must not contain a path, query, or fragment")
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func candidateCheckpointKey(projectPath, targetOrigin, candidateKey string) string {
	sum := sha256.Sum256([]byte(
		projectPath + "\x00" + targetOrigin + "\x00" +
			normalizeCheckpointCandidateKey(candidateKey),
	))
	return hex.EncodeToString(sum[:])
}

func normalizeCheckpointCandidateKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	return value
}
