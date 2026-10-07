package saved

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// StableExplorationID derives the retry-stable destination identity shared by
// browser and REST adapters. Authored labels and client-supplied resource IDs
// never participate in identity allocation.
func StableExplorationID(prefix, project, actor, idempotencyKey, operation string) (string, error) {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(project) == "" || strings.TrimSpace(actor) == "" || strings.TrimSpace(idempotencyKey) == "" || strings.TrimSpace(operation) == "" {
		return "", fmt.Errorf("%w: stable saved-exploration identity inputs are required", ErrInvalid)
	}
	digest := sha256.Sum256([]byte("leapview.saved-exploration.id.v1\x00" + project + "\x00" + actor + "\x00" + idempotencyKey + "\x00" + operation))
	return prefix + hex.EncodeToString(digest[:16]), nil
}

// MutationReplayAuthorizationRequest identifies a previously committed
// mutation without carrying a writable mutation input. It is part of the
// public saved-exploration contract because both REST and browser transports
// must reconstruct the same durable retry identity.
type MutationReplayAuthorizationRequest struct {
	ProjectID      projectgraph.ResourceID
	ActorID        string
	Action         MutationAction
	IdempotencyKey string
	Fingerprint    string
	TargetID       ExplorationID
}

func (request MutationReplayAuthorizationRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: replay project id: %v", ErrInvalid, err)
	}
	if strings.TrimSpace(request.ActorID) == "" {
		return fmt.Errorf("%w: replay actor id is required", ErrInvalid)
	}
	if !request.Action.Valid() {
		return fmt.Errorf("%w: replay mutation action is invalid", ErrInvalid)
	}
	if err := request.TargetID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.Fingerprint) == "" {
		return fmt.Errorf("%w: replay identity is incomplete", ErrInvalid)
	}
	return nil
}

// Fingerprint helpers expose the exact durable request identity expected in
// MutationEvidence. Generated revision IDs, timestamps, and evidence are
// deliberately excluded so retries compare caller intent.
func FingerprintCreate(request CreateRequest) (string, error) {
	payload, err := request.ValidatedPayload()
	if err != nil {
		return "", err
	}
	return CanonicalFingerprint(createFingerprint{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility, Payload: payload.Canonical()})
}

func FingerprintUpdate(request UpdateVersionRequest) (string, error) {
	payload, err := request.ValidatedPayload()
	if err != nil {
		return "", err
	}
	return CanonicalFingerprint(updateFingerprint{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID, ExpectedRevision: request.ExpectedRevision, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility, Payload: payload.Canonical()})
}

func FingerprintDuplicate(request DuplicateRequest) (string, error) {
	return CanonicalFingerprint(duplicateFingerprint{ProjectID: request.ProjectID, SourceID: request.SourceID, ExpectedSourceRevision: request.ExpectedSourceRevision, ID: request.ID, ActorID: request.ActorID, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility})
}

func FingerprintArchive(request ArchiveRequest) (string, error) {
	return CanonicalFingerprint(archiveFingerprint{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID, ExpectedRevision: request.ExpectedRevision})
}

type createFingerprint struct {
	ProjectID  projectgraph.ResourceID
	ID         ExplorationID
	ActorID    string
	Title      string
	Slug       string
	Visibility Visibility
	Payload    []byte
}

type updateFingerprint struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	ActorID          string
	ExpectedRevision RevisionToken
	Title            string
	Slug             string
	Visibility       Visibility
	Payload          []byte
}

type duplicateFingerprint struct {
	ProjectID              projectgraph.ResourceID
	SourceID               ExplorationID
	ExpectedSourceRevision RevisionToken
	ID                     ExplorationID
	ActorID                string
	Title                  string
	Slug                   string
	Visibility             Visibility
}

type archiveFingerprint struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	ActorID          string
	ExpectedRevision RevisionToken
}
