// Package dashboardappearanceaudit adapts dashboard-appearance audit rows to
// Access' canonical PostgreSQL audit authority.
package dashboardappearanceaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	appearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	"github.com/jackc/pgx/v5"
)

type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ appearancepostgres.AuditPort = (*Adapter)(nil)

func New() *Adapter { return &Adapter{audit: accesspostgres.New()} }

func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{audit: audit}
}

// Matches proves this adapter is bound to the exact Access audit authority
// allocated by application composition.
func (a *Adapter) Matches(audit *accesspostgres.AuditRepository) bool {
	return a != nil && a.audit != nil && a.audit == audit
}

// RecordAuditEvent translates the appearance projection into Access' canonical
// audit intent, appends it through the exact caller transaction, and validates
// the complete immutable row returned by Access.
func (a *Adapter) RecordAuditEvent(ctx context.Context, tx appearancepostgres.Tx, input appearancepostgres.AuditInput) error {
	if a == nil || a.audit == nil {
		return appearancepostgres.ErrAuditMissing
	}
	if tx == nil {
		return errors.New("dashboard appearance audit transaction is required")
	}
	intent := access.AuditIntent{
		EventID: input.AuditID, DomainEventID: input.DomainEventID, ActorID: input.ActorID,
		ScopeID: input.ProjectID, Source: "dashboard.appearance", Operation: "dashboard.appearance.update",
		Action: input.Action, ResourceKind: "dashboard", ResourceID: input.DashboardID,
		Capability: access.CapabilityResourceManage, Outcome: "success",
		AggregateKey:      "dashboard_appearance:" + input.ProjectID + ":" + input.DashboardID,
		AggregateSequence: input.AggregateSequence, MetadataJSON: input.MetadataJSON,
	}
	stored, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		if errors.Is(err, access.ErrAuditIntentConflict) || errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: dashboard appearance audit identity differs", appearancepostgres.ErrConflict)
		}
		return err
	}
	if err := validateStored(stored, intent); err != nil {
		return fmt.Errorf("%w: dashboard appearance audit canonical identity differs", appearancepostgres.ErrConflict)
	}
	return nil
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
		stored.Source != canonical.Source || stored.Operation != canonical.Operation ||
		stored.Action != canonical.Action || stored.ResourceKind != canonical.ResourceKind ||
		stored.ResourceID != canonical.ResourceID || stored.Capability != canonical.Capability ||
		stored.Outcome != canonical.Outcome || stored.AggregateKey != canonical.AggregateKey ||
		stored.AggregateSequence != canonical.AggregateSequence || !sameJSON(stored.MetadataJSON, canonical.MetadataJSON) ||
		stored.IntentDigest != digest {
		return errors.New("stored audit projection differs")
	}
	return nil
}

func sameJSON(left, right string) bool {
	var l, r any
	if json.Unmarshal([]byte(left), &l) != nil || json.Unmarshal([]byte(right), &r) != nil {
		return false
	}
	lb, err := json.Marshal(l)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(r)
	return err == nil && bytes.Equal(lb, rb)
}
