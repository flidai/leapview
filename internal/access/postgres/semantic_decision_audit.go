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
