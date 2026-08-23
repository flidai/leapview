package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/flidai/leapview/internal/platform/transaction"
)

const MaxAuditIntentMetadataBytes = 64 * 1024

// MaxUndeliveredAuditIntents is the fail-closed local backlog bound. Delivered
// handoff rows are governed by retention and do not consume this capacity.
const MaxUndeliveredAuditIntents = 100_000

var (
	ErrAuditIntentConflict = errors.New("audit intent conflicts with durable state")
	ErrAuditIntentFence    = errors.New("audit intent lease fence is stale")
	ErrAuditOutboxCapacity = errors.New("audit outbox undelivered capacity is exhausted")
	ErrAuditOutboxNotFound = errors.New("audit outbox event was not found")
)

// AuditIntent is the canonical, non-secret handoff committed by a source
// capability in the same transaction as its security-relevant mutation.
type AuditIntent struct {
	EventID           string
	Source            string
	Operation         string
	PrincipalID       string
	Action            string
	ResourceKind      string
	ResourceID        string
	Capability        Capability
	Outcome           string
	RequestID         string
	CorrelationID     string
	AggregateKey      string
	AggregateSequence int64
	MetadataJSON      string
}

// AuditIntentRecorder is an Access-owned policy port that accepts the
// transaction owned by the source capability. Implementations must not commit
// or roll back the caller's transaction.
type AuditIntentRecorder interface {
	RecordAuditIntent(context.Context, transaction.Transaction, AuditIntent) error
}

type AuditIntentRecorderFunc func(context.Context, transaction.Transaction, AuditIntent) error

func (f AuditIntentRecorderFunc) RecordAuditIntent(ctx context.Context, tx transaction.Transaction, intent AuditIntent) error {
	return f(ctx, tx, intent)
}

type AuditIntentState string

const (
	AuditIntentPending     AuditIntentState = "pending"
	AuditIntentRetry       AuditIntentState = "retry"
	AuditIntentLeased      AuditIntentState = "leased"
	AuditIntentDelivered   AuditIntentState = "delivered"
	AuditIntentPoison      AuditIntentState = "poison"
	AuditIntentQuarantined AuditIntentState = "quarantined"
)

type AuditIntentLease struct {
	Intent          AuditIntent
	State           AuditIntentState
	AttemptCount    int
	LeaseOwner      string
	LeaseGeneration int64
	LeaseExpiresAt  time.Time
	CreatedAt       time.Time
}

type AuditOutboxStats struct {
	Pending, Retry, Leased, Delivered, Poison, Quarantined int64
	OldestUndeliveredAge                                   time.Duration
	// AttemptCount is the bounded aggregate of persisted delivery attempts.
	// Capacity and CapacityRemaining deliberately describe only the local
	// undelivered queue; they never expose event or actor dimensions.
	AttemptCount      int64
	Capacity          int64
	CapacityRemaining int64
}

// MaxAuditOutboxInspectionRows bounds operator output and protects the
// offline command from turning a terminal backlog into an unbounded export.
const MaxAuditOutboxInspectionRows = 100

// AuditOutboxTerminalIntent is the safe operator view of one terminal intent.
// It intentionally omits the immutable payload and metadata. PayloadDigest
// lets an operator bind a recovery command to the exact stored payload without
// copying sensitive fields into a shell history or incident ticket.
type AuditOutboxTerminalIntent struct {
	EventID           string
	State             AuditIntentState
	AttemptCount      int
	LastErrorCode     string
	PayloadDigest     string
	AggregateKey      string
	AggregateSequence int64
	LeaseGeneration   int64
	CreatedAt         time.Time
}

type AuditOutboxInspection struct {
	Stats     AuditOutboxStats
	Terminals []AuditOutboxTerminalIntent
	Truncated bool
}

// AuditOutboxRequeueRequest provides optional compare-and-swap guards for a
// recovery operation. EventID is always required; omitted optional guards
// preserve the original state-protected API for trusted callers.
type AuditOutboxRequeueRequest struct {
	EventID               string
	ExpectedState         AuditIntentState
	ExpectedAttempts      *int
	ExpectedFailureCode   string
	ExpectedPayloadDigest string
}

// AuditOutboxOperator is the least-privilege operator surface. It is separate
// from AuditOutboxStore so worker implementations and producer packages do not
// gain inspection or recovery authority accidentally.
type AuditOutboxOperator interface {
	InspectAuditOutbox(context.Context, time.Time, int) (AuditOutboxInspection, error)
	RequeueAuditIntentExact(context.Context, AuditOutboxRequeueRequest) error
}

// AuditOutboxStore is the durable worker and operator boundary. Complete must
// atomically materialize the final Access event and fence the leased intent.
type AuditOutboxStore interface {
	ClaimAuditIntent(context.Context, string, time.Duration) (AuditIntentLease, bool, error)
	CompleteAuditIntent(context.Context, AuditIntentLease) error
	RetryAuditIntent(context.Context, AuditIntentLease, time.Time, string) error
	PoisonAuditIntent(context.Context, AuditIntentLease, string) error
	QuarantineAuditIntent(context.Context, AuditIntentLease, string) error
	RequeueAuditIntent(context.Context, string) error
	AuditOutboxStats(context.Context, time.Time) (AuditOutboxStats, error)
}

// AuditStore is the module-owned durable audit surface used by process
// composition. Producer capabilities receive only AuditIntentRecorder, while
// lifecycle and operator wiring may use the worker or recovery subsets.
type AuditStore interface {
	AuditIntentRecorder
	AuditOutboxStore
	AuditOutboxOperator
}

func (intent AuditIntent) Canonicalize() (AuditIntent, error) {
	if !canonicalAuditIntentEventID(intent.EventID) {
		return AuditIntent{}, fmt.Errorf("audit intent event id is not canonical")
	}
	for name, value := range map[string]string{
		"source": intent.Source, "operation": intent.Operation, "action": intent.Action,
		"outcome": intent.Outcome, "aggregate key": intent.AggregateKey,
	} {
		if !canonicalAuditIntentLiteral(value, auditIntentLimit(name)) {
			return AuditIntent{}, fmt.Errorf("audit intent %s is not canonical", name)
		}
	}
	for name, value := range map[string]string{
		"principal id":  intent.PrincipalID,
		"resource kind": intent.ResourceKind,
		"resource id":   intent.ResourceID,
	} {
		if !optionalCanonicalAuditIntentLiteral(value, auditIntentLimit(name)) {
			return AuditIntent{}, fmt.Errorf("audit intent %s is not canonical", name)
		}
	}
	if intent.RequestID != strings.TrimSpace(intent.RequestID) || len(intent.RequestID) > 256 ||
		intent.CorrelationID != strings.TrimSpace(intent.CorrelationID) || len(intent.CorrelationID) > 256 {
		return AuditIntent{}, fmt.Errorf("audit intent request identity is not canonical")
	}
	if intent.AggregateSequence < 0 {
		return AuditIntent{}, fmt.Errorf("audit intent aggregate sequence cannot be negative")
	}
	if intent.Capability != "" {
		if err := intent.Capability.Validate(); err != nil {
			return AuditIntent{}, fmt.Errorf("audit intent capability: %w", err)
		}
	}
	metadata, err := canonicalAuditIntentMetadata(intent.MetadataJSON)
	if err != nil {
		return AuditIntent{}, err
	}
	intent.MetadataJSON = metadata
	return intent, nil
}

func (intent AuditIntent) PayloadDigest() (string, error) {
	canonical, err := intent.Canonicalize()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalAuditIntentMetadata(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	if len(raw) > MaxAuditIntentMetadataBytes {
		return "", fmt.Errorf("audit intent metadata exceeds %d bytes", MaxAuditIntentMetadataBytes)
	}
	if err := rejectDuplicateCanonicalJSONKeys([]byte(raw)); err != nil {
		return "", fmt.Errorf("audit intent metadata: %w", err)
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		if err == nil {
			err = errors.New("metadata must be a JSON object")
		}
		return "", fmt.Errorf("audit intent metadata: %w", err)
	}
	if key, ok := unsafeAuditMetadataKey(value); ok {
		return "", fmt.Errorf("audit intent metadata key %q is not permitted", key)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("audit intent metadata has trailing data")
		}
		return "", fmt.Errorf("audit intent metadata: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("audit intent metadata: %w", err)
	}
	if len(encoded) > MaxAuditIntentMetadataBytes {
		return "", fmt.Errorf("audit intent metadata exceeds %d bytes", MaxAuditIntentMetadataBytes)
	}
	return string(encoded), nil
}

var deniedAuditMetadataKeys = map[string]struct{}{
	"authorization": {},
	"bearertoken":   {},
	"cookie":        {},
	"password":      {},
	"passwordhash":  {},
	"prompt":        {},
	"providervalue": {},
	"querytext":     {},
	"rawsql":        {},
	"refreshtoken":  {},
	"secret":        {},
	"secretvalue":   {},
	"accesstoken":   {},
}

func unsafeAuditMetadataKey(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := normalizeAuditMetadataKey(key)
			if _, denied := deniedAuditMetadataKeys[normalized]; denied {
				return key, true
			}
			if nested, denied := unsafeAuditMetadataKey(child); denied {
				return nested, true
			}
		}
	case []any:
		for _, child := range typed {
			if nested, denied := unsafeAuditMetadataKey(child); denied {
				return nested, true
			}
		}
	}
	return "", false
}

func normalizeAuditMetadataKey(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			return unicode.ToLower(char)
		}
		return -1
	}, strings.TrimSpace(value))
}

func auditIntentLimit(name string) int {
	switch name {
	case "principal id", "action", "resource id":
		return 256
	case "outcome":
		return 64
	case "aggregate key":
		return 512
	default:
		return 128
	}
}

func canonicalAuditIntentLiteral(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
}

func canonicalAuditIntentEventID(value string) bool {
	if !canonicalAuditIntentLiteral(value, 128) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:/-", char) {
			continue
		}
		return false
	}
	return true
}

func optionalCanonicalAuditIntentLiteral(value string, limit int) bool {
	return value == "" || canonicalAuditIntentLiteral(value, limit)
}
