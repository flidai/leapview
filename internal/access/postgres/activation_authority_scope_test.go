package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestCredentialActivationAuthorityRejectsScopeAndPermissionSubstitution(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	manage := mustActivationPair(t, 60, access.ActionConnectionManage)
	fixture := newActivationAuthorityFixture(t, db, 60, access.SubjectKindPrincipal, true, []access.PermissionPair{manage}, []access.PermissionPair{manage})
	issuer := access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}
	tests := []struct {
		name string
		edit func(*access.AuthorizationPolicyScope, *access.GrantIssuerEvidence, *[]access.PermissionPair)
	}{
		{"different project", func(scope *access.AuthorizationPolicyScope, _ *access.GrantIssuerEvidence, _ *[]access.PermissionPair) {
			scope.ProjectID = "another_project"
		}},
		{"different environment", func(scope *access.AuthorizationPolicyScope, _ *access.GrantIssuerEvidence, _ *[]access.PermissionPair) {
			scope.Environment = "another_environment"
		}},
		{"different target", func(scope *access.AuthorizationPolicyScope, _ *access.GrantIssuerEvidence, _ *[]access.PermissionPair) {
			scope.TargetID = "another_target"
		}},
		{"different connection", func(_ *access.AuthorizationPolicyScope, _ *access.GrantIssuerEvidence, pairs *[]access.PermissionPair) {
			for i := range *pairs {
				(*pairs)[i].Target.ResourceID = "another_connection"
			}
		}},
		{"permissions split across connections", func(_ *access.AuthorizationPolicyScope, _ *access.GrantIssuerEvidence, pairs *[]access.PermissionPair) {
			(*pairs)[1].Target.ResourceID = "another_connection"
		}},
		{"permissions for another project", func(_ *access.AuthorizationPolicyScope, _ *access.GrantIssuerEvidence, pairs *[]access.PermissionPair) {
			for i := range *pairs {
				(*pairs)[i].Target.ProjectID = "another_project"
			}
		}},
		{"duplicate manage permission", func(_ *access.AuthorizationPolicyScope, _ *access.GrantIssuerEvidence, pairs *[]access.PermissionPair) {
			(*pairs)[1] = (*pairs)[0]
		}},
		{"empty permission request", func(_ *access.AuthorizationPolicyScope, _ *access.GrantIssuerEvidence, pairs *[]access.PermissionPair) {
			*pairs = nil
		}},
		{"another actor's credential", func(_ *access.AuthorizationPolicyScope, actor *access.GrantIssuerEvidence, _ *[]access.PermissionPair) {
			actor.PrincipalID = activationUUID(9999)
		}},
		{"unsupported credential class", func(_ *access.AuthorizationPolicyScope, actor *access.GrantIssuerEvidence, _ *[]access.PermissionPair) {
			actor.Credential.Class = "workload"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope, actor := fixture.scope, issuer
			pairs := []access.PermissionPair{fixture.manage, fixture.use}
			test.edit(&scope, &actor, &pairs)
			tx, err := db.runtime.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if err := fixture.repo.AuthorizeCredentialActivationTx(t.Context(), tx, scope, fixture.snapshot, actor, pairs); err == nil {
				t.Fatal("substituted activation authority was accepted")
			}
		})
	}
}

func TestCredentialActivationAuthorityLeavesTransactionWithCaller(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	manage := mustActivationPair(t, 61, access.ActionConnectionManage)
	fixture := newActivationAuthorityFixture(t, db, 61, access.SubjectKindPrincipal, true, []access.PermissionPair{manage}, []access.PermissionPair{manage})
	issuer := access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}
	tx, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Permission order carries no authority; both exact pairs must be present.
	if err := fixture.repo.AuthorizeCredentialActivationTx(t.Context(), tx, fixture.scope, fixture.snapshot, issuer, []access.PermissionPair{fixture.use, fixture.manage}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.AuthorizeCredentialActivationTx(t.Context(), tx, fixture.scope, fixture.snapshot, issuer, []access.PermissionPair{fixture.manage}); err == nil {
		t.Fatal("incomplete authority was accepted")
	}
	var one int
	if err := tx.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("checker completed or invalidated caller transaction: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("caller could not commit its transaction: %v", err)
	}
}

func TestCredentialActivationAuthorityHonorsCurrentTokenCeiling(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	manage := mustActivationPair(t, 62, access.ActionConnectionManage)
	fixture := newActivationAuthorityFixture(t, db, 62, access.SubjectKindPrincipal, true, []access.PermissionPair{manage}, []access.PermissionPair{manage})
	issuer := access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.token}
	if err := authorizeActivation(t, fixture, issuer, fixture.snapshot, fixture.manage, fixture.use); err != nil {
		t.Fatal(err)
	}
	ceiling, err := access.EncodePermissionPairs([]access.PermissionPair{fixture.read, fixture.use})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(t.Context(), "UPDATE access.api_token SET permissions = $1::jsonb WHERE id = $2::uuid", ceiling, fixture.token.ID); err != nil {
		t.Fatal(err)
	}
	if err := authorizeActivation(t, fixture, issuer, fixture.snapshot, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantPermissionCeiling) {
		t.Fatalf("attenuated token authorized activation: %v", err)
	}
}
