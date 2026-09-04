package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	apptesting "github.com/flidai/leapview/internal/app/testing"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	controlInstanceA = "instance-control-a"
	controlInstanceB = "instance-control-b"
	controlProject   = "project-control"
	controlActorID   = "00000000-0000-7000-8000-000000000001"
	controlSubjectID = "00000000-0000-7000-8000-000000000002"
)

type controlAuthorityDatabase struct {
	admin   *pgxpool.Pool
	runtime *pgxpool.Pool
	repo    *Repository
}

func newControlAuthorityDatabase(t *testing.T) controlAuthorityDatabase {
	t.Helper()
	h := postgrestest.Start(t, apptesting.PostgresConformanceRequired())
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{
		Name: "leapview_control_runtime", Password: "leapview-conformance-secret", Login: true,
	})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		t.Fatalf("apply control-plane migrations: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()

	runtime, err := pgxpool.New(ctx, database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	repo, err := NewAccess(runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	return controlAuthorityDatabase{admin: admin, runtime: runtime, repo: repo}
}

func TestControlAuthorityPostgreSQL18(t *testing.T) {
	db := newControlAuthorityDatabase(t)
	project := controlAuthorityProject(t)
	seedControlPrincipal(t, db.admin, controlActorID)
	seedControlPrincipal(t, db.admin, controlSubjectID)
	seedControlResources(t, db.admin, controlInstanceA, project)
	seedControlResources(t, db.admin, controlInstanceB, project)

	t.Run("compatibility snapshot import is create-once and replay-safe", func(t *testing.T) {
		instanceID := "instance-control-seed"
		seedControlResources(t, db.admin, instanceID, project)
		dashboard := mustControlAuthorityResource(t, "dashboard-control", projectgraph.KindDashboard)
		seed := access.ControlStateSeed{
			InstanceID: instanceID,
			ProjectID:  controlProject,
			ActorID:    controlActorID,
			RoleAssignments: []access.RoleAssignmentInput{{
				ID:      "00000000-0000-7000-8000-000000000011",
				Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
				Role:    string(access.ProjectRoleViewer),
				Name:    "seeded viewer",
			}},
			Grants: []access.ControlGrantInput{{
				ID:         "00000000-0000-7000-8000-000000000012",
				Subject:    access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
				Resource:   dashboard,
				Capability: access.CapabilityResourceRead,
				Name:       "seeded dashboard read",
			}},
		}
		first, err := db.repo.InitializeControlState(t.Context(), seed, project)
		if err != nil {
			t.Fatalf("initialize live control state: %v", err)
		}
		if first.Revision != 1 || len(first.RoleAssignments) != 1 || len(first.Grants) != 1 {
			t.Fatalf("initialized control state = %#v", first)
		}
		replayed, err := db.repo.InitializeControlState(t.Context(), seed, project)
		if err != nil {
			t.Fatalf("exact control seed replay: %v", err)
		}
		if replayed.Revision != first.Revision || len(replayed.RoleAssignments) != 1 || len(replayed.Grants) != 1 {
			t.Fatalf("replayed control state = %#v, want exact state", replayed)
		}
		changed := seed
		changed.RoleAssignments = append([]access.RoleAssignmentInput(nil), seed.RoleAssignments...)
		changed.RoleAssignments[0].Name = "changed seed"
		if _, err := db.repo.InitializeControlState(t.Context(), changed, project); !errors.Is(err, access.ErrControlConflict) {
			t.Fatalf("changed control seed replay error = %v, want conflict", err)
		}
	})

	t.Run("role binding lifecycle and instance isolation", func(t *testing.T) {
		created, err := db.repo.CreateRoleAssignment(t.Context(), access.RoleAssignmentInput{
			ID:         "00000000-0000-7000-8000-000000000101",
			InstanceID: controlInstanceA,
			ProjectID:  controlProject,
			Subject:    access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
			Role:       string(access.ProjectRoleViewer),
			Name:       "initial viewer",
			ActorID:    controlActorID,
		})
		if err != nil {
			t.Fatalf("create role binding: %v", err)
		}
		if created.Revision != 1 || created.RevokedAt != "" {
			t.Fatalf("created role binding = %#v", created)
		}
		state, err := db.repo.ControlState(t.Context(), controlInstanceA)
		if err != nil {
			t.Fatal(err)
		}
		if state.Revision != 1 || len(state.RoleAssignments) != 1 {
			t.Fatalf("state after role creation = revision %d, %d assignments", state.Revision, len(state.RoleAssignments))
		}

		updated, err := db.repo.UpdateRoleAssignment(t.Context(), access.RoleAssignmentInput{
			ID:               created.ID,
			InstanceID:       controlInstanceA,
			ProjectID:        controlProject,
			Subject:          created.Subject,
			Role:             created.Role,
			Name:             "updated viewer",
			ExpectedRevision: created.Revision,
			ActorID:          controlActorID,
		})
		if err != nil {
			t.Fatalf("update role binding: %v", err)
		}
		if updated.Revision != 2 || updated.Role != created.Role {
			t.Fatalf("updated role binding = %#v", updated)
		}
		if _, err := db.repo.UpdateRoleAssignment(t.Context(), access.RoleAssignmentInput{
			ID:               created.ID,
			InstanceID:       controlInstanceA,
			ProjectID:        controlProject,
			Subject:          created.Subject,
			Role:             string(access.ProjectRoleEditor),
			Name:             updated.Name,
			ExpectedRevision: updated.Revision,
			ActorID:          controlActorID,
		}); !errors.Is(err, access.ErrControlIdentityConflict) {
			t.Fatalf("role retarget error = %v, want identity conflict", err)
		}
		if _, err := db.repo.UpdateRoleAssignment(t.Context(), access.RoleAssignmentInput{
			ID:               created.ID,
			InstanceID:       controlInstanceA,
			ProjectID:        "project-other",
			Subject:          created.Subject,
			Role:             string(access.ProjectRoleEditor),
			ExpectedRevision: updated.Revision,
		}); !errors.Is(err, access.ErrControlIdentityConflict) {
			t.Fatalf("retargeted role binding error = %v, want identity conflict", err)
		}
		if _, err := db.repo.UpdateRoleAssignment(t.Context(), access.RoleAssignmentInput{
			ID:               created.ID,
			InstanceID:       controlInstanceA,
			ProjectID:        controlProject,
			Subject:          created.Subject,
			Role:             string(access.ProjectRoleEditor),
			ExpectedRevision: created.Revision,
		}); !errors.Is(err, access.ErrControlRevisionConflict) {
			t.Fatalf("stale role binding update error = %v, want revision conflict", err)
		}

		deleted, err := db.repo.DeleteRoleAssignment(t.Context(), controlInstanceA, created.ID, updated.Revision, controlActorID)
		if err != nil {
			t.Fatalf("delete role binding: %v", err)
		}
		if deleted.Revision != 3 || deleted.RevokedAt == "" {
			t.Fatalf("deleted role binding = %#v", deleted)
		}
		if _, err := db.repo.DeleteRoleAssignment(t.Context(), controlInstanceA, created.ID, deleted.Revision, controlActorID); !errors.Is(err, access.ErrControlRevoked) {
			t.Fatalf("second role delete error = %v, want revoked", err)
		}
		if assignments, err := db.repo.ListRoleAssignments(t.Context(), controlInstanceA); err != nil || len(assignments) != 0 {
			t.Fatalf("active role bindings after delete = %#v, %v", assignments, err)
		}
		stored, err := db.repo.RoleAssignment(t.Context(), controlInstanceA, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.RevokedAt == "" {
			t.Fatal("role binding tombstone was not retained")
		}

		_, err = db.repo.CreateRoleAssignment(t.Context(), access.RoleAssignmentInput{
			ID:         created.ID,
			InstanceID: controlInstanceB,
			ProjectID:  controlProject,
			Subject:    access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
			Role:       string(access.ProjectRoleViewer),
			Name:       "other instance",
			ActorID:    controlActorID,
		})
		if err != nil {
			t.Fatalf("create other-instance role binding: %v", err)
		}
		original, err := db.repo.RoleAssignment(t.Context(), controlInstanceA, created.ID)
		if err != nil || original.RevokedAt == "" {
			t.Fatalf("same role id in original instance = %#v, %v; want retained tombstone", original, err)
		}
		if assignments, err := db.repo.ListRoleAssignments(t.Context(), controlInstanceB); err != nil || len(assignments) != 1 {
			t.Fatalf("other-instance role bindings = %#v, %v", assignments, err)
		}

		state, err = db.repo.ControlState(t.Context(), controlInstanceA)
		if err != nil {
			t.Fatal(err)
		}
		if state.Revision != 3 {
			t.Fatalf("control revision after role lifecycle = %d, want 3", state.Revision)
		}
		if _, err := db.admin.Exec(t.Context(), `UPDATE access.control_state SET revision=revision WHERE instance_id=$1`, controlInstanceA); err == nil {
			t.Fatal("non-increasing control revision unexpectedly succeeded")
		}
		var persistedRevision int64
		if err := db.admin.QueryRow(t.Context(), `SELECT revision FROM access.control_state WHERE instance_id=$1`, controlInstanceA).Scan(&persistedRevision); err != nil {
			t.Fatal(err)
		}
		if persistedRevision != state.Revision {
			t.Fatalf("control revision after rejected rewrite = %d, want %d", persistedRevision, state.Revision)
		}
		otherState, err := db.repo.ControlState(t.Context(), controlInstanceB)
		if err != nil {
			t.Fatal(err)
		}
		if otherState.Revision != 1 {
			t.Fatalf("other-instance control revision = %d, want 1", otherState.Revision)
		}
	})

	t.Run("grant lifecycle, immutable target, and semantic conflict", func(t *testing.T) {
		dashboard := mustControlAuthorityResource(t, "dashboard-control", projectgraph.KindDashboard)
		model := mustControlAuthorityResource(t, "model-control", projectgraph.KindModel)
		created, err := db.repo.CreateGrant(t.Context(), access.ControlGrantInput{
			ID:         "00000000-0000-7000-8000-000000000201",
			InstanceID: controlInstanceA,
			ProjectID:  controlProject,
			Subject:    access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
			Resource:   dashboard,
			Capability: access.CapabilityResourceRead,
			Name:       "dashboard read",
			ActorID:    controlActorID,
		}, project)
		if err != nil {
			t.Fatalf("create grant: %v", err)
		}
		if created.Revision != 1 || created.ReferenceLifecycle != access.ControlReferenceActive {
			t.Fatalf("created grant = %#v", created)
		}
		if _, err := db.repo.CreateGrant(t.Context(), access.ControlGrantInput{
			ID:         "00000000-0000-7000-8000-000000000202",
			InstanceID: controlInstanceA,
			ProjectID:  controlProject,
			Subject:    created.Subject,
			Resource:   dashboard,
			Capability: created.Capability,
			Name:       "semantic duplicate",
			ActorID:    controlActorID,
		}, project); !errors.Is(err, access.ErrControlConflict) {
			t.Fatalf("semantic duplicate error = %v, want conflict", err)
		}
		state, err := db.repo.ControlState(t.Context(), controlInstanceA)
		if err != nil {
			t.Fatal(err)
		}
		if state.Revision != 4 || len(state.Grants) != 1 {
			t.Fatalf("state after duplicate grant = revision %d, %d grants", state.Revision, len(state.Grants))
		}

		updated, err := db.repo.UpdateGrant(t.Context(), access.ControlGrantInput{
			ID:               created.ID,
			InstanceID:       controlInstanceA,
			ProjectID:        controlProject,
			Subject:          created.Subject,
			Resource:         dashboard,
			Capability:       access.CapabilityResourcePublish,
			Name:             "dashboard publish",
			ExpectedRevision: created.Revision,
			ActorID:          controlActorID,
		}, project)
		if err != nil {
			t.Fatalf("update grant: %v", err)
		}
		if updated.Revision != 2 || updated.Capability != access.CapabilityResourcePublish {
			t.Fatalf("updated grant = %#v", updated)
		}
		if _, err := db.repo.UpdateGrant(t.Context(), access.ControlGrantInput{
			ID:               created.ID,
			InstanceID:       controlInstanceA,
			ProjectID:        controlProject,
			Subject:          created.Subject,
			Resource:         model,
			Capability:       access.CapabilityResourceEdit,
			ExpectedRevision: updated.Revision,
		}, project); !errors.Is(err, access.ErrControlIdentityConflict) {
			t.Fatalf("retargeted grant error = %v, want identity conflict", err)
		}
		if _, err := db.repo.UpdateGrant(t.Context(), access.ControlGrantInput{
			ID:               created.ID,
			InstanceID:       controlInstanceA,
			ProjectID:        controlProject,
			Subject:          created.Subject,
			Resource:         dashboard,
			Capability:       access.CapabilityResourcePublish,
			ExpectedRevision: created.Revision,
		}, project); !errors.Is(err, access.ErrControlRevisionConflict) {
			t.Fatalf("stale grant update error = %v, want revision conflict", err)
		}

		if _, err := db.admin.Exec(t.Context(), `UPDATE access.control_grant SET resource_id=$1 WHERE id=$2::uuid`, model.ID().String(), created.ID); err == nil {
			t.Fatal("direct grant target rewrite unexpectedly succeeded")
		}
		var target string
		if err := db.admin.QueryRow(t.Context(), `SELECT resource_id FROM access.control_grant WHERE id=$1::uuid`, created.ID).Scan(&target); err != nil {
			t.Fatal(err)
		}
		if target != dashboard.ID().String() {
			t.Fatalf("grant target after rejected rewrite = %q, want %q", target, dashboard.ID())
		}
		if _, err := db.admin.Exec(t.Context(), `
			UPDATE access.control_grant
			SET revoked_at=clock_timestamp(),revision=revision+1,updated_at=clock_timestamp()
			WHERE instance_id=$1 AND id=$2`, controlInstanceA, created.ID); err == nil {
			t.Fatal("grant revocation without control-state/reference reconciliation unexpectedly committed")
		}
		stillActive, err := db.repo.ControlGrant(t.Context(), controlInstanceA, created.ID)
		if err != nil || stillActive.Revision != updated.Revision || stillActive.RevokedAt != "" {
			t.Fatalf("grant after rejected direct revoke = %#v, %v", stillActive, err)
		}

		if _, err := db.admin.Exec(t.Context(), `
			UPDATE project.durable_resource_reference
			SET owner_authored_id='corrupted-owner'
			WHERE instance_id=$1 AND reference_id=$2`, controlInstanceA, controlGrantReferencePrefix+created.ID); err != nil {
			t.Fatalf("corrupt reference owner for fail-closed test: %v", err)
		}
		if _, err := db.repo.RevokeGrant(t.Context(), controlInstanceA, created.ID, updated.Revision, controlActorID); !errors.Is(err, access.ErrControlReferenceConflict) {
			t.Fatalf("revoke with mismatched reference owner error = %v, want reference conflict", err)
		}
		if _, err := db.admin.Exec(t.Context(), `
			UPDATE project.durable_resource_reference
			SET owner_authored_id=$3
			WHERE instance_id=$1 AND reference_id=$2`, controlInstanceA, controlGrantReferencePrefix+created.ID, created.ID); err != nil {
			t.Fatalf("restore reference owner after fail-closed test: %v", err)
		}

		revoked, err := db.repo.RevokeGrant(t.Context(), controlInstanceA, created.ID, updated.Revision, controlActorID)
		if err != nil {
			t.Fatalf("revoke grant: %v", err)
		}
		if revoked.Revision != 3 || revoked.RevokedAt == "" || revoked.ReferenceLifecycle != access.ControlReferenceSuspended {
			t.Fatalf("revoked grant = %#v", revoked)
		}
		if grants, err := db.repo.ListControlGrants(t.Context(), controlInstanceA, controlProject); err != nil || len(grants) != 0 {
			t.Fatalf("active grants after revoke = %#v, %v", grants, err)
		}
		if _, err := db.repo.ReactivateGrant(t.Context(), controlInstanceA, created.ID, revoked.Revision, project, controlActorID); !errors.Is(err, access.ErrControlRevoked) {
			t.Fatalf("reactivate revoked grant error = %v, want revoked", err)
		}
		state, err = db.repo.ControlState(t.Context(), controlInstanceA)
		if err != nil {
			t.Fatal(err)
		}
		if state.Revision != 6 {
			t.Fatalf("control revision after grant lifecycle = %d, want 6", state.Revision)
		}

		_, err = db.repo.CreateGrant(t.Context(), access.ControlGrantInput{
			ID:         created.ID,
			InstanceID: controlInstanceB,
			ProjectID:  controlProject,
			Subject:    created.Subject,
			Resource:   dashboard,
			Capability: access.CapabilityResourceRead,
			Name:       "other instance dashboard",
			ActorID:    controlActorID,
		}, project)
		if err != nil {
			t.Fatalf("create other-instance grant: %v", err)
		}
		original, err := db.repo.ControlGrant(t.Context(), controlInstanceA, created.ID)
		if err != nil || original.RevokedAt == "" {
			t.Fatalf("same grant id in original instance = %#v, %v; want retained tombstone", original, err)
		}
		if grants, err := db.repo.ListControlGrants(t.Context(), controlInstanceB, controlProject); err != nil || len(grants) != 1 {
			t.Fatalf("other-instance grants = %#v, %v", grants, err)
		}
	})

	t.Run("tombstone, explicit restore, and revoked reactivation", func(t *testing.T) {
		model := mustControlAuthorityResource(t, "model-control", projectgraph.KindModel)
		tombstoneControlResource(t, db.admin, controlInstanceA, model.ID().String())
		created, err := db.repo.CreateGrant(t.Context(), access.ControlGrantInput{
			ID:         "00000000-0000-7000-8000-000000000301",
			InstanceID: controlInstanceA,
			ProjectID:  controlProject,
			Subject:    access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
			Resource:   model,
			Capability: access.CapabilityResourceRead,
			Name:       "tombstoned model read",
			ActorID:    controlActorID,
		}, project)
		if err != nil {
			t.Fatalf("create grant for tombstoned target: %v", err)
		}
		if created.Revision != 1 || created.ReferenceLifecycle != access.ControlReferenceSuspended || created.ReferenceSuspendedAt == "" {
			t.Fatalf("tombstoned-target grant = %#v", created)
		}

		restoreControlResource(t, db.admin, controlInstanceA, model.ID().String())
		reactivated, err := db.repo.ReactivateGrant(t.Context(), controlInstanceA, created.ID, created.Revision, project, controlActorID)
		if err != nil {
			t.Fatalf("explicitly reactivate restored target: %v", err)
		}
		if reactivated.ReferenceLifecycle != access.ControlReferenceActive || reactivated.ReferenceReactivatedAt == "" || reactivated.Revision != created.Revision {
			t.Fatalf("reactivated restored-target grant = %#v", reactivated)
		}

		revoked, err := db.repo.RevokeGrant(t.Context(), controlInstanceA, created.ID, reactivated.Revision, controlActorID)
		if err != nil {
			t.Fatalf("revoke restored-target grant: %v", err)
		}
		if _, err := db.repo.ReactivateGrant(t.Context(), controlInstanceA, created.ID, revoked.Revision, project, controlActorID); !errors.Is(err, access.ErrControlRevoked) {
			t.Fatalf("reactivate revoked restored-target grant = %v, want revoked", err)
		}
	})

	t.Run("control and audit transactions are atomic", func(t *testing.T) {
		rollbackInstance := "instance-control-rollback"
		rollbackRoleID := "00000000-0000-7000-8000-000000000401"
		_, err := db.repo.CreateRoleAssignment(t.Context(), access.RoleAssignmentInput{
			ID:         rollbackRoleID,
			InstanceID: rollbackInstance,
			ProjectID:  controlProject,
			Subject:    access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
			Role:       string(access.ProjectRoleViewer),
			Name:       "audit failure must rollback",
			ActorID:    "not-a-uuid",
		})
		if err == nil {
			t.Fatal("control mutation with invalid audit actor unexpectedly committed")
		}
		var roles, states, audits int
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.control_role_binding WHERE id=$1::uuid`, rollbackRoleID).Scan(&roles); err != nil {
			t.Fatal(err)
		}
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.control_state WHERE instance_id=$1`, rollbackInstance).Scan(&states); err != nil {
			t.Fatal(err)
		}
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='role_assignment.created' AND resource_id=$1`, rollbackRoleID).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if roles != 0 || states != 0 || audits != 0 {
			t.Fatalf("failed audited control mutation rows = role %d, state %d, audit %d; want 0/0/0", roles, states, audits)
		}

		rollbackGroupID := "00000000-0000-7000-8000-000000000402"
		rollbackAuditAction := "control.test.rollback"
		err = db.repo.RunAuditedMutation(t.Context(), func(transactional access.Repository) (access.AuditEventInput, error) {
			tx := transactional.(*Repository)
			if _, err := tx.db.Exec(t.Context(), `INSERT INTO access.access_group(id,name) VALUES($1::uuid,'rollback group')`, rollbackGroupID); err != nil {
				return access.AuditEventInput{}, err
			}
			return access.AuditEventInput{
				PrincipalID: controlActorID, Action: rollbackAuditAction, ResourceKind: "access_group", ResourceID: rollbackGroupID, Status: "success",
			}, errors.New("force rollback")
		})
		if err == nil {
			t.Fatal("forced audited rollback unexpectedly succeeded")
		}
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.access_group WHERE id=$1::uuid`, rollbackGroupID).Scan(&roles); err != nil {
			t.Fatal(err)
		}
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action=$1`, rollbackAuditAction).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if roles != 0 || audits != 0 {
			t.Fatalf("forced rollback rows = group %d, audit %d; want 0/0", roles, audits)
		}

		commitGroupID := "00000000-0000-7000-8000-000000000403"
		commitAuditAction := "control.test.commit"
		if err := db.repo.RunAuditedMutation(t.Context(), func(transactional access.Repository) (access.AuditEventInput, error) {
			tx := transactional.(*Repository)
			if _, err := tx.db.Exec(t.Context(), `INSERT INTO access.access_group(id,name) VALUES($1::uuid,'commit group')`, commitGroupID); err != nil {
				return access.AuditEventInput{}, err
			}
			return access.AuditEventInput{PrincipalID: controlActorID, Action: commitAuditAction, ResourceKind: "access_group", ResourceID: commitGroupID, Status: "success"}, nil
		}); err != nil {
			t.Fatalf("audited commit: %v", err)
		}
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.access_group WHERE id=$1::uuid`, commitGroupID).Scan(&roles); err != nil {
			t.Fatal(err)
		}
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action=$1`, commitAuditAction).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if roles != 1 || audits != 1 {
			t.Fatalf("committed rows = group %d, audit %d; want 1/1", roles, audits)
		}

		var roleAudit, grantAudit, revokeAudit int
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='role_assignment.created'`).Scan(&roleAudit); err != nil {
			t.Fatal(err)
		}
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='grant.created'`).Scan(&grantAudit); err != nil {
			t.Fatal(err)
		}
		if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='grant.deleted'`).Scan(&revokeAudit); err != nil {
			t.Fatal(err)
		}
		if roleAudit < 2 || grantAudit < 3 || revokeAudit < 2 {
			t.Fatalf("control audit actions = role create %d, grant create %d, grant delete %d", roleAudit, grantAudit, revokeAudit)
		}
	})
}

func controlAuthorityProject(t *testing.T) projectgraph.ProjectGraph {
	t.Helper()
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: controlProject, Kind: projectgraph.KindProject, Name: "Control"},
		{ID: "dashboard-control", Kind: projectgraph.KindDashboard, Name: "Dashboard"},
		{ID: "model-control", Kind: projectgraph.KindModel, Name: "Model"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func mustControlAuthorityResource(t *testing.T, id projectgraph.ResourceID, kind projectgraph.Kind) access.ResourceRef {
	t.Helper()
	resource, err := access.NewResourceRef(id, kind)
	if err != nil {
		t.Fatal(err)
	}
	return resource
}

func seedControlPrincipal(t *testing.T, db *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `
		INSERT INTO access.principal(id,principal_type,status,display_name)
		VALUES($1::uuid,'user','active','control test') ON CONFLICT (id) DO NOTHING`, id); err != nil {
		t.Fatalf("seed control principal %s: %v", id, err)
	}
}

func seedControlResources(t *testing.T, db *pgxpool.Pool, instanceID string, project projectgraph.ProjectGraph) {
	t.Helper()
	for _, resource := range project.Resources() {
		if resource.Kind == projectgraph.KindProject {
			continue
		}
		if _, err := db.Exec(t.Context(), `
			INSERT INTO project.resource_identity(instance_id,authored_id,resource_kind,lifecycle_state,active_bundle_id)
			VALUES($1,$2,$3,'active',$4) ON CONFLICT (instance_id,authored_id) DO NOTHING`,
			instanceID, resource.ID.String(), resource.Kind, fmt.Sprintf("bundle-%s", instanceID)); err != nil {
			t.Fatalf("seed resource %s/%s: %v", instanceID, resource.ID, err)
		}
	}
}

func tombstoneControlResource(t *testing.T, db *pgxpool.Pool, instanceID, authoredID string) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `
		UPDATE project.resource_identity
		SET lifecycle_state='tombstoned',active_bundle_id=NULL,tombstone_reason='control test',tombstoned_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE instance_id=$1 AND authored_id=$2`, instanceID, authoredID); err != nil {
		t.Fatalf("tombstone resource %s/%s: %v", instanceID, authoredID, err)
	}
}

func restoreControlResource(t *testing.T, db *pgxpool.Pool, instanceID, authoredID string) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `
		UPDATE project.resource_identity
		SET lifecycle_state='active',active_bundle_id=$3,tombstone_reason='',tombstoned_at=NULL,restored_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE instance_id=$1 AND authored_id=$2`, instanceID, authoredID, fmt.Sprintf("bundle-restore-%s", instanceID)); err != nil {
		t.Fatalf("restore resource %s/%s: %v", instanceID, authoredID, err)
	}
}
