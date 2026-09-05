package saved

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxIdempotencyKeyLength = 200
	MaxRequestIDLength      = 256
	MaxCorrelationIDLength  = 256
	MaxAdminReasonLength    = 500
	MutationEvidenceVersion = 1

	mutationFingerprintDomain = "flid.savedexploration.mutation.v1"
)

// MutationAction identifies the durable mutation represented by evidence.
// The action is included in replay lookup so one idempotency key cannot be
// reused to cross mutation kinds.
type MutationAction string

const (
	MutationActionCreate    MutationAction = "create"
	MutationActionUpdate    MutationAction = "update"
	MutationActionDuplicate MutationAction = "duplicate"
	MutationActionArchive   MutationAction = "archive"
)

func (a MutationAction) Valid() bool {
	return a == MutationActionCreate || a == MutationActionUpdate || a == MutationActionDuplicate || a == MutationActionArchive
}

// MutationEvidence is the immutable retry and audit identity for one saved
// exploration mutation. The actor and idempotency key form a scoped identity;
// Fingerprint detects reuse of that identity with a changed request. All
// fields are transport-neutral and safe to persist alongside the mutation.
type MutationEvidence struct {
	Version        uint32         `json:"version"`
	ActorID        string         `json:"actorId"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Fingerprint    string         `json:"fingerprint"`
	Action         MutationAction `json:"action"`
	RequestID      string         `json:"requestId"`
	CorrelationID  string         `json:"correlationId"`
	AdminOverride  bool           `json:"adminOverride"`
	AdminReason    string         `json:"adminReason,omitempty"`
	OccurredAt     time.Time      `json:"occurredAt"`
}

func (e MutationEvidence) IsZero() bool {
	return e.Version == 0 && e.ActorID == "" && e.IdempotencyKey == "" && e.Fingerprint == "" && e.Action == "" && e.RequestID == "" && e.CorrelationID == "" && !e.AdminOverride && e.AdminReason == "" && e.OccurredAt.IsZero()
}

func (e MutationEvidence) Validate() error {
	if e.Version != MutationEvidenceVersion {
		return fmt.Errorf("%w: unsupported mutation evidence version %d", ErrInvalid, e.Version)
	}
	if err := validateSubjectID(e.ActorID, maxOwnerLength, "mutation actor id"); err != nil {
		return err
	}
	if err := validateBoundedText(e.IdempotencyKey, MaxIdempotencyKeyLength, "mutation idempotency key"); err != nil {
		return err
	}
	if !sha256Pattern.MatchString(e.Fingerprint) {
		return fmt.Errorf("%w: mutation fingerprint must be a canonical SHA-256 identity", ErrInvalid)
	}
	if !e.Action.Valid() {
		return fmt.Errorf("%w: unsupported mutation action %q", ErrInvalid, e.Action)
	}
	if err := validateBoundedText(e.RequestID, MaxRequestIDLength, "mutation request id"); err != nil {
		return err
	}
	if err := validateBoundedText(e.CorrelationID, MaxCorrelationIDLength, "mutation correlation id"); err != nil {
		return err
	}
	if e.AdminOverride {
		if err := validateBoundedText(e.AdminReason, MaxAdminReasonLength, "admin override reason"); err != nil {
			return err
		}
	} else if e.AdminReason != "" {
		return fmt.Errorf("%w: admin reason requires admin override", ErrInvalid)
	}
	if e.OccurredAt.IsZero() || e.OccurredAt.Location() != time.UTC {
		return fmt.Errorf("%w: mutation occurredAt must be a non-zero UTC timestamp", ErrInvalid)
	}
	return nil
}

// ScopedIdentity is the exact actor-scoped retry key. It is descriptive only;
// persistence should use separate actor/key columns or equivalent uniqueness
// constraints rather than parsing this string.
func (e MutationEvidence) ScopedIdentity() string {
	return e.ActorID + "\x00" + e.IdempotencyKey
}

// CanonicalFingerprint hashes one canonical JSON request value. Callers should
// pass only the durable mutation request (not query results, SQL, UI state, or
// generated IDs) so retries compare the intended operation.
func CanonicalFingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: encode mutation request: %v", ErrInvalid, err)
	}
	digest := sha256.Sum256(append(append([]byte(mutationFingerprintDomain), 0), encoded...))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// NewMutationEvidence builds validated immutable retry/audit context.
func NewMutationEvidence(actorID string, action MutationAction, idempotencyKey, fingerprint, requestID, correlationID string, occurredAt time.Time) (MutationEvidence, error) {
	evidence := MutationEvidence{
		Version: MutationEvidenceVersion, ActorID: actorID, IdempotencyKey: idempotencyKey,
		Fingerprint: fingerprint, Action: action, RequestID: requestID,
		CorrelationID: correlationID, OccurredAt: occurredAt,
	}
	if err := evidence.Validate(); err != nil {
		return MutationEvidence{}, err
	}
	return evidence, nil
}

func validateBoundedText(value string, max int, kind string) error {
	if value == "" || !utf8.ValidString(value) || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > max || strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%w: invalid %s", ErrInvalid, kind)
	}
	return nil
}

func validateMutationEvidence(evidence MutationEvidence, expected MutationAction) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	if evidence.Action != expected {
		return fmt.Errorf("%w: mutation evidence action %q does not match %q", ErrInvalid, evidence.Action, expected)
	}
	return nil
}
