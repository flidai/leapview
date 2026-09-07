package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

var errSemanticDecisionAuditVerification = errors.New("semantic decision audit verification failed")

// ReadSemanticDecisionAuditEvent reconstructs only the retained semantic
// decision contract. It reads the existing canonical row, re-canonicalizes
// the redacted envelope, and verifies the existing intent digest before
// returning anything to a caller. No new read store or digest is introduced.
func (r *Repository) ReadSemanticDecisionAuditEvent(ctx context.Context, auditID string) (access.CanonicalAuditEvent, error) {
	if ctx == nil {
		return access.CanonicalAuditEvent{}, errors.New("semantic decision audit context is nil")
	}
	db, err := r.requireDB()
	if err != nil {
		return access.CanonicalAuditEvent{}, err
	}
	id, err := canonicalUUID("audit event id", auditID)
	if err != nil {
		return access.CanonicalAuditEvent{}, err
	}
	var projectID, environment, generationID string
	var principalID, source, operation, action, resourceKind, resourceID string
	var capability, outcome, requestID, correlationID, metadata, intentDigest string
	err = db.QueryRow(ctx, `SELECT COALESCE(project_id, ''), COALESCE(environment, ''), COALESCE(generation_id, ''),
 COALESCE(principal_id::text, ''), source, operation, action, COALESCE(resource_kind, ''),
 COALESCE(resource_id, ''), capability, outcome, COALESCE(request_id::text, ''),
 COALESCE(correlation_id::text, ''), metadata::text, intent_digest
 FROM audit.audit_event WHERE audit_id = $1::uuid`, id).Scan(
		&projectID, &environment, &generationID, &principalID, &source, &operation, &action,
		&resourceKind, &resourceID, &capability, &outcome, &requestID, &correlationID,
		&metadata, &intentDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.CanonicalAuditEvent{}, fmt.Errorf("semantic decision audit event %s not found: %w", id, err)
	}
	if err != nil {
		return access.CanonicalAuditEvent{}, fmt.Errorf("read semantic decision audit event: %w", err)
	}
	if source != "access" || operation != "authorization" || action != access.SemanticDecisionAuditAction {
		return access.CanonicalAuditEvent{}, fmt.Errorf("%w: row is not a semantic access decision", errSemanticDecisionAuditVerification)
	}
	if projectID == "" || environment == "" || generationID == "" || principalID == "" || resourceID == "" {
		return access.CanonicalAuditEvent{}, fmt.Errorf("%w: canonical serving binding is incomplete", errSemanticDecisionAuditVerification)
	}
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), environment, generationID)
	if err != nil {
		return access.CanonicalAuditEvent{}, fmt.Errorf("%w: serving identity: %v", errSemanticDecisionAuditVerification, err)
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID(resourceID), projectgraph.Kind(resourceKind))
	if err != nil {
		return access.CanonicalAuditEvent{}, fmt.Errorf("%w: resource: %v", errSemanticDecisionAuditVerification, err)
	}
	evidence, err := access.DecodeSemanticDecisionEvidence(metadata)
	if err != nil {
		return access.CanonicalAuditEvent{}, fmt.Errorf("%w: metadata: %v", errSemanticDecisionAuditVerification, err)
	}
	canonicalMetadata, err := evidence.MetadataJSON()
	if err != nil {
		return access.CanonicalAuditEvent{}, fmt.Errorf("%w: canonical metadata: %v", errSemanticDecisionAuditVerification, err)
	}
	event := access.CanonicalAuditEvent{
		Identity: identity, PrincipalID: principalID, Action: action, Resource: resource,
		Capability: access.Capability(capability), Status: outcome, RequestID: requestID,
		CorrelationID: correlationID, MetadataJSON: canonicalMetadata,
	}
	if err := event.Validate(); err != nil {
		return access.CanonicalAuditEvent{}, fmt.Errorf("%w: canonical event: %v", errSemanticDecisionAuditVerification, err)
	}
	if strings.TrimSpace(intentDigest) == "" || canonicalAuditDigest(event, canonicalMetadata) != intentDigest {
		return access.CanonicalAuditEvent{}, fmt.Errorf("%w: intent digest does not match retained identity and evidence", errSemanticDecisionAuditVerification)
	}
	return event, nil
}

// VerifySemanticDecisionAuditEvent verifies that a retained event is the exact
// event expected by a caller. The expected event is an already-bound
// CanonicalAuditEvent supplied by the caller; this method does not read live
// registry, control, or lifecycle state. Historical reads remain valid after
// those authorities advance, while a caller with an exact expected binding can
// reject substitution of another valid retained event.
func (r *Repository) VerifySemanticDecisionAuditEvent(ctx context.Context, auditID string, expected access.CanonicalAuditEvent) error {
	if expected.Action != access.SemanticDecisionAuditAction {
		return fmt.Errorf("%w: expected event action is not semantic access", errSemanticDecisionAuditVerification)
	}
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("%w: expected canonical event: %v", errSemanticDecisionAuditVerification, err)
	}
	canonicalMetadata, err := expected.CanonicalMetadataJSON()
	if err != nil {
		return fmt.Errorf("%w: expected canonical metadata: %v", errSemanticDecisionAuditVerification, err)
	}
	expected.MetadataJSON = canonicalMetadata

	actual, err := r.ReadSemanticDecisionAuditEvent(ctx, auditID)
	if err != nil {
		return err
	}
	if actual.Identity != expected.Identity || actual.PrincipalID != expected.PrincipalID ||
		actual.Action != expected.Action || actual.Resource.ID() != expected.Resource.ID() ||
		actual.Resource.Kind() != expected.Resource.Kind() || actual.Capability != expected.Capability ||
		actual.Status != expected.Status || actual.RequestID != expected.RequestID ||
		actual.CorrelationID != expected.CorrelationID || actual.MetadataJSON != expected.MetadataJSON {
		return fmt.Errorf("%w: retained event does not match expected identity and evidence", errSemanticDecisionAuditVerification)
	}
	return nil
}
