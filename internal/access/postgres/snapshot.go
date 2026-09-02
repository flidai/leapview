package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// InstallAuthorizationSnapshotTx installs one immutable, graph-bound
// authorization snapshot in the caller-owned pgx transaction. Replaying the
// same identity with the same digest is idempotent; a different digest fails.
func InstallAuthorizationSnapshotTx(ctx context.Context, tx Tx, snapshot accesssnapshot.AuthorizationSnapshot) error {
	if tx == nil {
		return errors.New("authorization snapshot PostgreSQL transaction is required")
	}
	if err := snapshot.ValidateBound(); err != nil {
		return fmt.Errorf("validate authorization snapshot: %w", err)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return fmt.Errorf("digest authorization snapshot: %w", err)
	}
	identity := snapshot.Identity()
	project, environment, generation := identity.ProjectID.String(), identity.Environment, identity.GenerationID
	tag, err := tx.Exec(ctx, `INSERT INTO access.authorization_snapshot(project_id,environment,generation_id,digest) VALUES($1,$2,$3,$4) ON CONFLICT(project_id,environment,generation_id) DO NOTHING`, project, environment, generation, digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var installed string
		if err := tx.QueryRow(ctx, `SELECT digest FROM access.authorization_snapshot WHERE project_id=$1 AND environment=$2 AND generation_id=$3`, project, environment, generation).Scan(&installed); err != nil {
			return err
		}
		if installed != digest {
			return fmt.Errorf("%w: project=%s environment=%s generation=%s", ErrAuthorizationSnapshotIdentityConflict, project, environment, generation)
		}
		return nil
	}
	for _, item := range snapshot.RoleBindings() {
		caps := make([]string, 0, len(item.Capabilities))
		for _, capability := range item.Capabilities {
			caps = append(caps, capability.String())
		}
		encoded, err := json.Marshal(caps)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO access.authorization_role_binding(id,project_id,environment,generation_id,subject_kind,subject_id,role,capabilities,name) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)`, item.ID, project, environment, generation, string(item.Subject.Kind), item.Subject.ID, string(item.Role), encoded, item.Name); err != nil {
			return err
		}
	}
	for _, item := range snapshot.Grants() {
		canonical := item.Canonical
		if _, err = tx.Exec(ctx, `INSERT INTO access.authorization_grant(id,project_id,environment,generation_id,subject_kind,subject_id,resource_id,resource_kind,capability,name) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, item.ID, project, environment, generation, string(canonical.Subject().Kind), canonical.Subject().ID, canonical.Resource().ID().String(), string(canonical.Resource().Kind()), canonical.Capability().String(), item.Name); err != nil {
			return err
		}
	}
	for _, item := range snapshot.DataPolicies() {
		var subjectKind, subjectID any
		if item.Subject != nil {
			subjectKind, subjectID = string(item.Subject.Kind), item.Subject.ID
		}
		if _, err = tx.Exec(ctx, `INSERT INTO access.authorization_data_policy(id,project_id,environment,generation_id,resource_id,resource_kind,subject_kind,subject_id,policy_type,expression) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)`, item.ID, project, environment, generation, item.Resource.ID().String(), string(item.Resource.Kind()), subjectKind, subjectID, item.PolicyType, item.ExpressionJSON); err != nil {
			return err
		}
	}
	return nil
}

// ActivateDashboardPublicationPrincipalTx creates the deterministic identity
// used by anonymous publication. The UUID is derived from the canonical
// project/publication key and remains stable across process restarts.
func ActivateDashboardPublicationPrincipalTx(ctx context.Context, tx Tx, projectID projectgraph.ResourceID, name string) error {
	if tx == nil {
		return errors.New("dashboard publication principal PostgreSQL transaction is required")
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("dashboard publication principal project: %w", err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("dashboard publication principal requires name")
	}
	if len(name) > 512 {
		return errors.New("dashboard publication principal name exceeds 512 bytes")
	}
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("dashboard_publication:"+projectID.String()+"."+name)).String()
	_, err := tx.Exec(ctx, `INSERT INTO access.principal(id,principal_type,status,email,display_name) VALUES($1,'dashboard_publication','active','',$2) ON CONFLICT(id) DO UPDATE SET display_name=EXCLUDED.display_name,updated_at=clock_timestamp()`, id, name)
	return err
}
