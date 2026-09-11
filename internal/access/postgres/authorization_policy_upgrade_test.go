package postgres

import (
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestInitializeAuthorizationPolicyFromActiveSnapshot(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	scope := access.AuthorizationPolicyScope{TargetID: "target-upgrade", ProjectID: "project-upgrade", Environment: "production"}
	const generationID = "80000000-0000-0000-0000-000000000001"
	binding := access.RoleBinding{
		ID: "binding-upgrade", Name: "Existing owner",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID},
		Role:    access.ProjectRoleOwner, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleOwner),
	}
	repo := &Repository{db: db.runtime}
	policy, err := repo.InitializeAuthorizationPolicyFromServingPolicy(ctx, scope, generationID, []access.RoleBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := access.AuthorizationPolicyDigest(scope, []access.RoleBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Scope != scope || policy.Revision != 1 || policy.Digest != wantDigest || !reflect.DeepEqual(policy.RoleBindings, []access.RoleBinding{binding}) {
		t.Fatalf("upgraded policy = %+v, want exact active snapshot binding and digest %s", policy, wantDigest)
	}
	// Once established, startup is idempotent and does not require the source
	// generation to remain the caller's current active pointer.
	replayed, err := repo.InitializeAuthorizationPolicyFromServingPolicy(ctx, scope, "80000000-0000-0000-0000-000000000099", []access.RoleBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, policy) {
		t.Fatalf("upgrade replay = %+v, want %+v", replayed, policy)
	}
	var heads, revisions, bindings int
	var sourceGeneration string
	if err := db.runtime.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM access.authorization_policy WHERE target_id=$1 AND project_id=$2 AND environment=$3),
		(SELECT count(*) FROM access.authorization_policy_revision WHERE target_id=$1 AND project_id=$2 AND environment=$3),
		(SELECT count(*) FROM access.authorization_policy_role_binding WHERE target_id=$1 AND project_id=$2 AND environment=$3)`,
		scope.TargetID, scope.ProjectID, scope.Environment).Scan(&heads, &revisions, &bindings); err != nil {
		t.Fatal(err)
	}
	if heads != 1 || revisions != 1 || bindings != 1 {
		t.Fatalf("upgrade history = heads %d revisions %d bindings %d, want 1/1/1", heads, revisions, bindings)
	}
	if err := db.runtime.QueryRow(ctx, `SELECT source_generation_id FROM access.authorization_policy_revision WHERE target_id=$1 AND project_id=$2 AND environment=$3 AND revision=1`, scope.TargetID, scope.ProjectID, scope.Environment).Scan(&sourceGeneration); err != nil {
		t.Fatal(err)
	}
	if sourceGeneration != generationID {
		t.Fatalf("upgrade source generation = %q, want %q", sourceGeneration, generationID)
	}
}

func TestInitializeAuthorizationPolicyFailsClosedForMalformedServingEvidence(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo := &Repository{db: db.runtime}
	scope := access.AuthorizationPolicyScope{TargetID: "target-missing-upgrade", ProjectID: "project-missing-upgrade", Environment: "production"}
	invalid := access.RoleBinding{ID: "binding-invalid", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-invalid"}, Role: access.ProjectRoleOwner}
	if _, err := repo.InitializeAuthorizationPolicyFromServingPolicy(t.Context(), scope, "80000000-0000-0000-0000-000000000002", []access.RoleBinding{invalid}); err == nil {
		t.Fatal("accepted malformed active serving policy")
	}
	var heads int
	if err := db.runtime.QueryRow(t.Context(), `SELECT count(*) FROM access.authorization_policy WHERE target_id=$1`, scope.TargetID).Scan(&heads); err != nil {
		t.Fatal(err)
	}
	if heads != 0 {
		t.Fatalf("malformed serving evidence created %d policy heads", heads)
	}
}
