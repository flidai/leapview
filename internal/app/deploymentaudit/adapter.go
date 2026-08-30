// Package deploymentaudit composes the deployment activation-audit port with
// the Access-owned PostgreSQL audit repository. Keeping this adapter in app
// composition prevents deployment persistence from importing Access storage
// while preserving one caller-owned transaction for activation evidence.
package deploymentaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/jackc/pgx/v5"
)

// Adapter is the concrete composition implementation of deployment's narrow
// activation-audit port. The Access repository is stateless; the adapter is
// likewise safe to share between requests.
type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ deploymentpostgres.ActivationAuditPort = (*Adapter)(nil)

// New returns an adapter backed by the Access-owned immutable audit table.
func New() *Adapter { return &Adapter{audit: accesspostgres.New()} }

// NewWithRepository keeps the Access authority explicit at the composition
// boundary. The audit repository is stateless, so the same adapter can be
// shared by activation and delivery-mutation audit projections.
func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{audit: audit}
}

// AppendActivationAudit appends the canonical Access audit intent in the
// caller-owned transaction and reads it back before returning. Access and
// deployment therefore share exactly one commit/rollback boundary.
func (a *Adapter) AppendActivationAudit(ctx context.Context, tx deploymentpostgres.Tx, input deploymentpostgres.ActivationAuditInput) (deploymentpostgres.AuditEvent, error) {
	if a == nil || a.audit == nil {
		return deploymentpostgres.AuditEvent{}, fmt.Errorf("%w: activation audit adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	intent, err := activationIntent(input)
	if err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	stored, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		return deploymentpostgres.AuditEvent{}, normalize(err, "append")
	}
	if err := validateStored(stored, intent); err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	return mapAuditEvent(stored), nil
}

// GetActivationAudit reads and fully validates the canonical Access audit
// identity for replay. This is intentionally an expected-input read rather
// than a bare lookup: ScopeID, DomainEventID, ActorID, RequestDigest, all
// immutable intent fields, and the payload digest are checked before the
// deployment projection is returned.
func (a *Adapter) GetActivationAudit(ctx context.Context, tx deploymentpostgres.Tx, input deploymentpostgres.ActivationAuditInput) (deploymentpostgres.AuditEvent, error) {
	if a == nil || a.audit == nil {
		return deploymentpostgres.AuditEvent{}, fmt.Errorf("%w: activation audit adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	intent, err := activationIntent(input)
	if err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	stored, err := a.audit.GetAuditEvent(ctx, tx, input.EventID)
	if err != nil {
		return deploymentpostgres.AuditEvent{}, normalize(err, "read")
	}
	if err := validateStored(stored, intent); err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	return mapAuditEvent(stored), nil
}

// AppendMutationAudit appends the canonical Access audit intent for a native
// delivery publication mutation. It deliberately accepts the deployment
// module's capability-neutral projection and forwards the caller-owned
// transaction without beginning, committing, or rolling it back.
func (a *Adapter) AppendMutationAudit(ctx context.Context, tx deploymentpostgres.Tx, input deploymentmodule.NativeDeliveryAuditInput) (deploymentpostgres.AuditEvent, error) {
	if a == nil || a.audit == nil {
		return deploymentpostgres.AuditEvent{}, fmt.Errorf("%w: delivery mutation audit adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	intent, err := mutationIntent(input)
	if err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	stored, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		return deploymentpostgres.AuditEvent{}, normalize(err, "append delivery mutation")
	}
	if err := validateStored(stored, intent); err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	return mapAuditEvent(stored), nil
}

func mutationIntent(input deploymentmodule.NativeDeliveryAuditInput) (access.AuditIntent, error) {
	if input.Outcome != "accepted" {
		return access.AuditIntent{}, fmt.Errorf("%w: delivery mutation audit outcome is not accepted", deploymentpostgres.ErrInvalid)
	}
	return access.AuditIntent{
		EventID: input.AuditID, DomainEventID: input.DomainEventID, ScopeID: input.ScopeID,
		ActorID: input.ActorID, Source: "deployment", Operation: "publication",
		Action: input.Action, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID,
		Outcome: "success", RequestDigest: input.RequestDigest, CorrelationID: input.CorrelationID,
		AggregateKey: input.AggregateKey, AggregateSequence: input.AggregateSequence,
		MetadataJSON: string(input.Metadata),
	}, nil
}

func activationIntent(input deploymentpostgres.ActivationAuditInput) (access.AuditIntent, error) {
	if input.Outcome != "accepted" {
		return access.AuditIntent{}, fmt.Errorf("%w: activation audit outcome is not accepted", deploymentpostgres.ErrInvalid)
	}
	return access.AuditIntent{
		EventID:           input.EventID,
		DomainEventID:     input.DomainEventID,
		ScopeID:           input.ScopeID,
		ActorID:           input.ActorID,
		Source:            "deployment",
		Operation:         "activate",
		Action:            input.Action,
		ResourceKind:      input.ResourceKind,
		ResourceID:        input.ResourceID,
		Outcome:           "success",
		CorrelationID:     input.CorrelationID,
		RequestDigest:     input.RequestDigest,
		AggregateKey:      input.AggregateKey,
		AggregateSequence: input.AggregateSequence,
		MetadataJSON:      string(input.Metadata),
	}, nil
}

func validateStored(stored accesspostgres.Event, expected access.AuditIntent) error {
	canonical, err := expected.Canonicalize()
	if err != nil {
		return err
	}
	digest, err := canonical.PayloadDigest()
	if err != nil {
		return err
	}
	if stored.AuditID != canonical.EventID || stored.DomainEventID != canonical.DomainEventID ||
		stored.ScopeID != canonical.ScopeID || stored.ActorID != canonical.ActorID ||
		stored.PrincipalID != canonical.PrincipalID || stored.Source != canonical.Source ||
		stored.Operation != canonical.Operation || stored.Action != canonical.Action ||
		stored.ResourceKind != canonical.ResourceKind || stored.ResourceID != canonical.ResourceID ||
		stored.Capability != canonical.Capability || stored.Outcome != canonical.Outcome ||
		stored.RequestID != canonical.RequestID || stored.CorrelationID != canonical.CorrelationID ||
		stored.RequestDigest != canonical.RequestDigest || stored.AggregateKey != canonical.AggregateKey ||
		stored.AggregateSequence != canonical.AggregateSequence || !sameJSON(stored.MetadataJSON, canonical.MetadataJSON) ||
		stored.IntentDigest != digest {
		return fmt.Errorf("%w: activation audit canonical identity differs", deploymentpostgres.ErrConflict)
	}
	return nil
}

func sameJSON(left, right string) bool {
	var a, b any
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil {
		return false
	}
	leftCanonical, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(b)
	return err == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func mapAuditEvent(stored accesspostgres.Event) deploymentpostgres.AuditEvent {
	outcome := stored.Outcome
	if outcome == "success" {
		outcome = "accepted"
	}
	return deploymentpostgres.AuditEvent{
		AuditID: stored.AuditID, EventID: stored.DomainEventID, ScopeID: stored.ScopeID,
		ActorID: stored.ActorID, Action: stored.Action, ResourceKind: stored.ResourceKind,
		ResourceID: stored.ResourceID, Outcome: outcome, RequestDigest: stored.RequestDigest,
		Metadata: []byte(stored.MetadataJSON), OccurredAt: stored.OccurredAt,
	}
}

func normalize(err error, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, deploymentpostgres.ErrConflict) {
		return err
	}
	if errors.Is(err, access.ErrAuditIntentConflict) || errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: activation audit %s identity differs", deploymentpostgres.ErrConflict, operation)
	}
	return err
}
