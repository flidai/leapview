package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
	tag, err := accessdb.New(tx).InsertAuthorizationSnapshot(ctx, accessdb.InsertAuthorizationSnapshotParams{ProjectID: project, Environment: environment, GenerationID: generation, Digest: digest})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		installed, err := accessdb.New(tx).GetAuthorizationSnapshotDigest(ctx, accessdb.GetAuthorizationSnapshotDigestParams{ProjectID: project, Environment: environment, GenerationID: generation})
		if err != nil {
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
		if err = accessdb.New(tx).InsertAuthorizationRoleBinding(ctx, accessdb.InsertAuthorizationRoleBindingParams{ID: item.ID, ProjectID: project, Environment: environment, GenerationID: generation, SubjectKind: string(item.Subject.Kind), SubjectID: item.Subject.ID, Role: string(item.Role), Capabilities: encoded, Name: item.Name}); err != nil {
			return err
		}
	}
	for _, item := range snapshot.Grants() {
		canonical := item.Canonical
		if err = accessdb.New(tx).InsertAuthorizationGrant(ctx, accessdb.InsertAuthorizationGrantParams{ID: item.ID, ProjectID: project, Environment: environment, GenerationID: generation, SubjectKind: string(canonical.Subject().Kind), SubjectID: canonical.Subject().ID, ResourceID: canonical.Resource().ID().String(), ResourceKind: string(canonical.Resource().Kind()), Capability: canonical.Capability().String(), Name: item.Name}); err != nil {
			return err
		}
	}
	for _, item := range snapshot.DataPolicies() {
		var subjectKind, subjectID *string
		if item.Subject != nil {
			kind, id := string(item.Subject.Kind), item.Subject.ID
			subjectKind, subjectID = &kind, &id
		}
		if err = accessdb.New(tx).InsertAuthorizationDataPolicy(ctx, accessdb.InsertAuthorizationDataPolicyParams{ID: item.ID, ProjectID: project, Environment: environment, GenerationID: generation, ResourceID: item.Resource.ID().String(), ResourceKind: string(item.Resource.Kind()), SubjectKind: subjectKind, SubjectID: subjectID, PolicyType: item.PolicyType, Expression: []byte(item.ExpressionJSON)}); err != nil {
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
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("dashboard_publication:"+projectID.String()+"."+name))
	return accessdb.New(tx).UpsertDashboardPublicationPrincipal(ctx, accessdb.UpsertDashboardPublicationPrincipalParams{ID: pgtype.UUID{Bytes: id, Valid: true}, Name: name})
}
