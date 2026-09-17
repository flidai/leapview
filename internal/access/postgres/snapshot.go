package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
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
	profile, err := typedSnapshotProfile(snapshot)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(tx).InsertAuthorizationSnapshot(ctx, accessdb.InsertAuthorizationSnapshotParams{ProjectID: project, Environment: environment, GenerationID: generation, Digest: digest, PermissionProfile: profile})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		installed, err := accessdb.New(tx).GetAuthorizationSnapshotAuthority(ctx, accessdb.GetAuthorizationSnapshotAuthorityParams{ProjectID: project, Environment: environment, GenerationID: generation})
		if err != nil {
			return err
		}
		if installed.Digest != digest || !sameOptionalString(installed.PermissionProfile, profile) {
			return fmt.Errorf("%w: project=%s environment=%s generation=%s", ErrAuthorizationSnapshotIdentityConflict, project, environment, generation)
		}
		return nil
	}
	for _, item := range snapshot.RoleBindings() {
		caps, permissions, permissionProfile, err := assignmentEncoding(item.PermissionProfile, item.Permissions, item.Capabilities)
		if err != nil {
			return fmt.Errorf("encode authorization role binding %q: %w", item.ID, err)
		}
		if err = accessdb.New(tx).InsertAuthorizationRoleBinding(ctx, accessdb.InsertAuthorizationRoleBindingParams{ID: item.ID, ProjectID: project, Environment: environment, GenerationID: generation, SubjectKind: string(item.Subject.Kind), SubjectID: item.Subject.ID, Role: nullableString(string(item.Role)), Capabilities: caps, PermissionProfile: permissionProfile, Permissions: permissions, PermissionRole: nullableString(string(item.PermissionRole)), Name: item.Name}); err != nil {
			return err
		}
	}
	for _, item := range snapshot.Grants() {
		canonical := item.Canonical
		subject := item.Subject
		if subject == (access.SubjectRef{}) && canonical.Validate() == nil {
			subject = canonical.Subject()
		}
		var resourceID, resourceKind, capability *string
		if item.PermissionProfile == "" && item.Permissions == nil {
			id, kind, cap := canonical.Resource().ID().String(), string(canonical.Resource().Kind()), canonical.Capability().String()
			resourceID, resourceKind, capability = &id, &kind, &cap
		}
		var permissionProfile *string
		var permissions []byte
		if item.PermissionProfile != "" || item.Permissions != nil {
			encoded, encodeErr := access.EncodePermissionPairs(item.Permissions)
			if encodeErr != nil {
				return fmt.Errorf("encode authorization grant %q: %w", item.ID, encodeErr)
			}
			permissionProfile, permissions = stringPtr(item.PermissionProfile), encoded
		}
		if err = accessdb.New(tx).InsertAuthorizationGrant(ctx, accessdb.InsertAuthorizationGrantParams{ID: item.ID, ProjectID: project, Environment: environment, GenerationID: generation, SubjectKind: string(subject.Kind), SubjectID: subject.ID, ResourceID: resourceID, ResourceKind: resourceKind, Capability: capability, PermissionProfile: permissionProfile, Permissions: permissions, Name: item.Name}); err != nil {
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

func stringPtr(value string) *string { return &value }

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func assignmentEncoding(profile string, permissions []access.PermissionPair, capabilities []access.Capability) ([]byte, []byte, *string, error) {
	if profile != "" || permissions != nil {
		encoded, err := access.EncodePermissionPairs(permissions)
		if err != nil {
			return nil, nil, nil, err
		}
		return nil, encoded, stringPtr(profile), nil
	}
	if capabilities == nil {
		return nil, nil, nil, errors.New("legacy role binding capabilities are omitted")
	}
	encoded, err := json.Marshal(capabilities)
	return encoded, nil, nil, err
}

func typedSnapshotProfile(snapshot accesssnapshot.AuthorizationSnapshot) (*string, error) {
	profile := snapshot.PermissionProfile()
	if profile != "" && profile != access.PermissionCatalogProfile {
		return nil, fmt.Errorf("authorization snapshot uses unsupported permission profile %q", profile)
	}
	for _, binding := range snapshot.RoleBindings() {
		if binding.PermissionProfile != "" || binding.Permissions != nil {
			if profile != binding.PermissionProfile {
				return nil, fmt.Errorf("authorization snapshot profile %q disagrees with role binding profile %q", profile, binding.PermissionProfile)
			}
		}
	}
	for _, grant := range snapshot.Grants() {
		if grant.PermissionProfile != "" || grant.Permissions != nil {
			if profile != grant.PermissionProfile {
				return nil, fmt.Errorf("authorization snapshot profile %q disagrees with grant profile %q", profile, grant.PermissionProfile)
			}
		}
	}
	if profile == "" {
		return nil, nil
	}
	return stringPtr(profile), nil
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
