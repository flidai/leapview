package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/platform/transaction"
)

var ErrAuthorizationSnapshotIdentityConflict = errors.New("authorization snapshot identity already installed with a different digest")

// InstallAuthorizationSnapshotTx atomically installs one immutable canonical
// authorization snapshot. A generation identity is write-once: reinstalling
// the same digest is a no-op, while a different digest for the same identity
// is rejected. The caller owns the transaction and may combine installation
// with serving-state activation.
func InstallAuthorizationSnapshotTx(ctx context.Context, tx transaction.Transaction, snapshot accesssnapshot.AuthorizationSnapshot) error {
	if tx == nil {
		return errors.New("authorization snapshot transaction is required")
	}
	if err := snapshot.ValidateBound(); err != nil {
		return fmt.Errorf("validate authorization snapshot: %w", err)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return fmt.Errorf("digest authorization snapshot: %w", err)
	}
	identity := snapshot.Identity()
	result, err := tx.ExecContext(ctx, `
INSERT INTO authorization_snapshots (project_id, environment, generation_id, digest)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(project_id, environment, generation_id) DO NOTHING`, identity.ProjectID.String(), identity.Environment, identity.GenerationID, digest)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		var installedDigest string
		if err := tx.QueryRowContext(ctx, `
SELECT digest FROM authorization_snapshots
WHERE project_id = ? AND environment = ? AND generation_id = ?`,
			identity.ProjectID.String(), identity.Environment, identity.GenerationID).Scan(&installedDigest); err != nil {
			return err
		}
		if installedDigest != digest {
			return fmt.Errorf("%w: project=%s environment=%s generation=%s", ErrAuthorizationSnapshotIdentityConflict, identity.ProjectID, identity.Environment, identity.GenerationID)
		}
		return nil
	}
	for _, item := range snapshot.RoleBindings() {
		capabilities := make([]string, 0, len(item.Capabilities))
		for _, capability := range item.Capabilities {
			capabilities = append(capabilities, capability.String())
		}
		encoded, err := json.Marshal(capabilities)
		if err != nil {
			return fmt.Errorf("encode role binding %q capabilities: %w", item.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO authorization_role_bindings
 (id, project_id, environment, generation_id, subject_kind, subject_id, role, capabilities_json, name)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, identity.ProjectID.String(), identity.Environment, identity.GenerationID,
			string(item.Subject.Kind), item.Subject.ID, string(item.Role), string(encoded), item.Name); err != nil {
			return err
		}
	}
	for _, item := range snapshot.Grants() {
		canonical := item.Canonical
		if _, err := tx.ExecContext(ctx, `
INSERT INTO authorization_grants
 (id, project_id, environment, generation_id, subject_kind, subject_id, resource_id, resource_kind, capability, name)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, identity.ProjectID.String(), identity.Environment, identity.GenerationID,
			string(canonical.Subject().Kind), canonical.Subject().ID, canonical.Resource().ID().String(), string(canonical.Resource().Kind()), canonical.Capability().String(), item.Name); err != nil {
			return err
		}
	}
	for _, item := range snapshot.DataPolicies() {
		var subjectKind, subjectID any
		if item.Subject != nil {
			subjectKind, subjectID = string(item.Subject.Kind), item.Subject.ID
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO authorization_data_policies
 (id, project_id, environment, generation_id, resource_id, resource_kind, subject_kind, subject_id, policy_type, expression_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, identity.ProjectID.String(), identity.Environment, identity.GenerationID,
			item.Resource.ID().String(), string(item.Resource.Kind()), subjectKind, subjectID, item.PolicyType, item.ExpressionJSON); err != nil {
			return err
		}
	}
	return nil
}

// InstallAuthorizationSnapshot installs an immutable snapshot in its own
// transaction.
func (r *Repository) InstallAuthorizationSnapshot(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot) error {
	if r == nil || r.root == nil {
		return errors.New("access repository database is required")
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := InstallAuthorizationSnapshotTx(ctx, tx, snapshot); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordCanonicalAuditEvent persists an event for the exact installed
// project/environment/generation snapshot. Global identity audit events use
// the separate global recorder and never fill this scope from a route value.
func (r *Repository) RecordCanonicalAuditEvent(ctx context.Context, event access.CanonicalAuditEvent) error {
	if r == nil || r.root == nil {
		return errors.New("access repository database is required")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	id, err := newID("audit")
	if err != nil {
		return err
	}
	metadata := event.MetadataJSON
	if metadata == "" {
		metadata = "{}"
	}
	_, err = r.root.ExecContext(ctx, `
INSERT INTO authorization_audit_events
 (id, project_id, environment, generation_id, principal_id, action, resource_id, resource_kind, capability, status, request_id, correlation_id, metadata_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, event.Identity.ProjectID.String(), event.Identity.Environment, event.Identity.GenerationID,
		event.PrincipalID, event.Action, event.Resource.ID().String(), string(event.Resource.Kind()), event.Capability.String(),
		event.Status, event.RequestID, event.CorrelationID, metadata)
	return err
}
