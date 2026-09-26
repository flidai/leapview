package migrations

import (
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestTypedAuthorizationPolicyGrantUpgradePreservesLegacyDigestPostgreSQL18(t *testing.T) {
	pool, _, provider := newDemoUpgradeDatabase(t)
	if _, err := provider.UpTo(t.Context(), 43); err != nil {
		t.Fatal(err)
	}
	const principalID = "4e4cc3b3-3350-4e5d-9e3b-6e4e0c2ef643"
	if _, err := pool.Exec(t.Context(), `INSERT INTO access.principal (id,principal_type,status) VALUES ($1::uuid,'user','active')`, principalID); err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "typed-policy-upgrade", ProjectID: "project-upgrade", Environment: "prod"}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}
	legacyResource, err := access.NewResourceRef("dashboard_sales", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	legacy := access.AuthorizationGrant{ID: "legacy-sales-read", Subject: subject, Resource: legacyResource, Capability: access.CapabilityResourceRead}
	digest, err := access.AuthorizationPolicyDigest(scope, nil, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO access.authorization_policy (target_id,project_id,environment,revision,digest) VALUES ($1,$2,$3,1,$4)`, scope.TargetID, scope.ProjectID, scope.Environment, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO access.authorization_policy_revision (target_id,project_id,environment,revision,digest) VALUES ($1,$2,$3,1,$4)`, scope.TargetID, scope.ProjectID, scope.Environment, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO access.authorization_policy_grant (target_id,project_id,environment,revision,id,subject_kind,subject_id,resource_kind,resource_id,capability) VALUES ($1,$2,$3,1,$4,'principal',$5,'dashboard','dashboard_sales','RESOURCE_READ')`, scope.TargetID, scope.ProjectID, scope.Environment, legacy.ID, principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	repo, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.AuthorizationPolicy(t.Context(), scope)
	if err != nil {
		t.Fatalf("migration changed or invalidated preexisting policy digest %s: %v", digest, err)
	}
	if loaded.Revision != 1 || loaded.Digest != digest || len(loaded.Grants) != 1 || !reflect.DeepEqual(loaded.Grants[0], legacy) {
		t.Fatalf("legacy grant changed across migration: %+v", loaded)
	}

	projectID, err := graph.NewResourceID(scope.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	typedResource, err := access.NewResourceRef("pipeline_refresh", graph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := access.NewExactPermissionPair(access.ActionPipelineRun, projectID, typedResource)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repo.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{
		Scope:            scope,
		Grant:            access.AuthorizationGrant{ID: "typed-refresh-run", Subject: subject, Resource: typedResource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{permission}},
		ExpectedRevision: 1,
		IdempotencyKey:   "typed-refresh-run",
	})
	if err != nil {
		t.Fatalf("migrated policy rejected a typed grant: %v", err)
	}
	if updated.Revision != 2 || len(updated.Grants) != 2 {
		t.Fatalf("typed grant did not advance policy: %+v", updated)
	}
	var foundTyped, foundLegacy bool
	for _, grant := range updated.Grants {
		switch grant.ID {
		case legacy.ID:
			foundLegacy = grant.Capability == legacy.Capability && grant.PermissionProfile == "" && grant.Permissions == nil
		case "typed-refresh-run":
			foundTyped = grant.Capability == "" && grant.PermissionProfile == access.PermissionCatalogProfile && len(grant.Permissions) == 1 && grant.Permissions[0].Key() == permission.Key()
		}
	}
	if !foundLegacy || !foundTyped {
		t.Fatalf("migrated policy lost one grant representation: %+v", updated.Grants)
	}
	historical, err := repo.AuthorizationPolicyRevision(t.Context(), scope, 1)
	if err != nil || historical.Digest != digest || len(historical.Grants) != 1 || !reflect.DeepEqual(historical.Grants[0], legacy) {
		t.Fatalf("migration or typed mutation rewrote legacy history: %+v, %v", historical, err)
	}
}
