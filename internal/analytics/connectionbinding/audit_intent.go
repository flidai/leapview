package connectionbinding

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/google/uuid"
)

// AdministrationAuditInvocation carries transport identity into a
// connection-binding mutation.  The command producer is responsible for
// validating the generated operation contract before building the intent;
// this package owns the durable identity and context handoff used by the
// source repository.
type AdministrationAuditInvocation struct {
	OperationID    string
	PrincipalID    string
	RequestID      string
	CorrelationID  string
	IdempotencyKey string
}

// WithAuditIntent carries a source-built audit intent to the connection
// binding repository.  The repository owns the SQLite transaction and records
// the intent before committing the binding mutation.
func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

// AuditIntentFromContext returns the transaction-scoped audit intent, when a
// command producer supplied one.
func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	if ctx == nil {
		return access.AuditIntent{}, false
	}
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}

type auditIntentContextKey struct{}

// BuildConnectionAdministrationAuditIntent builds the non-secret Access
// handoff for a successful binding administration mutation.  Event identity
// is derived from the durable binding aggregate and revision rather than a
// request UUID, so a retry of the same committed mutation is idempotent even
// when a transport does not provide an idempotency key.
func BuildConnectionAdministrationAuditIntent(
	invocation AdministrationAuditInvocation,
	event AdministrationAuditEvent,
) (access.AuditIntent, error) {
	operationID, ok := administrationOperationForAction(event.Action)
	if !ok {
		return access.AuditIntent{}, fmt.Errorf("connection administration action %q has no generated operation", event.Action)
	}
	operation := strings.TrimSpace(invocation.OperationID)
	if operation == "" {
		operation = operationID
	}
	if operation != operationID {
		return access.AuditIntent{}, fmt.Errorf("connection administration operation %q does not match action %q", operation, event.Action)
	}
	if event.BindingID.String() == "" || event.Revision <= 0 {
		return access.AuditIntent{}, fmt.Errorf("connection administration binding identity and positive revision are required")
	}
	if strings.TrimSpace(string(event.Outcome)) == "" {
		return access.AuditIntent{}, fmt.Errorf("connection administration outcome is required")
	}
	if err := event.ProjectID.Validate(); err != nil {
		return access.AuditIntent{}, fmt.Errorf("connection administration project identity: %w", err)
	}
	if err := event.ConnectionID.Validate(); err != nil {
		return access.AuditIntent{}, err
	}
	if _, err := ParseTargetID(event.TargetID.String()); err != nil {
		return access.AuditIntent{}, err
	}
	if _, err := ParseBindingID(event.BindingID.String()); err != nil {
		return access.AuditIntent{}, err
	}

	aggregateKey := "connection_binding:" + event.BindingID.String()
	identity := strings.Join([]string{aggregateKey, operation, fmt.Sprintf("%d", event.Revision)}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	// Access's PostgreSQL audit authority stores audit_id as uuid. Derive a
	// replay-stable RFC 9562 version-8 UUID from the aggregate identity;
	// retaining the aggregate/revision fields keeps the canonical payload
	// independently inspectable while retries map to one audit row.
	eventID := uuid.UUID(digest[:16])
	eventID[6] = (eventID[6] & 0x0f) | 0x80 // version 8 (custom)
	eventID[8] = (eventID[8] & 0x3f) | 0x80 // RFC 9562 variant
	metadata, err := connectionAdministrationAuditMetadata(event)
	if err != nil {
		return access.AuditIntent{}, err
	}
	principal := strings.TrimSpace(invocation.PrincipalID)
	if principal == "" {
		principal = strings.TrimSpace(event.Actor)
	}
	return access.AuditIntent{
		EventID:           eventID.String(),
		Source:            "analytics.connectionbinding",
		Operation:         operation,
		PrincipalID:       principal,
		Action:            string(event.Action),
		ResourceKind:      "connection",
		ResourceID:        event.ConnectionID.String(),
		Capability:        access.CapabilityResourceManage,
		Outcome:           string(event.Outcome),
		RequestID:         strings.TrimSpace(invocation.RequestID),
		CorrelationID:     strings.TrimSpace(invocation.CorrelationID),
		AggregateKey:      aggregateKey,
		AggregateSequence: event.Revision,
		MetadataJSON:      metadata,
	}, nil
}

// BuildAdministrationAuditIntent is kept as a concise alias for command
// adapters that already operate in the connectionbinding package namespace.
func BuildAdministrationAuditIntent(
	invocation AdministrationAuditInvocation,
	event AdministrationAuditEvent,
) (access.AuditIntent, error) {
	return BuildConnectionAdministrationAuditIntent(invocation, event)
}

func administrationOperationForAction(action AdministrationAuditAction) (string, bool) {
	switch action {
	case AuditBindingCreated:
		return "createTargetConnectionBinding", true
	case AuditBindingUpdated:
		return "updateTargetConnectionBinding", true
	case AuditBindingEnabled:
		return "enableTargetConnectionBinding", true
	case AuditBindingDisabled:
		return "disableTargetConnectionBinding", true
	default:
		return "", false
	}
}

// connectionAdministrationAuditMetadata mirrors the generated TypeSpec audit
// envelope while intentionally retaining only graph/binding identities and a
// revision. Endpoint and credential-reference values never enter the outbox.
func connectionAdministrationAuditMetadata(event AdministrationAuditEvent) (string, error) {
	metadata := struct {
		SchemaVersion int    `json:"schemaVersion"`
		Retention     string `json:"retention"`
		PayloadSchema string `json:"payloadSchema"`
		Payload       struct {
			BindingID         string `json:"bindingId"`
			TargetID          string `json:"targetId"`
			LogicalConnection string `json:"logicalConnection"`
			Revision          int64  `json:"revision"`
		} `json:"payload"`
	}{SchemaVersion: 1, Retention: "security", PayloadSchema: "TargetConnectionAdministrationAuditPayload"}
	metadata.Payload.BindingID = event.BindingID.String()
	metadata.Payload.TargetID = event.TargetID.String()
	metadata.Payload.LogicalConnection = event.ConnectionID.String()
	metadata.Payload.Revision = event.Revision
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("encode connection administration audit metadata: %w", err)
	}
	return string(encoded), nil
}
