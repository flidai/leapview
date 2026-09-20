package postgres

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

const policySubjectID = "70000000-0000-0000-0000-000000000001"

func TestAuthorizationPolicyPostgreSQLCASIdempotencyAndHistoricalReads(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, policySubjectID); err != nil {
		t.Fatal(err)
	}
	repo := &Repository{db: db.runtime}
	scope := access.AuthorizationPolicyScope{TargetID: "target-policy", ProjectID: "project-policy", Environment: "production"}
	binding := access.RoleBinding{ID: "binding-policy", Name: "Policy viewer", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID}, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}
	first, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: "policy-create", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.Digest == "" || len(first.RoleBindings) != 1 {
		t.Fatalf("initial policy = %+v, want positive revision, digest, and one binding", first)
	}
	replay, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: "policy-create", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Revision != first.Revision || replay.Digest != first.Digest {
		t.Fatalf("idempotent replay = %+v, want exact revision %d/%s", replay, first.Revision, first.Digest)
	}
	changed := binding
	changed.Name = "Changed viewer"
	if _, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: changed, IdempotencyKey: "policy-create", ExpectedRevision: 0}); !errors.Is(err, access.ErrAuthorizationPolicyIdempotency) {
		t.Fatalf("conflicting idempotency error = %v, want ErrAuthorizationPolicyIdempotency", err)
	}
	second, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: changed, IdempotencyKey: "policy-update", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || second.Digest == first.Digest {
		t.Fatalf("changed policy = %+v, want revision 2 and a new digest", second)
	}
	historical, err := repo.AuthorizationPolicyRevision(ctx, scope, 1)
	if err != nil {
		t.Fatal(err)
	}
	if historical.Revision != 1 || historical.RoleBindings[0].Name != binding.Name {
		t.Fatalf("historical policy = %+v, want original binding", historical)
	}
	byDigest, err := repo.AuthorizationPolicyDigest(ctx, scope, first.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if byDigest.Revision != 1 {
		t.Fatalf("digest lookup revision = %d, want 1", byDigest.Revision)
	}
	tx, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := ValidateAuthorizationPolicyRevisionTx(ctx, tx, scope, second.Revision, second.Digest)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if sealed.Revision != second.Revision || sealed.Digest != second.Digest {
		_ = tx.Rollback(ctx)
		t.Fatalf("tx-bound policy = %+v, want revision/digest %d/%s", sealed, second.Revision, second.Digest)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateAuthorizationPolicyRevisionTx(ctx, tx, scope, first.Revision, first.Digest); !errors.Is(err, access.ErrAuthorizationPolicyStaleRevision) {
		_ = tx.Rollback(ctx)
		t.Fatalf("tx-bound stale policy error = %v, want ErrAuthorizationPolicyStaleRevision", err)
	}
	_ = tx.Rollback(ctx)
	if _, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: changed, IdempotencyKey: "policy-stale", ExpectedRevision: 1}); !errors.Is(err, access.ErrAuthorizationPolicyStaleRevision) {
		t.Fatalf("stale CAS error = %v, want ErrAuthorizationPolicyStaleRevision", err)
	}
	if _, err := db.runtime.Exec(ctx, `UPDATE access.authorization_policy_revision SET digest = $1 WHERE target_id = $2 AND project_id = $3 AND environment = $4 AND revision = 1`, first.Digest, scope.TargetID, scope.ProjectID, scope.Environment); err == nil {
		t.Fatal("runtime updated immutable policy revision evidence")
	}
	if _, err := db.runtime.Exec(ctx, `UPDATE access.authorization_policy_role_binding SET name = $1 WHERE target_id = $2 AND project_id = $3 AND environment = $4 AND revision = 1 AND id = $5`, "tampered", scope.TargetID, scope.ProjectID, scope.Environment, binding.ID); err == nil {
		t.Fatal("runtime updated immutable policy role binding evidence")
	}
	if _, err := db.runtime.Exec(ctx, `DELETE FROM access.authorization_policy_operation WHERE target_id = $1 AND project_id = $2 AND environment = $3 AND idempotency_key = $4`, scope.TargetID, scope.ProjectID, scope.Environment, "policy-create"); err == nil {
		t.Fatal("runtime deleted immutable policy operation evidence")
	}
	wrongScope := scope
	wrongScope.Environment = "staging"
	if _, err := repo.AuthorizationPolicy(ctx, wrongScope); !errors.Is(err, access.ErrAuthorizationPolicyNotFound) {
		t.Fatalf("wrong environment read error = %v, want ErrAuthorizationPolicyNotFound", err)
	}
	deleted, err := repo.DeleteAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{Scope: scope, BindingID: binding.ID, ExpectedRevision: second.Revision, IdempotencyKey: "policy-delete"})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Revision != 3 || len(deleted.RoleBindings) != 0 || deleted.Digest == second.Digest {
		t.Fatalf("deleted policy = %+v, want empty successor revision 3", deleted)
	}
	deleteReplay, err := repo.DeleteAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{Scope: scope, BindingID: binding.ID, ExpectedRevision: second.Revision, IdempotencyKey: "policy-delete"})
	if err != nil {
		t.Fatal(err)
	}
	if deleteReplay.Revision != deleted.Revision || deleteReplay.Digest != deleted.Digest || len(deleteReplay.RoleBindings) != 0 {
		t.Fatalf("delete idempotent replay = %+v, want exact empty successor", deleteReplay)
	}
	if _, err := repo.DeleteAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{Scope: scope, BindingID: "different-binding", ExpectedRevision: second.Revision, IdempotencyKey: "policy-delete"}); !errors.Is(err, access.ErrAuthorizationPolicyIdempotency) {
		t.Fatalf("conflicting delete idempotency error = %v, want ErrAuthorizationPolicyIdempotency", err)
	}
	if _, err := repo.DeleteAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{Scope: scope, BindingID: binding.ID, ExpectedRevision: second.Revision, IdempotencyKey: "policy-delete-conflict"}); !errors.Is(err, access.ErrAuthorizationPolicyStaleRevision) {
		t.Fatalf("delete stale CAS error = %v, want ErrAuthorizationPolicyStaleRevision", err)
	}
	if _, err := repo.DeleteAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{Scope: scope, BindingID: "missing-binding", ExpectedRevision: deleted.Revision, IdempotencyKey: "policy-delete-missing"}); !errors.Is(err, access.ErrAuthorizationPolicyNotFound) {
		t.Fatalf("delete missing binding error = %v, want ErrAuthorizationPolicyNotFound", err)
	}
	historicalAfterDelete, err := repo.AuthorizationPolicyRevision(ctx, scope, second.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(historicalAfterDelete.RoleBindings) != 1 || historicalAfterDelete.RoleBindings[0].Name != changed.Name {
		t.Fatalf("historical policy after delete = %+v, want changed binding preserved", historicalAfterDelete)
	}
	adminBinding := access.RoleBinding{ID: "binding-admin", Name: "Policy administrator", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID}, Role: access.ProjectRoleAdmin, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin)}
	adminPolicy, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: adminBinding, ExpectedRevision: deleted.Revision, IdempotencyKey: "policy-admin-create"})
	if err != nil {
		t.Fatal(err)
	}
	if adminPolicy.Revision != 4 || len(adminPolicy.RoleBindings) != 1 {
		t.Fatalf("administrator policy = %+v, want one binding at revision 4", adminPolicy)
	}
	if _, err := repo.DeleteAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{Scope: scope, BindingID: adminBinding.ID, ExpectedRevision: adminPolicy.Revision, IdempotencyKey: "policy-last-admin-delete"}); !errors.Is(err, access.ErrAuthorizationPolicyConflict) {
		t.Fatalf("last administrator deletion error = %v, want ErrAuthorizationPolicyConflict", err)
	}
	currentAfterAdminReject, err := repo.AuthorizationPolicy(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if currentAfterAdminReject.Revision != adminPolicy.Revision || len(currentAfterAdminReject.RoleBindings) != 1 {
		t.Fatalf("policy after rejected last-admin delete = %+v, want unchanged revision and binding", currentAfterAdminReject)
	}
}

func TestAuthorizationPolicyPostgreSQLConcurrentFirstWritesReplayExactly(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, policySubjectID); err != nil {
		t.Fatal(err)
	}
	repo := &Repository{db: db.runtime}
	scope := access.AuthorizationPolicyScope{TargetID: "target-policy-race", ProjectID: "project-policy-race", Environment: "production"}
	input := access.AuthorizationRoleBindingInput{
		Scope: scope,
		Binding: access.RoleBinding{
			ID: "binding-policy-race", Name: "Policy race viewer",
			Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID},
			Role:    access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer),
		},
		ExpectedRevision: 0, IdempotencyKey: "policy-race-create",
	}

	const workers = 8
	var start sync.WaitGroup
	start.Add(1)
	var workersDone sync.WaitGroup
	results := make([]access.AuthorizationPolicy, workers)
	errs := make([]error, workers)
	for index := 0; index < workers; index++ {
		workersDone.Add(1)
		go func(index int) {
			defer workersDone.Done()
			start.Wait()
			results[index], errs[index] = repo.UpsertAuthorizationRoleBinding(ctx, input)
		}(index)
	}
	start.Done()
	workersDone.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent first write %d: %v", index, err)
		}
	}
	for index := 1; index < workers; index++ {
		if !reflect.DeepEqual(results[index], results[0]) {
			t.Fatalf("concurrent replay %d = %+v, want exact first result %+v", index, results[index], results[0])
		}
	}
	if results[0].Revision != 1 || len(results[0].RoleBindings) != 1 {
		t.Fatalf("concurrent first policy = %+v, want one revision with one binding", results[0])
	}

	var revisions, bindingRows, operations int64
	if err := db.runtime.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM access.authorization_policy_revision WHERE target_id=$1 AND project_id=$2 AND environment=$3),
			(SELECT count(*) FROM access.authorization_policy_role_binding WHERE target_id=$1 AND project_id=$2 AND environment=$3),
			(SELECT count(*) FROM access.authorization_policy_operation WHERE target_id=$1 AND project_id=$2 AND environment=$3)`,
		scope.TargetID, scope.ProjectID, scope.Environment).Scan(&revisions, &bindingRows, &operations); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 || bindingRows != 1 || operations != 1 {
		t.Fatalf("concurrent first-write history = revisions %d, bindings %d, operations %d; want 1, 1, 1", revisions, bindingRows, operations)
	}
	var headRevision int64
	if err := db.runtime.QueryRow(ctx, `SELECT revision FROM access.authorization_policy WHERE target_id=$1 AND project_id=$2 AND environment=$3`, scope.TargetID, scope.ProjectID, scope.Environment).Scan(&headRevision); err != nil {
		t.Fatal(err)
	}
	if headRevision != 1 {
		t.Fatalf("concurrent first-write head revision = %d, want 1", headRevision)
	}
}

func TestAuthorizationPolicyPostgreSQLRoleBindingAuditsAreProjectScoped(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, policySubjectID); err != nil {
		t.Fatal(err)
	}
	repo := &Repository{db: db.runtime}
	scope := access.AuthorizationPolicyScope{TargetID: "target-audit-journey", ProjectID: "project:audit-journey", Environment: "production"}
	binding := access.RoleBinding{
		ID: "binding-audit-journey", Name: "Audit journey viewer",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID},
		Role:    access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer),
	}

	var created access.AuthorizationPolicy
	if err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		writer, ok := txRepo.(access.AuthorizationPolicyWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional policy writer is unavailable")
		}
		var err error
		created, err = writer.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{
			Scope: scope, Binding: binding, ExpectedRevision: 0, IdempotencyKey: "audit-journey-create",
		})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return access.AuditEventInput{
			ProjectID: scope.ProjectID, PrincipalID: policySubjectID,
			Action: "role_binding.created", ResourceKind: "role_binding", ResourceID: binding.ID,
			Capability: access.CapabilityProjectAdmin, Status: "success", MetadataJSON: `{}`,
		}, nil
	}); err != nil {
		t.Fatalf("create role binding with audit: %v", err)
	}

	if err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		writer, ok := txRepo.(access.AuthorizationPolicyWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional policy writer is unavailable")
		}
		if _, err := writer.DeleteAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{
			Scope: scope, BindingID: binding.ID, ExpectedRevision: created.Revision, IdempotencyKey: "audit-journey-delete",
		}); err != nil {
			return access.AuditEventInput{}, err
		}
		return access.AuditEventInput{
			ProjectID: scope.ProjectID, PrincipalID: policySubjectID,
			Action: "role_binding.deleted", ResourceKind: "role_binding", ResourceID: binding.ID,
			Capability: access.CapabilityProjectAdmin, Status: "success", MetadataJSON: `{}`,
		}, nil
	}); err != nil {
		t.Fatalf("delete role binding with audit: %v", err)
	}

	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{
		ProjectID: scope.ProjectID, ResourceKind: "role_binding", ResourceID: binding.ID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list project role-binding audits: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("project role-binding audits = %d, want exactly 2: %+v", len(events), events)
	}
	actions := make(map[string]int, len(events))
	for _, event := range events {
		if event.ProjectID != scope.ProjectID {
			t.Fatalf("project audit event = %+v, want project %q", event, scope.ProjectID)
		}
		actions[event.Action]++
	}
	if actions["role_binding.created"] != 1 || actions["role_binding.deleted"] != 1 {
		t.Fatalf("project role-binding audit actions = %#v, want one create and one delete", actions)
	}

	foreign, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{
		ProjectID: "project:other", ResourceKind: "role_binding", ResourceID: binding.ID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list foreign project role-binding audits: %v", err)
	}
	if len(foreign) != 0 {
		t.Fatalf("foreign project role-binding audits = %+v, want none", foreign)
	}
}

func TestAuthorizationPolicyPostgreSQLRejectsUnknownSubjectAndRole(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo := &Repository{db: db.runtime}
	scope := access.AuthorizationPolicyScope{TargetID: "target-invalid", ProjectID: "project-invalid", Environment: "production"}
	binding := access.RoleBinding{ID: "binding-invalid", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "70000000-0000-0000-0000-000000000099"}, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}
	if _, err := repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: " non-canonical ", ExpectedRevision: 0}); !errors.Is(err, access.ErrAuthorizationPolicyIdempotency) {
		t.Fatalf("non-canonical idempotency error = %v, want ErrAuthorizationPolicyIdempotency", err)
	}
	if _, err := repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: "invalid-subject", ExpectedRevision: 0}); err == nil {
		t.Fatal("accepted unknown principal subject")
	}
	binding.Subject.ID = "not-a-uuid"
	if _, err := repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: "invalid-id", ExpectedRevision: 0}); err == nil {
		t.Fatal("accepted malformed principal subject")
	}
}

func TestAuthorizationPolicyRepositoryRejectsConfiguredScopeMismatch(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	scope := access.AuthorizationPolicyScope{TargetID: "target-configured", ProjectID: "project-configured", Environment: "production"}
	repo, err := NewAuthorizationPolicyRepository(db.runtime, scope)
	if err != nil {
		t.Fatal(err)
	}
	wrong := scope
	wrong.Environment = "staging"
	if _, err := repo.AuthorizationPolicy(t.Context(), wrong); !errors.Is(err, access.ErrAuthorizationPolicyConflict) {
		t.Fatalf("configured scope mismatch error = %v, want ErrAuthorizationPolicyConflict", err)
	}
}

func TestAuthorizationPolicyAuditedMutationPreservesConfiguredScope(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	scope := access.AuthorizationPolicyScope{TargetID: "target-audited", ProjectID: "project-audited", Environment: "production"}
	repo, err := NewAuthorizationPolicyRepository(db.runtime, scope)
	if err != nil {
		t.Fatal(err)
	}
	wrongScope := scope
	wrongScope.Environment = "staging"
	binding := access.RoleBinding{
		ID:           "binding-audited",
		Subject:      access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID},
		Role:         access.ProjectRoleViewer,
		Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer),
	}
	err = repo.RunAuditedMutation(t.Context(), func(txRepo access.Repository) (access.AuditEventInput, error) {
		transactional, ok := txRepo.(*Repository)
		if !ok {
			return access.AuditEventInput{}, errors.New("audited repository lost PostgreSQL transaction type")
		}
		_, mutationErr := transactional.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{
			Scope: wrongScope, Binding: binding, ExpectedRevision: 0, IdempotencyKey: "audited-scope-check",
		})
		if !errors.Is(mutationErr, access.ErrAuthorizationPolicyConflict) {
			return access.AuditEventInput{}, mutationErr
		}
		return access.AuditEventInput{}, mutationErr
	})
	if !errors.Is(err, access.ErrAuthorizationPolicyConflict) {
		t.Fatalf("audited configured scope error = %v, want ErrAuthorizationPolicyConflict", err)
	}
}
