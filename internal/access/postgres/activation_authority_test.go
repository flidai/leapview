package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

type activationAuthorityFixture struct {
	db            auditDatabase
	repo          *Repository
	sequence      int
	scope         access.AuthorizationPolicyScope
	identity      graph.ServingIdentity
	project       graph.ProjectGraph
	resource      access.ResourceRef
	principal     access.SubjectRef
	group         access.SubjectRef
	policySubject access.SubjectRef
	manage        access.PermissionPair
	use           access.PermissionPair
	read          access.PermissionPair
	session       access.GrantCredentialEvidence
	token         access.GrantCredentialEvidence
	policy        access.AuthorizationPolicy
	snapshot      accesssnapshot.AuthorizationSnapshot
}

func newActivationAuthorityFixture(t *testing.T, db auditDatabase, sequence int, subjectKind access.SubjectKind, withEditorRole bool, snapshotGrant, policyGrant []access.PermissionPair) activationAuthorityFixture {
	t.Helper()
	ctx := t.Context()
	projectID := graph.ResourceID(fmt.Sprintf("activation_project_%d", sequence))
	resourceID := graph.ResourceID(fmt.Sprintf("activation_connection_%d", sequence))
	resource, err := access.NewResourceRef(resourceID, graph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	project, err := graph.NewProjectGraph([]graph.Resource{{ID: resourceID, Kind: graph.KindConnection, Name: "activation"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewExactPermissionPair(access.ActionConnectionManage, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	use, err := access.NewExactPermissionPair(access.ActionConnectionUse, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewExactPermissionPair(access.ActionConnectionRead, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	principalID := activationUUID(sequence*10 + 1)
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal(id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, principalID); err != nil {
		t.Fatal(err)
	}
	principal := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}
	group := access.SubjectRef{Kind: access.SubjectKindGroup, ID: activationUUID(sequence*10 + 2)}
	policySubject := principal
	if subjectKind == access.SubjectKindGroup {
		if _, err := db.admin.Exec(ctx, `INSERT INTO access.access_group(id, name, provider) VALUES ($1::uuid, $2, 'activation')`, group.ID, fmt.Sprintf("activation-%d", sequence)); err != nil {
			t.Fatal(err)
		}
		if err := (&Repository{db: db.runtime}).AddGroupMember(ctx, group.ID, principal.ID); err != nil {
			t.Fatal(err)
		}
		policySubject = group
	}
	identity := graph.ServingIdentity{ProjectID: projectID, Environment: "production", GenerationID: fmt.Sprintf("activation_generation_%d", sequence)}
	scope := access.AuthorizationPolicyScope{TargetID: fmt.Sprintf("activation_target_%d", sequence), ProjectID: projectID.String(), Environment: identity.Environment}
	var snapshotRoles []access.RoleBinding
	if withEditorRole {
		binding, bindingErr := access.NewTypedRoleBinding("activation-editor", "Activation editor", policySubject, access.PermissionRoleEditor, projectID)
		if bindingErr != nil {
			t.Fatal(bindingErr)
		}
		snapshotRoles = append(snapshotRoles, binding)
	}
	snapshotGrants := make([]accesssnapshot.Grant, 0, len(snapshotGrant))
	for index, pair := range snapshotGrant {
		snapshotGrants = append(snapshotGrants, accesssnapshot.Grant{ID: fmt.Sprintf("activation-grant-%d", index), Subject: policySubject, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}})
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, project, snapshotRoles, snapshotGrants, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallAuthorizationSnapshotTx(ctx, tx, snapshot); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("install activation snapshot: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	var policy access.AuthorizationPolicy
	bootstrapBindingID := ""
	if withEditorRole {
		binding := snapshotRoles[0]
		policy, err = repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: fmt.Sprintf("activation-role-%d", sequence)})
		if err != nil {
			t.Fatalf("install current activation role: %v", err)
		}
	} else if len(policyGrant) > 0 {
		// The grant writer expects an existing mutable policy head. Seed one
		// temporary typed role, install the durable grant rows, then remove the
		// temporary role so the current policy still contains only those grants.
		bootstrapBindingID = "activation-bootstrap"
		binding, bindingErr := access.NewTypedRoleBinding(bootstrapBindingID, "bootstrap", policySubject, access.PermissionRoleAuditor, projectID)
		if bindingErr != nil {
			t.Fatal(bindingErr)
		}
		policy, err = repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: fmt.Sprintf("activation-bootstrap-%d", sequence)})
		if err != nil {
			t.Fatalf("initialize current policy head: %v", err)
		}
	}
	for index, pair := range policyGrant {
		policy, err = repo.UpsertAuthorizationGrant(ctx, access.AuthorizationGrantInput{
			Scope:            scope,
			Grant:            access.AuthorizationGrant{ID: fmt.Sprintf("activation-grant-%d", index), Subject: policySubject, Resource: resource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}},
			ExpectedRevision: policy.Revision,
			IdempotencyKey:   fmt.Sprintf("activation-grant-%d-%d", sequence, index),
		})
		if err != nil {
			t.Fatalf("install current activation grant: %v", err)
		}
	}
	if bootstrapBindingID != "" {
		policy, err = repo.RemoveAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{Scope: scope, BindingID: bootstrapBindingID, ExpectedRevision: policy.Revision, IdempotencyKey: fmt.Sprintf("activation-bootstrap-remove-%d", sequence)})
		if err != nil {
			t.Fatalf("remove policy bootstrap role: %v", err)
		}
	}
	if !withEditorRole && len(policyGrant) == 0 {
		t.Fatal("fixture must install at least one current policy assignment")
	}
	fingerprint := activationFingerprint(sequence)
	sessionEvidence := access.GrantCredentialEvidence{Class: access.GrantCredentialClassSession, ID: activationUUID(sequence*10 + 3), Fingerprint: hex.EncodeToString(fingerprint)}
	tokenEvidence := access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: activationUUID(sequence*10 + 4), Fingerprint: hex.EncodeToString(fingerprint)}
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at, kind)
		VALUES ($1::uuid, $2::uuid, $3::bytea, $4::bytea, clock_timestamp() + interval '1 hour', 'browser')`, sessionEvidence.ID, principalID, fingerprint, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	pairs, err := access.EncodePermissionPairs([]access.PermissionPair{manage, use, read})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.api_token(id, principal_id, name, token_fingerprint, verifier, permission_profile, permissions, expires_at)
		VALUES ($1::uuid, $2::uuid, 'activation', $3::bytea, $4::bytea, $5, $6::jsonb, clock_timestamp() + interval '1 hour')`, tokenEvidence.ID, principalID, fingerprint, make([]byte, 32), access.PermissionCatalogProfile, pairs); err != nil {
		t.Fatal(err)
	}
	return activationAuthorityFixture{
		db: db, repo: repo, sequence: sequence, scope: scope, identity: identity, project: project, resource: resource,
		principal: principal, group: group, policySubject: policySubject, manage: manage, use: use, read: read,
		session: sessionEvidence, token: tokenEvidence, policy: policy, snapshot: snapshot,
	}
}

func activationUUID(value int) string { return fmt.Sprintf("71000000-0000-0000-0000-%012x", value) }

func activationFingerprint(value int) []byte {
	fingerprint := make([]byte, 32)
	for index := range fingerprint {
		fingerprint[index] = byte(value + index)
	}
	return fingerprint
}

func authorizeActivation(t *testing.T, fixture activationAuthorityFixture, issuer access.GrantIssuerEvidence, snapshot accesssnapshot.AuthorizationSnapshot, requested ...access.PermissionPair) error {
	t.Helper()
	tx, err := fixture.db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	return fixture.repo.AuthorizeCredentialActivationTx(t.Context(), tx, fixture.scope, snapshot, issuer, requested)
}

func TestCredentialActivationAuthorityPostgreSQL(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	fixture := newActivationAuthorityFixture(t, db, 1, access.SubjectKindPrincipal, true, []access.PermissionPair{mustActivationPair(t, 1, access.ActionConnectionManage)}, []access.PermissionPair{mustActivationPair(t, 1, access.ActionConnectionManage)})
	issuer := access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}
	if err := authorizeActivation(t, fixture, issuer, fixture.snapshot, fixture.manage, fixture.use); err != nil {
		t.Fatalf("browser session authorization failed: %v", err)
	}
	issuer.Credential = fixture.token
	if err := authorizeActivation(t, fixture, issuer, fixture.snapshot, fixture.manage, fixture.use); err != nil {
		t.Fatalf("API token authorization failed: %v", err)
	}

	t.Run("snapshot must match persisted digest and profile", func(t *testing.T) {
		other, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(fixture.identity, fixture.project, fixture.snapshot.RoleBindings(), []accesssnapshot.Grant{{ID: "different", Subject: fixture.principal, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{fixture.use}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}, other, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantAuthorityUnavailable) {
			t.Fatalf("mismatched snapshot error = %v, want unavailable authority", err)
		}
	})
	t.Run("requested actions must be both exact connection pairs", func(t *testing.T) {
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}, fixture.snapshot, fixture.manage); !errors.Is(err, access.ErrGrantAuthorityInvalid) {
			t.Fatalf("incomplete request error = %v, want invalid authority", err)
		}
	})
	t.Run("credential fingerprint must match", func(t *testing.T) {
		wrong := fixture.session
		wrong.Fingerprint = hex.EncodeToString(activationFingerprint(50))
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: wrong}, fixture.snapshot, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantCredentialInvalid) {
			t.Fatalf("wrong fingerprint error = %v, want invalid credential", err)
		}
	})
	t.Run("inactive principal is denied", func(t *testing.T) {
		if _, err := db.admin.Exec(t.Context(), `UPDATE access.principal SET status='disabled', disabled_at=clock_timestamp() WHERE id=$1::uuid`, fixture.principal.ID); err != nil {
			t.Fatal(err)
		}
		defer db.admin.Exec(context.Background(), `UPDATE access.principal SET status='active', disabled_at=NULL WHERE id=$1::uuid`, fixture.principal.ID)
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}, fixture.snapshot, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantPrincipalInactive) {
			t.Fatalf("inactive principal error = %v, want inactive principal", err)
		}
	})
	t.Run("expired browser session is denied", func(t *testing.T) {
		expiredID := activationUUID(1003)
		expiredFingerprint := activationFingerprint(100)
		if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, created_at, expires_at, kind)
			VALUES ($1::uuid, $2::uuid, $3::bytea, $4::bytea, clock_timestamp()-interval '2 seconds', clock_timestamp()-interval '1 second', 'browser')`, expiredID, fixture.principal.ID, expiredFingerprint, make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
		expired := access.GrantCredentialEvidence{Class: access.GrantCredentialClassSession, ID: expiredID, Fingerprint: hex.EncodeToString(expiredFingerprint)}
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: expired}, fixture.snapshot, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantCredentialInvalid) {
			t.Fatalf("expired session error = %v, want invalid credential", err)
		}
	})
	t.Run("desktop session is not browser activation evidence", func(t *testing.T) {
		desktopID := activationUUID(1005)
		desktopFingerprint := activationFingerprint(101)
		if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at, kind, instance_id, profile_id, client_id, absolute_expires_at)
			VALUES ($1::uuid, $2::uuid, $3::bytea, $4::bytea, clock_timestamp()+interval '1 hour', 'desktop', 'instance_activation', 'profile_activation', 'leapview-desktop', clock_timestamp()+interval '1 hour')`, desktopID, fixture.principal.ID, desktopFingerprint, make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
		desktop := access.GrantCredentialEvidence{Class: access.GrantCredentialClassSession, ID: desktopID, Fingerprint: hex.EncodeToString(desktopFingerprint)}
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: desktop}, fixture.snapshot, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantCredentialInvalid) {
			t.Fatalf("desktop session error = %v, want invalid credential", err)
		}
	})
	t.Run("revoked session is denied", func(t *testing.T) {
		if _, err := db.admin.Exec(t.Context(), `UPDATE access.session SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, fixture.session.ID); err != nil {
			t.Fatal(err)
		}
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}, fixture.snapshot, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantCredentialInvalid) {
			t.Fatalf("revoked session error = %v, want invalid credential", err)
		}
	})
	t.Run("expired API token is denied", func(t *testing.T) {
		expiredID := activationUUID(1006)
		expiredFingerprint := activationFingerprint(102)
		permissions, err := access.EncodePermissionPairs([]access.PermissionPair{fixture.manage, fixture.use, fixture.read})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.api_token(id, principal_id, name, token_fingerprint, verifier, permission_profile, permissions, created_at, expires_at)
			VALUES ($1::uuid, $2::uuid, 'expired activation', $3::bytea, $4::bytea, $5, $6::jsonb, clock_timestamp()-interval '2 seconds', clock_timestamp()-interval '1 second')`, expiredID, fixture.principal.ID, expiredFingerprint, make([]byte, 32), access.PermissionCatalogProfile, permissions); err != nil {
			t.Fatal(err)
		}
		expired := access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: expiredID, Fingerprint: hex.EncodeToString(expiredFingerprint)}
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: expired}, fixture.snapshot, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantCredentialInvalid) {
			t.Fatalf("expired token error = %v, want invalid credential", err)
		}
	})
	t.Run("revoked token is denied", func(t *testing.T) {
		if _, err := db.admin.Exec(t.Context(), `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, fixture.token.ID); err != nil {
			t.Fatal(err)
		}
		if err := authorizeActivation(t, fixture, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.token}, fixture.snapshot, fixture.manage, fixture.use); !errors.Is(err, access.ErrGrantCredentialInvalid) {
			t.Fatalf("revoked token error = %v, want invalid credential", err)
		}
	})
}

func mustActivationPair(t *testing.T, sequence int, action access.Action) access.PermissionPair {
	t.Helper()
	projectID := graph.ResourceID(fmt.Sprintf("activation_project_%d", sequence))
	resource, err := access.NewResourceRef(graph.ResourceID(fmt.Sprintf("activation_connection_%d", sequence)), graph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(action, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func TestCredentialActivationAuthorityUsesCurrentPolicyAndMembership(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	manage := mustActivationPair(t, 20, access.ActionConnectionManage)
	grantFixture := newActivationAuthorityFixture(t, db, 20, access.SubjectKindPrincipal, true, []access.PermissionPair{manage}, []access.PermissionPair{manage})
	issuer := access.GrantIssuerEvidence{PrincipalID: grantFixture.principal.ID, Credential: grantFixture.session}
	changed, err := grantFixture.repo.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{
		Scope:            grantFixture.scope,
		Grant:            access.AuthorizationGrant{ID: "activation-grant-0", Subject: grantFixture.policySubject, Resource: grantFixture.resource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{grantFixture.read}},
		ExpectedRevision: grantFixture.policy.Revision,
		IdempotencyKey:   "activation-replace-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	grantFixture.policy = changed
	if err := authorizeActivation(t, grantFixture, issuer, grantFixture.snapshot, grantFixture.manage, grantFixture.use); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("replaced grant still authorized activation: %v", err)
	}

	roleFixture := newActivationAuthorityFixture(t, db, 21, access.SubjectKindPrincipal, true, []access.PermissionPair{mustActivationPair(t, 21, access.ActionConnectionManage)}, []access.PermissionPair{mustActivationPair(t, 21, access.ActionConnectionManage)})
	roleFixture.policy, err = roleFixture.repo.RemoveAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingDeleteInput{Scope: roleFixture.scope, BindingID: "activation-editor", ExpectedRevision: roleFixture.policy.Revision, IdempotencyKey: "activation-remove-role"})
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizeActivation(t, roleFixture, access.GrantIssuerEvidence{PrincipalID: roleFixture.principal.ID, Credential: roleFixture.session}, roleFixture.snapshot, roleFixture.manage, roleFixture.use); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("removed role still authorized activation: %v", err)
	}

	additionFixture := newActivationAuthorityFixture(t, db, 22, access.SubjectKindPrincipal, false, []access.PermissionPair{mustActivationPair(t, 22, access.ActionConnectionManage), mustActivationPair(t, 22, access.ActionConnectionRead)}, []access.PermissionPair{mustActivationPair(t, 22, access.ActionConnectionManage), mustActivationPair(t, 22, access.ActionConnectionRead)})
	added, err := additionFixture.repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: additionFixture.scope, Binding: mustActivationEditor(t, additionFixture.principal, additionFixture.identity.ProjectID), ExpectedRevision: additionFixture.policy.Revision, IdempotencyKey: "activation-added-role"})
	if err != nil {
		t.Fatal(err)
	}
	additionFixture.policy = added
	if err := authorizeActivation(t, additionFixture, access.GrantIssuerEvidence{PrincipalID: additionFixture.principal.ID, Credential: additionFixture.session}, additionFixture.snapshot, additionFixture.manage, additionFixture.use); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("policy addition entered captured snapshot authority: %v", err)
	}

	groupFixture := newActivationAuthorityFixture(t, db, 23, access.SubjectKindGroup, false, []access.PermissionPair{mustActivationPair(t, 23, access.ActionConnectionManage), mustActivationPair(t, 23, access.ActionConnectionUse), mustActivationPair(t, 23, access.ActionConnectionRead)}, []access.PermissionPair{mustActivationPair(t, 23, access.ActionConnectionManage), mustActivationPair(t, 23, access.ActionConnectionUse), mustActivationPair(t, 23, access.ActionConnectionRead)})
	if err := authorizeActivation(t, groupFixture, access.GrantIssuerEvidence{PrincipalID: groupFixture.principal.ID, Credential: groupFixture.session}, groupFixture.snapshot, groupFixture.manage, groupFixture.use); err != nil {
		t.Fatalf("active group membership was not honored: %v", err)
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.principal_group SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND group_id=$2::uuid`, groupFixture.principal.ID, groupFixture.group.ID); err != nil {
		t.Fatal(err)
	}
	if err := authorizeActivation(t, groupFixture, access.GrantIssuerEvidence{PrincipalID: groupFixture.principal.ID, Credential: groupFixture.session}, groupFixture.snapshot, groupFixture.manage, groupFixture.use); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("removed group membership still authorized activation: %v", err)
	}

	revokedGroup := newActivationAuthorityFixture(t, db, 24, access.SubjectKindGroup, false, []access.PermissionPair{mustActivationPair(t, 24, access.ActionConnectionManage), mustActivationPair(t, 24, access.ActionConnectionUse), mustActivationPair(t, 24, access.ActionConnectionRead)}, []access.PermissionPair{mustActivationPair(t, 24, access.ActionConnectionManage), mustActivationPair(t, 24, access.ActionConnectionUse), mustActivationPair(t, 24, access.ActionConnectionRead)})
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.access_group SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, revokedGroup.group.ID); err != nil {
		t.Fatal(err)
	}
	if err := authorizeActivation(t, revokedGroup, access.GrantIssuerEvidence{PrincipalID: revokedGroup.principal.ID, Credential: revokedGroup.session}, revokedGroup.snapshot, revokedGroup.manage, revokedGroup.use); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("revoked group still authorized activation: %v", err)
	}

}

func mustActivationEditor(t *testing.T, subject access.SubjectRef, project graph.ResourceID) access.RoleBinding {
	t.Helper()
	binding, err := access.NewTypedRoleBinding("activation-editor", "Activation editor", subject, access.PermissionRoleEditor, project)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestCredentialActivationRequiresCallerOwnedReadCommittedTx(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	fixture := newActivationAuthorityFixture(t, db, 30, access.SubjectKindPrincipal, true, []access.PermissionPair{mustActivationPair(t, 30, access.ActionConnectionManage)}, []access.PermissionPair{mustActivationPair(t, 30, access.ActionConnectionManage)})
	tx, err := db.runtime.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	err = fixture.repo.AuthorizeCredentialActivationTx(t.Context(), tx, fixture.scope, fixture.snapshot, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.session}, []access.PermissionPair{fixture.manage, fixture.use})
	if !errors.Is(err, access.ErrGrantAuthorityInvalid) {
		t.Fatalf("serializable caller transaction error = %v, want invalid authority", err)
	}
}

func TestCredentialActivationChecksExpiryAfterCredentialLockWait(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	fixture := newActivationAuthorityFixture(t, db, 31, access.SubjectKindPrincipal, true, []access.PermissionPair{mustActivationPair(t, 31, access.ActionConnectionManage)}, []access.PermissionPair{mustActivationPair(t, 31, access.ActionConnectionManage)})
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.api_token SET expires_at=clock_timestamp()+interval '5 seconds' WHERE id=$1::uuid`, fixture.token.ID); err != nil {
		t.Fatal(err)
	}
	holder, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if err := holder.QueryRow(t.Context(), `SELECT id FROM access.api_token WHERE id=$1::uuid FOR UPDATE`, fixture.token.ID).Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	checkerTx, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer checkerTx.Rollback(context.Background())
	var checkerPID int
	if err := checkerTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&checkerPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- fixture.repo.AuthorizeCredentialActivationTx(ctx, checkerTx, fixture.scope, fixture.snapshot, access.GrantIssuerEvidence{PrincipalID: fixture.principal.ID, Credential: fixture.token}, []access.PermissionPair{fixture.manage, fixture.use})
	}()
	waitForCredentialActivationLock(t, db, checkerPID)
	waitForActivationTokenExpiry(t, db, fixture.token.ID)
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, access.ErrGrantCredentialInvalid) {
		t.Fatalf("credential that expired during row-lock wait returned %v, want invalid credential", err)
	}
}

func waitForActivationTokenExpiry(t *testing.T, db auditDatabase, tokenID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		var expired bool
		if err := db.admin.QueryRow(ctx, `SELECT expires_at <= clock_timestamp() FROM access.api_token WHERE id=$1::uuid`, tokenID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("API token %s did not expire before timeout: %v", tokenID, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitForCredentialActivationLock(t *testing.T, db auditDatabase, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := db.admin.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("activation backend %d did not reach a credential row wait: %v", pid, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
