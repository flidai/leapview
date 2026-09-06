package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ access.ControlStore = (*Repository)(nil)
var _ access.AuthorizationControlRevisionReader = (*Repository)(nil)

const controlGrantReferencePrefix = "grant:"

// InitializeControlState performs the only compatibility-snapshot import into
// live control authority. It is create-once: exact retries replay the stored
// state and any changed role/grant evidence conflicts without replacement.
func (r *Repository) InitializeControlState(ctx context.Context, seed access.ControlStateSeed, project projectgraph.ProjectGraph) (access.ControlState, error) {
	if err := validateControlInstance(seed.InstanceID); err != nil {
		return access.ControlState{}, err
	}
	if err := project.Validate(); err != nil || seed.ProjectID != project.ProjectID().String() {
		if err != nil {
			return access.ControlState{}, err
		}
		return access.ControlState{}, access.ErrControlIdentityConflict
	}
	roles := append([]access.RoleAssignmentInput(nil), seed.RoleAssignments...)
	grants := append([]access.ControlGrantInput(nil), seed.Grants...)
	for i := range roles {
		roles[i].InstanceID, roles[i].ProjectID = seed.InstanceID, seed.ProjectID
		if roles[i].ID == "" {
			return access.ControlState{}, fmt.Errorf("%w: seeded role assignment id is required", access.ErrControlInvalidInput)
		}
		if err := roles[i].Validate(); err != nil {
			return access.ControlState{}, err
		}
	}
	for i := range grants {
		grants[i].InstanceID, grants[i].ProjectID = seed.InstanceID, seed.ProjectID
		if grants[i].ID == "" {
			return access.ControlState{}, fmt.Errorf("%w: seeded grant id is required", access.ErrControlInvalidInput)
		}
		if _, err := grants[i].ValidateAgainstGraph(project); err != nil {
			return access.ControlState{}, err
		}
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.ControlState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockControlInstance(ctx, tx, seed.InstanceID); err != nil {
		return access.ControlState{}, err
	}
	var existingProject string
	existingErr := tx.QueryRow(ctx, `SELECT project_id FROM access.control_state WHERE instance_id=$1 FOR UPDATE`, seed.InstanceID).Scan(&existingProject)
	if existingErr == nil {
		if existingProject != seed.ProjectID {
			return access.ControlState{}, access.ErrControlIdentityConflict
		}
		state, err := loadControlState(ctx, tx, seed.InstanceID)
		if err != nil {
			return access.ControlState{}, err
		}
		if !sameControlSeed(state, roles, grants) {
			return access.ControlState{}, access.ErrControlConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return access.ControlState{}, err
		}
		return state, nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return access.ControlState{}, existingErr
	}
	if _, err := tx.Exec(ctx, `INSERT INTO access.control_state(instance_id,project_id,revision) VALUES($1,$2,0)`, seed.InstanceID, seed.ProjectID); err != nil {
		return access.ControlState{}, mapControlError("initialize control state", err)
	}
	for _, input := range roles {
		if err := validateStoredSubject(ctx, tx, input.Subject); err != nil {
			return access.ControlState{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO access.control_role_binding(id,instance_id,project_id,subject_kind,subject_id,role,name)
			VALUES($1,$2,$3,$4,$5::uuid,$6,$7)`, input.ID, seed.InstanceID, seed.ProjectID,
			input.Subject.Kind, input.Subject.ID, input.Role, input.Name); err != nil {
			return access.ControlState{}, mapControlError("seed role assignment", err)
		}
	}
	for _, input := range grants {
		if err := validateStoredSubject(ctx, tx, input.Subject); err != nil {
			return access.ControlState{}, err
		}
		lifecycle, err := resolveControlTarget(ctx, tx, seed.InstanceID, input.Resource)
		if err != nil {
			return access.ControlState{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO access.control_grant(id,instance_id,project_id,subject_kind,subject_id,resource_id,resource_kind,capability,name)
			VALUES($1,$2,$3,$4,$5::uuid,$6,$7,$8,$9)`, input.ID, seed.InstanceID, seed.ProjectID,
			input.Subject.Kind, input.Subject.ID, input.Resource.ID().String(), input.Resource.Kind(), input.Capability, input.Name); err != nil {
			return access.ControlState{}, mapControlError("seed grant", err)
		}
		grant := access.ControlGrant{ID: input.ID, InstanceID: seed.InstanceID, ProjectID: seed.ProjectID, Subject: input.Subject, Resource: input.Resource, Capability: input.Capability, Name: input.Name}
		if err := putControlReference(ctx, tx, grant, lifecycle); err != nil {
			return access.ControlState{}, err
		}
	}
	if err := advanceControlRevision(ctx, tx, seed.InstanceID); err != nil {
		return access.ControlState{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"projectId": seed.ProjectID, "roleAssignments": len(roles), "grants": len(grants)})
	txRepository := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	if err := txRepository.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: seed.ActorID, Action: "control_state.initialized", ResourceKind: "instance", ResourceID: seed.InstanceID, Status: "success", MetadataJSON: string(metadata)}); err != nil {
		return access.ControlState{}, err
	}
	state, err := loadControlState(ctx, tx, seed.InstanceID)
	if err != nil {
		return access.ControlState{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return access.ControlState{}, err
	}
	return state, nil
}

func (r *Repository) RoleAssignment(ctx context.Context, instanceID, assignmentID string) (access.RoleAssignment, error) {
	if err := validateControlLookup(instanceID, assignmentID); err != nil {
		return access.RoleAssignment{}, err
	}
	return readRoleAssignment(ctx, r.db, instanceID, assignmentID)
}

func (r *Repository) ListRoleAssignments(ctx context.Context, instanceID string) ([]access.RoleAssignment, error) {
	if err := validateControlInstance(instanceID); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT id::text,instance_id,project_id,subject_kind,subject_id::text,role,name,revision,created_at,updated_at,revoked_at
		FROM access.control_role_binding
		WHERE instance_id=$1 AND revoked_at IS NULL
		ORDER BY id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRoleAssignments(rows)
}

func (r *Repository) CreateRoleAssignment(ctx context.Context, input access.RoleAssignmentInput) (access.RoleAssignment, error) {
	if err := input.Validate(); err != nil {
		return access.RoleAssignment{}, err
	}
	if input.ID == "" {
		id, err := newUUID()
		if err != nil {
			return access.RoleAssignment{}, err
		}
		input.ID = id
	}
	var result access.RoleAssignment
	err := r.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		tx := repository.(*Repository)
		if err := lockControlInstance(ctx, tx.db, input.InstanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		if err := ensureControlState(ctx, tx.db, input.InstanceID, input.ProjectID); err != nil {
			return access.AuditEventInput{}, err
		}
		if err := validateStoredSubject(ctx, tx.db, input.Subject); err != nil {
			return access.AuditEventInput{}, err
		}
		row := tx.db.QueryRow(ctx, `
			INSERT INTO access.control_role_binding
			(id,instance_id,project_id,subject_kind,subject_id,role,name)
			VALUES($1,$2,$3,$4,$5::uuid,$6,$7)
			RETURNING id::text,instance_id,project_id,subject_kind,subject_id::text,role,name,revision,created_at,updated_at,revoked_at`,
			input.ID, input.InstanceID, input.ProjectID, input.Subject.Kind, input.Subject.ID, input.Role, input.Name)
		var err error
		result, err = scanRoleAssignment(row)
		if err != nil {
			return access.AuditEventInput{}, mapControlError("create role assignment", err)
		}
		if err := advanceControlRevision(ctx, tx.db, input.InstanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		return roleAssignmentAudit(input, result, "role_assignment.created"), nil
	})
	return result, err
}

func (r *Repository) UpdateRoleAssignment(ctx context.Context, input access.RoleAssignmentInput) (access.RoleAssignment, error) {
	if err := input.Validate(); err != nil || input.ID == "" || input.ExpectedRevision <= 0 {
		if err != nil {
			return access.RoleAssignment{}, err
		}
		return access.RoleAssignment{}, fmt.Errorf("%w: role assignment id and expected revision are required", access.ErrControlInvalidInput)
	}
	var result access.RoleAssignment
	err := r.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		tx := repository.(*Repository)
		if err := lockControlInstance(ctx, tx.db, input.InstanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		current, err := readRoleAssignment(ctx, tx.db, input.InstanceID, input.ID)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if current.ProjectID != input.ProjectID || current.Subject != input.Subject || current.Role != input.Role {
			return access.AuditEventInput{}, access.ErrControlIdentityConflict
		}
		if current.RevokedAt != "" {
			return access.AuditEventInput{}, access.ErrControlRevoked
		}
		row := tx.db.QueryRow(ctx, `
			UPDATE access.control_role_binding
			SET name=$4,revision=revision+1,updated_at=clock_timestamp()
			WHERE instance_id=$1 AND id=$2 AND revision=$3 AND revoked_at IS NULL
			RETURNING id::text,instance_id,project_id,subject_kind,subject_id::text,role,name,revision,created_at,updated_at,revoked_at`,
			input.InstanceID, input.ID, input.ExpectedRevision, input.Name)
		result, err = scanRoleAssignment(row)
		if errors.Is(err, access.ErrControlNotFound) {
			return access.AuditEventInput{}, access.ErrControlRevisionConflict
		}
		if err != nil {
			return access.AuditEventInput{}, mapControlError("update role assignment", err)
		}
		if err := advanceControlRevision(ctx, tx.db, input.InstanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		return roleAssignmentAudit(input, result, "role_assignment.updated"), nil
	})
	return result, err
}

func (r *Repository) RevokeRoleAssignment(ctx context.Context, instanceID, assignmentID string, expectedRevision int64, actorID string) (access.RoleAssignment, error) {
	return r.revokeRoleAssignment(ctx, instanceID, assignmentID, expectedRevision, actorID, "role_assignment.revoked")
}

func (r *Repository) DeleteRoleAssignment(ctx context.Context, instanceID, assignmentID string, expectedRevision int64, actorID string) (access.RoleAssignment, error) {
	return r.revokeRoleAssignment(ctx, instanceID, assignmentID, expectedRevision, actorID, "role_assignment.deleted")
}

func (r *Repository) revokeRoleAssignment(ctx context.Context, instanceID, assignmentID string, expectedRevision int64, actorID, action string) (access.RoleAssignment, error) {
	if err := validateControlLookup(instanceID, assignmentID); err != nil || expectedRevision <= 0 {
		if err != nil {
			return access.RoleAssignment{}, err
		}
		return access.RoleAssignment{}, fmt.Errorf("%w: expected revision is required", access.ErrControlInvalidInput)
	}
	var result access.RoleAssignment
	err := r.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		tx := repository.(*Repository)
		if err := lockControlInstance(ctx, tx.db, instanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		row := tx.db.QueryRow(ctx, `
			UPDATE access.control_role_binding
			SET revoked_at=clock_timestamp(),revision=revision+1,updated_at=clock_timestamp()
			WHERE instance_id=$1 AND id=$2 AND revision=$3 AND revoked_at IS NULL
			RETURNING id::text,instance_id,project_id,subject_kind,subject_id::text,role,name,revision,created_at,updated_at,revoked_at`,
			instanceID, assignmentID, expectedRevision)
		var err error
		result, err = scanRoleAssignment(row)
		if errors.Is(err, access.ErrControlNotFound) {
			return access.AuditEventInput{}, classifyRoleMutationMiss(ctx, tx.db, instanceID, assignmentID)
		}
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if err := advanceControlRevision(ctx, tx.db, instanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		return access.AuditEventInput{PrincipalID: actorID, Action: action, ResourceKind: "role_assignment", ResourceID: assignmentID, Status: "success", MetadataJSON: `{}`}, nil
	})
	return result, err
}

func (r *Repository) ControlGrant(ctx context.Context, instanceID, grantID string) (access.ControlGrant, error) {
	if err := validateControlLookup(instanceID, grantID); err != nil {
		return access.ControlGrant{}, err
	}
	return readControlGrant(ctx, r.db, instanceID, grantID)
}

func (r *Repository) ListControlGrants(ctx context.Context, instanceID, projectID string) ([]access.ControlGrant, error) {
	if err := validateControlInstance(instanceID); err != nil {
		return nil, err
	}
	if _, err := projectgraph.NewResourceID(projectID); err != nil {
		return nil, access.ErrControlInvalidInput
	}
	rows, err := r.db.Query(ctx, controlGrantSelect+`
		WHERE g.instance_id=$1 AND g.project_id=$2 AND g.revoked_at IS NULL
		ORDER BY g.id`, instanceID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanControlGrants(rows)
}

func (r *Repository) CreateGrant(ctx context.Context, input access.ControlGrantInput, project projectgraph.ProjectGraph) (access.ControlGrant, error) {
	if _, err := input.ValidateAgainstGraph(project); err != nil {
		return access.ControlGrant{}, err
	}
	if input.ProjectID == "" {
		input.ProjectID = project.ProjectID().String()
	}
	if input.ID == "" {
		id, err := newUUID()
		if err != nil {
			return access.ControlGrant{}, err
		}
		input.ID = id
	}
	var result access.ControlGrant
	err := r.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		tx := repository.(*Repository)
		if err := lockControlInstance(ctx, tx.db, input.InstanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		if err := ensureControlState(ctx, tx.db, input.InstanceID, input.ProjectID); err != nil {
			return access.AuditEventInput{}, err
		}
		if err := validateStoredSubject(ctx, tx.db, input.Subject); err != nil {
			return access.AuditEventInput{}, err
		}
		lifecycle, err := resolveControlTarget(ctx, tx.db, input.InstanceID, input.Resource)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		row := tx.db.QueryRow(ctx, `
			INSERT INTO access.control_grant
			(id,instance_id,project_id,subject_kind,subject_id,resource_id,resource_kind,capability,name)
			VALUES($1,$2,$3,$4,$5::uuid,$6,$7,$8,$9)
			RETURNING id::text`,
			input.ID, input.InstanceID, input.ProjectID, input.Subject.Kind, input.Subject.ID,
			input.Resource.ID().String(), input.Resource.Kind(), input.Capability, input.Name)
		var storedID string
		if err := row.Scan(&storedID); err != nil {
			return access.AuditEventInput{}, mapControlError("create grant", err)
		}
		stored := access.ControlGrant{ID: storedID, InstanceID: input.InstanceID, ProjectID: input.ProjectID, Subject: input.Subject, Resource: input.Resource, Capability: input.Capability, Name: input.Name}
		if err := putControlReference(ctx, tx.db, stored, lifecycle); err != nil {
			return access.AuditEventInput{}, err
		}
		result, err = readControlGrant(ctx, tx.db, input.InstanceID, input.ID)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if err := advanceControlRevision(ctx, tx.db, input.InstanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		return grantAudit(input, result, "grant.created"), nil
	})
	return result, err
}

func (r *Repository) UpdateGrant(ctx context.Context, input access.ControlGrantInput, project projectgraph.ProjectGraph) (access.ControlGrant, error) {
	if _, err := input.ValidateAgainstGraph(project); err != nil {
		return access.ControlGrant{}, err
	}
	if input.ID == "" || input.ExpectedRevision <= 0 {
		return access.ControlGrant{}, fmt.Errorf("%w: grant id and expected revision are required", access.ErrControlInvalidInput)
	}
	if input.ProjectID == "" {
		input.ProjectID = project.ProjectID().String()
	}
	var result access.ControlGrant
	err := r.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		tx := repository.(*Repository)
		if err := lockControlInstance(ctx, tx.db, input.InstanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		current, err := readControlGrant(ctx, tx.db, input.InstanceID, input.ID)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if current.ProjectID != input.ProjectID || current.Resource != input.Resource {
			return access.AuditEventInput{}, access.ErrControlIdentityConflict
		}
		if current.RevokedAt != "" {
			return access.AuditEventInput{}, access.ErrControlRevoked
		}
		if err := validateStoredSubject(ctx, tx.db, input.Subject); err != nil {
			return access.AuditEventInput{}, err
		}
		row := tx.db.QueryRow(ctx, `
			UPDATE access.control_grant
			SET subject_kind=$4,subject_id=$5::uuid,capability=$6,name=$7,revision=revision+1,updated_at=clock_timestamp()
			WHERE instance_id=$1 AND id=$2 AND revision=$3 AND revoked_at IS NULL
			RETURNING id::text`,
			input.InstanceID, input.ID, input.ExpectedRevision, input.Subject.Kind, input.Subject.ID, input.Capability, input.Name)
		var storedID string
		if err := row.Scan(&storedID); errors.Is(err, pgx.ErrNoRows) {
			return access.AuditEventInput{}, access.ErrControlRevisionConflict
		} else if err != nil {
			return access.AuditEventInput{}, mapControlError("update grant", err)
		}
		result, err = readControlGrant(ctx, tx.db, input.InstanceID, input.ID)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if err := advanceControlRevision(ctx, tx.db, input.InstanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		return grantAudit(input, result, "grant.updated"), nil
	})
	return result, err
}

func (r *Repository) RevokeGrant(ctx context.Context, instanceID, grantID string, expectedRevision int64, actorID string) (access.ControlGrant, error) {
	if err := validateControlLookup(instanceID, grantID); err != nil || expectedRevision <= 0 {
		if err != nil {
			return access.ControlGrant{}, err
		}
		return access.ControlGrant{}, fmt.Errorf("%w: expected revision is required", access.ErrControlInvalidInput)
	}
	var result access.ControlGrant
	err := r.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		tx := repository.(*Repository)
		if err := lockControlInstance(ctx, tx.db, instanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		row := tx.db.QueryRow(ctx, `
			UPDATE access.control_grant
			SET revoked_at=clock_timestamp(),revision=revision+1,updated_at=clock_timestamp()
			WHERE instance_id=$1 AND id=$2 AND revision=$3 AND revoked_at IS NULL
			RETURNING id::text,resource_id,resource_kind`,
			instanceID, grantID, expectedRevision)
		var storedID, resourceID, resourceKind string
		if err := row.Scan(&storedID, &resourceID, &resourceKind); errors.Is(err, pgx.ErrNoRows) {
			return access.AuditEventInput{}, classifyGrantMutationMiss(ctx, tx.db, instanceID, grantID)
		} else if err != nil {
			return access.AuditEventInput{}, err
		}
		tag, err := tx.db.Exec(ctx, `
			UPDATE project.durable_resource_reference
			SET lifecycle_state='suspended',suspended_at=COALESCE(suspended_at,clock_timestamp()),reactivated_at=NULL,updated_at=clock_timestamp()
			WHERE instance_id=$1 AND reference_id=$2
			  AND owner_authored_id=$3 AND owner_kind='grant'
			  AND target_authored_id=$4 AND expected_kind=$5`,
			instanceID, controlGrantReferencePrefix+grantID, grantID, resourceID, resourceKind)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if tag.RowsAffected() != 1 {
			return access.AuditEventInput{}, access.ErrControlReferenceConflict
		}
		var readErr error
		result, readErr = readControlGrant(ctx, tx.db, instanceID, grantID)
		if readErr != nil {
			return access.AuditEventInput{}, readErr
		}
		if err := advanceControlRevision(ctx, tx.db, instanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		return access.AuditEventInput{PrincipalID: actorID, Action: "grant.deleted", ResourceKind: string(result.Resource.Kind()), ResourceID: result.Resource.ID().String(), Capability: result.Capability, Status: "success", MetadataJSON: `{}`}, nil
	})
	return result, err
}

func (r *Repository) ReactivateGrant(ctx context.Context, instanceID, grantID string, expectedRevision int64, project projectgraph.ProjectGraph, actorID string) (access.ControlGrant, error) {
	if err := validateControlLookup(instanceID, grantID); err != nil || expectedRevision <= 0 {
		if err != nil {
			return access.ControlGrant{}, err
		}
		return access.ControlGrant{}, access.ErrControlInvalidInput
	}
	var result access.ControlGrant
	err := r.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		tx := repository.(*Repository)
		if err := lockControlInstance(ctx, tx.db, instanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		current, err := readControlGrant(ctx, tx.db, instanceID, grantID)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if current.RevokedAt != "" {
			return access.AuditEventInput{}, access.ErrControlRevoked
		}
		if current.Revision != expectedRevision {
			return access.AuditEventInput{}, access.ErrControlRevisionConflict
		}
		input := access.ControlGrantInput{ID: current.ID, InstanceID: current.InstanceID, ProjectID: current.ProjectID, Subject: current.Subject, Resource: current.Resource, Capability: current.Capability, Name: current.Name}
		if _, err := input.ValidateAgainstGraph(project); err != nil {
			return access.AuditEventInput{}, err
		}
		lifecycle, err := resolveControlTarget(ctx, tx.db, instanceID, current.Resource)
		if err != nil || lifecycle != access.ControlReferenceActive {
			if err == nil {
				err = access.ErrControlTargetConflict
			}
			return access.AuditEventInput{}, err
		}
		tag, err := tx.db.Exec(ctx, `
			UPDATE project.durable_resource_reference
			SET lifecycle_state='active',suspended_at=NULL,reactivated_at=clock_timestamp(),updated_at=clock_timestamp()
			WHERE instance_id=$1 AND reference_id=$2 AND owner_authored_id=$3 AND owner_kind='grant'
			  AND target_authored_id=$4 AND expected_kind=$5 AND lifecycle_state='suspended'`,
			instanceID, controlGrantReferencePrefix+grantID, grantID, current.Resource.ID().String(), current.Resource.Kind())
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if tag.RowsAffected() != 1 {
			return access.AuditEventInput{}, access.ErrControlReferenceConflict
		}
		if err := advanceControlRevision(ctx, tx.db, instanceID); err != nil {
			return access.AuditEventInput{}, err
		}
		result, err = readControlGrant(ctx, tx.db, instanceID, grantID)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return access.AuditEventInput{PrincipalID: actorID, Action: "grant.reference.reactivated", ResourceKind: string(result.Resource.Kind()), ResourceID: result.Resource.ID().String(), Capability: result.Capability, Status: "success", MetadataJSON: `{}`}, nil
	})
	return result, err
}

func (r *Repository) ControlState(ctx context.Context, instanceID string) (access.ControlState, error) {
	if err := validateControlInstance(instanceID); err != nil {
		return access.ControlState{}, err
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return access.ControlState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockControlInstance(ctx, tx, instanceID); err != nil {
		return access.ControlState{}, err
	}
	state, err := loadControlState(ctx, tx, instanceID)
	if err != nil {
		return access.ControlState{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return access.ControlState{}, err
	}
	return state, nil
}

// ReadAuthorizationControlRevision reads only the live control-state
// identity and monotonic revision. It intentionally uses one ordinary row
// query: lifecycle/cache admission must not take the write/advisory lock used
// by ControlState's mutation-safe full projection.
func (r *Repository) ReadAuthorizationControlRevision(ctx context.Context, instanceID string) (access.AuthorizationControlRevision, error) {
	if err := validateControlInstance(instanceID); err != nil {
		return access.AuthorizationControlRevision{}, err
	}
	if r == nil || r.db == nil {
		return access.AuthorizationControlRevision{}, errors.New("access PostgreSQL database is unavailable")
	}
	var value access.AuthorizationControlRevision
	if err := r.db.QueryRow(ctx, `SELECT instance_id,project_id,revision FROM access.control_state WHERE instance_id=$1`, instanceID).
		Scan(&value.InstanceID, &value.ProjectID, &value.Revision); err != nil {
		return access.AuthorizationControlRevision{}, mapControlNotFound(err)
	}
	if value.InstanceID != instanceID {
		return access.AuthorizationControlRevision{}, fmt.Errorf("%w: control state instance identity does not match lookup", access.ErrControlIdentityConflict)
	}
	if err := value.Validate(); err != nil {
		return access.AuthorizationControlRevision{}, err
	}
	return value, nil
}

func loadControlState(ctx context.Context, db DBTX, instanceID string) (access.ControlState, error) {
	var state access.ControlState
	if err := db.QueryRow(ctx, `SELECT instance_id,project_id,revision FROM access.control_state WHERE instance_id=$1`, instanceID).Scan(&state.InstanceID, &state.ProjectID, &state.Revision); err != nil {
		return access.ControlState{}, mapControlNotFound(err)
	}
	rows, err := db.Query(ctx, `
		SELECT id::text,instance_id,project_id,subject_kind,subject_id::text,role,name,revision,created_at,updated_at,revoked_at
		FROM access.control_role_binding WHERE instance_id=$1 AND revoked_at IS NULL ORDER BY id`, instanceID)
	if err != nil {
		return access.ControlState{}, err
	}
	state.RoleAssignments, err = scanRoleAssignments(rows)
	rows.Close()
	if err != nil {
		return access.ControlState{}, err
	}
	grantRows, err := db.Query(ctx, controlGrantSelect+` WHERE g.instance_id=$1 AND g.revoked_at IS NULL ORDER BY g.id`, instanceID)
	if err != nil {
		return access.ControlState{}, err
	}
	state.Grants, err = scanControlGrants(grantRows)
	grantRows.Close()
	return state, err
}

const controlGrantSelect = `
	SELECT g.id::text,g.instance_id,g.project_id,g.subject_kind,g.subject_id::text,
	       g.resource_id,g.resource_kind,g.capability,g.name,g.revision,g.created_at,g.updated_at,g.revoked_at,
	       r.lifecycle_state,r.suspended_at,r.reactivated_at
	FROM access.control_grant g
	JOIN project.durable_resource_reference r
	  ON r.instance_id=g.instance_id AND r.reference_id=('grant:' || g.id::text)
	 AND r.owner_authored_id=g.id AND r.owner_kind='grant'
	 AND r.target_authored_id=g.resource_id AND r.expected_kind=g.resource_kind`

func readRoleAssignment(ctx context.Context, db DBTX, instanceID, assignmentID string) (access.RoleAssignment, error) {
	return scanRoleAssignment(db.QueryRow(ctx, `
		SELECT id::text,instance_id,project_id,subject_kind,subject_id::text,role,name,revision,created_at,updated_at,revoked_at
		FROM access.control_role_binding WHERE instance_id=$1 AND id=$2`, instanceID, assignmentID))
}

func scanRoleAssignment(row rowScanner) (access.RoleAssignment, error) {
	var result access.RoleAssignment
	var kind string
	var created, updated time.Time
	var revoked *time.Time
	if err := row.Scan(&result.ID, &result.InstanceID, &result.ProjectID, &kind, &result.Subject.ID, &result.Role, &result.Name, &result.Revision, &created, &updated, &revoked); err != nil {
		return access.RoleAssignment{}, mapControlNotFound(err)
	}
	result.Subject.Kind = access.SubjectKind(kind)
	result.CreatedAt, result.UpdatedAt, result.RevokedAt = formatTime(created), formatTime(updated), formatTimePtr(revoked)
	return result, nil
}

func scanRoleAssignments(rows pgx.Rows) ([]access.RoleAssignment, error) {
	result := make([]access.RoleAssignment, 0)
	for rows.Next() {
		value, err := scanRoleAssignment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func readControlGrant(ctx context.Context, db DBTX, instanceID, grantID string) (access.ControlGrant, error) {
	row := db.QueryRow(ctx, controlGrantSelect+` WHERE g.instance_id=$1 AND g.id=$2`, instanceID, grantID)
	return scanControlGrant(row)
}

func scanControlGrant(row rowScanner) (access.ControlGrant, error) {
	var value access.ControlGrant
	var subjectKind, resourceID, resourceKind, capability, lifecycle string
	var created, updated time.Time
	var revoked, suspended, reactivated *time.Time
	if err := row.Scan(&value.ID, &value.InstanceID, &value.ProjectID, &subjectKind, &value.Subject.ID,
		&resourceID, &resourceKind, &capability, &value.Name, &value.Revision, &created, &updated, &revoked,
		&lifecycle, &suspended, &reactivated); err != nil {
		return access.ControlGrant{}, mapControlNotFound(err)
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID(resourceID), projectgraph.Kind(resourceKind))
	if err != nil {
		return access.ControlGrant{}, err
	}
	value.Subject.Kind, value.Resource, value.Capability = access.SubjectKind(subjectKind), resource, access.Capability(capability)
	value.ReferenceLifecycle = access.ControlReferenceLifecycle(lifecycle)
	value.CreatedAt, value.UpdatedAt, value.RevokedAt = formatTime(created), formatTime(updated), formatTimePtr(revoked)
	value.ReferenceSuspendedAt, value.ReferenceReactivatedAt = formatTimePtr(suspended), formatTimePtr(reactivated)
	return value, nil
}

func scanControlGrants(rows pgx.Rows) ([]access.ControlGrant, error) {
	result := make([]access.ControlGrant, 0)
	for rows.Next() {
		value, err := scanControlGrant(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func ensureControlState(ctx context.Context, db DBTX, instanceID, projectID string) error {
	if _, err := db.Exec(ctx, `INSERT INTO access.control_state(instance_id,project_id) VALUES($1,$2) ON CONFLICT(instance_id) DO NOTHING`, instanceID, projectID); err != nil {
		return err
	}
	var stored string
	if err := db.QueryRow(ctx, `SELECT project_id FROM access.control_state WHERE instance_id=$1 FOR UPDATE`, instanceID).Scan(&stored); err != nil {
		return err
	}
	if stored != projectID {
		return access.ErrControlIdentityConflict
	}
	return nil
}

func advanceControlRevision(ctx context.Context, db DBTX, instanceID string) error {
	tag, err := db.Exec(ctx, `UPDATE access.control_state SET revision=revision+1,updated_at=clock_timestamp() WHERE instance_id=$1`, instanceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return access.ErrControlNotFound
	}
	return nil
}

func lockControlInstance(ctx context.Context, db DBTX, instanceID string) error {
	_, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, instanceID)
	return err
}

func validateStoredSubject(ctx context.Context, db DBTX, subject access.SubjectRef) error {
	var exists bool
	var err error
	switch subject.Kind {
	case access.SubjectKindPrincipal:
		err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.principal WHERE id=$1::uuid AND status='active' AND revoked_at IS NULL)`, subject.ID).Scan(&exists)
	case access.SubjectKindGroup:
		err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.access_group WHERE id=$1::uuid AND revoked_at IS NULL)`, subject.ID).Scan(&exists)
	default:
		return access.ErrControlInvalidInput
	}
	if err != nil {
		return err
	}
	if !exists {
		return access.ErrControlTargetNotFound
	}
	return nil
}

func resolveControlTarget(ctx context.Context, db DBTX, instanceID string, resource access.ResourceRef) (access.ControlReferenceLifecycle, error) {
	var kind, lifecycle string
	if err := db.QueryRow(ctx, `SELECT resource_kind,lifecycle_state FROM project.resource_identity WHERE instance_id=$1 AND authored_id=$2 FOR UPDATE`, instanceID, resource.ID().String()).Scan(&kind, &lifecycle); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", access.ErrControlTargetNotFound
		}
		return "", err
	}
	if projectgraph.Kind(kind) != resource.Kind() {
		return "", access.ErrControlTargetConflict
	}
	if lifecycle == "tombstoned" {
		return access.ControlReferenceSuspended, nil
	}
	if lifecycle != "active" {
		return "", access.ErrControlTargetConflict
	}
	return access.ControlReferenceActive, nil
}

func putControlReference(ctx context.Context, db DBTX, grant access.ControlGrant, lifecycle access.ControlReferenceLifecycle) error {
	referenceID := controlGrantReferencePrefix + grant.ID
	if _, err := db.Exec(ctx, `
		INSERT INTO project.durable_resource_reference
		(instance_id,reference_id,owner_authored_id,owner_kind,target_authored_id,expected_kind,lifecycle_state,suspended_at)
		VALUES($1,$2,$3,'grant',$4,$5,$6,CASE WHEN $6='suspended' THEN clock_timestamp() ELSE NULL END)
		ON CONFLICT(instance_id,reference_id) DO NOTHING`, grant.InstanceID, referenceID, grant.ID,
		grant.Resource.ID().String(), grant.Resource.Kind(), lifecycle); err != nil {
		return err
	}
	var ownerID, ownerKind, targetID, expectedKind string
	if err := db.QueryRow(ctx, `SELECT owner_authored_id,owner_kind,target_authored_id,expected_kind FROM project.durable_resource_reference WHERE instance_id=$1 AND reference_id=$2 FOR UPDATE`, grant.InstanceID, referenceID).Scan(&ownerID, &ownerKind, &targetID, &expectedKind); err != nil {
		return err
	}
	if ownerID != grant.ID || ownerKind != "grant" || targetID != grant.Resource.ID().String() || expectedKind != string(grant.Resource.Kind()) {
		return access.ErrControlReferenceConflict
	}
	return nil
}

func validateControlLookup(instanceID, objectID string) error {
	if err := validateControlInstance(instanceID); err != nil {
		return err
	}
	if _, err := projectgraph.NewResourceID(objectID); err != nil {
		return fmt.Errorf("%w: %v", access.ErrControlInvalidInput, err)
	}
	return nil
}

func validateControlInstance(instanceID string) error {
	if instanceID == "" || instanceID != strings.TrimSpace(instanceID) || len(instanceID) > 255 {
		return access.ErrControlInvalidInput
	}
	return nil
}

func mapControlNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrControlNotFound
	}
	return err
}

func mapControlError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", access.ErrControlConflict, operation)
	}
	return err
}

func classifyRoleMutationMiss(ctx context.Context, db DBTX, instanceID, assignmentID string) error {
	current, err := readRoleAssignment(ctx, db, instanceID, assignmentID)
	if err != nil {
		return err
	}
	if current.RevokedAt != "" {
		return access.ErrControlRevoked
	}
	return access.ErrControlRevisionConflict
}

func classifyGrantMutationMiss(ctx context.Context, db DBTX, instanceID, grantID string) error {
	current, err := readControlGrant(ctx, db, instanceID, grantID)
	if err != nil {
		return err
	}
	if current.RevokedAt != "" {
		return access.ErrControlRevoked
	}
	return access.ErrControlRevisionConflict
}

func roleAssignmentAudit(input access.RoleAssignmentInput, result access.RoleAssignment, action string) access.AuditEventInput {
	metadata, _ := json.Marshal(map[string]any{"projectId": result.ProjectID, "role": result.Role, "subjectId": result.Subject.ID, "subjectType": result.Subject.Kind})
	return access.AuditEventInput{PrincipalID: input.ActorID, Action: action, ResourceKind: "role_assignment", ResourceID: result.ID, Status: "success", RequestID: input.RequestID, CorrelationID: input.CorrelationID, MetadataJSON: string(metadata)}
}

func grantAudit(input access.ControlGrantInput, result access.ControlGrant, action string) access.AuditEventInput {
	metadata, _ := json.Marshal(map[string]any{"projectId": result.ProjectID, "subjectId": result.Subject.ID, "subjectType": result.Subject.Kind})
	return access.AuditEventInput{PrincipalID: input.ActorID, Action: action, ResourceKind: string(result.Resource.Kind()), ResourceID: result.Resource.ID().String(), Capability: result.Capability, Status: "success", RequestID: input.RequestID, CorrelationID: input.CorrelationID, MetadataJSON: string(metadata)}
}

func sameControlSeed(state access.ControlState, roles []access.RoleAssignmentInput, grants []access.ControlGrantInput) bool {
	if len(state.RoleAssignments) != len(roles) || len(state.Grants) != len(grants) {
		return false
	}
	roleByID := make(map[string]access.RoleAssignmentInput, len(roles))
	for _, role := range roles {
		if _, exists := roleByID[role.ID]; exists {
			return false
		}
		roleByID[role.ID] = role
	}
	for _, stored := range state.RoleAssignments {
		want, ok := roleByID[stored.ID]
		if !ok || stored.InstanceID != want.InstanceID || stored.ProjectID != want.ProjectID || stored.Subject != want.Subject || stored.Role != want.Role || stored.Name != want.Name || stored.RevokedAt != "" {
			return false
		}
	}
	grantByID := make(map[string]access.ControlGrantInput, len(grants))
	for _, grant := range grants {
		if _, exists := grantByID[grant.ID]; exists {
			return false
		}
		grantByID[grant.ID] = grant
	}
	for _, stored := range state.Grants {
		want, ok := grantByID[stored.ID]
		if !ok || stored.InstanceID != want.InstanceID || stored.ProjectID != want.ProjectID || stored.Subject != want.Subject || stored.Resource != want.Resource || stored.Capability != want.Capability || stored.Name != want.Name || stored.RevokedAt != "" {
			return false
		}
	}
	return true
}
